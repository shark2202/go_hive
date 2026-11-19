package cli

// MCP configuration resolution for `hive serve`.
//
// Two sources feed the loader, in this order:
//
//  1. the MCP registry the dashboard and `hive mcp` write
//     (<HIVE_HOME>/mcp_registry/installed.json): enabled servers in "tools"
//     mode only — a "cli" server is exposed as a subcommand, not as tools;
//  2. an "mcp" block in <HIVE_HOME>/configuration.json, which lets an operator
//     pin servers (or switch the subsystem off) without the UI. A name declared
//     in both is taken from configuration.json, because that file is the
//     operator's explicit statement and the registry is UI state.

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/mcpregistry"
	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	// defaultMCPTimeout mirrors the client's built-in tools/call ceiling; it is
	// only used when configuration.json states no timeout_seconds.
	defaultMCPTimeout = 240 * time.Second
	// mcpStartupBudget bounds how long serve waits for the configured servers to
	// answer tools/list. A server that misses it is reported as unavailable and
	// serve starts without its tools.
	mcpStartupBudget = 15 * time.Second
)

// mcpFileSettings is the parsed "mcp" block of configuration.json.
type mcpFileSettings struct {
	Enabled   bool
	Namespace string
	Timeout   *time.Duration
	Servers   []tools.MCPServerSpec
}

// readMCPFileSettings reads the optional "mcp" block. A missing file, a missing
// block or a malformed block all mean "no file-level override": MCP stays
// enabled with the registry's servers and the client defaults, which is the
// behaviour of a home that never configured MCP at all.
func readMCPFileSettings(configPath string) mcpFileSettings {
	settings := mcpFileSettings{Enabled: true, Namespace: tools.MCPToolNamespace}
	if strings.TrimSpace(configPath) == "" {
		return settings
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return settings
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return settings
	}
	block, ok := root["mcp"]
	if !ok {
		return settings
	}
	var parsed struct {
		Enabled        *bool           `json:"enabled"`
		Namespace      string          `json:"namespace"`
		TimeoutSeconds *float64        `json:"timeout_seconds"`
		Servers        json.RawMessage `json:"servers"`
	}
	if json.Unmarshal(block, &parsed) != nil {
		return settings
	}
	if parsed.Enabled != nil {
		settings.Enabled = *parsed.Enabled
	}
	if namespace := strings.TrimSpace(parsed.Namespace); namespace != "" {
		settings.Namespace = namespace
	}
	if parsed.TimeoutSeconds != nil && *parsed.TimeoutSeconds > 0 {
		timeout := time.Duration(*parsed.TimeoutSeconds * float64(time.Second))
		settings.Timeout = &timeout
	}
	settings.Servers = parseMCPFileServers(parsed.Servers)
	return settings
}

// parseMCPFileServers accepts both shapes the config has carried historically:
// a list of server objects (each with its own "name") and an object mapping
// name -> server. The object form is what the legacy LoadMCPConfig supports too.
func parseMCPFileServers(raw json.RawMessage) []tools.MCPServerSpec {
	if len(raw) == 0 {
		return nil
	}
	var list []tools.MCPServerSpec
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var byName map[string]tools.MCPServerSpec
	if err := json.Unmarshal(raw, &byName); err != nil {
		return nil
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]tools.MCPServerSpec, 0, len(names))
	for _, name := range names {
		spec := byName[name]
		spec.Name = name
		out = append(out, spec)
	}
	return out
}

// registryMCPSpecs reads the servers the MCP registry installed and enabled for
// tool use. Registry IO failures are not fatal: serve starts with whatever else
// is configured (usually nothing) rather than refusing to boot.
func registryMCPSpecs(home hivehome.Home) []tools.MCPServerSpec {
	if strings.TrimSpace(home.Root) == "" {
		return nil
	}
	registry := mcpregistry.NewAtHome(home)
	if err := registry.Initialize(); err != nil {
		return nil
	}
	installed, err := registry.ListInstalled()
	if err != nil {
		return nil
	}
	specs := make([]tools.MCPServerSpec, 0, len(installed))
	for _, entry := range installed {
		name, _ := entry["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		if enabled, ok := entry["enabled"].(bool); ok && !enabled {
			continue
		}
		// "cli" mode means the server runs as a subcommand; only "tools"
		// servers contribute tool definitions to a session.
		if mode, _ := entry["mode"].(string); mode == "cli" {
			continue
		}
		config, err := registry.ServerConfig(name)
		if err != nil || config == nil {
			continue
		}
		specs = append(specs, specFromServerConfig(config))
	}
	return specs
}

func specFromServerConfig(config *mcpregistry.ServerConfig) tools.MCPServerSpec {
	deref := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	return tools.MCPServerSpec{
		Name:        config.Name,
		Transport:   config.Transport,
		Command:     deref(config.Command),
		Args:        append([]string(nil), config.Args...),
		Env:         copyStringMap(config.Env),
		Cwd:         deref(config.Cwd),
		URL:         deref(config.URL),
		Headers:     copyStringMap(config.Headers),
		SocketPath:  deref(config.SocketPath),
		Description: config.Description,
	}
}

// configuredMCPServers merges registry state with the configuration.json block.
// A configuration.json entry replaces the same-named registry entry, and
// "enabled": false there removes a registry server from the load set.
func configuredMCPServers(home hivehome.Home, settings mcpFileSettings) []tools.MCPServerSpec {
	byName := map[string]tools.MCPServerSpec{}
	for _, spec := range registryMCPSpecs(home) {
		byName[spec.Name] = spec
	}
	for _, spec := range settings.Servers {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		if spec.Enabled != nil && !*spec.Enabled {
			delete(byName, name)
			continue
		}
		// Enabled is the config switch, not part of the connect spec: the
		// loader must not re-filter a server it was handed to load.
		spec.Name = name
		spec.Enabled = nil
		byName[name] = spec
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]tools.MCPServerSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, byName[name])
	}
	return specs
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

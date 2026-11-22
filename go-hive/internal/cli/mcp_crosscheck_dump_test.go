package cli

// MCP cross-check evidence dump: the Go half of the Python-vs-Go double run.
//
// Gated: runs only when MCP_CROSSCHECK_OUT and MCP_CROSSCHECK_SERVERS are set,
// because it spawns the real stdio fixture server. go_hive/scripts/mcp_crosscheck.py
// sets both, runs this test, then diffs the output against the Python-side dump
// produced by go_hive/scripts/mcp_crosscheck_python.py over the same fixture.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// crosscheckCanonical renders a value the same way the Python probe's
// canonical() does (sorted keys, compact separators, no HTML escaping) so
// sha256 digests are comparable across the two implementations.
func crosscheckCanonical(value any) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func crosscheckSha256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func crosscheckSha256(value any) string {
	return crosscheckSha256Text(crosscheckCanonical(value))
}

type crosscheckToolRecord struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Sha256      string         `json:"parameters_sha256"`
}

func crosscheckRecordDefs(defs []llm.ToolDef, include func(string) bool) []crosscheckToolRecord {
	out := []crosscheckToolRecord{}
	for _, def := range defs {
		if !include(def.Name) {
			continue
		}
		out = append(out, crosscheckToolRecord{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.Parameters,
			Sha256:      crosscheckSha256(def.Parameters),
		})
	}
	return out
}

func crosscheckStatuses(statuses []tools.MCPServerStatus) []map[string]any {
	out := []map[string]any{}
	for _, status := range statuses {
		out = append(out, map[string]any{
			"name":       status.Name,
			"transport":  status.Transport,
			"connected":  status.Connected,
			"tool_count": status.Tools,
			"tool_names": status.ToolNames,
			"namespace":  status.Namespace,
			"error":      status.Error,
		})
	}
	return out
}

// mcpCrosscheckConfigJSON builds a configuration.json with the fixture entry,
// optionally disabled and with a 12s tools/call timeout (the file value the
// timeout-precedence probe compares against).
func mcpCrosscheckConfigJSON(t *testing.T, entry map[string]any, enabled *bool, includeTimeout bool) string {
	t.Helper()
	cloned := map[string]any{}
	for key, value := range entry {
		cloned[key] = value
	}
	if enabled != nil {
		cloned["enabled"] = *enabled
	}
	block := map[string]any{"enabled": true, "servers": []any{cloned}}
	if includeTimeout {
		block["timeout_seconds"] = 12
	}
	payload, err := json.Marshal(map[string]any{"mcp": block})
	if err != nil {
		t.Fatalf("marshal configuration: %v", err)
	}
	return string(payload)
}

func mcpCrosscheckRegistryInstalled(t *testing.T, command string, args []any, enabled bool) string {
	t.Helper()
	entry := map[string]any{
		"source":    "local",
		"transport": "stdio",
		"mode":      "tools",
		"enabled":   enabled,
		"manifest": map[string]any{
			"transport": map[string]any{"supported": []any{"stdio"}, "default": "stdio"},
			"stdio":     map[string]any{"command": command, "args": args},
		},
	}
	payload, err := json.Marshal(map[string]any{"servers": map[string]any{"mcp-crosscheck-echo": entry}})
	if err != nil {
		t.Fatalf("marshal installed.json: %v", err)
	}
	return string(payload)
}

// Parity-exempt: this is the Go half of a two-runtime evidence dump; its Python
// half is go_hive/scripts/mcp_crosscheck_python.py and the comparison happens in
// go_hive/scripts/mcp_crosscheck.py, so there is no upstream unit test to mirror.
func TestMCPCrosscheckDump(t *testing.T) {
	outPath := os.Getenv("MCP_CROSSCHECK_OUT")
	serversFile := os.Getenv("MCP_CROSSCHECK_SERVERS")
	if outPath == "" || serversFile == "" {
		t.Skip("set MCP_CROSSCHECK_OUT and MCP_CROSSCHECK_SERVERS to write the Go-side MCP cross-check dump")
	}

	raw, err := os.ReadFile(serversFile)
	if err != nil {
		t.Fatalf("read fixture servers: %v", err)
	}
	var fixture struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture servers: %v", err)
	}
	if len(fixture.Servers) == 0 {
		t.Fatal("fixture servers file carries no servers")
	}
	entry := fixture.Servers[0]
	serverName, _ := entry["name"].(string)
	if serverName == "" {
		t.Fatal("fixture entry has no name")
	}
	command, _ := entry["command"].(string)
	args, _ := entry["args"].([]any)
	entryCanonical := crosscheckCanonical(entry)

	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, mcpCrosscheckConfigJSON(t, entry, nil, true))

	runner := newMCPRuntime(home, io.Discard)
	if err := runner.Reload(); err != nil {
		t.Fatalf("initial reload: %v", err)
	}
	tools.SetProcessMCP(runner)
	t.Cleanup(func() {
		tools.SetProcessMCP(nil)
		runner.Close()
	})

	root := filepath.Join(home.Root, "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	buildCatalogue := func() *sessionToolCatalogue {
		return newSessionToolCatalogue(root, builtin.New(root), home)
	}
	namespacedTool := func(remote string) string {
		return tools.NamespacedMCPToolName(tools.MCPToolNamespace, serverName, remote)
	}

	initialStatuses := runner.Status()
	catalogue := buildCatalogue()
	defs := catalogue.Defs()
	registeredMCP := []string{}
	for _, def := range defs {
		if strings.HasPrefix(def.Name, tools.MCPToolNamespace+"__") {
			registeredMCP = append(registeredMCP, def.Name)
		}
	}
	sort.Strings(registeredMCP)

	include := func(name string) bool {
		return strings.HasPrefix(name, tools.MCPToolNamespace+"__") || name == "read_file"
	}
	toolRecords := crosscheckRecordDefs(defs, include)

	calls := map[string]any{}
	ctx := context.Background()
	if content, isError, err := catalogue.Execute(ctx, namespacedTool("echo_text"), map[string]any{
		"text": "Hello MCP", "count": 2, "mode": "upper",
	}); err != nil {
		t.Fatalf("echo_text execute: %v", err)
	} else {
		calls["echo_text"] = map[string]any{"content": content, "is_error": isError}
	}
	if content, isError, err := catalogue.Execute(ctx, namespacedTool("read_file"), map[string]any{
		"path": "/tmp/fixture.txt",
	}); err != nil {
		t.Fatalf("read_file execute: %v", err)
	} else {
		calls["read_file"] = map[string]any{"content": content, "is_error": isError}
	}

	barePresence := map[string]bool{}
	for _, name := range []string{"echo_text", "read_file"} {
		barePresence[name] = hasToolDef(defs, name)
	}

	// ── configuration.json enable/disable ────────────────────────────────
	disabled := false
	writeConfiguration(t, home, mcpCrosscheckConfigJSON(t, entry, &disabled, true))
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload after config disable: %v", err)
	}
	configDisabledCount := len(runner.Status())

	writeConfiguration(t, home, mcpCrosscheckConfigJSON(t, entry, nil, true))
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload after config re-enable: %v", err)
	}
	configReenabledCount := len(runner.Status())

	// ── registry installed.json enable/disable (the UI path) ─────────────
	registryDir := home.MCPRegistryDir()
	if err := os.MkdirAll(registryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfiguration(t, home, `{"mcp":{"enabled":true,"timeout_seconds":12}}`)
	installedPath := filepath.Join(registryDir, "installed.json")
	if err := os.WriteFile(installedPath, []byte(mcpCrosscheckRegistryInstalled(t, command, args, true)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload with registry server enabled: %v", err)
	}
	registryEnabledStatuses := runner.Status()

	if err := os.WriteFile(installedPath, []byte(mcpCrosscheckRegistryInstalled(t, command, args, false)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload with registry server disabled: %v", err)
	}
	registryDisabledCount := len(runner.Status())

	if err := os.WriteFile(installedPath, []byte(mcpCrosscheckRegistryInstalled(t, command, args, true)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload with registry server re-enabled: %v", err)
	}
	registryReenabledCount := len(runner.Status())

	// ── reload replaces the server set (rename drops the stale tools) ────
	// Clear the registry first: it still holds the original name, and a
	// reload must serve only the configuration.json set from here on.
	if err := os.WriteFile(installedPath, []byte(`{"servers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	renamed := map[string]any{}
	for key, value := range entry {
		renamed[key] = value
	}
	renamed["name"] = serverName + "-renamed"
	writeConfiguration(t, home, mcpCrosscheckConfigJSON(t, renamed, nil, true))
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload after rename: %v", err)
	}
	renamedCatalogue := buildCatalogue()
	renamedDefs := renamedCatalogue.Defs()
	stalePresent := hasToolDef(renamedDefs, namespacedTool("echo_text"))
	freshPresent := hasToolDef(renamedDefs, tools.NamespacedMCPToolName(tools.MCPToolNamespace, serverName+"-renamed", "echo_text"))

	// ── timeout precedence (env > configuration.json > 240s default) ─────
	writeConfiguration(t, home, mcpCrosscheckConfigJSON(t, entry, nil, true))
	if err := runner.Reload(); err != nil {
		t.Fatalf("reload before timeout probe: %v", err)
	}
	fileTimeout := tools.MCPCallResultTimeout().Seconds()

	previousEnv, hadEnv := os.LookupEnv("HIVE_MCP_CALL_RESULT_TIMEOUT")
	t.Cleanup(func() {
		if hadEnv {
			_ = os.Setenv("HIVE_MCP_CALL_RESULT_TIMEOUT", previousEnv)
		} else {
			_ = os.Unsetenv("HIVE_MCP_CALL_RESULT_TIMEOUT")
		}
	})
	if err := os.Setenv("HIVE_MCP_CALL_RESULT_TIMEOUT", "37"); err != nil {
		t.Fatal(err)
	}
	envTimeout := tools.MCPCallResultTimeout().Seconds()

	payload := map[string]any{
		"side":                    "go",
		"go_version":              runtime.Version(),
		"config_file":             serversFile,
		"config_sha256":           crosscheckSha256File(t, serversFile),
		"server_entry":            entry,
		"server_entry_sha256":     crosscheckSha256Text(entryCanonical),
		"server_name":             serverName,
		"namespace":               tools.MCPToolNamespace,
		"statuses_on_load":        crosscheckStatuses(initialStatuses),
		"registered_mcp_names":    registeredMCP,
		"namespace_prefix":        strings.TrimSuffix(namespacedTool("echo_text"), "echo_text"),
		"tools":                   toolRecords,
		"bare_registered_names":   barePresence,
		"calls":                   calls,
		"registry_enabled_status": crosscheckStatuses(registryEnabledStatuses),
		"lifecycle": map[string]any{
			"config_disabled_status_count":    configDisabledCount,
			"config_reenabled_status_count":   configReenabledCount,
			"registry_disabled_status_count":  registryDisabledCount,
			"registry_reenabled_status_count": registryReenabledCount,
			"renamed_stale_tools_present":     stalePresent,
			"renamed_new_tools_present":       freshPresent,
		},
		"timeout": map[string]any{
			"configuration_file_seconds": fileTimeout,
			"env_seconds":                envTimeout,
			"default_seconds":            240,
		},
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write dump: %v", err)
	}
	fmt.Printf("mcp crosscheck dump written: %s (%d MCP tools)\n", outPath, len(registeredMCP))
}

func crosscheckSha256File(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

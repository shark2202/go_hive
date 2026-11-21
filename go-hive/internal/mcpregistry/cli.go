package mcpregistry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (c *CLI) ensureIndexAvailable(registry *Registry) bool {
	cacheExists := fileExists(filepath.Join(registry.CacheDir(), "registry_index.json"))

	if c.isIndexStale(registry) {
		fmt.Fprintln(c.errOut(), "Updating registry index...")
		count, err := c.updateIndex(registry)
		if err == nil {
			fmt.Fprintf(c.errOut(), "Registry index updated (%d servers available).\n", count)
			return true
		}
		if cacheExists {
			fmt.Fprintf(c.errOut(), "Warning: failed to update registry index: %v\nUsing cached index.\n", err)
			return true
		}
		fmt.Fprintf(c.errOut(),
			"Error: no registry index available and refresh failed: %v\nCheck your network connection and try: hive mcp update\n", err)
		return false
	}

	return cacheExists
}

const securityNotice = "Registry servers run code on your machine. Only install servers you trust.\n" +
	"Learn more: https://github.com/aden-hive/hive-mcp-registry"

const noticeSentinelName = ".security_notice_shown"

func (c *CLI) printSecurityNoticeIfFirstUse(base string) {
	if fileExists(filepath.Join(base, noticeSentinelName)) {
		return
	}
	fmt.Fprintf(c.errOut(), "\n  %s\n\n", securityNotice)
}

func (c *CLI) markSecurityNoticeShown(base string) {
	_ = os.WriteFile(filepath.Join(base, noticeSentinelName), nil, 0o600)
}

func (c *CLI) promptForMissingCredentials(registry *Registry, name string, manifest map[string]any) {
	credentials := asSlice(manifest["credentials"])
	if len(credentials) == 0 {
		return
	}

	server, err := registry.GetServer(name)
	var existing map[string]any
	if err == nil && server != nil {
		existing = asMap(asMap(server["overrides"])["env"])
	}

	prompted := false
	for _, raw := range credentials {
		cred := asMap(raw)
		if cred == nil {
			continue
		}
		envVar := asString(cred["env_var"])
		if envVar == "" {
			continue
		}
		if !truthyValue(cred["required"]) {
			continue
		}

		if c.getenv(envVar) != "" {
			continue
		}
		if existing != nil && truthyValue(existing[envVar]) {
			continue
		}

		if !prompted {
			fmt.Fprintf(c.errOut(), "\n%s requires credentials:\n", name)
			prompted = true
		}

		description := stringOr(cred["description"], envVar)
		helpURL := stringOr(cred["help_url"], "")
		helpHint := ""
		if helpURL != "" {
			helpHint = " (get one at " + helpURL + ")"
		}

		value, err := c.inputFunc()(fmt.Sprintf("  %s%s\n  %s: ", description, helpHint, envVar))
		if err != nil {
			fmt.Fprintln(c.errOut(), "\nSkipped credential prompting.")
			return
		}
		value = strings.TrimSpace(value)
		if value != "" {
			_ = registry.SetOverride(name, "env", envVar, value)
		}
	}
}

func parseKeyValuePairs(values []string) (map[string]string, error) {
	result := map[string]string{}
	for _, item := range values {
		idx := strings.Index(item, "=")
		if idx < 0 {
			return nil, fmt.Errorf("Invalid format: '%s'. Expected KEY=VALUE.\nExample: --set JIRA_API_TOKEN=abc123", item)
		}
		key, value := item[:idx], item[idx+1:]
		if key == "" {
			return nil, fmt.Errorf("Invalid format: '%s'. Key cannot be empty.", item)
		}
		result[key] = value
	}
	return result, nil
}

func DefaultAgentCandidates() []string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return []string{
		filepath.Join(cwd, "exports"),
		filepath.Join(cwd, "core", "exports"),
		filepath.Join(cwd, "core", "framework", "agents"),
	}
}

func FindAgentsUsingServerIn(candidates []string, registry *Registry, name string) []string {
	matches := []string{}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(candidate)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			agentDir := filepath.Join(candidate, entry.Name())
			if !fileExists(filepath.Join(agentDir, "mcp_registry.json")) {
				continue
			}
			configs, _, err := registry.LoadAgentSelection(agentDir)
			if err != nil {
				continue
			}
			found := false
			for _, config := range configs {
				if asString(config["name"]) == name {
					found = true
					break
				}
			}
			if found {
				matches = append(matches, agentDir)
			}
		}
	}
	return matches
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func padRight(s string, width int) string {
	if n := runeLen(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

func healthSymbol(status string) string {
	switch status {
	case "healthy":
		return "✓"
	case "unhealthy":
		return "✗"
	default:
		return "●"
	}
}

func toolsCountString(manifest, entry map[string]any) string {
	if list := asSlice(manifest["tools"]); len(list) > 0 {
		return strconv.Itoa(len(list))
	}
	if last := entry["last_tool_count"]; truthyValue(last) {
		return pyStr(last)
	}
	return "0"
}

type InstallArgs struct {
	Name      string
	Version   string
	Transport string
}

func (c *CLI) Install(args InstallArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	c.printSecurityNoticeIfFirstUse(registry.Base())
	if !c.ensureIndexAvailable(registry) {
		return 1
	}

	if err := registry.Install(args.Name, InstallOptions{Transport: args.Transport, Version: args.Version}); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	c.markSecurityNoticeShown(registry.Base())

	entry, err := registry.GetServer(args.Name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	version, transport := "", ""
	if entry != nil {
		version = asString(entry["manifest_version"])
		transport = asString(entry["transport"])
	}
	fmt.Fprintf(c.out(), "✓ Installed %s v%s (%s)\n", args.Name, version, transport)

	manifest := map[string]any{}
	if entry != nil {
		manifest = asMap(entry["manifest"])
	}
	c.promptForMissingCredentials(registry, args.Name, manifest)

	fmt.Fprintln(c.out(), "\nNext steps:")
	fmt.Fprintf(c.out(), "  hive mcp health %s    Check that the server is reachable\n", args.Name)
	fmt.Fprintf(c.out(), "  hive mcp info %s      View server details\n", args.Name)
	return 0
}

type AddArgs struct {
	Name         string
	Transport    string
	URL          string
	Command      string
	Args         []string
	SocketPath   string
	Description  string
	Env          []string
	Cwd          string
	AsCLI        bool
	FromManifest string
	CommandLine  []string
}

func (c *CLI) Add(args AddArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	if args.FromManifest != "" {
		return c.addFromManifest(registry, args.FromManifest)
	}

	if args.Name == "" {
		fmt.Fprint(c.errOut(), "Error: --name is required.\n"+
			"Usage: hive mcp add --name my-server --transport http --url http://localhost:8080\n"+
			"   or: hive mcp add --from manifest.json\n")
		return 1
	}

	if args.Transport == "" {
		fmt.Fprintf(c.errOut(), "Error: --transport is required.\n"+
			"Supported transports: stdio, http, unix, sse\n"+
			"Example: hive mcp add --name %s --transport http --url http://localhost:8080\n", args.Name)
		return 1
	}

	command, commandArgs := args.Command, args.Args
	if len(args.CommandLine) > 0 {
		command = args.CommandLine[0]
		commandArgs = args.CommandLine[1:]
	}

	env, err := parseKeyValuePairs(args.Env)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	mode := "tools"
	if args.AsCLI {
		mode = "cli"
	}
	if err := registry.AddLocal(args.Name, AddLocalOptions{
		Transport:   args.Transport,
		Command:     command,
		Args:        commandArgs,
		Env:         env,
		Cwd:         args.Cwd,
		URL:         args.URL,
		SocketPath:  args.SocketPath,
		Description: args.Description,
		Mode:        mode,
	}); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	entry, err := registry.GetServer(args.Name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	transport := ""
	if entry != nil {
		transport = asString(entry["transport"])
	}
	reached := ""
	if args.AsCLI {
		reached = fmt.Sprintf("; agents run it with: hive mcp run %s --list", args.Name)
	}
	fmt.Fprintf(c.out(), "✓ Registered %s (%s%s)\n", args.Name, transport, reached)
	return 0
}

func (c *CLI) addFromManifest(registry *Registry, manifestPath string) int {
	if !fileExists(manifestPath) {
		fmt.Fprintf(c.errOut(), "Error: manifest file not found: %s\nCheck the path and try again.\n", manifestPath)
		return 1
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: invalid JSON in %s: %v\nValidate with: python -m json.tool %s\n",
			manifestPath, err, manifestPath)
		return 1
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var manifest map[string]any
	if err := decoder.Decode(&manifest); err != nil {
		fmt.Fprintf(c.errOut(), "Error: invalid JSON in %s: %v\nValidate with: python -m json.tool %s\n",
			manifestPath, err, manifestPath)
		return 1
	}

	name := asString(manifest["name"])
	if name == "" {
		fmt.Fprintf(c.errOut(), "Error: manifest missing 'name' field.\nAdd a 'name' field to %s.\n", manifestPath)
		return 1
	}

	if err := registry.AddLocal(name, AddLocalOptions{Manifest: manifest}); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	entry, err := registry.GetServer(name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	transport := ""
	if entry != nil {
		transport = asString(entry["transport"])
	}
	fmt.Fprintf(c.out(), "✓ Registered %s from %s (%s)\n", name, manifestPath, transport)
	return 0
}

type ModeArgs struct {
	Name string
	Mode string
}

func (c *CLI) Mode(args ModeArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if err := registry.SetMode(args.Name, args.Mode); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	how := "as tools"
	if args.Mode == "cli" {
		how = fmt.Sprintf("from the terminal: hive mcp run %s --list", args.Name)
	}
	fmt.Fprintf(c.out(), "✓ Agents reach %s %s (from their next session)\n", args.Name, how)
	return 0
}

type RunArgs struct {
	Name string
	Args []string
}

func (c *CLI) Run(args RunArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	config, err := registry.ServerConfig(args.Name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if config == nil {
		fmt.Fprintf(c.errOut(), "Error: no MCP server named '%s'.\nRun: hive mcp list  — to see installed servers.\n", args.Name)
		return 1
	}

	rest := args.Args
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	code, err := c.runServer(config, rest)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	return code
}

func (c *CLI) Remove(name string) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if err := registry.Remove(name); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.out(), "✓ Removed %s\n", name)
	return 0
}

func (c *CLI) Enable(name string) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if err := registry.Enable(name); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.out(), "✓ Enabled %s\n", name)
	return 0
}

func (c *CLI) Disable(name string) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if err := registry.Disable(name); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.out(), "✓ Disabled %s\n", name)
	return 0
}

type ListArgs struct {
	Available  bool
	OutputJSON bool
}

func (c *CLI) List(args ListArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	if args.Available {
		if !c.ensureIndexAvailable(registry) {
			return 1
		}
		entries, err := registry.ListAvailable()
		if err != nil {
			fmt.Fprintf(c.errOut(), "Error: %v\n", err)
			return 1
		}
		if args.OutputJSON {
			c.emitJSON(entries)
		} else {
			c.renderAvailableTable(entries)
		}
		return 0
	}

	entries, err := registry.ListInstalled()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if args.OutputJSON {
		safeEntries := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			safe := copyMap(entry)
			safe["overrides"] = maskOverrides(asMap(safe["overrides"]))
			safeEntries = append(safeEntries, safe)
		}
		c.emitJSON(safeEntries)
	} else {
		c.renderInstalledTable(entries)
	}
	return 0
}

type InfoArgs struct {
	Name       string
	OutputJSON bool
}

func (c *CLI) Info(args InfoArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	server, err := registry.GetServer(args.Name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if server == nil {
		fmt.Fprintf(c.errOut(), "Error: server '%s' is not installed.\n"+
			"Run 'hive mcp list' to see installed servers.\n"+
			"Run 'hive mcp install %s' to install from registry.\n", args.Name, args.Name)
		return 1
	}

	agents := c.findAgents(registry, args.Name)
	if len(agents) > 0 {
		server["used_by_agents"] = agents
	}

	if args.OutputJSON {
		safe := copyMap(server)
		safe["overrides"] = maskOverrides(asMap(safe["overrides"]))
		c.emitJSON(safe)
		return 0
	}

	manifest := asMap(server["manifest"])
	overrides := maskOverrides(asMap(server["overrides"]))
	toolsList := asSlice(manifest["tools"])
	status := "community"
	if _, ok := manifest["status"]; ok {
		status = pyStr(manifest["status"])
	}
	hiveBlock := asMap(manifest["hive"])

	fmt.Fprintln(c.out(), pyStr(server["name"]))
	fmt.Fprintln(c.out(), strings.Repeat("=", 50))

	fmt.Fprintf(c.out(), "  Source:     %s\n", pyGetString(server, "source", ""))
	fmt.Fprintf(c.out(), "  Transport:  %s\n", pyGetString(server, "transport", ""))
	fmt.Fprintf(c.out(), "  Version:    %s\n", pyGetString(server, "manifest_version", "unknown"))
	fmt.Fprintf(c.out(), "  Trust tier: %s\n", status)
	enabled := "no"
	if getTruthy(server, "enabled", true) {
		enabled = "yes"
	}
	fmt.Fprintf(c.out(), "  Enabled:    %s\n", enabled)

	if desc := asString(manifest["description"]); desc != "" {
		fmt.Fprintf(c.out(), "  Description: %s\n", desc)
	}

	if health := server["last_health_status"]; truthyValue(health) {
		fmt.Fprintf(c.out(), "  Health:     %s %s\n", healthSymbol(pyStr(health)), pyStr(health))
		if lastCheck := server["last_health_check_at"]; truthyValue(lastCheck) {
			fmt.Fprintf(c.out(), "  Last check: %s\n", pyStr(lastCheck))
		}
	}
	if lastError := server["last_error"]; truthyValue(lastError) {
		fmt.Fprintf(c.out(), "  Last error: %s\n", pyStr(lastError))
	}

	if len(toolsList) > 0 {
		fmt.Fprintf(c.out(), "\n  Tools (%d):\n", len(toolsList))
		for _, raw := range toolsList {
			if tool := asMap(raw); tool != nil {
				toolName := asString(tool["name"])
				toolDesc := asString(tool["description"])
				if toolDesc != "" {
					fmt.Fprintf(c.out(), "    • %s: %s\n", toolName, toolDesc)
				} else {
					fmt.Fprintf(c.out(), "    • %s\n", toolName)
				}
			} else {
				fmt.Fprintf(c.out(), "    • %s\n", pyStr(raw))
			}
		}
	}

	envOverrides := asMap(overrides["env"])
	headerOverrides := asMap(overrides["headers"])
	if len(envOverrides) > 0 || len(headerOverrides) > 0 {
		fmt.Fprintln(c.out(), "\n  Overrides:")
		for _, key := range sortedKeys(envOverrides) {
			fmt.Fprintf(c.out(), "    env.%s = <set>\n", key)
		}
		for _, key := range sortedKeys(headerOverrides) {
			fmt.Fprintf(c.out(), "    header.%s = <set>\n", key)
		}
	}

	if len(hiveBlock) > 0 {
		if profiles := asStringSlice(hiveBlock["profiles"]); len(profiles) > 0 {
			fmt.Fprintf(c.out(), "\n  Profiles: %s\n", strings.Join(profiles, ", "))
		}
		if minVer := hiveBlock["min_version"]; truthyValue(minVer) {
			fmt.Fprintf(c.out(), "  Min Hive version: %s\n", pyStr(minVer))
		}
	}

	if len(agents) > 0 {
		fmt.Fprintln(c.out(), "\n  Used by agents:")
		for _, agent := range agents {
			fmt.Fprintf(c.out(), "    • %s\n", agent)
		}
	}

	fmt.Fprintf(c.out(), "\n  Installed:  %s\n", pyGetString(server, "installed_at", "unknown"))
	fmt.Fprintf(c.out(), "  Installed by: %s\n", pyGetString(server, "installed_by", "unknown"))
	return 0
}

type ConfigArgs struct {
	Name      string
	SetEnv    []string
	SetHeader []string
}

func (c *CLI) Config(args ConfigArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	if len(args.SetEnv) == 0 && len(args.SetHeader) == 0 {
		server, err := registry.GetServer(args.Name)
		if err != nil {
			fmt.Fprintf(c.errOut(), "Error: %v\n", err)
			return 1
		}
		if server == nil {
			fmt.Fprintf(c.errOut(), "Error: server '%s' is not installed.\nRun 'hive mcp list' to see installed servers.\n", args.Name)
			return 1
		}
		masked := maskOverrides(asMap(server["overrides"]))
		envOverrides := asMap(masked["env"])
		headerOverrides := asMap(masked["headers"])
		if len(envOverrides) == 0 && len(headerOverrides) == 0 {
			fmt.Fprintf(c.out(), "No overrides set for %s.\n", args.Name)
			fmt.Fprintf(c.out(), "Set one with: hive mcp config %s --set KEY=VALUE\n", args.Name)
		} else {
			fmt.Fprintf(c.out(), "Overrides for %s:\n", args.Name)
			for _, key := range sortedKeys(envOverrides) {
				fmt.Fprintf(c.out(), "  env.%s = <set>\n", key)
			}
			for _, key := range sortedKeys(headerOverrides) {
				fmt.Fprintf(c.out(), "  header.%s = <set>\n", key)
			}
		}
		return 0
	}

	if len(args.SetEnv) > 0 {
		pairs, err := parseKeyValuePairs(args.SetEnv)
		if err != nil {
			fmt.Fprintf(c.errOut(), "Error: %v\n", err)
			return 1
		}
		for _, key := range sortedStringKeys(pairs) {
			if err := registry.SetOverride(args.Name, "env", key, pairs[key]); err != nil {
				fmt.Fprintf(c.errOut(), "Error: %v\n", err)
				return 1
			}
		}
		fmt.Fprintf(c.out(), "✓ Set %d env override(s) for %s\n", len(pairs), args.Name)
	}

	if len(args.SetHeader) > 0 {
		pairs, err := parseKeyValuePairs(args.SetHeader)
		if err != nil {
			fmt.Fprintf(c.errOut(), "Error: %v\n", err)
			return 1
		}
		for _, key := range sortedStringKeys(pairs) {
			if err := registry.SetOverride(args.Name, "headers", key, pairs[key]); err != nil {
				fmt.Fprintf(c.errOut(), "Error: %v\n", err)
				return 1
			}
		}
		fmt.Fprintf(c.out(), "✓ Set %d header override(s) for %s\n", len(pairs), args.Name)
	}

	return 0
}

type SearchArgs struct {
	Query      string
	OutputJSON bool
}

func (c *CLI) Search(args SearchArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if !c.ensureIndexAvailable(registry) {
		return 1
	}

	results, err := registry.Search(args.Query)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	if args.OutputJSON {
		c.emitJSON(results)
		return 0
	}

	if len(results) == 0 {
		fmt.Fprintf(c.out(), "No servers matching '%s'.\n", args.Query)
		return 0
	}

	fmt.Fprintf(c.out(), "Found %d server(s) matching '%s':\n\n", len(results), args.Query)
	c.renderAvailableTable(results)
	return 0
}

type HealthArgs struct {
	Name       string
	OutputJSON bool
}

func (c *CLI) Health(args HealthArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	results, err := c.healthCheck(registry, args.Name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	if args.OutputJSON {
		c.emitJSON(results)
		return 0
	}

	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		result := results[name]
		status := "unknown"
		if value, ok := result["status"]; ok {
			status = pyStr(value)
		}
		tools := result["tools"]
		errorValue := result["error"]
		fmt.Fprintf(c.out(), "  %s %s: %s", healthSymbol(status), name, status)
		if status == "healthy" && truthyValue(tools) {
			fmt.Fprintf(c.out(), " (%s tools)\n", pyStr(tools))
		} else if truthyValue(errorValue) {
			fmt.Fprintf(c.out(), "\n    Error: %s\n", pyStr(errorValue))
		} else {
			fmt.Fprintln(c.out())
		}
	}

	return 0
}

func (c *CLI) Init() int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	added := registry.EnsureDefaults()
	if len(added) > 0 {
		for _, name := range added {
			fmt.Fprintf(c.out(), "✓ Registered %s\n", name)
		}
	} else {
		fmt.Fprintln(c.out(), "✓ MCP registry already initialized (no changes)")
	}
	return 0
}

type UpdateArgs struct {
	Name string
}

func (c *CLI) Update(args UpdateArgs) int {
	registry, err := c.getRegistry()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}

	if args.Name != "" {
		return c.updateServer(args.Name, registry)
	}

	count, err := c.updateIndex(registry)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: failed to update registry index: %v\nCheck your network connection and try again.\n", err)
		return 1
	}

	fmt.Fprintf(c.out(), "✓ Registry index updated (%d servers available)\n", count)

	installed, err := registry.ListInstalled()
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	registryServers := make([]map[string]any, 0, len(installed))
	for _, server := range installed {
		if asString(server["source"]) == "registry" && !truthyValue(server["pinned"]) {
			registryServers = append(registryServers, server)
		}
	}

	if len(registryServers) == 0 {
		return 0
	}

	fmt.Fprintf(c.out(), "\nUpdating %d installed server(s)...\n", len(registryServers))
	failures := 0
	for _, server := range registryServers {
		if code := c.updateServer(asString(server["name"]), registry); code != 0 {
			failures++
		}
	}

	if failures > 0 {
		return 1
	}
	return 0
}

func (c *CLI) updateServer(name string, registry *Registry) int {
	server, err := registry.GetServer(name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if server == nil {
		fmt.Fprintf(c.errOut(), "Error: server '%s' is not installed.\nRun: hive mcp install %s to install it.\n", name, name)
		return 1
	}

	if asString(server["source"]) != "registry" {
		fmt.Fprintf(c.errOut(), "Error: '%s' is a local server and cannot be updated from the registry.\n"+
			"Use 'hive mcp remove %s' and 'hive mcp add' to re-register it.\n", name, name)
		return 1
	}

	if truthyValue(server["pinned"]) {
		fmt.Fprintf(c.errOut(), "Error: '%s' is pinned to v%s.\n"+
			"To update a pinned server, remove and reinstall:\n"+
			"  hive mcp remove %s && hive mcp install %s\n",
			name, pyGetString(server, "manifest_version", "?"), name, name)
		return 1
	}

	if !c.ensureIndexAvailable(registry) {
		return 1
	}

	oldVersion := pyGetString(server, "manifest_version", "unknown")
	transport := asString(server["transport"])
	overrides := asMap(server["overrides"])
	wasEnabled := getTruthy(server, "enabled", true)

	savedEntry := copyMap(server)
	delete(savedEntry, "name")

	if err := registry.Remove(name); err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	if err := registry.Install(name, InstallOptions{Transport: transport}); err != nil {
		data, readErr := registry.ReadInstalled()
		if readErr == nil {
			serversOf(data)[name] = savedEntry
			_ = registry.WriteInstalled(data)
		}
		fmt.Fprintf(c.errOut(), "Error: %v\nServer '%s' has been restored to its previous state.\n", err, name)
		return 1
	}

	newEntry, err := registry.GetServer(name)
	if err != nil {
		fmt.Fprintf(c.errOut(), "Error: %v\n", err)
		return 1
	}
	newVersion := "unknown"
	if newEntry != nil {
		newVersion = pyGetString(newEntry, "manifest_version", "unknown")
	}

	for _, key := range sortedKeys(asMap(overrides["env"])) {
		_ = registry.SetOverride(name, "env", key, asString(asMap(overrides["env"])[key]))
	}
	for _, key := range sortedKeys(asMap(overrides["headers"])) {
		_ = registry.SetOverride(name, "headers", key, asString(asMap(overrides["headers"])[key]))
	}
	if !wasEnabled {
		_ = registry.Disable(name)
	}

	if oldVersion == newVersion {
		fmt.Fprintf(c.out(), "✓ %s is already at v%s\n", name, newVersion)
	} else {
		fmt.Fprintf(c.out(), "✓ Updated %s: v%s → v%s\n", name, oldVersion, newVersion)
	}
	return 0
}

type MCPCommand struct {
	Kind string

	Install InstallArgs
	Add     AddArgs
	Mode    ModeArgs
	Run     RunArgs
	List    ListArgs
	Info    InfoArgs
	Config  ConfigArgs
	Search  SearchArgs
	Health  HealthArgs
	Update  UpdateArgs

	Name string
}

var mcpSubcommands = map[string]bool{
	"install": true, "add": true, "mode": true, "run": true, "remove": true,
	"enable": true, "disable": true, "list": true, "info": true, "config": true,
	"search": true, "health": true, "init": true, "update": true,
}

func MCPHelpText() string {
	return strings.Join([]string{
		"usage: hive mcp <command> [options]",
		"",
		"Manage MCP servers",
		"",
		"commands:",
		"  install <name>            Install a server from the registry",
		"  add                       Register a local/running MCP server",
		"  mode <name> cli|tools     Reach a server from the terminal or as tools",
		"  run <name> [args...]      Run a server's tools as commands (mcp2cli)",
		"  remove <name>             Remove an installed server",
		"  enable <name>             Enable a disabled server",
		"  disable <name>            Disable a server without removing it",
		"  list                      List servers",
		"  info <name>               Show server details",
		"  config <name>             Set server configuration overrides",
		"  search <query>            Search the registry",
		"  health [name]             Check server health",
		"  init                      Initialize the local MCP registry",
		"  update [name]             Update installed servers or refresh the registry index",
		"",
		"options:",
		"  -h, --help                show this help message and exit",
	}, "\n")
}

func parseMCPError(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func splitFlag(token string) (string, string, bool) {
	if idx := strings.Index(token, "="); idx >= 0 && strings.HasPrefix(token, "--") {
		return token[:idx], token[idx+1:], true
	}
	return token, "", false
}

func takeValue(rest []string, index int, inline string, hasInline bool, flag string) (string, int, error) {
	if hasInline {
		return inline, index + 1, nil
	}
	if index+1 >= len(rest) {
		return "", index, parseMCPError("argument %s: expected one argument", flag)
	}
	return rest[index+1], index + 2, nil
}

func takeMany(rest []string, index int, inline string, hasInline bool) ([]string, int) {
	values := []string{}
	if hasInline {
		values = append(values, inline)
	}
	index++
	for index < len(rest) && !strings.HasPrefix(rest[index], "-") {
		values = append(values, rest[index])
		index++
	}
	return values, index
}

func ParseMCPArgs(argv []string) (*MCPCommand, error) {
	if len(argv) > 0 && argv[0] == "hive" {
		argv = argv[1:]
	}
	if len(argv) > 0 && argv[0] == "mcp" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		// Upstream runs on argparse with a required subcommand, so a bare
		// `hive mcp` is a usage error (exit 2) with the usage on stderr — it
		// prints no group help. `hive mcp --help` / `hive mcp help` stay 0.
		return &MCPCommand{Kind: "missing-command"}, nil
	}
	if argv[0] == "-h" || argv[0] == "--help" || argv[0] == "help" {
		return &MCPCommand{Kind: "help"}, nil
	}

	command := argv[0]
	if !mcpSubcommands[command] {
		return nil, parseMCPError("invalid choice: '%s' (choose from 'install', 'add', 'mode', 'run', 'remove', 'enable', 'disable', 'list', 'info', 'config', 'search', 'health', 'init', 'update')", command)
	}
	rest := argv[1:]

	switch command {
	case "install":
		return parseInstall(rest)
	case "add":
		return parseAdd(rest)
	case "mode":
		return parseMode(rest)
	case "run":
		return parseRun(rest)
	case "remove", "enable", "disable":
		return parseNameOnly(command, rest)
	case "list":
		return parseList(rest)
	case "info":
		return parseInfo(rest)
	case "config":
		return parseConfig(rest)
	case "search":
		return parseSearch(rest)
	case "health":
		return parseHealth(rest)
	case "init":
		if len(rest) > 0 && isHelp(rest[0]) {
			return &MCPCommand{Kind: "help"}, nil
		}
		if len(rest) > 0 {
			return nil, parseMCPError("unrecognized arguments: %s", strings.Join(rest, " "))
		}
		return &MCPCommand{Kind: "init"}, nil
	case "update":
		return parseUpdate(rest)
	}
	return nil, parseMCPError("invalid choice: '%s'", command)
}

func isHelp(token string) bool { return token == "-h" || token == "--help" }

func parseInstall(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "install"}
	for index := 0; index < len(rest); {
		token := rest[index]
		if isHelp(token) {
			return &MCPCommand{Kind: "help"}, nil
		}
		flag, inline, hasInline := splitFlag(token)
		switch {
		case flag == "--version":
			value, next, err := takeValue(rest, index, inline, hasInline, "--version")
			if err != nil {
				return nil, err
			}
			cmd.Install.Version = value
			index = next
		case flag == "--transport":
			value, next, err := takeValue(rest, index, inline, hasInline, "--transport")
			if err != nil {
				return nil, err
			}
			cmd.Install.Transport = value
			index = next
		case strings.HasPrefix(token, "-"):
			return nil, parseMCPError("unrecognized arguments: %s", token)
		default:
			if cmd.Install.Name != "" {
				return nil, parseMCPError("unrecognized arguments: %s", token)
			}
			cmd.Install.Name = token
			index++
		}
	}
	if cmd.Install.Name == "" {
		return nil, parseMCPError("the following arguments are required: name")
	}
	return cmd, nil
}

func parseAdd(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "add"}
	positional := []string{}
	endOfOptions := false

	for index := 0; index < len(rest); {
		token := rest[index]
		if endOfOptions {
			positional = append(positional, token)
			index++
			continue
		}
		if token == "--" {
			endOfOptions = true
			index++
			continue
		}
		if isHelp(token) {
			return &MCPCommand{Kind: "help"}, nil
		}
		if !strings.HasPrefix(token, "-") {
			positional = append(positional, token)
			index++
			continue
		}

		flag, inline, hasInline := splitFlag(token)
		switch flag {
		case "--name":
			value, next, err := takeValue(rest, index, inline, hasInline, "--name")
			if err != nil {
				return nil, err
			}
			cmd.Add.Name = value
			index = next
		case "--transport":
			value, next, err := takeValue(rest, index, inline, hasInline, "--transport")
			if err != nil {
				return nil, err
			}
			if value != "stdio" && value != "http" && value != "unix" && value != "sse" {
				return nil, parseMCPError("argument --transport: invalid choice: '%s' (choose from 'stdio', 'http', 'unix', 'sse')", value)
			}
			cmd.Add.Transport = value
			index = next
		case "--url":
			value, next, err := takeValue(rest, index, inline, hasInline, "--url")
			if err != nil {
				return nil, err
			}
			cmd.Add.URL = value
			index = next
		case "--command":
			value, next, err := takeValue(rest, index, inline, hasInline, "--command")
			if err != nil {
				return nil, err
			}
			cmd.Add.Command = value
			index = next
		case "--args":
			values, next := takeMany(rest, index, inline, hasInline)
			cmd.Add.Args = values
			index = next
		case "--socket-path":
			value, next, err := takeValue(rest, index, inline, hasInline, "--socket-path")
			if err != nil {
				return nil, err
			}
			cmd.Add.SocketPath = value
			index = next
		case "--description":
			value, next, err := takeValue(rest, index, inline, hasInline, "--description")
			if err != nil {
				return nil, err
			}
			cmd.Add.Description = value
			index = next
		case "--env":
			values, next := takeMany(rest, index, inline, hasInline)
			if len(values) == 0 {
				return nil, parseMCPError("argument --env: expected at least one argument")
			}
			cmd.Add.Env = values
			index = next
		case "--cwd":
			value, next, err := takeValue(rest, index, inline, hasInline, "--cwd")
			if err != nil {
				return nil, err
			}
			cmd.Add.Cwd = value
			index = next
		case "--as-cli":
			cmd.Add.AsCLI = true
			index++
		case "--from":
			value, next, err := takeValue(rest, index, inline, hasInline, "--from")
			if err != nil {
				return nil, err
			}
			cmd.Add.FromManifest = value
			index = next
		default:
			return nil, parseMCPError("unrecognized arguments: %s", token)
		}
	}

	cmd.Add.CommandLine = positional
	return cmd, nil
}

func parseMode(rest []string) (*MCPCommand, error) {
	positional := []string{}
	for _, token := range rest {
		if isHelp(token) {
			return &MCPCommand{Kind: "help"}, nil
		}
		if strings.HasPrefix(token, "-") {
			return nil, parseMCPError("unrecognized arguments: %s", token)
		}
		positional = append(positional, token)
	}
	if len(positional) < 2 {
		return nil, parseMCPError("the following arguments are required: name, mode")
	}
	if positional[1] != "cli" && positional[1] != "tools" {
		return nil, parseMCPError("argument mode: invalid choice: '%s' (choose from 'cli', 'tools')", positional[1])
	}
	if len(positional) > 2 {
		return nil, parseMCPError("unrecognized arguments: %s", strings.Join(positional[2:], " "))
	}
	return &MCPCommand{Kind: "mode", Mode: ModeArgs{Name: positional[0], Mode: positional[1]}}, nil
}

func parseRun(rest []string) (*MCPCommand, error) {
	if len(rest) == 0 {
		return nil, parseMCPError("the following arguments are required: name")
	}
	if isHelp(rest[0]) {
		return &MCPCommand{Kind: "help"}, nil
	}
	return &MCPCommand{Kind: "run", Run: RunArgs{Name: rest[0], Args: append([]string{}, rest[1:]...)}}, nil
}

func parseNameOnly(kind string, rest []string) (*MCPCommand, error) {
	positional := []string{}
	for _, token := range rest {
		if isHelp(token) {
			return &MCPCommand{Kind: "help"}, nil
		}
		if strings.HasPrefix(token, "-") {
			return nil, parseMCPError("unrecognized arguments: %s", token)
		}
		positional = append(positional, token)
	}
	if len(positional) == 0 {
		return nil, parseMCPError("the following arguments are required: name")
	}
	if len(positional) > 1 {
		return nil, parseMCPError("unrecognized arguments: %s", strings.Join(positional[1:], " "))
	}
	return &MCPCommand{Kind: kind, Name: positional[0]}, nil
}

func parseList(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "list"}
	for _, token := range rest {
		switch token {
		case "-h", "--help":
			return &MCPCommand{Kind: "help"}, nil
		case "--available":
			cmd.List.Available = true
		case "--json":
			cmd.List.OutputJSON = true
		default:
			return nil, parseMCPError("unrecognized arguments: %s", token)
		}
	}
	return cmd, nil
}

func parseInfo(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "info"}
	for _, token := range rest {
		switch token {
		case "-h", "--help":
			return &MCPCommand{Kind: "help"}, nil
		case "--json":
			cmd.Info.OutputJSON = true
		default:
			if strings.HasPrefix(token, "-") || cmd.Info.Name != "" {
				return nil, parseMCPError("unrecognized arguments: %s", token)
			}
			cmd.Info.Name = token
		}
	}
	if cmd.Info.Name == "" {
		return nil, parseMCPError("the following arguments are required: name")
	}
	return cmd, nil
}

func parseConfig(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "config"}
	for index := 0; index < len(rest); {
		token := rest[index]
		if isHelp(token) {
			return &MCPCommand{Kind: "help"}, nil
		}
		flag, inline, hasInline := splitFlag(token)
		switch flag {
		case "--set":
			values, next := takeMany(rest, index, inline, hasInline)
			if len(values) == 0 {
				return nil, parseMCPError("argument --set: expected at least one argument")
			}
			cmd.Config.SetEnv = values
			index = next
		case "--set-header":
			values, next := takeMany(rest, index, inline, hasInline)
			if len(values) == 0 {
				return nil, parseMCPError("argument --set-header: expected at least one argument")
			}
			cmd.Config.SetHeader = values
			index = next
		default:
			if strings.HasPrefix(token, "-") || cmd.Config.Name != "" {
				return nil, parseMCPError("unrecognized arguments: %s", token)
			}
			cmd.Config.Name = token
			index++
		}
	}
	if cmd.Config.Name == "" {
		return nil, parseMCPError("the following arguments are required: name")
	}
	return cmd, nil
}

func parseSearch(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "search"}
	for _, token := range rest {
		switch token {
		case "-h", "--help":
			return &MCPCommand{Kind: "help"}, nil
		case "--json":
			cmd.Search.OutputJSON = true
		default:
			if strings.HasPrefix(token, "-") || cmd.Search.Query != "" {
				return nil, parseMCPError("unrecognized arguments: %s", token)
			}
			cmd.Search.Query = token
		}
	}
	if cmd.Search.Query == "" {
		return nil, parseMCPError("the following arguments are required: query")
	}
	return cmd, nil
}

func parseHealth(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "health"}
	for _, token := range rest {
		switch token {
		case "-h", "--help":
			return &MCPCommand{Kind: "help"}, nil
		case "--json":
			cmd.Health.OutputJSON = true
		default:
			if strings.HasPrefix(token, "-") || cmd.Health.Name != "" {
				return nil, parseMCPError("unrecognized arguments: %s", token)
			}
			cmd.Health.Name = token
		}
	}
	return cmd, nil
}

func parseUpdate(rest []string) (*MCPCommand, error) {
	cmd := &MCPCommand{Kind: "update"}
	for _, token := range rest {
		if isHelp(token) {
			return &MCPCommand{Kind: "help"}, nil
		}
		if strings.HasPrefix(token, "-") || cmd.Update.Name != "" {
			return nil, parseMCPError("unrecognized arguments: %s", token)
		}
		cmd.Update.Name = token
	}
	return cmd, nil
}

func (c *CLI) Main(argv []string) int {
	command, err := ParseMCPArgs(argv)
	if err != nil {
		fmt.Fprintf(c.errOut(), "%v\n", err)
		return 2
	}

	switch command.Kind {
	case "help":
		fmt.Fprintln(c.out(), MCPHelpText())
		return 0
	case "missing-command":
		fmt.Fprintln(c.errOut(), "usage: hive mcp [-h] {install,add,mode,run,remove,enable,disable,list,info,config,search,health,init,update} ...")
		fmt.Fprintln(c.errOut(), "hive mcp: error: the following arguments are required: command")
		return 2
	case "install":
		return c.Install(command.Install)
	case "add":
		return c.Add(command.Add)
	case "mode":
		return c.Mode(command.Mode)
	case "run":
		return c.Run(command.Run)
	case "remove":
		return c.Remove(command.Name)
	case "enable":
		return c.Enable(command.Name)
	case "disable":
		return c.Disable(command.Name)
	case "list":
		return c.List(command.List)
	case "info":
		return c.Info(command.Info)
	case "config":
		return c.Config(command.Config)
	case "search":
		return c.Search(command.Search)
	case "health":
		return c.Health(command.Health)
	case "init":
		return c.Init()
	case "update":
		return c.Update(command.Update)
	}
	return 2
}

type CLI struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader

	Input            func(prompt string) (string, error)
	RegistryFunc     func() (*Registry, error)
	IsIndexStaleFunc func(*Registry) bool
	UpdateIndexFunc  func(*Registry) (int, error)
	GetenvFunc       func(string) string
	FindAgentsFunc   func(*Registry, string) []string
	HealthCheckFunc  func(*Registry, string) (map[string]map[string]any, error)
	RunServerFunc    func(*ServerConfig, []string) (int, error)

	reader *bufio.Reader
}

func (c *CLI) out() io.Writer {
	if c.Out != nil {
		return c.Out
	}
	return os.Stdout
}

func (c *CLI) errOut() io.Writer {
	if c.Err != nil {
		return c.Err
	}
	return os.Stderr
}

func (c *CLI) in() io.Reader {
	if c.In != nil {
		return c.In
	}
	return os.Stdin
}

func (c *CLI) inputFunc() func(string) (string, error) {
	if c.Input != nil {
		return c.Input
	}
	return func(prompt string) (string, error) {
		if _, err := io.WriteString(c.out(), prompt); err != nil {
			return "", err
		}
		if c.reader == nil {
			c.reader = bufio.NewReader(c.in())
		}
		line, err := c.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && line != "" {
				return strings.TrimRight(line, "\n"), nil
			}
			return line, err
		}
		return strings.TrimSuffix(line, "\n"), nil
	}
}

func (c *CLI) getRegistry() (*Registry, error) {
	if c.RegistryFunc != nil {
		return c.RegistryFunc()
	}
	registry, err := Default()
	if err != nil {
		return nil, err
	}
	if err := registry.Initialize(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (c *CLI) isIndexStale(registry *Registry) bool {
	if c.IsIndexStaleFunc != nil {
		return c.IsIndexStaleFunc(registry)
	}
	return registry.IsIndexStale()
}

func (c *CLI) updateIndex(registry *Registry) (int, error) {
	if c.UpdateIndexFunc != nil {
		return c.UpdateIndexFunc(registry)
	}
	return registry.UpdateIndex()
}

func (c *CLI) getenv(name string) string {
	if c.GetenvFunc != nil {
		return c.GetenvFunc(name)
	}
	return os.Getenv(name)
}

func (c *CLI) findAgents(registry *Registry, name string) []string {
	if c.FindAgentsFunc != nil {
		return c.FindAgentsFunc(registry, name)
	}
	return FindAgentsUsingServerIn(DefaultAgentCandidates(), registry, name)
}

func (c *CLI) healthCheck(registry *Registry, name string) (map[string]map[string]any, error) {
	if c.HealthCheckFunc != nil {
		return c.HealthCheckFunc(registry, name)
	}
	return DefaultHealthCheck(registry, name)
}

func DefaultHealthCheck(registry *Registry, name string) (map[string]map[string]any, error) {
	if name == "" {
		return registry.HealthCheckAll()
	}
	result, err := registry.HealthCheck(name)
	if err != nil {
		return nil, err
	}
	return map[string]map[string]any{name: result}, nil
}

func (c *CLI) runServer(config *ServerConfig, args []string) (int, error) {
	if c.RunServerFunc != nil {
		return c.RunServerFunc(config, args)
	}
	return RunMCP2CLI(config, args)
}

func pyGetString(m map[string]any, key, def string) string {
	value, ok := m[key]
	if !ok {
		return def
	}
	return pyStr(value)
}

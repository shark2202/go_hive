package mcpregistry

import (
	"reflect"
	"strings"
	"testing"
)

func addCLIServer(t *testing.T, registry *Registry, name, mode string) {
	t.Helper()
	if err := registry.AddLocal(name, AddLocalOptions{
		Transport:   "stdio",
		Command:     "srv",
		Args:        []string{name},
		Description: name + " things",
		Mode:        mode,
	}); err != nil {
		t.Fatalf("AddLocal(%s): %v", name, err)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_a_cli_mode_server_is_never_loaded_as_tools
func TestCLIModeServerNeverLoadedAsTools(t *testing.T) {
	registry := newDefaultRegistry(t)
	addCLIServer(t, registry, "plain", "")
	addCLIServer(t, registry, "comfy", "cli")

	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"plain", "comfy"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	got := make([]string, 0, len(configs))
	for _, config := range configs {
		got = append(got, config.Name)
	}
	if !reflect.DeepEqual(got, []string{"plain"}) {
		t.Fatalf("resolved = %v, want [plain]", got)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_cli_servers_lists_the_enabled_servers_in_cli_mode
func TestCLIServersListsEnabledCLIModeServers(t *testing.T) {
	registry := newDefaultRegistry(t)
	addCLIServer(t, registry, "plain", "")
	addCLIServer(t, registry, "comfy", "cli")
	addCLIServer(t, registry, "paused", "cli")
	if err := registry.Disable("paused"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := registry.SetMode("plain", "cli"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}

	servers, err := registry.CLIServers()
	if err != nil {
		t.Fatalf("CLIServers: %v", err)
	}
	want := []map[string]any{
		{"name": "comfy", "description": "comfy things", "tool_count": nil},
		{"name": "plain", "description": "plain things", "tool_count": nil},
	}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("CLIServers = %v, want %v", servers, want)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_setting_the_mode_of_an_unknown_server_is_refused
func TestSetModeUnknownServerRefused(t *testing.T) {
	registry := newDefaultRegistry(t)
	err := registry.SetMode("ghost", "cli")
	requireErrContains(t, err, "Cannot set the mode of server 'ghost'")
}

// Parity: core/tests/test_mcp_cli.py::test_a_stdio_server_becomes_one_command_line_mcp2cli_splits_back
func TestStdioServerBecomesOneCommandLine(t *testing.T) {
	command := []string{"/usr/bin/python3", "-m", "my server", "--from", "comfy-mcp", "comfy-cli>=1.14.0"}
	config := &ServerConfig{
		Name:      "s",
		Transport: "stdio",
		Command:   strPtr(command[0]),
		Args:      command[1:],
		Env:       map[string]string{"TOKEN": "t0p"},
		Cwd:       strPtr("/srv"),
	}

	argv, env, cwd, err := MCP2CLIInvocation(config)
	if err != nil {
		t.Fatalf("MCP2CLIInvocation: %v", err)
	}
	if len(argv) < 2 || argv[0] != "--mcp-stdio" {
		t.Fatalf("argv = %v", argv)
	}
	splitted, err := ShellSplit(argv[1])
	if err != nil {
		t.Fatalf("ShellSplit: %v", err)
	}
	if !reflect.DeepEqual(splitted, command) {
		t.Fatalf("split = %v, want %v", splitted, command)
	}
	if !reflect.DeepEqual(env, map[string]string{"TOKEN": "t0p"}) {
		t.Fatalf("env = %v", env)
	}
	if cwd == nil || *cwd != "/srv" {
		t.Fatalf("cwd = %v", cwd)
	}
	if strings.Contains(strings.Join(argv, " "), "t0p") {
		t.Fatalf("secret leaked into argv: %v", argv)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_headers_reach_mcp2cli_through_the_environment
func TestHeadersReachMCP2CLIThroughEnvironment(t *testing.T) {
	config := &ServerConfig{
		Name:      "s",
		Transport: "sse",
		URL:       strPtr("https://mcp.example/sse"),
		Headers:   map[string]string{"Authorization": "Bearer t0p"},
	}

	argv, env, _, err := MCP2CLIInvocation(config)
	if err != nil {
		t.Fatalf("MCP2CLIInvocation: %v", err)
	}
	wantPrefix := []string{"--mcp", "https://mcp.example/sse", "--transport", "sse"}
	if !reflect.DeepEqual(argv[:4], wantPrefix) {
		t.Fatalf("argv[:4] = %v, want %v", argv[:4], wantPrefix)
	}
	wantAuth := []string{"--auth-header", "Authorization:env:HIVE_MCP_HEADER_0"}
	if !reflect.DeepEqual(argv[4:6], wantAuth) {
		t.Fatalf("argv[4:6] = %v, want %v", argv[4:6], wantAuth)
	}
	if !reflect.DeepEqual(env, map[string]string{"HIVE_MCP_HEADER_0": "Bearer t0p"}) {
		t.Fatalf("env = %v", env)
	}
	if strings.Contains(strings.Join(argv, " "), "t0p") {
		t.Fatalf("secret leaked into argv: %v", argv)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_a_unix_socket_server_cant_run_from_the_terminal
func TestUnixSocketServerCantRunFromTerminal(t *testing.T) {
	config := &ServerConfig{
		Name:       "s",
		Transport:  "unix",
		SocketPath: strPtr("/tmp/s.sock"),
	}
	_, _, _, err := MCP2CLIInvocation(config)
	requireErrContains(t, err, "unix transport")
}

// Parity: core/tests/test_mcp_cli.py::test_the_prompt_names_each_server_and_how_to_run_it
func TestPromptNamesEachServerAndHowToRunIt(t *testing.T) {
	block := CLIServersPrompt([]map[string]any{
		{"name": "comfy", "description": "ComfyUI image generation", "tool_count": 39},
	})
	if !strings.Contains(block, "- comfy: ComfyUI image generation (39 commands)") {
		t.Fatalf("block = %q", block)
	}
	if !strings.Contains(block, "hive mcp run <server> --list") {
		t.Fatalf("block = %q", block)
	}
	if got := CLIServersPrompt(nil); got != "" {
		t.Fatalf("empty prompt = %q", got)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_the_prompt_names_each_server_and_how_to_run_it
func TestRegistryPromptIsEmptyWithoutCLIServers(t *testing.T) {
	block := CLIServersPrompt(nil)
	if block != "" {
		t.Fatalf("block = %q, want empty", block)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_hive_mcp_add_takes_a_command_after_double_dash
func TestAddTakesCommandAfterDoubleDash(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errBuf := newCLI(registry)

	code := cli.Main([]string{
		"mcp", "add",
		"--name", "comfy",
		"--transport", "stdio",
		"--as-cli",
		"--env", "COMFYUI_URL=http://127.0.0.1:8000",
		"--", "uvx", "--from", "comfy-mcp", "comfy-mcp",
	})
	if code != 0 {
		t.Fatalf("Main = %d, stderr = %s", code, errBuf.String())
	}

	entry, err := registry.GetServer("comfy")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if entry["mode"] != "cli" {
		t.Fatalf("mode = %v", entry["mode"])
	}
	stdio := asMap(asMap(entry["manifest"])["stdio"])
	if stdio == nil {
		t.Fatalf("manifest.stdio missing: %v", entry["manifest"])
	}
	if stdio["command"] != "uvx" {
		t.Fatalf("command = %v", stdio["command"])
	}
	if !reflect.DeepEqual(asStringSlice(stdio["args"]), []string{"--from", "comfy-mcp", "comfy-mcp"}) {
		t.Fatalf("args = %v", stdio["args"])
	}
	if !reflect.DeepEqual(asStringMap(stdio["env"]), map[string]string{"COMFYUI_URL": "http://127.0.0.1:8000"}) {
		t.Fatalf("env = %v", stdio["env"])
	}
	if stdio["cwd"] != nil {
		t.Fatalf("cwd = %v, want nil", stdio["cwd"])
	}
}

// Parity-exempt: test_hive_mcp_run_calls_a_real_server needs a live mcp2cli
func TestRunForwardsArgsToMCP2CLI(t *testing.T) {
	registry := newDefaultRegistry(t)
	if err := registry.AddLocal("echo", AddLocalOptions{
		Transport: "stdio",
		Command:   "srv",
		Args:      []string{"echo"},
		Env:       map[string]string{"ECHO_MOOD": "loud"},
		Mode:      "cli",
	}); err != nil {
		t.Fatalf("AddLocal: %v", err)
	}

	cli, _, _ := newCLI(registry)
	var gotName string
	var gotArgs []string
	cli.RunServerFunc = func(config *ServerConfig, args []string) (int, error) {
		gotName = config.Name
		gotArgs = append([]string{}, args...)
		return 0, nil
	}

	code := cli.Run(RunArgs{Name: "echo", Args: []string{"--json", "shout", "--text", "hello hive"}})
	if code != 0 {
		t.Fatalf("Run = %d", code)
	}
	if gotName != "echo" {
		t.Fatalf("server = %q", gotName)
	}
	if !reflect.DeepEqual(gotArgs, []string{"--json", "shout", "--text", "hello hive"}) {
		t.Fatalf("args = %v", gotArgs)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_hive_mcp_run_names_an_unknown_server
func TestRunNamesUnknownServer(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errBuf := newCLI(registry)

	if code := cli.Run(RunArgs{Name: "ghost"}); code != 1 {
		t.Fatalf("Run = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "no MCP server named 'ghost'") {
		t.Fatalf("stderr = %q", errBuf.String())
	}
}

// Parity: core/tests/test_mcp_cli.py::test_the_routes_add_a_server_in_cli_mode_and_switch_it
// The upstream case switches cli->tools through the HTTP routes; this one drives Registry.SetMode directly.
func TestModeRoundTripOnInstalledServer(t *testing.T) {
	registry := newDefaultRegistry(t)
	addCLIServer(t, registry, "comfy", "cli")

	entry, err := registry.GetServer("comfy")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if entry["mode"] != "cli" {
		t.Fatalf("mode = %v", entry["mode"])
	}
	if err := registry.SetMode("comfy", "tools"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	entry, err = registry.GetServer("comfy")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if entry["mode"] != "tools" {
		t.Fatalf("mode = %v", entry["mode"])
	}
}

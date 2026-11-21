package mcpregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newCLI(registry *Registry) (*CLI, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cli := &CLI{
		Out:          out,
		Err:          errOut,
		RegistryFunc: func() (*Registry, error) { return registry, nil },
		Input:        func(string) (string, error) { return "", nil },
		GetenvFunc:   func(string) string { return "" },
	}
	return cli, out, errOut
}

func sampleIndex(t *testing.T, registry *Registry) map[string]any {
	t.Helper()
	servers := map[string]any{
		"jira": map[string]any{
			"version":     "1.2.0",
			"description": "Jira issue tracker integration",
			"status":      "verified",
			"transport":   map[string]any{"supported": []any{"stdio", "http"}, "default": "stdio"},
			"stdio":       map[string]any{"command": "uvx", "args": []any{"jira-mcp"}},
			"http":        map[string]any{"url": "http://localhost:4010"},
			"tools": []any{
				map[string]any{"name": "jira_create_issue", "description": "Create a Jira issue"},
				map[string]any{"name": "jira_search", "description": "Search issues with JQL"},
			},
			"credentials": []any{
				map[string]any{
					"id":          "api_token",
					"env_var":     "JIRA_API_TOKEN",
					"description": "Jira API token",
					"help_url":    "https://id.atlassian.com/manage-profile/security/api-tokens",
					"required":    true,
				},
			},
			"tags": []any{"project-management", "atlassian"},
			"hive": map[string]any{"profiles": []any{"productivity"}, "min_version": "0.5.0"},
		},
		"slack": map[string]any{
			"version":     "2.0.0",
			"description": "Slack messaging integration",
			"status":      "community",
			"transport":   map[string]any{"supported": []any{"http"}, "default": "http"},
			"http":        map[string]any{"url": "http://localhost:4011"},
			"tools":       []any{map[string]any{"name": "send_message", "description": "Send a Slack message"}},
			"tags":        []any{"messaging"},
		},
	}
	writeJSONAt(t, filepath.Join(registry.CacheDir(), "registry_index.json"), map[string]any{"servers": servers})
	writeJSONAt(t, filepath.Join(registry.CacheDir(), "last_fetched"), map[string]any{
		"timestamp": "2099-01-01T00:00:00+00:00",
	})
	return servers
}

func sampleManifest(t *testing.T, registry *Registry) map[string]any {
	t.Helper()
	return asMap(sampleIndex(t, registry)["jira"])
}

// Parity: core/tests/test_mcp_registry_cli.py::test_register_mcp_commands_creates_all_subcommands
func TestRegisterMCPCommandsCreatesAllSubcommands(t *testing.T) {
	help := MCPHelpText()
	for _, command := range []string{
		"install", "add", "remove", "enable", "disable",
		"list", "info", "config", "search", "health", "update",
	} {
		if !strings.Contains(help, command) {
			t.Fatalf("subcommand %q missing from mcp --help", command)
		}
	}
	parsed, err := ParseMCPArgs([]string{"mcp", "--help"})
	if err != nil {
		t.Fatalf("ParseMCPArgs: %v", err)
	}
	if parsed.Kind != "help" {
		t.Fatalf("kind = %q", parsed.Kind)
	}
}

// Parity-exempt: pins the argparse-compatible exit code of a bare `hive mcp`
// (upstream: required subcommand → 2); no upstream test exists for this path.
func TestMCPBareInvocationIsUsageError(t *testing.T) {
	cli, out, errOut := newCLI(newDefaultRegistry(t))
	if rc := cli.Main(nil); rc != 2 {
		t.Fatalf("bare `hive mcp` exit = %d, want 2", rc)
	}
	if !strings.Contains(errOut.String(), "required: command") {
		t.Fatalf("stderr = %q, want the argparse-style required-command error", errOut.String())
	}
	if strings.Contains(out.String(), "Manage MCP servers") {
		t.Fatalf("bare invocation printed group help to stdout: %q", out.String())
	}
	if rc := cli.Main([]string{"--help"}); rc != 0 {
		t.Fatalf("`hive mcp --help` exit = %d, want 0", rc)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_argparse_round_trip_install
func TestArgparseRoundTripInstall(t *testing.T) {
	parsed, err := ParseMCPArgs([]string{"mcp", "install", "jira", "--version", "1.0"})
	if err != nil {
		t.Fatalf("ParseMCPArgs: %v", err)
	}
	if parsed.Kind != "install" {
		t.Fatalf("kind = %q", parsed.Kind)
	}
	if parsed.Install.Name != "jira" || parsed.Install.Version != "1.0" {
		t.Fatalf("install args = %+v", parsed.Install)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_install_writes_to_installed_json
func TestInstallWritesToInstalledJSON(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, _ := newCLI(registry)

	if rc := cli.Install(InstallArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Installed jira") {
		t.Fatalf("out = %q", out.String())
	}
	server, err := registry.GetServer("jira")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if server == nil || server["transport"] != "stdio" {
		t.Fatalf("server = %v", server)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_install_with_version_pin
func TestInstallWithVersionPin(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, _, _ := newCLI(registry)

	if rc := cli.Install(InstallArgs{Name: "jira", Version: "1.2.0"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	server, _ := registry.GetServer("jira")
	if server["pinned"] != true {
		t.Fatalf("pinned = %v", server["pinned"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_install_not_found_fails
func TestInstallNotFoundFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, _, errOut := newCLI(registry)

	if rc := cli.Install(InstallArgs{Name: "nonexistent"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "not found in registry") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_install_duplicate_fails
func TestInstallDuplicateFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, _, errOut := newCLI(registry)

	if rc := cli.Install(InstallArgs{Name: "jira"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "already exists") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_security_notice_shown_only_once
func TestSecurityNoticeShownOnlyOnce(t *testing.T) {
	base := t.TempDir()
	first := &bytes.Buffer{}
	cli := &CLI{Err: first}

	cli.printSecurityNoticeIfFirstUse(base)
	cli.markSecurityNoticeShown(base)
	second := &bytes.Buffer{}
	cli.Err = second
	cli.printSecurityNoticeIfFirstUse(base)

	if !strings.Contains(first.String(), "Registry servers run code") {
		t.Fatalf("first = %q", first.String())
	}
	if second.String() != "" {
		t.Fatalf("second = %q", second.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_credential_prompt_stores_override
func TestCredentialPromptStoresOverride(t *testing.T) {
	registry := newDefaultRegistry(t)
	manifest := sampleManifest(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	cli, _, _ := newCLI(registry)
	cli.GetenvFunc = func(string) string { return "" }
	cli.Input = func(string) (string, error) { return "my-secret-token", nil }
	cli.promptForMissingCredentials(registry, "jira", manifest)

	server, _ := registry.GetServer("jira")
	env := asMap(asMap(server["overrides"])["env"])
	if env["JIRA_API_TOKEN"] != "my-secret-token" {
		t.Fatalf("overrides = %v", server["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_credential_prompt_skips_when_env_set
func TestCredentialPromptSkipsWhenEnvSet(t *testing.T) {
	registry := newDefaultRegistry(t)
	manifest := sampleManifest(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	cli, _, _ := newCLI(registry)
	cli.GetenvFunc = func(name string) string {
		if name == "JIRA_API_TOKEN" {
			return "already-set"
		}
		return ""
	}
	var calls []string
	cli.Input = func(prompt string) (string, error) {
		calls = append(calls, prompt)
		return "", nil
	}
	cli.promptForMissingCredentials(registry, "jira", manifest)

	if len(calls) != 0 {
		t.Fatalf("prompted %d times", len(calls))
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_add_registers_local_http_server
func TestAddRegistersLocalHTTPServer(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, out, _ := newCLI(registry)

	rc := cli.Add(AddArgs{
		Name:        "my-db",
		Transport:   "http",
		URL:         "http://localhost:9090",
		Description: "Custom DB",
	})
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Registered my-db") {
		t.Fatalf("out = %q", out.String())
	}
	server, _ := registry.GetServer("my-db")
	if server == nil || server["transport"] != "http" {
		t.Fatalf("server = %v", server)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_add_missing_name_fails
func TestAddMissingNameFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errOut := newCLI(registry)

	rc := cli.Add(AddArgs{Transport: "http", URL: "http://localhost:9090"})
	if rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "--name is required") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_add_missing_transport_fails
func TestAddMissingTransportFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errOut := newCLI(registry)

	if rc := cli.Add(AddArgs{Name: "my-db"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "--transport is required") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_add_from_manifest_file
func TestAddFromManifestFile(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, out, _ := newCLI(registry)

	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	writeJSONAt(t, manifestPath, map[string]any{
		"name":        "from-file",
		"description": "Loaded from manifest",
		"transport":   map[string]any{"supported": []any{"http"}, "default": "http"},
		"http":        map[string]any{"url": "http://localhost:5000"},
	})

	if rc := cli.Add(AddArgs{FromManifest: manifestPath}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Registered from-file") {
		t.Fatalf("out = %q", out.String())
	}
	server, _ := registry.GetServer("from-file")
	if server == nil {
		t.Fatalf("from-file not installed")
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_remove_deletes_server
func TestRemoveDeletesServer(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Remove("jira"); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Removed jira") {
		t.Fatalf("out = %q", out.String())
	}
	server, _ := registry.GetServer("jira")
	if server != nil {
		t.Fatalf("jira still installed")
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_remove_nonexistent_fails
func TestRemoveNonexistentFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errOut := newCLI(registry)

	if rc := cli.Remove("nonexistent"); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "not installed") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_enable_disable_toggles_state
func TestEnableDisableTogglesState(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Disable("jira"); rc != 0 {
		t.Fatalf("disable rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Disabled") {
		t.Fatalf("out = %q", out.String())
	}
	server, _ := registry.GetServer("jira")
	if server["enabled"] != false {
		t.Fatalf("enabled = %v", server["enabled"])
	}

	out.Reset()
	if rc := cli.Enable("jira"); rc != 0 {
		t.Fatalf("enable rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Enabled") {
		t.Fatalf("out = %q", out.String())
	}
	server, _ = registry.GetServer("jira")
	if server["enabled"] != true {
		t.Fatalf("enabled = %v", server["enabled"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_list_renders_installed_entries
func TestListRendersInstalledEntries(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install jira: %v", err)
	}
	if err := registry.Install("slack", InstallOptions{}); err != nil {
		t.Fatalf("Install slack: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.List(ListArgs{}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	for _, want := range []string{"jira", "slack", "NAME"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("out missing %q: %q", want, rendered)
		}
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_list_empty_shows_help
func TestListEmptyShowsHelp(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, out, _ := newCLI(registry)

	if rc := cli.List(ListArgs{}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "No servers installed") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_list_json_output
func TestListJSONOutput(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.List(ListArgs{OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var data []map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, out.String())
	}
	if len(data) != 1 || data[0]["name"] != "jira" {
		t.Fatalf("data = %v", data)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_list_available_reads_index
func TestListAvailableReadsIndex(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, _ := newCLI(registry)

	if rc := cli.List(ListArgs{Available: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "jira") || !strings.Contains(rendered, "slack") {
		t.Fatalf("out = %q", rendered)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_info_shows_details
func TestInfoShowsDetails(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Info(InfoArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	for _, want := range []string{"jira", "stdio", "1.2.0", "verified"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("out missing %q: %q", want, rendered)
		}
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_info_json_output
func TestInfoJSONOutput(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Info(InfoArgs{Name: "jira", OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var data map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, out.String())
	}
	if data["name"] != "jira" || data["transport"] != "stdio" {
		t.Fatalf("data = %v", data)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_info_not_found_fails
func TestInfoNotFoundFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errOut := newCLI(registry)

	if rc := cli.Info(InfoArgs{Name: "nonexistent"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := errOut.String()
	if !strings.Contains(rendered, "not installed") || !strings.Contains(rendered, "hive mcp install") {
		t.Fatalf("err = %q", rendered)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_config_sets_env_override
func TestConfigSetsEnvOverride(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, _, _ := newCLI(registry)

	if rc := cli.Config(ConfigArgs{Name: "jira", SetEnv: []string{"JIRA_API_TOKEN=abc123"}}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	server, _ := registry.GetServer("jira")
	env := asMap(asMap(server["overrides"])["env"])
	if env["JIRA_API_TOKEN"] != "abc123" {
		t.Fatalf("overrides = %v", server["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_config_sets_header_override
func TestConfigSetsHeaderOverride(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, _, _ := newCLI(registry)

	if rc := cli.Config(ConfigArgs{Name: "jira", SetHeader: []string{"Authorization=Bearer xyz"}}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	server, _ := registry.GetServer("jira")
	headers := asMap(asMap(server["overrides"])["headers"])
	if headers["Authorization"] != "Bearer xyz" {
		t.Fatalf("overrides = %v", server["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_config_shows_current_when_no_args
func TestConfigShowsCurrentWhenNoArgs(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "secret"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Config(ConfigArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "JIRA_API_TOKEN") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_search_finds_by_name
func TestSearchFindsByName(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, _ := newCLI(registry)

	if rc := cli.Search(SearchArgs{Query: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "jira") {
		t.Fatalf("out = %q", rendered)
	}
	if strings.Contains(rendered, "slack") {
		t.Fatalf("out unexpectedly mentions slack: %q", rendered)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_search_finds_by_tag
func TestSearchFindsByTag(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, _ := newCLI(registry)

	if rc := cli.Search(SearchArgs{Query: "messaging"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "slack") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_search_no_results
func TestCLISearchNoResults(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, _ := newCLI(registry)

	if rc := cli.Search(SearchArgs{Query: "zzz_nonexistent_zzz"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "No servers matching") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_search_json_output
func TestSearchJSONOutput(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, _ := newCLI(registry)

	if rc := cli.Search(SearchArgs{Query: "jira", OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var data []map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, out.String())
	}
	if len(data) != 1 || data[0]["name"] != "jira" {
		t.Fatalf("data = %v", data)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_health_returns_status
func TestHealthReturnsStatus(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)
	cli.HealthCheckFunc = func(*Registry, string) (map[string]map[string]any, error) {
		return map[string]map[string]any{
			"jira": {"name": "jira", "status": "healthy", "tools": json.Number("2"), "error": nil},
		}, nil
	}

	if rc := cli.Health(HealthArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "healthy") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_health_json_output
func TestHealthJSONOutput(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)
	cli.HealthCheckFunc = func(*Registry, string) (map[string]map[string]any, error) {
		return map[string]map[string]any{
			"jira": {"name": "jira", "status": "healthy", "tools": json.Number("2"), "error": nil},
		}, nil
	}

	if rc := cli.Health(HealthArgs{Name: "jira", OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var data map[string]map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, out.String())
	}
	if _, ok := data["jira"]; !ok {
		t.Fatalf("data = %v", data)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_named_server_reinstalls
func TestUpdateNamedServerReinstalls(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "my-token"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Update(UpdateArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "jira") {
		t.Fatalf("out = %q", out.String())
	}
	server, _ := registry.GetServer("jira")
	if server == nil {
		t.Fatalf("jira missing after update")
	}
	env := asMap(asMap(server["overrides"])["env"])
	if env["JIRA_API_TOKEN"] != "my-token" {
		t.Fatalf("overrides = %v", server["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_local_server_fails
func TestUpdateLocalServerFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "my-db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	cli, _, errOut := newCLI(registry)

	if rc := cli.Update(UpdateArgs{Name: "my-db"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "local server") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_pinned_server_fails_with_correct_remediation
func TestUpdatePinnedServerFailsWithCorrectRemediation(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{Version: "1.2.0"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, _, errOut := newCLI(registry)

	if rc := cli.Update(UpdateArgs{Name: "jira"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := errOut.String()
	for _, want := range []string{"pinned", "hive mcp remove", "hive mcp install"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("err missing %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "config") {
		t.Fatalf("err must not suggest config: %q", rendered)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_restores_server_on_reinstall_failure
func TestUpdateRestoresServerOnReinstallFailure(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "my-token"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}

	indexPath := filepath.Join(registry.CacheDir(), "registry_index.json")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var index map[string]any
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(serversOf(index), "jira")
	writeJSONAt(t, indexPath, index)

	cli, _, errOut := newCLI(registry)
	if rc := cli.Update(UpdateArgs{Name: "jira"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "restored") {
		t.Fatalf("err = %q", errOut.String())
	}
	server, _ := registry.GetServer("jira")
	if server == nil {
		t.Fatalf("jira missing after failed update")
	}
	if server["transport"] != "stdio" {
		t.Fatalf("transport = %v", server["transport"])
	}
	env := asMap(asMap(server["overrides"])["env"])
	if env["JIRA_API_TOKEN"] != "my-token" {
		t.Fatalf("overrides = %v", server["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_index_succeeds
func TestUpdateIndexSucceeds(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, out, _ := newCLI(registry)
	cli.UpdateIndexFunc = func(*Registry) (int, error) { return 5, nil }

	if rc := cli.Update(UpdateArgs{}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "5 servers available") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_all_updates_registry_servers
func TestUpdateAllUpdatesRegistryServers(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install jira: %v", err)
	}
	if err := registry.Install("slack", InstallOptions{}); err != nil {
		t.Fatalf("Install slack: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "keep-me"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	cli, out, _ := newCLI(registry)
	cli.UpdateIndexFunc = func(*Registry) (int, error) { return 2, nil }

	if rc := cli.Update(UpdateArgs{}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	for _, want := range []string{"2 installed server(s)", "jira", "slack"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("out missing %q: %q", want, rendered)
		}
	}
	server, _ := registry.GetServer("jira")
	env := asMap(asMap(server["overrides"])["env"])
	if env["JIRA_API_TOKEN"] != "keep-me" {
		t.Fatalf("overrides = %v", server["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_all_skips_local_and_pinned
func TestUpdateAllSkipsLocalAndPinned(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{Version: "1.2.0"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	mustAddLocal(t, registry, "my-db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	cli, out, _ := newCLI(registry)
	cli.UpdateIndexFunc = func(*Registry) (int, error) { return 2, nil }

	if rc := cli.Update(UpdateArgs{}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if strings.Contains(out.String(), "installed server(s)") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_parse_key_value_pairs_valid
func TestParseKeyValuePairsValid(t *testing.T) {
	result, err := parseKeyValuePairs([]string{"KEY=value", "FOO=bar=baz"})
	if err != nil {
		t.Fatalf("parseKeyValuePairs: %v", err)
	}
	if result["KEY"] != "value" || result["FOO"] != "bar=baz" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_parse_key_value_pairs_invalid
func TestParseKeyValuePairsInvalid(t *testing.T) {
	_, err := parseKeyValuePairs([]string{"NOEQUALS"})
	requireErrContains(t, err, "Invalid format")
}

// Parity: core/tests/test_mcp_registry_cli.py::test_parse_key_value_pairs_empty_key
func TestParseKeyValuePairsEmptyKey(t *testing.T) {
	_, err := parseKeyValuePairs([]string{"=value"})
	requireErrContains(t, err, "Key cannot be empty")
}

// Parity: core/tests/test_mcp_registry_cli.py::test_install_fails_when_no_cache_and_refresh_fails
func TestInstallFailsWhenNoCacheAndRefreshFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errOut := newCLI(registry)
	cli.IsIndexStaleFunc = func(*Registry) bool { return true }
	cli.UpdateIndexFunc = func(*Registry) (int, error) { return 0, errors.New("offline") }

	if rc := cli.Install(InstallArgs{Name: "jira"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "no registry index available") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_install_uses_stale_cache_when_refresh_fails
func TestInstallUsesStaleCacheWhenRefreshFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, errOut := newCLI(registry)
	cli.IsIndexStaleFunc = func(*Registry) bool { return true }
	cli.UpdateIndexFunc = func(*Registry) (int, error) { return 0, errors.New("offline") }

	if rc := cli.Install(InstallArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "Using cached index") {
		t.Fatalf("err = %q", errOut.String())
	}
	if !strings.Contains(out.String(), "Installed jira") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_list_includes_tools_count_and_trust_tier
func TestListIncludesToolsCountAndTrustTier(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.List(ListArgs{}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	for _, want := range []string{"TOOLS", "TRUST", "2", "verified"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("out missing %q: %q", want, rendered)
		}
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_config_display_masks_values
func TestConfigDisplayMasksValues(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "super-secret-value"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Config(ConfigArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "<set>") {
		t.Fatalf("out = %q", rendered)
	}
	if strings.Contains(rendered, "super-secret-value") || strings.Contains(rendered, "supe...") {
		t.Fatalf("out leaked value: %q", rendered)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_info_masks_override_values
func TestInfoMasksOverrideValues(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "my-secret"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Info(InfoArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "<set>") {
		t.Fatalf("out = %q", rendered)
	}
	if strings.Contains(rendered, "my-secret") {
		t.Fatalf("out leaked value: %q", rendered)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_credential_prompt_cancel_does_not_abort_install
func TestCredentialPromptCancelDoesNotAbortInstall(t *testing.T) {
	registry := newDefaultRegistry(t)
	manifest := sampleManifest(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	cli, _, _ := newCLI(registry)
	cli.GetenvFunc = func(string) string { return "" }
	cli.Input = func(string) (string, error) { return "", errors.New("interrupted") }
	cli.promptForMissingCredentials(registry, "jira", manifest)

	server, _ := registry.GetServer("jira")
	if server == nil {
		t.Fatalf("jira missing after cancelled prompt")
	}
	env := asMap(asMap(server["overrides"])["env"])
	if len(env) != 0 {
		t.Fatalf("overrides = %v", server["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_nonexistent_server_fails
func TestUpdateNonexistentServerFails(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errOut := newCLI(registry)

	if rc := cli.Update(UpdateArgs{Name: "nonexistent"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := errOut.String()
	if !strings.Contains(rendered, "not installed") || !strings.Contains(rendered, "hive mcp install") {
		t.Fatalf("err = %q", rendered)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_main_dispatches_mcp_list_through_real_argparse
func TestMainDispatchesMCPListThroughRealArgparse(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, out, _ := newCLI(registry)

	if rc := cli.Main([]string{"hive", "mcp", "list"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "No servers installed") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_main_dispatches_mcp_install_through_real_argparse
func TestMainDispatchesMCPInstallThroughRealArgparse(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	cli, out, _ := newCLI(registry)

	if rc := cli.Main([]string{"hive", "mcp", "install", "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Installed jira") {
		t.Fatalf("out = %q", out.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_main_dispatches_mcp_update_named_through_real_argparse
func TestMainDispatchesMCPUpdateNamedThroughRealArgparse(t *testing.T) {
	registry := newDefaultRegistry(t)
	cli, _, errOut := newCLI(registry)

	if rc := cli.Main([]string{"hive", "mcp", "update", "nonexistent"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errOut.String(), "not installed") {
		t.Fatalf("err = %q", errOut.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_info_json_includes_agent_usage
func TestInfoJSONIncludesAgentUsage(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	agentDir := filepath.Join(t.TempDir(), "fake_agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cli, out, _ := newCLI(registry)
	cli.FindAgentsFunc = func(*Registry, string) []string { return []string{agentDir} }

	if rc := cli.Info(InfoArgs{Name: "jira", OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var data map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, out.String())
	}
	agents := asStringSlice(data["used_by_agents"])
	if !contains(agents, agentDir) {
		t.Fatalf("used_by_agents = %v", data["used_by_agents"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_info_json_masks_override_secrets
func TestInfoJSONMasksOverrideSecrets(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "super-secret-token-123"); err != nil {
		t.Fatalf("SetOverride env: %v", err)
	}
	if err := registry.SetOverride("jira", "headers", "Authorization", "Bearer top-secret"); err != nil {
		t.Fatalf("SetOverride headers: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.Info(InfoArgs{Name: "jira", OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	if strings.Contains(rendered, "super-secret-token-123") || strings.Contains(rendered, "top-secret") {
		t.Fatalf("out leaked secret: %q", rendered)
	}
	var data map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	overrides := asMap(data["overrides"])
	if asMap(overrides["env"])["JIRA_API_TOKEN"] != "<set>" {
		t.Fatalf("env = %v", overrides["env"])
	}
	if asMap(overrides["headers"])["Authorization"] != "<set>" {
		t.Fatalf("headers = %v", overrides["headers"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_health_all_servers
func TestHealthAllServers(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install jira: %v", err)
	}
	if err := registry.Install("slack", InstallOptions{}); err != nil {
		t.Fatalf("Install slack: %v", err)
	}
	cli, out, _ := newCLI(registry)
	cli.HealthCheckFunc = func(*Registry, string) (map[string]map[string]any, error) {
		return map[string]map[string]any{
			"jira":  {"status": "healthy", "tools": json.Number("2"), "error": nil},
			"slack": {"status": "unhealthy", "tools": json.Number("0"), "error": "Connection refused"},
		}, nil
	}

	if rc := cli.Health(HealthArgs{}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	rendered := out.String()
	for _, want := range []string{"jira", "healthy", "slack", "Connection refused"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("out missing %q: %q", want, rendered)
		}
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_health_all_servers_json
func TestHealthAllServersJSON(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cli, out, _ := newCLI(registry)
	cli.HealthCheckFunc = func(*Registry, string) (map[string]map[string]any, error) {
		return map[string]map[string]any{
			"jira": {"status": "healthy", "tools": json.Number("2"), "error": nil},
		}, nil
	}

	if rc := cli.Health(HealthArgs{OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var data map[string]map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v (%q)", err, out.String())
	}
	if data["jira"]["status"] != "healthy" {
		t.Fatalf("data = %v", data)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_find_agents_using_server_resolves_via_load_agent_selection
func TestFindAgentsUsingServerResolvesViaLoadAgentSelection(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeJSONAt(t, filepath.Join(registry.CacheDir(), "registry_index.json"), map[string]any{
		"servers": map[string]any{
			"jira": map[string]any{
				"version":   "1.0.0",
				"transport": map[string]any{"supported": []any{"http"}, "default": "http"},
				"http":      map[string]any{"url": "http://localhost:4010"},
				"tools":     []any{},
				"tags":      []any{"pm"},
			},
		},
	})
	writeJSONAt(t, filepath.Join(registry.CacheDir(), "last_fetched"), map[string]any{
		"timestamp": "2099-01-01T00:00:00+00:00",
	})
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	exportsDir := filepath.Join(t.TempDir(), "exports")
	agentYes := filepath.Join(exportsDir, "agent_with_jira")
	agentNo := filepath.Join(exportsDir, "agent_without_jira")
	writeJSONAt(t, filepath.Join(agentYes, "mcp_registry.json"), map[string]any{"include": []any{"jira"}})
	writeJSONAt(t, filepath.Join(agentNo, "mcp_registry.json"), map[string]any{"include": []any{"slack"}})

	results := FindAgentsUsingServerIn([]string{exportsDir}, registry, "jira")
	if !contains(results, agentYes) {
		t.Fatalf("results = %v", results)
	}
	if contains(results, agentNo) {
		t.Fatalf("agent without jira matched: %v", results)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_integration_real_registry_install_list_info_remove
func TestIntegrationRealRegistryInstallListInfoRemove(t *testing.T) {
	base := filepath.Join(t.TempDir(), "mcp_registry")
	registry := newRegistryAt(t, base)
	writeJSONAt(t, filepath.Join(registry.CacheDir(), "registry_index.json"), map[string]any{
		"servers": map[string]any{
			"jira": map[string]any{
				"version":     "1.2.0",
				"description": "Jira integration",
				"status":      "verified",
				"transport":   map[string]any{"supported": []any{"stdio", "http"}, "default": "http"},
				"http":        map[string]any{"url": "http://localhost:4010"},
				"tools": []any{
					map[string]any{"name": "create_issue", "description": "Create a Jira issue"},
					map[string]any{"name": "search", "description": "Search issues"},
				},
				"credentials": []any{
					map[string]any{"env_var": "JIRA_TOKEN", "description": "API token", "required": true},
				},
				"tags": []any{"pm"},
			},
		},
	})
	writeJSONAt(t, filepath.Join(registry.CacheDir(), "last_fetched"), map[string]any{
		"timestamp": "2099-01-01T00:00:00+00:00",
	})
	if err := os.WriteFile(filepath.Join(base, noticeSentinelName), nil, 0o600); err != nil {
		t.Fatalf("WriteFile sentinel: %v", err)
	}

	cli, out, _ := newCLI(registry)
	cli.GetenvFunc = func(string) string { return "from-env" }

	if rc := cli.Install(InstallArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("install rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Installed jira") {
		t.Fatalf("out = %q", out.String())
	}
	installed := installedEntry(t, registry, "jira")
	if installed["transport"] != "http" || installed["source"] != "registry" {
		t.Fatalf("installed = %v", installed)
	}

	out.Reset()
	if rc := cli.List(ListArgs{}); rc != 0 {
		t.Fatalf("list rc = %d", rc)
	}
	rendered := out.String()
	for _, want := range []string{"jira", "2", "verified"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("list missing %q: %q", want, rendered)
		}
	}

	if rc := cli.Config(ConfigArgs{Name: "jira", SetEnv: []string{"JIRA_TOKEN=real-token"}}); rc != 0 {
		t.Fatalf("config rc = %d", rc)
	}
	installed = installedEntry(t, registry, "jira")
	if asMap(asMap(installed["overrides"])["env"])["JIRA_TOKEN"] != "real-token" {
		t.Fatalf("overrides = %v", installed["overrides"])
	}

	out.Reset()
	if rc := cli.Info(InfoArgs{Name: "jira", OutputJSON: true}); rc != 0 {
		t.Fatalf("info rc = %d", rc)
	}
	if strings.Contains(out.String(), "real-token") {
		t.Fatalf("info leaked token: %q", out.String())
	}
	var data map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["name"] != "jira" || data["transport"] != "http" || data["manifest_version"] != "1.2.0" {
		t.Fatalf("data = %v", data)
	}
	if asMap(asMap(data["overrides"])["env"])["JIRA_TOKEN"] != "<set>" {
		t.Fatalf("overrides = %v", data["overrides"])
	}

	out.Reset()
	if rc := cli.Remove("jira"); rc != 0 {
		t.Fatalf("remove rc = %d", rc)
	}
	if !strings.Contains(out.String(), "Removed jira") {
		t.Fatalf("out = %q", out.String())
	}
	if server, _ := registry.GetServer("jira"); server != nil {
		t.Fatalf("jira still installed")
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_list_json_masks_override_secrets
func TestListJSONMasksOverrideSecrets(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.SetOverride("jira", "env", "JIRA_API_TOKEN", "super-secret"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	cli, out, _ := newCLI(registry)

	if rc := cli.List(ListArgs{OutputJSON: true}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if strings.Contains(out.String(), "super-secret") {
		t.Fatalf("out leaked secret: %q", out.String())
	}
	var data []map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if asMap(asMap(data[0]["overrides"])["env"])["JIRA_API_TOKEN"] != "<set>" {
		t.Fatalf("overrides = %v", data[0]["overrides"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_update_preserves_disabled_state
func TestUpdatePreservesDisabledState(t *testing.T) {
	registry := newDefaultRegistry(t)
	sampleIndex(t, registry)
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := registry.Disable("jira"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	server, _ := registry.GetServer("jira")
	if server["enabled"] != false {
		t.Fatalf("enabled = %v", server["enabled"])
	}
	cli, _, _ := newCLI(registry)

	if rc := cli.Update(UpdateArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	server, _ = registry.GetServer("jira")
	if server["enabled"] != false {
		t.Fatalf("enabled after update = %v", server["enabled"])
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_security_notice_not_persisted_on_failed_install
func TestSecurityNoticeNotPersistedOnFailedInstall(t *testing.T) {
	registry := newDefaultRegistry(t)
	sentinel := filepath.Join(registry.Base(), noticeSentinelName)
	if fileExists(sentinel) {
		t.Fatalf("sentinel already present")
	}
	sampleIndex(t, registry)
	cli, _, _ := newCLI(registry)

	if rc := cli.Install(InstallArgs{Name: "nonexistent"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if fileExists(sentinel) {
		t.Fatalf("sentinel written after failed install")
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_security_notice_persisted_on_successful_install
func TestSecurityNoticePersistedOnSuccessfulInstall(t *testing.T) {
	registry := newDefaultRegistry(t)
	sentinel := filepath.Join(registry.Base(), noticeSentinelName)
	if fileExists(sentinel) {
		t.Fatalf("sentinel already present")
	}
	sampleIndex(t, registry)
	cli, _, _ := newCLI(registry)

	if rc := cli.Install(InstallArgs{Name: "jira"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !fileExists(sentinel) {
		t.Fatalf("sentinel missing after successful install")
	}
}

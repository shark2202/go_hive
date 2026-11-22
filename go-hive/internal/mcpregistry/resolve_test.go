package mcpregistry

import (
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// Parity: core/tests/test_mcp_registry.py::test_resolve_include
func TestResolveInclude(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"jira", "slack"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	got := names(configs)
	if len(got) != 2 || got[0] != "jira" || got[1] != "slack" {
		t.Fatalf("names = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_exclude
func TestResolveExclude(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{
		Include: []string{"jira", "slack", "github"},
		Exclude: []string{"github"},
	})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if containsName(configs, "github") {
		t.Fatalf("github should be excluded: %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_tags
func TestResolveTags(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{Tags: []string{"productivity"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if !containsName(configs, "jira") || !containsName(configs, "slack") || containsName(configs, "github") {
		t.Fatalf("names = %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_profile
func TestResolveProfile(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{Profile: "productivity"})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if !containsName(configs, "jira") || !containsName(configs, "slack") {
		t.Fatalf("names = %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_profile_all
func TestResolveProfileAll(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{Profile: "all"})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	for _, want := range []string{"jira", "slack", "github"} {
		if !containsName(configs, want) {
			t.Fatalf("%s missing from %v", want, names(configs))
		}
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_max_tools
func TestResolveMaxTools(t *testing.T) {
	registry := setupRegistryWithServers(t)
	data, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	servers := asMap(data["servers"])
	asMap(asMap(servers["jira"])["manifest"])["tools"] = []any{
		map[string]any{"name": "a"}, map[string]any{"name": "b"},
	}
	asMap(asMap(servers["slack"])["manifest"])["tools"] = []any{
		map[string]any{"name": "c"}, map[string]any{"name": "d"},
	}
	asMap(asMap(servers["github"])["manifest"])["tools"] = []any{
		map[string]any{"name": "e"},
	}
	if err := registry.WriteInstalled(data); err != nil {
		t.Fatalf("WriteInstalled: %v", err)
	}

	maxTools := 3
	configs, err := registry.ResolveForAgent(ResolveOptions{Profile: "all", MaxTools: &maxTools})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	fresh, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	freshServers := asMap(fresh["servers"])
	installed := 0
	total := 0
	for _, config := range configs {
		if config.Transport == "harness" {
			continue
		}
		installed++
		if list, ok := asMap(asMap(freshServers[config.Name])["manifest"])["tools"].([]any); ok {
			total += len(list)
		}
	}
	if total > 3 || installed < 1 {
		t.Fatalf("total=%d installed=%d", total, installed)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_skips_disabled
func TestResolveSkipsDisabled(t *testing.T) {
	registry := setupRegistryWithServers(t)
	if err := registry.Disable("jira"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"jira", "slack"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if containsName(configs, "jira") {
		t.Fatalf("disabled jira resolved: %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_missing_server_warns
func TestResolveMissingServerWarns(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"jira", "ghost"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_versions_match
func TestResolveVersionsMatch(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.2.0",
			"transport": map[string]any{"default": "stdio"},
			"stdio":     map[string]any{"command": "uvx"},
		},
	})
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	matched, err := registry.ResolveForAgent(ResolveOptions{
		Include:  []string{"jira"},
		Versions: map[string]string{"jira": "1.2.0"},
	})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("version match expected 1, got %v", names(matched))
	}
	mismatched, err := registry.ResolveForAgent(ResolveOptions{
		Include:  []string{"jira"},
		Versions: map[string]string{"jira": "9.9.9"},
	})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(mismatched) != 0 {
		t.Fatalf("version mismatch expected 0, got %v", names(mismatched))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_for_agent_no_criteria
func TestResolveForAgentNoCriteria(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected no configs, got %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_for_agent_synthesises_harness_groups
func TestResolveForAgentSynthesisesHarnessGroups(t *testing.T) {
	registry := newDefaultRegistry(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"terminal-tools", "hive_tools"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 harness configs, got %v", names(configs))
	}
	want := [][2]string{{"terminal-tools", "harness"}, {"hive_tools", "harness"}}
	for i, pair := range want {
		if configs[i].Name != pair[0] || configs[i].Transport != pair[1] {
			t.Fatalf("config %d = (%s, %s), want (%s, %s)", i, configs[i].Name, configs[i].Transport, pair[0], pair[1])
		}
	}

	every, err := registry.ResolveForAgent(ResolveOptions{Profile: "all", Exclude: []string{"memory-tools"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	got := map[string]bool{}
	for _, config := range every {
		got[config.Name] = true
	}
	wantNames := map[string]bool{}
	for name := range tools.HarnessGroupNames {
		if name != "memory-tools" {
			wantNames[name] = true
		}
	}
	if len(got) != len(wantNames) {
		t.Fatalf("got %v, want %v", got, wantNames)
	}
	for name := range wantNames {
		if !got[name] {
			t.Fatalf("missing %s from %v", name, got)
		}
	}
}

// Parity: core/tests/test_mcp_registry.py::test_resolve_profile_matches_nothing
func TestResolveProfileMatchesNothing(t *testing.T) {
	registry := setupRegistryWithServers(t)
	configs, err := registry.ResolveForAgent(ResolveOptions{Profile: "nonexistent_profile"})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected no configs, got %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_config_merges_env
func TestConfigMergesEnv(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "t", AddLocalOptions{
		Transport: "stdio", Command: "uvx", Env: map[string]string{"A": "1"},
	})
	if err := registry.SetOverride("t", "env", "B", "2"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"t"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %v", names(configs))
	}
	if configs[0].Env["A"] != "1" || configs[0].Env["B"] != "2" {
		t.Fatalf("env = %v", configs[0].Env)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_config_overrides_win
func TestConfigOverridesWin(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "t", AddLocalOptions{
		Transport: "stdio", Command: "uvx", Env: map[string]string{"K": "old"},
	})
	if err := registry.SetOverride("t", "env", "K", "new"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"t"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 || configs[0].Env["K"] != "new" {
		t.Fatalf("configs = %+v", configs)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_config_includes_cwd
func TestConfigIncludesCwd(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "t", AddLocalOptions{Transport: "stdio", Command: "uvx", Cwd: "/opt"})
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"t"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 || deref(configs[0].Cwd) != "/opt" {
		t.Fatalf("configs = %+v", configs)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_config_includes_description
func TestConfigIncludesDescription(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "t", AddLocalOptions{Transport: "stdio", Command: "uvx", Description: "desc"})
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"t"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 || configs[0].Description != "desc" {
		t.Fatalf("configs = %+v", configs)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_config_unix_transport
func TestConfigUnixTransport(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "u", AddLocalOptions{Transport: "unix", SocketPath: "/tmp/s.sock"})
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"u"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %v", names(configs))
	}
	if configs[0].Transport != "unix" || deref(configs[0].SocketPath) != "/tmp/s.sock" {
		t.Fatalf("config = %+v", configs[0])
	}
}

// Parity: core/tests/test_mcp_registry.py::test_config_unsupported_transport_returns_none
func TestConfigUnsupportedTransportReturnsNone(t *testing.T) {
	registry := newDefaultRegistry(t)
	data, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	asMap(data["servers"])["x"] = map[string]any{
		"source":    "local",
		"transport": "grpc",
		"enabled":   true,
		"manifest":  map[string]any{"name": "x", "transport": map[string]any{"default": "grpc"}},
		"overrides": map[string]any{"env": map[string]any{}, "headers": map[string]any{}},
	}
	if err := registry.WriteInstalled(data); err != nil {
		t.Fatalf("WriteInstalled: %v", err)
	}
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"x"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected no configs, got %v", names(configs))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_server_config_to_dict
func TestServerConfigToDict(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "t", AddLocalOptions{
		Transport: "stdio", Command: "uvx", Cwd: "/opt", Description: "d",
	})
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"t"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %v", names(configs))
	}
	dict := configs[0].ToDict()
	if dict["name"] != "t" || dict["transport"] != "stdio" {
		t.Fatalf("dict = %v", dict)
	}
	if dict["cwd"] != "/opt" || dict["description"] != "d" {
		t.Fatalf("dict = %v", dict)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_load_agent_selection
func TestLoadAgentSelection(t *testing.T) {
	registry := setupRegistryWithServers(t)
	agentDir := filepath.Join(t.TempDir(), "agent")
	writeJSONAt(t, filepath.Join(agentDir, "mcp_registry.json"), map[string]any{
		"include": []any{"jira", "slack"},
	})
	dicts, maxTools, err := registry.LoadAgentSelection(agentDir)
	if err != nil {
		t.Fatalf("LoadAgentSelection: %v", err)
	}
	if len(dicts) != 2 || maxTools != nil {
		t.Fatalf("dicts=%v maxTools=%v", dicts, maxTools)
	}
	for _, dict := range dicts {
		if _, ok := dict["transport"]; !ok {
			t.Fatalf("config dict missing transport: %v", dict)
		}
	}
}

// Parity: core/tests/test_mcp_registry.py::test_load_agent_selection_no_file
func TestLoadAgentSelectionNoFile(t *testing.T) {
	registry := newDefaultRegistry(t)
	agentDir := filepath.Join(t.TempDir(), "agent")
	dicts, maxTools, err := registry.LoadAgentSelection(agentDir)
	if err != nil {
		t.Fatalf("LoadAgentSelection: %v", err)
	}
	if len(dicts) != 0 || maxTools != nil {
		t.Fatalf("dicts=%v maxTools=%v", dicts, maxTools)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_load_agent_selection_rejects_wrong_types
func TestLoadAgentSelectionRejectsWrongTypes(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		badValue any
	}{
		{"include_string", "include", "jira"},
		{"tags_string", "tags", "pm"},
		{"exclude_string", "exclude", "jira"},
		{"profile_list", "profile", []any{"all"}},
		{"max_tools_string", "max_tools", "50"},
		{"versions_list", "versions", []any{"1.0.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := setupRegistryWithServers(t)
			agentDir := filepath.Join(t.TempDir(), "agent")
			writeJSONAt(t, filepath.Join(agentDir, "mcp_registry.json"), map[string]any{tc.field: tc.badValue})
			dicts, maxTools, err := registry.LoadAgentSelection(agentDir)
			if err != nil {
				t.Fatalf("LoadAgentSelection: %v", err)
			}
			if len(dicts) != 0 || maxTools != nil {
				t.Fatalf("expected dropped field, got dicts=%v maxTools=%v", dicts, maxTools)
			}
		})
	}
}

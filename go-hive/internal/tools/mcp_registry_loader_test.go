package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loaderClients(toolMap map[string][]string) map[string]MCPClient {
	clients := map[string]MCPClient{}
	for name, tools := range toolMap {
		exposed := make([]MCPTool, 0, len(tools))
		for _, toolName := range tools {
			exposed = append(exposed, tool(toolName))
		}
		clients[name] = newFakeMCPClient(&MCPServerConfig{Name: name}, exposed...)
	}
	return clients
}

func stdioSpec(name string) MCPServerSpec {
	return MCPServerSpec{Name: name, Transport: "stdio", Command: "fake"}
}

func maxToolsPtr(v int) *int { return &v }

func serverToolNames(registry *ToolRegistry, server string) []string {
	names := make([]string, 0)
	for name := range registry.GetServerToolNames(server) {
		names = append(names, name)
	}
	return names
}

// Parity: core/tests/test_mcp_registry_loader.py::test_registry_first_wins_collisions
func TestRegistryFirstWinsCollisions(t *testing.T) {
	registry := NewToolRegistry()
	registry.newMCPClient = fakeFactory(loaderClients(map[string][]string{
		"s1": {"tool_common", "tool_hive"},
		"s2": {"tool_common", "tool_coder"},
	}))

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("s1"), stdioSpec("s2")},
		LoadRegistryOptions{
			LogSummary:            boolPtr(false),
			PreserveExistingTools: boolPtr(true),
			LogCollisions:         true,
		})

	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	for _, name := range []string{"tool_common", "tool_hive", "tool_coder"} {
		if !registry.HasTool(name) {
			t.Fatalf("tool %q missing", name)
		}
	}
	hasNames(t, registry.GetServerToolNames("s1"), "tool_common", "tool_hive")
	hasNames(t, registry.GetServerToolNames("s2"), "tool_coder")
	if _, ok := registry.GetServerToolNames("s2")["tool_common"]; ok {
		t.Fatalf("s2 owns tool_common: %v", serverToolNames(registry, "s2"))
	}
}

// Parity: core/tests/test_mcp_registry_loader.py::test_registry_precedence_over_existing_mcp_servers
func TestRegistryPrecedenceOverExistingMCPServers(t *testing.T) {
	registry := NewToolRegistry()
	registry.newMCPClient = fakeFactory(loaderClients(map[string][]string{
		"pre": {"tool_common", "tool_pre"},
		"s1":  {"tool_common", "tool_hive"},
		"s2":  {"tool_common", "tool_coder"},
	}))

	registry.RegisterMCPServer(stdioSpec("pre"), RegisterMCPServerOptions{})

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("s1"), stdioSpec("s2")},
		LoadRegistryOptions{
			LogSummary:            boolPtr(false),
			PreserveExistingTools: boolPtr(true),
			LogCollisions:         true,
		})

	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	hasNames(t, registry.GetServerToolNames("pre"), "tool_common", "tool_pre")
	hasNames(t, registry.GetServerToolNames("s1"), "tool_hive")
	hasNames(t, registry.GetServerToolNames("s2"), "tool_coder")
	if _, ok := registry.GetServerToolNames("s1")["tool_common"]; ok {
		t.Fatalf("s1 owns tool_common: %v", serverToolNames(registry, "s1"))
	}
}

// Parity: core/tests/test_mcp_registry_loader.py::test_registry_max_tools_cap
func TestRegistryMaxToolsCap(t *testing.T) {
	registry := NewToolRegistry()
	registry.newMCPClient = fakeFactory(loaderClients(map[string][]string{
		"s1": {"tool_a", "tool_b"},
		"s2": {"tool_c"},
	}))

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("s1"), stdioSpec("s2")},
		LoadRegistryOptions{
			LogSummary:            boolPtr(false),
			PreserveExistingTools: boolPtr(true),
			MaxTools:              maxToolsPtr(2),
		})

	if !registry.HasTool("tool_a") || !registry.HasTool("tool_b") {
		t.Fatalf("s1 tools not loaded: %v", serverToolNames(registry, "s1"))
	}
	if registry.HasTool("tool_c") {
		t.Fatalf("tool_c loaded past max_tools: %v", serverToolNames(registry, "s2"))
	}
	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	if results[0].Status != "loaded" || results[0].ToolsLoaded != 2 {
		t.Fatalf("s1 result = %+v", results[0])
	}
	if results[1].Status != "skipped" || results[1].ToolsLoaded != 0 {
		t.Fatalf("s2 result = %+v", results[1])
	}
	if results[1].SkippedReason == nil || !strings.Contains(*results[1].SkippedReason, "max_tools=2") {
		t.Fatalf("s2 skipped_reason = %v", results[1].SkippedReason)
	}
}

// Parity: core/framework/loader/tool_registry.py::ToolRegistry.load_mcp_config
// -- the dict form becomes [{"name": name, **cfg} for name, cfg in
// config.items()], so file order decides cross-server first-wins.
func TestLoadMCPConfigDictFormPreservesFileOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp_servers.json")
	raw := `{"zeta":{"transport":"stdio","command":"fake"},` +
		`"alpha":{"transport":"stdio","command":"fake"}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	registry := NewToolRegistry()
	registry.newMCPClient = fakeFactory(loaderClients(map[string][]string{
		"zeta":  {"tool_common", "tool_zeta"},
		"alpha": {"tool_common", "tool_alpha"},
	}))
	registry.LoadMCPConfig(path)

	hasNames(t, registry.GetServerToolNames("zeta"), "tool_common", "tool_zeta")
	hasNames(t, registry.GetServerToolNames("alpha"), "tool_alpha")
	if _, ok := registry.GetServerToolNames("alpha")["tool_common"]; ok {
		t.Fatalf("alpha owns tool_common; zeta must win because it appears first: %v", serverToolNames(registry, "alpha"))
	}
}

package tools

import (
	"context"
	"strings"
	"testing"
)

// This file pins the D3 option-A compatibility layer: a caller that uses the
// server-side (Python-style) bare MCP tool name is routed to the namespaced
// registry key, while the canonical prefixed name keeps winning whenever it is
// an exact match. The design and rollout notes live in
// go_hive/docs/mcp-alias-prep.md; the naming scheme itself is frozen in
// go_hive/docs/architecture/contracts.md (tool-schema.mcp-namespace).

func executeToolCall(t *testing.T, registry *ToolRegistry, name string) ToolResult {
	t.Helper()
	result, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID:    "call_" + name,
		Name:  name,
		Input: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute(%q) returned error: %v", name, err)
	}
	return result
}

// TestMCPBareNameAliasResolvesPrefixedTool pins the basic alias: the bare
// server-side name resolves to mcp__<server>__<tool> and the executor still
// sends the remote (unprefixed) name to the server.
//
// Parity-exempt: the namespace prefix and this alias fallback are Go-side
// integration decisions; Python registers the bare name with no lookup layer.
func TestMCPBareNameAliasResolvesPrefixedTool(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)
	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)

	if got := executeToolCall(t, registry, "echo_text").Content; got != "srv:echo_text" {
		t.Fatalf("bare call = %q, want srv:echo_text", got)
	}
	if got := executeToolCall(t, registry, "mcp__srv__echo_text").Content; got != "srv:echo_text" {
		t.Fatalf("prefixed call = %q, want srv:echo_text", got)
	}
	if len(clients["srv"].calls) != 2 || clients["srv"].calls[0] != "echo_text" || clients["srv"].calls[1] != "echo_text" {
		t.Fatalf("remote calls = %v, want [echo_text echo_text]", clients["srv"].calls)
	}
}

// TestMCPBareNameAliasFoldConflictUsesFirstWins pins the a-b vs a_b case:
// servers "a-b" (get-sum) and "a_b" (get_sum) fold to one registry key, so the
// first server's tool owns it. Both legacy bare names resolve to that key and
// the existing first-wins rule decides which remote tool executes.
//
// Parity-exempt: Python keeps two distinct bare keys (get-sum/get_sum); the
// fold and its alias fallback are Go extensions. Residual R1 in
// go_hive/docs/mcp-namespace-residuals.md records the folded-away tool.
func TestMCPBareNameAliasFoldConflictUsesFirstWins(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"a-b": {config: &MCPServerConfig{Name: "a-b"}, tools: []MCPTool{scriptedTool("get-sum")}},
		"a_b": {config: &MCPServerConfig{Name: "a_b"}, tools: []MCPTool{scriptedTool("get_sum")}},
	}
	registry := scriptedRegistry(clients)
	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("a-b"), stdioSpec("a_b")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 2 || results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 0 {
		t.Fatalf("results = %+v, want a-b loaded and a_b folded away", results)
	}

	wantTarget := "mcp__a_b__get_sum"
	for _, bare := range []string{"get-sum", "get_sum"} {
		if got := registry.mcpAliases[bare]; got != wantTarget {
			t.Fatalf("alias[%q] = %q, want %q", bare, got, wantTarget)
		}
		if got := executeToolCall(t, registry, bare).Content; got != "a-b:get-sum" {
			t.Fatalf("bare call %q = %q, want a-b:get-sum (first server wins)", bare, got)
		}
	}
	if got := executeToolCall(t, registry, wantTarget).Content; got != "a-b:get-sum" {
		t.Fatalf("prefixed call = %q, want a-b:get-sum", got)
	}
}

// TestMCPBareNameAliasSameServerFold pins the same-server fold: one server
// exposes x-y then x_y, which collapse to one registry key. Both legacy bare
// names resolve to the surviving key, whose executor belongs to the first tool.
//
// Parity-exempt: Python keeps x-y and x_y as two distinct callable tools;
// the fold is a Go extension. Residual R1.
func TestMCPBareNameAliasSameServerFold(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"fold": {config: &MCPServerConfig{Name: "fold"}, tools: []MCPTool{scriptedTool("x-y"), scriptedTool("x_y")}},
	}
	registry := scriptedRegistry(clients)
	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("fold")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 1 || results[0].ToolsLoaded != 1 {
		t.Fatalf("results = %+v, want one folded tool", results)
	}

	for _, bare := range []string{"x-y", "x_y"} {
		if got := registry.mcpAliases[bare]; got != "mcp__fold__x_y" {
			t.Fatalf("alias[%q] = %q, want mcp__fold__x_y", bare, got)
		}
		if got := executeToolCall(t, registry, bare).Content; got != "fold:x-y" {
			t.Fatalf("bare call %q = %q, want fold:x-y (first tool wins)", bare, got)
		}
	}
}

// TestMCPBareNameAliasSameRemoteNameFirstWinsAcrossServers pins the one-way
// rule: when two servers expose the same remote name, the bare alias belongs to
// the server registered first, while the later server stays reachable by its
// own prefixed name.
//
// Parity-exempt: Python would drop the later bare key entirely; Go keeps both
// prefixed keys and resolves the ambiguous bare name by first-wins. D3 option A.
func TestMCPBareNameAliasSameRemoteNameFirstWinsAcrossServers(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"s1": {config: &MCPServerConfig{Name: "s1"}, tools: []MCPTool{scriptedTool("echo_text")}},
		"s2": {config: &MCPServerConfig{Name: "s2"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)
	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("s1"), stdioSpec("s2")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 2 || results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 1 {
		t.Fatalf("results = %+v, want both servers loaded under distinct prefixes", results)
	}

	if got := registry.mcpAliases["echo_text"]; got != "mcp__s1__echo_text" {
		t.Fatalf("alias[echo_text] = %q, want the first server's key", got)
	}
	if got := executeToolCall(t, registry, "echo_text").Content; got != "s1:echo_text" {
		t.Fatalf("bare call = %q, want s1:echo_text", got)
	}
	if got := executeToolCall(t, registry, "mcp__s2__echo_text").Content; got != "s2:echo_text" {
		t.Fatalf("prefixed call = %q, want s2:echo_text (reverse direction unaffected)", got)
	}
}

// TestMCPBareNameAliasDoesNotShadowBuiltin pins the precedence rule: an exact
// registered name always wins, so a remote tool's bare name can never take over
// a builtin even though the alias exists.
//
// Parity-exempt: this is the namespace guarantee (docs/architecture/contracts.md
// tool-schema.mcp-namespace) applied to the new fallback.
func TestMCPBareNameAliasDoesNotShadowBuiltin(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterFunction("echo_text", "builtin echo", nil, func(map[string]any) (any, error) {
		return "builtin", nil
	})
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient { return clients[config.Name] }
	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)

	if got := executeToolCall(t, registry, "echo_text").Content; got != "builtin" {
		t.Fatalf("bare call = %q, want the builtin executor", got)
	}
	if got := executeToolCall(t, registry, "mcp__srv__echo_text").Content; got != "srv:echo_text" {
		t.Fatalf("prefixed call = %q, want srv:echo_text", got)
	}
}

// TestMCPBareNameAliasLongName pins that the alias keeps the full long remote
// name: a 70-char tool name resolves bare and the registered key is unchanged
// (no truncation is introduced by the alias layer).
//
// Parity-exempt: Python keeps the 70-char bare key; Go keeps
// mcp__<server>__<tool> at 79 chars (residual R2). D3 option A adds lookup only.
func TestMCPBareNameAliasLongName(t *testing.T) {
	long := strings.Repeat("t", 70)
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool(long)}},
	}
	registry := scriptedRegistry(clients)
	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)

	registered := NamespacedMCPToolName(MCPToolNamespace, "srv", long)
	if registered != "mcp__srv__"+long || len(registered) != len("mcp__srv__")+70 {
		t.Fatalf("registered = %q (len %d), want the untruncated prefixed name", registered, len(registered))
	}
	if got := executeToolCall(t, registry, long).Content; got != "srv:"+long {
		t.Fatalf("bare call = %q, want srv:<long name>", got)
	}
	if got := registry.mcpAliases[long]; got != registered {
		t.Fatalf("alias[long] = %q, want %q", got, registered)
	}
	if got := executeToolCall(t, registry, registered).Content; got != "srv:"+long {
		t.Fatalf("prefixed call = %q, want srv:<long name>", got)
	}
}

// TestMCPBareNameAliasUnknownNameStaysUnknown pins the fallback boundary: an
// unresolvable bare name still returns the ordinary unknown-tool error, and a
// name that merely folds similarly to a live tool is not invented by guessing.
//
// Parity-exempt: no upstream behavior to compare; this is the negative case of
// the Go-side alias layer (D3 option A).
func TestMCPBareNameAliasUnknownNameStaysUnknown(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)
	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)

	for _, name := range []string{"nope", "echo-text"} {
		result := executeToolCall(t, registry, name)
		if !result.IsError {
			t.Fatalf("call %q was not an error result: %+v", name, result)
		}
		if !strings.Contains(result.Content, "Unknown tool: "+name) {
			t.Fatalf("call %q error = %q, want Unknown tool", name, result.Content)
		}
	}
	if _, exists := registry.mcpAliases["echo-text"]; exists {
		t.Fatal("alias layer invented echo-text from a fold-equivalent live name")
	}
}

// TestMCPBareNameAliasClearedOnServerDrop pins teardown: when MCP servers are
// dropped for a reload, the alias index is emptied with them so a bare name
// cannot outlive the tool it pointed at.
//
// Parity-exempt: no upstream reload alias layer exists; this is the cleanup
// side of the Go extension.
func TestMCPBareNameAliasClearedOnServerDrop(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)
	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if got := executeToolCall(t, registry, "echo_text").Content; got != "srv:echo_text" {
		t.Fatalf("bare call before drop = %q, want srv:echo_text", got)
	}

	registry.dropMCPServers("test")

	if len(registry.mcpAliases) != 0 {
		t.Fatalf("aliases after drop = %v, want empty", registry.mcpAliases)
	}
	result := executeToolCall(t, registry, "echo_text")
	if !result.IsError || !strings.Contains(result.Content, "Unknown tool: echo_text") {
		t.Fatalf("bare call after drop = %+v, want unknown-tool error", result)
	}
}

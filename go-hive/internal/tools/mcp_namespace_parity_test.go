package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// This file pins the MCP namespace / long-name / content-block semantics that
// the Python reference run established. The comparison table and the residuals
// it leaves open live in go_hive/docs/mcp-namespace-residuals.md; the Python
// probe and its captured output live in
// go_hive/docs/e2e-assets/mcp-namespace/.

// scriptedMCPClient is fakeMCPClient with per-call results: CallTool pops the
// next scripted value (falling back to "<server>:<tool>") so a test can drive
// the executor with the result shape a specific transport produces.
type scriptedMCPClient struct {
	config  *MCPServerConfig
	tools   []MCPTool
	results []any
	calls   []string
}

func (c *scriptedMCPClient) Connect() error    { return nil }
func (c *scriptedMCPClient) Disconnect() error { return nil }
func (c *scriptedMCPClient) ListTools() []MCPTool {
	return append([]MCPTool(nil), c.tools...)
}
func (c *scriptedMCPClient) CallTool(name string, arguments map[string]any) (any, error) {
	c.calls = append(c.calls, name)
	if len(c.results) == 0 {
		return c.config.Name + ":" + name, nil
	}
	result := c.results[0]
	c.results = c.results[1:]
	return result, nil
}
func (c *scriptedMCPClient) Config() *MCPServerConfig { return c.config }

func scriptedTool(name string) MCPTool {
	return MCPTool{Name: name, Description: name, InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{}, "required": []any{},
	}}
}

func scriptedRegistry(clients map[string]*scriptedMCPClient) *ToolRegistry {
	registry := NewToolRegistry()
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient { return clients[config.Name] }
	registry.sleepFn = func(time.Duration) {}
	return registry
}

// TestMCPNamespaceFoldCollisionKeepsFirstTool pins the opt-in prefix mode's
// non-injective fold: a server exposing both "x-y" and "x_y" folds both to one
// registry key, so only the first tool is callable and ToolsLoaded undercounts
// the server's list.
//
// Parity-exempt: the prefix is a Go-side integration switch (mcp.namespace).
// The default bare mode keeps both names, matching Python, which keys MCP
// tools by their raw name (core/framework/loader/tool_registry.py::
// register_mcp_server) — see TestMCPBareDefaultKeepsDistinctToolNames and
// probe collision_tool_names_in_one_server in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json. Prefix mode is
// documented as the residual R1 behaviour in
// go_hive/docs/mcp-namespace-residuals.md.
func TestMCPNamespaceFoldCollisionKeepsFirstTool(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"fold": {
			config: &MCPServerConfig{Name: "fold"},
			tools:  []MCPTool{scriptedTool("x-y"), scriptedTool("x_y")},
		},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("fold")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 1 || results[0].Status != "loaded" {
		t.Fatalf("results = %+v", results)
	}
	if results[0].ToolsLoaded != 1 {
		t.Fatalf("ToolsLoaded = %d, want 1 (fold collapses x-y and x_y)", results[0].ToolsLoaded)
	}
	if !registry.HasTool("mcp__fold__x_y") {
		t.Fatalf("folded tool missing: %v", registry.RegisteredNames())
	}
	if got := len(registry.GetServerToolNames("fold")); got != 1 {
		t.Fatalf("server tool count = %d, want 1", got)
	}

	result, _ := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_1", Name: "mcp__fold__x_y", Input: map[string]any{},
	})
	if result.Content != "fold:x-y" {
		t.Fatalf("folded tool called %q, want the first remote tool fold:x-y", result.Content)
	}
}

// TestMCPNamespacePrefixFoldCollidesAcrossServers pins the prefix mode's
// cross-server case: servers "a-b" and "a_b" produce the same prefix, so tools
// that only differ by '-' vs '_' fold to one registry key; the first server
// wins.
//
// Parity-exempt: Python has no prefix fold and keeps "get-sum" and "get_sum"
// as distinct keys on two distinct servers (probe collision_cross_server in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json). The default bare
// mode reproduces that — TestMCPBareDefaultCrossServerNamesSurvive.
func TestMCPNamespacePrefixFoldCollidesAcrossServers(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"a-b": {config: &MCPServerConfig{Name: "a-b"}, tools: []MCPTool{scriptedTool("get-sum")}},
		"a_b": {config: &MCPServerConfig{Name: "a_b"}, tools: []MCPTool{scriptedTool("get_sum")}},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("a-b"), stdioSpec("a_b")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 0 {
		t.Fatalf("loaded counts = %d, %d; want 1, 0 (both fold to mcp__a_b__get_sum)",
			results[0].ToolsLoaded, results[1].ToolsLoaded)
	}
	if len(registry.GetServerToolNames("a_b")) != 0 {
		t.Fatalf("a_b tools = %v, want none (shadowed by a-b)", registry.GetServerToolNames("a_b"))
	}

	result, _ := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_1", Name: "mcp__a_b__get_sum", Input: map[string]any{},
	})
	if result.Content != "a-b:get-sum" {
		t.Fatalf("call = %q, want the first server's remote tool a-b:get-sum", result.Content)
	}
}

// TestMCPNamespaceSameRemoteNameFirstWins pins the aligned half of the
// collision story in prefix mode: when two servers expose the same remote tool
// name, the first one wins and the later server registers nothing under it.
//
// Parity: core/tests/test_mcp_registry_loader.py::test_registry_first_wins_collisions
// — Python keys the bare name, so the later server's identical tool is skipped;
// prefix mode reaches the same outcome through the namespaced key (probe
// collision_cross_server in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json). The default bare
// mode is directly identical (TestMCPBareDefaultSameRemoteNameFirstWins).
func TestMCPNamespaceSameRemoteNameFirstWins(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"a-b": {config: &MCPServerConfig{Name: "a-b"}, tools: []MCPTool{scriptedTool("echo_text")}},
		"a_b": {config: &MCPServerConfig{Name: "a_b"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("a-b"), stdioSpec("a_b")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 0 {
		t.Fatalf("loaded counts = %d, %d; want 1, 0 (same remote name shadows)", results[0].ToolsLoaded, results[1].ToolsLoaded)
	}
	if len(registry.GetServerToolNames("a_b")) != 0 {
		t.Fatalf("a_b tools = %v, want none", registry.GetServerToolNames("a_b"))
	}

	result, _ := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_1", Name: "mcp__a_b__echo_text", Input: map[string]any{},
	})
	if result.Content != "a-b:echo_text" {
		t.Fatalf("call = %q, want a-b:echo_text", result.Content)
	}
}

// TestMCPNamespaceFoldOverwriteWhenNotPreserving pins the prefix mode path with
// preserve=false: the later tool overwrites the folded registry key, so the
// surviving executor calls the later server's remote tool (both servers still
// report the load).
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server with
// preserve_existing_tools=False overwrites the earlier bare-name registration;
// probe collision_call_echo_text_preserve_false == "a_b:echo_text" in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json. The default bare
// mode behaves identically (TestMCPBareDefaultPreserveFalseOverwrites).
func TestMCPNamespaceFoldOverwriteWhenNotPreserving(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"a-b": {config: &MCPServerConfig{Name: "a-b"}, tools: []MCPTool{scriptedTool("echo_text")}},
		"a_b": {config: &MCPServerConfig{Name: "a_b"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("a-b"), stdioSpec("a_b")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(false), ToolNamespace: "mcp"},
	)
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 1 {
		t.Fatalf("loaded counts = %d, %d; want 1, 1", results[0].ToolsLoaded, results[1].ToolsLoaded)
	}
	if names := registry.RegisteredNames(); len(names) != 1 || names[0] != "mcp__a_b__echo_text" {
		t.Fatalf("registered = %v", names)
	}

	result, _ := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_1", Name: "mcp__a_b__echo_text", Input: map[string]any{},
	})
	if result.Content != "a_b:echo_text" {
		t.Fatalf("call = %q, want the later server's tool a_b:echo_text", result.Content)
	}
}

// TestMCPNamespaceLongToolNameNotTruncated pins that neither side applies a
// length cap at registration in prefix mode: Python keeps the bare 70-char
// key, Go keeps the whole tool name under the prefix, so the model-visible
// name is 79 chars.
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server (keys
// mcp_tool.name verbatim) — probe long_name.registered_len == 70 in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json. The default bare
// mode registers the same 70 characters with no prefix
// (TestMCPBareDefaultLongNameRegisteredVerbatim); the provider-side 64-char
// limit stays Antigravity-only.
func TestMCPNamespaceLongToolNameNotTruncated(t *testing.T) {
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
	if registered != "mcp__srv__"+long {
		t.Fatalf("registered = %q", registered)
	}
	if len(registered) != len("mcp__srv__")+70 {
		t.Fatalf("len = %d, want %d (no truncation)", len(registered), len("mcp__srv__")+70)
	}
	if !registry.HasTool(registered) {
		t.Fatalf("long tool missing: %v", registry.RegisteredNames())
	}
}

// TestMCPBareDefaultKeepsDistinctToolNames pins the default (bare) registration
// mode on the reference's first naming case: a server exposing "x-y" and "x_y"
// yields two distinct model-visible tools, both callable, with no folding.
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server — the
// registry key is the bare mcp_tool.name (line 1053; the converter at line 1422
// does not rename it), so "x-y" and "x_y" stay separate. Probe
// collision_tool_names_in_one_server in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPBareDefaultKeepsDistinctToolNames(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"fold": {
			config: &MCPServerConfig{Name: "fold"},
			tools:  []MCPTool{scriptedTool("x-y"), scriptedTool("x_y")},
		},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("fold")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true)},
	)
	if len(results) != 1 || results[0].ToolsLoaded != 2 {
		t.Fatalf("results = %+v, want both tools loaded", results)
	}
	if !registry.HasTool("x-y") || !registry.HasTool("x_y") {
		t.Fatalf("registered = %v, want two distinct bare names", registry.RegisteredNames())
	}
	if got := len(registry.GetServerToolNames("fold")); got != 2 {
		t.Fatalf("server tool count = %d, want 2", got)
	}
	if got := executeToolCall(t, registry, "x-y").Content; got != "fold:x-y" {
		t.Fatalf("x-y called %q, want its own remote tool", got)
	}
	if got := executeToolCall(t, registry, "x_y").Content; got != "fold:x_y" {
		t.Fatalf("x_y called %q, want its own remote tool", got)
	}
}

// TestMCPBareDefaultCrossServerNamesSurvive pins the cross-server half of the
// same reference case: "a-b"/"get-sum" and "a_b"/"get_sum" stay two distinct
// bare keys instead of folding into one prefixed key.
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server — the
// server name never enters the key, so both tools remain reachable. Probe
// collision_cross_server in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPBareDefaultCrossServerNamesSurvive(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"a-b": {config: &MCPServerConfig{Name: "a-b"}, tools: []MCPTool{scriptedTool("get-sum")}},
		"a_b": {config: &MCPServerConfig{Name: "a_b"}, tools: []MCPTool{scriptedTool("get_sum")}},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("a-b"), stdioSpec("a_b")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true)},
	)
	if len(results) != 2 || results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 1 {
		t.Fatalf("results = %+v, want 1 + 1 tools loaded", results)
	}
	if got := executeToolCall(t, registry, "get-sum").Content; got != "a-b:get-sum" {
		t.Fatalf("get-sum = %q, want a-b:get-sum", got)
	}
	if got := executeToolCall(t, registry, "get_sum").Content; got != "a_b:get_sum" {
		t.Fatalf("get_sum = %q, want a_b:get_sum", got)
	}
}

// TestMCPBareDefaultSameRemoteNameFirstWins pins Python's first-wins collision
// rule under the default mode: two servers exposing "echo_text" register one
// bare key, owned by the first server; the later server's surface stays empty.
//
// Parity: core/tests/test_mcp_registry_loader.py::test_registry_first_wins_collisions
// — Python keys the bare name, so the later server's identical tool is skipped
// (probe collision_cross_server in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json).
func TestMCPBareDefaultSameRemoteNameFirstWins(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"a-b": {config: &MCPServerConfig{Name: "a-b"}, tools: []MCPTool{scriptedTool("echo_text")}},
		"a_b": {config: &MCPServerConfig{Name: "a_b"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("a-b"), stdioSpec("a_b")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true)},
	)
	if len(results) != 2 || results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 0 {
		t.Fatalf("results = %+v, want 1 then 0 (same bare name shadows)", results)
	}
	if got := len(registry.GetServerToolNames("a_b")); got != 0 {
		t.Fatalf("a_b tools = %v, want none", registry.GetServerToolNames("a_b"))
	}
	if got := executeToolCall(t, registry, "echo_text").Content; got != "a-b:echo_text" {
		t.Fatalf("echo_text = %q, want the first server's tool", got)
	}
}

// TestMCPBareDefaultPreserveFalseOverwrites pins the preserve=false path in the
// default mode: the later server's registration replaces the earlier bare key,
// so the surviving executor calls the later remote tool.
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server with
// preserve_existing_tools=False overwrites the earlier registration; probe
// collision_call_echo_text_preserve_false == "a_b:echo_text" in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPBareDefaultPreserveFalseOverwrites(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"a-b": {config: &MCPServerConfig{Name: "a-b"}, tools: []MCPTool{scriptedTool("echo_text")}},
		"a_b": {config: &MCPServerConfig{Name: "a_b"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("a-b"), stdioSpec("a_b")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(false)},
	)
	if len(results) != 2 || results[0].ToolsLoaded != 1 || results[1].ToolsLoaded != 1 {
		t.Fatalf("results = %+v, want 1 + 1", results)
	}
	if names := registry.RegisteredNames(); len(names) != 1 || names[0] != "echo_text" {
		t.Fatalf("registered = %v, want the single bare key", names)
	}
	if got := executeToolCall(t, registry, "echo_text").Content; got != "a_b:echo_text" {
		t.Fatalf("echo_text = %q, want the later server's tool", got)
	}
}

// TestMCPBareDefaultLongNameRegisteredVerbatim pins the long-name case in the
// default mode: a 70-character tool name is the registry key, character for
// character, with no prefix and no truncation.
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server — probe
// long_name.registered_key / registered_len == 70 in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPBareDefaultLongNameRegisteredVerbatim(t *testing.T) {
	long := strings.Repeat("t", 70)
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool(long)}},
	}
	registry := scriptedRegistry(clients)

	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true)},
	)

	if !registry.HasTool(long) {
		t.Fatalf("70-char bare tool missing: %v", registry.RegisteredNames())
	}
	if got := len(registry.RegisteredNames()); got != 1 || len(registry.RegisteredNames()[0]) != 70 {
		t.Fatalf("registered = %v, want one 70-char key", registry.RegisteredNames())
	}
	if got := executeToolCall(t, registry, long).Content; got != "srv:"+long {
		t.Fatalf("long tool called %q, want srv:<70 chars>", got)
	}
}

// TestMCPBareDefaultAliasesStayIdentity pins the alias rule in the default
// mode: bare name == registry key, so the compatibility map stays empty (no
// duplicate or invented names) and a folded twin that never existed on the
// server is still unknown.
//
// Parity-exempt: mcpAliases is the Go-side D3 compatibility layer; in bare mode
// it is the identity, which is exactly Python's lookup (the registry key).
func TestMCPBareDefaultAliasesStayIdentity(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool("x-y")}},
	}
	registry := scriptedRegistry(clients)

	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true)},
	)

	if len(registry.mcpAliases) != 0 {
		t.Fatalf("aliases = %v, want none in bare mode", registry.mcpAliases)
	}
	if registry.mcpToolClients["x-y"] == nil {
		t.Fatalf("mcpToolClients = %v, want the bare key", registry.mcpToolClients)
	}
	result := executeToolCall(t, registry, "x_y")
	if !result.IsError {
		t.Fatalf("x_y resolved to %q; bare mode must not invent a fold alias", result.Content)
	}
}

// TestMCPRegistryToolNameModeMatrix pins the two naming modes in one table.
//
// Parity-exempt: the mode switch itself is Go-side (mcp.namespace); the bare
// branch reproduces core/framework/loader/tool_registry.py::register_mcp_server
// (registry key = mcp_tool.name verbatim).
func TestMCPRegistryToolNameModeMatrix(t *testing.T) {
	cases := []struct {
		namespace, server, tool, want string
	}{
		{"", "a-b", "x-y", "x-y"},
		{"", "any server", "x.y", "x.y"},
		{"", "", "read_file", "read_file"},
		{"mcp", "a-b", "x-y", "mcp__a_b__x_y"},
		{"mcp", "a_b", "get_sum", "mcp__a_b__get_sum"},
		{"remote", "custom", "ping", "remote__custom__ping"},
	}
	for _, tc := range cases {
		if got := MCPRegistryToolName(tc.namespace, tc.server, tc.tool); got != tc.want {
			t.Fatalf("MCPRegistryToolName(%q,%q,%q) = %q, want %q",
				tc.namespace, tc.server, tc.tool, got, tc.want)
		}
	}
}

// TestMCPBareDefaultProbeAndKillUseBareKeys pins the liveness/disconnect path:
// the executor's probe and kill lookups resolve the same bare key the registry
// stored, so a hung bare-name tool is still diagnosed and disconnected.
//
// Parity-exempt: the registry-owned client map is Go-side plumbing; upstream's
// MCP client has no separate probe/kill seam.
func TestMCPBareDefaultProbeAndKillUseBareKeys(t *testing.T) {
	client := &probeKillMCPClient{
		config: &MCPServerConfig{Name: "srv", Transport: "stdio"},
		tools:  []MCPTool{scriptedTool("echo_text")},
	}
	registry := NewToolRegistry()
	registry.newMCPClient = func(*MCPServerConfig) MCPClient { return client }
	registry.sleepFn = func(time.Duration) {}

	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true)},
	)
	if !registry.HasTool("echo_text") {
		t.Fatalf("bare tool missing: %v", registry.RegisteredNames())
	}
	if got := registry.GetExecutor().ProbeForTool("echo_text"); got != "alive" {
		t.Fatalf("ProbeForTool = %q, want alive", got)
	}
	if client.probes != 1 {
		t.Fatalf("probes = %d, want 1", client.probes)
	}
	if err := registry.GetExecutor().KillForTool("echo_text"); err != nil {
		t.Fatalf("KillForTool: %v", err)
	}
	if !client.killed {
		t.Fatal("KillForTool did not disconnect the bare-name tool's client")
	}
}

// probeKillMCPClient is a scripted client that also answers the optional
// liveness interface ProbeForTool uses.
type probeKillMCPClient struct {
	config *MCPServerConfig
	tools  []MCPTool
	probes int
	killed bool
}

func (c *probeKillMCPClient) Connect() error    { return nil }
func (c *probeKillMCPClient) Disconnect() error { c.killed = true; return nil }
func (c *probeKillMCPClient) ListTools() []MCPTool {
	return append([]MCPTool(nil), c.tools...)
}
func (c *probeKillMCPClient) CallTool(name string, _ map[string]any) (any, error) {
	return c.config.Name + ":" + name, nil
}
func (c *probeKillMCPClient) Config() *MCPServerConfig { return c.config }
func (c *probeKillMCPClient) ProbeLiveness() bool      { c.probes++; return true }
func (c *probeKillMCPClient) IsConnected() bool        { return true }

// TestMCPNamespacePrefixModeAliasFallbackStillResolves pins the compatibility
// half of the mode switch: when mcp.namespace is non-empty the registry key
// stays mcp__<ns>__<server>__<tool>, and the legacy bare name still resolves to
// it through mcpAliases.
//
// Parity-exempt: the prefix mode and its alias fallback are the Go-side D3
// compatibility layer; the default bare mode is the Python-identical path.
func TestMCPNamespacePrefixModeAliasFallbackStillResolves(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"srv": {config: &MCPServerConfig{Name: "srv"}, tools: []MCPTool{scriptedTool("echo_text")}},
	}
	registry := scriptedRegistry(clients)
	registry.LoadRegistryServers(
		[]MCPServerSpec{stdioSpec("srv")},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)

	if got := registry.mcpAliases["echo_text"]; got != "mcp__srv__echo_text" {
		t.Fatalf("alias = %q, want mcp__srv__echo_text", got)
	}
	if got := executeToolCall(t, registry, "mcp__srv__echo_text").Content; got != "srv:echo_text" {
		t.Fatalf("prefixed call = %q", got)
	}
	if got := executeToolCall(t, registry, "echo_text").Content; got != "srv:echo_text" {
		t.Fatalf("bare fallback = %q, want the prefixed tool's result", got)
	}
}

// TestMCPSplitContentDataOnlyItemBecomesText pins the data-without-mimeType
// branch: the raw data string is forwarded as text.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content — data-only
// item -> str(item.data); probe split.data_only == "raw-blob" in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPSplitContentDataOnlyItemBecomesText(t *testing.T) {
	text, images := SplitMCPContent([]any{map[string]any{"data": "raw-blob"}})
	if text != "raw-blob" {
		t.Fatalf("text = %q", text)
	}
	if len(images) != 0 {
		t.Fatalf("images = %v", images)
	}
}

// TestMCPSplitContentEmptyMimeTypeStaysImageURL pins that an empty mimeType
// still produces an image_url block with an empty media type, not a text part.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content — an item
// with data + mimeType "" -> "data:;base64,AAA"; probe split.empty_mime in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPSplitContentEmptyMimeTypeStaysImageURL(t *testing.T) {
	text, images := SplitMCPContent([]any{mcpImageItem("AAA", "", nil)})
	if text != "" {
		t.Fatalf("text = %q", text)
	}
	if len(images) != 1 {
		t.Fatalf("images = %#v", images)
	}
	imageURL, _ := images[0]["image_url"].(map[string]any)
	if imageURL["url"] != "data:;base64,AAA" {
		t.Fatalf("url = %v", imageURL["url"])
	}
}

// TestMCPSplitContentNullMimeTypeMatchesPython pins the null-mimeType branch:
// keying on field presence (Python's hasattr) rather than a string type check
// keeps {data, mimeType: null} on the image branch, and the URL renders the
// null the way Python's f-string does.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content —
// {data: "AAA", mimeType: None} -> image_url "data:None;base64,AAA"; probe
// split.null_mime in go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPSplitContentNullMimeTypeMatchesPython(t *testing.T) {
	text, images := SplitMCPContent([]any{map[string]any{"data": "AAA", "mimeType": nil}})
	if text != "" {
		t.Fatalf("text = %q, want no text for a data+mimeType block", text)
	}
	if len(images) != 1 {
		t.Fatalf("images = %#v, want the image branch", images)
	}
	imageURL, _ := images[0]["image_url"].(map[string]any)
	if got := imageURL["url"]; got != "data:None;base64,AAA" {
		t.Fatalf("url = %v, want data:None;base64,AAA (Python f-string spelling)", got)
	}
}

// TestMCPSplitContentMimeTypeAliasNotRecognised is the negative regression for
// R4: Python only reads the SDK's camelCase mimeType field, so a snake_case
// `mime_type` must not turn a data block into image content. The block falls
// through to the data-only text branch.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content (reads the
// mimeType field only; there is no snake_case alias).
func TestMCPSplitContentMimeTypeAliasNotRecognised(t *testing.T) {
	text, images := SplitMCPContent([]any{map[string]any{
		"data": "AAA", "mime_type": "image/png",
	}})
	if text != "AAA" {
		t.Fatalf("text = %q, want the data-only fallback", text)
	}
	if len(images) != 0 {
		t.Fatalf("images = %#v, want none (mime_type is not an alias for mimeType)", images)
	}
}

// TestMCPSplitContentNonStringDataUsesPythonStr pins the type handling for a
// data field that is not a string: Python's data-only branch appends
// str(item.data), so numbers/bools/null keep their Python spellings instead of
// being dropped by a Go string assertion.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content (data-only
// branch appends str(item.data)).
func TestMCPSplitContentNonStringDataUsesPythonStr(t *testing.T) {
	for _, tc := range []struct {
		name string
		item map[string]any
		want string
	}{
		{"number", map[string]any{"data": float64(42)}, "42"},
		{"large number", map[string]any{"data": float64(1_000_000)}, "1000000"},
		{"decimal", map[string]any{"data": 1.5}, "1.5"},
		{"bool", map[string]any{"data": true}, "True"},
		{"null", map[string]any{"data": nil}, "None"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, images := SplitMCPContent([]any{tc.item})
			if text != tc.want || len(images) != 0 {
				t.Fatalf("text = %q images = %#v, want text %q", text, images, tc.want)
			}
		})
	}
}

// TestMCPSplitContentResourceLinkItemDropped pins the shared blind spot: a
// standard resource_link item has neither text nor data, so both sides drop it.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content — item with
// only uri/name/mimeType -> skipped; probe split.resource_link_only in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json. Shared gap
// recorded in go_hive/docs/mcp-crosscheck.md §8.3 item 2.
func TestMCPSplitContentResourceLinkItemDropped(t *testing.T) {
	text, images := SplitMCPContent([]any{map[string]any{
		"type": "resource_link", "uri": "https://example.test/x", "name": "x", "mimeType": "text/plain",
	}})
	if text != "" || len(images) != 0 {
		t.Fatalf("text = %q images = %#v, want both empty (resource_link dropped)", text, images)
	}
}

// TestMCPSplitContentResourceLinkExtraFieldsMatchPydantic pins what "drop
// resource_link" means at the field level: the SDK models allow extra fields
// (ConfigDict(extra="allow")), so an extra text field is forwarded and an extra
// data field takes the image branch. Go's key-presence checks reproduce that —
// it is not a type-based special case for resource_link.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content with
// mcp/types.py Resource/ResourceLink (extra="allow"); verified against the
// installed mcp 1.26.0 objects, not assumed.
func TestMCPSplitContentResourceLinkExtraFieldsMatchPydantic(t *testing.T) {
	link := map[string]any{
		"type": "resource_link", "uri": "https://example.test/x", "name": "x", "mimeType": "text/plain",
	}
	link["text"] = "from-extra"
	text, images := SplitMCPContent([]any{link})
	if text != "from-extra" || len(images) != 0 {
		t.Fatalf("text = %q images = %#v, want the extra text field forwarded", text, images)
	}

	withData := map[string]any{
		"type": "resource_link", "uri": "https://example.test/x", "name": "x", "mimeType": "image/png",
	}
	withData["data"] = "AAA"
	text, images = SplitMCPContent([]any{withData})
	if text != "" || len(images) != 1 {
		t.Fatalf("text = %q images = %#v, want the image branch", text, images)
	}
	imageURL, _ := images[0]["image_url"].(map[string]any)
	if got := imageURL["url"]; got != "data:image/png;base64,AAA" {
		t.Fatalf("url = %v", got)
	}
}

// TestMCPSplitContentUnknownItemDropped pins that an item matching neither the
// text nor the data branch is skipped, not serialized.
//
// Parity: core/framework/loader/mcp_client.py::_split_mcp_content — an object
// with no text/data attribute -> skipped; probe split.unknown_object in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPSplitContentUnknownItemDropped(t *testing.T) {
	text, images := SplitMCPContent([]any{map[string]any{"foo": "bar"}})
	if text != "" || len(images) != 0 {
		t.Fatalf("text = %q images = %#v, want both empty (unknown item dropped)", text, images)
	}
}

// TestMCPExecutorLegacyContentListFirstTextBlock pins the HTTP/unix path: the
// client returns the raw content array, and the executor keeps the first
// block's text.
//
// Parity: core/framework/loader/tool_registry.py::make_mcp_executor — the
// legacy list branch returns result[0]["text"]; probe model_visible.http-like
// == "a" in go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPExecutorLegacyContentListFirstTextBlock(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"http": {
			config: &MCPServerConfig{Name: "http", Transport: "http"},
			tools:  []MCPTool{scriptedTool("echo")},
			results: []any{[]any{
				map[string]any{"type": "text", "text": "a"},
				map[string]any{"type": "text", "text": "b"},
			}},
		},
	}
	registry := scriptedRegistry(clients)
	registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "http", Transport: "http", URL: "http://fake"}},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)

	result, _ := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_1", Name: "mcp__http__echo", Input: map[string]any{},
	})
	if result.Content != "a" {
		t.Fatalf("content = %q, want first block text", result.Content)
	}
}

// TestMCPExecutorLegacyContentListEmptyAndNil pins the empty-list and nil
// cases: Python serializes them as "[]" and "null" respectively; Go's
// PythonJSONDumps produces the same bytes.
//
// Parity: core/framework/loader/tool_registry.py::_wrap_result — probe
// model_visible.http-empty == "[]" and http-none == "null" in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
func TestMCPExecutorLegacyContentListEmptyAndNil(t *testing.T) {
	cases := []struct {
		name   string
		result any
		want   string
	}{
		{"empty", []any{}, "[]"},
		{"nil", nil, "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clients := map[string]*scriptedMCPClient{
				"http": {
					config:  &MCPServerConfig{Name: "http", Transport: "http"},
					tools:   []MCPTool{scriptedTool("echo")},
					results: []any{tc.result},
				},
			}
			registry := scriptedRegistry(clients)
			registry.LoadRegistryServers(
				[]MCPServerSpec{{Name: "http", Transport: "http", URL: "http://fake"}},
				LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
			)
			result, _ := registry.GetExecutor().Execute(context.Background(), ToolUse{
				ID: "call_1", Name: "mcp__http__echo", Input: map[string]any{},
			})
			if result.Content != tc.want {
				t.Fatalf("content = %q, want %q", result.Content, tc.want)
			}
		})
	}
}

// TestMCPExecutorLegacyContentListFirstNonTextBlock pins the fallback: when the
// first block has no "text", the executor returns that block, which
// _wrap_result serializes as a JSON object.
//
// Parity: core/framework/loader/tool_registry.py::make_mcp_executor — returns
// result[0] when it has no text; probe model_visible.http-image-first in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json. Shape and key
// order are equal on both sides: the HTTP transport re-decodes the first
// non-text block in server order (mcp_content_order.go,
// TestMCPHTTPFirstNonTextContentBlockPreservesKeyOrder). This scripted client
// bypasses that decode, so the assertion here stays key-insensitive.
func TestMCPExecutorLegacyContentListFirstNonTextBlock(t *testing.T) {
	clients := map[string]*scriptedMCPClient{
		"http": {
			config: &MCPServerConfig{Name: "http", Transport: "http"},
			tools:  []MCPTool{scriptedTool("echo")},
			results: []any{[]any{
				map[string]any{"type": "image", "data": "AAA", "mimeType": "image/png"},
				map[string]any{"type": "text", "text": "caption"},
			}},
		},
	}
	registry := scriptedRegistry(clients)
	registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "http", Transport: "http", URL: "http://fake"}},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)

	result, _ := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_1", Name: "mcp__http__echo", Input: map[string]any{},
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("content %q is not a JSON object: %v", result.Content, err)
	}
	want := map[string]any{"type": "image", "data": "AAA", "mimeType": "image/png"}
	if len(payload) != len(want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
	for key, value := range want {
		if payload[key] != value {
			t.Fatalf("payload = %#v, want %#v", payload, want)
		}
	}
}

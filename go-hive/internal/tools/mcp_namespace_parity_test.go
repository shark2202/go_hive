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

// TestMCPNamespaceFoldCollisionKeepsFirstTool pins the non-injective fold: a
// server exposing both "x-y" and "x_y" folds both to one registry key, so only
// the first tool is callable and ToolsLoaded undercounts the server's list.
//
// Parity-exempt: the namespace prefix is a Go-side integration decision.
// Python keys MCP tools by their raw name (core/framework/loader/
// tool_registry.py::register_mcp_server), so "x-y" and "x_y" stay two distinct
// callable tools there (probe go_hive/docs/e2e-assets/mcp-namespace/
// python-output.json: collision_tool_names_in_one_server). Registered as
// residual R1 in go_hive/docs/mcp-namespace-residuals.md.
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

// TestMCPNamespacePrefixFoldCollidesAcrossServers pins the cross-server case:
// servers "a-b" and "a_b" produce the same prefix, so tools that only differ by
// '-' vs '_' fold to one registry key; the first server wins.
//
// Parity-exempt: Python has no prefix fold and keeps "get-sum" and "get_sum"
// as distinct keys on two distinct servers (probe:
// collision_cross_server in go_hive/docs/e2e-assets/mcp-namespace/
// python-output.json). Residual R1.
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
// collision story: when two servers expose the same remote tool name, the
// first one wins and the later server registers nothing under it.
//
// Parity: core/tests/test_mcp_registry_loader.py::test_registry_first_wins_collisions
// — Python keys the bare name, so the later server's identical tool is skipped;
// the Go folded key reproduces the same outcome (probe collision_cross_server
// in go_hive/docs/e2e-assets/mcp-namespace/python-output.json).
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

// TestMCPNamespaceFoldOverwriteWhenNotPreserving pins the preserve=false path:
// the later tool overwrites the folded registry key, so the surviving executor
// calls the later server's remote tool (both servers still report the load).
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server with
// preserve_existing_tools=False overwrites the earlier bare-name registration;
// probe collision_call_echo_text_preserve_false == "a_b:echo_text" in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json.
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
// length cap at registration: Python keeps the bare 70-char key, Go keeps the
// whole tool name under the prefix, so the model-visible name is 79 chars.
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server (keys
// mcp_tool.name verbatim) — probe long_name.registered_len == 70 in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json. Residual R2 tracks
// the provider-side 64-char limit this can exceed.
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

// TestMCPSplitContentNullMimeTypeDiverges pins the one malformed-payload
// divergence the probe found: a data item whose mimeType is null goes to the
// image branch on the Python side (hasattr check) and to the data-only text
// branch on the Go side (string type check).
//
// Parity-exempt: Python emits an unconvertible "data:None;base64,AAA" image
// URL for {data: "AAA", mimeType: null} (probe split.null_mime in
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json); Go degrades to
// text "AAA". Registered as residual R3 note in
// go_hive/docs/mcp-namespace-residuals.md.
func TestMCPSplitContentNullMimeTypeDiverges(t *testing.T) {
	text, images := SplitMCPContent([]any{map[string]any{"data": "AAA", "mimeType": nil}})
	if text != "AAA" || len(images) != 0 {
		t.Fatalf("text = %q images = %#v, want text fallback for a null mimeType", text, images)
	}
}

// TestMCPSplitContentResourceLinkItemDropped pins the shared blind spot: a
// resource_link item has neither text nor data, so both sides drop it.
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
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json. Shape (L1/L2) is
// equal on both sides; key order differs (L3, PythonJSONDumps sorts) — see
// residual R3.
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

package tools

// Parity: core/framework/loader/tool_registry.py::_wrap_result
//
// L3 closure: Python forwards an HTTP tool result's first non-text content
// block as the dict json.loads built and json.dumps renders, so the model sees
// the server's key order. The frozen probe is
// go_hive/docs/e2e-assets/mcp-namespace/python-output.json
// (model_visible.http-image-first). These tests pin the Go bytes to that probe
// and keep the plain-map sort as a negative guard.

import (
	"strings"
	"testing"
)

func httpClientForContent(t *testing.T, tools []string, body string) *httpMCPClient {
	t.Helper()
	client := newHTTPMCPClient(&MCPServerConfig{Name: "http", Transport: "http"})
	client.http = &fakeMCPHTTPClient{
		postFn: func(string, any) (*mcpHTTPResponse, error) {
			return &mcpHTTPResponse{Status: 200, Body: []byte(body)}, nil
		},
	}
	markHTTPConnected(client, tools...)
	return client
}

func executeMCPHTTPTool(t *testing.T, body, toolName string) ToolResult {
	t.Helper()
	client := httpClientForContent(t, []string{toolName}, body)
	executor := NewToolRegistry().makeMCPExecutor(client, toolName, toolName, nil)
	result, err := executor(map[string]any{})
	if err != nil {
		t.Fatalf("executor() = %v", err)
	}
	return wrapToolResult("call_1", result)
}

// TestMCPHTTPFirstNonTextContentBlockPreservesKeyOrder is the L3 closure: the
// HTTP content block keeps the server's "type", "data", "mimeType" order
// instead of PythonJSONDumps' alphabetical sort.
// Parity: core/framework/loader/tool_registry.py::_wrap_result
func TestMCPHTTPFirstNonTextContentBlockPreservesKeyOrder(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"result":{"content":[` +
		`{"type":"image","data":"AAA","mimeType":"image/png"},` +
		`{"type":"text","text":"caption"}]}}`

	result := executeMCPHTTPTool(t, body, "shot")
	want := `{"type": "image", "data": "AAA", "mimeType": "image/png"}`
	if result.Content != want {
		t.Fatalf("content = %q, want %q", result.Content, want)
	}
	if result.ImageContent != nil {
		t.Fatalf("image_content = %v, want nil (legacy HTTP list stays text)", result.ImageContent)
	}
}

// Parity: core/framework/loader/tool_registry.py::_wrap_result
//
// Mixed lists keep the same rule as Python: only content[0] reaches the model;
// a first text block yields its text string, a first non-text block yields that
// object (in server order), and later blocks are ignored.
func TestMCPHTTPMixedContentOrderReturnsFirstBlockOnly(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"result":{"content":[` +
		`{"type":"image","data":"AAA","mimeType":"image/png"},` +
		`{"type":"text","text":"caption"},` +
		`{"type":"image","data":"BBB","mimeType":"image/jpeg"}]}}`

	result := executeMCPHTTPTool(t, body, "shot")
	want := `{"type": "image", "data": "AAA", "mimeType": "image/png"}`
	if result.Content != want {
		t.Fatalf("content = %q, want %q", result.Content, want)
	}
	if strings.Contains(result.Content, "BBB") || strings.Contains(result.Content, "caption") {
		t.Fatalf("later content blocks leaked into the result: %q", result.Content)
	}
}

// TestMCPHTTPFirstNonTextContentNestedOrder pins recursive order: nested
// objects (e.g. _meta) keep their own server order and number literals.
// Parity: core/framework/loader/tool_registry.py::_wrap_result
func TestMCPHTTPFirstNonTextContentNestedOrder(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"result":{"content":[` +
		`{"type":"image","data":"AAA","mimeType":"image/png","_meta":{"filename":"a.pdf","size":3}}]}}`

	result := executeMCPHTTPTool(t, body, "shot")
	want := `{"type": "image", "data": "AAA", "mimeType": "image/png", "_meta": {"filename": "a.pdf", "size": 3}}`
	if result.Content != want {
		t.Fatalf("content = %q, want %q", result.Content, want)
	}
}

// TestMCPHTTPFirstTextContentBlockStillReturnsText is the negative guard: a
// text-first list must still return only the text, so ordering never applies.
// Parity: core/framework/loader/tool_registry.py::_wrap_result
func TestMCPHTTPFirstTextContentBlockStillReturnsText(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"result":{"content":[` +
		`{"type":"text","text":"caption"},` +
		`{"type":"image","data":"AAA","mimeType":"image/png"}]}}`

	result := executeMCPHTTPTool(t, body, "shot")
	if result.Content != "caption" {
		t.Fatalf("content = %q, want caption", result.Content)
	}
}

// TestMCPPythonJSONDumpsStillSortsPlainMaps is the scoping guard: only the
// HTTP first-non-text block carries order; every ordinary map keeps the
// existing alphabetical PythonJSONDumps rendering.
// Parity-exempt: Go-side ordered-decode/serialization guard; the Python probe
// (docs/e2e-assets/mcp-namespace/python-output.json) is not a pytest target.
func TestMCPPythonJSONDumpsStillSortsPlainMaps(t *testing.T) {
	got := PythonJSONDumps(map[string]any{"b": 2, "a": 1})
	want := `{"a": 1, "b": 2}`
	if got != want {
		t.Fatalf("PythonJSONDumps() = %q, want %q", got, want)
	}
}

// TestMCPDecodeOrderedJSONKeepsFirstSlotForDuplicateKeys matches Python's
// json.loads: a repeated key keeps its first position and the last value.
// Parity-exempt: Go-side ordered-decode/serialization guard; the Python probe
// (docs/e2e-assets/mcp-namespace/python-output.json) is not a pytest target.
func TestMCPDecodeOrderedJSONKeepsFirstSlotForDuplicateKeys(t *testing.T) {
	decoded, err := decodeMCPOrderedJSON([]byte(`{"a":1,"b":2,"a":3,"list":[{"y":4,"x":5}]}`))
	if err != nil {
		t.Fatalf("decodeMCPOrderedJSON() = %v", err)
	}
	got := PythonJSONDumps(decoded)
	want := `{"a": 3, "b": 2, "list": [{"y": 4, "x": 5}]}`
	if got != want {
		t.Fatalf("rendered = %q, want %q", got, want)
	}
}

// TestMCPOrderedContentHelperLeavesUnexpectedShapesAlone keeps the helper
// strictly additive: text-first, non-object and malformed payloads come back
// untouched.
// Parity-exempt: Go-side ordered-decode/serialization guard; the Python probe
// (docs/e2e-assets/mcp-namespace/python-output.json) is not a pytest target.
func TestMCPOrderedContentHelperLeavesUnexpectedShapesAlone(t *testing.T) {
	textFirst := []any{map[string]any{"text": "caption"}}
	if got := mcpOrderFirstNonTextContentBlock([]byte(`{}`), textFirst); len(got) != 1 || got[0].(map[string]any)["text"] != "caption" {
		t.Fatalf("text-first content was rewritten: %#v", got)
	}
	scalar := []any{"raw"}
	if got := mcpOrderFirstNonTextContentBlock([]byte(`{}`), scalar); got[0] != "raw" {
		t.Fatalf("scalar content was rewritten: %#v", got)
	}
	imageOnly := []any{map[string]any{"type": "image"}}
	got := mcpOrderFirstNonTextContentBlock([]byte(`not-json`), imageOnly)
	if _, ok := got[0].(map[string]any); !ok {
		t.Fatalf("malformed body replaced the decoded block: %#v", got)
	}
}

// TestMCPOrderedContentHelperDoesNotAliasInput guards the copy: ordering the
// first block must not mutate the caller's decoded slice.
// Parity-exempt: Go-side ordered-decode/serialization guard; the Python probe
// (docs/e2e-assets/mcp-namespace/python-output.json) is not a pytest target.
func TestMCPOrderedContentHelperDoesNotAliasInput(t *testing.T) {
	body := []byte(`{"result":{"content":[{"type":"image","data":"AAA"}]}}`)
	content := []any{map[string]any{"data": "AAA", "type": "image"}}
	ordered := mcpOrderFirstNonTextContentBlock(body, content)
	if _, ok := content[0].(map[string]any); !ok {
		t.Fatalf("input content was mutated: %#v", content)
	}
	if !strings.Contains(PythonJSONDumps(ordered[0]), `"type": "image", "data": "AAA"`) {
		t.Fatalf("ordered block = %q", PythonJSONDumps(ordered[0]))
	}
}

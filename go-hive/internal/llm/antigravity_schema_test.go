package llm

// Schema sanitizer and message-conversion tests for the Antigravity provider.
// The first block ports core/tests/test_antigravity_schema.py assertion for
// assertion; the conversion cases pin _to_gemini_contents behaviour.

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Parity: core/tests/test_antigravity_schema.py::test_union_with_null_becomes_nullable
func TestAntigravitySchemaUnionWithNullBecomesNullable(t *testing.T) {
	out, err := sanitizeSchemaForGemini(map[string]any{"type": []any{"string", "null"}})
	if err != nil {
		t.Fatalf("sanitize = %v", err)
	}
	want := map[string]any{"type": "string", "nullable": true}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("sanitize = %#v, want %#v", out, want)
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_plain_schema_passthrough
func TestAntigravitySchemaPlainPassthrough(t *testing.T) {
	out, err := sanitizeSchemaForGemini(map[string]any{"type": "string"})
	if err != nil {
		t.Fatalf("sanitize = %v", err)
	}
	if !reflect.DeepEqual(out, map[string]any{"type": "string"}) {
		t.Fatalf("sanitize = %#v", out)
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_recurses_into_properties
func TestAntigravitySchemaRecursesIntoProperties(t *testing.T) {
	out, err := sanitizeSchemaForGemini(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "integer"},
			"owner": map[string]any{"type": []any{"string", "null"}},
		},
		"required": []any{"id"},
	})
	if err != nil {
		t.Fatalf("sanitize = %v", err)
	}
	object, _ := out.(map[string]any)
	properties, _ := object["properties"].(map[string]any)
	if !reflect.DeepEqual(properties["id"], map[string]any{"type": "integer"}) {
		t.Fatalf("id = %#v", properties["id"])
	}
	if !reflect.DeepEqual(properties["owner"], map[string]any{"type": "string", "nullable": true}) {
		t.Fatalf("owner = %#v", properties["owner"])
	}
	if !reflect.DeepEqual(object["required"], []any{"id"}) {
		t.Fatalf("required = %#v", object["required"])
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_recurses_into_items
func TestAntigravitySchemaRecursesIntoItems(t *testing.T) {
	out, err := sanitizeSchemaForGemini(map[string]any{
		"type":  "array",
		"items": map[string]any{"type": []any{"integer", "null"}},
	})
	if err != nil {
		t.Fatalf("sanitize = %v", err)
	}
	want := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "integer", "nullable": true},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("sanitize = %#v, want %#v", out, want)
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_recurses_into_combinators
func TestAntigravitySchemaRecursesIntoCombinators(t *testing.T) {
	out, err := sanitizeSchemaForGemini(map[string]any{
		"anyOf": []any{
			map[string]any{"type": []any{"string", "null"}},
			map[string]any{"type": "integer"},
		},
	})
	if err != nil {
		t.Fatalf("sanitize = %v", err)
	}
	want := map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string", "nullable": true},
			map[string]any{"type": "integer"},
		},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("sanitize = %#v, want %#v", out, want)
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_does_not_mutate_input
func TestAntigravitySchemaDoesNotMutateInput(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": []any{"string", "null"}},
		},
	}
	snapshot, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if _, err := sanitizeSchemaForGemini(schema); err != nil {
		t.Fatalf("sanitize = %v", err)
	}
	after, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if string(snapshot) != string(after) {
		t.Fatalf("input mutated: %s → %s", snapshot, after)
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_pure_null_type_falls_back_to_string
func TestAntigravitySchemaPureNullFallsBackToString(t *testing.T) {
	out, err := sanitizeSchemaForGemini(map[string]any{"type": []any{"null"}})
	if err != nil {
		t.Fatalf("sanitize = %v", err)
	}
	want := map[string]any{"type": "string", "nullable": true}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("sanitize = %#v, want %#v", out, want)
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_multi_type_non_null_union_raises
func TestAntigravitySchemaMultiTypeUnionRaises(t *testing.T) {
	for _, types := range [][]any{{"string", "integer", "null"}, {"string", "integer"}} {
		if _, err := sanitizeSchemaForGemini(map[string]any{"type": types}); err == nil ||
			!strings.Contains(err.Error(), "Unsupported Gemini schema union") {
			t.Fatalf("types %#v error = %v, want the union rejection", types, err)
		}
	}
}

// Parity: core/tests/test_antigravity_schema.py::test_build_body_leaves_out_only_the_tool_gemini_cannot_express
func TestAntigravityBuildBodyLeavesOutOnlyTheUnsupportedTool(t *testing.T) {
	provider := testAntigravityProvider(t, AntigravityOptions{Model: "gemini-3-pro"})
	tools := []ToolDef{
		{
			Name:        "ok_tool",
			Description: "fine",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"q": map[string]any{"type": []any{"string", "null"}}},
			},
		},
		{
			Name:        "bad_tool",
			Description: "union",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"v": map[string]any{"type": []any{"string", "integer"}}},
			},
		},
	}
	body := provider.buildBody([]map[string]any{{"role": "user", "content": "hi"}}, "", tools, 100)

	inner, _ := body["request"].(map[string]any)
	groups, _ := inner["tools"].([]any)
	if len(groups) != 1 {
		t.Fatalf("tools groups = %#v", inner["tools"])
	}
	group, _ := groups[0].(map[string]any)
	declarations, _ := group["functionDeclarations"].([]any)
	names := make([]string, 0, len(declarations))
	for _, rawDeclaration := range declarations {
		declaration, _ := rawDeclaration.(map[string]any)
		name, _ := declaration["name"].(string)
		names = append(names, name)
	}
	if !reflect.DeepEqual(names, []string{"ok_tool"}) {
		t.Fatalf("declarations = %v, want only ok_tool", names)
	}
}

// TestAntigravityBuildBodyShape pins the envelope and the systemInstruction
// placement.
// Parity: core/framework/llm/antigravity.py::AntigravityProvider._build_body
func TestAntigravityBuildBodyShape(t *testing.T) {
	provider := testAntigravityProvider(t, AntigravityOptions{Model: "gemini-3-flash"})
	body := provider.buildBody(
		[]map[string]any{{"role": "user", "content": "hi"}},
		"be brief",
		nil,
		64,
	)
	if body["project"] != AntigravityDefaultProjectID {
		t.Fatalf("project = %v, want the default project id", body["project"])
	}
	if body["requestType"] != "agent" || body["userAgent"] != "antigravity" {
		t.Fatalf("envelope = %#v", body)
	}
	if id, _ := body["requestId"].(string); !strings.HasPrefix(id, "agent-") {
		t.Fatalf("requestId = %v", body["requestId"])
	}
	inner, _ := body["request"].(map[string]any)
	system, _ := inner["systemInstruction"].(map[string]any)
	parts, _ := system["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	if part["text"] != "be brief" {
		t.Fatalf("systemInstruction = %#v", inner["systemInstruction"])
	}
	generationConfig, _ := inner["generationConfig"].(map[string]any)
	if value, _ := antigravityInt(generationConfig["maxOutputTokens"]); value != 64 {
		t.Fatalf("generationConfig = %#v", generationConfig)
	}
	if _, ok := inner["tools"]; ok {
		t.Fatalf("tools present without tool definitions: %#v", inner["tools"])
	}
}

// TestAntigravityToGeminiContentsBatchesToolResults pins the message
// conversion: system messages fold into the envelope, consecutive tool results
// become one user turn, and leading model turns are dropped.
// Parity: core/framework/llm/antigravity.py::_to_gemini_contents
func TestAntigravityToGeminiContentsBatchesToolResults(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "ignored here"},
		{"role": "assistant", "content": "leading model turn"}, // dropped
		{"role": "user", "content": "run two tools"},
		{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": "call-a", "function": map[string]any{"name": "alpha", "arguments": `{"n":1}`}},
			map[string]any{"id": "call-b", "function": map[string]any{"name": "beta", "arguments": "not-json"}},
		}},
		{"role": "tool", "tool_call_id": "call-a", "content": "a-result"},
		{"role": "tool", "tool_call_id": "call-b", "content": "b-result"},
	}
	contents := toGeminiContents(messages, nil)
	if len(contents) != 3 {
		t.Fatalf("contents = %d, want user + model + batched tool results: %#v", len(contents), contents)
	}
	first, _ := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("first role = %v, want the leading model turn dropped", first["role"])
	}
	modelTurn, _ := contents[1].(map[string]any)
	if modelTurn["role"] != "model" {
		t.Fatalf("second role = %v", modelTurn["role"])
	}
	modelParts, _ := modelTurn["parts"].([]any)
	if len(modelParts) != 2 {
		t.Fatalf("model parts = %#v", modelParts)
	}
	alphaPart, _ := modelParts[0].(map[string]any)
	alphaCall, _ := alphaPart["functionCall"].(map[string]any)
	if alphaCall["name"] != "alpha" || alphaCall["id"] != "call-a" {
		t.Fatalf("alpha call = %#v", alphaCall)
	}
	if args, _ := alphaCall["args"].(map[string]any); args["n"] != float64(1) {
		t.Fatalf("alpha args = %#v", alphaCall["args"])
	}
	betaPart, _ := modelParts[1].(map[string]any)
	betaCall, _ := betaPart["functionCall"].(map[string]any)
	if args, ok := betaCall["args"].(map[string]any); !ok || len(args) != 0 {
		t.Fatalf("invalid JSON arguments must fall back to {}: %#v", betaCall["args"])
	}

	toolTurn, _ := contents[2].(map[string]any)
	if toolTurn["role"] != "user" {
		t.Fatalf("tool turn role = %v", toolTurn["role"])
	}
	toolParts, _ := toolTurn["parts"].([]any)
	if len(toolParts) != 2 {
		t.Fatalf("tool parts = %#v, want both results batched", toolParts)
	}
	for index, wantName := range []string{"alpha", "beta"} {
		part, _ := toolParts[index].(map[string]any)
		response, _ := part["functionResponse"].(map[string]any)
		if response["name"] != wantName {
			t.Fatalf("tool part %d name = %v, want %s (tool_call_id → name)", index, response["name"], wantName)
		}
		body, _ := response["response"].(map[string]any)
		if body["content"] == "" {
			t.Fatalf("tool part %d missing content: %#v", index, response)
		}
	}
}

// TestAntigravityToGeminiContentsSkipsImages pins that non-text content blocks
// are dropped, exactly as upstream does for image parts.
// Parity: core/framework/llm/antigravity.py::_to_gemini_contents
func TestAntigravityToGeminiContentsSkipsImages(t *testing.T) {
	contents := toGeminiContents([]map[string]any{
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "look"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
		}},
	}, nil)
	if len(contents) != 1 {
		t.Fatalf("contents = %#v", contents)
	}
	turn, _ := contents[0].(map[string]any)
	parts, _ := turn["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("parts = %#v, want only the text block", parts)
	}
	part, _ := parts[0].(map[string]any)
	if part["text"] != "look" {
		t.Fatalf("part = %#v", part)
	}
}

// TestAntigravityCleanToolName pins _clean_tool_name: separators, the leading
// character rule and the 64-character cap.
// Parity: core/framework/llm/antigravity.py::_clean_tool_name
func TestAntigravityCleanToolName(t *testing.T) {
	cases := map[string]string{
		"a/b c":                 "a_b_c",
		"1tool":                 "_1tool",
		"_ok":                   "_ok",
		strings.Repeat("x", 80): strings.Repeat("x", 64),
	}
	names := make([]string, 0, len(cases))
	for input := range cases {
		names = append(names, input)
	}
	sort.Strings(names)
	for _, input := range names {
		if got := cleanGeminiToolName(input); got != cases[input] {
			t.Fatalf("cleanGeminiToolName(%q) = %q, want %q", input, got, cases[input])
		}
	}
}

// TestAntigravityParseCompleteResponse pins the response parse: thought parts
// are dropped, the bare-envelope fallback works, and finish reasons map.
// Parity: core/framework/llm/antigravity.py::_parse_complete_response
func TestAntigravityParseCompleteResponse(t *testing.T) {
	completion, err := parseAntigravityCompleteResponse([]byte(`{
		"response": {
			"candidates": [{"content": {"parts": [{"text": "hi"}, {"text": "hidden", "thought": true}]}, "finishReason": "MAX_TOKENS"}],
			"usageMetadata": {"promptTokenCount": 3, "candidatesTokenCount": 4},
			"modelVersion": "gemini-3-flash-002"
		},
		"traceId": "t"
	}`), "gemini-3-flash")
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if completion.Content != "hi" || completion.Model != "gemini-3-flash-002" {
		t.Fatalf("completion = %#v", completion)
	}
	if completion.StopReason != "max_tokens" || completion.InputTokens != 3 || completion.OutputTokens != 4 {
		t.Fatalf("completion = %#v", completion)
	}

	bare, err := parseAntigravityCompleteResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":"yo"}]},"finishReason":"OTHER"}]}`), "requested-model")
	if err != nil {
		t.Fatalf("bare parse = %v", err)
	}
	if bare.Content != "yo" || bare.Model != "requested-model" || bare.StopReason != "tool_use" {
		t.Fatalf("bare completion = %#v", bare)
	}
}

// TestAntigravityFinishReasonMapping pins _map_finish_reason.
// Parity: core/framework/llm/antigravity.py::_map_finish_reason
func TestAntigravityFinishReasonMapping(t *testing.T) {
	cases := map[string]string{
		"STOP":       "stop",
		"MAX_TOKENS": "max_tokens",
		"OTHER":      "tool_use",
		"":           "stop",
		"MALFORMED":  "stop",
	}
	for input, want := range cases {
		if got := mapAntigravityFinishReason(input); got != want {
			t.Fatalf("mapAntigravityFinishReason(%q) = %q, want %q", input, got, want)
		}
	}
}

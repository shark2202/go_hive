package agentloop

// Parity tests for the JSON-aware tool-result preview (A4):
// framework/agent_loop/internals/tool_result_handler.py::extract_json_metadata
// and ::build_json_preview.

import (
	"encoding/json"
	"strings"
	"testing"
)

// Parity-exempt: Go-only JSON metadata diagnostics for tool previews.
func TestExtractJSONMetadataReportsArrayLengthsAndKeyTypes(t *testing.T) {
	var parsed any
	payload := `{"query":"q","items":[{"id":1,"name":"a"},{"id":2,"name":"b"}],"meta":{"total":2,"next":null}}`
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	metadata := ExtractJSONMetadata(parsed)
	for _, want := range []string{
		`"items": list of 2 items (each item: dict with keys ['id', 'name'])`,
		`"query": str`,
		`"total": int`,
		`"next": NoneType`,
	} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("metadata missing %q:\n%s", want, metadata)
		}
	}
	if got := ExtractJSONMetadata("scalar"); got != "" {
		t.Fatalf("scalar metadata = %q, want empty", got)
	}
}

// Parity: core/framework/agent_loop/internals/tool_result_handler.py::build_json_preview
func TestBuildJSONPreviewTruncatesLargeArraysWithCountMarker(t *testing.T) {
	items := make([]any, 0, 20)
	for i := 0; i < 20; i++ {
		items = append(items, map[string]any{"id": float64(i)})
	}
	parsed := map[string]any{"items": items, "ok": true}

	preview, ok := BuildJSONPreview(parsed, 5000)
	if !ok {
		t.Fatal("BuildJSONPreview reported no truncation for a 20-item array")
	}
	if !strings.Contains(preview, "... (16 more items omitted, 20 total) ...") {
		t.Fatalf("preview missing the omission marker:\n%s", preview)
	}
	if strings.Count(preview, `"id":`) != 4 {
		t.Fatalf("preview kept %d ids, want head 3 + tail 1", strings.Count(preview, `"id":`))
	}
	// No large array means no rewrite: the caller slices the raw text.
	if _, ok := BuildJSONPreview(map[string]any{"items": []any{1, 2, 3}}, 5000); ok {
		t.Fatal("BuildJSONPreview rewrote a small array")
	}
}

// Parity: core/framework/agent_loop/internals/tool_result_handler.py::build_json_preview
func TestLargeToolResultWithoutSpillDirTruncatesWithMetadata(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.MaxToolResultChars = 2000
	loop := NewAgentLoop(cfg, successToolExecutor)
	items := make([]any, 0, 30)
	for i := 0; i < 30; i++ {
		items = append(items, map[string]any{"id": float64(i), "text": strings.Repeat("x", 100)})
	}
	encoded, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result := loop.applyToolResultSpillover(ToolResult{Content: string(encoded)}, "big_tool")
	if result.SpilloverPath != "" {
		t.Fatalf("spillover path = %q without a spill dir", result.SpilloverPath)
	}
	if !strings.Contains(result.Content, "no spillover dir configured") {
		t.Fatalf("missing the in-place truncation header: %.200s", result.Content)
	}
	if !strings.Contains(result.Content, "Data structure:") ||
		!strings.Contains(result.Content, `"items": list of 30 items`) {
		t.Fatalf("missing JSON metadata:\n%.400s", result.Content)
	}
	if !strings.Contains(result.Content, "... (26 more items omitted, 30 total) ...") {
		t.Fatalf("missing the smart-preview marker:\n%.400s", result.Content)
	}
	if len(result.Content) >= len(encoded) {
		t.Fatalf("content was not truncated: %d >= %d", len(result.Content), len(encoded))
	}
}

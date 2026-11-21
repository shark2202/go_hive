package visionfallback

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

type captionCall struct {
	Request llm.CompletionRequest
}

type scriptedCompleter struct {
	content string
	err     error
	calls   []captionCall
}

func (s *scriptedCompleter) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	s.calls = append(s.calls, captionCall{Request: req})
	if s.err != nil {
		return llm.Completion{}, s.err
	}
	return llm.Completion{Content: s.content}, nil
}

func pngBlock() map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}
}

func pdfBlock() map[string]any {
	return map[string]any{"type": "file", "file": map[string]any{
		"file_data": "data:application/pdf;base64,JVBE",
		"filename":  "doc.pdf",
	}}
}

func hiveReplayBlock() map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "hive://attachments/old.png"}}
}

// TestCropBoxExtraction mirrors the upstream crop_box reader.
//
// Parity: core/tests/test_vision_fallback.py::TestCropBoxExtraction::test_returns_crop_box_when_present
func TestCropBoxExtraction(t *testing.T) {
	if box, ok := CropBoxFromToolResult(`{"ok": true, "crop_box": [0.6, 0.1, 0.8, 0.3]}`); !ok || box != [4]float64{0.6, 0.1, 0.8, 0.3} {
		t.Fatalf("crop box = %v, ok=%v", box, ok)
	}
}

// TestCropBoxExtractionRejectsDegenerateInputs covers the full-viewport,
// missing, zero-area and unparseable cases the remap treats as "no crop".
//
// Parity: core/tests/test_vision_fallback.py::TestCropBoxExtraction::test_full_viewport_box_is_treated_as_none,
// core/tests/test_vision_fallback.py::TestCropBoxExtraction::test_degraded_inputs_return_none,
// core/tests/test_vision_fallback.py::TestCropBoxExtraction::test_zero_area_box_rejected
func TestCropBoxExtractionRejectsDegenerateInputs(t *testing.T) {
	cases := []string{
		`{"ok": true, "crop_box": [0, 0, 1, 1]}`,
		`{"ok": true, "cssWidth": 800}`,
		``,
		`not json`,
		`[1, 2, 3]`,
		`{"crop_box": [0, 0]}`,
		`{"crop_box": [0.5, 0.5, 0.5, 0.8]}`,
	}
	for _, raw := range cases {
		if _, ok := CropBoxFromToolResult(raw); ok {
			t.Fatalf("CropBoxFromToolResult(%q) reported a crop", raw)
		}
	}
}

// TestCaptionRemap rewrites crop-relative fractions into viewport space, and
// leaves non-coordinate parentheses alone.
//
// Parity: core/tests/test_vision_fallback.py::TestCaptionRemap::test_remaps_coordinates_into_viewport_space,
// core/tests/test_vision_fallback.py::TestCaptionRemap::test_non_coordinate_parens_untouched,
// core/tests/test_vision_fallback.py::TestCaptionRemap::test_full_viewport_caption_unchanged
func TestCaptionRemap(t *testing.T) {
	meta := `{"action": "zoom", "crop_box": [0.6, 0.1, 0.8, 0.3]}`
	caption := `The "Submit" button (0.5, 0.5) and a link (0.0, 1.0).`
	remapped := RemapCaptionForCrop(caption, meta)
	if !strings.Contains(remapped, "(0.7, 0.2)") || !strings.Contains(remapped, "(0.6, 0.3)") {
		t.Fatalf("remapped caption = %q", remapped)
	}

	pixels := "Captured at (1920, 1080) — button (0.5, 0.5)."
	remapped = RemapCaptionForCrop(pixels, meta)
	if !strings.Contains(remapped, "(1920, 1080)") || !strings.Contains(remapped, "(0.7, 0.2)") {
		t.Fatalf("pixel label touched: %q", remapped)
	}

	unchanged := `Button (0.5, 0.5).`
	if got := RemapCaptionForCrop(unchanged, `{"crop_box": [0, 0, 1, 1]}`); got != unchanged {
		t.Fatalf("full-viewport caption = %q", got)
	}
	if got := RemapCaptionForCrop(unchanged, `{"cssWidth": 800}`); got != unchanged {
		t.Fatalf("no-crop caption = %q", got)
	}
}

// TestCaptionCallsTheVisionModel proves the captioner sends the configured
// model's request with the image blocks and the coordinate-labeling system
// prompt, and returns the model's text.
//
// Parity: core/tests/test_vision_fallback.py::TestDrainInjectionFilter::test_image_url_with_data_uri_kept
func TestCaptionCallsTheVisionModel(t *testing.T) {
	completer := &scriptedCompleter{content: ` "Submit" button (0.83, 0.92)`}
	captioner := New(completer, "gemini/gemini-3-flash", 640)
	caption, ok := captioner.Caption(context.Background(), "Agent intent: click submit", []map[string]any{pngBlock(), pdfBlock()})
	if !ok {
		t.Fatal("caption failed")
	}
	if caption != `"Submit" button (0.83, 0.92)` {
		t.Fatalf("caption = %q", caption)
	}
	if len(completer.calls) != 1 {
		t.Fatalf("calls = %d", len(completer.calls))
	}
	request := completer.calls[0].Request
	if request.MaxTokens != 640 {
		t.Fatalf("max tokens = %d, want the slot's 640", request.MaxTokens)
	}
	if !strings.Contains(request.System, "fractional viewport") || !strings.Contains(request.System, "(fx, fy)") {
		t.Fatalf("system prompt lost the coordinate instructions: %q", request.System)
	}
	if len(request.Messages) != 1 || len(request.Messages[0].ContentParts) != 3 {
		t.Fatalf("content parts = %#v", request.Messages)
	}
	if request.Messages[0].ContentParts[0]["type"] != "text" {
		t.Fatalf("first part = %#v, want the intent text", request.Messages[0].ContentParts[0])
	}
}

// TestCaptionDropsNonFetchableImages pins the attachment filter: a hive://
// reference would fail the whole request, so it is dropped — and an image-only
// set of undeliverable blocks is no caption at all.
//
// Parity: core/tests/test_vision_fallback.py::TestDrainInjectionFilter::test_mixed_blocks_only_data_uris_kept,
// core/tests/test_vision_fallback.py::TestDrainInjectionFilter::test_non_data_uri_dropped
func TestCaptionDropsNonFetchableImages(t *testing.T) {
	completer := &scriptedCompleter{content: "caption"}
	captioner := New(completer, "gemini/gemini-3-flash", 0)
	if _, ok := captioner.Caption(context.Background(), "intent", []map[string]any{pngBlock(), hiveReplayBlock(), pdfBlock()}); !ok {
		t.Fatal("caption failed")
	}
	parts := completer.calls[0].Request.Messages[0].ContentParts
	if len(parts) != 3 {
		t.Fatalf("parts = %#v, want intent + 2 deliverable blocks", parts)
	}
	for _, part := range parts[1:] {
		if part["type"] == "image_url" {
			url, _ := part["image_url"].(map[string]any)["url"].(string)
			if strings.HasPrefix(url, "hive://") {
				t.Fatalf("non-fetchable block survived: %#v", part)
			}
		}
	}

	completer = &scriptedCompleter{content: "caption"}
	captioner = New(completer, "gemini/gemini-3-flash", 0)
	if _, ok := captioner.Caption(context.Background(), "intent", []map[string]any{hiveReplayBlock()}); ok {
		t.Fatal("undeliverable-only images produced a caption")
	}
	if len(completer.calls) != 0 {
		t.Fatalf("the model was called for undeliverable images: %d", len(completer.calls))
	}
}

// TestCaptionDegradesSilently covers the failure paths that must never fail a
// turn: transport error, empty answer, no provider, no configured model.
//
// Parity-exempt: caption_tool_image's failure paths in
// internals/vision_fallback.py have no upstream unit test; the Go test pins
// the silent-degradation contract the port mirrors.
func TestCaptionDegradesSilently(t *testing.T) {
	images := []map[string]any{pngBlock()}
	if _, ok := New(&scriptedCompleter{err: errors.New("boom")}, "gemini/flash", 0).Caption(context.Background(), "i", images); ok {
		t.Fatal("transport error produced a caption")
	}
	if _, ok := New(&scriptedCompleter{content: "   "}, "gemini/flash", 0).Caption(context.Background(), "i", images); ok {
		t.Fatal("empty answer produced a caption")
	}
	if _, ok := New(nil, "gemini/flash", 0).Caption(context.Background(), "i", images); ok {
		t.Fatal("nil provider produced a caption")
	}
	if New(&scriptedCompleter{}, "", 0) != nil {
		t.Fatal("a captioner was built without a model")
	}
	var missing *Captioner
	if _, ok := missing.Caption(context.Background(), "i", images); ok {
		t.Fatal("nil captioner produced a caption")
	}
}

// TestIntentForTool covers the three intent sources: an explicit intent
// argument, an --intent flag inside a hive-browser command, and the tool
// descriptor plus the agent's latest reasoning.
//
// Parity-exempt: extract_intent_for_tool in internals/vision_fallback.py has
// no upstream unit test; the Go test pins the three sources the port mirrors.
func TestIntentForTool(t *testing.T) {
	if got := IntentForTool("browser_screenshot", map[string]any{"intent": "  click submit  "}, nil); got != "Agent intent: click submit" {
		t.Fatalf("explicit intent = %q", got)
	}
	command := map[string]any{"command": `hive-browser interact --intent="open the invoice" --coordinate 0.5,0.5`}
	if got := IntentForTool("terminal_exec", command, nil); got != "Agent intent: open the invoice" {
		t.Fatalf("command intent = %q", got)
	}
	got := IntentForTool("browser_screenshot", map[string]any{"path": "/tmp/a.png"}, func() string { return "looking for the invoice" })
	if !strings.Contains(got, "Called: browser_screenshot(") || !strings.Contains(got, "looking for the invoice") {
		t.Fatalf("fallback intent = %q", got)
	}
	got = IntentForTool("browser_screenshot", nil, nil)
	if !strings.Contains(got, "<no preceding reasoning>") {
		t.Fatalf("empty-reasoning intent = %q", got)
	}
}

// TestIntentJSONMatchesPythonDumps pins the args rendering inside the intent:
// Python's json.dumps defaults (", " / ": " separators, ensure_ascii) so the
// vision model receives the same prompt text from either implementation.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::extract_intent_for_tool
func TestIntentJSONMatchesPythonDumps(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "single key keeps python spacing",
			args: map[string]any{"paths": []any{"/tmp/a.png"}},
			want: "Called: attach_file({\"paths\": [\"/tmp/a.png\"]})\n\nReasoning before call:\n<no preceding reasoning>",
		},
		{
			name: "non-ascii is escaped, angle brackets are not",
			args: map[string]any{"note": "中文 <&>", "paths": []any{"/tmp/截图.png"}},
			want: "Called: attach_file({\"note\": \"\\u4e2d\\u6587 <&>\", \"paths\": [\"/tmp/\\u622a\\u56fe.png\"]})\n\nReasoning before call:\n<no preceding reasoning>",
		},
		{
			// Go maps lose insertion order, so the encoder sorts keys; upstream
			// preserves the model's order (see docs/vision-caption-parity.md).
			name: "sorted keys, python scalars",
			args: map[string]any{"z": nil, "b": true, "n": float64(10), "f": 1.5},
			want: "Called: tool({\"b\": true, \"f\": 1.5, \"n\": 10, \"z\": null})\n\nReasoning before call:\n<no preceding reasoning>",
		},
		{
			name: "control characters use json shortcuts",
			args: map[string]any{"text": "a\tb\nc\"d\\e"},
			want: "Called: tool({\"text\": \"a\\tb\\nc\\\"d\\\\e\"})\n\nReasoning before call:\n<no preceding reasoning>",
		},
	}
	for _, tc := range cases {
		name := "attach_file"
		if tc.name != "single key keeps python spacing" && tc.name != "non-ascii is escaped, angle brackets are not" {
			name = "tool"
		}
		if got := IntentForTool(name, tc.args, nil); got != tc.want {
			t.Fatalf("%s: intent =\n%q\nwant\n%q", tc.name, got, tc.want)
		}
	}
}

package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// fakeCaptioner records the caption requests the loop makes.
type fakeCaptioner struct {
	caption  string
	ok       bool
	requests []string
	images   [][]map[string]any
}

func (f *fakeCaptioner) Caption(_ context.Context, intent string, images []map[string]any) (string, bool) {
	f.requests = append(f.requests, intent)
	f.images = append(f.images, images)
	return f.caption, f.ok
}

// fixedModelCompleter is a scripted completer that reports a chosen model name
// so the loop can decide whether the model accepts image blocks.
type fixedModelCompleter struct {
	scriptedCompleter
	model string
}

func (c *fixedModelCompleter) ModelName() string { return c.model }

func captionTestLoop(t *testing.T, model string, captioner VisionCaptioner) (*AgentLoop, *NodeConversation, *fakeCaptioner) {
	t.Helper()
	completer := &fixedModelCompleter{
		scriptedCompleter: scriptedCompleter{scenarios: []scriptedResponse{
			{toolCalls: []llm.ToolCall{{ID: "c1", Name: "browser_screenshot", Arguments: `{"intent": "find the submit button"}`}}},
			{content: "done"},
		}},
		model: model,
	}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{
			Content:      `{"ok": true, "crop_box": [0.6, 0.1, 0.8, 0.3]}`,
			ImageContent: []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}},
		}, nil
	}
	cfg := DefaultLoopConfig()
	cfg.SpilloverDir = t.TempDir()
	ctx := &AgentContext{
		AgentID:        "vision_agent",
		AgentSpec:      NewAgentSpec("vision_agent", "Vision Agent", "caption tool images"),
		LLM:            completer,
		AvailableTools: []llm.ToolDef{{Name: "browser_screenshot", Parameters: map[string]any{"type": "object"}}},
		StreamID:       "vision",
	}
	conv := NewNodeConversation("You are an agent.")
	loop := NewAgentLoop(cfg, executor)
	loop.VisionCaptioner = captioner
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	fake, _ := captioner.(*fakeCaptioner)
	return loop, conv, fake
}

func toolMessage(t *testing.T, conv *NodeConversation) Message {
	t.Helper()
	for _, message := range conv.Messages() {
		if message.Role == RoleTool {
			return message
		}
	}
	t.Fatalf("no tool message recorded; messages = %#v", conv.Messages())
	return Message{}
}

// TestTextOnlyModelGetsC CaptionsInsteadOfImages pins the vision-fallback splice:
// with a text-only main model and a captioner installed, the tool result
// carries the caption (with crop coordinates remapped to viewport fractions)
// and no image blocks.
//
// Parity: core/tests/test_vision_fallback.py::TestCaptionRemap::test_remaps_coordinates_into_viewport_space
func TestTextOnlyModelGetsCaptionsInsteadOfImages(t *testing.T) {
	captioner := &fakeCaptioner{caption: `"Submit" button (0.5, 0.5)`, ok: true}
	_, conv, fake := captionTestLoop(t, "groq/llama-3.1-8b-instant", captioner)

	message := toolMessage(t, conv)
	if len(message.ImageContent) != 0 {
		t.Fatalf("image blocks survived captioning: %#v", message.ImageContent)
	}
	if !strings.Contains(message.Content, "[vision-fallback caption]") {
		t.Fatalf("caption marker missing from %q", message.Content)
	}
	if !strings.Contains(message.Content, "(0.7, 0.2)") {
		t.Fatalf("caption was not remapped into viewport space: %q", message.Content)
	}
	if len(fake.images) != 1 || len(fake.images[0]) != 1 {
		t.Fatalf("captioner images = %#v", fake.images)
	}
	if !strings.Contains(fake.requests[0], "find the submit button") {
		t.Fatalf("intent = %q", fake.requests[0])
	}
}

// TestVisionModelKeepsImages is the other half of the gate: a model that can
// accept image tool results never reaches the captioner.
//
// Parity-exempt: upstream's accept-image gate (_vision_fallback_active in
// agent_loop.py) has no unit test of its own; the Go test pins the branch the
// port mirrors.
func TestVisionModelKeepsImages(t *testing.T) {
	captioner := &fakeCaptioner{caption: "should not be used", ok: true}
	_, conv, fake := captionTestLoop(t, "anthropic/claude-sonnet-4-5-20250929", captioner)

	message := toolMessage(t, conv)
	if len(message.ImageContent) != 1 {
		t.Fatalf("image blocks = %#v, want the original block", message.ImageContent)
	}
	if strings.Contains(message.Content, "[vision-fallback caption]") {
		t.Fatalf("a vision model's images were captioned: %q", message.Content)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("captioner was called %d times for a vision model", len(fake.requests))
	}
}

// TestCaptionFailureMarksTheImageStripped keeps the honest degradation: a
// captioner that cannot produce a caption leaves a marker saying so, so the
// model never believes it saw an image it did not receive.
//
// Parity-exempt: the exhausted-marker branch in agent_loop.py has no upstream
// unit test; the Go test pins the degradation contract the port mirrors.
func TestCaptionFailureMarksTheImageStripped(t *testing.T) {
	captioner := &fakeCaptioner{ok: false}
	_, conv, _ := captionTestLoop(t, "groq/llama-3.1-8b-instant", captioner)

	message := toolMessage(t, conv)
	if len(message.ImageContent) != 0 {
		t.Fatalf("image blocks survived a failed caption: %#v", message.ImageContent)
	}
	if !strings.Contains(message.Content, "[image stripped — vision fallback exhausted]") {
		t.Fatalf("exhausted marker missing from %q", message.Content)
	}
}

// TestCaptionSplicedBeforeReminderTail pins the splice position: upstream
// appends the caption before the batch's reminder tail (prefix, caption,
// tail), and the port must store the same bytes in the same order.
//
// Parity: framework/agent_loop/agent_loop.py (vision_fallback_marker splice,
// then the _combined_tail append)
func TestCaptionSplicedBeforeReminderTail(t *testing.T) {
	completer := &fixedModelCompleter{model: "groq/llama-3.1-8b-instant"}
	cfg := DefaultLoopConfig()
	cfg.SpilloverDir = t.TempDir()
	loop := NewAgentLoop(cfg, func(ToolCallRequest) (ToolResult, error) { return ToolResult{}, nil })
	loop.VisionCaptioner = &fakeCaptioner{caption: `"Submit" button (0.5, 0.5)`, ok: true}

	conv := NewNodeConversation("You are an agent.")
	agentCtx := &AgentContext{
		AgentID:   "tail_agent",
		AgentSpec: NewAgentSpec("tail_agent", "Tail", "caption order"),
		LLM:       completer,
		StreamID:  "tail",
	}
	toolCalls := []llm.ToolCall{{ID: "t1", Name: "attach_file", Arguments: `{"paths": ["/tmp/a.png"]}`}}
	results := []ToolResult{{
		Content:      `{"attached": [], "errors": []}`,
		ImageContent: []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}},
	}}
	tail := "<system-reminder>\nreminder body\n</system-reminder>"
	if _, err := loop.storeToolBatch(context.Background(), agentCtx, conv, toolCalls, results, tail, []bool{true}, nil); err != nil {
		t.Fatalf("storeToolBatch: %v", err)
	}

	want := `{"attached": [], "errors": []}` + "\n\n[vision-fallback caption]\n\"Submit\" button (0.5, 0.5)" + "\n\n" + tail
	if got := toolMessage(t, conv).Content; got != want {
		t.Fatalf("stored tool result =\n%q\nwant\n%q", got, want)
	}
}

// TestNoCaptionerKeepsToolImages is the default path: a session without a
// configured vision_fallback keeps the previous behaviour — images ride the
// tool result to the model (or to the media gate).
//
// Parity-exempt: the default no-captioner path is implicit upstream and has no
// dedicated unit test; the Go test pins that it stays unchanged.
func TestNoCaptionerKeepsToolImages(t *testing.T) {
	_, conv, _ := captionTestLoop(t, "groq/llama-3.1-8b-instant", nil)

	message := toolMessage(t, conv)
	if len(message.ImageContent) != 1 {
		t.Fatalf("image blocks = %#v, want the untouched block", message.ImageContent)
	}
	if strings.Contains(message.Content, "vision-fallback") {
		t.Fatalf("unexpected caption marker: %q", message.Content)
	}
}

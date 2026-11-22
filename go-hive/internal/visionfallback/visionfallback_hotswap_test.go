package visionfallback

import (
	"context"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// TestCaptionerResolvesTheSlotPerCaption pins the vision_fallback write
// semantics: upstream reads configuration.json on every caption_tool_image
// call, so a PUT to the slot must reach the next image without a session
// rebuild. The resolver therefore runs per caption, and a swap between two
// captions must route the second image to the rebuilt transport.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::caption_tool_image
// (get_vision_fallback_model / get_vision_fallback_api_key resolve per call)
// Parity-exempt: 上游没有「逐次 caption 重读配置」的单测（行为内联在
// framework/agent_loop/internals/vision_fallback.py::caption_tool_image）；
// 本测试锁定 Go 的 Resolver 逐次解析语义。
func TestCaptionerResolvesTheSlotPerCaption(t *testing.T) {
	first := &scriptedCompleter{content: "first caption"}
	second := &scriptedCompleter{content: "second caption"}
	current := llm.Completer(first)
	model := "openai/caption-1"
	captioner := NewResolver(func() (llm.Completer, string, int, bool) {
		return current, model, 512, true
	})
	if captioner == nil {
		t.Fatal("NewResolver returned nil")
	}

	caption, ok := captioner.Caption(context.Background(), "what is shown?", []map[string]any{pngBlock()})
	if !ok || caption != "first caption" {
		t.Fatalf("first caption = (%q, %v)", caption, ok)
	}
	if len(first.calls) != 1 || len(second.calls) != 0 {
		t.Fatalf("calls before swap: first=%d second=%d", len(first.calls), len(second.calls))
	}

	// A PUT for the slot rebuilds the resolver's transport; the next caption
	// must use it without any session-level work.
	current = second
	model = "openai/caption-2"
	caption, ok = captioner.Caption(context.Background(), "and now?", []map[string]any{pngBlock()})
	if !ok || caption != "second caption" {
		t.Fatalf("second caption = (%q, %v)", caption, ok)
	}
	if len(first.calls) != 1 || len(second.calls) != 1 {
		t.Fatalf("calls after swap: first=%d second=%d", len(first.calls), len(second.calls))
	}
}

// TestCaptionerResolverReportsAnUnusableSlot pins the no-key branch: a slot
// present but without a resolvable key reports no caption, and the loop drops
// the images with the exhausted marker (upstream: "configured but no API key
// resolved; skipping").
// Parity-exempt: 上游无 key 的跳过分支无单测（内联在 caption_tool_image）；
// 本测试锁定 Go 的 ok=false 语义（图片按 exhausted marker 丢弃）。
func TestCaptionerResolverReportsAnUnusableSlot(t *testing.T) {
	calls := 0
	captioner := NewResolver(func() (llm.Completer, string, int, bool) {
		calls++
		return nil, "openai/caption-1", 0, false
	})
	if captioner == nil {
		t.Fatal("NewResolver returned nil")
	}
	if caption, ok := captioner.Caption(context.Background(), "intent", []map[string]any{pngBlock()}); ok || caption != "" {
		t.Fatalf("caption = (%q, %v), want no caption", caption, ok)
	}
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls)
	}
}

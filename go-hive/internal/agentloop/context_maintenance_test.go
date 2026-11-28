package agentloop

// Post-batch context maintenance: screenshot eviction and mid-turn pruning.
//
// Upstream runs both after the tool batch is stored and before it publishes the
// post_tool_results context readout, so the readout reports the reclaimed size
// and the next model call fits. These tests drive the loop (not the helpers) so
// they pin the call site, its ordering, and the config gates.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// batchReadoutObserver records the conversation's tool-message state at each
// batch-end context readout. It is the port's equivalent of the server's
// context_usage_updated(trigger=post_tool_results) frame.
type batchReadoutObserver struct {
	conv  *NodeConversation
	reads []int // number of image-bearing tool messages at each readout
}

func (o *batchReadoutObserver) IterationStarted(int)                          {}
func (o *batchReadoutObserver) LLMTurnComplete(llm.Completion, int)           {}
func (o *batchReadoutObserver) AssistantText(string, string, int)             {}
func (o *batchReadoutObserver) ToolCallStarted(ToolCallRequest)               {}
func (o *batchReadoutObserver) ToolCallCompleted(ToolCallRequest, ToolResult) {}
func (o *batchReadoutObserver) ToolResultStored()                             {}

func (o *batchReadoutObserver) ToolBatchStored() {
	images := 0
	for _, message := range o.conv.Messages() {
		if message.Role == RoleTool && len(message.ImageContent) > 0 {
			images++
		}
	}
	o.reads = append(o.reads, images)
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (image eviction:
// `evict_old_images(keep_latest=max_retained_screenshots)` after every batch)
// Parity: core/tests/test_node_conversation.py (evict_old_images keeps the
// newest N image messages and leaves the text metadata behind)
func TestScreenshotEvictionRunsBeforeTheBatchEndReadout(t *testing.T) {
	screenshot := func() []map[string]any {
		return []map[string]any{{
			"type":      "image_url",
			"image_url": map[string]any{"url": "data:image/png;base64,AAAA"},
		}}
	}
	cfg := LoopConfig{MaxIterations: 6, MaxRetainedScreenshots: 1}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "screenshot_a", Arguments: `{"page":1}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: "screenshot_b", Arguments: `{"page":2}`}}},
		{content: "both screenshots reviewed"},
	}}
	call := 0
	executor := func(req ToolCallRequest) (ToolResult, error) {
		call++
		return ToolResult{Content: fmt.Sprintf("screenshot %d taken", call), ImageContent: screenshot()}, nil
	}
	loop := NewAgentLoop(cfg, executor)
	ctx := midturnAgentContext(completer, "screenshot_a", "screenshot_b")
	conv := NewNodeConversation("You are a test assistant.")
	observer := &batchReadoutObserver{conv: conv}
	ctx.Observer = observer

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	messages := toolMessages(conv)
	if len(messages) != 2 {
		t.Fatalf("tool messages = %d, want 2", len(messages))
	}
	if got := len(messages[0].ImageContent); got != 0 {
		t.Fatalf("oldest screenshot kept %d image blocks, want 0 (max_retained_screenshots=1)", got)
	}
	if messages[0].Content != "screenshot 1 taken" {
		t.Fatalf("evicted message lost its text metadata: %q", messages[0].Content)
	}
	if got := len(messages[1].ImageContent); got != 1 {
		t.Fatalf("newest screenshot image blocks = %d, want 1", got)
	}
	// The readout after the second batch must already see the eviction: one
	// image in the conversation, not two.
	if len(observer.reads) != 2 {
		t.Fatalf("batch readouts = %d, want 2", len(observer.reads))
	}
	if observer.reads[1] != 1 {
		t.Fatalf("images at second post_tool_results readout = %d, want 1 (eviction runs before the readout)", observer.reads[1])
	}
	if got := loop.Counter("screenshots_evicted"); got != 1 {
		t.Fatalf("screenshots_evicted counter = %d, want 1", got)
	}
}

// Parity: framework/agent_loop/internals/types.py::LoopConfig.max_retained_screenshots
// (a negative bound disables eviction entirely)
func TestNegativeScreenshotBoundKeepsEveryImage(t *testing.T) {
	cfg := LoopConfig{MaxIterations: 5, MaxRetainedScreenshots: -1}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "shot_one", Arguments: `{}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: "shot_two", Arguments: `{}`}}},
		{content: "done"},
	}}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{
			Content:      "shot",
			ImageContent: []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}},
		}, nil
	}
	loop := NewAgentLoop(cfg, executor)
	ctx := midturnAgentContext(completer, "shot_one", "shot_two")
	conv := NewNodeConversation("You are a test assistant.")

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	for i, message := range toolMessages(conv) {
		if len(message.ImageContent) != 1 {
			t.Fatalf("tool message %d image blocks = %d, want 1 (eviction disabled)", i, len(message.ImageContent))
		}
	}
	if got := loop.Counter("screenshots_evicted"); got != 0 {
		t.Fatalf("screenshots_evicted counter = %d, want 0", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (mid-turn pruning:
// usage_ratio >= 0.6 → prune_old_tool_results(protect=max(2000, window//12),
// min_prune=max(1000, protect//3)))
// Parity: core/tests/test_phase_compaction.py (prune_old_tool_results keeps the
// newest protected results and replaces recoverable older ones)
func TestMidTurnPruneRunsAfterTheToolBatch(t *testing.T) {
	const resultChars = 8000
	cfg := LoopConfig{
		MaxIterations: 5,
		// 20k window: seven 8k-char results put the batch at ~0.9 usage, and
		// the post-prune conversation stays under the compaction trigger.
		MaxContextTokens:   20_000,
		MaxToolResultChars: 0, // no preview truncation: the full result is stored
	}
	calls := make([]llm.ToolCall, 0, 7)
	for i := 0; i < 7; i++ {
		calls = append(calls, llm.ToolCall{
			ID:        fmt.Sprintf("c%d", i),
			Name:      fmt.Sprintf("reader_%d", i),
			Arguments: fmt.Sprintf(`{"part":%d}`, i),
		})
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "read everything"},
	}}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		body := strings.Repeat("x", resultChars)
		return ToolResult{
			Content:       body,
			SpilloverPath: "/spill/" + req.Name + ".txt",
		}, nil
	}
	loop := NewAgentLoop(cfg, executor)
	ctx := midturnAgentContext(completer, "reader_0", "reader_1", "reader_2", "reader_3", "reader_4", "reader_5", "reader_6")
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:     "You are a test assistant.",
		MaxContextTokens: cfg.MaxContextTokens,
	})

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	messages := toolMessages(conv)
	if len(messages) != 7 {
		t.Fatalf("tool messages = %d, want 7", len(messages))
	}
	pruned := 0
	for i := 0; i < len(messages)-1; i++ {
		if !strings.HasPrefix(messages[i].Content, "Pruned tool result") {
			t.Fatalf("tool message %d was not pruned: %.60q", i, messages[i].Content)
		}
		if !strings.Contains(messages[i].Content, "/spill/reader_") {
			t.Fatalf("pruned message %d lost its recovery pointer: %q", i, messages[i].Content)
		}
		pruned++
	}
	if newest := messages[len(messages)-1]; strings.HasPrefix(newest.Content, "Pruned tool result") {
		t.Fatalf("newest protected tool result was pruned: %q", newest.Content)
	}
	if got := loop.Counter("mid_turn_pruned"); got != pruned {
		t.Fatalf("mid_turn_pruned counter = %d, want %d", got, pruned)
	}
}

// Parity-exempt: the 0.6 usage gate is upstream arithmetic; this pins that the
// Go port leaves a small conversation untouched so a normal turn pays nothing.
func TestMidTurnPruneSkipsConversationsUnderTheGate(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.MaxContextTokens = 180_000
	loop := NewAgentLoop(cfg, nil)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:     "You are a test assistant.",
		MaxContextTokens: cfg.MaxContextTokens,
	})
	if _, err := conv.AddToolResult("tu_1", strings.Repeat("y", 12_000), ToolResultOptions{
		SpilloverPath: stringPtr("/spill/old.txt"),
	}); err != nil {
		t.Fatalf("AddToolResult: %v", err)
	}
	loop.maintainContextAfterBatch(conv)
	if got := len(toolMessages(conv)[0].Content); !strings.HasPrefix(toolMessages(conv)[0].Content, strings.Repeat("y", 10)) {
		t.Fatalf("small conversation was pruned: %.40q (len %d)", toolMessages(conv)[0].Content, got)
	}
	if got := loop.Counter("mid_turn_pruned"); got != 0 {
		t.Fatalf("mid_turn_pruned counter = %d, want 0", got)
	}
}

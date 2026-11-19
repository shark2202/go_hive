package agentloop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: core/tests/test_event_loop_node.py::test_soft_budget_checkpoint_rides_tail_without_stopping
func TestToolBudgetSoftCheckpointFiresBeforeHardStop(t *testing.T) {
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok " + req.Name}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"a"}`},
			{ID: "call_2", Name: "read_file", Arguments: `{"path":"b"}`},
			{ID: "call_3", Name: "read_file", Arguments: `{"path":"c"}`},
			{ID: "call_4", Name: "read_file", Arguments: `{"path":"d"}`},
		}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:        4,
		ToolCallBudget:       2,
		ToolCallHardMultiple: 5, // hard stop at 10
	}, executor)
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if len(result.ToolResults) != 4 {
		t.Fatalf("tool results = %d, want 4 (the checkpoint must not defer work)", len(result.ToolResults))
	}
	for i, record := range result.ToolResults {
		if !strings.HasPrefix(record.Content, "ok read_file") {
			t.Fatalf("call %d did not execute cleanly: %q", i, record.Content)
		}
	}
	joined := allText(completer.request(1).Messages)
	if !strings.Contains(joined, "Tool-call checkpoint") || !strings.Contains(joined, "checkpoint, not a stop") {
		t.Fatalf("soft budget checkpoint missing from the next request:\n%s", joined)
	}
	if strings.Contains(joined, "Not executed") {
		t.Fatalf("soft checkpoint deferred a call:\n%s", joined)
	}
	if got := loop.Counter("tool_budget_soft_reminder"); got != 1 {
		t.Fatalf("soft reminder counter = %d, want 1", got)
	}
}

// Parity-exempt: upstream derives the hard stop only from
// tool_call_hard_multiple; the audit pins the port's queen at 30 soft / 50 hard,
// which needs the explicit cap this test locks.
func TestQueenLoopBudgetIsThirtySoftFiftyHard(t *testing.T) {
	cfg := DefaultLoopConfig()
	if cfg.ToolCallBudget != 30 || cfg.ToolCallHardLimit != 50 {
		t.Fatalf("queen defaults = budget %d hard %d, want 30/50",
			cfg.ToolCallBudget, cfg.ToolCallHardLimit)
	}
	queen := NewAgentLoop(cfg, nil)
	if soft, hard := queen.toolBudgetLimits(); soft != 30 || hard != 50 {
		t.Fatalf("queen limits = %d/%d, want 30/50", soft, hard)
	}
	// The worker profile clears the explicit cap so its multiple rule wins
	// (30*3=90) — the two roles must not collapse into one budget.
	worker := NewAgentLoop(LoopConfig{ToolCallBudget: 30, ToolCallHardMultiple: 3}, nil)
	if soft, hard := worker.toolBudgetLimits(); soft != 30 || hard != 90 {
		t.Fatalf("worker limits = %d/%d, want 30/90", soft, hard)
	}
	pinned := NewAgentLoop(LoopConfig{ToolCallBudget: 30, ToolCallHardMultiple: 3, ToolCallHardLimit: 50}, nil)
	if _, hard := pinned.toolBudgetLimits(); hard != 50 {
		t.Fatalf("explicit hard limit ignored: %d, want 50", hard)
	}
	off := NewAgentLoop(LoopConfig{}, nil)
	if soft, hard := off.toolBudgetLimits(); soft != 0 || hard != 0 {
		t.Fatalf("budget 0 limits = %d/%d, want 0/0", soft, hard)
	}
}

// Parity-exempt: the end-to-end half of the queen profile — the soft checkpoint
// must actually fire at 30 and the hard stop must defer past 50, which is what
// the audit called "永不触发" before the budget was set.
func TestQueenBudgetCheckpointsAtThirtyAndStopsAtFifty(t *testing.T) {
	calls := make([]llm.ToolCall, 0, 51)
	for i := 0; i < 51; i++ {
		calls = append(calls, llm.ToolCall{
			ID:        fmt.Sprintf("call_%d", i),
			Name:      "read_file",
			Arguments: fmt.Sprintf(`{"path":"%d"}`, i),
		})
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "done"},
	}}
	loop := NewAgentLoop(DefaultLoopConfig(), successToolExecutor)
	ctx := midturnAgentContext(completer, "read_file")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if got := loop.Counter("tool_budget_soft_reminder"); got != 1 {
		t.Fatalf("soft checkpoint counter = %d, want 1 at the 30-call mark", got)
	}
	if got := loop.Counter("tool_budget_deferred"); got != 1 {
		t.Fatalf("deferred counter = %d, want 1 (the 51st call is past the hard stop)", got)
	}
	if len(result.ToolResults) != 51 {
		t.Fatalf("tool results = %d, want 51", len(result.ToolResults))
	}
	next := allText(completer.request(1).Messages)
	if !strings.Contains(next, "Tool-call checkpoint") || !strings.Contains(next, "hard stop at 50") {
		t.Fatalf("queen checkpoint missing or names the wrong hard stop:\n%s", next)
	}
	if !strings.Contains(result.ToolResults[50].Content, "hard stop") {
		t.Fatalf("51st call was not deferred: %q", result.ToolResults[50].Content)
	}
}

// Parity-exempt: upstream has no test for the tool_call_budget=0 gate; this
// covers the Go default (no checkpoint, no counter).
func TestToolBudgetSoftCheckpointSilentWhenUnset(t *testing.T) {
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok " + req.Name}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{}`}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, executor)
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if !strings.Contains(result.ToolResults[0].Content, "ok read_file") {
		t.Fatalf("tool did not execute: %q", result.ToolResults[0].Content)
	}
	if strings.Contains(result.ToolResults[0].Content, "Tool-call checkpoint:") {
		t.Fatalf("budget unset must not checkpoint:\n%s", result.ToolResults[0].Content)
	}
	if got := loop.Counter("tool_budget_soft_reminder"); got != 0 {
		t.Fatalf("soft reminder counter = %d, want 0", got)
	}
}

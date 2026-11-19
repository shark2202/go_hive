package agentloop

import (
	"context"
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

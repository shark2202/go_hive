package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// countingReminderSource records how often the hub renders it, so a test can
// assert a lifecycle point was consulted even when the body is compacted away.
type countingReminderSource struct {
	point ReminderPoint
	body  string
	calls int
}

func (s *countingReminderSource) Name() string { return "counting-" + string(s.point) }
func (s *countingReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{s.point}
}
func (s *countingReminderSource) AppliesTo(*ReminderAgentContext) bool { return true }
func (s *countingReminderSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.Point != s.point {
		return "", false
	}
	s.calls++
	return s.body, true
}

// Parity-exempt: upstream has no isolated test for the TOOL_BUDGET_CHECKPOINT
// wiring; this exercises the loop's existing hard-stop collect site.
func TestToolBudgetCheckpointReminderIsInjected(t *testing.T) {
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok " + req.Name}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{}`},
			{ID: "call_2", Name: "read_file", Arguments: `{"path":"b"}`},
		}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:        4,
		ToolCallBudget:       1,
		ToolCallHardMultiple: 1,
	}, executor)
	loop.Hub.Register(&countingReminderSource{point: TOOL_BUDGET_CHECKPOINT, body: "BUDGET-MARKER"})
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if got := allText(completer.request(1).Messages); !strings.Contains(got, "BUDGET-MARKER") {
		t.Fatalf("budget checkpoint reminder was not injected:\n%s", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_compact (PRE_COMPACT /
// POST_COMPACT reminder points). Upstream fires these inside _compact; the Go
// loop fires them around its LlmCompact call.
func TestCompactReminderPointsFireAroundCompaction(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "compact summary"},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	pre := &countingReminderSource{point: PRE_COMPACT, body: "PRE-COMPACT-MARKER"}
	post := &countingReminderSource{point: POST_COMPACT, body: "POST-COMPACT-MARKER"}
	loop.Hub.Register(pre)
	loop.Hub.Register(post)

	ctx := midturnAgentContext(completer)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "You are a test assistant.",
		MaxContextTokens:    120,
		CompactionThreshold: 0.8,
	})
	for i := 0; i < 6; i++ {
		if _, err := conv.AddUserMessage(strings.Repeat("context ", 80), UserMessageOptions{}); err != nil {
			t.Fatalf("AddUserMessage: %v", err)
		}
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if pre.calls != 1 {
		t.Fatalf("PRE_COMPACT fired %d times, want 1", pre.calls)
	}
	if post.calls != 1 {
		t.Fatalf("POST_COMPACT fired %d times, want 1", post.calls)
	}
	last := allText(completer.request(completer.callCount() - 1).Messages)
	if !strings.Contains(last, "POST-COMPACT-MARKER") {
		t.Fatalf("post-compact reminder was not injected:\n%s", last)
	}
}

// Parity-exempt: upstream active_workers_reminder.py ships no dedicated test;
// the body and gates are asserted directly.
func TestActiveWorkersReminderListsLiveWorkers(t *testing.T) {
	source := NewActiveWorkersReminderSource()
	ctx := &ReminderAgentContext{
		IsQueenStream: true,
		ActiveWorkersProvider: func() []ActiveWorker {
			return []ActiveWorker{{ID: "worker-1", Status: "running", Task: "scrape 200 rows"}}
		},
	}
	body, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx})
	if !ok {
		t.Fatal("expected an active-workers reminder")
	}
	for _, want := range []string{"1 parallel worker", "worker-1 [running]: scrape 200 rows"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

// Parity-exempt: Go-side gate coverage for the ported source.
func TestActiveWorkersReminderSelfSkips(t *testing.T) {
	source := NewActiveWorkersReminderSource()
	if source.AppliesTo(&ReminderAgentContext{}) {
		t.Fatal("active-workers source must be queen-only")
	}
	body, ok := source.Render(ReminderContext{
		Point:    USER_PROMPT_SUBMIT,
		AgentCtx: &ReminderAgentContext{IsQueenStream: true},
	})
	if ok || body != "" {
		t.Fatalf("expected no reminder without workers, got %q", body)
	}
}

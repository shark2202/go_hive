package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type staticReminderSource struct {
	point ReminderPoint
	body  string
}

func (s *staticReminderSource) Name() string                         { return "static" }
func (s *staticReminderSource) Points() []ReminderPoint              { return []ReminderPoint{s.point} }
func (s *staticReminderSource) AppliesTo(*ReminderAgentContext) bool { return true }
func (s *staticReminderSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.Point != s.point {
		return "", false
	}
	return s.body, true
}

func messageWith(messages []llm.ChatMessage, needles ...string) (string, bool) {
	for _, message := range messages {
		all := true
		for _, needle := range needles {
			if !strings.Contains(message.Content, needle) {
				all = false
				break
			}
		}
		if all {
			return message.Content, true
		}
	}
	return "", false
}

// Parity: core/tests/test_reminder_e2e.py::test_session_start_reminder_reaches_the_model
func TestSessionStartReminderReachesTheModel(t *testing.T) {
	const snapshot = "Here are the existing tasks:\n- [ ] Verify the sheet entries\n- [ ] Search X for recent posts"

	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "all done"}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Hub.Register(&staticReminderSource{point: SESSION_START, body: snapshot})
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	first := allText(completer.request(0).Messages)
	for _, want := range []string{
		"<system-reminder>",
		"Here are the existing tasks:",
		"Verify the sheet entries",
		"Search X for recent posts",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("first call missing %q:\n%s", want, first)
		}
	}
}

// Parity: core/tests/test_reminder_e2e.py::test_post_tool_use_reminder_rides_the_tool_result
func TestPostToolUseReminderRidesTheToolResult(t *testing.T) {
	const snapshot = "Here are the existing tasks:\n- [ ] Record results in the sheet"

	executor := func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == "task_create" {
			return ToolResult{Content: "Created task #1"}, nil
		}
		return ToolResult{Content: "Result for " + req.Name}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Name: "task_create", Arguments: `{"subject": "Record results in the sheet"}`}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5}, executor)
	loop.Hub.Register(&staticReminderSource{point: POST_TOOL_USE, body: snapshot})
	ctx := midturnAgentContext(completer, "task_create")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	if _, ok := messageWith(completer.request(1).Messages,
		"Created task #1", "<system-reminder>", "Here are the existing tasks:", "Record results in the sheet"); !ok {
		t.Fatalf("the reminder was not appended to the tool result tail:\n%s",
			allText(completer.request(1).Messages))
	}
}

// Parity: core/tests/test_reminder_e2e.py::test_update_only_turn_does_not_re_emit_snapshot
func TestUpdateOnlyTurnDoesNotReEmitSnapshot(t *testing.T) {
	t.Skip("skipped: depends on the task-store TaskReminderSource's update-only suppression, which is outside Lane G")
}

// Parity: core/tests/test_reminder_e2e.py::test_counter_ticks_per_inner_turn_not_per_outer_iteration
func TestCounterTicksPerInnerTurnNotPerOuterIteration(t *testing.T) {
	t.Skip("skipped: depends on the task-store reminder drift counter persisted to reminder_state.json, which is outside Lane G")
}

// Parity: core/tests/test_reminder_e2e.py::test_no_task_reminder_when_agent_lacks_task_tools
func TestNoTaskReminderWhenAgentLacksTaskTools(t *testing.T) {
	t.Skip("skipped: depends on TaskReminderSource.applies_to gating by task-tool presence, which is outside Lane G")
}

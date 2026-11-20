package agentloop

// Ask_user turn semantics: a successful ask_user call ends the turn at the
// tool batch. Upstream's _run_turn_loop returns on user_input_requested before
// streaming again, so the ask turn costs exactly one model call; a failed
// ask_user stays a normal tool error and the turn keeps going. These pin the
// Go port's early return so the ask turn cannot drift back into an extra call
// (the sse-payload-parity probe caught it as one extra client_output_delta +
// llm_turn_complete).

import (
	"context"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

func askUserCall(id string) llm.ToolCall {
	return llm.ToolCall{
		ID:        id,
		Name:      AskUserToolName,
		Arguments: `{"questions":[{"id":"next","prompt":"Which next?","options":["Ship it","Wait"]}]}`,
	}
}

func conversationHas(messages []llm.ChatMessage, role, needle string) bool {
	for _, message := range messages {
		if message.Role == role && strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

// Parity: core/tests/test_event_loop_node.py::test_client_input_requested_event_published
// Parity: core/tests/test_event_loop_node.py::test_queen_ask_user_with_real_tools
func TestAskUserTurnEndsAtTheToolBatchWithoutAnotherModelCall(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "tool-1", Name: "search", Arguments: "{}"},
			askUserCall("ask-1"),
		}},
		{content: "Answered."},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5}, func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == AskUserToolName {
			return ToolResult{Content: AskUserWaitingContent}, nil
		}
		return ToolResult{Content: "search result"}, nil
	})
	ctx := midturnAgentContext(completer, "search", AskUserToolName)
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start the work", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if calls := completer.callCount(); calls != 1 {
		t.Fatalf("model calls in the ask_user turn = %d, want 1 (upstream returns on user_input_requested)", calls)
	}
	if result.InnerTurns != 1 {
		t.Fatalf("inner turns = %d, want 1", result.InnerTurns)
	}
	if result.FinalText != "" {
		t.Fatalf("final text = %q, want empty: the turn ended on the question, not a model answer", result.FinalText)
	}

	// Both calls ran, in order, before the turn returned; the ask_user result
	// is what the answer turn will read.
	var toolResults []Message
	for _, message := range conv.Messages() {
		if message.Role == RoleTool {
			toolResults = append(toolResults, message)
		}
	}
	if len(toolResults) != 2 {
		t.Fatalf("tool results = %#v, want the real tool's and the ask_user wait", toolResults)
	}
	if toolResults[0].Content != "search result" {
		t.Fatalf("first tool result = %#v, want the real tool's result", toolResults[0])
	}
	if toolResults[1].Content != AskUserWaitingContent {
		t.Fatalf("second tool result = %#v, want the ask_user wait", toolResults[1])
	}

	// The answer is the next turn: one more call, with the question's tool
	// result in front of the model.
	if _, err := conv.AddUserMessage("Ship it", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage answer: %v", err)
	}
	resumed, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("resumed ExecuteTurn: %v", err)
	}
	if resumed.FinalText != "Answered." {
		t.Fatalf("resumed final text = %q, want the scripted answer", resumed.FinalText)
	}
	if calls := completer.callCount(); calls != 2 {
		t.Fatalf("model calls after the answer = %d, want 2 (ask turn + answer turn)", calls)
	}
	if !conversationHas(completer.request(1).Messages, RoleTool, AskUserWaitingContent) {
		t.Fatalf("answer turn request missing the ask_user tool result: %#v", completer.request(1).Messages)
	}
	if !conversationHas(completer.request(1).Messages, RoleUser, "Ship it") {
		t.Fatalf("answer turn request missing the user's answer: %#v", completer.request(1).Messages)
	}
}

// Parity: core/tests/test_event_loop_node.py::test_client_input_requested_event_published
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
func TestFailedAskUserKeepsTheTurnGoing(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{askUserCall("ask-1")}},
		{content: "corrected"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5}, func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ERROR: ask_user requires a non-empty 'questions' array.", IsError: true}, nil
	})
	ctx := midturnAgentContext(completer, AskUserToolName)
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start the work", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	// Upstream sets user_input_requested=False for an invalid ask_user, so the
	// model gets the error and may fix it in the same turn.
	if calls := completer.callCount(); calls != 2 {
		t.Fatalf("model calls after a failed ask_user = %d, want 2", calls)
	}
	if result.FinalText != "corrected" || result.InnerTurns != 2 {
		t.Fatalf("result = %+v, want the second call's text after 2 inner turns", result)
	}
}

// Parity: core/tests/test_event_loop_node.py::test_tool_execution_feedback
func TestOrdinaryToolTurnStillCallsTheModelAgain(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "tool-1", Name: "search", Arguments: "{}"}}},
		{content: "final answer"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5}, func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "search result"}, nil
	})
	ctx := midturnAgentContext(completer, "search")
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("use the tool", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if result.FinalText != "final answer" || result.InnerTurns != 2 {
		t.Fatalf("result = %+v, want a second model call after the tool batch", result)
	}
	if calls := completer.callCount(); calls != 2 {
		t.Fatalf("model calls in a normal tool turn = %d, want 2", calls)
	}
}

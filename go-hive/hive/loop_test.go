package hive

import (
	"context"
	"strings"
	"testing"
)

type textModel struct {
	content string
	calls   int
}

func (m *textModel) AComplete(_ context.Context, _ CompletionRequest) (Completion, error) {
	m.calls++
	return Completion{Content: m.content, StopReason: "stop"}, nil
}

// Parity: core/tests/test_public_api.py::test_every_primitive_resolves
func TestLoopRunBuildsTheConversationFromTheSystemPrompt(t *testing.T) {
	model := &textModel{content: "Done."}
	loop := NewLoop(DefaultLoopConfig(), nil)
	result, err := loop.Run(context.Background(), &AgentContext{
		AgentID:      "scout",
		AgentSpec:    NewAgentSpec("scout", "Scout", "Looks around"),
		LLM:          model,
		SystemPrompt: "Look outside.",
		InputData:    map[string]any{"place": "street"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Success || result.FinalText != "Done." {
		t.Fatalf("result = %+v, want success with the model text", result)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
}

// Parity: framework/agent_loop/conversation.py::Conversation
func TestConversationTracksTurns(t *testing.T) {
	conv := NewConversation("You are a test assistant.")
	if conv.SystemPrompt() != "You are a test assistant." {
		t.Fatalf("SystemPrompt = %q", conv.SystemPrompt())
	}
	if _, err := conv.AddUserMessage("hello"); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if _, err := conv.AddAssistantMessage("hi", nil); err != nil {
		t.Fatalf("AddAssistantMessage: %v", err)
	}
	if conv.MessageCount() != 2 {
		t.Fatalf("MessageCount = %d, want 2", conv.MessageCount())
	}
	if conv.LastAssistantText() != "hi" {
		t.Fatalf("LastAssistantText = %q", conv.LastAssistantText())
	}
	if conv.EstimateTokens() <= 0 {
		t.Fatalf("EstimateTokens = %d, want positive", conv.EstimateTokens())
	}
	if err := conv.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if conv.MessageCount() != 0 {
		t.Fatalf("MessageCount after Clear = %d", conv.MessageCount())
	}
}

// Parity-exempt: Go-only nil-safety checks on the facade.
func TestLoopRejectsNilInputs(t *testing.T) {
	loop := NewLoop(DefaultLoopConfig(), nil)
	if _, err := loop.ExecuteTurn(context.Background(), nil, NewConversation("s"), nil); err == nil {
		t.Fatal("ExecuteTurn with a nil agent context returned no error")
	}
	ctx := &AgentContext{AgentID: "a", AgentSpec: NewAgentSpec("a", "A", "A"), LLM: &textModel{}}
	if _, err := loop.ExecuteTurn(context.Background(), ctx, nil, nil); err == nil {
		t.Fatal("ExecuteTurn with a nil conversation returned no error")
	}
	if _, err := (*Loop)(nil).ExecuteTurn(context.Background(), ctx, NewConversation("s"), nil); err == nil {
		t.Fatal("ExecuteTurn on a nil loop returned no error")
	}
	if _, err := loop.Run(context.Background(), nil); err == nil {
		t.Fatal("Run with a nil agent context returned no error")
	}
}

// Parity: framework/agent_loop/reminders.py ParkReason
func TestParkReasonConstantsAreWireStrings(t *testing.T) {
	cases := map[ParkReason]string{
		ParkAskUser:      "ask_user",
		ParkTurnDone:     "turn_done",
		ParkUserStopped:  "user_stopped",
		ParkLLMError:     "llm_error",
		ParkEmptyReplies: "empty_responses",
		ParkDoomLoop:     "doom_loop",
	}
	for reason, want := range cases {
		if string(reason) != want {
			t.Fatalf("park reason = %q, want %q", reason, want)
		}
	}
}

// Parity: framework/agent_loop/agent_loop.py::_build_initial_message
func TestInputDataReachesTheTaskFrame(t *testing.T) {
	model := &textModel{content: "ok"}
	loop := NewLoop(DefaultLoopConfig(), nil)
	conv := NewConversation("system")
	result, err := loop.ExecuteTurn(context.Background(), &AgentContext{
		AgentID:   "scout",
		AgentSpec: NewAgentSpec("scout", "Scout", "Looks around"),
		LLM:       model,
		InputData: map[string]any{"place": "street"},
	}, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("result = %+v", result)
	}
	messages := conv.Messages()
	if len(messages) == 0 || !strings.Contains(messages[0].Content, "place: street") {
		t.Fatalf("first message = %#v, want the input-data frame", messages)
	}
}

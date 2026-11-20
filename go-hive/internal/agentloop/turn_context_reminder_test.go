package agentloop

// Parity: framework/agent_loop/turn_context_reminder.py

import (
	"strings"
	"testing"
)

// TestTurnContextReminderRendersOnlyRealText pins the render guard: no provider,
// a blank message and a blank package reply all produce nothing, and a real
// reply is trimmed.
//
// Parity: framework/server/tests/test_turn_context.py::test_only_queens_with_the_provider_wired_are_asked
func TestTurnContextReminderRendersOnlyRealText(t *testing.T) {
	source := NewTurnContextReminderSource()
	if source.Name() != "turn_context" {
		t.Fatalf("source name = %q", source.Name())
	}
	points := source.Points()
	if len(points) != 2 || points[0] != SESSION_START || points[1] != USER_PROMPT_SUBMIT {
		t.Fatalf("points = %v, want session_start + user_prompt_submit", points)
	}

	queen := func(provider func(string) string) *ReminderAgentContext {
		return &ReminderAgentContext{IsQueenStream: true, TurnContextProvider: provider}
	}

	if text, ok := source.Render(ReminderContext{AgentCtx: queen(nil), UserText: "hi"}); ok || text != "" {
		t.Fatalf("nil provider rendered %q (ok=%v)", text, ok)
	}
	if text, ok := source.Render(ReminderContext{AgentCtx: queen(func(string) string { return "x" }), UserText: "   "}); ok || text != "" {
		t.Fatalf("blank message rendered %q (ok=%v)", text, ok)
	}
	if text, ok := source.Render(ReminderContext{AgentCtx: queen(func(string) string { return "  " }), UserText: "hi"}); ok || text != "" {
		t.Fatalf("blank reply rendered %q (ok=%v)", text, ok)
	}

	var asked string
	text, ok := source.Render(ReminderContext{
		AgentCtx: queen(func(message string) string {
			asked = message
			return "  <voice_examples>…</voice_examples>  "
		}),
		UserText: "  今天有点累  ",
	})
	if !ok {
		t.Fatal("real reply should render")
	}
	if asked != "今天有点累" {
		t.Fatalf("provider asked with %q, want the trimmed message", asked)
	}
	if strings.TrimSpace(text) != "<voice_examples>…</voice_examples>" || text != strings.TrimSpace(text) {
		t.Fatalf("rendered %q, want the trimmed reply", text)
	}
}

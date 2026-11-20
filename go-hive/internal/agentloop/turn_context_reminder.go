// Turn context: what installed turn-context packages want the queen to see.
//
// Parity: framework/agent_loop/turn_context_reminder.py
package agentloop

import "strings"

// turnContextProviderFrom adapts the AgentContext.TurnContextProvider slot,
// which the server fills with the started packages' aggregator
// (server.TurnContextRuntime.ForSession). It lives here rather than in loop.go
// so the loop file stays inside its size budget.
//
// Parity: framework/agent_loop/types.py AgentContext.turn_context_provider
func turnContextProviderFrom(provider any) func(string) string {
	switch p := provider.(type) {
	case nil:
		return nil
	case func(string) string:
		return p
	case func(string) (string, bool):
		return func(message string) string {
			text, ok := p(message)
			if !ok {
				return ""
			}
			return text
		}
	default:
		return nil
	}
}

// TurnContextReminderSource puts a turn-context package's text ahead of each
// user turn. Fires at SESSION_START (the session's first message) and
// USER_PROMPT_SUBMIT, queen-only, when the agent context carries a
// turn-context provider (see server.StartTurnContexts). Silent when no package
// has anything to add.
//
// Parity: framework/agent_loop/turn_context_reminder.py::TurnContextReminderSource
type TurnContextReminderSource struct{}

func NewTurnContextReminderSource() *TurnContextReminderSource {
	return &TurnContextReminderSource{}
}

// Name is the reminder source id ("turn_context" upstream).
func (s *TurnContextReminderSource) Name() string { return "turn_context" }

// Points mirrors upstream points():
// {SESSION_START, USER_PROMPT_SUBMIT}.
func (s *TurnContextReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{SESSION_START, USER_PROMPT_SUBMIT}
}

// AppliesTo mirrors upstream applies_to: a queen stream with the provider
// wired. A worker stream or a nil provider stays silent.
func (s *TurnContextReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	return agentCtx.IsQueenStream && agentCtx.TurnContextProvider != nil
}

// Render asks the provider for this turn's text. An empty (or whitespace-only)
// message yields nothing, matching upstream's blank guard; the provider itself
// bounds each package's time and swallows its failures.
func (s *TurnContextReminderSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.AgentCtx == nil || rctx.AgentCtx.TurnContextProvider == nil {
		return "", false
	}
	text := strings.TrimSpace(rctx.UserText)
	if text == "" {
		return "", false
	}
	body := strings.TrimSpace(rctx.AgentCtx.TurnContextProvider(text))
	if body == "" {
		return "", false
	}
	return body, true
}

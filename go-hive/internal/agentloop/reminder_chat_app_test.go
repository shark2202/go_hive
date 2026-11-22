package agentloop

// The chat-app provider's trip from the agent context into the reminder view,
// where the tasks source's chat-app gate reads it.
//
// Parity: core/framework/agent_loop/types.py (AgentContext.chat_app_provider)
// Parity: core/framework/tasks/reminders.py::TaskReminderSource._drift_kind

import "testing"

// Parity: core/framework/agent_loop/reminders.py (ReminderContext.agent_ctx
// exposes chat_app_provider to the sources)
func TestReminderAgentContextCarriesTheChatAppProvider(t *testing.T) {
	ctx := NewReminderAgentContext(&AgentContext{
		ChatAppProvider: func() any { return "queens" },
	})
	if ctx.ChatAppProvider == nil {
		t.Fatal("ChatAppProvider is nil, want the AgentContext provider")
	}
	if got := ctx.ChatAppProvider(); got != "queens" {
		t.Fatalf("ChatAppProvider() = %#v, want queens", got)
	}

	// The string-returning shape reads the same way.
	asString := NewReminderAgentContext(&AgentContext{
		ChatAppProvider: func() string { return "onebot" },
	})
	if asString.ChatAppProvider == nil {
		t.Fatal("ChatAppProvider is nil, want the string provider")
	}
	if got := asString.ChatAppProvider(); got != "onebot" {
		t.Fatalf("ChatAppProvider() = %#v, want onebot", got)
	}

	empty := NewReminderAgentContext(&AgentContext{})
	if empty.ChatAppProvider != nil {
		t.Fatal("ChatAppProvider without a source is set, want nil")
	}
}

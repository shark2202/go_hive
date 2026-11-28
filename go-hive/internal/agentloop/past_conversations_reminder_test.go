package agentloop

import (
	"strings"
	"testing"
	"time"
)

func pastConversationsContext(provider func(string) string) *ReminderAgentContext {
	return &ReminderAgentContext{IsQueenStream: true, PastConversationRecallProvider: provider}
}

// Parity: core/framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource.render
// (ported from core/tests/test_memory_timeline_recall.py::test_reminder_passes_the_user_message_to_the_provider).
func TestPastConversationsReminderPassesTheUserMessageToTheProvider(t *testing.T) {
	seen := []string{}
	ctx := pastConversationsContext(func(text string) string {
		seen = append(seen, text)
		return "Excerpts: commute is 45 minutes"
	})

	body, ok := NewPastConversationsReminderSource().Render(ReminderContext{
		Point:    USER_PROMPT_SUBMIT,
		AgentCtx: ctx,
		UserText: "How long is my commute?",
	})
	if !ok || body != "Excerpts: commute is 45 minutes" {
		t.Fatalf("body = %q, ok = %v, want the provider's excerpts", body, ok)
	}
	if len(seen) != 1 || seen[0] != "How long is my commute?" {
		t.Fatalf("provider saw %v, want the user's message", seen)
	}
}

// Parity: core/framework/agent_loop/past_conversations_reminder.py::RECALL_TIMEOUT_S
// (ported from core/tests/test_memory_timeline_recall.py::test_reminder_skips_short_messages_non_queens_and_slow_providers).
func TestPastConversationsRecallTimeoutDefaultAndOverride(t *testing.T) {
	t.Setenv("HIVE_PAST_RECALL_TIMEOUT", "")
	if got := pastConversationsRecallTimeoutFromEnv(); got != 3*time.Second {
		t.Fatalf("default recall timeout = %s, want 3s", got)
	}
	t.Setenv("HIVE_PAST_RECALL_TIMEOUT", "0.5")
	if got := pastConversationsRecallTimeoutFromEnv(); got != 500*time.Millisecond {
		t.Fatalf("overridden recall timeout = %s, want 500ms", got)
	}
}

// Parity: core/framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource.render
// (ported from core/tests/test_memory_timeline_recall.py::test_reminder_skips_short_messages_non_queens_and_slow_providers).
func TestPastConversationsReminderSkipsShortMessagesNonQueensAndSlowProviders(t *testing.T) {
	source := NewPastConversationsReminderSource()
	calls := []string{}
	ctx := pastConversationsContext(func(text string) string {
		calls = append(calls, text)
		return "x"
	})
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "ok"}); ok {
		t.Fatal("a short message must not recall")
	}
	if len(calls) != 0 {
		t.Fatalf("provider saw %v for a short message", calls)
	}
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "1234567"}); ok {
		t.Fatal("a 7-character message must not recall")
	}
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "12345678"}); !ok {
		t.Fatal("an 8-character message must recall")
	}
	if len(calls) != 1 || calls[0] != "12345678" {
		t.Fatalf("provider saw %v, want only the 8-character message", calls)
	}
	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: false, PastConversationRecallProvider: func(string) string { return "x" }}) {
		t.Fatal("a non-queen stream must not apply")
	}
	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: true}) {
		t.Fatal("a queen without a recall provider must not apply")
	}

	release := make(chan struct{})
	slow := pastConversationsContext(func(string) string {
		<-release
		return "late"
	})
	previous := pastConversationsRecallTimeout
	pastConversationsRecallTimeout = 50 * time.Millisecond
	defer func() { pastConversationsRecallTimeout = previous }()

	started := time.Now()
	body, ok := source.Render(ReminderContext{
		Point:    SESSION_START,
		AgentCtx: slow,
		UserText: "What did I buy last week?",
	})
	elapsed := time.Since(started)
	close(release)
	if ok || body != "" {
		t.Fatalf("a slow provider must be dropped, got %q", body)
	}
	if elapsed >= time.Second {
		t.Fatalf("render took %s; the recall wait must be capped", elapsed)
	}
}

// Parity: core/framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource.render
// (the Exception branch: a failing provider is a failed recall, never a failed turn).
func TestPastConversationsReminderToleratesProviderFailure(t *testing.T) {
	source := NewPastConversationsReminderSource()
	ctx := pastConversationsContext(func(string) string { panic("recall exploded") })
	body, ok := source.Render(ReminderContext{
		Point:    USER_PROMPT_SUBMIT,
		AgentCtx: ctx,
		UserText: "How long is my commute?",
	})
	if ok || body != "" {
		t.Fatalf("a panicking provider must yield nothing, got %q", body)
	}
}

// Parity: core/framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource.points
// (ported from core/tests/test_memory_timeline_recall.py::test_hub_hands_user_text_to_sources).
func TestReminderHubHandsUserTextToPastConversationsSource(t *testing.T) {
	hub := NewReminderHub()
	hub.Register(NewPastConversationsReminderSource())
	ctx := pastConversationsContext(func(text string) string {
		return "related to: " + text
	})

	block := hub.Fire(USER_PROMPT_SUBMIT, ctx, nil, "Where did I meet Sophia?")
	if !strings.Contains(block, "related to: Where did I meet Sophia?") {
		t.Fatalf("block = %q, want the user's message handed to the source", block)
	}
	if !strings.Contains(block, "<system-reminder>") || !strings.Contains(block, "</system-reminder>") {
		t.Fatalf("block = %q, want the shared reminder envelope", block)
	}
}

// Parity: core/framework/agent_loop/types.py AgentContext.past_conversation_recall_provider
func TestNewReminderAgentContextCarriesPastConversationRecallProvider(t *testing.T) {
	provider := func(string) string { return "excerpts" }
	ctx := NewReminderAgentContext(&AgentContext{PastConversationRecallProvider: provider})
	if ctx.PastConversationRecallProvider == nil || ctx.PastConversationRecallProvider("question") != "excerpts" {
		t.Fatal("a callable AgentContext provider must reach the reminder view")
	}

	unreadable := NewReminderAgentContext(&AgentContext{PastConversationRecallProvider: "not a callable"})
	if unreadable.PastConversationRecallProvider != nil {
		t.Fatal("an unreadable provider slot must read as not wired")
	}
}

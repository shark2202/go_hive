package agentloop

// Past-conversations recall: excerpts of earlier conversations related to the
// user's message, placed before the queen answers. Without it, recalling
// history depends on the queen deciding to call search_messages.
//
// Parity: framework/agent_loop/past_conversations_reminder.py

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Messages shorter than this ("ok", "thanks!") carry no topic to recall by.
//
// Parity: framework/agent_loop/past_conversations_reminder.py::MIN_MESSAGE_CHARS
const pastConversationsMinMessageChars = 8

// Retrieval runs before the turn starts, so it must never hold a turn
// hostage: past this, the turn proceeds without excerpts. A query takes tens
// of milliseconds; what can run long is a first build of the index, which
// keeps going in its goroutine, so only that turn misses out.
//
// Parity: framework/agent_loop/past_conversations_reminder.py::RECALL_TIMEOUT_S
var pastConversationsRecallTimeout = pastConversationsRecallTimeoutFromEnv()

// pastConversationsRecallTimeoutFromEnv reads HIVE_PAST_RECALL_TIMEOUT, with
// upstream's 3s default; an unset or malformed value keeps the default.
//
// Parity: framework/agent_loop/past_conversations_reminder.py::RECALL_TIMEOUT_S
func pastConversationsRecallTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("HIVE_PAST_RECALL_TIMEOUT"))
	if raw == "" {
		return 3 * time.Second
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || seconds <= 0 {
		return 3 * time.Second
	}
	return time.Duration(seconds * float64(time.Second))
}

// pastConversationRecallProviderFrom adapts AgentContext's dynamic provider
// slot to the reminder view's typed reader. A nil or unreadable provider reads
// as "not wired", exactly as upstream's callable check does.
//
// Parity: framework/agent_loop/types.py AgentContext.past_conversation_recall_provider
func pastConversationRecallProviderFrom(provider any) func(string) string {
	switch p := provider.(type) {
	case nil:
		return nil
	case func(string) string:
		return p
	default:
		return nil
	}
}

// PastConversationsReminderSource injects related past-conversation excerpts
// ahead of each user turn.
//
// Parity: framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource
type PastConversationsReminderSource struct{}

func NewPastConversationsReminderSource() *PastConversationsReminderSource {
	return &PastConversationsReminderSource{}
}

// Name is the reminder source id ("past_conversations" upstream).
func (s *PastConversationsReminderSource) Name() string { return "past_conversations" }

// Points mirrors upstream points():
// {SESSION_START, USER_PROMPT_SUBMIT}.
func (s *PastConversationsReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{SESSION_START, USER_PROMPT_SUBMIT}
}

// AppliesTo mirrors upstream applies_to: a queen stream with the recall
// provider wired. A worker stream or a nil provider stays silent.
func (s *PastConversationsReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	return agentCtx.IsQueenStream && agentCtx.PastConversationRecallProvider != nil
}

// Render asks the provider for excerpts related to this turn's message. The
// hub wraps the body in the shared <system-reminder> envelope; a short
// message, a failed recall, a timeout, or an empty result yields nothing.
//
// Parity: framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource.render
func (s *PastConversationsReminderSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.AgentCtx == nil || rctx.AgentCtx.PastConversationRecallProvider == nil {
		return "", false
	}
	text := strings.TrimSpace(rctx.UserText)
	if len([]rune(text)) < pastConversationsMinMessageChars {
		return "", false
	}
	body, ok := callPastConversationRecall(rctx.AgentCtx.PastConversationRecallProvider, text)
	if !ok {
		return "", false
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	return body, true
}

// callPastConversationRecall waits up to the recall cap for the provider. Past
// the cap the turn proceeds without excerpts (the provider keeps running in
// its goroutine); a panicking provider is a failed recall, never a failed
// turn.
//
// Parity: framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource.render
// (asyncio.wait_for + asyncio.to_thread, with its TimeoutError/Exception
// branches).
func callPastConversationRecall(provider func(string) string, text string) (string, bool) {
	type outcome struct {
		body string
		ok   bool
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- outcome{}
			}
		}()
		done <- outcome{body: provider(text), ok: true}
	}()
	select {
	case result := <-done:
		return result.body, result.ok
	case <-time.After(pastConversationsRecallTimeout):
		return "", false
	}
}

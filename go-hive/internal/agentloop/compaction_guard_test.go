package agentloop

// Regression tests for the two compaction stages the port was missing (A3):
// step 0a (oversized historical user messages) and the LLM-compaction circuit
// breaker.
//
// Parity: framework/agent_loop/internals/compaction.py::compact
// (truncate_oversized_user_messages, MAX_CONSECUTIVE_FAILURES)

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// compactionTestLoop returns a loop whose conversation budget is small enough
// that a few-KB message needs compaction. The buffers are zeroed so the legacy
// multiplicative threshold rule applies.
func compactionTestLoop() *AgentLoop {
	config := DefaultLoopConfig()
	config.MaxContextTokens = 1000
	config.CompactionBufferTokens = 0
	config.CompactionBufferRatio = 0
	config.CompactionWarningBufferTokens = 0
	return NewAgentLoop(config, successToolExecutor)
}

func compactionTestConversation(t *testing.T) *NodeConversation {
	t.Helper()
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    1000,
		CompactionThreshold: 0.5,
	})
	if _, err := conv.AddUserMessage(strings.Repeat("x", 5000), UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if !conv.NeedsCompaction() {
		t.Fatal("test conversation does not need compaction")
	}
	return conv
}

// Parity: framework/agent_loop/internals/compaction.py::truncate_oversized_user_messages
//
// Step 0a clips an oversized historical message (head + notice + tail) before
// any compaction prompt is built. The pass is entered only when the
// conversation is over budget, and the clipped text is what the summarizer
// sees; it is idempotent, so a second pass is a no-op.
func TestCompactionTruncatesOversizedHistoricalUserMessage(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{MaxContextTokens: 1000}, successToolExecutor)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    1000,
		CompactionThreshold: 0.5,
	})
	original := strings.Repeat("A", 150_000) + "TAIL-MARKER"
	if _, err := conv.AddUserMessage(original, UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if !conv.NeedsCompaction() {
		t.Fatal("test setup: the oversized message does not need compaction")
	}
	summarizer := &compactFake{respond: func(string, int) (string, error) {
		return "summary", nil
	}}
	agentCtx := judgeTestContext(summarizer, nil)

	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))

	if got := summarizer.callCount(); got != 1 {
		t.Fatalf("summarizer calls = %d, want 1", got)
	}
	prompt := summarizer.request(0).Messages[0].Content
	if !strings.Contains(prompt, "chars truncated to keep compaction safe") {
		t.Fatalf("compaction prompt lacks the truncation notice: %.400s", prompt)
	}
	if !strings.Contains(prompt, "TAIL-MARKER") {
		t.Fatal("compaction prompt dropped the tail (attachment path recovery)")
	}
	if got := loop.Counter(oversizedUserMessageClippedStat); got != 1 {
		t.Fatalf("stats[%s] = %d, want 1", oversizedUserMessageClippedStat, got)
	}
	// The summary replaced the clipped history (the summarizer ran), so the
	// retained transcript is the summary message alone.
	messages := conv.Messages()
	if len(messages) != 1 || messages[0].Content != "summary" {
		t.Fatalf("messages after compaction = %#v, want the single summary", messages)
	}

	// Idempotent: the summary already fits under the threshold.
	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))
	if got := loop.Counter(oversizedUserMessageClippedStat); got != 1 {
		t.Fatalf("second pass clipped again: stats[%s] = %d", oversizedUserMessageClippedStat, got)
	}
	if got := summarizer.callCount(); got != 1 {
		t.Fatalf("second pass called the summarizer: %d calls", got)
	}
}

// Parity: framework/agent_loop/internals/compaction.py::compact
// (circuit breaker: _failure_counts >= MAX_CONSECUTIVE_FAILURES)
//
// A persistent summarizer outage must stop after three consecutive failures
// instead of burning one doomed LLM call per iteration.
func TestCompactionCircuitBreakerStopsAfterConsecutiveFailures(t *testing.T) {
	loop := compactionTestLoop()
	conv := compactionTestConversation(t)
	failing := &compactFake{respond: func(string, int) (string, error) {
		return "", errors.New("provider down")
	}}
	agentCtx := judgeTestContext(failing, nil)

	for i := 0; i < DefaultMaxConsecutiveCompactionFailures; i++ {
		loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))
	}
	if got := failing.callCount(); got != DefaultMaxConsecutiveCompactionFailures {
		t.Fatalf("summarizer calls = %d, want %d", got, DefaultMaxConsecutiveCompactionFailures)
	}
	if got := loop.Counter(compactionFailureCounter); got != DefaultMaxConsecutiveCompactionFailures {
		t.Fatalf("stats[%s] = %d, want %d", compactionFailureCounter, got, DefaultMaxConsecutiveCompactionFailures)
	}

	// The breaker is open: another over-budget iteration asks no model.
	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))
	if got := failing.callCount(); got != DefaultMaxConsecutiveCompactionFailures {
		t.Fatalf("summarizer called with the circuit open: %d calls", got)
	}
	if got := loop.Counter(compactionCircuitOpenCounter); got != 1 {
		t.Fatalf("stats[%s] = %d, want 1", compactionCircuitOpenCounter, got)
	}
}

// Parity: framework/agent_loop/internals/compaction.py::compact (_record_success)
//
// A successful summary closes the breaker again, so a recovered provider is
// used on the next over-budget iteration.
func TestCompactionSuccessResetsCircuitBreaker(t *testing.T) {
	loop := compactionTestLoop()
	conv := compactionTestConversation(t)
	var fail = true
	summarizer := &compactFake{respond: func(string, int) (string, error) {
		if fail {
			return "", errors.New("transient outage")
		}
		return "summary", nil
	}}
	agentCtx := judgeTestContext(summarizer, nil)

	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))
	if loop.compactionFailures != 1 {
		t.Fatalf("compactionFailures = %d, want 1", loop.compactionFailures)
	}
	fail = false
	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))
	if loop.compactionFailures != 0 {
		t.Fatalf("compactionFailures = %d after success, want 0", loop.compactionFailures)
	}
	if got := summarizer.callCount(); got != 2 {
		t.Fatalf("summarizer calls = %d, want 2", got)
	}
}

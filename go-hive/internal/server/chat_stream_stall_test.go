package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// blockingStreamCompleter is a StreamCompleter whose stream emits a fixed
// number of deltas and then blocks until its context is cancelled — the
// deterministic stand-in for a hung upstream.
type blockingStreamCompleter struct {
	eventsBeforeBlock int
}

func (s *blockingStreamCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	return s.ACompleteStream(ctx, req, func(llm.StreamEvent) {})
}

func (s *blockingStreamCompleter) ACompleteStream(ctx context.Context, _ llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	for i := 0; i < s.eventsBeforeBlock; i++ {
		emit(llm.TextDeltaEvent{Content: "tok", Snapshot: "tok"})
	}
	<-ctx.Done()
	return llm.Completion{}, ctx.Err()
}

func newWatchdogAdjustedCompleter(t *testing.T, streamer llm.Completer, ttft, interEvent time.Duration) *chatStreamingCompleter {
	t.Helper()
	completer := newChatStreamingCompleter(streamer)
	if completer == nil {
		t.Fatal("completer does not implement StreamCompleter")
	}
	completer.setStreamWatchdog(ttft, interEvent)
	completer.beginTurn("sys", func(string, string, int) {})
	t.Cleanup(completer.endTurn)
	return completer
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (the "ttft"
// watchdog verdict: stream open but no first event).
func TestStreamWatchdogTripsOnTTFT(t *testing.T) {
	completer := newWatchdogAdjustedCompleter(t, &blockingStreamCompleter{}, 30*time.Millisecond, time.Second)

	_, err := completer.AComplete(context.Background(), llm.CompletionRequest{System: "sys"})
	var stalled *llm.StreamStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("AComplete error = %v, want a stream-stalled verdict", err)
	}
	if stalled.Reason != "ttft" {
		t.Fatalf("reason = %q, want ttft", stalled.Reason)
	}
	if stalled.LimitSeconds <= 0 {
		t.Fatalf("limit = %v, want the configured budget", stalled.LimitSeconds)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (the "inactive"
// verdict: events arrived, then the stream went silent).
func TestStreamWatchdogTripsOnInactivityAfterFirstEvent(t *testing.T) {
	completer := newWatchdogAdjustedCompleter(t, &blockingStreamCompleter{eventsBeforeBlock: 1}, time.Second, 30*time.Millisecond)

	_, err := completer.AComplete(context.Background(), llm.CompletionRequest{System: "sys"})
	var stalled *llm.StreamStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("AComplete error = %v, want a stream-stalled verdict", err)
	}
	if stalled.Reason != "inactive" {
		t.Fatalf("reason = %q, want inactive", stalled.Reason)
	}
}

// Parity-exempt: the Go watchdog's opt-out (both budgets zero) keeps the
// pre-existing streaming behaviour byte-for-byte; asserted directly because
// upstream derives it from its LoopConfig defaults instead of a zero budget.
func TestStreamWatchdogDisabledByZeroBudgets(t *testing.T) {
	streamer := &instantStreamCompleter{}
	completer := newWatchdogAdjustedCompleter(t, streamer, 0, 0)

	completion, err := completer.AComplete(context.Background(), llm.CompletionRequest{System: "sys"})
	if err != nil {
		t.Fatalf("AComplete: %v", err)
	}
	if completion.Content != "done" {
		t.Fatalf("completion = %+v, want the streamed answer", completion)
	}
}

type instantStreamCompleter struct{}

func (s *instantStreamCompleter) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{Content: "done"}, nil
}

func (s *instantStreamCompleter) ACompleteStream(_ context.Context, _ llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	emit(llm.TextDeltaEvent{Content: "done", Snapshot: "done"})
	return llm.Completion{Content: "done"}, nil
}

// Parity: framework/agent_loop/agent_loop.py::AgentLoop.__init__ (the loop's
// hub registers the stream-stall source) + queen_orchestrator.py's colony
// sources; pins the chat hub's registered matrix and its teardown.
func TestChatHubRegistersStreamStallAndFleetSources(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       bus,
		Root:      t.TempDir(),
		SessionID: "s-reminder-matrix",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	registered := runtime.hub.RegisteredSourceNames()
	has := func(name string) bool {
		for _, candidate := range registered {
			if candidate == name {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"tasks", "colony_parallel", "active_workers", "tracker_snapshot",
		"colony_worker_snapshot", "searchable_tools", "skills_catalog",
		"turn_context", "idle_nudge", "stream_stall",
	} {
		if !has(want) {
			t.Fatalf("chat hub sources = %v, missing %q", registered, want)
		}
	}

	if !runtime.hub.HasTicker() {
		t.Fatal("chat hub did not start its idle ticker")
	}
	runtime.hub.Post(agentloop.Reminder{Body: "parked", Source: "test"})
	if runtime.hub.PendingLen() == 0 {
		t.Fatal("Post did not park the reminder")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if runtime.hub.HasTicker() {
		t.Fatal("ticker still running after Close")
	}
	if got := runtime.hub.PendingLen(); got != 0 {
		t.Fatalf("pending reminders after Close = %d, want 0", got)
	}
}

// stallOnceStreamLLM stalls its first streamed conversation call and answers
// every later one, recording every request so the test can inspect what the
// model actually received.
type stallOnceStreamLLM struct {
	mu    sync.Mutex
	calls []llm.CompletionRequest
}

func (m *stallOnceStreamLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	return m.ACompleteStream(ctx, req, func(llm.StreamEvent) {})
}

func (m *stallOnceStreamLLM) ACompleteStream(ctx context.Context, req llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	first := len(m.calls) == 1
	m.mu.Unlock()
	if first {
		<-ctx.Done()
		return llm.Completion{}, ctx.Err()
	}
	emit(llm.TextDeltaEvent{Content: "all clear", Snapshot: "all clear"})
	return llm.Completion{Content: "all clear", StopReason: "stop"}, nil
}

func (m *stallOnceStreamLLM) callTexts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.calls))
	for _, call := range m.calls {
		var builder strings.Builder
		for _, message := range call.Messages {
			builder.WriteString(message.Content)
			builder.WriteString("\n")
		}
		out = append(out, builder.String())
	}
	return out
}

// Parity-exempt: the Go watchdog lives in the streaming completer rather than
// the loop; this drives the real session runtime (Deliver → runTurn →
// ExecuteTurn) so the recovery is exercised on the production path: stall →
// STREAM_STALLED reminder → retried call carries the nudge.
func TestChatRuntimeStreamStallReminderReachesTheModel(t *testing.T) {
	t.Setenv("HIVE_LLM_STREAM_TTFT_TIMEOUT_SECONDS", "0.05")
	t.Setenv("HIVE_LLM_STREAM_INTER_EVENT_IDLE_SECONDS", "0.05")

	provider := &stallOnceStreamLLM{}
	bus := eventbus.NewEventBus()
	var (
		eventsMu sync.Mutex
		events   = map[eventbus.EventType]int{}
	)
	subID := bus.Subscribe([]eventbus.EventType{
		eventbus.EventStreamTTFTExceeded,
		eventbus.EventStreamInactive,
		eventbus.EventStreamNudgeSent,
		eventbus.EventReminderInjected,
	}, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		eventsMu.Lock()
		events[event.Type]++
		eventsMu.Unlock()
	})
	t.Cleanup(func() { bus.Unsubscribe(subID) })

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       provider,
		Bus:       bus,
		Root:      t.TempDir(),
		SessionID: "s-stall-recovery",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.Deliver("please answer"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(provider.callTexts()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the stalled turn never retried; calls = %d", len(provider.callTexts()))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let the retried call finish writing its assistant message.
	for {
		parts, err := runtime.Transcript()
		if err != nil {
			t.Fatalf("Transcript: %v", err)
		}
		done := false
		for _, part := range parts {
			if content, _ := part["content"].(string); strings.Contains(content, "all clear") {
				done = true
				break
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the retried turn never recorded its answer")
		}
		time.Sleep(10 * time.Millisecond)
	}

	texts := provider.callTexts()
	retry := texts[len(texts)-1]
	for _, want := range []string{
		"<system-reminder>",
		"previous stream stalled",
		"no tokens before TTFT budget",
	} {
		if !strings.Contains(retry, want) {
			t.Fatalf("retried call missing %q:\n%s", want, retry)
		}
	}
	if got := runtime.loop.Counter("stream_stall_nudged"); got != 1 {
		t.Fatalf("stream_stall_nudged = %d, want 1", got)
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, want := range []eventbus.EventType{
		eventbus.EventStreamTTFTExceeded,
		eventbus.EventStreamNudgeSent,
		eventbus.EventReminderInjected,
	} {
		if events[want] == 0 {
			t.Fatalf("session events = %v, missing %s", events, want)
		}
	}
}

// Parity: framework/agent_loop/internals/types.py::LoopConfig
// llm_stream_* defaults and the legacy inactivity alias override.
func TestStreamWatchdogBudgetsFromLoopConfig(t *testing.T) {
	ttft, inter := streamWatchdogBudgets(agentloop.DefaultLoopConfig())
	if ttft != 600*time.Second || inter != 120*time.Second {
		t.Fatalf("default budgets = %v/%v, want 600s/120s", ttft, inter)
	}
	legacy := agentloop.DefaultLoopConfig()
	legacy.LLMStreamInterEventIdleSeconds = 120
	legacy.LLMStreamInactivityTimeoutSeconds = 30
	_, inter = streamWatchdogBudgets(legacy)
	if inter != 30*time.Second {
		t.Fatalf("legacy alias budget = %v, want 30s", inter)
	}
	disabled := agentloop.DefaultLoopConfig()
	disabled.LLMStreamTTFTTimeoutSeconds = 0
	disabled.LLMStreamInterEventIdleSeconds = 0
	ttft, inter = streamWatchdogBudgets(disabled)
	if ttft != 0 || inter != 0 {
		t.Fatalf("zeroed budgets = %v/%v, want 0/0 (an explicit 0 disables that budget)", ttft, inter)
	}
}

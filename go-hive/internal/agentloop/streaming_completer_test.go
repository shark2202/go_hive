package agentloop

// Tests for the reusable streaming completer adapter and its loop recovery.
//
// The adapter is the Go home for the upstream loop's stream watchdog: the
// loop's LLM seam is a single AComplete call, so the watchdog has to sit in the
// completer. These tests drive a fake streaming provider through the real
// AgentLoop and assert the upstream contract: a trip becomes a
// *llm.StreamStalledError, the STREAM_STALLED source injects its nudge, the
// same inner turn is retried, and the per-turn cap eventually surfaces the
// error instead of looping.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// streamScenario is one scripted streaming call. waitForCancel makes the
// provider hang like a dead upstream until the watchdog cancels it; events are
// emitted before the wait.
type streamScenario struct {
	events        []llm.StreamEvent
	waitForCancel bool
	content       string
	err           error
	onBlock       func()
}

// scriptedStreamCompleter is a Completer + StreamCompleter double that runs one
// scenario per call and records every request, so tests can assert what the
// retried model call actually received.
type scriptedStreamCompleter struct {
	mu        sync.Mutex
	scenarios []streamScenario
	calls     []llm.CompletionRequest
}

func (s *scriptedStreamCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	return s.ACompleteStream(ctx, req, func(llm.StreamEvent) {})
}

func (s *scriptedStreamCompleter) ACompleteStream(ctx context.Context, req llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	index := len(s.calls) - 1
	var scenario streamScenario
	if index < len(s.scenarios) {
		scenario = s.scenarios[index]
	} else if len(s.scenarios) > 0 {
		scenario = s.scenarios[len(s.scenarios)-1]
	}
	s.mu.Unlock()

	for _, event := range scenario.events {
		emit(event)
	}
	if scenario.waitForCancel {
		if scenario.onBlock != nil {
			scenario.onBlock()
		}
		<-ctx.Done()
		return llm.Completion{}, ctx.Err()
	}
	if scenario.err != nil {
		return llm.Completion{}, scenario.err
	}
	if scenario.content != "" {
		emit(llm.TextDeltaEvent{Content: scenario.content, Snapshot: scenario.content})
	}
	return llm.Completion{Content: scenario.content, StopReason: "stop"}, nil
}

func (s *scriptedStreamCompleter) request(i int) llm.CompletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

func (s *scriptedStreamCompleter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func requestText(req llm.CompletionRequest) string {
	var builder strings.Builder
	for _, message := range req.Messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (a TTFT trip
// cancels the stream, consults STREAM_STALLED, injects the nudge and retries
// the same inner turn).
func TestStreamingCompleterTTFTTripsAndRecoversTheLoop(t *testing.T) {
	provider := &scriptedStreamCompleter{scenarios: []streamScenario{
		{waitForCancel: true},
		{content: "recovered"},
	}}
	completer := NewStreamingCompleter(provider, 40*time.Millisecond, time.Second)
	if completer == nil {
		t.Fatal("streaming provider was not wrapped")
	}
	provider.scenarios[0].onBlock = func() {
		if !completer.StreamActive() {
			t.Error("StreamActive = false while the stream is open")
		}
		if completer.FirstEventSeen() {
			t.Error("FirstEventSeen = true before any event arrived")
		}
		signals := completer.Signals()
		if signals == nil || !signals.StreamActive || signals.FirstEventSeen {
			t.Errorf("Signals = %+v, want an open stream with no first event", signals)
		}
	}

	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Hub.Register(NewStreamStallSource(3, true))
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "recovered" {
		t.Fatalf("turn did not recover: %+v", result)
	}
	if result.InnerTurns != 1 {
		t.Fatalf("InnerTurns = %d, want 1 (the nudge retries the same inner turn)", result.InnerTurns)
	}
	if provider.callCount() != 2 {
		t.Fatalf("model calls = %d, want the stalled call plus one retry", provider.callCount())
	}
	retry := requestText(provider.request(1))
	for _, want := range []string{
		"<system-reminder>",
		"previous stream stalled",
		"no tokens before TTFT budget",
	} {
		if !strings.Contains(retry, want) {
			t.Fatalf("retry call missing %q:\n%s", want, retry)
		}
	}
	if got := loop.Counter("stream_stall_nudged"); got != 1 {
		t.Fatalf("stream_stall_nudged = %d, want 1", got)
	}
	if completer.StreamActive() {
		t.Fatal("StreamActive = true after the stream returned")
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (events
// arrived, then the stream went silent -> the "inactive" verdict).
func TestStreamingCompleterInactiveTripsAfterFirstEvent(t *testing.T) {
	provider := &scriptedStreamCompleter{scenarios: []streamScenario{{
		events:        []llm.StreamEvent{llm.TextDeltaEvent{Content: "tok", Snapshot: "tok"}},
		waitForCancel: true,
	}}}
	completer := NewStreamingCompleter(provider, time.Second, 40*time.Millisecond)
	if completer == nil {
		t.Fatal("streaming provider was not wrapped")
	}
	provider.scenarios[0].onBlock = func() {
		if !completer.StreamActive() || !completer.FirstEventSeen() {
			t.Errorf("state during activity = active:%v first:%v, want true/true",
				completer.StreamActive(), completer.FirstEventSeen())
		}
	}

	_, err := completer.AComplete(context.Background(), llm.CompletionRequest{})
	var stalled *llm.StreamStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("AComplete error = %v, want a stream-stalled verdict", err)
	}
	if stalled.Reason != "inactive" {
		t.Fatalf("reason = %q, want inactive", stalled.Reason)
	}
	if stalled.ElapsedSeconds <= 0 || stalled.LimitSeconds <= 0 {
		t.Fatalf("verdict = %+v, want positive elapsed/limit", stalled)
	}
	if !completer.FirstEventSeen() {
		t.Fatal("FirstEventSeen = false after the stream produced an event")
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (a watchdog
// trip with the nudge cap exhausted surfaces the error instead of retrying
// forever).
func TestStreamingCompleterCapSurfacesTheStall(t *testing.T) {
	provider := &scriptedStreamCompleter{scenarios: []streamScenario{{waitForCancel: true}}}
	completer := NewStreamingCompleter(provider, 40*time.Millisecond, time.Second)

	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Hub.Register(NewStreamStallSource(1, true))
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	_, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	var stalled *llm.StreamStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("ExecuteTurn error = %v, want the stream-stalled error after the cap", err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("model calls = %d, want the capped nudge then the surfaced error", provider.callCount())
	}
	if got := loop.Counter("stream_stall_nudged"); got != 1 {
		t.Fatalf("stream_stall_nudged = %d, want 1", got)
	}
}

// unsupportedStreamCompleter reports ErrStreamingUnsupported once, then
// answers through the non-streaming path.
type unsupportedStreamCompleter struct {
	mu            sync.Mutex
	streamCalls   int
	completeCalls int
}

func (s *unsupportedStreamCompleter) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	s.mu.Lock()
	s.completeCalls++
	s.mu.Unlock()
	return llm.Completion{Content: "fallback"}, nil
}

func (s *unsupportedStreamCompleter) ACompleteStream(context.Context, llm.CompletionRequest, func(llm.StreamEvent)) (llm.Completion, error) {
	s.mu.Lock()
	s.streamCalls++
	s.mu.Unlock()
	return llm.Completion{}, llm.ErrStreamingUnsupported
}

func (s *unsupportedStreamCompleter) counts() (stream, complete int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streamCalls, s.completeCalls
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (a transport
// that cannot stream keeps the non-streaming path; the adapter latches so later
// calls do not retry streaming).
func TestStreamingCompleterFallsBackOnUnsupported(t *testing.T) {
	provider := &unsupportedStreamCompleter{}
	completer := NewStreamingCompleter(provider, 40*time.Millisecond, time.Second)
	if completer == nil {
		t.Fatal("streaming provider was not wrapped")
	}

	if completion, err := completer.AComplete(context.Background(), llm.CompletionRequest{}); err != nil || completion.Content != "fallback" {
		t.Fatalf("first AComplete = %+v, %v; want the non-streaming fallback", completion, err)
	}
	if completion, err := completer.AComplete(context.Background(), llm.CompletionRequest{}); err != nil || completion.Content != "fallback" {
		t.Fatalf("second AComplete = %+v, %v; want the latched fallback", completion, err)
	}
	stream, complete := provider.counts()
	if stream != 1 || complete != 2 {
		t.Fatalf("stream/complete calls = %d/%d, want 1/2 (streaming disabled after the first unsupported result)", stream, complete)
	}
}

// streamScopedProbe counts how each call was served, so scoping is observable.
type streamScopedProbe struct {
	mu          sync.Mutex
	streamed    []string
	nonstreamed []string
}

func (s *streamScopedProbe) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	s.mu.Lock()
	s.nonstreamed = append(s.nonstreamed, req.System)
	s.mu.Unlock()
	return llm.Completion{Content: "plain"}, nil
}

func (s *streamScopedProbe) ACompleteStream(_ context.Context, req llm.CompletionRequest, _ func(llm.StreamEvent)) (llm.Completion, error) {
	s.mu.Lock()
	s.streamed = append(s.streamed, req.System)
	s.mu.Unlock()
	return llm.Completion{Content: "streamed"}, nil
}

// Parity-exempt: upstream always streams its conversation call while
// compaction runs through a separate helper; the Go adapter scopes streaming by
// system prompt because both share the AgentContext.LLM seam.
func TestStreamingCompleterScopesToConversationPrompt(t *testing.T) {
	provider := &streamScopedProbe{}
	completer := NewStreamingCompleter(provider, 40*time.Millisecond, time.Second)
	if completer == nil {
		t.Fatal("streaming provider was not wrapped")
	}
	completer.ScopeToSystemPrompt("conversation")

	if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{System: "conversation"}); err != nil {
		t.Fatalf("conversation AComplete: %v", err)
	}
	if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{System: "compactor"}); err != nil {
		t.Fatalf("auxiliary AComplete: %v", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.streamed) != 1 || provider.streamed[0] != "conversation" {
		t.Fatalf("streamed prompts = %v, want only the conversation call", provider.streamed)
	}
	if len(provider.nonstreamed) != 1 || provider.nonstreamed[0] != "compactor" {
		t.Fatalf("non-streamed prompts = %v, want the auxiliary call", provider.nonstreamed)
	}
}

// Parity: framework/agent_loop/internals/types.py::LoopConfig
// llm_stream_* defaults and the legacy inactivity alias override.
func TestStreamWatchdogBudgetsDefaults(t *testing.T) {
	ttft, inter := StreamWatchdogBudgets(DefaultLoopConfig())
	if ttft != 600*time.Second || inter != 120*time.Second {
		t.Fatalf("default budgets = %v/%v, want 600s/120s", ttft, inter)
	}
	legacy := DefaultLoopConfig()
	legacy.LLMStreamInactivityTimeoutSeconds = 30
	if _, inter := StreamWatchdogBudgets(legacy); inter != 30*time.Second {
		t.Fatalf("legacy alias budget = %v, want 30s", inter)
	}
	disabled := DefaultLoopConfig()
	disabled.LLMStreamTTFTTimeoutSeconds = 0
	disabled.LLMStreamInterEventIdleSeconds = 0
	if ttft, inter := StreamWatchdogBudgets(disabled); ttft != 0 || inter != 0 {
		t.Fatalf("zeroed budgets = %v/%v, want 0/0", ttft, inter)
	}
}

// Parity-exempt: the adapter is a Go-only seam (upstream's watchdog reads loop
// state directly); a non-streaming provider must remain byte-for-byte
// unchanged rather than being wrapped.
func TestStreamingCompleterLeavesNonStreamingCompleterAlone(t *testing.T) {
	plain := &scriptedCompleter{scenarios: []scriptedResponse{{content: "plain"}}}
	if wrapped := NewStreamingCompleter(plain, time.Second, time.Second); wrapped != nil {
		t.Fatalf("non-streaming completer was wrapped: %+v", wrapped)
	}
}

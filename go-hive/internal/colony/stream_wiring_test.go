package colony

// Production-path coverage for the colony streaming watchdog.
//
// The worker and overseer loops only have the Completer.AComplete seam, so a
// stalled stream can only trip the upstream watchdog if an adapter wraps the
// provider the AgentContext carries. These tests drive newWorkerAgentLoop /
// startOverseerReminders — the real wiring — with a fake streaming provider and
// assert the loop recovers: stall -> STREAM_STALLED nudge -> same inner turn
// retried.

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// stallOnceColonyStream stalls its first conversation stream until the
// watchdog cancels it, then answers every later call.
type stallOnceColonyStream struct {
	mu          sync.Mutex
	calls       []llm.CompletionRequest
	duringFirst func()
}

func (s *stallOnceColonyStream) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	return s.ACompleteStream(ctx, req, func(llm.StreamEvent) {})
}

func (s *stallOnceColonyStream) ACompleteStream(ctx context.Context, req llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	first := len(s.calls) == 1
	s.mu.Unlock()
	if first {
		if s.duringFirst != nil {
			s.duringFirst()
		}
		<-ctx.Done()
		return llm.Completion{}, ctx.Err()
	}
	emit(llm.TextDeltaEvent{Content: "recovered", Snapshot: "recovered"})
	return llm.Completion{Content: "recovered", StopReason: "stop"}, nil
}

func (s *stallOnceColonyStream) callTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.calls))
	for _, call := range s.calls {
		var builder strings.Builder
		for _, message := range call.Messages {
			builder.WriteString(message.Content)
			builder.WriteString("\n")
		}
		out = append(out, builder.String())
	}
	return out
}

func (s *stallOnceColonyStream) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func fastStreamWatchdogConfig() agentloop.LoopConfig {
	cfg := agentloop.DefaultLoopConfig()
	cfg.MaxIterations = 3
	cfg.LLMStreamTTFTTimeoutSeconds = 0.05
	cfg.LLMStreamInterEventIdleSeconds = 0.05
	cfg.ContinueNudgeMaxPerTurn = 2
	// This fixture deliberately reuses "recovered" across stream retries; the
	// response n-gram guard has its own tests and must not preempt the stream
	// watchdog assertions here.
	cfg.StallDetectionThreshold = 999
	return cfg
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop +
// framework/host/colony_runtime.py (a worker's AgentLoop runs the same stream
// watchdog/continue-nudge path upstream gives every loop).
func TestWorkerLoopStreamStallTripsAndRecovers(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_worker_stream_stall",
	})
	worker := NewWorker("w1", "do the thing", "worker:w1")
	worker.StoragePath = colony.workerStorage(worker.ID)
	if err := os.MkdirAll(worker.StoragePath, 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	provider := &stallOnceColonyStream{}
	worker.Context = colony.buildContext(worker, colony.agentSpec)
	worker.Context.LLM = provider
	worker.conversation = agentloop.NewNodeConversation(colony.workerSystemPrompt(worker, colony.agentSpec))

	loop, stop := colony.newWorkerAgentLoop(worker, fastStreamWatchdogConfig(), nil)
	defer stop()
	streamer, ok := worker.Context.LLM.(*agentloop.StreamingCompleter)
	if !ok {
		t.Fatalf("worker LLM = %T, want the streaming adapter", worker.Context.LLM)
	}
	provider.duringFirst = func() {
		if !streamer.StreamActive() {
			t.Error("worker StreamActive = false while the provider is blocked mid-stream")
		}
		signals := loopSignalsSnapshot(agentloop.LoopSignals{IdleSeconds: 7}, streamer)
		if !signals.StreamActive || signals.FirstEventSeen || signals.IdleSeconds != 7 {
			t.Errorf("worker LoopSignals = %+v, want active stream, no first event, idle preserved", signals)
		}
	}

	// The hub's signals provider must be able to see the live stream while the
	// provider is blocked inside AComplete.
	signals := streamer.Signals()
	if signals == nil || signals.StreamActive || signals.FirstEventSeen {
		t.Fatalf("pre-call signals = %+v, want idle with no first event", signals)
	}

	result, err := loop.ExecuteTurn(context.Background(), worker.Context, worker.conversation, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("worker turn failed: %+v", result)
	}
	// The worker's own text-only stall policy may spend its grace after the
	// retry succeeds, so only the first two calls are load-bearing: the stalled
	// one and the continue-nudge retry.
	if provider.callCount() < 2 {
		t.Fatalf("model calls = %d, want the stalled call plus a retry", provider.callCount())
	}
	retry := provider.callTexts()[1]
	for _, want := range []string{
		"<system-reminder>",
		"previous stream stalled",
		"no tokens before TTFT budget",
	} {
		if !strings.Contains(retry, want) {
			t.Fatalf("retried worker call missing %q:\n%s", want, retry)
		}
	}
	if got := loop.Counter("stream_stall_nudged"); got != 1 {
		t.Fatalf("stream_stall_nudged = %d, want 1", got)
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop +
// framework/host/colony_runtime.py::ColonyRuntime.start_overseer (the overseer
// wraps a full AgentLoop and therefore carries the same watchdog recovery).
func TestOverseerLoopStreamStallTripsAndRecovers(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_overseer_stream_stall",
	})
	worker := NewWorker("overseer:colony_overseer_stream_stall", "", "overseer")
	worker.Persistent = true
	worker.StoragePath = colony.storagePath
	provider := &stallOnceColonyStream{}
	worker.Context = colony.buildContext(worker, colony.agentSpec)
	worker.Context.LLM = provider
	worker.conversation = agentloop.NewNodeConversation(systemPromptFor(colony.agentSpec))

	cfg := fastStreamWatchdogConfig()
	loop := agentloop.NewAgentLoop(cfg, colony.executorFor(worker))
	stop := colony.startOverseerReminders(loop, worker, loop.Config, nil)
	defer stop()

	streamer, ok := worker.Context.LLM.(*agentloop.StreamingCompleter)
	if !ok {
		t.Fatalf("overseer LLM = %T, want the streaming adapter", worker.Context.LLM)
	}
	provider.duringFirst = func() {
		if !streamer.StreamActive() {
			t.Error("overseer StreamActive = false while the provider is blocked mid-stream")
		}
		signals := loopSignalsSnapshot(agentloop.LoopSignals{IdleSeconds: 7}, streamer)
		if !signals.StreamActive || signals.FirstEventSeen || signals.IdleSeconds != 7 {
			t.Errorf("overseer LoopSignals = %+v, want active stream, no first event, idle preserved", signals)
		}
	}
	if signals := streamer.Signals(); signals == nil || signals.StreamActive || signals.FirstEventSeen {
		t.Fatalf("pre-call signals = %+v, want idle with no first event", signals)
	}

	result, err := loop.ExecuteTurn(context.Background(), worker.Context, worker.conversation, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "recovered" {
		t.Fatalf("overseer turn did not recover: %+v", result)
	}
	if provider.callCount() != 2 {
		t.Fatalf("model calls = %d, want the stalled call plus one retry", provider.callCount())
	}
	retry := provider.callTexts()[1]
	if !strings.Contains(retry, "previous stream stalled") {
		t.Fatalf("retried overseer call missing the continue-nudge:\n%s", retry)
	}
	if got := loop.Counter("stream_stall_nudged"); got != 1 {
		t.Fatalf("stream_stall_nudged = %d, want 1", got)
	}
}

// Parity-exempt: a non-streaming provider must keep the worker's previous
// behaviour (no adapter installed, loop untouched).
func TestWorkerLoopLeavesNonStreamingProviderUnwrapped(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_worker_no_stream",
	})
	worker := NewWorker("w1", "do the thing", "worker:w1")
	worker.StoragePath = colony.workerStorage(worker.ID)
	if err := os.MkdirAll(worker.StoragePath, 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	plain := &nonStreamingColonyLLM{}
	worker.Context = colony.buildContext(worker, colony.agentSpec)
	worker.Context.LLM = plain

	_, stop := colony.newWorkerAgentLoop(worker, fastStreamWatchdogConfig(), nil)
	defer stop()
	if worker.Context.LLM != plain {
		t.Fatalf("worker LLM = %T, want the original non-streaming completer", worker.Context.LLM)
	}
}

type nonStreamingColonyLLM struct{}

func (n *nonStreamingColonyLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{Content: "plain"}, nil
}

// paymentFailingColonyLLM reports that streaming is unsupported and answers
// the non-streaming fallback with a classified 402.
type paymentFailingColonyLLM struct{}

func (p *paymentFailingColonyLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, &llm.ProviderError{
		Message:    "team has no active plan",
		StatusCode: http.StatusPaymentRequired,
	}
}

func (p *paymentFailingColonyLLM) ACompleteStream(context.Context, llm.CompletionRequest, func(llm.StreamEvent)) (llm.Completion, error) {
	return llm.Completion{}, llm.ErrStreamingUnsupported
}

// Colony workers and the overseer share one provider, so the per-worker
// stream/execution identity cannot be stamped on the provider (one worker's
// 402 would be attributed to another). wireStreamingLLM must install it on the
// worker's adapter instead, or the billing gate only reaches the session as a
// generic execution failure.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (StreamErrorEvent error_type=="payment_required" -> emit_payment_required)
func TestWireStreamingLLMInstallsPerWorkerPaymentSink(t *testing.T) {
	cases := []struct {
		name          string
		executionID   string
		wantExecution string
	}{
		{name: "context execution id", executionID: "exec-77", wantExecution: "exec-77"},
		{name: "worker id fallback", executionID: "", wantExecution: "w1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := eventbus.NewEventBus()
			colony := NewColonyRuntime(RuntimeOptions{
				AgentSpec:   testAgentSpec(),
				StoragePath: t.TempDir(),
				StreamID:    "colony_payment_sink",
				EventBus:    bus,
			})
			worker := NewWorker("w1", "do the thing", "worker:w1")
			worker.Context = colony.buildContext(worker, colony.agentSpec)
			worker.Context.ExecutionID = tc.executionID
			worker.Context.LLM = &paymentFailingColonyLLM{}
			payments := collectEvents(bus, eventbus.EventPaymentRequired)

			if completer := colony.wireStreamingLLM(worker, fastStreamWatchdogConfig()); completer == nil {
				t.Fatal("streaming provider was not wrapped")
			}
			if _, err := worker.Context.LLM.AComplete(context.Background(), llm.CompletionRequest{}); err == nil {
				t.Fatal("AComplete succeeded on a 402 provider")
			}
			if !waitForColony(t, time.Second, func() bool { return payments.count() > 0 }) {
				t.Fatal("worker 402 never reached the colony bus as payment_required")
			}
			events := payments.snapshot()
			if len(events) != 1 {
				t.Fatalf("payment_required count = %d, want 1", len(events))
			}
			event := events[0]
			if event.StreamID != "worker:w1" {
				t.Fatalf("payment_required stream = %q, want worker:w1", event.StreamID)
			}
			if event.ExecutionID == nil || *event.ExecutionID != tc.wantExecution {
				t.Fatalf("payment_required execution = %v, want %q", event.ExecutionID, tc.wantExecution)
			}
			if status, ok := event.Data["upstream_status"].(int); !ok || status != http.StatusPaymentRequired {
				t.Fatalf("payment_required upstream_status = %#v, want 402", event.Data["upstream_status"])
			}
		})
	}
}

package server

// Stop lifecycle coverage against the real production assembly: the session's
// queen node is a *ChatRuntime (what the loader registers), not a test double
// implementing the route's interfaces. The cancel-queen route must cancel only
// the in-flight turn, the idle signal must already carry the user-stop flag as
// the stream unwinds, and the next client message must lift it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// stopFlowLLM blocks the session's first model call until its context is
// cancelled, then answers later calls so the test can prove the runtime itself
// survived the stop. probe runs at the moment the blocked call observes the
// cancellation: the reminder signals must already report the user-stop then.
type stopFlowLLM struct {
	mu              sync.Mutex
	requests        []llm.CompletionRequest
	started         chan struct{}
	probe           func() bool
	stoppedObserved atomic.Bool
}

func (f *stopFlowLLM) ModelName() string { return "stop-flow-test-model" }

func (f *stopFlowLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	first := len(f.requests) == 1
	f.mu.Unlock()
	if !first {
		return llm.Completion{Content: "resumed", Model: "stop-flow-test-model", StopReason: "end_turn"}, nil
	}
	select {
	case f.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	if f.probe != nil {
		f.stoppedObserved.Store(f.probe())
	}
	return llm.Completion{}, ctx.Err()
}

func (f *stopFlowLLM) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *stopFlowLLM) sawUserText(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		for _, message := range request.Messages {
			if message.Role == "user" && strings.Contains(message.Content, text) {
				return true
			}
		}
	}
	return false
}

// stopFlowSession registers the runtime as the session's queen node exactly as
// the loader does, so the route's interface assertions hit the production type.
func stopFlowSession(t *testing.T, runtime *ChatRuntime) ExecutionRouteDeps {
	t.Helper()
	deps, _ := executionDeps(t)
	deps.Manager.AddSession(&Session{
		ID:            runtime.sessionID,
		Chat:          runtime,
		QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": runtime}},
	})
	return deps
}

func cancelQueenRequest(t *testing.T, deps ExecutionRouteDeps, sessionID string) (int, map[string]any) {
	t.Helper()
	recorder, body := serveColonyRequest(executionMux(deps),
		httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/cancel-queen", nil))
	return recorder.Code, body
}

func waitForStopFlow(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCancelQueenStopsRealChatRuntimeTurn drives POST /cancel-queen against a
// live *ChatRuntime whose model call is blocked in flight: the route must
// report cancelled, the turn must unwind (parked on user_stopped, no failure
// frame) before the 500ms wait returns, the idle signal must carry
// UserStopped, and the next client message must clear it and run normally.
//
// Parity: core/tests/test_cancel_user_stop_flow.py::test_cancel_queen_marks_user_stop_before_cancelling
// Parity: core/tests/test_cancel_user_stop_flow.py::test_idle_nudge_fires_again_once_user_stop_cleared
// Parity-exempt: tests/test_cancel_user_stop_flow.py replaces the queen loop
// with a scripted double; this pins the same flow on the real *ChatRuntime the
// loader registers as the session's queen node, which is the assembly that was
// missing the two interfaces.
func TestCancelQueenStopsRealChatRuntimeTurn(t *testing.T) {
	model := &stopFlowLLM{started: make(chan struct{}, 1)}
	runtime, recorder, _ := newQueenChatRuntime(t, model, "", "")
	model.probe = func() bool { return runtime.reminderSignals().UserStopped }

	deps := stopFlowSession(t, runtime)
	if err := runtime.Deliver("work until stopped"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	select {
	case <-model.started:
	case <-time.After(10 * time.Second):
		t.Fatalf("the in-flight turn never reached the model; events: %v", recorder.types())
	}
	if runtime.reminderSignals().UserStopped {
		t.Fatal("user-stop signal set before the Stop request")
	}

	status, body := cancelQueenRequest(t, deps, runtime.sessionID)
	if status != http.StatusOK {
		t.Fatalf("cancel-queen status = %d, want 200 (body %v)", status, body)
	}
	if body["cancelled"] != true {
		t.Fatalf("cancel-queen body = %v, want cancelled:true from the real runtime", body)
	}
	if _, present := body["error"]; present {
		t.Fatalf("cancel-queen body = %v, want no error for a real queen node", body)
	}
	if _, ok := body["workers"].(map[string]any); !ok || len(body) != 2 {
		t.Fatalf("cancel-queen body = %v, want exactly {cancelled, workers}", body)
	}

	// The route waits on the turn's done channel, so by the time it answered the
	// cancelled turn had unwound and its park cleanup had run.
	signals := runtime.reminderSignals()
	if !signals.UserStopped {
		t.Fatal("idle signal lost the user-stop flag")
	}
	if !model.stoppedObserved.Load() {
		t.Fatal("the cancelled model call did not observe the user-stop before unwinding")
	}
	if signals.StreamActive {
		t.Fatal("turn still marked active after cancel-queen returned")
	}
	if !signals.AwaitingInput || signals.ParkReason == nil || *signals.ParkReason != agentloop.ParkReasonUserStopped {
		t.Fatalf("park after Stop = awaiting:%v reason:%v, want awaiting on user_stopped",
			signals.AwaitingInput, signals.ParkReason)
	}
	if failed := recorder.ofType(eventbus.EventExecutionFailed); len(failed) != 0 {
		t.Fatalf("Stop published %d execution_failed frame(s); events: %v", len(failed), recorder.types())
	}
	if err := runtime.LastError(); err != nil {
		t.Fatalf("Stop recorded a turn error: %v", err)
	}

	// A real client message lifts the flag and runs the next turn.
	if err := runtime.Deliver("resume the work"); err != nil {
		t.Fatalf("Deliver after Stop: %v", err)
	}
	waitForStopFlow(t, "the turn after Stop to complete", func() bool {
		return len(recorder.ofType(eventbus.EventExecutionCompleted)) > 0
	})
	waitForStopFlow(t, "the resumed turn to park on turn_done", func() bool {
		signals := runtime.reminderSignals()
		return !signals.StreamActive && signals.AwaitingInput &&
			signals.ParkReason != nil && *signals.ParkReason == agentloop.ParkReasonTurnDone
	})
	if runtime.reminderSignals().UserStopped {
		t.Fatal("user-stop flag survived the next client input")
	}
	if got := model.requestCount(); got != 2 {
		t.Fatalf("model requests = %d, want the cancelled turn plus the resumed turn", got)
	}
	if !model.sawUserText("resume the work") {
		t.Fatal("the resumed turn never carried the client message to the model")
	}
}

// TestCancelQueenKeepsQueuedTurnRunning proves the Stop cancels only the turn in
// flight: a client message queued behind it still reaches the model and
// completes, so the runtime context and the queue survived the cancel.
//
// Parity-exempt: upstream's cancel tests await a single scripted turn; the
// queue-behind-the-cancelled-turn case only exists in the Go port's
// worker-goroutine queue and has no Python counterpart to mirror.
func TestCancelQueenKeepsQueuedTurnRunning(t *testing.T) {
	model := &stopFlowLLM{started: make(chan struct{}, 1)}
	runtime, recorder, _ := newQueenChatRuntime(t, model, "", "")
	deps := stopFlowSession(t, runtime)

	if err := runtime.Deliver("first request"); err != nil {
		t.Fatalf("Deliver first: %v", err)
	}
	select {
	case <-model.started:
	case <-time.After(10 * time.Second):
		t.Fatalf("the first turn never reached the model; events: %v", recorder.types())
	}
	if err := runtime.Deliver("second request"); err != nil {
		t.Fatalf("Deliver second: %v", err)
	}

	status, body := cancelQueenRequest(t, deps, runtime.sessionID)
	if status != http.StatusOK || body["cancelled"] != true {
		t.Fatalf("cancel-queen = %d %v, want 200 cancelled:true", status, body)
	}

	waitForStopFlow(t, "the queued turn to complete", func() bool {
		return len(recorder.ofType(eventbus.EventExecutionCompleted)) > 0
	})
	if got := model.requestCount(); got != 2 {
		t.Fatalf("model requests = %d, want the cancelled turn plus the queued turn", got)
	}
	if !model.sawUserText("second request") {
		t.Fatal("the queued client message never reached the model after the Stop")
	}
	if runtime.reminderSignals().UserStopped {
		t.Fatal("user-stop flag survived the queued turn's start")
	}
	if failed := recorder.ofType(eventbus.EventExecutionFailed); len(failed) != 0 {
		t.Fatalf("Stop queued turn path published %d execution_failed frame(s): %v", len(failed), recorder.types())
	}
}

// TestCancelCurrentTurnWithoutInflightTurn proves the production canceller, not
// just the route's fake, reports "nothing to await" when the runtime is idle.
//
// Parity: core/framework/agent_loop/agent_loop.py::cancel_current_turn returns
// an empty list when no stream/tool task is running.
// Parity-exempt: the route-level counterpart above covers the fake node; this
// pins the real *ChatRuntime's idle branch, which upstream has no Python test
// for because the task list is trivially empty there.
func TestCancelCurrentTurnWithoutInflightTurn(t *testing.T) {
	runtime, recorder, _ := newQueenChatRuntime(t, &queenChatLLM{}, "", "")
	if tasks := runtime.CancelCurrentTurn(); len(tasks) != 0 {
		t.Fatalf("idle CancelCurrentTurn = %d task(s), want none", len(tasks))
	}
	// Marking a stop with no turn in flight keeps the gate closed but must not
	// cancel or fail the session: the runtime still accepts the next message.
	runtime.MarkUserStopped()
	if !runtime.reminderSignals().UserStopped {
		t.Fatal("MarkUserStopped did not surface on the idle signal")
	}
	if err := runtime.Deliver("hello after an idle Stop"); err != nil {
		t.Fatalf("Deliver after idle Stop: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	// The finished turn released its handle: a later Stop cannot reach it.
	waitForStopFlow(t, "the finished turn's cancellation handle to be released", func() bool {
		runtime.turnMu.Lock()
		defer runtime.turnMu.Unlock()
		return runtime.turnCancel == nil && runtime.turnDone == nil
	})
	if runtime.reminderSignals().UserStopped {
		t.Fatal("user-stop flag survived the next client input")
	}
	if tasks := runtime.CancelCurrentTurn(); len(tasks) != 0 {
		t.Fatalf("CancelCurrentTurn after the turn finished = %d task(s), want none", len(tasks))
	}
	if err := runtime.LastError(); err != nil {
		t.Fatalf("idle Stop recorded a turn error: %v", err)
	}
}

package colony

// Colony stop sweep: bounded terminal state + browser reaper wiring. Both are
// in-memory fakes — no process is signalled and no bridge is required.

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type browserCloseCall struct {
	Profile string
	Reason  string
	Browser string
}

type recordingBrowserCloser struct {
	mu    sync.Mutex
	calls []browserCloseCall
}

func (c *recordingBrowserCloser) close(_ context.Context, profileName, reason, browserProfile string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, browserCloseCall{Profile: profileName, Reason: reason, Browser: browserProfile})
	return nil
}

func (c *recordingBrowserCloser) snapshot() []browserCloseCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]browserCloseCall(nil), c.calls...)
}

func newStopTestColony(t *testing.T, closeProfile CloseProfileContext, stopTimeout time.Duration) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         newScriptedLLM(textResponse("ok")),
		Tools:       []llm.ToolDef{},
		StreamID:    "stop_test_colony",
		// The LLM in this helper is only a placeholder: the tests below drive
		// the worker registry directly instead of spawning a model turn.
		BrowserClose: closeProfile,
		StopTimeout:  stopTimeout,
	})
	t.Cleanup(runtime.Stop)
	return runtime
}

// registerStopWorker installs a running worker in the registry without a model
// loop. The returned context is cancelled by StopWorker / Stop.
func registerStopWorker(runtime *ColonyRuntime, id, browserProfile string) (*Worker, context.Context) {
	worker := NewWorker(id, "task "+id, runtime.streamID)
	worker.BrowserProfile = browserProfile
	worker.SetStatus(WorkerRunning)
	worker.MarkStarted(time.Now())
	workerCtx, cancel := context.WithCancel(context.Background())
	worker.setCancel(cancel)
	runtime.mu.Lock()
	runtime.workers[worker.ID] = worker
	runtime.mu.Unlock()
	return worker, workerCtx
}

// Parity-exempt: upstream's budgeted Worker.stop is exercised indirectly by
// the stop sweep (no standalone Python test); this fixture pins the Go
// ColonyRuntime.Stop bound with an in-memory wedge instead of a model.
func TestStopIsBoundedWhenAWorkerIgnoresCancellation(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StopTimeout: 50 * time.Millisecond,
	})
	worker, _ := registerStopWorker(runtime, "worker_wedged", "")
	// A goroutine that ignores cancellation models a worker wedged in a
	// shielded cleanup or a tool call; the old unbounded wg.Wait hung on it.
	runtime.wg.Add(1)
	go func() {
		defer runtime.wg.Done()
		<-release
	}()

	done := make(chan struct{})
	go func() {
		runtime.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hung on a worker that ignores cancellation")
	}

	if status := worker.Status(); status != WorkerStopped {
		t.Fatalf("wedged worker status = %s, want stopped", status)
	}
	select {
	case <-worker.Done():
	default:
		t.Fatal("wedged worker never reached a terminal state")
	}
}

// Parity: core/tests/test_colony_reap_timeout.py::test_reap_gives_up_on_a_bridge_that_never_answers
func TestStopWorkerReapsItsBrowserGroup(t *testing.T) {
	closer := &recordingBrowserCloser{}
	runtime := newStopTestColony(t, closer.close, time.Second)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	worker, workerCtx := registerStopWorker(runtime, "worker_reap", "research")
	go func() {
		<-workerCtx.Done()
		worker.Finish(WorkerStopped, WorkerResult{Output: map[string]any{}})
	}()

	if !runtime.StopWorker(worker.ID) {
		t.Fatal("StopWorker refused a live worker")
	}
	if !waitForColony(t, 2*time.Second, func() bool { return worker.Status().Done() }) {
		t.Fatalf("worker status = %s, want a terminal state", worker.Status())
	}

	calls := closer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("browser close calls = %#v, want exactly one", calls)
	}
	if calls[0].Profile != worker.ID {
		t.Fatalf("closed profile = %q, want the worker id %q", calls[0].Profile, worker.ID)
	}
	if calls[0].Reason != browserShutdownReason {
		t.Fatalf("close reason = %q, want %q", calls[0].Reason, browserShutdownReason)
	}
	if calls[0].Browser != "research" {
		t.Fatalf("browser profile = %q, want research", calls[0].Browser)
	}
}

// Parity: core/tests/test_colony_reap_timeout.py::test_reap_gives_up_on_a_bridge_that_never_answers
func TestColonyStopReapsLiveWorkerBrowsers(t *testing.T) {
	closer := &recordingBrowserCloser{}
	runtime := newStopTestColony(t, closer.close, time.Second)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runtime.mu.Lock()
	runCtx := runtime.ctx
	runtime.mu.Unlock()

	workerA, _ := registerStopWorker(runtime, "worker_alpha", "work")
	workerB, _ := registerStopWorker(runtime, "worker_beta", "")
	for _, worker := range []*Worker{workerA, workerB} {
		go func(w *Worker) {
			<-runCtx.Done()
			w.Finish(WorkerStopped, WorkerResult{Output: map[string]any{}})
		}(worker)
	}

	runtime.Stop()

	calls := closer.snapshot()
	if len(calls) != 2 {
		t.Fatalf("browser close calls = %#v, want one per live worker", calls)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].Profile < calls[j].Profile })
	want := []browserCloseCall{
		{Profile: "worker_alpha", Reason: browserShutdownReason, Browser: "work"},
		{Profile: "worker_beta", Reason: browserShutdownReason, Browser: "default"},
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("close call %d = %#v, want %#v", index, calls[index], want[index])
		}
	}
}

// Regression: a worker that reaches terminal while Stop is running must still
// be reaped. The sweep snapshots the live set before cancelling and reaps
// exactly that set; re-filtering it after the bounded wait dropped every
// worker whose goroutine returned on cancellation, leaking its tab group. The
// cancel wrapper makes the interleaving deterministic: worker_alpha finishes
// inside Stop, after the snapshot and before the sweep used to pick its reap
// target, while worker_beta is still live.
//
// Parity-exempt: the Go stop sweep's snapshot-before-cancel ordering has no
// Python counterpart to align with — upstream's close_profile_context is
// idempotent and its done-callback overlaps the colony backstop freely, so
// this pins a Go-only race.
func TestColonyStopReapsWorkersThatFinishDuringStop(t *testing.T) {
	closer := &recordingBrowserCloser{}
	runtime := newStopTestColony(t, closer.close, 50*time.Millisecond)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	alpha, _ := registerStopWorker(runtime, "worker_alpha", "work")
	registerStopWorker(runtime, "worker_beta", "")
	// A worker's goroutine calls Finish the instant the runtime context is
	// cancelled. Running that transition from the cancel call itself pins the
	// same ordering without depending on goroutine scheduling.
	runtime.mu.Lock()
	cancel := runtime.cancel
	runtime.cancel = func() {
		cancel()
		alpha.Finish(WorkerStopped, WorkerResult{Output: map[string]any{}})
	}
	runtime.mu.Unlock()

	runtime.Stop()

	calls := closer.snapshot()
	// worker_beta is force-stopped at the deadline; worker_alpha is only in the
	// reap set if Stop kept the set it snapshotted before cancelling.
	if len(calls) != 2 {
		t.Fatalf("browser close calls = %#v, want exactly one per originally-live worker", calls)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].Profile < calls[j].Profile })
	want := []browserCloseCall{
		{Profile: "worker_alpha", Reason: browserShutdownReason, Browser: "work"},
		{Profile: "worker_beta", Reason: browserShutdownReason, Browser: "default"},
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("close call %d = %#v, want %#v", index, calls[index], want[index])
		}
	}
}

// Regression: preserving the snapshot's reap set must not resurrect a worker
// that was already terminal when Stop began. Its tab group was released by
// whatever stopped it, and this sweep never owned it.
//
// Parity-exempt: guards the Go snapshot/reap set against re-reaping a worker
// that was already terminal; upstream has no such snapshot to keep.
func TestColonyStopSkipsWorkersAlreadyTerminalAtSnapshot(t *testing.T) {
	closer := &recordingBrowserCloser{}
	runtime := newStopTestColony(t, closer.close, 50*time.Millisecond)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished, _ := registerStopWorker(runtime, "worker_finished", "work")
	finished.Finish(WorkerCompleted, WorkerResult{Output: map[string]any{}})
	registerStopWorker(runtime, "worker_live", "")

	runtime.Stop()

	calls := closer.snapshot()
	if len(calls) != 1 || calls[0].Profile != "worker_live" {
		t.Fatalf("browser close calls = %#v, want only worker_live", calls)
	}
	if status := finished.Status(); status != WorkerCompleted {
		t.Fatalf("already-terminal worker status = %s, want completed (untouched)", status)
	}
}

// Parity-exempt: upstream leans on close_profile_context being idempotent
// instead of tracking closed groups; the Go port has no per-worker done
// callback, so this pins the dedupe that replaces it.
func TestStopDoesNotDoubleCloseAReapedWorker(t *testing.T) {
	closer := &recordingBrowserCloser{}
	runtime := newStopTestColony(t, closer.close, 20*time.Millisecond)
	worker, _ := registerStopWorker(runtime, "worker_wedged", "")

	if !runtime.StopWorker(worker.ID) {
		t.Fatal("StopWorker refused a live worker")
	}
	if calls := closer.snapshot(); len(calls) != 1 {
		t.Fatalf("browser close calls = %#v after StopWorker, want one", calls)
	}

	// The worker never finished, so the colony sweep force-finishes it and
	// would reap it a second time without the runtime's reaped set.
	runtime.Stop()

	if status := worker.Status(); status != WorkerStopped {
		t.Fatalf("wedged worker status = %s, want stopped", status)
	}
	if calls := closer.snapshot(); len(calls) != 1 {
		t.Fatalf("browser close calls = %#v after Stop, want the StopWorker close only", calls)
	}
}

// Parity-exempt: upstream has no standalone test for Worker.stop's timeout
// branch; this pins the Go sweep's force-terminal convergence with an
// in-memory wedge (no process is signalled).
func TestStopWorkersForceStopsAWedgedWorker(t *testing.T) {
	closer := &recordingBrowserCloser{}
	runtime := newStopTestColony(t, closer.close, 20*time.Millisecond)
	worker, _ := registerStopWorker(runtime, "worker_wedged", "")

	summary, err := runtime.StopWorkers(false)
	if err != nil {
		t.Fatalf("StopWorkers: %v", err)
	}
	timedOut, _ := summary["timed_out"].([]string)
	if len(timedOut) != 1 || timedOut[0] != worker.ID {
		t.Fatalf("timed_out = %v, want [%s]", summary["timed_out"], worker.ID)
	}
	if status := worker.Status(); status != WorkerStopped {
		t.Fatalf("wedged worker status = %s, want stopped", status)
	}
	select {
	case <-worker.Done():
	default:
		t.Fatal("wedged worker never reached a terminal state")
	}
	calls := closer.snapshot()
	if len(calls) != 1 || calls[0].Profile != worker.ID {
		t.Fatalf("browser close calls = %#v, want one for %s", calls, worker.ID)
	}
}

// Parity: core/tests/test_colony_reap_timeout.py::test_reap_gives_up_on_a_bridge_that_never_answers
func TestStopWorkersReapsTheStoppedSet(t *testing.T) {
	provider := newGateLLM("slow task")
	closer := &recordingBrowserCloser{}
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:    testAgentSpec(),
		Goal:         testGoal(),
		StoragePath:  t.TempDir(),
		LLM:          provider,
		Tools:        []llm.ToolDef{},
		EventBus:     eventbus.NewEventBus(),
		StreamID:     "stop_sweep_test_colony",
		BrowserClose: closer.close,
		StopTimeout:  time.Second,
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "slow task"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		return workerStatusOf(runtime, ids[0]) == WorkerRunning
	}) {
		t.Fatal("worker never started")
	}

	summary, err := runtime.StopWorkers(false)
	if err != nil {
		t.Fatalf("StopWorkers: %v", err)
	}
	if timedOut, _ := summary["timed_out"].([]string); len(timedOut) != 0 {
		t.Fatalf("timed_out = %v, want none", timedOut)
	}
	if stopped, _ := summary["stopped"].([]string); len(stopped) != 1 || stopped[0] != ids[0] {
		t.Fatalf("stopped = %v, want [%s]", summary["stopped"], ids[0])
	}

	calls := closer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("browser close calls = %#v, want one for the stopped worker", calls)
	}
	if calls[0].Profile != ids[0] || calls[0].Reason != browserShutdownReason || calls[0].Browser != "default" {
		t.Fatalf("close call = %#v, want worker %s with %q/default", calls[0], ids[0], browserShutdownReason)
	}
}

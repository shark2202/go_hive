package colony

// The colony stop sweep: cancel every worker (the overseer included), wait a
// bounded 10 s for them to reach a terminal state, force the stragglers
// terminal, then release their browser tab groups. Upstream's stop is
// asynchronous; this is the same sweep with an explicit deadline so one
// wedged worker can never hang shutdown.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop
// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop_all_workers
// Parity: framework/host/colony_runtime.py::ColonyRuntime._reap_worker_browsers

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultStopTimeout is how long Stop waits for its workers to unwind before
// forcing them terminal. Bounded on purpose: one worker wedged in a shielded
// cleanup (or swallowing cancellation) must never be able to hang the
// colony-wide stop sweep. It is upstream's STOP_TIMEOUT_SEC, the same bound
// the per-worker stop sweep uses.
//
// Parity: framework/host/worker.py::STOP_TIMEOUT_SEC
const DefaultStopTimeout = DefaultStopWorkersTimeout

// snapshotWorkers returns every worker the colony owns, the persistent
// overseer included, in no particular order.
func (r *ColonyRuntime) snapshotWorkers() []*Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	workers := make([]*Worker, 0, len(r.workers)+1)
	if r.overseer != nil {
		workers = append(workers, r.overseer)
	}
	for _, worker := range r.workers {
		workers = append(workers, worker)
	}
	return workers
}

// liveWorkers keeps the workers that have not reached a terminal state yet,
// deduplicated by identity. This is upstream's `live` selection: a worker
// that already finished is not part of the stop sweep (or its reap).
func liveWorkers(workers []*Worker) []*Worker {
	live := make([]*Worker, 0, len(workers))
	seen := make(map[*Worker]struct{}, len(workers))
	for _, worker := range workers {
		if worker == nil {
			continue
		}
		if _, duplicate := seen[worker]; duplicate {
			continue
		}
		seen[worker] = struct{}{}
		if worker.Status().Done() {
			continue
		}
		live = append(live, worker)
	}
	return live
}

// snapshotLiveWorkers returns the workers that are live when the stop sweep
// begins, deduplicated by identity. Stop takes this set before it cancels
// anything and reaps exactly it afterwards: a worker whose goroutine returns
// as soon as ctx.Done fires is terminal a moment later, but it was part of the
// colony at shutdown and still owns a browser tab group.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop (`live`)
func (r *ColonyRuntime) snapshotLiveWorkers() []*Worker {
	return liveWorkers(r.snapshotWorkers())
}

// awaitWorkersStopped waits for the given live set — selected before the
// sweep cancelled anything — to reach a terminal state against one shared
// deadline and returns exactly that set as the reaper's target. It must not
// re-filter on status: a worker that reaches terminal while Stop waits is
// still owed its browser reap, and dropping it here leaks its tab group. A
// worker still running at the deadline is force-finished STOPPED so the
// registry converges even when the goroutine that owns it is wedged.
//
// Parity: framework/host/worker.py::Worker.stop (timeout branch)
func (r *ColonyRuntime) awaitWorkersStopped(live []*Worker) []*Worker {
	timeout := r.effectiveStopTimeout()
	deadlineAt := time.Now().Add(timeout)
	if len(live) == 0 {
		// A worker's status flips terminal inside finishOnce before its
		// terminal report is written, so "no live workers" does not mean the
		// owning goroutines are gone. The bounded wg wait is what keeps a
		// caller's cleanup (result.json, events.jsonl) from racing shutdown.
		r.awaitTrackedGoroutines(deadlineAt)
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for index, worker := range live {
		select {
		case <-worker.Done():
		case <-timer.C:
			for _, wedged := range live[index:] {
				r.forceStopWorker(wedged, timeout)
			}
			return live
		}
	}
	// The run watchers ride the same workers. Their aggregation is normally
	// instantaneous; the shared deadline keeps a wedged one from outliving the
	// stop budget.
	r.awaitTrackedGoroutines(deadlineAt)
	return live
}

// forceStopWorker converges a worker the stop deadline caught. finishOnce is
// idempotent, so the worker's own goroutine — if it ever unwinds — sees an
// already-terminal worker instead of publishing a second report. No bus
// publish here: the colony is shutting down, and upstream's Worker.stop
// timeout branch only flips the status too. The run-state record is written so
// a restart never resumes work the stop sweep abandoned.
func (r *ColonyRuntime) forceStopWorker(worker *Worker, timeout time.Duration) {
	if worker == nil {
		return
	}
	errorText := fmt.Sprintf(
		"Worker %s did not exit within %.1fs of cancel — force-stopping",
		worker.ID,
		timeout.Seconds(),
	)
	if worker.finishOnce(WorkerStopped, WorkerResult{Output: map[string]any{}, Error: &errorText}, nil) {
		r.writeWorkerRunState(worker, string(WorkerStopped))
	}
}

// awaitTrackedGoroutines keeps Stop's old guarantee — every run watcher and
// unwinding worker goroutine finished before Stop returns — without letting a
// wedged goroutine hold it past the sweep deadline.
func (r *ColonyRuntime) awaitTrackedGoroutines(deadline time.Time) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (r *ColonyRuntime) effectiveStopTimeout() time.Duration {
	if r != nil && r.stopTimeout > 0 {
		return r.stopTimeout
	}
	return DefaultStopTimeout
}

// reapWorkerBrowsers closes the browser tab group of every worker in the set
// through the host-injected bridge closer, skipping any worker this runtime
// already reaped so a per-worker stop and a colony stop cannot double-close
// the same group.
//
// Python's close_profile_context is idempotent and its done-callback overlaps
// the colony backstop freely; the Go port has no per-worker done-callback, so
// the reaped set is what keeps the two stop paths from racing the same close.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime._reap_worker_browsers
func (r *ColonyRuntime) reapWorkerBrowsers(workers []*Worker) {
	if r == nil || r.browserClose == nil || len(workers) == 0 {
		return
	}
	ids := make([]string, 0, len(workers))
	r.mu.Lock()
	if r.browserReaped == nil {
		r.browserReaped = map[string]struct{}{}
	}
	for _, worker := range workers {
		if worker == nil || strings.TrimSpace(worker.ID) == "" {
			continue
		}
		if _, reaped := r.browserReaped[worker.ID]; reaped {
			continue
		}
		r.browserReaped[worker.ID] = struct{}{}
		ids = append(ids, worker.ID)
	}
	r.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	// A fresh context: a colony stop has already cancelled r.ctx, and the
	// bridge round-trip must not inherit that cancellation. The reaper bounds
	// each close on its own (DefaultBrowserReapTimeout).
	reaper := BrowserReaper{Close: r.browserClose, Profile: r.WorkerBrowserProfile}
	reaper.Reap(context.Background(), ids)
}

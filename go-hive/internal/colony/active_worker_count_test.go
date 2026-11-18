package colony

import "testing"

// Parity-exempt: framework/server/routes_sessions.py::_active_worker_count has
func TestActiveWorkerCountCountsInFlightWorkers(t *testing.T) {
	colony := newBudgetTestRuntime(true)
	for id, status := range map[string]WorkerStatus{
		"w_queued":    WorkerQueued,
		"w_pending":   WorkerPending,
		"w_running":   WorkerRunning,
		"w_completed": WorkerCompleted,
		"w_failed":    WorkerFailed,
		"w_stopped":   WorkerStopped,
	} {
		colony.workers[id] = newBudgetStubWorker(id, status, 10, false, false)
	}
	if got := colony.ActiveWorkerCount(); got != 3 {
		t.Fatalf("ActiveWorkerCount = %d, want 3 (queued+pending+running)", got)
	}

	var nilRuntime *ColonyRuntime
	if got := nilRuntime.ActiveWorkerCount(); got != 0 {
		t.Fatalf("nil runtime count = %d, want 0", got)
	}
}

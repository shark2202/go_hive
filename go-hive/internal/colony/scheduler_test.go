package colony

import (
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

type schedHarness struct {
	t        *testing.T
	sched    *WorkerScheduler
	bus      *eventbus.EventBus
	colonyID string
	tasks    []string

	mu       sync.Mutex
	reports  []map[string]any
	gates    map[string]chan struct{}
	stops    map[string]chan struct{}
	stopOnce map[string]*sync.Once
	opened   map[string]bool
	started  map[string]int
}

func newSchedHarness(t *testing.T, maxConcurrent int, tasks ...string) *schedHarness {
	t.Helper()
	h := &schedHarness{
		t:        t,
		bus:      eventbus.NewEventBus(),
		colonyID: "sched_test",
		tasks:    append([]string(nil), tasks...),
		gates:    map[string]chan struct{}{},
		stops:    map[string]chan struct{}{},
		stopOnce: map[string]*sync.Once{},
		opened:   map[string]bool{},
		started:  map[string]int{},
	}
	for _, task := range tasks {
		h.gates[task] = make(chan struct{})
		h.stops[task] = make(chan struct{})
		h.stopOnce[task] = &sync.Once{}
	}

	h.sched = NewWorkerScheduler(maxConcurrent, h.colonyID, h.bus)
	h.sched.StartFunc = func(w *SchedulerWorker) error {
		h.mu.Lock()
		h.started[w.Task]++
		gate, hasGate := h.gates[w.Task]
		stop := h.stops[w.Task]
		h.mu.Unlock()

		if !hasGate {
			h.complete(w)
			return nil
		}
		select {
		case <-gate:
			h.complete(w)
		case <-stop:
			h.sched.MarkTerminal(w.ID, WorkerStopped)
			h.publish(w, "stopped", "stopped")
		}
		return nil
	}
	h.sched.StopFunc = func(w *SchedulerWorker) {
		h.mu.Lock()
		once := h.stopOnce[w.Task]
		stop := h.stops[w.Task]
		h.mu.Unlock()
		if once == nil {
			return
		}
		once.Do(func() { close(stop) })
	}
	h.bus.Subscribe([]eventbus.EventType{eventbus.EventSubagentReport}, func(ev *eventbus.AgentEvent) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.reports = append(h.reports, shallowCopy(ev.Data))
	})
	h.sched.Start()
	t.Cleanup(h.sched.Stop)
	return h
}

func (h *schedHarness) complete(w *SchedulerWorker) {
	h.sched.MarkTerminal(w.ID, WorkerCompleted)
	h.publish(w, "success", w.Task+" done")
}

func (h *schedHarness) publish(w *SchedulerWorker, status, summary string) {
	ev := eventbus.NewAgentEvent(eventbus.EventSubagentReport, w.ID)
	ev.NodeID = eventbus.StrPtr(w.ID)
	ev.ExecutionID = eventbus.StrPtr(w.ID)
	ev.Data = map[string]any{
		"worker_id":   w.ID,
		"colony_id":   h.colonyID,
		"task":        w.Task,
		"status":      status,
		"summary":     summary,
		"batch_id":    w.BatchID,
		"batch_index": w.BatchIndex,
		"batch_size":  w.BatchSize,
	}
	h.bus.Publish(ev)
}

func shallowCopy(data map[string]any) map[string]any {
	out := make(map[string]any, len(data))
	for k, v := range data {
		out[k] = v
	}
	return out
}

func (h *schedHarness) openGate(task string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.opened[task] {
		return
	}
	h.opened[task] = true
	close(h.gates[task])
}

func (h *schedHarness) openAllGates() {
	for _, task := range h.tasks {
		h.openGate(task)
	}
}

func (h *schedHarness) reportSnapshot() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.reports...)
}

func (h *schedHarness) reportCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.reports)
}

func (h *schedHarness) stoppedReports() []map[string]any {
	var stopped []map[string]any
	for _, report := range h.reportSnapshot() {
		if report["status"] == "stopped" {
			stopped = append(stopped, report)
		}
	}
	return stopped
}

func (h *schedHarness) startedCount(task string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started[task]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func tasksNamed(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return out
}

func countStatus(h *schedHarness, ids []string, pred func(WorkerStatus) bool) int {
	count := 0
	for _, id := range ids {
		status, ok := h.sched.StatusOf(id)
		if ok && pred(status) {
			count++
		}
	}
	return count
}

// Parity: core/tests/test_colony_scheduler.py::test_spawn_under_cap_starts_all_running
func TestSpawnUnderCapStartsAllRunning(t *testing.T) {
	tasks := tasksNamed("task", 3)
	h := newSchedHarness(t, 4, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	ids := h.sched.SpawnBatch(tasks, "")
	if len(ids) != 3 {
		t.Fatalf("spawned %d workers, want 3", len(ids))
	}
	if depth := h.sched.PendingQueueDepth(); depth != 0 {
		t.Fatalf("pending queue depth = %d, want 0", depth)
	}
	for _, id := range ids {
		status, ok := h.sched.StatusOf(id)
		if !ok {
			t.Fatalf("worker %s is not registered", id)
		}
		if status == WorkerQueued {
			t.Errorf("worker %s is QUEUED under the cap", id)
		}
	}
}

// Parity: core/tests/test_colony_scheduler.py::test_spawn_over_cap_queues_excess
func TestSpawnOverCapQueuesExcess(t *testing.T) {
	tasks := tasksNamed("task", 5)
	h := newSchedHarness(t, 2, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	ids := h.sched.SpawnBatch(tasks, "")
	if len(ids) != 5 {
		t.Fatalf("spawned %d workers, want 5", len(ids))
	}
	running := countStatus(h, ids, WorkerStatus.IsRunningWorker)
	queued := countStatus(h, ids, func(s WorkerStatus) bool { return s == WorkerQueued })
	if running != 2 {
		t.Errorf("running/pending = %d, want 2", running)
	}
	if queued != 3 {
		t.Errorf("queued = %d, want 3", queued)
	}
	if depth := h.sched.PendingQueueDepth(); depth != 3 {
		t.Errorf("pending queue depth = %d, want 3", depth)
	}
}

// Parity: core/tests/test_colony_scheduler.py::test_queued_workers_promote_as_running_peers_finish
func TestQueuedWorkersPromoteAsRunningPeersFinish(t *testing.T) {
	tasks := tasksNamed("task", 4)
	h := newSchedHarness(t, 2, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	h.sched.SpawnBatch(tasks, "")
	if depth := h.sched.PendingQueueDepth(); depth != 2 {
		t.Fatalf("initial pending queue depth = %d, want 2", depth)
	}

	h.openGate("task-0")
	h.openGate("task-1")
	waitFor(t, "two reports and an empty queue", func() bool {
		return h.reportCount() >= 2 && h.sched.PendingQueueDepth() == 0
	})
	if got := h.reportCount(); got != 2 {
		t.Fatalf("reports = %d, want 2", got)
	}

	h.openGate("task-2")
	h.openGate("task-3")
	waitFor(t, "four reports", func() bool { return h.reportCount() >= 4 })
	reports := h.reportSnapshot()
	if len(reports) != 4 {
		t.Fatalf("reports = %d, want 4", len(reports))
	}
	for _, report := range reports {
		if report["status"] != "success" {
			t.Errorf("report status = %v, want success", report["status"])
		}
	}
}

// Parity: core/tests/test_colony_scheduler.py::test_colony_stop_synthesises_stopped_report_for_queued
func TestColonyStopSynthesisesStoppedReportForQueued(t *testing.T) {
	tasks := tasksNamed("task", 3)
	h := newSchedHarness(t, 1, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	ids := h.sched.SpawnBatch(tasks, "")
	if len(ids) != 3 {
		t.Fatalf("spawned %d workers, want 3", len(ids))
	}
	if depth := h.sched.PendingQueueDepth(); depth != 2 {
		t.Fatalf("pending queue depth = %d, want 2", depth)
	}

	h.openGate("task-0")
	waitFor(t, "task-0 success report", func() bool {
		for _, report := range h.reportSnapshot() {
			if report["status"] == "success" {
				return true
			}
		}
		return false
	})

	h.sched.StopWorkers()

	waitFor(t, "stopped reports", func() bool { return len(h.stoppedReports()) >= 2 })
	stopped := h.stoppedReports()
	if len(stopped) < 2 {
		t.Fatalf("stopped reports = %d, want >= 2", len(stopped))
	}
	success := 0
	for _, report := range h.reportSnapshot() {
		if report["status"] == "success" {
			success++
		}
	}
	if success < 1 {
		t.Fatalf("success reports = %d, want >= 1", success)
	}
	for _, report := range stopped {
		for _, key := range []string{"batch_id", "batch_index", "batch_size"} {
			if _, ok := report[key]; !ok {
				t.Errorf("stopped report missing %q: %#v", key, report)
			}
		}
	}
}

// Parity: core/tests/test_colony_scheduler.py::test_batch_metadata_on_subagent_report_with_queued_workers
func TestBatchMetadataOnSubagentReportWithQueuedWorkers(t *testing.T) {
	tasks := tasksNamed("t", 4)
	h := newSchedHarness(t, 2, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	ids := h.sched.SpawnBatch(tasks, "")

	h.openGate("t-0")
	waitFor(t, "first report", func() bool { return h.reportCount() >= 1 })

	first := h.reportSnapshot()[0]
	batchID, ok := first["batch_id"].(string)
	if !ok || batchID == "" {
		t.Fatalf("batch_id = %#v, want a non-empty string", first["batch_id"])
	}
	if first["batch_size"] != 4 {
		t.Errorf("batch_size = %v, want 4", first["batch_size"])
	}
	switch first["batch_index"] {
	case 1, 2, 3, 4:
	default:
		t.Errorf("batch_index = %v, want 1..4", first["batch_index"])
	}
	for _, id := range ids {
		got, ok := h.sched.BatchIDOf(id)
		if !ok || got != batchID {
			t.Errorf("worker %s batch_id = %q, want %q", id, got, batchID)
		}
	}

	h.openAllGates()
	waitFor(t, "four reports", func() bool { return h.reportCount() >= 4 })
	if got := h.reportCount(); got != 4 {
		t.Fatalf("reports = %d, want 4", got)
	}
}

// Parity: core/tests/test_colony_scheduler.py::test_two_batches_interleave_in_pending_queue
func TestTwoBatchesInterleaveInPendingQueue(t *testing.T) {
	tasks := tasksNamed("x", 6)
	h := newSchedHarness(t, 1, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	h.sched.SpawnBatch(tasks[:3], "batch_A")
	h.sched.SpawnBatch(tasks[3:], "batch_B")

	if depth := h.sched.PendingQueueDepth(); depth != 5 {
		t.Fatalf("pending queue depth = %d, want 5", depth)
	}
	queued := h.sched.QueuedWorkers()
	var lastA = -1
	firstB := len(queued)
	for i, worker := range queued {
		switch worker.BatchID {
		case "batch_A":
			lastA = i
		case "batch_B":
			if i < firstB {
				firstB = i
			}
		default:
			t.Fatalf("queued worker %s has batch_id %q", worker.ID, worker.BatchID)
		}
	}
	if lastA < 0 || firstB == len(queued) {
		t.Fatalf("queue lacks both batches: %#v", queued)
	}
	if lastA > firstB {
		t.Fatalf("FIFO violation: last batch_A at %d, first batch_B at %d", lastA, firstB)
	}
}

// Parity-exempt: the dispatch gate is the reference's stop-sweep guard; its
func TestSchedulerDispatchBlockRefusesWithStoppedReport(t *testing.T) {
	h := newSchedHarness(t, 4, "blocked")
	defer h.sched.StopWorkers()

	h.sched.BlockDispatch()
	ids := h.sched.SpawnBatch([]string{"blocked"}, "")
	if len(ids) != 1 {
		t.Fatalf("spawned %d workers, want 1", len(ids))
	}
	status, ok := h.sched.StatusOf(ids[0])
	if !ok || status != WorkerStopped {
		t.Fatalf("status = %q (found=%v), want stopped", status, ok)
	}
	if depth := h.sched.PendingQueueDepth(); depth != 0 {
		t.Errorf("pending queue depth = %d, want 0", depth)
	}
	reports := h.reportSnapshot()
	if len(reports) != 1 || reports[0]["status"] != "stopped" {
		t.Fatalf("reports = %#v, want one stopped report", reports)
	}
	if got := reports[0]["summary"]; got != "Colony was stopped — this task was never started." {
		t.Errorf("summary = %q", got)
	}
}

// Parity-exempt: the reference filters terminal events by colony_id so another
func TestSchedulerIgnoresReportsFromOtherColonies(t *testing.T) {
	tasks := tasksNamed("task", 2)
	h := newSchedHarness(t, 1, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	ids := h.sched.SpawnBatch(tasks, "")
	if depth := h.sched.PendingQueueDepth(); depth != 1 {
		t.Fatalf("pending queue depth = %d, want 1", depth)
	}

	h.sched.MarkTerminal(ids[0], WorkerCompleted)

	foreign := eventbus.NewAgentEvent(eventbus.EventSubagentReport, "elsewhere")
	foreign.Data = map[string]any{"colony_id": "other_colony"}
	h.bus.Publish(foreign)
	if depth := h.sched.PendingQueueDepth(); depth != 1 {
		t.Fatalf("foreign report drained the queue: depth = %d, want 1", depth)
	}

	own := eventbus.NewAgentEvent(eventbus.EventSubagentReport, "mine")
	own.Data = map[string]any{"colony_id": "sched_test"}
	h.bus.Publish(own)
	waitFor(t, "own report to drain the queue", func() bool {
		return h.sched.PendingQueueDepth() == 0
	})
}

// Parity-exempt: the drain's defensive skip of a worker cancelled between queue
func TestSchedulerDrainSkipsCancelledQueuedWorker(t *testing.T) {
	tasks := tasksNamed("task", 2)
	h := newSchedHarness(t, 1, tasks...)
	defer h.sched.StopWorkers()
	defer h.openAllGates()

	ids := h.sched.SpawnBatch(tasks, "")
	if len(ids) != 2 {
		t.Fatalf("spawned %d workers, want 2", len(ids))
	}
	h.sched.MarkTerminal(ids[0], WorkerCompleted)
	h.sched.MarkTerminal(ids[1], WorkerStopped)

	if promoted := h.sched.DrainPendingQueue(); promoted != 0 {
		t.Fatalf("promoted = %d, want 0", promoted)
	}
	if depth := h.sched.PendingQueueDepth(); depth != 0 {
		t.Errorf("pending queue depth = %d, want 0", depth)
	}
	if got := h.startedCount(tasks[1]); got != 0 {
		t.Errorf("cancelled worker started %d times, want 0", got)
	}
}

// Parity-exempt: the minted batch id format (rpw_<UTC timestamp>_<8 hex>) is
func TestMintBatchIDFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^rpw_\d{8}T\d{6}Z_[0-9a-f]{8}$`)
	if got := mintBatchID(); !pattern.MatchString(got) {
		t.Fatalf("mintBatchID() = %q, want %s", got, pattern)
	}
}

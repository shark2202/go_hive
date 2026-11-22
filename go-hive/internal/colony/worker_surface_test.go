package colony

// Runtime read/control surface tests. They exercise the seams the HTTP routes
// duck-type on Session.Colony: registry reads, the live-workers rows, the stop
// cascade, worker input injection, bus subscription, and the accessors used by
// the skills/tools/model/profile routes.

import (
	"context"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

func waitForColony(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return condition()
}

func workerStatusOf(runtime *ColonyRuntime, id string) WorkerStatus {
	worker := runtime.GetWorker(id)
	if worker == nil {
		return ""
	}
	return worker.Status()
}

// Parity: tests/test_worker_started_at.py::test_info_started_at_is_wall_clock
func TestWorkerInfoCarriesRegistryFields(t *testing.T) {
	provider := newGateLLM("slow task")
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{
		{Task: "slow task", ProfileName: "slack-work"},
		{Task: "fast task"},
	})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %d, want 2", len(ids))
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		return workerStatusOf(runtime, ids[0]) == WorkerRunning
	}) {
		t.Fatalf("slow worker never started: %q", workerStatusOf(runtime, ids[0]))
	}

	infos := runtime.ListWorkers()
	if len(infos) != 2 {
		t.Fatalf("ListWorkers = %d, want 2", len(infos))
	}
	if infos[0].WorkerID != ids[0] || infos[1].WorkerID != ids[1] {
		t.Fatalf("ListWorkers order = [%s %s], want sorted [%s %s]", infos[0].WorkerID, infos[1].WorkerID, ids[0], ids[1])
	}

	info, ok := runtime.Worker(ids[0])
	if !ok {
		t.Fatalf("Worker(%q) not found", ids[0])
	}
	if info.ProfileName != "slack-work" {
		t.Fatalf("ProfileName = %q, want slack-work", info.ProfileName)
	}
	if info.StartedAt <= 0 {
		t.Fatalf("StartedAt = %v, want wall-clock seconds", info.StartedAt)
	}
	if info.Status != WorkerRunning {
		t.Fatalf("Status = %q, want running", info.Status)
	}
	if info.Result != nil {
		t.Fatalf("Result = %+v while running, want nil", info.Result)
	}
	if _, ok := runtime.Worker("worker_missing"); ok {
		t.Fatal("Worker reported a missing worker as found")
	}

	close(provider.gate)
	if !waitForColony(t, 5*time.Second, func() bool {
		finished, _ := runtime.Worker(ids[0])
		return finished.Result != nil
	}) {
		t.Fatal("worker never reached a terminal result")
	}
	finished, _ := runtime.Worker(ids[0])
	result := finished.Result
	if result.ReportStatus != "success" {
		t.Fatalf("ReportStatus = %q, want success", result.ReportStatus)
	}
	if result.Summary != "slow task done" {
		t.Fatalf("Summary = %q, want the explicit report summary", result.Summary)
	}
	if result.DurationSeconds <= 0 {
		t.Fatalf("DurationSeconds = %v, want > 0", result.DurationSeconds)
	}
	if result.Data["task"] != "slow task" {
		t.Fatalf("Data = %v, want the report payload", result.Data)
	}
}

// Parity-exempt: routes_workers.py has no Python test for the live-workers
// payload helper; this pins the Go registry row the stream/stop routes map.
func TestLiveWorkersExposeRegistryRows(t *testing.T) {
	provider := newGateLLM("slow task")
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "slow task"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		return workerStatusOf(runtime, ids[0]) == WorkerRunning
	}) {
		t.Fatal("worker never started")
	}

	rows := runtime.LiveWorkers()
	if len(rows) != 1 {
		t.Fatalf("LiveWorkers = %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.ID != ids[0] || row.Status != string(WorkerRunning) || !row.IsActive {
		t.Fatalf("row = %+v, want the running worker", row)
	}
	if row.StartedAt <= 0 {
		t.Fatalf("StartedAt = %v, want wall-clock seconds", row.StartedAt)
	}
	if row.ResultStatus != nil || row.ResultSummary != nil {
		t.Fatalf("result fields = %v/%v while running, want nil", row.ResultStatus, row.ResultSummary)
	}

	if _, ok := runtime.LiveWorker("worker_missing"); ok {
		t.Fatal("LiveWorker reported a missing worker as found")
	}

	close(provider.gate)
	if !waitForColony(t, 5*time.Second, func() bool {
		worker := runtime.GetWorker(ids[0])
		return worker != nil && !worker.IsActive()
	}) {
		t.Fatal("worker never reached a terminal state")
	}
	row, ok := runtime.LiveWorker(ids[0])
	if !ok {
		t.Fatal("terminal worker missing from the registry")
	}
	if row.IsActive {
		t.Fatal("terminal worker still reported active")
	}
	if row.ResultStatus == nil || *row.ResultStatus != "success" {
		t.Fatalf("ResultStatus = %v, want success", row.ResultStatus)
	}
	if row.ResultSummary == nil || *row.ResultSummary != "slow task done" {
		t.Fatalf("ResultSummary = %v, want the report summary", row.ResultSummary)
	}
}

// Parity: tests/test_colony_scheduler.py::test_colony_stop_synthesises_stopped_report_for_queued
func TestStopWorkersCancelsQueuedAndRunningWorkers(t *testing.T) {
	provider := newGateLLM("slow task")
	runtime := makeRunTestColony(t, provider, eventbus.NewEventBus(), 1)

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "slow task a"}, {Task: "slow task b"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		statuses := map[WorkerStatus]int{}
		for _, id := range ids {
			statuses[workerStatusOf(runtime, id)]++
		}
		return statuses[WorkerRunning] == 1 && statuses[WorkerQueued] == 1
	}) {
		t.Fatalf("want one running + one queued worker, got %q/%q", workerStatusOf(runtime, ids[0]), workerStatusOf(runtime, ids[1]))
	}

	summary, err := runtime.StopWorkers(true)
	if err != nil {
		t.Fatalf("StopWorkers: %v", err)
	}
	if got := summary["stopped_count"]; got != 2 {
		t.Fatalf("stopped_count = %v, want 2", got)
	}
	if got := summary["queued_cancelled"]; got != 1 {
		t.Fatalf("queued_cancelled = %v, want 1", got)
	}
	if got := summary["timed_out"].([]string); len(got) != 0 {
		t.Fatalf("timed_out = %v, want none", got)
	}
	for _, id := range ids {
		worker := runtime.GetWorker(id)
		if worker == nil || worker.IsActive() {
			t.Fatalf("worker %s still active after the sweep", id)
		}
		select {
		case <-worker.Done():
		default:
			t.Fatalf("worker %s done channel still open", id)
		}
	}
	if !runtime.DispatchBlocked() {
		t.Fatal("blockDispatch=true did not hold dispatch closed")
	}

	// A fresh spawn is a new work request and re-opens dispatch.
	if _, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "after stop"}}); err != nil {
		t.Fatalf("SpawnBatch after stop: %v", err)
	}
	if runtime.DispatchBlocked() {
		t.Fatal("spawn did not re-open dispatch")
	}
	close(provider.gate)
}

// Parity-exempt: the terminal/unknown branching lives in the Go route seam;
// upstream stop_worker has no Python test of its own.
func TestStopLiveWorkerReportsUnknownAndTerminalWorkers(t *testing.T) {
	provider := newGateLLM("slow task")
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "slow task"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		return workerStatusOf(runtime, ids[0]) == WorkerRunning
	}) {
		t.Fatal("worker never started")
	}

	summary, err := runtime.StopLiveWorker(ids[0])
	if err != nil {
		t.Fatalf("StopLiveWorker: %v", err)
	}
	if got := summary["stopped_count"]; got != 1 {
		t.Fatalf("stopped_count = %v, want 1", got)
	}

	terminal, err := runtime.StopLiveWorker(ids[0])
	if err != nil {
		t.Fatalf("StopLiveWorker on terminal worker: %v", err)
	}
	if got := terminal["stopped_count"]; got != 0 {
		t.Fatalf("stopped_count = %v on an already-terminal worker, want 0", got)
	}

	if _, err := runtime.StopLiveWorker("worker_missing"); err == nil {
		t.Fatal("StopLiveWorker accepted an unknown worker")
	}
	var nilRuntime *ColonyRuntime
	if _, err := nilRuntime.StopLiveWorker("worker_1"); err == nil {
		t.Fatal("StopLiveWorker on a nil runtime returned no error")
	}
	close(provider.gate)
}

// Parity-exempt: framework/host/colony_runtime.py::send_to_worker has no
// Python test; this pins the Go injection path the escalation reply uses.
func TestInjectInputReachesRunningWorker(t *testing.T) {
	provider := newGateLLM("slow task")
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "slow task"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		worker := runtime.GetWorker(ids[0])
		return worker != nil && worker.AgentLoop() != nil
	}) {
		t.Fatal("worker loop never started")
	}
	if err := runtime.InjectInput(ids[0], "escalation queue is full"); err != nil {
		t.Fatalf("InjectInput: %v", err)
	}
	if !runtime.GetWorker(ids[0]).AgentLoop().InputWaiting() {
		t.Fatal("injected message is not queued on the worker loop")
	}
	if err := runtime.InjectInput("worker_missing", "hi"); err == nil {
		t.Fatal("InjectInput accepted an unknown worker")
	}
	close(provider.gate)
}

// Parity: tests/test_sse_worker_filter.py::test_worker_lifecycle_and_report_events_pass_through
func TestSubscribeToEventsDeliversSubagentReports(t *testing.T) {
	runtime := makeTestColony(t, newGateLLM("never matches"), eventbus.NewEventBus())

	received := make(chan *eventbus.AgentEvent, 1)
	subscriptionID, err := runtime.SubscribeToEvents(
		[]eventbus.EventType{eventbus.EventSubagentReport},
		func(event *eventbus.AgentEvent) { received <- event },
	)
	if err != nil {
		t.Fatalf("SubscribeToEvents: %v", err)
	}
	if subscriptionID == "" {
		t.Fatal("SubscribeToEvents returned an empty subscription id")
	}

	if _, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "report please"}}); err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	select {
	case event := <-received:
		if event.Data["colony_id"] != "test_colony" {
			t.Fatalf("colony_id = %v, want test_colony", event.Data["colony_id"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no subagent report on the bus")
	}

	busless := NewColonyRuntime(RuntimeOptions{StoragePath: t.TempDir()})
	if _, err := busless.SubscribeToEvents(nil, func(*eventbus.AgentEvent) {}); err == nil {
		t.Fatal("SubscribeToEvents on a busless runtime returned no error")
	}
}

// Parity-exempt: direct accessor coverage for the skills/tools/model route
// seams; upstream reads these attributes with getattr defaults.
func TestReadSurfaceAccessors(t *testing.T) {
	catalog := []*skills.ParsedSkill{{Name: "alpha", Description: "first"}}
	workerLLM := newGateLLM("never matches")
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:       testAgentSpec(),
		StoragePath:     t.TempDir(),
		LLM:             newGateLLM("never matches"),
		Tools:           []llm.ToolDef{{Name: "shell", Description: "run a command"}},
		StreamID:        "colony_alpha",
		EventBus:        eventbus.NewEventBus(),
		WorkerLLM:       workerLLM,
		SkillsProvider:  func() ([]*skills.ParsedSkill, bool) { return catalog, true },
		ToolProviderMap: map[string]string{"shell": "local"},
	})

	if got := runtime.ColonyID(); got != "colony_alpha" {
		t.Fatalf("ColonyID = %q, want colony_alpha", got)
	}
	if got := runtime.StreamID(); got != runtime.ColonyID() {
		t.Fatalf("StreamID = %q, want the colony id", got)
	}
	if got := runtime.StoragePath(); got != runtime.storagePath {
		t.Fatalf("StoragePath = %q, want %q", got, runtime.storagePath)
	}
	listed, ok := runtime.Skills()
	if !ok || len(listed) != 1 || listed[0].Name != "alpha" {
		t.Fatalf("Skills = %v/%v, want the catalog", listed, ok)
	}
	tools, providers := runtime.Tools()
	if len(tools) != 1 || tools[0].Name != "shell" || tools[0].Description != "run a command" {
		t.Fatalf("Tools = %+v, want the shell tool", tools)
	}
	if providers["shell"] != "local" {
		t.Fatalf("providers = %v, want shell->local", providers)
	}
	if runtime.WorkerLLM() != any(workerLLM) {
		t.Fatalf("WorkerLLM = %v, want the configured worker completer", runtime.WorkerLLM())
	}

	bare := NewColonyRuntime(RuntimeOptions{StoragePath: t.TempDir()})
	if _, ok := bare.Skills(); ok {
		t.Fatal("Skills reported a catalog on a runtime without a provider")
	}
	if got := bare.WorkerLLM(); got != nil {
		t.Fatalf("WorkerLLM = %v on a bare runtime, want nil", got)
	}
	if tools, providers := bare.Tools(); len(tools) != 0 || len(providers) != 0 {
		t.Fatalf("Tools = %v/%v on a bare runtime, want empty", tools, providers)
	}
	if got := bare.ColonyID(); got != "" {
		t.Fatalf("ColonyID = %q on a bare runtime, want empty", got)
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_workers_inherit_only_queens_tools
func TestBoundWorkersForProfile(t *testing.T) {
	provider := newGateLLM("slow task")
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{
		{Task: "slow task one", ProfileName: "slack-work"},
		{Task: "slow task two"},
		{Task: "slow task three", ProfileName: "slack-work"},
	})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %d, want 3", len(ids))
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		return len(runtime.ListWorkers()) == 3
	}) {
		t.Fatal("workers never registered")
	}

	bound := runtime.BoundWorkersForProfile("slack-work")
	if len(bound) != 2 || bound[0] != ids[0] || bound[1] != ids[2] {
		t.Fatalf("BoundWorkersForProfile = %v, want [%s %s]", bound, ids[0], ids[2])
	}
	if other := runtime.BoundWorkersForProfile("missing-profile"); other == nil || len(other) != 0 {
		t.Fatalf("BoundWorkersForProfile(missing) = %v, want empty non-nil", other)
	}
	if empty := runtime.BoundWorkersForProfile(""); empty == nil || len(empty) != 0 {
		t.Fatalf("BoundWorkersForProfile(\"\") = %v, want empty non-nil", empty)
	}
	close(provider.gate)
}

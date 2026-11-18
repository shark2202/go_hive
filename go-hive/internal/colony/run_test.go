package colony

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// makeRunTestColony mirrors makeTestColony but exposes the concurrency cap.
func makeRunTestColony(t *testing.T, provider llm.Completer, bus *eventbus.EventBus, maxConcurrent int) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus:             bus,
		StreamID:             "run_test_colony",
		MaxConcurrentWorkers: maxConcurrent,
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// gateLLM blocks the task whose text contains slowTask until gate closes (or
// the worker context is cancelled), then reports success.
type gateLLM struct {
	slowTask string
	gate     chan struct{}

	mu    sync.Mutex
	calls map[string]int
}

func newGateLLM(slowTask string) *gateLLM {
	return &gateLLM{slowTask: slowTask, gate: make(chan struct{}), calls: map[string]int{}}
}

func (g *gateLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	task := firstUserText(req.Messages)
	g.mu.Lock()
	g.calls[task]++
	g.mu.Unlock()

	if strings.Contains(task, g.slowTask) {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return llm.Completion{}, ctx.Err()
		}
	}
	arguments, _ := json.Marshal(map[string]any{
		"status":  "success",
		"summary": task + " done",
		"data":    map[string]any{"task": task},
	})
	return llm.Completion{
		ToolCalls: []llm.ToolCall{{
			ID:        "report_1",
			Name:      "report_to_parent",
			Arguments: string(arguments),
		}},
		StopReason: "tool_calls",
	}, nil
}

func (g *gateLLM) callCount(task string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[task]
}

func (g *gateLLM) totalCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	total := 0
	for _, count := range g.calls {
		total += count
	}
	return total
}

// outcomeLLM routes each task to success, partial, or a hard error.
type outcomeLLM struct {
	byTask map[string]string
}

func (o *outcomeLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	task := firstUserText(req.Messages)
	outcome := ""
	for key, value := range o.byTask {
		if strings.Contains(task, key) {
			outcome = value
			break
		}
	}
	switch outcome {
	case "error":
		return llm.Completion{}, errors.New("simulated worker crash")
	case "partial":
		arguments, _ := json.Marshal(map[string]any{
			"status":  "partial",
			"summary": task + " partially done",
			"data":    map[string]any{"task": task},
		})
		return llm.Completion{
			ToolCalls:  []llm.ToolCall{{ID: "report_1", Name: "report_to_parent", Arguments: string(arguments)}},
			StopReason: "tool_calls",
		}, nil
	default:
		arguments, _ := json.Marshal(map[string]any{
			"status":  "success",
			"summary": task + " done",
			"data":    map[string]any{"task": task},
		})
		return llm.Completion{
			ToolCalls:  []llm.ToolCall{{ID: "report_1", Name: "report_to_parent", Arguments: string(arguments)}},
			StopReason: "tool_calls",
		}, nil
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports
func TestRunBatchExecutesWorkersAndAggregates(t *testing.T) {
	provider := newTaskScriptedLLM(map[string]scriptedResponse{
		"alpha": reportResponse("success", "alpha done", map[string]any{"rows": 1}),
		"beta":  reportResponse("success", "beta done", map[string]any{"rows": 2}),
	})
	runtime := makeRunTestColony(t, provider, eventbus.NewEventBus(), 2)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{
		{Task: "alpha", Data: map[string]any{"batch": 1}},
		{Task: "beta", Data: map[string]any{"batch": 2}},
	}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}

	if result.Status != RunCompleted {
		t.Fatalf("status = %s, want completed", result.Status)
	}
	if result.Succeeded != 2 || result.Failed != 0 || result.Stopped != 0 || result.Partial != 0 {
		t.Fatalf("counts = succeeded:%d partial:%d failed:%d stopped:%d, want 2/0/0/0",
			result.Succeeded, result.Partial, result.Failed, result.Stopped)
	}
	if len(result.WorkerResults) != 2 {
		t.Fatalf("worker results = %d, want 2", len(result.WorkerResults))
	}
	if result.WorkerResults[0].Task != "alpha" || result.WorkerResults[1].Task != "beta" {
		t.Fatalf("worker order = %q, %q; want alpha, beta",
			result.WorkerResults[0].Task, result.WorkerResults[1].Task)
	}
	if result.WorkerResults[0].Summary != "alpha done" || result.WorkerResults[1].Summary != "beta done" {
		t.Fatalf("summaries = %q, %q", result.WorkerResults[0].Summary, result.WorkerResults[1].Summary)
	}
	firstID := result.WorkerResults[0].WorkerID
	assertJSONEqual(t, result.Data[firstID], map[string]any{"rows": 1})
	if worker := runtime.GetWorker(firstID); worker == nil || worker.Context == nil {
		t.Fatal("worker context missing after the run")
	} else if worker.Context.InputData["batch"] != 1 {
		t.Fatalf("worker InputData.batch = %v, want 1", worker.Context.InputData["batch"])
	}
	if result.Duration <= 0 {
		t.Fatal("duration <= 0")
	}
}

// Parity-exempt: Go colony-run aggregation of an all-failure batch; no Python counterpart.
func TestRunBatchAllFailuresYieldFailedPhase(t *testing.T) {
	provider := &outcomeLLM{byTask: map[string]string{
		"boom one": "error",
		"boom two": "error",
	}}
	runtime := makeRunTestColony(t, provider, eventbus.NewEventBus(), 2)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{
		{Task: "boom one"},
		{Task: "boom two"},
	}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Status != RunFailed {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if result.Failed != 2 || result.Succeeded != 0 {
		t.Fatalf("failed=%d succeeded=%d, want 2/0", result.Failed, result.Succeeded)
	}
	if result.Error == nil {
		t.Fatal("result.Error = nil, want a run-level failure reason")
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports
func TestRunBatchAggregatesPartialAndFailedOutcomes(t *testing.T) {
	provider := &outcomeLLM{byTask: map[string]string{
		"good": "success",
		"half": "partial",
		"bad":  "error",
	}}
	runtime := makeRunTestColony(t, provider, eventbus.NewEventBus(), 3)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{
		{Task: "good task"},
		{Task: "half task"},
		{Task: "bad task"},
	}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}

	if result.Status != RunPartial {
		t.Fatalf("status = %s, want partial", result.Status)
	}
	if result.Succeeded != 1 || result.Partial != 1 || result.Failed != 1 {
		t.Fatalf("counts = %d/%d/%d, want 1 success, 1 partial, 1 failed",
			result.Succeeded, result.Partial, result.Failed)
	}
	if result.Error == nil {
		t.Fatal("aggregate error = nil, want the failed worker's error")
	}
	if len(result.Summaries) != 3 {
		t.Fatalf("summaries = %d, want 3", len(result.Summaries))
	}
}

// Parity: core/tests/test_colony_scheduler.py::test_spawn_over_cap_queues_excess, core/tests/test_colony_scheduler.py::test_queued_workers_promote_as_running_peers_finish
func TestRunOverCapQueuesExcessAndPromotes(t *testing.T) {
	provider := newGateLLM("slow")
	runtime := makeRunTestColony(t, provider, eventbus.NewEventBus(), 1)

	run, err := runtime.SpawnRun(context.Background(), []BatchTask{
		{Task: "slow task one"},
		{Task: "slow task two"},
	}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("SpawnRun: %v", err)
	}
	if len(run.WorkerIDs()) != 2 {
		t.Fatalf("worker ids = %d, want 2", len(run.WorkerIDs()))
	}

	first := runtime.GetWorker(run.WorkerIDs()[0])
	second := runtime.GetWorker(run.WorkerIDs()[1])
	if first == nil || second == nil {
		t.Fatal("spawned workers missing from the runtime registry")
	}
	if !eventually(t, 2*time.Second, func() bool {
		running, queued := 0, 0
		for _, worker := range []*Worker{first, second} {
			switch worker.Status() {
			case WorkerRunning:
				running++
			case WorkerQueued:
				queued++
			}
		}
		return running == 1 && queued == 1 && provider.totalCalls() == 1
	}) {
		t.Fatalf("statuses = %s/%s calls=%d, want exactly one running and one queued",
			first.Status(), second.Status(), provider.totalCalls())
	}

	close(provider.gate)
	result, err := run.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.Status != RunCompleted {
		t.Fatalf("status = %s, want completed after promotion", result.Status)
	}
	if result.Succeeded != 2 {
		t.Fatalf("succeeded = %d, want 2", result.Succeeded)
	}
}

// Parity-exempt: Go colony-run timeout path; no Python test exercises run-level deadlines.
func TestRunTimeoutStopsWorkersAndPublishesStoppedReport(t *testing.T) {
	provider := newGateLLM("blocked")
	bus := eventbus.NewEventBus()
	runtime := makeRunTestColony(t, provider, bus, 1)
	reports := collectEvents(bus, eventbus.EventSubagentReport)

	run, err := runtime.SpawnRun(context.Background(), []BatchTask{{Task: "blocked task"}}, RunSpawnOptions{
		Timeout: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("SpawnRun: %v", err)
	}
	result, err := run.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if result.Status != RunStopped {
		t.Fatalf("status = %s, want stopped", result.Status)
	}
	if result.Stopped != 1 || result.Succeeded != 0 {
		t.Fatalf("counts = succeeded:%d stopped:%d, want 0/1", result.Succeeded, result.Stopped)
	}
	if result.Error == nil {
		t.Fatal("aggregate error = nil, want a stopped explanation")
	}
	if run.Phase() != RunStopped {
		t.Fatalf("phase = %s, want stopped", run.Phase())
	}
	worker := runtime.GetWorker(run.WorkerIDs()[0])
	if worker == nil || worker.Status() != WorkerStopped {
		t.Fatalf("worker status = %v, want stopped", worker)
	}

	stoppedReports := 0
	for _, event := range reports.snapshot() {
		if event.Data["worker_id"] == worker.ID && event.Data["status"] == "stopped" {
			stoppedReports++
			if event.Data["colony_id"] != "run_test_colony" {
				t.Fatalf("report colony_id = %v, want run_test_colony", event.Data["colony_id"])
			}
		}
	}
	if stoppedReports != 1 {
		t.Fatalf("stopped reports = %d, want exactly 1", stoppedReports)
	}
}

// Parity: core/tests/test_colony_scheduler.py::test_colony_stop_synthesises_stopped_report_for_queued
func TestStopWorkerStopsQueuedWorkerWithReport(t *testing.T) {
	provider := newGateLLM("slow")
	bus := eventbus.NewEventBus()
	runtime := makeRunTestColony(t, provider, bus, 1)
	reports := collectEvents(bus, eventbus.EventSubagentReport)

	run, err := runtime.SpawnRun(context.Background(), []BatchTask{
		{Task: "slow task one"},
		{Task: "slow task two"},
	}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("SpawnRun: %v", err)
	}

	first := runtime.GetWorker(run.WorkerIDs()[0])
	second := runtime.GetWorker(run.WorkerIDs()[1])
	var queued *Worker
	if !eventually(t, 2*time.Second, func() bool {
		running, waiting := 0, 0
		queued = nil
		for _, worker := range []*Worker{first, second} {
			switch worker.Status() {
			case WorkerRunning:
				running++
			case WorkerQueued:
				waiting++
				queued = worker
			}
		}
		return running == 1 && waiting == 1
	}) {
		t.Fatalf("statuses = %s/%s, want one running and one queued", first.Status(), second.Status())
	}

	if !runtime.StopWorker(queued.ID) {
		t.Fatal("StopWorker(queued) = false, want true")
	}
	if !eventually(t, 2*time.Second, func() bool { return queued.Status() == WorkerStopped }) {
		t.Fatalf("queued worker status = %s, want stopped", queued.Status())
	}
	if runtime.StopWorker(queued.ID) {
		t.Fatal("StopWorker on a terminal worker = true, want false")
	}

	close(provider.gate)
	result, err := run.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.Status != RunPartial {
		t.Fatalf("status = %s, want partial (one success, one stopped)", result.Status)
	}
	if result.Succeeded != 1 || result.Stopped != 1 {
		t.Fatalf("counts = succeeded:%d stopped:%d, want 1/1", result.Succeeded, result.Stopped)
	}

	stoppedReports := 0
	for _, event := range reports.snapshot() {
		if event.Data["worker_id"] == queued.ID && event.Data["status"] == "stopped" {
			stoppedReports++
		}
	}
	if stoppedReports != 1 {
		t.Fatalf("stopped reports = %d, want exactly 1", stoppedReports)
	}
}

// Parity-exempt: Go-only colony lifecycle events on the colony stream; no Python counterpart.
func TestRunPublishesColonyLifecycleEvents(t *testing.T) {
	provider := newTaskScriptedLLM(map[string]scriptedResponse{
		"one": reportResponse("success", "one done", nil),
		"two": reportResponse("success", "two done", nil),
	})
	bus := eventbus.NewEventBus()
	runtime := makeRunTestColony(t, provider, bus, 2)
	lifecycle := collectEvents(bus, eventbus.EventExecutionStarted, eventbus.EventExecutionCompleted, eventbus.EventExecutionFailed)

	run, err := runtime.SpawnRun(context.Background(), []BatchTask{
		{Task: "one"},
		{Task: "two"},
	}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("SpawnRun: %v", err)
	}
	if _, err := run.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	var started, completed *eventbus.AgentEvent
	for _, event := range lifecycle.snapshot() {
		if event.ExecutionID == nil || *event.ExecutionID != run.ID() {
			continue
		}
		switch event.Type {
		case eventbus.EventExecutionStarted:
			started = event
		case eventbus.EventExecutionCompleted:
			completed = event
		}
	}
	if started == nil {
		t.Fatal("no execution_started event for the colony run")
	}
	if completed == nil {
		t.Fatal("no execution_completed event for the colony run")
	}
	if started.ColonyID == nil || *started.ColonyID != "run_test_colony" {
		t.Fatalf("started colony_id = %v, want run_test_colony", started.ColonyID)
	}
	if completed.Data["status"] != string(RunCompleted) {
		t.Fatalf("completed data.status = %v, want completed", completed.Data["status"])
	}
	if completed.Data["succeeded"] != 2 {
		t.Fatalf("completed data.succeeded = %v, want 2", completed.Data["succeeded"])
	}
}

// Parity-exempt: Go-only cancellation race guard; preserves a report that lands before stop.
func TestFinishWorkerStoppedPreservesExplicitReport(t *testing.T) {
	bus := eventbus.NewEventBus()
	harness := makeRunTestColony(t, newScriptedLLM(textResponse("noop")), bus, 1)
	reports := collectEvents(bus, eventbus.EventSubagentReport)

	worker := NewWorker("worker_manual", "manual task", "worker:worker_manual")
	worker.RecordExplicitReport(map[string]any{
		"status":  "success",
		"summary": "finished just before the stop",
		"data":    map[string]any{"rows": 3},
	})
	harness.finishWorkerStopped(worker, "Worker was cancelled before completion.")

	if worker.Status() != WorkerCompleted {
		t.Fatalf("status = %s, want completed (report preserved)", worker.Status())
	}
	if report := worker.ExplicitReport(); report == nil || report["summary"] != "finished just before the stop" {
		t.Fatalf("explicit report lost: %v", report)
	}
	events := reports.snapshot()
	if len(events) != 1 {
		t.Fatalf("reports = %d, want 1", len(events))
	}
	if events[0].Data["status"] != "success" {
		t.Fatalf("report status = %v, want success", events[0].Data["status"])
	}
}

// Parity-exempt: Go-only guard for the run facade's empty-batch contract.
func TestSpawnRunRejectsEmptyTaskList(t *testing.T) {
	runtime := makeRunTestColony(t, newScriptedLLM(textResponse("noop")), eventbus.NewEventBus(), 1)

	if _, err := runtime.SpawnRun(context.Background(), nil, RunSpawnOptions{}); err == nil {
		t.Fatal("SpawnRun(nil) error = nil, want an error")
	}
}

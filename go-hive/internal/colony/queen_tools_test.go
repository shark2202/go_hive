package colony

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// fakeRunWorkerSpawner is the fake colony wire: it records the batch it was
// handed and answers with worker stubs, so the tool's validation, payload and
// breadcrumbs can be asserted without an LLM.
type fakeRunWorkerSpawner struct {
	mu       sync.Mutex
	tasks    []BatchTask
	opts     SpawnBatchOptions
	workers  map[string]*Worker
	failWith error
	watched  []string
}

func (f *fakeRunWorkerSpawner) SpawnBatchWithOptions(_ context.Context, tasks []BatchTask, opts SpawnBatchOptions) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.tasks = append([]BatchTask(nil), tasks...)
	f.opts = opts
	if f.workers == nil {
		f.workers = map[string]*Worker{}
	}
	ids := make([]string, 0, len(tasks))
	for index := range tasks {
		id := "worker:" + string(rune('a'+index))
		worker := NewWorker(id, tasks[index].Task, "")
		worker.BatchID = opts.BatchID
		worker.BatchIndex = index
		worker.BatchSize = len(tasks)
		f.workers[id] = worker
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *fakeRunWorkerSpawner) GetWorker(id string) *Worker {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.workers[id]
}

func (f *fakeRunWorkerSpawner) MaxConcurrentWorkers() int { return 4 }

func (f *fakeRunWorkerSpawner) WatchBatchTimeouts(ids []string, _, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watched = append([]string(nil), ids...)
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_validates_tasks_input
func TestRunWorkerToolSchema(t *testing.T) {
	def := RunWorkerToolDef()
	if def.Name != "run_worker" {
		t.Fatalf("tool name = %q, want run_worker", def.Name)
	}
	properties, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters.properties type = %T", def.Parameters["properties"])
	}
	for _, key := range []string{"tasks", "timeout", "max_iterations", "tool_call_lifetime_budget", "resume_worker_ids", "guidance"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("schema missing %q", key)
		}
	}
	tasks := properties["tasks"].(map[string]any)
	items := tasks["items"].(map[string]any)
	required, _ := items["required"].([]string)
	if len(required) != 1 || required[0] != "task" {
		t.Fatalf("tasks.items.required = %v, want [task]", items["required"])
	}
	itemProps := items["properties"].(map[string]any)
	for _, key := range []string{"task", "data", "goal", "preload_tools"} {
		if _, ok := itemProps[key]; !ok {
			t.Fatalf("tasks.items.properties missing %q", key)
		}
	}
	if tasks["minItems"] != 1 {
		t.Fatalf("tasks.minItems = %v, want 1", tasks["minItems"])
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_returns_error_when_no_colony
func TestRunWorkerRejectsWithoutColony(t *testing.T) {
	output, isError := RunWorker(context.Background(), nil, map[string]any{
		"tasks": []any{map[string]any{"task": "do a thing"}},
	})
	if !isError {
		t.Fatalf("unbound run_worker reported success: %s", output)
	}
	payload := decodeToolPayload(t, output)
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "No colony is bound") {
		t.Fatalf("error = %q, want an actionable no-colony message", message)
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_immediate_return_includes_batch_breadcrumbs
func TestRunWorkerSpawnsBatchAndReturnsBreadcrumbs(t *testing.T) {
	spawner := &fakeRunWorkerSpawner{}
	output, isError := RunWorker(context.Background(), spawner, map[string]any{
		"tasks": []any{
			map[string]any{"task": "first slice", "data": map[string]any{"rows": []any{1.0, 2.0}}},
			map[string]any{"task": "second slice", "goal": "Check two rows"},
		},
		"max_iterations":            7,
		"tool_call_lifetime_budget": 20,
		"timeout":                   30,
	})
	if isError {
		t.Fatalf("run_worker failed: %s", output)
	}
	payload := decodeToolPayload(t, output)
	if payload["status"] != "started" {
		t.Fatalf("status = %v, want started", payload["status"])
	}
	batchID, _ := payload["batch_id"].(string)
	if !strings.HasPrefix(batchID, "rpw_") {
		t.Fatalf("batch_id = %q, want rpw_ prefix", batchID)
	}
	if payload["worker_count"] != float64(2) {
		t.Fatalf("worker_count = %v, want 2", payload["worker_count"])
	}
	if payload["soft_timeout_seconds"] != float64(30) {
		t.Fatalf("soft_timeout_seconds = %v, want 30", payload["soft_timeout_seconds"])
	}
	if payload["hard_timeout_seconds"] != float64(630) {
		t.Fatalf("hard_timeout_seconds = %v, want 630 (soft+600)", payload["hard_timeout_seconds"])
	}
	workers, _ := payload["workers"].([]any)
	if len(workers) != 2 {
		t.Fatalf("breadcrumbs = %d, want 2", len(workers))
	}
	first := workers[0].(map[string]any)
	if first["task_preview"] != "first slice" || first["task_index"] != float64(1) {
		t.Fatalf("first breadcrumb = %v", first)
	}
	if spawner.opts.BatchID != batchID {
		t.Fatalf("spawned batch id = %q, want %q", spawner.opts.BatchID, batchID)
	}
	if spawner.opts.LoopConfigOverrides["max_iterations"] != 7 ||
		spawner.opts.LoopConfigOverrides["tool_call_lifetime_budget"] != 20 {
		t.Fatalf("loop overrides = %v", spawner.opts.LoopConfigOverrides)
	}
	if len(spawner.tasks) != 2 || spawner.tasks[0].Data["task"] != "first slice" {
		t.Fatalf("tasks handed to the colony = %+v", spawner.tasks)
	}
	if spawner.tasks[1].Goal != "Check two rows" {
		t.Fatalf("goal = %q", spawner.tasks[1].Goal)
	}
	if len(spawner.watched) != 2 {
		t.Fatalf("timeout watcher got %v, want both worker ids", spawner.watched)
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_validates_tasks_input
func TestRunWorkerValidatesInputs(t *testing.T) {
	spawner := &fakeRunWorkerSpawner{}
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"missing tasks", map[string]any{}, "non-empty list"},
		{"empty task", map[string]any{"tasks": []any{map[string]any{"task": "  "}}}, "task is empty"},
		{"not a dict", map[string]any{"tasks": []any{"nope"}}, "is not a dict"},
		{"both modes", map[string]any{
			"tasks":             []any{map[string]any{"task": "x"}},
			"resume_worker_ids": []any{"worker:a"},
		}, "not both"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output, isError := RunWorker(context.Background(), spawner, testCase.input)
			if !isError {
				t.Fatalf("invalid input reported success: %s", output)
			}
			payload := decodeToolPayload(t, output)
			message, _ := payload["error"].(string)
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("error = %q, want substring %q", message, testCase.want)
			}
		})
	}
	if len(spawner.tasks) != 0 {
		t.Fatalf("invalid input still spawned %d workers", len(spawner.tasks))
	}
}

// Parity-exempt: resume mode intentionally reports an unsupported-mode error
// in this port (the Go colony runtime has no persisted worker conversations),
// so the upstream resume test ::test_run_worker_resume_mode_calls_resume_worker
// has no behavioural counterpart to assert here.
func TestRunWorkerResumeIsExplicitlyUnsupported(t *testing.T) {
	output, isError := RunWorker(context.Background(), &fakeRunWorkerSpawner{}, map[string]any{
		"resume_worker_ids": []any{"worker:a"},
	})
	if !isError {
		t.Fatalf("resume mode reported success: %s", output)
	}
	payload := decodeToolPayload(t, output)
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "not supported") {
		t.Fatalf("error = %q, want an explicit unsupported-mode message", message)
	}
}

// Parity: tests/test_run_worker_tool.py::test_subagent_report_carries_batch_metadata
func TestFormatWorkerReportMatchesUpstreamShape(t *testing.T) {
	report := FormatWorkerReport(map[string]any{
		"worker_id":        "worker:1",
		"batch_id":         "rpw_20260101T000000Z_abcd1234",
		"batch_index":      2,
		"batch_size":       3,
		"status":           "success",
		"summary":          "did the thing",
		"task":             strings.Repeat("long task ", 40),
		"duration_seconds": 12.34,
		"tool_calls_used":  5,
		"data":             map[string]any{"rows": 7.0},
	}, 1)

	for _, want := range []string{
		"[WORKER_REPORT]",
		"<worker_id>worker:1</worker_id>",
		"<batch_id>rpw_20260101T000000Z_abcd1234</batch_id>",
		"<task_index>2</task_index>",
		"<task_count>3</task_count>",
		"<batch_remaining>1</batch_remaining>",
		"<status>success</status>",
		"<duration_seconds>12.3</duration_seconds>",
		"<tool_calls>5</tool_calls>",
		"<summary>did the thing</summary>",
		`<data>{"rows":7}</data>`,
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if !strings.Contains(report, "…</original_task>") {
		t.Fatalf("long task was not previewed:\n%s", report)
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_tool_returns_immediately_and_emits_reports
func TestSubscribeQueenReportsForwardsFormattedReports(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := NewColonyRuntime(RuntimeOptions{EventBus: bus, StreamID: "colony_alpha"})
	worker := NewWorker("worker:1", "slice", "")
	worker.BatchID = "batch_1"
	worker.BatchSize = 2
	worker.SetStatus(WorkerRunning)
	runtime.mu.Lock()
	runtime.workers[worker.ID] = worker
	runtime.mu.Unlock()

	var mu sync.Mutex
	var reports []string
	unsubscribe, err := SubscribeQueenReports(runtime, func(report string) {
		mu.Lock()
		reports = append(reports, report)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("SubscribeQueenReports: %v", err)
	}
	defer unsubscribe()

	runtime.publishEvent(worker, map[string]any{
		"worker_id":  "worker:1",
		"batch_id":   "batch_1",
		"batch_size": 2,
		"status":     "success",
		"summary":    "finished",
		"task":       "slice",
	})

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("sink calls = %d, want 1", len(reports))
	}
	if !strings.Contains(reports[0], "[WORKER_REPORT]") ||
		!strings.Contains(reports[0], "<batch_remaining>0</batch_remaining>") {
		t.Fatalf("report = %q", reports[0])
	}
}

// Parity: tests/test_run_worker_tool.py::test_watch_batch_timeouts_soft_inject_only_hits_stragglers
func TestWatchBatchTimeoutsSoftInjectsStragglers(t *testing.T) {
	runtime := NewColonyRuntime(RuntimeOptions{EventBus: eventbus.NewEventBus(), StreamID: "colony_alpha"})

	straggler := NewWorker("worker:slow", "slow", "")
	straggler.SetStatus(WorkerRunning)
	var cancelled sync.WaitGroup
	cancelled.Add(1)
	straggler.setCancel(cancelled.Done)
	reporter := NewWorker("worker:done", "done", "")
	reporter.SetStatus(WorkerRunning)
	reporter.RecordExplicitReport(map[string]any{"status": "success", "summary": "reported"})

	runtime.mu.Lock()
	runtime.workers[straggler.ID] = straggler
	runtime.workers[reporter.ID] = reporter
	runtime.mu.Unlock()

	runtime.WatchBatchTimeouts([]string{straggler.ID, reporter.ID}, 10*time.Millisecond, 40*time.Millisecond)
	cancelled.Wait()
	straggler.mu.Lock()
	stopped := straggler.stopRequested
	straggler.mu.Unlock()
	if !stopped {
		t.Fatal("hard timeout did not stop the straggler")
	}
	if reporter.ExplicitReport() == nil {
		t.Fatal("explicit report was dropped")
	}
}

// Parity-exempt: Go-side tracker wiring for the watcher (upstream's
// _background_tasks set); the Python test only pins the soft-inject behaviour,
// and this pins that Stop cancels the watcher instead of leaving it sleeping.
func TestWatchBatchTimeoutsTracksWatcherUntilStop(t *testing.T) {
	runtime := NewColonyRuntime(RuntimeOptions{
		EventBus:    eventbus.NewEventBus(),
		StreamID:    "colony_alpha",
		StoragePath: t.TempDir(),
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runtime.Stop()

	const workerID = "worker:idle"
	runtime.WatchBatchTimeouts([]string{workerID}, time.Hour, 2*time.Hour)

	handle, ok := runtime.background.Lookup("batch-timeout:" + workerID)
	if !ok {
		t.Fatal("batch-timeout watcher was not tracked")
	}
	if runtime.background.Len() != 1 {
		t.Fatalf("tracked watchers = %d, want 1", runtime.background.Len())
	}

	runtime.Stop()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handle.Wait(waitCtx); err == nil {
		t.Fatal("watcher Wait = nil after Stop, want cancellation")
	}
	if runtime.background.Len() != 0 {
		t.Fatalf("tracked watchers after Stop = %d, want 0", runtime.background.Len())
	}
}

func decodeToolPayload(t *testing.T, output string) map[string]any {
	t.Helper()
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("tool output is not JSON (%q): %v", output, err)
	}
	return payload
}

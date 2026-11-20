package colony

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/tracker"
)

// stopHookRuntime wraps the playbook fake so a test can act while a run is in
// flight — exactly the window action="status"/"stop" exists for.
type stopHookRuntime struct {
	*fakePlaybookRuntime
	onSpawn func(runID string)
}

func (r *stopHookRuntime) SpawnBatchWithOptions(ctx context.Context, tasks []BatchTask, opts SpawnBatchOptions) ([]string, error) {
	if r.onSpawn != nil {
		r.onSpawn(opts.BatchID)
	}
	return r.fakePlaybookRuntime.SpawnBatchWithOptions(ctx, tasks, opts)
}

// Parity-exempt: upstream's get_playbook_status reads a background task table;
// the Go runner is synchronous, so its status surface is the colony's
// playbook_runs history plus the in-process registry.
func TestRunPlaybookStatusListsRunsAndHistory(t *testing.T) {
	runtime := playbookTestRuntime(t, 0)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"action": "status"})
	if isError {
		t.Fatalf("status reported an error: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "ok" {
		t.Fatalf("status = %v, want ok", payload["status"])
	}
	if got, _ := payload["running_count"].(float64); got != 0 {
		t.Fatalf("running_count = %v, want 0", payload["running_count"])
	}
	if runs, ok := payload["runs"].([]any); !ok || len(runs) != 0 {
		t.Fatalf("runs = %v, want an empty history", payload["runs"])
	}
	if !strings.HasSuffix(payload["log_db"].(string), "colony.db") {
		t.Fatalf("log_db = %v", payload["log_db"])
	}
}

// Parity: core/framework/tools/playbook_tools.py::stop_playbook (not_running)
func TestRunPlaybookStopUnknownRun(t *testing.T) {
	runtime := playbookTestRuntime(t, 0)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"action": "stop", "run_id": "pb_does_not_exist",
	})
	if isError {
		t.Fatalf("stopping an unknown run is not a tool error: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "not_running" {
		t.Fatalf("status = %v, want not_running", payload["status"])
	}

	// Missing run_id is the caller's mistake, so it is a tool error.
	content, isError = RunPlaybook(context.Background(), runtime, map[string]any{"action": "stop"})
	if !isError {
		t.Fatalf("stop without run_id reported success: %s", content)
	}
	if !strings.Contains(playbookPayload(t, content)["error"].(string), "needs `run_id`") {
		t.Fatalf("error = %s", content)
	}

	// An unknown action names the supported set instead of running anything.
	content, isError = RunPlaybook(context.Background(), runtime, map[string]any{"action": "pause"})
	if !isError {
		t.Fatalf("unknown action reported success: %s", content)
	}
	if !strings.Contains(playbookPayload(t, content)["error"].(string), "'run', 'status' or 'stop'") {
		t.Fatalf("error = %s", content)
	}
}

// Parity: core/framework/tools/playbook_tools.py::stop_playbook — a run is
// visible by run_id while it executes and stop cancels it without the tool
// reporting a failure.
func TestRunPlaybookStatusSeesLiveRunAndStopCancelsIt(t *testing.T) {
	base := playbookTestRuntime(t, 1)
	var (
		statusPayload map[string]any
		stopPayload   map[string]any
		liveRunID     string
	)
	runtime := &stopHookRuntime{fakePlaybookRuntime: base}
	runtime.onSpawn = func(runID string) {
		liveRunID = runID
		status, isError := RunPlaybook(context.Background(), runtime, map[string]any{"action": "status"})
		if isError {
			t.Errorf("status during a run failed: %s", status)
			return
		}
		statusPayload = playbookPayload(t, status)
		stopped, isError := RunPlaybook(context.Background(), runtime, map[string]any{
			"action": "stop", "run_id": runID,
		})
		if isError {
			t.Errorf("stop during a run failed: %s", stopped)
		}
		stopPayload = playbookPayload(t, stopped)
	}

	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": nativeSpecJSON()})
	if isError {
		t.Fatalf("run_playbook: %s", content)
	}
	if liveRunID == "" {
		t.Fatal("the run never reached the dispatch hook")
	}

	running, _ := statusPayload["running"].([]any)
	if len(running) != 1 {
		t.Fatalf("status saw %v running runs, want the live one", statusPayload["running"])
	}
	row, _ := running[0].(map[string]any)
	if row["run_id"] != liveRunID || row["status"] != "running" || row["playbook_name"] != "test-jobs" {
		t.Fatalf("running row = %v", row)
	}

	if stopPayload["status"] != "stopped" || stopPayload["run_id"] != liveRunID {
		t.Fatalf("stop payload = %v", stopPayload)
	}

	// The stop is the outcome of the run, not a failure.
	payload := playbookPayload(t, content)
	if payload["status"] != "stopped" {
		t.Fatalf("run payload = %v, want status=stopped", payload)
	}
	if _, failed := payload["error"]; failed {
		t.Fatalf("a stopped run reported an error: %v", payload)
	}
	if payload["run_id"] != liveRunID {
		t.Fatalf("run_id = %v, want %v", payload["run_id"], liveRunID)
	}

	// The run is recorded in the colony's history so nothing vanishes.
	runs, err := ListPlaybookRuns(filepath.Join(base.binding.Dir, "colony.db"), 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != liveRunID {
		t.Fatalf("history = %+v, want one stopped run", runs)
	}
	if runs[0].Error == nil || !strings.Contains(*runs[0].Error, "stopped by stop_playbook") {
		t.Fatalf("history error = %v", runs[0].Error)
	}

	// Nothing is left registered once the run returns.
	if rows := runningPlaybookRows(base.binding.Dir, time.Now()); len(rows) != 0 {
		t.Fatalf("registry still holds %v", rows)
	}
}

// Parity-exempt: process cleanup on stop is Go-specific (upstream cancels an
// asyncio task); this port must cancel the run, kill the interpreter, and
// record the stop like any other run.
func TestRunPlaybookPythonRunStopKillsInterpreter(t *testing.T) {
	requirePlaybookPython(t)
	base := playbookTestRuntime(t, 3)
	var (
		mu          sync.Mutex
		stopPayload map[string]any
		liveRunID   string
	)
	script := strings.Join([]string{
		`meta = {"name": "py-stop", "concurrency": 1}`,
		"",
		"async def run(args):",
		"    await converge(",
		`        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),`,
		`        dispatch=lambda row, i: worker(task=f"stop {row['id']}", data={"id": row["id"]}),`,
		"        max_rounds=1)",
		`    return {"remaining": tracker_count("SELECT id FROM jobs WHERE done=0")}`,
	}, "\n") + "\n"
	runtime := &stopHookRuntime{fakePlaybookRuntime: base}
	runtime.onSpawn = func(runID string) {
		mu.Lock()
		liveRunID = runID
		mu.Unlock()
		stopped, isError := RunPlaybook(context.Background(), runtime, map[string]any{
			"action": "stop", "run_id": runID,
		})
		if isError {
			t.Errorf("stop during a Python run failed: %s", stopped)
			return
		}
		mu.Lock()
		stopPayload = playbookPayload(t, stopped)
		mu.Unlock()
	}

	started := time.Now()
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": script})
	elapsed := time.Since(started)
	if isError {
		t.Fatalf("stopped Python run reported a tool error: %s", content)
	}
	mu.Lock()
	runID := liveRunID
	stop := stopPayload
	mu.Unlock()
	if runID == "" {
		t.Fatal("the Python run never reached the dispatch hook")
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "stopped" || payload["run_id"] != runID {
		t.Fatalf("run payload = %v, want status=stopped run_id=%s", payload, runID)
	}
	if stop == nil || stop["status"] != "stopped" {
		t.Fatalf("stop payload = %v", stop)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("cancellation took %v; the interpreter should die promptly", elapsed)
	}

	runs, err := ListPlaybookRuns(filepath.Join(base.binding.Dir, "colony.db"), 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != runID {
		t.Fatalf("history = %+v, want one stopped run", runs)
	}
	if runs[0].Error == nil || !strings.Contains(*runs[0].Error, "stopped by stop_playbook") {
		t.Fatalf("history error = %v", runs[0].Error)
	}
}

// Parity: core/framework/tools/playbook_tools.py::stop_playbooks_for_colony
// ("Scoped by colony identity so one session's stop never reaches another's runs")
func TestPlaybookStopIsScopedToTheColony(t *testing.T) {
	otherDir := t.TempDir()
	otherBinding, err := tracker.BindingForColonyDir(otherDir, "other-colony")
	if err != nil || otherBinding == nil {
		t.Fatalf("other binding: %v", err)
	}
	cancelled := false
	registerPlaybookRun(&playbookRunHandle{
		runID:     "pb_other_colony",
		name:      "other",
		colonyDir: otherBinding.Dir,
		started:   time.Now(),
		cancel:    func() { cancelled = true },
	})
	t.Cleanup(func() { unregisterPlaybookRun("pb_other_colony") })

	runtime := playbookTestRuntime(t, 0)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"action": "stop", "run_id": "pb_other_colony",
	})
	if isError {
		t.Fatalf("stop: %s", content)
	}
	if payload := playbookPayload(t, content); payload["status"] != "not_running" {
		t.Fatalf("stop reached another colony's run: %v", payload)
	}
	if cancelled {
		t.Fatal("another colony's run was cancelled")
	}
	// It is not even visible from this colony's status.
	status, _ := RunPlaybook(context.Background(), runtime, map[string]any{"action": "status"})
	if rows, _ := playbookPayload(t, status)["running"].([]any); len(rows) != 0 {
		t.Fatalf("status leaked another colony's run: %v", rows)
	}
}

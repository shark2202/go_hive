package colony

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/tracker"
)

type fakePlaybookRuntime struct {
	mu      sync.Mutex
	binding *tracker.Binding
	max     int

	spawned []BatchTask
	runIDs  []string
	// doneIDs limits which dispatched rows the fake marks done; nil means all.
	doneIDs map[int]bool
}

func (f *fakePlaybookRuntime) SpawnBatchWithOptions(_ context.Context, tasks []BatchTask, opts SpawnBatchOptions) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(tasks))
	for index, task := range tasks {
		f.spawned = append(f.spawned, task)
		f.runIDs = append(f.runIDs, opts.BatchID)
		ids = append(ids, fmt.Sprintf("worker_%d", len(f.spawned)))
		id := rowInt(task.Data["id"])
		mark := f.doneIDs == nil
		if f.doneIDs != nil && f.doneIDs[id] {
			mark = true
		}
		if mark && id > 0 {
			if _, err := tracker.ExecuteSQL(f.binding.TrackerDB,
				fmt.Sprintf("UPDATE jobs SET done=1 WHERE id=%d", id), 100); err != nil {
				return nil, err
			}
		}
		_ = index
	}
	return ids, nil
}

func (f *fakePlaybookRuntime) WaitForWorkerReports(_ context.Context, ids []string, _ time.Duration) ([]map[string]any, error) {
	reports := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		reports = append(reports, map[string]any{
			"worker_id": id,
			"status":    "success",
			"summary":   "ok",
			"data":      map[string]any{"worker_id": id},
		})
	}
	return reports, nil
}

func (f *fakePlaybookRuntime) GetWorker(string) *Worker { return nil }
func (f *fakePlaybookRuntime) MaxConcurrentWorkers() int {
	if f.max <= 0 {
		return 4
	}
	return f.max
}
func (f *fakePlaybookRuntime) TrackerBinding() *tracker.Binding { return f.binding }

func rowInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		var parsed int
		_, _ = fmt.Sscanf(fmt.Sprint(value), "%d", &parsed)
		return parsed
	}
}

func playbookTestRuntime(t *testing.T, rows int) *fakePlaybookRuntime {
	t.Helper()
	colonyDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(colonyDir, "playbooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	binding, err := tracker.BindingForColonyDir(colonyDir, "test-colony")
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	if binding == nil {
		t.Fatal("binding is nil")
	}
	if _, err := tracker.ExecuteSQL(binding.TrackerDB,
		"CREATE TABLE jobs (id INTEGER PRIMARY KEY, name TEXT, done INTEGER NOT NULL DEFAULT 0)", 100); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 1; i <= rows; i++ {
		if _, err := tracker.ExecuteSQL(binding.TrackerDB,
			fmt.Sprintf("INSERT INTO jobs (id, name, done) VALUES (%d, 'job-%d', 0)", i, i), 100); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	return &fakePlaybookRuntime{binding: binding, max: 4}
}

func playbookPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v (%q)", err, content)
	}
	return payload
}

func nativeSpecJSON() string {
	return `{
		"name": "test-jobs",
		"description": "one worker per pending job",
		"concurrency": 2,
		"pending_sql": "SELECT id, name FROM jobs WHERE done=0",
		"task": "Process job {name} (id {id})",
		"goal": "Processing {name}",
		"data_columns": ["id", "name"],
		"retries": 1,
		"backoff": "exp",
		"max_rounds": 1
	}`
}

// Parity-exempt: the tool schema is declared inline upstream; this asserts the
// four upstream parameter names survived the Go declaration.
func TestRunPlaybookToolDefParametersMatchUpstream(t *testing.T) {
	def := RunPlaybookToolDef()
	if def.Name != RunPlaybookToolName {
		t.Fatalf("name = %q", def.Name)
	}
	properties, _ := def.Parameters["properties"].(map[string]any)
	want := []string{"playbook", "playbook_name", "playbook_path", "args", "action", "run_id"}
	if len(properties) != len(want) {
		t.Fatalf("parameters = %v, want %v", properties, want)
	}
	for _, name := range want {
		if _, ok := properties[name]; !ok {
			t.Fatalf("parameter %q missing from %v", name, properties)
		}
	}
}

// Parity: tests/test_playbook_tools.py::test_playbook_tools_are_colony_queen_only
func TestRunPlaybookRejectsWithoutColony(t *testing.T) {
	content, isError := RunPlaybook(context.Background(), nil, map[string]any{"playbook_name": "anything"})
	if !isError {
		t.Fatalf("run_playbook without a colony reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	if !strings.Contains(payload["error"].(string), "no colony is bound") {
		t.Fatalf("error = %q", payload["error"])
	}
}

// Parity-exempt: upstream executes Python natively, so it has no
// "Python is unsupported in the Go build" result to test against.
func TestRunPlaybookPythonScriptIsUnsupported(t *testing.T) {
	runtime := playbookTestRuntime(t, 2)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": "meta = {\"name\": \"legacy\"}\n\nasync def run(args):\n    return {}\n",
	})
	if !isError {
		t.Fatalf("Python playbook reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "unsupported" {
		t.Fatalf("status = %v, want unsupported", payload["status"])
	}
	if payload["dispatched"] != float64(0) {
		t.Fatalf("dispatched = %v, want 0", payload["dispatched"])
	}
	if !strings.Contains(payload["error"].(string), "cannot execute Python") {
		t.Fatalf("error = %q", payload["error"])
	}
	if len(runtime.spawned) != 0 {
		t.Fatalf("unsupported playbook spawned %d workers", len(runtime.spawned))
	}
}

// Parity: tests/test_playbook_runner.py::test_converges_all_rows
func TestRunPlaybookNativeConvergesAndRecordsRun(t *testing.T) {
	runtime := playbookTestRuntime(t, 3)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": nativeSpecJSON()})
	if isError {
		t.Fatalf("native playbook failed: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "completed" {
		t.Fatalf("status = %v, want completed", payload["status"])
	}
	if payload["dispatched"] != float64(3) {
		t.Fatalf("dispatched = %v, want 3", payload["dispatched"])
	}
	if len(runtime.spawned) != 3 {
		t.Fatalf("runtime spawned %d workers, want 3", len(runtime.spawned))
	}
	seenTasks := map[string]bool{}
	for _, task := range runtime.spawned {
		seenTasks[task.Task] = true
		if task.Data["name"] == nil {
			t.Fatalf("worker data = %#v", task.Data)
		}
	}
	for _, want := range []string{"Process job job-1 (id 1)", "Process job job-2 (id 2)", "Process job job-3 (id 3)"} {
		if !seenTasks[want] {
			t.Fatalf("task template %q missing from %v", want, seenTasks)
		}
	}
	result, _ := payload["result"].(map[string]any)
	if result["remaining"] != float64(0) {
		t.Fatalf("remaining = %v, want 0", result["remaining"])
	}
	res, err := tracker.ExecuteSQL(runtime.binding.TrackerDB, "SELECT COUNT(*) AS n FROM jobs WHERE done=1", 100)
	if err != nil {
		t.Fatalf("query done rows: %v", err)
	}
	rowsRes := res["rows"].([]any)
	values := rowsRes[0].([]any)
	if fmt.Sprint(values[0]) != "3" {
		t.Fatalf("done rows = %v, want 3", values[0])
	}

	runs, err := ListPlaybookRuns(filepath.Join(runtime.binding.Dir, "colony.db"), 10)
	if err != nil {
		t.Fatalf("list run log: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "done" || runs[0].Dispatched != 3 {
		t.Fatalf("run log = %#v", runs)
	}
	runID, _ := payload["run_id"].(string)
	if runs[0].RunID != runID {
		t.Fatalf("run log id = %q, want %q", runs[0].RunID, runID)
	}
	if _, err := os.Stat(filepath.Join(runtime.binding.Dir, "playbooks", "test-jobs.play.json")); err != nil {
		t.Fatalf("inline native playbook was not saved: %v", err)
	}
}

// Parity: tests/test_playbook_runner.py::test_rerun_is_resume
func TestRunPlaybookNativeResumesFromTracker(t *testing.T) {
	runtime := playbookTestRuntime(t, 3)
	// First run only completes rows 1 and 2; row 3 is dead-lettered after
	// max_rounds=1.
	runtime.doneIDs = map[int]bool{1: true, 2: true}
	first, firstErr := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": nativeSpecJSON()})
	if firstErr {
		t.Fatalf("first run failed: %s", first)
	}
	firstPayload := playbookPayload(t, first)
	if firstPayload["dead_lettered"] != float64(1) {
		t.Fatalf("first dead_lettered = %v, want 1", firstPayload["dead_lettered"])
	}

	// Re-running by name re-queries the tracker: the two done rows drop out,
	// so exactly one worker is dispatched.
	secondRuntime := &fakePlaybookRuntime{binding: runtime.binding, max: 4}
	second, secondErr := RunPlaybook(context.Background(), secondRuntime, map[string]any{"playbook_name": "test-jobs"})
	if secondErr {
		t.Fatalf("resume run failed: %s", second)
	}
	secondPayload := playbookPayload(t, second)
	if secondPayload["dispatched"] != float64(1) {
		t.Fatalf("resume dispatched = %v, want 1", secondPayload["dispatched"])
	}
	result, _ := secondPayload["result"].(map[string]any)
	if result["remaining"] != float64(0) {
		t.Fatalf("resume remaining = %v, want 0", result["remaining"])
	}
	if len(secondRuntime.spawned) != 1 {
		t.Fatalf("resume spawned %d workers, want 1", len(secondRuntime.spawned))
	}
}

// Parity-exempt: the Go build's saved-playbook lookup also accepts a Python
// file so a Python-era library reports unsupported instead of "not found".
func TestRunPlaybookSavedPythonNameIsUnsupported(t *testing.T) {
	runtime := playbookTestRuntime(t, 1)
	pythonPath := filepath.Join(runtime.binding.Dir, "playbooks", "legacy.play.py")
	if err := os.WriteFile(pythonPath, []byte("meta = {'name': 'legacy'}\nasync def run(args):\n    return {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook_name": "legacy"})
	if !isError {
		t.Fatalf("saved Python playbook reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "unsupported" {
		t.Fatalf("status = %v, want unsupported", payload["status"])
	}
	if payload["playbook_path"] != pythonPath {
		t.Fatalf("playbook_path = %v, want %s", payload["playbook_path"], pythonPath)
	}
	if len(runtime.spawned) != 0 {
		t.Fatal("saved Python playbook dispatched workers")
	}
}

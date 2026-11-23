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

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/playbook"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
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
	f.mu.Lock()
	defer f.mu.Unlock()
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

// Parity: core/framework/tools/playbook_tools.py::register_playbook_tools
func TestRunPlaybookToolDefParametersMatchUpstream(t *testing.T) {
	def := RunPlaybookToolDef()
	if def.Name != RunPlaybookToolName {
		t.Fatalf("name = %q", def.Name)
	}
	properties, _ := def.Parameters["properties"].(map[string]any)
	want := []string{"playbook", "playbook_name", "playbook_path", "args"}
	if len(properties) != len(want) {
		t.Fatalf("parameters = %v, want %v", properties, want)
	}
	for _, name := range want {
		if _, ok := properties[name]; !ok {
			t.Fatalf("parameter %q missing from %v", name, properties)
		}
	}
	if required, present := def.Parameters["required"]; !present {
		t.Fatal("run_playbook must publish the request-layer `required` list — the Python request layer " +
			"(_tool_to_openai_format) always materializes it, even as []")
	} else if list, ok := required.([]any); !ok || len(list) != 0 {
		t.Fatalf("run_playbook required = %#v, want []", required)
	}

	// The three standalone control tools carry the run_id contract the folded
	// parameters used to document on run_playbook.
	control := map[string]llm.ToolDef{}
	for _, controlDef := range PlaybookControlToolDefs() {
		control[controlDef.Name] = controlDef
	}
	for _, name := range []string{GetPlaybookStatusToolName, StopPlaybookToolName} {
		controlDef, ok := control[name]
		if !ok {
			t.Fatalf("%s missing from the control tool defs", name)
		}
		required, _ := controlDef.Parameters["required"].([]string)
		if len(required) != 1 || required[0] != "run_id" {
			t.Fatalf("%s required = %#v, want [run_id]", name, required)
		}
	}
	listDef, ok := control[ListPlaybookRunsToolName]
	if !ok {
		t.Fatalf("%s missing from the control tool defs", ListPlaybookRunsToolName)
	}
	listProps, _ := listDef.Parameters["properties"].(map[string]any)
	if len(listProps) != 2 {
		t.Fatalf("%s properties = %v, want run_id + limit", ListPlaybookRunsToolName, listProps)
	}
	if required, present := listDef.Parameters["required"]; !present {
		t.Fatalf("%s must publish the request-layer `required` list ([] — the Python Tool has none)", ListPlaybookRunsToolName)
	} else if list, ok := required.([]any); !ok || len(list) != 0 {
		t.Fatalf("%s required = %#v, want []", ListPlaybookRunsToolName, required)
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

// requirePlaybookPython skips (rather than fails) on hosts without an
// interpreter; the recorded evidence runs against the machine's python3.
func requirePlaybookPython(t *testing.T) {
	t.Helper()
	if _, err := playbook.ResolvePythonInterpreter(); err != nil {
		t.Skipf("Python interpreter unavailable: %v", err)
	}
}

// pythonJobsScript is the inline `.play.py` converge loop over the jobs table
// the fake runtime provisions (one worker per undone row, goal per worker).
func pythonJobsScript(name string) string {
	return strings.Join([]string{
		`meta = {"name": "` + name + `", "description": "one worker per pending job", "concurrency": 2}`,
		"",
		"async def run(args):",
		"    await converge(",
		`        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),`,
		"        dispatch=lambda row, i: worker(",
		`            task=f"Process job {row['name']} (id {row['id']})",`,
		`            data={"id": row["id"], "name": row["name"]},`,
		`            goal=f"Processing {row['name']}"),`,
		"        max_rounds=1)",
		`    remaining = tracker_count("SELECT id FROM jobs WHERE done=0")`,
		`    log(f"remaining={remaining}")`,
		`    return {"remaining": remaining}`,
	}, "\n") + "\n"
}

// Parity: core/tests/test_playbook_runner.py::test_converges_all_rows
func TestRunPlaybookPythonScriptConverges(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 2)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": pythonJobsScript("py-jobs"),
	})
	if isError {
		t.Fatalf("Python playbook failed: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "completed" {
		t.Fatalf("status = %v, want completed", payload["status"])
	}
	if payload["dispatched"] != float64(2) {
		t.Fatalf("dispatched = %v, want 2", payload["dispatched"])
	}
	if payload["dead_lettered"] != float64(0) {
		t.Fatalf("dead_lettered = %v, want 0", payload["dead_lettered"])
	}
	if payload["concurrency"] != float64(2) {
		t.Fatalf("concurrency = %v, want meta's 2", payload["concurrency"])
	}
	if payload["playbook_name"] != "py-jobs" {
		t.Fatalf("playbook_name = %v, want py-jobs", payload["playbook_name"])
	}
	if len(runtime.spawned) != 2 {
		t.Fatalf("runtime spawned %d workers, want 2", len(runtime.spawned))
	}
	seenTasks := map[string]bool{}
	for _, task := range runtime.spawned {
		seenTasks[task.Task] = true
		if task.Data["name"] == nil {
			t.Fatalf("worker data = %#v", task.Data)
		}
	}
	for _, want := range []string{"Process job job-1 (id 1)", "Process job job-2 (id 2)"} {
		if !seenTasks[want] {
			t.Fatalf("task %q missing from %v", want, seenTasks)
		}
	}
	result, _ := payload["result"].(map[string]any)
	if result["remaining"] != float64(0) {
		t.Fatalf("result = %#v, want remaining 0", result)
	}
	// Inline Python is saved to the colony library as .play.py (upstream shape),
	// so a re-run by name resumes without re-sending the script.
	savedPath := filepath.Join(runtime.binding.Dir, "playbooks", "py-jobs.play.py")
	if _, err := os.Stat(savedPath); err != nil {
		t.Fatalf("inline Python playbook was not saved to %s: %v", savedPath, err)
	}
	if payload["playbook_path"] != savedPath {
		t.Fatalf("playbook_path = %v, want %s", payload["playbook_path"], savedPath)
	}
	runs, err := ListPlaybookRuns(filepath.Join(runtime.binding.Dir, "colony.db"), 10)
	if err != nil {
		t.Fatalf("list run log: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "done" || runs[0].Dispatched != 2 {
		t.Fatalf("run log = %#v", runs)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_rerun_is_resume
func TestRunPlaybookSavedPythonNameResumes(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 2)
	pythonPath := filepath.Join(runtime.binding.Dir, "playbooks", "legacy.play.py")
	if err := os.WriteFile(pythonPath, []byte(pythonJobsScript("legacy")), 0o644); err != nil {
		t.Fatal(err)
	}
	first, firstErr := RunPlaybook(context.Background(), runtime, map[string]any{"playbook_name": "legacy"})
	if firstErr {
		t.Fatalf("saved Python playbook failed: %s", first)
	}
	firstPayload := playbookPayload(t, first)
	if firstPayload["dispatched"] != float64(2) {
		t.Fatalf("first dispatched = %v, want 2", firstPayload["dispatched"])
	}
	if firstPayload["playbook_path"] != pythonPath {
		t.Fatalf("playbook_path = %v, want %s", firstPayload["playbook_path"], pythonPath)
	}

	// Re-running by name re-queries the tracker: done rows drop out, so no
	// worker is dispatched (resume by construction).
	second, secondErr := RunPlaybook(context.Background(), runtime, map[string]any{"playbook_name": "legacy"})
	if secondErr {
		t.Fatalf("resume run failed: %s", second)
	}
	secondPayload := playbookPayload(t, second)
	if secondPayload["dispatched"] != float64(0) {
		t.Fatalf("resume dispatched = %v, want 0", secondPayload["dispatched"])
	}
	if len(runtime.spawned) != 2 {
		t.Fatalf("runtime spawned %d workers total, want 2", len(runtime.spawned))
	}
}

// Parity: core/tests/test_playbook_runner.py::test_bad_script_missing_meta
func TestRunPlaybookPythonLoadErrorDoesNotDispatch(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 1)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": "async def run(args):\n    return {}\n",
	})
	if !isError {
		t.Fatalf("Python playbook without meta reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "failed" {
		t.Fatalf("status = %v, want failed", payload["status"])
	}
	if runID, _ := payload["run_id"].(string); runID == "" {
		t.Fatalf("run_id missing from %v", payload)
	}
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "playbook did not start") || !strings.Contains(message, "meta") {
		t.Fatalf("error = %q, want the load failure", message)
	}
	if len(runtime.spawned) != 0 {
		t.Fatalf("load failure spawned %d workers", len(runtime.spawned))
	}
}

// Parity-exempt: upstream executes Python in-process, so it has no
// external-interpreter lookup to fail; this port must report `unsupported`
// (with the reason) instead of faking a run.
func TestRunPlaybookPythonWithoutInterpreterIsUnsupported(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("HIVE_PYTHON", "")
	runtime := playbookTestRuntime(t, 1)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": "meta = {\"name\": \"x\"}\nasync def run(args):\n    return {}\n",
	})
	if !isError {
		t.Fatalf("Python playbook without an interpreter reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "unsupported" {
		t.Fatalf("status = %v, want unsupported", payload["status"])
	}
	if payload["dispatched"] != float64(0) {
		t.Fatalf("dispatched = %v, want 0", payload["dispatched"])
	}
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "Python") || !strings.Contains(message, "HIVE_PYTHON") {
		t.Fatalf("error = %q, want the interpreter lookup detail", message)
	}
	if len(runtime.spawned) != 0 {
		t.Fatal("missing-interpreter run dispatched workers")
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

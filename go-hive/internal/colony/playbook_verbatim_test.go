package colony

// Field-level verbatim checks for the `.play.py` path, plus the lossless
// migration contract for playbooks written by earlier Go releases
// (`.play.json`) and by Python Hive (`playbooks/<name>.play.py`).
//
// The reference is core/framework/tools/playbook_tools.py; every expected
// literal here is copied from that source. The cross-language harness in
// go_hive/scripts/playbook_verbatim_compare.py drives the REAL upstream tool
// and the REAL Go tool over one fixture and diffs the payloads field by field;
// these tests keep the shape pinned without needing Python for every run.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tracker"
)

const upstreamZeroDispatchHint = "dispatched 0 workers. If the table is already converged this is correct. " +
	"If you expected work: (1) `pending` must SELECT the undone ROWS (e.g. " +
	"`SELECT id, url FROM t WHERE done_at IS NULL`), NOT `SELECT COUNT(*)` " +
	"(which returns one row); (2) dispatch via `await converge(pending=..., " +
	"dispatch=lambda row, i: worker(...))` — worker() is async and converge " +
	"awaits it; calling worker() in a bare loop without await runs nothing; " +
	"(3) tracker_query returns a list of row dicts — index a row's column " +
	"(row['url']), not the list."

func payloadKeySet(payload map[string]any) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func requireKeys(t *testing.T, payload map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := payloadKeySet(payload)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("payload keys = %v, want %v\npayload: %#v", got, want, payload)
	}
}

// Parity: framework/tools/playbook_tools.py::run_playbook (fast-path result)
func TestPlaybookVerbatimCompletedPayloadFields(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 2)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": pythonJobsScript("verbatim-completed"),
	})
	if isError {
		t.Fatalf("Python playbook failed: %s", content)
	}
	payload := playbookPayload(t, content)
	requireKeys(t, payload, "status", "run_id", "concurrency", "dispatched", "dead_lettered",
		"result", "log_db", "playbook_name", "playbook_path")
	if payload["status"] != "completed" {
		t.Fatalf("status = %v", payload["status"])
	}
	if payload["playbook_name"] != "verbatim-completed" {
		t.Fatalf("playbook_name = %v", payload["playbook_name"])
	}
	if payload["log_db"] != filepath.Join(runtime.binding.Dir, "colony.db") {
		t.Fatalf("log_db = %v", payload["log_db"])
	}
}

// Parity: framework/tools/playbook_tools.py::_zero_dispatch_hint
func TestPlaybookVerbatimZeroDispatchHintMatchesUpstream(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 0)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": pythonJobsScript("verbatim-converged"),
	})
	if isError {
		t.Fatalf("Python playbook failed: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["dispatched"] != float64(0) {
		t.Fatalf("dispatched = %v", payload["dispatched"])
	}
	if payload["hint"] != upstreamZeroDispatchHint {
		t.Fatalf("hint =\n%q\nwant\n%q", payload["hint"], upstreamZeroDispatchHint)
	}
}

// Parity: framework/tools/playbook_tools.py::run_playbook (load failure)
func TestPlaybookVerbatimLoadFailurePayloadFields(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 1)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": "async def run(args):\n    return {}\n",
	})
	if !isError {
		t.Fatalf("playbook without meta reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	requireKeys(t, payload, "status", "run_id", "error", "traceback")
	want := "playbook did not start — PlaybookScriptError: playbook must define a `meta` dict with at least a 'name'"
	if payload["error"] != want {
		t.Fatalf("error = %q, want %q", payload["error"], want)
	}
	if traceback, _ := payload["traceback"].(string); !strings.Contains(traceback, "PlaybookScriptError") {
		t.Fatalf("traceback = %q", payload["traceback"])
	}
}

// Parity: framework/tools/playbook_tools.py::run_playbook (concurrency reject)
func TestPlaybookVerbatimConcurrencyFailurePayloadMatchesUpstream(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 1)
	script := "meta = {\"name\": \"too-wide\", \"concurrency\": 999}\nasync def run(args):\n    return {}\n"
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": script})
	if !isError {
		t.Fatalf("concurrency over the ceiling reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	requireKeys(t, payload, "status", "run_id", "error")
	want := "meta['concurrency']=999 exceeds the maximum 32. Lower it, split the batch, or raise HIVE_MAX_PLAYBOOK_CONCURRENCY."
	if payload["error"] != want {
		t.Fatalf("error = %q, want %q", payload["error"], want)
	}
	// Upstream persists the inline script BEFORE rejecting the concurrency:
	// the library write is not rolled back by a bad meta.
	saved := filepath.Join(runtime.binding.Dir, "playbooks", "too-wide.play.py")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("invalid-concurrency inline script was not persisted at %s: %v", saved, err)
	}
}

// Parity: framework/tools/playbook_tools.py::run_playbook (async acknowledgement)
func TestPlaybookVerbatimStartedPayloadAndMessage(t *testing.T) {
	requirePlaybookPython(t)
	t.Setenv("HIVE_PLAYBOOK_FAST_GRACE", "0")
	runtime := playbookTestRuntime(t, 2)
	name := "verbatim-started"
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": sleepingPythonPlaybook(name),
	})
	if isError {
		t.Fatalf("started payload reported an error: %s", content)
	}
	payload := playbookPayload(t, content)
	requireKeys(t, payload, "status", "run_id", "concurrency", "log_db", "playbook_name", "playbook_path", "message")
	if payload["status"] != "started" {
		t.Fatalf("status = %v", payload["status"])
	}
	runID := payload["run_id"].(string)
	saved := filepath.Join(runtime.binding.Dir, "playbooks", name+".play.py")
	want := "Playbook is dispatching workers in the background, 2 at a time (your meta['concurrency']). " +
		"Routine outcomes (retry, dead-letter) are handled by the convergence loop; you'll receive a " +
		"[PLAYBOOK_COMPLETE] notification when it finishes (status=done or error). Check progress " +
		"anytime with get_playbook_status(run_id='" + runID + "') or kill it with stop_playbook(run_id='" + runID + "'). " +
		"Saved to the colony library as '" + name + "' — TO RESUME (after a stop, or after editing " + saved + "), call " +
		"run_playbook(playbook_name='" + name + "'); done rows are skipped."
	if payload["message"] != want {
		t.Fatalf("message =\n%q\nwant\n%q", payload["message"], want)
	}
	waitForPlaybookRun(t, filepath.Join(runtime.binding.Dir, "colony.db"), 15*time.Second)
}

// Parity-exempt literal check of the run-log row: upstream _record_run passes
// name=None on the error path; the port must do the same so a reader sees one
// row shape across implementations.
// Parity: core/framework/tools/playbook_tools.py::_record_run
func TestPlaybookRunLogErrorRowHasNullName(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 1)
	script := "meta = {\"name\": \"boom\", \"concurrency\": 1}\nasync def run(args):\n    raise KeyError(0)\n"
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": script})
	if !isError {
		t.Fatalf("raising playbook reported success: %s", content)
	}
	payload := playbookPayload(t, content)
	requireKeys(t, payload, "status", "run_id", "error", "traceback", "log_db")
	runs, err := ListPlaybookRuns(filepath.Join(runtime.binding.Dir, "colony.db"), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("run log = %#v err=%v", runs, err)
	}
	if runs[0].Status != "error" || runs[0].Name != nil {
		t.Fatalf("run log row = %#v, want status=error name=NULL", runs[0])
	}
	if !strings.HasPrefix(runs[0].CreatedAt, time.Now().UTC().Format("2006-01-02T15:04:05")) {
		t.Fatalf("created_at = %q, want a Python isoformat UTC stamp", runs[0].CreatedAt)
	}
	if strings.Contains(runs[0].CreatedAt, "Z") || !strings.HasSuffix(runs[0].CreatedAt, "+00:00") {
		t.Fatalf("created_at = %q, want the upstream +00:00 offset", runs[0].CreatedAt)
	}
}

// ---- migration -----------------------------------------------------------

func seedMigrationRuntime(t *testing.T, rows int) *fakePlaybookRuntime {
	t.Helper()
	runtime := playbookTestRuntime(t, rows)
	copyFile(t, filepath.Join("testdata", "playbook-migration", "legacy-jobs.play.json"),
		filepath.Join(runtime.binding.Dir, "playbooks", "legacy-jobs.play.json"))
	copyFile(t, filepath.Join("testdata", "playbook-migration", "upstream-lanes.play.py"),
		filepath.Join(runtime.binding.Dir, "playbooks", "upstream-lanes.play.py"))
	return runtime
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// A legacy .play.json written by an older Go release runs by name unchanged:
// same file, same format, no rewrite, no migration step.
// Parity-exempt: Go 历史 .play.json 迁移回归（Python 无此格式）
func TestPlaybookMigrationLegacyNativeJsonRunsUnchanged(t *testing.T) {
	runtime := seedMigrationRuntime(t, 3)
	path := filepath.Join(runtime.binding.Dir, "playbooks", "legacy-jobs.play.json")
	before := fileDigest(t, path)

	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook_name": "legacy-jobs"})
	if isError {
		t.Fatalf("legacy .play.json failed: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "completed" || payload["dispatched"] != float64(3) {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["playbook_path"] != path {
		t.Fatalf("playbook_path = %v, want %s", payload["playbook_path"], path)
	}
	if after := fileDigest(t, path); after != before {
		t.Fatalf("legacy playbook was rewritten by the run")
	}
	for _, worker := range runtime.spawned {
		if worker.Task == "" || worker.Data["name"] == nil {
			t.Fatalf("spawned task lost its template/data: %#v", worker)
		}
	}
}

// An upstream playbooks/<name>.play.py runs by name unchanged: same bytes,
// same path, no .play.json conversion, and the upstream-only hooks
// (lane/deadletter) still work.
// Parity-exempt: 上游 .play.py 原样加载回归（Python 侧无对应迁移用例）
func TestPlaybookMigrationUpstreamPlayPyRunsUnchanged(t *testing.T) {
	requirePlaybookPython(t)
	runtime := seedMigrationRuntime(t, 2)
	path := filepath.Join(runtime.binding.Dir, "playbooks", "upstream-lanes.play.py")
	before := fileDigest(t, path)
	// Only row 1 converges; row 2 stays pending and is dead-lettered.
	runtime.doneIDs = map[int]bool{1: true}

	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook_name": "upstream-lanes"})
	if isError {
		t.Fatalf("upstream .play.py failed: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["playbook_path"] != path {
		t.Fatalf("playbook_path = %v, want %s", payload["playbook_path"], path)
	}
	if payload["playbook_name"] != "upstream-lanes" {
		t.Fatalf("playbook_name = %v", payload["playbook_name"])
	}
	if payload["dead_lettered"] != float64(1) {
		t.Fatalf("dead_lettered = %v, want 1", payload["dead_lettered"])
	}
	if after := fileDigest(t, path); after != before {
		t.Fatalf("upstream playbook was rewritten by the run")
	}
	if _, err := os.Stat(filepath.Join(runtime.binding.Dir, "playbooks", "upstream-lanes.play.json")); !os.IsNotExist(err) {
		t.Fatalf("running a .play.py created a .play.json sibling (err=%v)", err)
	}
}

// Both library formats coexist: the by-name resolver picks the legacy native
// file deterministically, and the upstream file stays runnable by path.
// Parity-exempt: 两种旧格式共存解析顺序（Python 无同名两种格式场景）
func TestPlaybookMigrationBothFormatsResolveDeterministically(t *testing.T) {
	requirePlaybookPython(t)
	runtime := seedMigrationRuntime(t, 1)
	// Same name in both formats: the .play.json (legacy) wins by name until
	// the user removes it; the .play.py remains addressable by path.
	pythonCopy := filepath.Join(runtime.binding.Dir, "playbooks", "upstream-lanes.play.json")
	if err := os.WriteFile(pythonCopy, []byte(nativeSpecJSON()), 0o644); err != nil {
		t.Fatal(err)
	}
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook_name": "upstream-lanes"})
	if isError {
		t.Fatalf("mixed library run failed: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["playbook_path"] != pythonCopy {
		t.Fatalf("playbook_path = %v, want the .play.json precedence %s", payload["playbook_path"], pythonCopy)
	}
	byPath, pathErr := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook_path": filepath.Join(runtime.binding.Dir, "playbooks", "upstream-lanes.play.py"),
	})
	if pathErr {
		t.Fatalf("playbook_path run failed: %s", byPath)
	}
	if got := playbookPayload(t, byPath)["playbook_name"]; got != "upstream-lanes" {
		t.Fatalf("playbook_name = %v", got)
	}
}

// Re-running an inline native playbook still writes the SAME name and format
// it always has (.play.json) — the Python era did not change legacy writes.
// Parity-exempt: Go 原生格式写回不变回归（Python 不写 .play.json）
func TestPlaybookMigrationNativeWritesKeepFormat(t *testing.T) {
	runtime := playbookTestRuntime(t, 1)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": nativeSpecJSON()})
	if isError {
		t.Fatalf("native playbook failed: %s", content)
	}
	payload := playbookPayload(t, content)
	want := filepath.Join(runtime.binding.Dir, "playbooks", "test-jobs.play.json")
	if payload["playbook_path"] != want {
		t.Fatalf("playbook_path = %v, want %s", payload["playbook_path"], want)
	}
	if content := fileDigest(t, want); content != nativeSpecJSON() {
		t.Fatalf("saved native playbook bytes changed:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(runtime.binding.Dir, "playbooks", "test-jobs.play.py")); !os.IsNotExist(err) {
		t.Fatalf("native write produced a .play.py sibling")
	}
}

// ---- concurrency cap + spawn spec ---------------------------------------

// recordingCapRuntime records the playbook's temporary colony-cap move.
type recordingCapRuntime struct {
	*fakePlaybookRuntime
	mu     sync.Mutex
	values []int
}

func (r *recordingCapRuntime) SetMaxConcurrentWorkers(capacity int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, capacity)
	return 0
}

func (r *recordingCapRuntime) capValues() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.values...)
}

// Upstream: colony cap = max(original, meta['concurrency']) for the run, then
// restored. A meta concurrency at/below the colony cap moves nothing.
// Parity: core/framework/tools/playbook_tools.py::register_playbook_tools
func TestPlaybookVerbatimConcurrencyCapRaiseAndRestore(t *testing.T) {
	requirePlaybookPython(t)
	base := playbookTestRuntime(t, 1)
	base.max = 1
	runtime := &recordingCapRuntime{fakePlaybookRuntime: base}
	script := "meta = {\"name\": \"wide\", \"concurrency\": 3}\nasync def run(args):\n    return {}\n"
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": script})
	if isError {
		t.Fatalf("playbook failed: %s", content)
	}
	if got := runtime.capValues(); len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Fatalf("cap moves = %v, want [3 1] (raise then restore)", got)
	}

	// concurrency <= cap: upstream still writes max(cap, concurrency) — a
	// no-op assignment — and restores the original afterwards.
	base2 := playbookTestRuntime(t, 1)
	base2.max = 8
	runtime2 := &recordingCapRuntime{fakePlaybookRuntime: base2}
	if _, isError := RunPlaybook(context.Background(), runtime2, map[string]any{"playbook": script}); isError {
		t.Fatal("playbook failed")
	}
	if got := runtime2.capValues(); len(got) != 2 || got[0] != 8 || got[1] != 8 {
		t.Fatalf("cap moves = %v, want [8 8] (no-op assign + restore)", got)
	}
}

// Upstream spawns the worker with `report_schema`; the port must carry the
// playbook's schema into the spawn spec.
// Parity: core/framework/tools/playbook_tools.py::register_playbook_tools
func TestPlaybookVerbatimSchemaRidesSpawnSpec(t *testing.T) {
	requirePlaybookPython(t)
	runtime := playbookTestRuntime(t, 1)
	script := `meta = {"name": "schema-job", "concurrency": 1}

RECEIPT = {"type": "object", "required": ["marker"], "properties": {"marker": {"type": "string"}}}

async def run(args):
    await worker(
        task="Report the marker.",
        data={"id": 1},
        schema=RECEIPT,
        timeout=60,
    )
    return {"ok": True}
`
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": script})
	if isError {
		t.Fatalf("schema playbook failed: %s", content)
	}
	if len(runtime.spawned) != 1 {
		t.Fatalf("spawned %d workers", len(runtime.spawned))
	}
	schema := runtime.spawned[0].ReportSchema
	if schema == nil || schema["type"] != "object" {
		t.Fatalf("spawn spec report_schema = %#v", schema)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "marker" {
		t.Fatalf("report_schema.required = %#v", schema["required"])
	}
}

// The admission gate itself is resizable while workers wait (upstream's
// scheduler mutation in Go form).
// Parity-exempt: Go 内部可调容量 gate（上游以替换调度器实现同一语义）
func TestWorkerSlotGateRaiseWakesWaiters(t *testing.T) {
	gate := newWorkerSlotGate(1)
	if !gate.acquire(context.Background()) {
		t.Fatal("first acquire failed")
	}
	admitted := make(chan struct{})
	go func() {
		if gate.acquire(context.Background()) {
			close(admitted)
		}
	}()
	select {
	case <-admitted:
		t.Fatal("second acquire passed a cap of 1")
	case <-time.After(50 * time.Millisecond):
	}
	gate.setCapacity(2)
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("raise did not wake the waiter")
	}
	gate.release()
	gate.release()
}

// The playbook schema must specialize the worker's report_to_parent tool AND
// its prompt (upstream: report_schema rides the spawn spec to the worker).
// Parity: core/framework/tools/playbook_tools.py::register_playbook_tools
func TestPlaybookSchemaSpecializesWorkerSurface(t *testing.T) {
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "schema_surface",
	})
	schema := map[string]any{
		"type":       "object",
		"required":   []any{"marker"},
		"properties": map[string]any{"marker": map[string]any{"type": "string"}},
	}
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it", ReportSchema: schema})
	if worker.Context == nil || worker.Context.AgentSpec.ReportSchema == nil {
		t.Fatalf("worker context did not inherit the dispatch schema: %#v", worker.Context)
	}
	tools := runtime.workerTools(worker)
	var reportTool map[string]any
	for _, def := range tools {
		if def.Name == "report_to_parent" {
			reportTool = def.Parameters
		}
	}
	if reportTool == nil {
		t.Fatalf("report_to_parent tool missing from %v", tools)
	}
	properties, _ := reportTool["properties"].(map[string]any)
	data, _ := properties["data"].(map[string]any)
	if data == nil || data["type"] != "object" {
		t.Fatalf("report_to_parent data param = %#v, want the playbook schema", data)
	}
	required, _ := reportTool["required"].([]string)
	if len(required) != 3 || required[2] != "data" {
		t.Fatalf("report_to_parent required = %#v, want data required under a schema", reportTool["required"])
	}
	if worker.ReportSchema["type"] != "object" {
		t.Fatalf("worker.ReportSchema = %#v", worker.ReportSchema)
	}
}

// ---- real-runtime cap raise ---------------------------------------------

// barrierLLM releases every in-flight call once two worker turns overlap. A
// fixed-cap colony (cap 1) could never reach that overlap, so the test proves
// the playbook's temporary raise is what admitted the second worker.
type barrierLLM struct {
	mu       sync.Mutex
	inFlight int
	max      int
	two      chan struct{}
	once     sync.Once
}

func newBarrierLLM() *barrierLLM {
	return &barrierLLM{two: make(chan struct{})}
}

func (b *barrierLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	b.mu.Lock()
	b.inFlight++
	if b.inFlight > b.max {
		b.max = b.inFlight
	}
	if b.inFlight >= 2 {
		b.once.Do(func() { close(b.two) })
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()
	select {
	case <-b.two:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
	}
	arguments, _ := json.Marshal(map[string]any{
		"status":  "success",
		"summary": "done",
		"data":    map[string]any{"ok": true},
	})
	return llm.Completion{
		ToolCalls:  []llm.ToolCall{{ID: "report_1", Name: "report_to_parent", Arguments: string(arguments)}},
		StopReason: "tool_calls",
	}, nil
}

func (b *barrierLLM) maxInFlight() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

// Parity: core/framework/tools/playbook_tools.py::register_playbook_tools
func TestPlaybookCapRaiseAdmitsDeclaredConcurrency(t *testing.T) {
	requirePlaybookPython(t)
	colonyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), []byte(`{"name":"cap_raise","queen_name":"queen_growth"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := newBarrierLLM()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:            testAgentSpec(),
		StoragePath:          colonyDir,
		LLM:                  provider,
		WorkerLLM:            provider,
		StreamID:             "cap_raise",
		MaxConcurrentWorkers: 1,
		Tools:                []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	if _, err := tracker.ExecuteSQL(runtime.TrackerBinding().TrackerDB,
		"CREATE TABLE jobs (id INTEGER PRIMARY KEY, name TEXT, done INTEGER NOT NULL DEFAULT 0)", 100); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 1; i <= 2; i++ {
		insert := "INSERT INTO jobs (id, name, done) VALUES (" +
			strconv.Itoa(i) + ", 'job-" + strconv.Itoa(i) + "', 0)"
		if _, err := tracker.ExecuteSQL(runtime.TrackerBinding().TrackerDB, insert, 100); err != nil {
			t.Fatal(err)
		}
	}
	script := `meta = {"name": "cap-raise", "concurrency": 2}

async def run(args):
    await converge(
        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),
        dispatch=lambda row, i: worker(task=f"Process {row['name']}", data={"id": row["id"]}, timeout=60),
        max_rounds=1)
    return {"remaining": tracker_count("SELECT id FROM jobs WHERE done=0")}
`
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{"playbook": script})
	if isError {
		t.Fatalf("cap playbook failed: %s", content)
	}
	if got := provider.maxInFlight(); got < 2 {
		t.Fatalf("max concurrent LLM turns = %d, want >= 2 (cap raise did not admit meta concurrency)", got)
	}
	if got := runtime.MaxConcurrentWorkers(); got != 1 {
		t.Fatalf("colony cap after the run = %d, want the original 1", got)
	}
}

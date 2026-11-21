package playbook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// bridgeFakeColony mirrors core/tests/test_playbook_runner.py::FakeColony: an
// in-memory tracker plus one dispatch handler. A successful dispatch advances
// its own row, so converge's re-query converges.
type bridgeFakeColony struct {
	mu sync.Mutex

	order       []string
	rows        map[string]bool
	attempts    map[string]int
	alwaysFail  map[string]bool
	failOnce    map[string]bool
	schemas     map[string]map[string]any
	goalRecords []bridgeGoalRecord

	inflight      int
	maxInflight   int
	dispatchDelay time.Duration
}

type bridgeGoalRecord struct {
	task string
	goal *string
}

func newBridgeFakeColony(slugs ...string) *bridgeFakeColony {
	colony := &bridgeFakeColony{
		order:      append([]string(nil), slugs...),
		rows:       map[string]bool{},
		attempts:   map[string]int{},
		alwaysFail: map[string]bool{},
		failOnce:   map[string]bool{},
		schemas:    map[string]map[string]any{},
	}
	for _, slug := range slugs {
		colony.rows[slug] = false
	}
	return colony
}

func (c *bridgeFakeColony) queryRows(string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]map[string]any, 0, len(c.order))
	for _, slug := range c.order {
		if !c.rows[slug] {
			rows = append(rows, map[string]any{"slug": slug})
		}
	}
	return rows
}

func (c *bridgeFakeColony) dispatchOne(_ context.Context, task string, opts DispatchOptions) (map[string]any, error) {
	slug, _ := opts.Data["slug"].(string)
	c.mu.Lock()
	c.attempts[slug]++
	attempt := c.attempts[slug]
	c.inflight++
	if c.inflight > c.maxInflight {
		c.maxInflight = c.inflight
	}
	c.schemas[slug] = opts.Schema
	c.goalRecords = append(c.goalRecords, bridgeGoalRecord{task: task, goal: opts.Goal})
	delay := c.dispatchDelay
	alwaysFail := c.alwaysFail[slug]
	failOnce := c.failOnce[slug]
	c.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	c.mu.Lock()
	c.inflight--
	c.mu.Unlock()

	if alwaysFail || (failOnce && attempt == 1) {
		return map[string]any{"status": "failed", "data": map[string]any{}, "error": "transient"}, nil
	}
	c.mu.Lock()
	c.rows[slug] = true
	c.mu.Unlock()
	return map[string]any{"status": "success", "summary": "ok", "data": map[string]any{"slug": slug}}, nil
}

func (c *bridgeFakeColony) attemptCount(slug string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts[slug]
}

func (c *bridgeFakeColony) maxConcurrent() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInflight
}

// requirePythonBridge skips (rather than fails) on hosts without an
// interpreter; the real run evidence in this repo is recorded with python3.
func requirePythonBridge(t *testing.T) {
	t.Helper()
	if _, err := ResolvePythonInterpreter(); err != nil {
		t.Skipf("Python interpreter unavailable: %v", err)
	}
}

func startPythonBridge(t *testing.T, colony *bridgeFakeColony, script string) *PythonPlaybook {
	t.Helper()
	requirePythonBridge(t)
	handle, err := StartPythonPlaybook(context.Background(), script, PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test-run",
	})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	t.Cleanup(handle.Close)
	return handle
}

// bridgeScript mirrors core/tests/test_playbook_runner.py::make_script.
func bridgeScript(maxRounds, retries int, chunk int, withLane bool, laneConcurrency int, ratePerMin int) string {
	rateArg := ""
	if ratePerMin > 0 {
		rateArg = fmt.Sprintf(", rate_per_min=%d", ratePerMin)
	}
	laneDecl := ""
	laneArg := ""
	if withLane {
		laneDecl = fmt.Sprintf("lane(\"acct-1\", concurrency=%d%s)\n", laneConcurrency, rateArg)
		laneArg = ", lane=\"acct-1\""
	}
	return fmt.Sprintf(`
meta = {"name": "test-enrich", "description": "converge the leads table",
        "phases": ["Enrich"]}

RECEIPT = {"type": "object", "required": ["slug", "status"],
           "properties": {"slug": {"type": "string"},
                          "status": {"enum": ["enriched", "no-data"]}}}

%s
async def run(args):
    await converge(
        pending=lambda: tracker_query("SELECT slug FROM leads WHERE done=0"),
        dispatch=lambda row, i: worker(
            task=f"Enrich {row['slug']}",
            data={"slug": row["slug"]},
            retries=%d, schema=RECEIPT%s),
        max_rounds=%d,
        chunk=%d,
    )
    remaining = tracker_count("SELECT slug FROM leads WHERE done=0")
    log(f"remaining={remaining}")
    return {"remaining": remaining, "dead": deadletter.size, "args": args}
`, laneDecl, retries, laneArg, maxRounds, chunk)
}

func bridgeResult(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", out["result"])
	}
	return result
}

func bridgeLogLines(t *testing.T, out map[string]any) []string {
	t.Helper()
	raw, ok := out["logs"].([]any)
	if !ok {
		t.Fatalf("logs = %#v, want an array", out["logs"])
	}
	lines := make([]string, 0, len(raw))
	for _, item := range raw {
		lines = append(lines, fmt.Sprint(item))
	}
	return lines
}

func bridgeDispatched(t *testing.T, out map[string]any) int {
	t.Helper()
	value, ok := out["dispatched"].(float64)
	if !ok {
		t.Fatalf("dispatched = %#v, want a number", out["dispatched"])
	}
	return int(value)
}

func anyLogContains(lines []string, fragment string) bool {
	for _, line := range lines {
		if strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

// Parity: core/tests/test_playbook_runner.py::test_converges_all_rows
func TestPythonBridgeConvergesAllRows(t *testing.T) {
	colony := newBridgeFakeColony("a", "b", "c", "d", "e")
	handle := startPythonBridge(t, colony, bridgeScript(4, 2, 2, true, 2, 0))
	if handle.Meta["name"] != "test-enrich" {
		t.Fatalf("meta = %#v, want name test-enrich", handle.Meta)
	}
	out, err := handle.Execute(context.Background(), nil, 4)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	result := bridgeResult(t, out)
	if result["remaining"] != float64(0) || result["dead"] != float64(0) {
		t.Fatalf("result = %#v, want remaining=0 dead=0", result)
	}
	if got := bridgeDispatched(t, out); got != 5 {
		t.Fatalf("dispatched = %d, want 5", got)
	}
	for _, slug := range colony.order {
		if got := colony.attemptCount(slug); got != 1 {
			t.Fatalf("attempts[%s] = %d, want 1", slug, got)
		}
	}
	if !anyLogContains(bridgeLogLines(t, out), "round 1: 5 dispatched, 5 ok, 0 failed") {
		t.Fatalf("logs = %v, want the round summary", bridgeLogLines(t, out))
	}
	// The receipt schema rides the dispatch exactly like upstream.
	if schema := colony.schemas["a"]; schema == nil || fmt.Sprint(schema["type"]) != "object" {
		t.Fatalf("schema = %#v, want the RECEIPT object", schema)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_retry_then_succeed
func TestPythonBridgeRetryThenSucceed(t *testing.T) {
	colony := newBridgeFakeColony("a", "b", "c")
	colony.failOnce["b"] = true
	handle := startPythonBridge(t, colony, bridgeScript(4, 2, 2, true, 2, 0))
	out, err := handle.Execute(context.Background(), nil, 4)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	result := bridgeResult(t, out)
	if result["remaining"] != float64(0) || result["dead"] != float64(0) {
		t.Fatalf("result = %#v, want remaining=0 dead=0", result)
	}
	if got := colony.attemptCount("b"); got != 2 {
		t.Fatalf("attempts[b] = %d, want 2 (fail once, retry)", got)
	}
	if got := colony.attemptCount("a"); got != 1 {
		t.Fatalf("attempts[a] = %d, want 1", got)
	}
	if got := bridgeDispatched(t, out); got != 4 {
		t.Fatalf("dispatched = %d, want 4 (every attempt counts)", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_permanent_failure_is_dead_lettered_once
func TestPythonBridgePermanentFailureDeadLetteredOnce(t *testing.T) {
	colony := newBridgeFakeColony("a", "b")
	colony.alwaysFail["b"] = true
	handle := startPythonBridge(t, colony, bridgeScript(1, 2, 2, true, 2, 0))
	out, err := handle.Execute(context.Background(), nil, 4)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	result := bridgeResult(t, out)
	if result["remaining"] != float64(1) || result["dead"] != float64(1) {
		t.Fatalf("result = %#v, want remaining=1 dead=1", result)
	}
	dead, ok := out["deadletter"].([]any)
	if !ok || len(dead) != 1 {
		t.Fatalf("deadletter = %#v, want exactly one entry", out["deadletter"])
	}
	entry, _ := dead[0].(map[string]any)
	row, _ := entry["row"].(map[string]any)
	if row["slug"] != "b" {
		t.Fatalf("deadletter row = %#v, want slug b", row)
	}
	if reason, _ := entry["reason"].(string); !strings.Contains(reason, "unresolved after 1 round(s)") {
		t.Fatalf("deadletter reason = %q", reason)
	}
	if got := colony.attemptCount("b"); got != 3 {
		t.Fatalf("attempts[b] = %d, want retries+1 = 3", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_rerun_is_resume
func TestPythonBridgeRerunIsResume(t *testing.T) {
	colony := newBridgeFakeColony("a", "b", "c")
	// One run = one interpreter process (the tool starts a fresh process per
	// run), so resume is proven across two handles over the same tracker.
	firstHandle := startPythonBridge(t, colony, bridgeScript(4, 2, 2, true, 2, 0))
	first, err := firstHandle.Execute(context.Background(), nil, 4)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if got := bridgeDispatched(t, first); got != 3 {
		t.Fatalf("first dispatched = %d, want 3", got)
	}
	firstHandle.Close()

	secondHandle := startPythonBridge(t, colony, bridgeScript(4, 2, 2, true, 2, 0))
	second, err := secondHandle.Execute(context.Background(), nil, 4)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if got := bridgeDispatched(t, second); got != 0 {
		t.Fatalf("second dispatched = %d, want 0 (converged)", got)
	}
	if result := bridgeResult(t, second); result["remaining"] != float64(0) {
		t.Fatalf("second result = %#v", result)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_bad_script_missing_meta
func TestPythonBridgeMissingMeta(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony("a")
	_, err := StartPythonPlaybook(context.Background(), "async def run(args):\n    return 1\n", PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	var scriptErr *PythonScriptError
	if !errors.As(err, &scriptErr) {
		t.Fatalf("err = %T %v, want *PythonScriptError", err, err)
	}
	if scriptErr.Stage != "load" {
		t.Fatalf("stage = %q, want load", scriptErr.Stage)
	}
	if !strings.Contains(err.Error(), "meta") {
		t.Fatalf("error = %q, want the meta validation message", err.Error())
	}
}

// Parity: core/tests/test_playbook_runner.py::test_bad_script_missing_run
func TestPythonBridgeMissingRun(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony("a")
	_, err := StartPythonPlaybook(context.Background(), "meta = {\"name\": \"x\"}\n", PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	if !strings.Contains(err.Error(), "run(args)") {
		t.Fatalf("error = %q, want a missing-run message", err.Error())
	}
}

// Parity: core/tests/test_playbook_runner.py::test_load_is_synchronous_and_validates
func TestPythonBridgeSyntaxError(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony("a")
	_, err := StartPythonPlaybook(context.Background(), "meta = {\n", PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	var scriptErr *PythonScriptError
	if !errors.As(err, &scriptErr) || scriptErr.Stage != "load" {
		t.Fatalf("err = %T %v, want a load-stage *PythonScriptError", err, err)
	}
	if !strings.Contains(err.Error(), "SyntaxError") {
		t.Fatalf("error = %q, want SyntaxError", err.Error())
	}
}

// Parity: core/tests/test_playbook_runner.py::test_playbook_can_use_stdlib_imports
func TestPythonBridgeStdlibImports(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony()
	script := "import json\n" +
		"import datetime as dt\n" +
		"meta = {\"name\": \"x\"}\n" +
		"async def run(args):\n" +
		"    return {\"j\": json.dumps({\"a\": 1}), \"has_dt\": dt.timezone is not None}\n"
	handle, err := StartPythonPlaybook(context.Background(), script, PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	defer handle.Close()
	out, err := handle.Execute(context.Background(), nil, 1)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	result := bridgeResult(t, out)
	if result["j"] != "{\"a\": 1}" || result["has_dt"] != true {
		t.Fatalf("result = %#v, want stdlib output", result)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_run_loaded_surfaces_runtime_error
func TestPythonBridgeRuntimeErrorSurfaces(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony()
	script := "meta = {\"name\": \"x\"}\nasync def run(args):\n    return undefined_name\n"
	handle, err := StartPythonPlaybook(context.Background(), script, PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err != nil {
		t.Fatalf("load should succeed: %v", err)
	}
	defer handle.Close()
	_, err = handle.Execute(context.Background(), nil, 1)
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	var scriptErr *PythonScriptError
	if !errors.As(err, &scriptErr) || scriptErr.Stage != "run" {
		t.Fatalf("err = %T %v, want a run-stage *PythonScriptError", err, err)
	}
	text := err.Error()
	if !strings.Contains(text, "NameError") || !strings.Contains(text, "undefined_name") {
		t.Fatalf("error = %q, want the root cause", text)
	}
	if !strings.Contains(text, "playbook line 3") {
		t.Fatalf("error = %q, want the playbook line", text)
	}
	if !strings.Contains(scriptErr.Traceback, "<playbook>") {
		t.Fatalf("traceback = %q, want <playbook> frames", scriptErr.Traceback)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_worker_goal_passes_through_to_dispatch
func TestPythonBridgeGoalPassesThrough(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony()
	script := "meta = {\"name\": \"g\"}\n" +
		"async def run(args):\n" +
		"    a = await worker(\"job A\", goal=\"Checking 6 profiles\")\n" +
		"    b = await worker(\"job B\")\n" +
		"    return {\"a\": a[\"status\"], \"b\": b[\"status\"]}\n"
	handle, err := StartPythonPlaybook(context.Background(), script, PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	defer handle.Close()
	out, err := handle.Execute(context.Background(), nil, 1)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result := bridgeResult(t, out); result["a"] != "done" || result["b"] != "done" {
		t.Fatalf("result = %#v, want both done", result)
	}
	colony.mu.Lock()
	records := append([]bridgeGoalRecord(nil), colony.goalRecords...)
	colony.mu.Unlock()
	if len(records) != 2 {
		t.Fatalf("goals recorded = %d, want 2", len(records))
	}
	if records[0].goal == nil || *records[0].goal != "Checking 6 profiles" {
		t.Fatalf("first goal = %#v, want it forwarded", records[0].goal)
	}
	if records[1].goal != nil {
		t.Fatalf("second goal = %#v, want nil (omitted)", *records[1].goal)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_chunk_defaults_to_cap
func TestPythonBridgeChunkDefaultsToCap(t *testing.T) {
	colony := newBridgeFakeColony("0", "1", "2", "3", "4", "5", "6", "7")
	colony.dispatchDelay = 30 * time.Millisecond
	// No explicit chunk and no lane: converge defaults chunk to the run's cap.
	script := `
meta = {"name": "chunkless"}

async def run(args):
    await converge(
        pending=lambda: tracker_query("SELECT slug FROM leads WHERE done=0"),
        dispatch=lambda row, i: worker(task=f"do {row['slug']}", data={"slug": row["slug"]}),
        max_rounds=1)
    return {"remaining": tracker_count("SELECT slug FROM leads WHERE done=0")}
`
	handle := startPythonBridge(t, colony, script)
	out, err := handle.Execute(context.Background(), nil, 2)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result := bridgeResult(t, out); result["remaining"] != float64(0) {
		t.Fatalf("result = %#v", result)
	}
	if got := colony.maxConcurrent(); got > 2 {
		t.Fatalf("max in-flight = %d, want <= 2 (chunk defaults to the cap)", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_lane_limits_concurrency
func TestPythonBridgeLaneLimitsConcurrency(t *testing.T) {
	colony := newBridgeFakeColony("0", "1", "2", "3", "4", "5", "6", "7")
	colony.dispatchDelay = 20 * time.Millisecond
	handle := startPythonBridge(t, colony, bridgeScript(4, 0, 2, true, 2, 0))
	out, err := handle.Execute(context.Background(), nil, 8)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result := bridgeResult(t, out); result["remaining"] != float64(0) {
		t.Fatalf("result = %#v", result)
	}
	if got := colony.maxConcurrent(); got > 2 {
		t.Fatalf("max in-flight = %d, want <= 2 (lane concurrency / chunk)", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_rate_per_min_spaces_dispatches
func TestPythonBridgeRateGateSpacesDispatches(t *testing.T) {
	colony := newBridgeFakeColony("0", "1", "2", "3")
	handle := startPythonBridge(t, colony, bridgeScript(1, 0, 4, true, 4, 600)) // 0.1s interval
	started := time.Now()
	out, err := handle.Execute(context.Background(), nil, 4)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result := bridgeResult(t, out); result["remaining"] != float64(0) {
		t.Fatalf("result = %#v", result)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("elapsed = %v, want >= 3*0.1s of spacing", elapsed)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_warns_when_chunk_exceeds_cap
func TestPythonBridgeWarnsWhenChunkExceedsCap(t *testing.T) {
	colony := newBridgeFakeColony("a", "b")
	handle := startPythonBridge(t, colony, bridgeScript(1, 0, 50, false, 1, 0))
	out, err := handle.Execute(context.Background(), nil, 4)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !anyLogContains(bridgeLogLines(t, out), "bounds this run") {
		t.Fatalf("logs = %v, want the cap warning", bridgeLogLines(t, out))
	}
}

// Parity: core/tests/test_playbook_runner.py::test_warns_when_lane_exceeds_cap
func TestPythonBridgeWarnsWhenLaneExceedsCap(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony("a")
	// Upstream's runner test constructs PlaybookRun(concurrency_cap=1), so the
	// module-scope lane(concurrency=2) sees the cap while loading.
	capValue := 1
	handle, err := StartPythonPlaybook(context.Background(), bridgeScript(1, 0, 1, true, 2, 0), PythonOptions{
		DispatchOne:    colony.dispatchOne,
		QueryRows:      colony.queryRows,
		RunID:          "test",
		ConcurrencyCap: &capValue,
	})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	t.Cleanup(handle.Close)
	out, err := handle.Execute(context.Background(), nil, 1)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !anyLogContains(bridgeLogLines(t, out), "exceeds the colony cap") {
		t.Fatalf("logs = %v, want the lane cap warning", bridgeLogLines(t, out))
	}
}

// Parity-exempt: upstream runs Python in-process, so it has no external
// interpreter lookup to test. This pins the deployment override semantics.
func TestResolvePythonInterpreterOverride(t *testing.T) {
	realPython, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	t.Setenv("HIVE_PYTHON", filepath.Join(t.TempDir(), "missing-python"))
	if _, err := ResolvePythonInterpreter(); err == nil || !strings.Contains(err.Error(), "HIVE_PYTHON") {
		t.Fatalf("err = %v, want an explicit HIVE_PYTHON error", err)
	}
	t.Setenv("HIVE_PYTHON", realPython)
	got, err := ResolvePythonInterpreter()
	if err != nil || got != realPython {
		t.Fatalf("ResolvePythonInterpreter = (%q, %v), want %q", got, err, realPython)
	}
}

// Parity-exempt: upstream has no interpreter to be missing. The tool must
// report `unsupported` rather than faking a successful run.
func TestPythonBridgeInterpreterMissingIsExplicit(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("HIVE_PYTHON", "")
	colony := newBridgeFakeColony("a")
	_, err := StartPythonPlaybook(context.Background(), "meta = {\"name\": \"x\"}\nasync def run(args):\n    return {}\n", PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err == nil {
		t.Fatal("expected an unavailable-interpreter error")
	}
	if !IsPythonUnavailable(err) {
		t.Fatalf("err = %T %v, want *PythonUnavailableError", err, err)
	}
	if !strings.Contains(err.Error(), "python3") {
		t.Fatalf("err = %q, want the lookup detail", err.Error())
	}
}

// Parity-exempt: protocol robustness is Go-specific (upstream has no wire to
// corrupt). An interpreter that writes non-JSON must fail loudly.
func TestPythonBridgeRejectsGarbageOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake interpreter is POSIX-only")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-python")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'this is not json'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_PYTHON", fake)
	colony := newBridgeFakeColony("a")
	_, err := StartPythonPlaybook(context.Background(), "meta = {\"name\": \"x\"}\nasync def run(args):\n    return {}\n", PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err == nil {
		t.Fatal("expected a protocol error")
	}
	var bridgeErr *PythonBridgeError
	if !errors.As(err, &bridgeErr) {
		t.Fatalf("err = %T %v, want *PythonBridgeError", err, err)
	}
	if !strings.Contains(err.Error(), "cannot parse") {
		t.Fatalf("err = %q, want the parse failure detail", err.Error())
	}
}

// Parity-exempt: upstream runs the playbook in-process, so it has no child
// process that can stall. This pins the Go port's parent-side run deadline: a
// child that never answers the run command must not park Execute forever — the
// deadline fires, the child is killed, and the error names the stall.
func TestPythonBridgeRunDeadlineStopsStalledChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake interpreter is POSIX-only")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "stalled-python")
	// Answer the handshake, consume the run command, then never speak again.
	stub := `#!/bin/sh
read -r _init
printf '{"type":"loaded","meta":{"name":"stalled"},"python_version":"3.9"}\n'
read -r _run
exec sleep 600
`
	if err := os.WriteFile(fake, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_PYTHON", fake)
	colony := newBridgeFakeColony("a")
	limit := 300 * time.Millisecond
	handle, err := StartPythonPlaybook(context.Background(),
		"meta = {\"name\": \"stalled\"}\nasync def run(args):\n    return {}\n",
		PythonOptions{
			DispatchOne:  colony.dispatchOne,
			QueryRows:    colony.queryRows,
			RunID:        "stalled-run",
			StallTimeout: &limit,
		})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	defer handle.Close()

	started := time.Now()
	out, err := handle.Execute(context.Background(), nil, 1)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatalf("Execute = %#v, want a stall error (the child never answered)", out)
	}
	var bridgeErr *PythonBridgeError
	if !errors.As(err, &bridgeErr) {
		t.Fatalf("err = %T %v, want *PythonBridgeError", err, err)
	}
	text := err.Error()
	if !strings.Contains(text, "no bridge activity for 300ms") || !strings.Contains(text, "bridge stalled") {
		t.Fatalf("error = %q, want the run-deadline diagnosis", text)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Execute held for %v, want the deadline to fire promptly", elapsed)
	}
	// The stall path must not return while the child still runs: a non-nil
	// ProcessState means cmd.Wait returned, i.e. the process group was
	// terminated and reaped before Execute came back.
	if handle.cmd == nil || handle.cmd.ProcessState == nil {
		t.Fatal("interpreter process was not reaped after the run deadline")
	}
	select {
	case <-handle.readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop did not exit after the stalled child was killed")
	}
}

// Parity-exempt: upstream runs in-process; this pins that a live but silent
// bridge (the interpreter is healthy, the run just produces no terminal event)
// is still cut at the parent-side limit and killed.
func TestPythonBridgeRunDeadlineCutsSilentLiveBridge(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony()
	script := "import asyncio\nmeta = {\"name\": \"quiet\"}\nasync def run(args):\n    await asyncio.sleep(600)\n    return {}\n"
	limit := 300 * time.Millisecond
	handle, err := StartPythonPlaybook(context.Background(), script, PythonOptions{
		DispatchOne:  colony.dispatchOne,
		QueryRows:    colony.queryRows,
		RunID:        "quiet-run",
		StallTimeout: &limit,
	})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	defer handle.Close()

	started := time.Now()
	_, err = handle.Execute(context.Background(), nil, 1)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Execute succeeded, want the run deadline to fire")
	}
	if !strings.Contains(err.Error(), "no bridge activity for 300ms") {
		t.Fatalf("error = %q, want the run-deadline message", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Execute held for %v, want the deadline to fire promptly", elapsed)
	}
	if handle.cmd == nil || handle.cmd.ProcessState == nil {
		t.Fatal("interpreter process was not reaped after the run deadline")
	}
}

// Parity-exempt: upstream calls its hooks as closures inside one process, so it
// has no request/response ordering window at all. This pins the Go port's rule
// that a reply may never be dropped for arriving before the child registered
// the pending request — the ordering that used to hang real runs (the host
// answers tracker_query/dispatch_one fast enough to win that race).
func TestPythonBridgePendingRequestRegisteredBeforeSend(t *testing.T) {
	interpreter, err := ResolvePythonInterpreter()
	if err != nil {
		t.Skipf("Python interpreter unavailable: %v", err)
	}
	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.py")
	if err := os.WriteFile(bootstrapPath, []byte(pythonBootstrapSource), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, interpreter, "-u",
		filepath.Join("testdata", "bridge_race_probe.py"), bootstrapPath)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("race probe hung (a reply was dropped before registration):\n%s", out)
	}
	if err != nil {
		t.Fatalf("race probe failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK:") {
		t.Fatalf("race probe output = %q, want the OK marker", out)
	}
}

// Parity-exempt: upstream has no child process to close, so there is no
// re-entrancy question there. This pins that the Go handle's Close can be
// called repeatedly and from several goroutines (Execute's kill path and the
// caller's deferred Close can overlap) without leaking the child or the
// bootstrap directory.
func TestPythonPlaybookCloseIsIdempotent(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony()
	handle, err := StartPythonPlaybook(context.Background(),
		"meta = {\"name\": \"close-me\"}\nasync def run(args):\n    return {}\n",
		PythonOptions{
			DispatchOne: colony.dispatchOne,
			QueryRows:   colony.queryRows,
			RunID:       "close-run",
		})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle.Close()
		}()
	}
	wg.Wait()
	handle.Close()

	if handle.cmd == nil || handle.cmd.ProcessState == nil {
		t.Fatal("interpreter process was not reaped by Close")
	}
	if _, err := os.Stat(handle.workDir); !os.IsNotExist(err) {
		t.Fatalf("bootstrap dir %q survived Close: %v", handle.workDir, err)
	}
}

// Parity-exempt: process-group cancellation is a Go-process boundary concern;
// upstream cancels an asyncio task instead.
func TestPythonBridgeCancellationTerminatesProcess(t *testing.T) {
	requirePythonBridge(t)
	colony := newBridgeFakeColony()
	script := "import asyncio\nmeta = {\"name\": \"slow\"}\nasync def run(args):\n    await asyncio.sleep(30)\n    return {}\n"
	handle, err := StartPythonPlaybook(context.Background(), script, PythonOptions{
		DispatchOne: colony.dispatchOne,
		QueryRows:   colony.queryRows,
		RunID:       "test",
	})
	if err != nil {
		t.Fatalf("StartPythonPlaybook: %v", err)
	}
	defer handle.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = handle.Execute(ctx, nil, 1)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("cancellation took %v, want the child killed promptly", elapsed)
	}
	handle.Close()
	// A non-nil ProcessState means cmd.Wait returned, i.e. the child was reaped
	// rather than left running behind the bridge.
	if handle.cmd == nil || handle.cmd.ProcessState == nil {
		t.Fatal("interpreter process was not reaped after cancellation")
	}
}

package playbook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func strPtr(v string) *string { return &v }

// Parity: core/tests/test_playbook_tools.py::test_extract_rows_kind_passthrough
func TestExtractRowsKindPassthrough(t *testing.T) {
	res := map[string]any{
		"kind":    "rows",
		"columns": []any{"a"},
		"rows":    []any{[]any{1}},
	}
	out := ExtractRowsResult(res)
	if out == nil || out["kind"] != "rows" {
		t.Fatalf("out = %v, want the rows result", out)
	}
	if got := out["rows"].([]any)[0].([]any)[0]; got != 1 {
		t.Fatalf("rows[0][0] = %v, want 1", got)
	}
	out["marker"] = true
	if res["marker"] != true {
		t.Fatal("expected ExtractRowsResult to return the same map")
	}
}

// Parity: core/tests/test_playbook_tools.py::test_extract_script_kind_trailing_semicolon
func TestExtractScriptKindTrailingSemicolon(t *testing.T) {
	res := map[string]any{
		"kind": "script",
		"results": []any{
			map[string]any{"kind": "rows", "columns": []any{"a"}, "rows": []any{[]any{1}, []any{2}}},
		},
	}
	out := ExtractRowsResult(res)
	if out == nil {
		t.Fatal("out is nil, want the script's rows result")
	}
	rows := out["rows"].([]any)
	if len(rows) != 2 || rows[1].([]any)[0] != 2 {
		t.Fatalf("rows = %v, want [[1] [2]]", rows)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_extract_script_uses_last_rows_result
func TestExtractScriptUsesLastRowsResult(t *testing.T) {
	res := map[string]any{
		"kind": "script",
		"results": []any{
			map[string]any{"kind": "exec", "rowcount": 0},
			map[string]any{"kind": "rows", "columns": []any{"a"}, "rows": []any{[]any{1}}},
			map[string]any{"kind": "rows", "columns": []any{"a"}, "rows": []any{[]any{9}}},
		},
	}
	out := ExtractRowsResult(res)
	if out == nil {
		t.Fatal("out is nil")
	}
	if got := out["rows"].([]any)[0].([]any)[0]; got != 9 {
		t.Fatalf("rows[0][0] = %v, want the last rows result (9)", got)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_extract_exec_kind_is_none
func TestExtractExecKindIsNone(t *testing.T) {
	if out := ExtractRowsResult(map[string]any{"kind": "exec", "rowcount": 1}); out != nil {
		t.Fatalf("out = %v, want nil", out)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_extract_script_without_rows_is_none
func TestExtractScriptWithoutRowsIsNone(t *testing.T) {
	res := map[string]any{"kind": "script", "results": []any{map[string]any{"kind": "exec"}}}
	if out := ExtractRowsResult(res); out != nil {
		t.Fatalf("out = %v, want nil", out)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_error_detail_surfaces_original_cause
func TestErrorDetailSurfacesOriginalCause(t *testing.T) {
	colony := newFakeColony()
	run := NewPlaybookRun(RunOptions{DispatchOne: colony.dispatchOne, QueryRows: colony.queryRows, RunID: "t"})
	if err := run.Load(&Script{
		Meta: map[string]any{"name": "x"},
		Run: func(context.Context, any, *Hooks) (any, error) {
			return nil, &ScriptRuntimeError{Kind: "NameError", Message: "name 'undefined_name' is not defined", Line: 3}
		},
	}); err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err := run.RunLoaded(context.Background(), nil)
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	concise, full := ErrorDetail(err)
	if !strings.HasPrefix(concise, "NameError:") {
		t.Fatalf("concise = %q, want NameError prefix", concise)
	}
	if !strings.Contains(concise, "undefined_name") {
		t.Fatalf("concise = %q, want the symbol name", concise)
	}
	if !strings.Contains(concise, "playbook line 3") {
		t.Fatalf("concise = %q, want playbook line 3", concise)
	}
	if !strings.Contains(full, "undefined_name") || !strings.Contains(full, "NameError") {
		t.Fatalf("full = %q, want the original error retained", full)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_persist_script_enables_resume
func TestPersistScriptEnablesResume(t *testing.T) {
	dir := t.TempDir()

	if got := SanitizeName("Reactor Outreach Batch!"); got != "reactor-outreach-batch" {
		t.Fatalf("SanitizeName = %q, want reactor-outreach-batch", got)
	}
	if got := SanitizeName("   "); got != "playbook" {
		t.Fatalf("SanitizeName(blank) = %q, want playbook", got)
	}

	p, ok := PersistScript(dir, "Reactor Outreach Batch!", "meta = {}\n# v1\n")
	if !ok {
		t.Fatal("PersistScript failed")
	}
	if filepath.Base(p) != "reactor-outreach-batch.play.py" {
		t.Fatalf("file = %q, want reactor-outreach-batch.play.py", filepath.Base(p))
	}
	if filepath.Base(filepath.Dir(p)) != "playbooks" {
		t.Fatalf("parent = %q, want playbooks", filepath.Base(filepath.Dir(p)))
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read persisted script: %v", err)
	}
	if !strings.Contains(string(data), "# v1") {
		t.Fatalf("content = %q, want v1", data)
	}

	p2, ok := PersistScript(dir, "Reactor Outreach Batch!", "meta = {}\n# v2\n")
	if !ok || p2 != p {
		t.Fatalf("p2 = %q (ok=%v), want the same path %q", p2, ok, p)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "# v2") {
		t.Fatalf("content = %q, want v2", data)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_resolve_script_three_sources
func TestResolveScriptThreeSources(t *testing.T) {
	dir := t.TempDir()
	pdir := filepath.Join(dir, "playbooks")

	script, src, err := ResolveScript(strPtr("meta={}\n# inline"), nil, nil, pdir)
	if err != nil || src != "" || !strings.Contains(script, "# inline") {
		t.Fatalf("inline: script=%q src=%q err=%v", script, src, err)
	}

	_, _, err = ResolveScript(strPtr("x"), strPtr("y"), nil, pdir)
	if err == nil || !strings.Contains(err.Error(), "exactly ONE") {
		t.Fatalf("two sources: err = %v, want exactly ONE", err)
	}

	if _, ok := PersistScript(dir, "Enrich Leads", "meta={}\n# saved"); !ok {
		t.Fatal("PersistScript failed")
	}
	script, src, err = ResolveScript(nil, nil, strPtr("Enrich Leads"), pdir)
	if err != nil || !strings.Contains(script, "# saved") {
		t.Fatalf("saved: script=%q err=%v", script, err)
	}
	if !strings.HasSuffix(src, "enrich-leads.play.py") {
		t.Fatalf("src = %q, want ...enrich-leads.play.py", src)
	}

	_, _, err = ResolveScript(nil, nil, strPtr("nope"), pdir)
	if err == nil || !strings.Contains(err.Error(), "no saved playbook named 'nope'") ||
		!strings.Contains(err.Error(), "enrich-leads") {
		t.Fatalf("unknown name: err = %v, want a listing error", err)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_resolve_concurrency_queen_declared
func TestResolveConcurrencyQueenDeclared(t *testing.T) {
	if n, err := ResolveConcurrency(map[string]any{"concurrency": 12}); err != nil || n != 12 {
		t.Fatalf("declared: n=%d err=%v, want 12", n, err)
	}
	if n, err := ResolveConcurrency(map[string]any{}); err != nil || n != DefaultPlaybookConcurrency {
		t.Fatalf("default: n=%d err=%v, want %d", n, err, DefaultPlaybookConcurrency)
	}
	n, err := ResolveConcurrency(map[string]any{"concurrency": MaxPlaybookConcurrency + 1})
	if n != 0 || err == nil || !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Fatalf("too big: n=%d err=%v", n, err)
	}
	n, err = ResolveConcurrency(map[string]any{"concurrency": "lots"})
	if n != 0 || err == nil || !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("non-integer: n=%d err=%v", n, err)
	}
	if n, err := ResolveConcurrency(map[string]any{"concurrency": 0}); err != nil || n != 1 {
		t.Fatalf("zero: n=%d err=%v, want 1", n, err)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_playbook_tools_are_colony_queen_only
func TestPlaybookToolsAreColonyQueenOnly(t *testing.T) {
	queenOnly := QueenOnlyTools()
	for _, tool := range PlaybookToolNames {
		if _, ok := ColonyQueenTools[tool]; !ok {
			t.Errorf("%s missing from ColonyQueenTools", tool)
		}
		if _, ok := IndependentQueenTools[tool]; ok {
			t.Errorf("%s must not be visible to the independent queen", tool)
		}
		if _, ok := queenOnly[tool]; !ok {
			t.Errorf("%s must be stripped from workers", tool)
		}
	}
	if _, ok := ColonyQueenTools["run_worker"]; !ok {
		t.Error("run_worker missing from ColonyQueenTools")
	}
	if _, ok := queenOnly["run_worker"]; !ok {
		t.Error("run_worker must be queen-only")
	}
}

type batchColony struct {
	workers []WorkerRef
	stopped []string
}

func (b *batchColony) Workers() []WorkerRef { return b.workers }

func (b *batchColony) StopWorker(_ context.Context, id string) error {
	b.stopped = append(b.stopped, id)
	return nil
}

// Parity: core/tests/test_playbook_tools.py::test_stop_batch_workers_targets_only_the_batch
func TestStopBatchWorkersTargetsOnlyTheBatch(t *testing.T) {
	colony := &batchColony{workers: []WorkerRef{
		{ID: "w1", BatchID: "pb_A", Active: true},
		{ID: "w2", BatchID: "pb_A", Active: false},
		{ID: "w3", BatchID: "pb_B", Active: true},
	}}
	n := StopBatchWorkers(context.Background(), colony, "pb_A")
	if n != 1 || len(colony.stopped) != 1 || colony.stopped[0] != "w1" {
		t.Fatalf("stopped %d %v, want 1 [w1]", n, colony.stopped)
	}
}

// Parity: core/tests/test_playbook_tools.py::test_stop_playbooks_for_colony_cancels_only_its_runs
func TestStopPlaybooksForColonyCancelsOnlyItsRuns(t *testing.T) {
	colonyA := &struct{ id string }{"a"}
	colonyB := &struct{ id string }{"b"}

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() { <-ctxA.Done(); close(doneA) }()
	go func() { <-ctxB.Done(); close(doneB) }()

	RegisterRunningPlaybook("run_a", &RunningPlaybook{Cancel: cancelA, Done: doneA, Colony: colonyA})
	RegisterRunningPlaybook("run_b", &RunningPlaybook{Cancel: cancelB, Done: doneB, Colony: colonyB})
	defer func() {
		UnregisterRunningPlaybook("run_a")
		UnregisterRunningPlaybook("run_b")
		cancelB()
		<-doneB
	}()

	targets := StopPlaybooksForColony(colonyA)
	if len(targets) != 1 || targets[0] != "run_a" {
		t.Fatalf("cancelled = %v, want [run_a]", targets)
	}
	if ctxA.Err() == nil {
		t.Error("colony A's run should have been cancelled")
	}
	if ctxB.Err() != nil {
		t.Error("colony B's run should have been left running")
	}
}

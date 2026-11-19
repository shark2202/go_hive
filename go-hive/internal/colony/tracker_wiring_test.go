package colony

// Tracker wiring tests: a real sqlite tracker.db in a temp colony dir, a fake
// LLM driving the actual agent loop, and the runtime's spawn path.
//
// The assertions are on the database, not on prose: the worker reports success
// only after the tracker row it wrote is readable from the colony's ledger.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// trackerTestColony materialises a colony directory (metadata.json is what
// marks it as one) and returns the runtime bound to it.
func trackerTestColony(t *testing.T, provider llm.Completer, colonyID string) (*ColonyRuntime, string) {
	t.Helper()
	colonyDir := filepath.Join(t.TempDir(), colonyID)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	// t.TempDir() sits behind /var → /private/var on macOS and
	// EnsureTrackerDB resolves symlinks; compare like with like.
	if resolved, err := filepath.EvalSymlinks(colonyDir); err == nil {
		colonyDir = resolved
	}
	if err := os.WriteFile(filepath.Join(colonyDir, colonyMetadataFile), []byte(`{"colony_id":"`+colonyID+`"}`), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: colonyDir,
		StreamID:    colonyID,
		LLM:         provider,
		Tools:       []llm.ToolDef{},
	})
	t.Cleanup(runtime.Stop)
	return runtime, colonyDir
}

// trackerScriptLLM replays one tool call per completion. An exhausted script
// ends the turn with plain text, so a runaway loop cannot hang the test.
type trackerScriptLLM struct {
	mu    sync.Mutex
	calls []llm.ToolCall
	index int
}

func (s *trackerScriptLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index >= len(s.calls) {
		return llm.Completion{Content: "done", StopReason: "stop"}, nil
	}
	call := s.calls[s.index]
	s.index++
	return llm.Completion{ToolCalls: []llm.ToolCall{call}, StopReason: "tool_calls"}, nil
}

func trackerToolCall(name string, input map[string]any) llm.ToolCall {
	arguments, _ := json.Marshal(input)
	return llm.ToolCall{ID: "call_" + name, Name: name, Arguments: string(arguments)}
}

func trackerReportCall(summary string) llm.ToolCall {
	return trackerToolCall("report_to_parent", map[string]any{
		"status":  "success",
		"summary": summary,
		"data":    map[string]any{},
	})
}

func trackerWaitReports(t *testing.T, runtime *ColonyRuntime, ids []string) []map[string]any {
	t.Helper()
	reports, err := runtime.WaitForWorkerReports(context.Background(), ids, 30*time.Second)
	if err != nil {
		t.Fatalf("wait reports: %v", err)
	}
	if len(reports) != len(ids) {
		t.Fatalf("reports = %d, want %d", len(reports), len(ids))
	}
	for _, report := range reports {
		if status, _ := report["status"].(string); status == "failed" || status == "stopped" {
			t.Fatalf("worker report = %+v", report)
		}
	}
	return reports
}

func trackerToolNames(defs []llm.ToolDef) map[string]struct{} {
	names := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		names[def.Name] = struct{}{}
	}
	return names
}

// Parity: core/tests/test_tracker_db.py::test_ensure_tracker_db_fresh
func TestColonyStartProvisionsTrackerDB(t *testing.T) {
	runtime, colonyDir := trackerTestColony(t, &trackerScriptLLM{}, "linkedin5")
	if binding := runtime.TrackerBinding(); binding != nil {
		t.Fatalf("binding before Start = %+v, want nil", binding)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	dbPath := filepath.Join(colonyDir, "tracker", "tracker.db")
	if info, err := os.Stat(dbPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("tracker.db not provisioned at %s: %v", dbPath, err)
	}
	binding := runtime.TrackerBinding()
	if binding == nil {
		t.Fatal("TrackerBinding = nil after Start")
	}
	if binding.Name != "linkedin5" {
		t.Errorf("binding name = %q, want linkedin5", binding.Name)
	}
	if binding.Dir != colonyDir {
		t.Errorf("binding dir = %q, want %q", binding.Dir, colonyDir)
	}
	if binding.TrackerDB != dbPath {
		t.Errorf("binding tracker_db = %q, want %q", binding.TrackerDB, dbPath)
	}
}

// Parity: core/tests/test_worker_tracker_db_propagation.py::test_worker_propagates_binding_from_input_data
func TestSpawnBatchInjectsTrackerBinding(t *testing.T) {
	provider := &trackerScriptLLM{calls: []llm.ToolCall{trackerReportCall("row filled")}}
	runtime, colonyDir := trackerTestColony(t, provider, "colonyA")
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{
		Task: "fill row acme",
		Data: map[string]any{"row_id": "acme"},
	}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	trackerWaitReports(t, runtime, ids)

	worker := runtime.GetWorker(ids[0])
	if worker == nil || worker.Context == nil {
		t.Fatalf("worker %q has no context", ids[0])
	}
	input := worker.Context.InputData
	if input["task"] != "fill row acme" {
		t.Errorf("input_data task = %v, want the task text preserved", input["task"])
	}
	if input["row_id"] != "acme" {
		t.Errorf("input_data lost the caller's data: %v", input)
	}
	if input["worker_id"] != ids[0] {
		t.Errorf("input_data worker_id = %v, want %q", input["worker_id"], ids[0])
	}
	if input["colony_id"] != "colonyA" {
		t.Errorf("input_data colony_id = %v, want colonyA", input["colony_id"])
	}
	raw, ok := input["binding"].(map[string]string)
	if !ok {
		t.Fatalf("input_data binding = %#v, want a name/dir/tracker_db map", input["binding"])
	}
	if raw["name"] != "colonyA" || raw["dir"] != colonyDir {
		t.Errorf("binding = %#v", raw)
	}
	if raw["tracker_db"] != filepath.Join(colonyDir, "tracker", "tracker.db") {
		t.Errorf("binding tracker_db = %q", raw["tracker_db"])
	}

	resolved, err := worker.TrackerResolver()()
	if err != nil || resolved == nil {
		t.Fatalf("worker TrackerResolver = %+v, %v", resolved, err)
	}
	if resolved.TrackerDB != raw["tracker_db"] {
		t.Errorf("resolved tracker_db = %q, want %q", resolved.TrackerDB, raw["tracker_db"])
	}
	if worker.Context.ColonyBindingProvider == nil {
		t.Error("ColonyBindingProvider = nil, want the injected binding")
	}
}

// Parity: core/tests/test_tracker_tools.py::test_register_happy_path_upsert, core/tests/test_tracker_tools.py::test_upsert_inserts_then_updates
func TestWorkerTrackerToolsUpsertAndQuery(t *testing.T) {
	provider := &trackerScriptLLM{calls: []llm.ToolCall{
		trackerToolCall("tracker_upsert", map[string]any{
			"table": "companies",
			"row":   map[string]any{"id": "acme", "name": "Acme", "status": "new"},
		}),
		trackerToolCall("tracker_query", map[string]any{
			"sql": "SELECT name, status FROM companies WHERE id = 'acme'",
		}),
		trackerReportCall("acme recorded"),
	}}
	runtime, _ := trackerTestColony(t, provider, "colonyA")
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	binding := runtime.TrackerBinding()
	if binding == nil {
		t.Fatal("no binding after Start")
	}
	// The queen's half of the loop: model the table and whitelist the columns
	// workers may write.
	if res := tracker.TrackerSQL(binding, "CREATE TABLE companies (id TEXT PRIMARY KEY, name TEXT, status TEXT)", 1000); res["success"] != true {
		t.Fatalf("CREATE TABLE: %+v", res)
	}
	if res := tracker.RegisterWritable(binding, "companies", []string{"name", "status"}, []string{"id"}); res["success"] != true {
		t.Fatalf("RegisterWritable: %+v", res)
	}

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "fill acme"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	trackerWaitReports(t, runtime, ids)

	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatalf("worker %q missing", ids[0])
	}
	names := trackerToolNames(worker.Context.AvailableTools)
	for _, want := range []string{"tracker_upsert", "tracker_query"} {
		if _, ok := names[want]; !ok {
			t.Errorf("worker tool table missing %s: %v", want, names)
		}
	}
	// report_to_parent is appended by workerTools at turn time, not part of
	// the spawn pool.
	if turnTools := trackerToolNames(runtime.workerTools(worker)); len(turnTools) == 0 {
		t.Error("workerTools returned no definitions")
	} else if _, ok := turnTools["report_to_parent"]; !ok {
		t.Errorf("workerTools missing report_to_parent: %v", turnTools)
	}
	for _, hidden := range []string{"tracker_sql", "tracker_register_writable"} {
		if _, ok := names[hidden]; ok {
			t.Errorf("worker tool table advertises queen-only %s", hidden)
		}
	}

	res := tracker.Query(binding, "SELECT name, status FROM companies WHERE id = 'acme'", 1000)
	if res["success"] != true {
		t.Fatalf("query after worker upsert: %+v", res)
	}
	rows, _ := res["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the worker's upserted row", rows)
	}
	row, _ := rows[0].([]any)
	if len(row) != 2 || row[0] != "Acme" || row[1] != "new" {
		t.Fatalf("row = %+v, want [Acme new]", row)
	}
}

// Parity: core/tests/test_tracker_tools.py::test_no_colony_context_returns_clear_error, core/tests/test_tracker_tools.py::test_tracker_query_no_colony_context
func TestWorkerTrackerToolsRejectWithoutBinding(t *testing.T) {
	// A plain DM storage root: no metadata.json, so the runtime must not
	// fabricate a tracker.
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		StreamID:    "dm_session",
		Tools:       []llm.ToolDef{},
	})
	t.Cleanup(runtime.Stop)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if binding := runtime.TrackerBinding(); binding != nil {
		t.Fatalf("binding = %+v, want nil for a non-colony root", binding)
	}

	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "t"})
	names := trackerToolNames(worker.Context.AvailableTools)
	if _, ok := names["tracker_upsert"]; !ok {
		t.Fatalf("worker tool table missing tracker_upsert: %v", names)
	}

	executor := runtime.executorFor(worker)
	for _, call := range []map[string]any{
		{"table": "companies", "row": map[string]any{"id": "acme"}},
		{"sql": "SELECT 1"},
	} {
		name := "tracker_upsert"
		if _, ok := call["sql"]; ok {
			name = "tracker_query"
		}
		result, err := executor(agentloop.ToolCallRequest{Name: name, Input: call})
		if err != nil {
			t.Fatalf("%s: executor error: %v", name, err)
		}
		if !result.IsError {
			t.Fatalf("%s without a binding reported success: %s", name, result.Content)
		}
		if !strings.Contains(result.Content, "no colony context") {
			t.Fatalf("%s error = %q, want the no-colony-context guidance", name, result.Content)
		}
	}
}

// Parity-exempt: colony isolation is structural (one tracker.db per colony
// directory) and upstream has no dedicated cross-colony test — its with_ctx
// fixture binds a single colony at a time. This guards the Go wiring, where the
// binding is derived per runtime instead of injected per test.
func TestTrackerIsolationAcrossColonies(t *testing.T) {
	providerA := &trackerScriptLLM{calls: []llm.ToolCall{
		trackerToolCall("tracker_upsert", map[string]any{
			"table": "companies",
			"row":   map[string]any{"id": "acme", "name": "Acme"},
		}),
		trackerReportCall("a done"),
	}}
	providerB := &trackerScriptLLM{calls: []llm.ToolCall{
		trackerToolCall("tracker_upsert", map[string]any{
			"table": "companies",
			"row":   map[string]any{"id": "globex", "name": "Globex"},
		}),
		trackerReportCall("b done"),
	}}
	runtimeA, _ := trackerTestColony(t, providerA, "colonyA")
	runtimeB, _ := trackerTestColony(t, providerB, "colonyB")
	for _, runtime := range []*ColonyRuntime{runtimeA, runtimeB} {
		if err := runtime.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}

	for _, setup := range []*ColonyRuntime{runtimeA, runtimeB} {
		binding := setup.TrackerBinding()
		if res := tracker.TrackerSQL(binding, "CREATE TABLE companies (id TEXT PRIMARY KEY, name TEXT)", 1000); res["success"] != true {
			t.Fatalf("CREATE TABLE: %+v", res)
		}
		if res := tracker.RegisterWritable(binding, "companies", []string{"name"}, []string{"id"}); res["success"] != true {
			t.Fatalf("RegisterWritable: %+v", res)
		}
	}

	for _, tc := range []struct {
		runtime  *ColonyRuntime
		task     string
		wantName string
	}{
		{runtimeA, "fill acme", "Acme"},
		{runtimeB, "fill globex", "Globex"},
	} {
		ids, err := tc.runtime.SpawnBatch(context.Background(), []BatchTask{{Task: tc.task}})
		if err != nil {
			t.Fatalf("SpawnBatch: %v", err)
		}
		trackerWaitReports(t, tc.runtime, ids)
	}

	for _, tc := range []struct {
		runtime  *ColonyRuntime
		wantName string
		otherID  string
	}{
		{runtimeA, "Acme", "globex"},
		{runtimeB, "Globex", "acme"},
	} {
		binding := tc.runtime.TrackerBinding()
		res := tracker.Query(binding, "SELECT name FROM companies", 1000)
		if res["success"] != true {
			t.Fatalf("%s query: %+v", binding.Name, res)
		}
		rows, _ := res["rows"].([]any)
		if len(rows) != 1 {
			t.Fatalf("%s rows = %+v, want exactly its own row", binding.Name, rows)
		}
		row, _ := rows[0].([]any)
		if len(row) != 1 || row[0] != tc.wantName {
			t.Fatalf("%s row = %+v, want %s", binding.Name, row, tc.wantName)
		}
		other := tracker.Query(binding, "SELECT name FROM companies WHERE id = '"+tc.otherID+"'", 1000)
		if otherRows, _ := other["rows"].([]any); len(otherRows) != 0 {
			t.Fatalf("%s sees the other colony's row: %+v", binding.Name, otherRows)
		}
	}
}

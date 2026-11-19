package cli

// Queen-session tracker wiring: the catalogue advertises the queen-role tracker
// table, resolves the bound colony's tracker.db per call (the catalogue is
// built before a colony attaches), and refuses clearly when no colony is bound.
// The worker half of the loop is exercised on a real sqlite temp DB.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// fakeTrackerSpawner is the fake runtime the queen's tools resolve through: it
// carries a binding (the colony runtime exposes TrackerBinding) and never
// spawns anything.
type fakeTrackerSpawner struct {
	binding *tracker.Binding
}

func (f *fakeTrackerSpawner) SpawnBatchWithOptions(context.Context, []colony.BatchTask, colony.SpawnBatchOptions) ([]string, error) {
	return nil, nil
}

func (f *fakeTrackerSpawner) GetWorker(string) *colony.Worker { return nil }

func (f *fakeTrackerSpawner) MaxConcurrentWorkers() int { return 1 }

func (f *fakeTrackerSpawner) TrackerBinding() *tracker.Binding { return f.binding }

func trackerColonyBinding(t *testing.T, name string) *tracker.Binding {
	t.Helper()
	colonyDir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	binding, err := tracker.BindingForColonyDir(colonyDir, name)
	if err != nil || binding == nil {
		t.Fatalf("BindingForColonyDir: %+v, %v", binding, err)
	}
	return binding
}

func trackerExecute(t *testing.T, catalogue *sessionToolCatalogue, ctx context.Context, name string, input map[string]any) map[string]any {
	t.Helper()
	content, isError, err := catalogue.Execute(ctx, name, input)
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(content), &payload); jsonErr != nil {
		// Error messages (no-binding) are plain text, not JSON.
		if !isError {
			t.Fatalf("%s: content is not JSON (%q): %v", name, content, jsonErr)
		}
		return map[string]any{"success": false, "error": content}
	}
	if isError && payload["success"] == nil {
		payload["success"] = false
	}
	return payload
}

// Parity: core/tests/test_tracker_tools.py::test_register_tracker_tools_full_set
func TestSessionCatalogueAdvertisesQueenTrackerTools(t *testing.T) {
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	defined := map[string]map[string]any{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = def.Parameters
	}
	for _, name := range []string{"tracker_sql", "tracker_register_writable", "tracker_upsert", "tracker_query"} {
		params, ok := defined[name]
		if !ok {
			t.Fatalf("queen tracker tool %q missing from the catalogue", name)
		}
		if params["type"] != "object" {
			t.Fatalf("%s parameters = %#v", name, params)
		}
	}
	if catalogue.registry.HasTool("tracker_upsert") || catalogue.registry.HasTool("tracker_sql") {
		t.Fatal("tracker tools leaked into the integration registry")
	}

	worker := tracker.NewRegistry(nil)
	if err := tracker.RegisterTrackerTools(worker, "worker"); err != nil {
		t.Fatalf("worker registration: %v", err)
	}
	if names := worker.Names(); len(names) != 2 || names[0] != "tracker_query" || names[1] != "tracker_upsert" {
		t.Fatalf("worker tool table = %v, want query+upsert only", names)
	}
}

// Parity: core/tests/test_tracker_tools.py::test_no_colony_context_returns_clear_error, core/tests/test_tracker_tools.py::test_tracker_query_no_colony_context
func TestSessionCatalogueTrackerRejectsWithoutColony(t *testing.T) {
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	for name, input := range map[string]map[string]any{
		"tracker_sql":               {"sql": "SELECT 1"},
		"tracker_register_writable": {"table": "t", "write_columns": []any{"a"}, "key_columns": []any{"a"}},
		"tracker_upsert":            {"table": "t", "row": map[string]any{"a": 1}},
		"tracker_query":             {"sql": "SELECT 1"},
	} {
		payload := trackerExecute(t, catalogue, context.Background(), name, input)
		if payload["success"] != false {
			t.Fatalf("%s without a colony reported success: %+v", name, payload)
		}
		message, _ := payload["error"].(string)
		if !strings.Contains(message, "no colony context") {
			t.Fatalf("%s error = %q, want the no-colony-context guidance", name, message)
		}
	}
}

// Parity: core/tests/test_tracker_tools.py::test_register_happy_path_upsert, core/tests/test_tracker_tools.py::test_upsert_inserts_then_updates, core/tests/test_tracker_tools.py::test_tracker_query_select_works
func TestQueenDeclarationClosesWorkerUpsertLoop(t *testing.T) {
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	binding := trackerColonyBinding(t, "colonyA")
	ctx := colony.WithRunWorkerSpawner(context.Background(), &fakeTrackerSpawner{binding: binding})

	created := trackerExecute(t, catalogue, ctx, "tracker_sql", map[string]any{
		"sql": "CREATE TABLE companies (id TEXT PRIMARY KEY, name TEXT, status TEXT)",
	})
	if created["success"] != true {
		t.Fatalf("CREATE TABLE via tracker_sql: %+v", created)
	}
	registered := trackerExecute(t, catalogue, ctx, "tracker_register_writable", map[string]any{
		"table":         "companies",
		"write_columns": []any{"name", "status"},
		"key_columns":   []any{"id"},
	})
	if registered["success"] != true {
		t.Fatalf("tracker_register_writable: %+v", registered)
	}

	// The worker's table (role=worker) resolves the same binding through
	// input_data in production; here the fake runtime's binding is explicit.
	worker := tracker.NewRegistry(tracker.StaticResolver(binding))
	if err := tracker.RegisterTrackerTools(worker, "worker"); err != nil {
		t.Fatalf("worker registration: %v", err)
	}
	upserted, ok := worker.Execute("tracker_upsert", map[string]any{
		"table": "companies",
		"row":   map[string]any{"id": "acme", "name": "Acme", "status": "new"},
	})
	if !ok || upserted["success"] != true {
		t.Fatalf("worker tracker_upsert: %+v", upserted)
	}
	// Same key again: upsert updates in place instead of duplicating.
	upserted, ok = worker.Execute("tracker_upsert", map[string]any{
		"table": "companies",
		"row":   map[string]any{"id": "acme", "name": "Acme", "status": "checked"},
	})
	if !ok || upserted["success"] != true {
		t.Fatalf("worker tracker_upsert retry: %+v", upserted)
	}
	queried, ok := worker.Execute("tracker_query", map[string]any{
		"sql": "SELECT name, status FROM companies",
	})
	if !ok || queried["success"] != true {
		t.Fatalf("worker tracker_query: %+v", queried)
	}
	rows, _ := queried["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one upserted row", rows)
	}
	row, _ := rows[0].([]any)
	if len(row) != 2 || row[0] != "Acme" || row[1] != "checked" {
		t.Fatalf("row = %+v, want [Acme checked]", row)
	}

	// A worker may not call the queen-only pair.
	if _, ok := worker.Get("tracker_sql"); ok {
		t.Fatal("worker role exposes tracker_sql")
	}
	if _, ok := worker.Get("tracker_register_writable"); ok {
		t.Fatal("worker role exposes tracker_register_writable")
	}
}

// Parity-exempt: the queen's binding is resolved per call (upstream publishes
// it through the execution context of the forked session; the Go catalogue is
// built before the colony attaches). This guards that per-call resolution
// targets only the bound colony.
func TestQueenTrackerToolsUseTheBoundColonyPerCall(t *testing.T) {
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	bindingA := trackerColonyBinding(t, "colonyA")
	bindingB := trackerColonyBinding(t, "colonyB")
	ctxA := colony.WithRunWorkerSpawner(context.Background(), &fakeTrackerSpawner{binding: bindingA})
	ctxB := colony.WithRunWorkerSpawner(context.Background(), &fakeTrackerSpawner{binding: bindingB})

	for _, tc := range []struct {
		ctx     context.Context
		binding *tracker.Binding
	}{{ctxA, bindingA}, {ctxB, bindingB}} {
		created := trackerExecute(t, catalogue, tc.ctx, "tracker_sql", map[string]any{
			"sql": "CREATE TABLE companies (id TEXT PRIMARY KEY, name TEXT)",
		})
		if created["success"] != true {
			t.Fatalf("%s CREATE TABLE: %+v", tc.binding.Name, created)
		}
		inserted := trackerExecute(t, catalogue, tc.ctx, "tracker_sql", map[string]any{
			"sql": "INSERT INTO companies (id, name) VALUES ('" + tc.binding.Name + "', 'only-here')",
		})
		if inserted["success"] != true {
			t.Fatalf("%s INSERT: %+v", tc.binding.Name, inserted)
		}
	}

	for _, tc := range []struct {
		ctx     context.Context
		binding *tracker.Binding
	}{{ctxA, bindingA}, {ctxB, bindingB}} {
		queried := trackerExecute(t, catalogue, tc.ctx, "tracker_query", map[string]any{
			"sql": "SELECT name FROM companies",
		})
		if queried["success"] != true {
			t.Fatalf("%s query: %+v", tc.binding.Name, queried)
		}
		rows, _ := queried["rows"].([]any)
		if len(rows) != 1 {
			t.Fatalf("%s rows = %+v, want only its own row", tc.binding.Name, rows)
		}
		row, _ := rows[0].([]any)
		if len(row) != 1 || row[0] != "only-here" {
			t.Fatalf("%s row = %+v", tc.binding.Name, row)
		}
	}
}

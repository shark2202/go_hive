package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func testBinding(t *testing.T) *Binding {
	t.Helper()
	colonyDir, dbPath := newColony(t)
	return &Binding{Name: filepath.Base(colonyDir), Dir: colonyDir, TrackerDB: dbPath}
}

func toolSet(t *testing.T, b *Binding) map[string]Tool {
	t.Helper()
	tools := map[string]Tool{}
	for _, tool := range BuildTrackerTools(func() (*Binding, error) { return b, nil }) {
		tools[tool.Name] = tool
	}
	return tools
}

func call(t *testing.T, tools map[string]Tool, name string, inputs map[string]any) map[string]any {
	t.Helper()
	tool, ok := tools[name]
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	return tool.Execute(inputs)
}

func requireFailure(t *testing.T, res map[string]any, wants ...string) {
	t.Helper()
	if res["success"] != false {
		t.Fatalf("expected failure, got %+v", res)
	}
	msg, _ := res["error"].(string)
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}

func requireSuccess(t *testing.T, res map[string]any) {
	t.Helper()
	if res["success"] != true {
		t.Fatalf("expected success, got %+v", res)
	}
}

func cellEq(got, want any) bool {
	switch w := want.(type) {
	case int64:
		return numberEq(got, w)
	case int:
		return numberEq(got, int64(w))
	default:
		return got == want
	}
}

func requireRows(t *testing.T, res map[string]any, want [][]any) {
	t.Helper()
	rows, ok := res["rows"].([]any)
	if !ok {
		t.Fatalf("no rows in %+v", res)
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
	for i, rowAny := range rows {
		row, ok := rowAny.([]any)
		if !ok {
			t.Fatalf("row %d is %T", i, rowAny)
		}
		if len(row) != len(want[i]) {
			t.Fatalf("row %d = %+v, want %+v", i, row, want[i])
		}
		for j := range row {
			if !cellEq(row[j], want[i][j]) {
				t.Fatalf("cell [%d][%d] = %#v, want %#v", i, j, row[j], want[i][j])
			}
		}
	}
}

func execTool(t *testing.T, tools map[string]Tool, sqlText string) map[string]any {
	t.Helper()
	res := call(t, tools, "tracker_sql", map[string]any{"sql": sqlText})
	requireSuccess(t, res)
	return res
}

func register(t *testing.T, tools map[string]Tool, table string, writes, keys []string) map[string]any {
	t.Helper()
	inputs := map[string]any{"table": table}
	if writes != nil {
		inputs["write_columns"] = stringSliceAny(writes)
	}
	if keys != nil {
		inputs["key_columns"] = stringSliceAny(keys)
	}
	return call(t, tools, "tracker_register_writable", inputs)
}

// Parity: core/tests/test_tracker_tools.py::test_no_colony_context_returns_clear_error, core/tests/test_tracker_tools.py::test_tracker_query_no_colony_context
func TestToolsWithoutColonyContext(t *testing.T) {
	tools := map[string]Tool{}
	for _, tool := range BuildTrackerTools(func() (*Binding, error) { return nil, nil }) {
		tools[tool.Name] = tool
	}
	for name, inputs := range map[string]map[string]any{
		"tracker_sql":               {"sql": "SELECT 1"},
		"tracker_upsert":            {"table": "t", "row": map[string]any{"a": 1}},
		"tracker_query":             {"sql": "SELECT 1"},
		"tracker_register_writable": {"table": "t", "write_columns": []any{"a"}, "key_columns": []any{"a"}},
	} {
		res := call(t, tools, name, inputs)
		requireFailure(t, res, "no colony context")
	}
}

// Parity: core/tests/test_tracker_tools.py::test_resolves_via_colony_id_when_path_missing
func TestResolveBindingFromColonyID(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "c1")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := ResolveBinding(ExecutionContext{ColoniesRoot: root, ColonyID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.Name != "c1" || b.Dir != colonyDir {
		t.Fatalf("binding = %+v", b)
	}
	if _, err := os.Stat(b.TrackerDB); err != nil {
		t.Fatalf("tracker not ensured: %v", err)
	}

	tools := toolSet(t, b)
	res := call(t, tools, "tracker_sql", map[string]any{"sql": "SELECT 1 AS x"})
	requireSuccess(t, res)
	columns := res["columns"].([]string)
	if len(columns) != 1 || columns[0] != "x" {
		t.Fatalf("columns = %v", columns)
	}
	requireRows(t, res, [][]any{{int64(1)}})

	if stray, err := BindingFromColonyID(root, "nope"); err != nil || stray != nil {
		t.Fatalf("stray colony resolved: %+v %v", stray, err)
	}
	if _, err := os.Stat(filepath.Join(root, "nope")); !os.IsNotExist(err) {
		t.Fatal("shadow colony directory was created")
	}
}

// Parity: core/tests/test_tracker_tools.py::test_tracker_sql_create_insert_select
func TestBindingFromTrackerDBPath(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "legacy")
	legacyDB := filepath.Join(colonyDir, "data", "tracker.db")
	b, err := BindingFromTrackerDBPath(legacyDB, "")
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.Dir != colonyDir || b.Name != "legacy" {
		t.Fatalf("binding = %+v", b)
	}
	trackerDir, err := filepath.EvalSymlinks(filepath.Join(colonyDir, "tracker"))
	if err != nil {
		t.Fatal(err)
	}
	if b.TrackerDB != filepath.Join(trackerDir, "tracker.db") {
		t.Fatalf("tracker db = %s", b.TrackerDB)
	}

	named, err := BindingFromTrackerDBPath(legacyDB, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if named.Name != "explicit" {
		t.Fatalf("colony id not preferred: %+v", named)
	}
}

// Parity: core/tests/test_tracker_tools.py::test_tracker_sql_create_insert_select
func TestTrackerSQLBasics(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)

	res := execTool(t, tools, "CREATE TABLE competitors (name TEXT, url TEXT)")
	if res["kind"] != "exec" {
		t.Fatalf("create = %+v", res)
	}
	res = execTool(t, tools, "INSERT INTO competitors (name, url) VALUES ('acme', 'https://acme.test')")
	if !numberEq(res["rowcount"], 1) {
		t.Fatalf("insert = %+v", res)
	}
	res = call(t, tools, "tracker_sql", map[string]any{"sql": "SELECT name, url FROM competitors"})
	requireSuccess(t, res)
	requireRows(t, res, [][]any{{"acme", "https://acme.test"}})
}

// Parity: core/tests/test_tracker_tools.py::test_tracker_sql_denylist_passthrough, core/tests/test_tracker_tools.py::test_tracker_sql_sqlite_error_surfaces, core/tests/test_tracker_tools.py::test_tracker_sql_empty_input
func TestTrackerSQLFailures(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)

	requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": "ATTACH DATABASE 'x' AS y"}), "denied")
	requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": "SELECT * FROM no_such_table"}), "sqlite error")
	for _, empty := range []string{"", "   "} {
		requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": empty}), "'sql' is required")
	}
	requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": "SELECT 1", "scope": "global"}), "cloud session")
}

// Parity: core/tests/test_tracker_tools.py::test_register_happy_path_upsert
func TestRegisterWritableSuccess(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE leads (id TEXT PRIMARY KEY, title TEXT, owner TEXT)")

	res := register(t, tools, "leads", []string{"title", "owner"}, []string{"id"})
	requireSuccess(t, res)
	if res["mode"] != "upsert" {
		t.Fatalf("mode = %v", res["mode"])
	}
	writes := res["write_columns"].([]any)
	if len(writes) != 2 || writes[0] != "title" || writes[1] != "owner" {
		t.Fatalf("write_columns = %v", writes)
	}
	if msg, _ := res["message"].(string); !strings.Contains(msg, "leads") {
		t.Fatalf("message = %q", msg)
	}

	q := call(t, tools, "tracker_query", map[string]any{
		"sql": "SELECT write_columns, key_columns, mode FROM _tracker_registry WHERE table_name='leads'",
	})
	requireSuccess(t, q)
	requireRows(t, q, [][]any{{`["title", "owner"]`, `["id"]`, "upsert"}})
}

// Parity: core/tests/test_tracker_tools.py::test_register_rejects_missing_key_columns, core/tests/test_tracker_tools.py::test_register_rejects_protected_table, core/tests/test_tracker_tools.py::test_register_rejects_missing_table, core/tests/test_tracker_tools.py::test_register_rejects_missing_columns, core/tests/test_tracker_tools.py::test_register_rejects_upsert_without_unique_index, core/tests/test_tracker_tools.py::test_register_accepts_unique_index_as_key
func TestRegisterWritableValidation(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE leads (id TEXT PRIMARY KEY, title TEXT, owner TEXT)")
	execTool(t, tools, "CREATE TABLE notes (id TEXT, body TEXT)")

	requireFailure(t, register(t, tools, "leads", []string{"title"}, nil), "key_columns is required")
	requireFailure(t, register(t, tools, "leads", []string{"title"}, []string{}), "key_columns is required")
	requireFailure(t, register(t, tools, "", []string{"title"}, []string{"id"}), "table is required")
	requireFailure(t, register(t, tools, "_tracker_meta", []string{"title"}, []string{"id"}), "protected")
	requireFailure(t, register(t, tools, "ghosts", []string{"title"}, []string{"id"}), "does not exist")
	requireFailure(t, register(t, tools, "leads", []string{"nonexistent"}, []string{"id"}), "write_columns not found", "nonexistent")
	requireFailure(t, register(t, tools, "leads", []string{"title"}, []string{"nonexistent"}), "key_columns not found", "nonexistent")

	requireFailure(t, register(t, tools, "notes", []string{"body"}, []string{"id"}), "UNIQUE")
	execTool(t, tools, "CREATE UNIQUE INDEX notes_id ON notes(id)")
	requireSuccess(t, register(t, tools, "notes", []string{"body"}, []string{"id"}))
}

// Parity: core/tests/test_tracker_tools.py::test_register_re_register_replaces_spec
func TestRegisterWritableReplacesSpec(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE t_reg (a TEXT PRIMARY KEY, b TEXT, c TEXT)")

	requireSuccess(t, register(t, tools, "t_reg", []string{"b"}, []string{"a"}))
	requireSuccess(t, register(t, tools, "t_reg", []string{"b", "c"}, []string{"a"}))

	q := call(t, tools, "tracker_query", map[string]any{
		"sql": "SELECT write_columns FROM _tracker_registry WHERE table_name='t_reg'",
	})
	requireSuccess(t, q)
	row := q["rows"].([]any)[0].([]any)
	var cols []string
	if err := json.Unmarshal([]byte(row[0].(string)), &cols); err != nil {
		t.Fatal(err)
	}
	sort.Strings(cols)
	if len(cols) != 2 || cols[0] != "b" || cols[1] != "c" {
		t.Fatalf("stored spec = %v", cols)
	}
}

// Parity: core/tests/test_tracker_tools.py::test_upsert_inserts_then_updates
func TestUpsertInsertThenUpdate(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE leads (id TEXT PRIMARY KEY, name TEXT, funding INTEGER)")
	requireSuccess(t, register(t, tools, "leads", []string{"name", "funding"}, []string{"id"}))

	res := call(t, tools, "tracker_upsert", map[string]any{
		"table": "leads",
		"row":   map[string]any{"id": "a", "name": "Acme", "funding": 1000},
	})
	requireSuccess(t, res)
	if res["mode"] != "upsert" || !numberEq(res["rowcount"], 1) {
		t.Fatalf("insert = %+v", res)
	}

	res = call(t, tools, "tracker_upsert", map[string]any{
		"table": "leads",
		"row":   map[string]any{"id": "a", "funding": 2000},
	})
	requireSuccess(t, res)
	if !numberEq(res["rowcount"], 1) {
		t.Fatalf("update = %+v", res)
	}

	q := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT name, funding FROM leads"})
	requireSuccess(t, q)
	requireRows(t, q, [][]any{{"Acme", int64(2000)}})
}

// Parity: core/tests/test_tracker_tools.py::test_upsert_rejects_unregistered_table, core/tests/test_tracker_tools.py::test_upsert_rejects_protected_namespace, core/tests/test_tracker_tools.py::test_upsert_rejects_columns_outside_writelist, core/tests/test_tracker_tools.py::test_upsert_requires_key_columns_in_upsert_mode
func TestUpsertValidation(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE leads (id TEXT PRIMARY KEY, name TEXT, funding INTEGER)")
	requireSuccess(t, register(t, tools, "leads", []string{"name", "funding"}, []string{"id"}))

	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{"table": "ghosts", "row": map[string]any{"id": "a"}}), "not registered")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{"table": "_tracker_meta", "row": map[string]any{"k": 1}}), "framework-owned")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{"table": "", "row": map[string]any{"id": "a"}}), "table is required")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{"table": "leads", "row": map[string]any{}}), "non-empty")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{
		"table": "leads", "row": map[string]any{"id": "a", "secret": "x"},
	}), "columns not in write/key list", "secret")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{
		"table": "leads", "row": map[string]any{"name": "Acme"},
	}), "key_columns", "id")
}

// Parity: core/tests/test_tracker_tools.py::test_upsert_encodes_complex_values_as_json
func TestUpsertJSONEncodesComplexValues(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE tags (id TEXT PRIMARY KEY, tags TEXT, meta TEXT)")
	requireSuccess(t, register(t, tools, "tags", []string{"tags", "meta"}, []string{"id"}))

	res := call(t, tools, "tracker_upsert", map[string]any{
		"table": "tags",
		"row": map[string]any{
			"id":   "a",
			"tags": []any{"x", "y"},
			"meta": map[string]any{"ok": true, "flag": false},
		},
	})
	requireSuccess(t, res)

	q := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT tags, meta FROM tags"})
	requireSuccess(t, q)
	row := q["rows"].([]any)[0].([]any)
	if row[0] != `["x", "y"]` {
		t.Fatalf("tags = %q", row[0])
	}
	if row[1] != `{"flag": false, "ok": true}` {
		t.Fatalf("meta = %q", row[1])
	}
}

// Parity: core/tests/test_tracker_tools.py::test_tracker_query_select_works, core/tests/test_tracker_tools.py::test_tracker_query_with_cte_allowed, core/tests/test_tracker_tools.py::test_tracker_query_rejects_writes, core/tests/test_tracker_tools.py::test_tracker_query_rejects_multi_statement, core/tests/test_tracker_tools.py::test_tracker_query_denylist_still_applies
func TestTrackerQuery(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE q (n INTEGER, s TEXT)")
	execTool(t, tools, "INSERT INTO q VALUES (1, 'x'), (2, 'y')")

	res := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT n, s FROM q ORDER BY n"})
	requireSuccess(t, res)
	requireRows(t, res, [][]any{{int64(1), "x"}, {int64(2), "y"}})

	res = call(t, tools, "tracker_query", map[string]any{
		"sql": "WITH x AS (SELECT 2 AS n UNION ALL SELECT 3) SELECT n FROM x ORDER BY n",
	})
	requireSuccess(t, res)
	requireRows(t, res, [][]any{{int64(2)}, {int64(3)}})

	requireFailure(t, call(t, tools, "tracker_query", map[string]any{"sql": "SELECT 1; SELECT 2"}), "ONE statement")
	for _, sqlText := range []string{
		"INSERT INTO q VALUES (3, 'z')",
		"UPDATE q SET s = 'z'",
		"DELETE FROM q",
		"CREATE TABLE z (id INTEGER)",
		"DROP TABLE q",
		"ALTER TABLE q ADD COLUMN t TEXT",
		"REPLACE INTO q VALUES (3, 'z')",
		"WITH x AS (SELECT 1) DELETE FROM q",
		"/* hi */ DELETE FROM q",
		"ATTACH DATABASE 'x' AS y",
	} {
		msg, _ := call(t, tools, "tracker_query", map[string]any{"sql": sqlText})["error"].(string)
		if !strings.Contains(msg, "SELECT-only") && !strings.Contains(msg, "denied") && !strings.Contains(msg, "rejected") {
			t.Errorf("%q not rejected as a write: %q", sqlText, msg)
		}
	}
	requireFailure(t, call(t, tools, "tracker_query", map[string]any{"sql": ""}), "'sql' is required")
	requireFailure(t, call(t, tools, "tracker_query", map[string]any{"sql": "-- nothing\n"}), "no statements")
}

// Parity: core/tests/test_tracker_tools.py::test_queen_only_tracker_tools_set, core/tests/test_tracker_tools.py::test_register_tracker_tools_full_set, core/tests/test_tracker_tools.py::test_register_tracker_tools_worker_role_skips_queen_only, core/tests/test_tracker_tools.py::test_register_tracker_tools_invalid_role
func TestTrackerToolRoles(t *testing.T) {
	wantQueenOnly := map[string]bool{"tracker_sql": true, "tracker_register_writable": true}
	if len(QueenOnlyTrackerTools) != len(wantQueenOnly) {
		t.Fatalf("queen-only set = %v", QueenOnlyTrackerTools)
	}
	for name := range wantQueenOnly {
		if !QueenOnlyTrackerTools[name] {
			t.Fatalf("%s missing from queen-only set", name)
		}
	}

	b := testBinding(t)
	queen := NewRegistry(func() (*Binding, error) { return b, nil })
	if err := RegisterTrackerTools(queen, "queen"); err != nil {
		t.Fatal(err)
	}
	if names := queen.Names(); len(names) != 4 {
		t.Fatalf("queen tools = %v", names)
	}

	worker := NewRegistry(func() (*Binding, error) { return b, nil })
	if err := RegisterTrackerTools(worker, "worker"); err != nil {
		t.Fatal(err)
	}
	names := worker.Names()
	if len(names) != 2 || names[0] != "tracker_query" || names[1] != "tracker_upsert" {
		t.Fatalf("worker tools = %v", names)
	}
	for _, hidden := range []string{"tracker_sql", "tracker_register_writable"} {
		if _, ok := worker.Get(hidden); ok {
			t.Fatalf("worker sees queen-only tool %s", hidden)
		}
	}

	upsertTool, _ := worker.Get("tracker_upsert")
	res := upsertTool.Execute(map[string]any{
		"table": "t", "row": map[string]any{"k": "a"}, "scope": "global",
	})
	requireFailure(t, res, "queen-only")

	if err := RegisterTrackerTools(NewRegistry(nil), "bogus"); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("invalid role accepted: %v", err)
	}
}

// Parity-exempt: the global-scope short-circuit for queen-only tracker tools has
func TestQueenGlobalScope(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)

	requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": "SELECT 1", "scope": "global"}), "cloud session")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{
		"table": "t", "row": map[string]any{"k": 1}, "scope": "global",
	}), "cloud session")
	requireFailure(t, call(t, tools, "tracker_query", map[string]any{"sql": "SELECT 1", "scope": "global"}), "cloud session")

	res := call(t, tools, "tracker_register_writable", map[string]any{
		"table": "t", "write_columns": []any{"k"}, "key_columns": []any{"k"}, "scope": "global",
	})
	requireSuccess(t, res)
	if msg, _ := res["message"].(string); !strings.Contains(msg, "no registration is needed") {
		t.Fatalf("global register message = %q", msg)
	}
}

// Parity: core/tests/test_tracker_tools.py::test_change_log_records_worker_upserts
func TestChangeLogWorkerUpserts(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE c (k TEXT PRIMARY KEY, v TEXT)")
	requireSuccess(t, register(t, tools, "c", []string{"v"}, []string{"k"}))

	requireSuccess(t, call(t, tools, "tracker_upsert", map[string]any{"table": "c", "row": map[string]any{"k": "a", "v": "1"}}))
	requireSuccess(t, call(t, tools, "tracker_upsert", map[string]any{"table": "c", "row": map[string]any{"k": "a", "v": "2"}}))

	q := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT table_name, pk, op FROM _tracker_changes ORDER BY id"})
	requireSuccess(t, q)
	requireRows(t, q, [][]any{{"c", `{"k":"a"}`, "insert"}, {"c", `{"k":"a"}`, "update"}})
}

// Parity: core/tests/test_tracker_tools.py::test_change_log_records_queen_raw_sql
func TestChangeLogQueenRawSQL(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE c (k TEXT PRIMARY KEY, v TEXT)")
	requireSuccess(t, register(t, tools, "c", []string{"v"}, []string{"k"}))
	execTool(t, tools, "INSERT INTO c (k, v) VALUES ('x', '1')")
	execTool(t, tools, "UPDATE c SET v = '2' WHERE k = 'x'")

	q := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT pk, op FROM _tracker_changes ORDER BY id"})
	requireSuccess(t, q)
	requireRows(t, q, [][]any{{`{"k":"x"}`, "insert"}, {`{"k":"x"}`, "update"}})
}

// Parity: core/tests/test_tracker_tools.py::test_change_log_triggers_survive_queen_drop_attempt
func TestChangeTriggersCannotBeDropped(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE c (k TEXT PRIMARY KEY, v TEXT)")
	requireSuccess(t, register(t, tools, "c", []string{"v"}, []string{"k"}))
	requireSuccess(t, call(t, tools, "tracker_upsert", map[string]any{"table": "c", "row": map[string]any{"k": "a", "v": "1"}}))

	insTrigger, _ := changeTriggerNames("c")
	requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": "DROP TRIGGER " + insTrigger}), "framework table")

	q := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT COUNT(*) FROM _tracker_changes"})
	requireSuccess(t, q)
	requireRows(t, q, [][]any{{int64(1)}})
}

// Parity: core/tests/test_tracker_tools.py::test_change_log_unregistered_table_not_logged
func TestUnregisteredTableNotLogged(t *testing.T) {
	b := testBinding(t)
	tools := toolSet(t, b)
	execTool(t, tools, "CREATE TABLE unreg (k TEXT)")
	execTool(t, tools, "INSERT INTO unreg (k) VALUES ('a')")

	q := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT COUNT(*) FROM _tracker_changes"})
	requireSuccess(t, q)
	requireRows(t, q, [][]any{{int64(0)}})
}

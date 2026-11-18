package server

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tasks"
	"github.com/aden-hive/hive-go/internal/tracker"
)

func colonyDataTestDeps(t *testing.T) (ColonyWorkerDeps, hivehome.Home) {
	t.Helper()
	home := serverTestHome(t)
	return ColonyWorkerDeps{
		Manager: NewSessionManager(),
		Home:    home,
		Tasks:   tasks.NewTaskStore(home.Root),
	}, home
}

func resetTrackerSnapshotCache() {
	trackerSnapshotCacheMu.Lock()
	trackerSnapshotCache = map[string]trackerSnapshotEntry{}
	trackerSnapshotCacheMu.Unlock()
}

func seedColonyTracker(t *testing.T, home hivehome.Home, colonyID string) string {
	t.Helper()
	colonyDir := filepath.Join(home.ColoniesDir(), colonyID)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony dir: %v", err)
	}
	dbPath, err := tracker.EnsureTrackerDB(colonyDir)
	if err != nil {
		t.Fatalf("ensure tracker db: %v", err)
	}
	return dbPath
}

func execTrackerSQL(t *testing.T, dbPath string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open tracker db: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
}

// Parity: framework/server/tests/test_colony_data_timeout.py::test_table_rows_times_out_to_503
func TestTableRowsTimesOutTo503(t *testing.T) {
	deps, _ := colonyDataTestDeps(t)
	deps.DataReadTimeout = 20 * time.Millisecond
	deps.ResolveTrackerDB = func(string) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "", nil
	}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/tables/t1/rows", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["error"] != "colony_data_timeout" || body["colony_id"] != "c1" {
		t.Errorf("body = %v, want colony_data_timeout/c1", body)
	}
}

// Parity: framework/server/tests/test_colony_data_timeout.py::test_list_tables_times_out_to_503
func TestListTablesTimesOutTo503(t *testing.T) {
	deps, _ := colonyDataTestDeps(t)
	deps.DataReadTimeout = 20 * time.Millisecond
	deps.ResolveTrackerDB = func(string) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "", nil
	}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/tables", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", recorder.Code, string(raw))
	}
}

// Parity: framework/server/tests/test_colony_data_timeout.py::test_list_tables_ok_when_no_db
func TestListTablesOKWhenNoDB(t *testing.T) {
	deps, _ := colonyDataTestDeps(t)
	deps.ResolveTrackerDB = func(string) (string, error) { return "", nil }

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/tables", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body struct {
		Tables []any `json:"tables"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body.Tables == nil || len(body.Tables) != 0 {
		t.Errorf("tables = %v, want []", body.Tables)
	}
}

// Parity-exempt: no upstream test drives the tracker overview read; pins the
// columns/primary_key/row_count contract of GET /data/tables.
func TestListTablesOverview(t *testing.T) {
	deps, home := colonyDataTestDeps(t)
	dbPath := seedColonyTracker(t, home, "c1")
	execTrackerSQL(t, dbPath,
		`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT DEFAULT 'x')`,
		`INSERT INTO notes (id, title) VALUES (1, 'one'), (2, 'two')`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/tables", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body struct {
		Tables []struct {
			Name       string   `json:"name"`
			RowCount   int      `json:"row_count"`
			PrimaryKey []string `json:"primary_key"`
			Columns    []struct {
				Name      string `json:"name"`
				Type      string `json:"type"`
				NotNull   bool   `json:"notnull"`
				PK        int    `json:"pk"`
				DfltValue any    `json:"dflt_value"`
			} `json:"columns"`
		} `json:"tables"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if len(body.Tables) != 1 || body.Tables[0].Name != "notes" {
		t.Fatalf("tables = %+v, want exactly [notes]", body.Tables)
	}
	notes := body.Tables[0]
	if notes.RowCount != 2 {
		t.Errorf("row_count = %d, want 2", notes.RowCount)
	}
	if len(notes.PrimaryKey) != 1 || notes.PrimaryKey[0] != "id" {
		t.Errorf("primary_key = %v, want [id]", notes.PrimaryKey)
	}
	if len(notes.Columns) != 3 {
		t.Fatalf("columns = %d, want 3", len(notes.Columns))
	}
	if notes.Columns[0].Name != "id" || notes.Columns[0].PK != 1 || notes.Columns[0].Type != "INTEGER" {
		t.Errorf("id column = %+v, want INTEGER pk", notes.Columns[0])
	}
	if !notes.Columns[1].NotNull {
		t.Errorf("title notnull = false, want true")
	}
	if notes.Columns[2].DfltValue != "'x'" {
		t.Errorf("body dflt_value = %v, want 'x'", notes.Columns[2].DfltValue)
	}
}

// Parity-exempt: no upstream test drives row pagination; pins limit/offset
// clamping, ORDER BY and the grid envelope of GET /data/tables/{table}/rows.
func TestTableRowsPaginationAndOrder(t *testing.T) {
	deps, home := colonyDataTestDeps(t)
	dbPath := seedColonyTracker(t, home, "c1")
	execTrackerSQL(t, dbPath,
		`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL)`,
		`INSERT INTO notes (id, title) VALUES (1, 'one'), (2, 'two'), (3, 'three')`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet,
		"/api/colonies/c1/data/tables/notes/rows?limit=2&offset=1&order_by=id&order_dir=desc", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body struct {
		Table      string           `json:"table"`
		PrimaryKey []string         `json:"primary_key"`
		Rows       []map[string]any `json:"rows"`
		Total      int              `json:"total"`
		Limit      int              `json:"limit"`
		Offset     int              `json:"offset"`
		Columns    []map[string]any `json:"columns"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body.Table != "notes" || body.Total != 3 || body.Limit != 2 || body.Offset != 1 {
		t.Fatalf("envelope = %+v, want notes/3/2/1", body)
	}
	if len(body.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(body.Rows))
	}
	if body.Rows[0]["id"] != float64(2) || body.Rows[1]["id"] != float64(1) {
		t.Errorf("row order = %v, want ids [2 1]", body.Rows)
	}
	if len(body.Columns) != 2 {
		t.Errorf("columns = %d, want 2", len(body.Columns))
	}
}

// Parity-exempt: no upstream test drives the unknown-table branch; pins the
// 400 {"error": "unknown table: ..."} envelope.
func TestTableRowsUnknownTable400(t *testing.T) {
	deps, home := colonyDataTestDeps(t)
	dbPath := seedColonyTracker(t, home, "c1")
	execTrackerSQL(t, dbPath, `CREATE TABLE notes (id INTEGER PRIMARY KEY)`, `PRAGMA wal_checkpoint(TRUNCATE)`)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/tables/ghost/rows", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["error"] != "unknown table: ghost" {
		t.Errorf("error = %v, want unknown table: ghost", body["error"])
	}
}

// Parity-exempt: no upstream test drives the query-param validation branch;
// pins the 400 {"error": "invalid limit/offset"} envelope.
func TestTableRowsInvalidQuery400(t *testing.T) {
	deps, _ := colonyDataTestDeps(t)

	for _, target := range []string{
		"/api/colonies/c1/data/tables/notes/rows?limit=abc",
		"/api/colonies/c1/data/tables/notes/rows?limit=",
		"/api/colonies/c1/data/tables/notes/rows?offset=nope",
	} {
		recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, target, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", target, recorder.Code, string(raw))
		}
		var body map[string]any
		decodeColonyWorkerBody(t, raw, &body)
		if body["error"] != "invalid limit/offset" {
			t.Errorf("%s: error = %v, want invalid limit/offset", target, body["error"])
		}
	}
}

// Parity-exempt: no upstream test drives the missing-tracker branch; pins the
// 404 {"error": "no tracker.db"} envelope.
func TestTableRowsNoTracker404(t *testing.T) {
	deps, _ := colonyDataTestDeps(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/tables/notes/rows", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["error"] != "no tracker.db" {
		t.Errorf("error = %v, want no tracker.db", body["error"])
	}
}

// Parity-exempt: no upstream test drives PATCH /data/tables/{table}/rows; pins
// the validation ladder and the {"updated": n} success envelope.
func TestUpdateRowValidations(t *testing.T) {
	deps, home := colonyDataTestDeps(t)
	dbPath := seedColonyTracker(t, home, "c1")
	execTrackerSQL(t, dbPath,
		`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL)`,
		`CREATE TABLE nopk (label TEXT)`,
		`INSERT INTO notes (id, title) VALUES (1, 'one')`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	)

	ok, raw := colonyWorkerDo(t, deps, http.MethodPatch, "/api/colonies/c1/data/tables/notes/rows",
		map[string]any{"pk": map[string]any{"id": 1}, "updates": map[string]any{"title": "edited"}})
	if ok.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", ok.Code, string(raw))
	}
	var updated map[string]any
	decodeColonyWorkerBody(t, raw, &updated)
	if updated["updated"] != float64(1) {
		t.Errorf("updated = %v, want 1", updated["updated"])
	}

	cases := []struct {
		name  string
		table string
		body  map[string]any
		want  string
	}{
		{
			name:  "no updates",
			table: "notes",
			body:  map[string]any{"pk": map[string]any{"id": 1}, "updates": map[string]any{}},
			want:  "no updates provided",
		},
		{
			name:  "unknown table",
			table: "ghost",
			body:  map[string]any{"pk": map[string]any{"id": 1}, "updates": map[string]any{"title": "x"}},
			want:  "unknown table: ghost",
		},
		{
			name:  "missing pk column",
			table: "notes",
			body:  map[string]any{"pk": map[string]any{}, "updates": map[string]any{"title": "x"}},
			want:  "missing primary key columns: ['id']",
		},
		{
			name:  "unknown column",
			table: "notes",
			body:  map[string]any{"pk": map[string]any{"id": 1}, "updates": map[string]any{"nope": "x"}},
			want:  "unknown columns: ['nope']",
		},
		{
			name:  "primary key edit",
			table: "notes",
			body:  map[string]any{"pk": map[string]any{"id": 1}, "updates": map[string]any{"id": 2}},
			want:  "cannot edit primary key columns: ['id']",
		},
		{
			name:  "table without primary key",
			table: "nopk",
			body:  map[string]any{"pk": map[string]any{"label": "a"}, "updates": map[string]any{"label": "b"}},
			want:  "table 'nopk' has no primary key; cannot edit by row",
		},
	}
	for _, testCase := range cases {
		recorder, payloadRaw := colonyWorkerDo(t, deps, http.MethodPatch, "/api/colonies/c1/data/tables/"+testCase.table+"/rows", testCase.body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", testCase.name, recorder.Code, string(payloadRaw))
			continue
		}
		var body map[string]any
		decodeColonyWorkerBody(t, payloadRaw, &body)
		if body["error"] != testCase.want {
			t.Errorf("%s: error = %v, want %s", testCase.name, body["error"], testCase.want)
		}
	}
}

// Parity-exempt: no upstream test drives the PATCH body validation branch; pins
// the 400 envelopes for malformed JSON and non-object pk/updates.
func TestUpdateRowInvalidBody400(t *testing.T) {
	deps, home := colonyDataTestDeps(t)
	dbPath := seedColonyTracker(t, home, "c1")
	execTrackerSQL(t, dbPath, `CREATE TABLE notes (id INTEGER PRIMARY KEY)`, `PRAGMA wal_checkpoint(TRUNCATE)`)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodPatch, "/api/colonies/c1/data/tables/notes/rows", map[string]any{
		"pk":      []any{1},
		"updates": map[string]any{"title": "x"},
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["error"] != "pk and updates must be objects" {
		t.Errorf("error = %v, want pk and updates must be objects", body["error"])
	}
}

// Parity-exempt: no upstream test drives GET /data/changes; pins the init
// call, the per-table dedupe (first op wins) and the cursor shape.
func TestChangesInitAndRows(t *testing.T) {
	deps, home := colonyDataTestDeps(t)
	dbPath := seedColonyTracker(t, home, "c1")
	execTrackerSQL(t, dbPath, `CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL)`)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open tracker db: %v", err)
	}
	if err := tracker.InstallChangeTriggers(db, "notes", []string{"id"}); err != nil {
		t.Fatalf("install change triggers: %v", err)
	}
	_ = db.Close()
	execTrackerSQL(t, dbPath,
		`INSERT INTO notes (id, title) VALUES (1, 'one')`,
		`UPDATE notes SET title = 'uno' WHERE id = 1`,
		`INSERT INTO notes (id, title) VALUES (2, 'two')`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	)
	resetTrackerSnapshotCache()

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/changes?since=-1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var init struct {
		Cursor    int64          `json:"cursor"`
		Covered   []string       `json:"covered"`
		Truncated bool           `json:"truncated"`
		Tables    map[string]any `json:"tables"`
	}
	decodeColonyWorkerBody(t, raw, &init)
	if init.Cursor != 3 {
		t.Errorf("cursor = %d, want 3", init.Cursor)
	}
	if len(init.Covered) != 1 || init.Covered[0] != "notes" {
		t.Errorf("covered = %v, want [notes]", init.Covered)
	}
	if len(init.Tables) != 0 {
		t.Errorf("tables = %v, want {} on the init call", init.Tables)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodGet, "/api/colonies/c1/data/changes?since=0", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body struct {
		Cursor    int64    `json:"cursor"`
		Covered   []string `json:"covered"`
		Truncated bool     `json:"truncated"`
		Tables    map[string]struct {
			Count int `json:"count"`
			Rows  []struct {
				PK map[string]any `json:"pk"`
				Op string         `json:"op"`
			} `json:"rows"`
		} `json:"tables"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body.Cursor != 3 || body.Truncated {
		t.Errorf("cursor/truncated = %d/%v, want 3/false", body.Cursor, body.Truncated)
	}
	notes, ok := body.Tables["notes"]
	if !ok {
		t.Fatalf("tables = %v, want a notes bucket", body.Tables)
	}
	if notes.Count != 2 || len(notes.Rows) != 2 {
		t.Fatalf("notes = %+v, want 2 deduped rows", notes)
	}
	ops := map[float64]string{}
	for _, row := range notes.Rows {
		id, _ := row.PK["id"].(float64)
		ops[id] = row.Op
	}
	if ops[1] != "insert" || ops[2] != "insert" {
		t.Errorf("ops = %v, want the first op per pk (insert)", ops)
	}
}

// Parity-exempt: no upstream test drives an invalid since cursor; pins the
// 400 {"error": "invalid since"} envelope.
func TestChangesInvalidSince400(t *testing.T) {
	deps, _ := colonyDataTestDeps(t)

	for _, target := range []string{
		"/api/colonies/c1/data/changes?since=nope",
		"/api/colonies/c1/data/changes?since=",
	} {
		recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, target, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", target, recorder.Code, string(raw))
		}
		var body map[string]any
		decodeColonyWorkerBody(t, raw, &body)
		if body["error"] != "invalid since" {
			t.Errorf("%s: error = %v, want invalid since", target, body["error"])
		}
	}
}

// Parity-exempt: no upstream test drives _resolve_tracker_db_by_name; pins the
// colony-name validation and the missing-directory/missing-DB branches.
func TestResolveTrackerDBByName(t *testing.T) {
	home := serverTestHome(t)

	if path, err := resolveTrackerDBByName(home, "../escape"); err != nil || path != "" {
		t.Errorf("path = %q, err = %v, want empty for a traversal name", path, err)
	}
	if path, err := resolveTrackerDBByName(home, "Missing"); err != nil || path != "" {
		t.Errorf("path = %q, err = %v, want empty for a non-slug name", path, err)
	}
	if path, err := resolveTrackerDBByName(home, "absent"); err != nil || path != "" {
		t.Errorf("path = %q, err = %v, want empty for a missing colony", path, err)
	}

	dbPath := seedColonyTracker(t, home, "present")
	resolved, err := resolveTrackerDBByName(home, "present")
	if err != nil {
		t.Fatalf("resolve present: %v", err)
	}
	resolvedLink, linkErr := filepath.EvalSymlinks(resolved)
	if linkErr != nil {
		t.Fatalf("resolve symlinks: %v", linkErr)
	}
	dbLink, linkErr := filepath.EvalSymlinks(dbPath)
	if linkErr != nil {
		t.Fatalf("resolve symlinks: %v", linkErr)
	}
	if resolvedLink != dbLink {
		t.Errorf("path = %q, want %q", resolvedLink, dbLink)
	}
}

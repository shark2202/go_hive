package tracker

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newColony(t *testing.T) (colonyDir, dbPath string) {
	t.Helper()
	colonyDir = filepath.Join(t.TempDir(), "alpha")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath, err := EnsureTrackerDB(colonyDir)
	if err != nil {
		t.Fatal(err)
	}
	return colonyDir, dbPath
}

func mustOpen(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func scalarInt(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func scalarString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRow(query, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return s
}

func numberEq(v any, want int64) bool {
	switch n := v.(type) {
	case int64:
		return n == want
	case int:
		return int64(n) == want
	case float64:
		return int64(n) == want
	}
	return false
}

// Parity: core/tests/test_tracker_db.py::test_ensure_tracker_db_fresh
func TestEnsureTrackerDBCreatesFreshDB(t *testing.T) {
	colonyDir, dbPath := newColony(t)

	if filepath.Base(dbPath) != "tracker.db" {
		t.Fatalf("db name = %s", filepath.Base(dbPath))
	}
	if filepath.Base(filepath.Dir(dbPath)) != "tracker" {
		t.Fatalf("db parent = %s, want tracker", filepath.Base(filepath.Dir(dbPath)))
	}
	if !filepath.IsAbs(dbPath) {
		t.Fatalf("ensure must return an absolute path: %s", dbPath)
	}
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		t.Fatalf("tracker.db missing: %v", err)
	}

	db := mustOpen(t, dbPath)
	if mode := scalarString(t, db, "PRAGMA journal_mode"); strings.ToLower(mode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	if v := scalarInt(t, db, "PRAGMA user_version"); v != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, SchemaVersion)
	}
	for _, table := range []string{"_tracker_registry", "_tracker_meta", "_tracker_changes"} {
		var one int
		if err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&one); err != nil {
			t.Fatalf("missing table %s: %v", table, err)
		}
	}
	var one int
	if err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='index' AND name='_tracker_changes_table_id'").Scan(&one); err != nil {
		t.Fatalf("missing change-log index: %v", err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM _tracker_meta WHERE key='schema_version'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "3" {
		t.Fatalf("meta schema_version = %q, want 3", value)
	}
	_ = colonyDir
}

// Parity: core/tests/test_tracker_db.py::test_ensure_tracker_db_idempotent
func TestEnsureTrackerDBIdempotent(t *testing.T) {
	colonyDir, first := newColony(t)
	second, err := EnsureTrackerDB(colonyDir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("path drifted: %s vs %s", first, second)
	}
	db := mustOpen(t, first)
	if v := scalarInt(t, db, "PRAGMA user_version"); v != SchemaVersion {
		t.Fatalf("version drifted: %d", v)
	}
}

// Parity: core/tests/test_tracker_db.py::test_ensure_tracker_db_preserves_user_data
func TestEnsureTrackerDBPreservesUserData(t *testing.T) {
	colonyDir, dbPath := newColony(t)
	if _, err := ExecuteSQL(dbPath, "CREATE TABLE competitors (name TEXT, url TEXT)", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSQL(dbPath, "INSERT INTO competitors (name, url) VALUES ('acme', 'https://acme.test')", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureTrackerDB(colonyDir); err != nil {
		t.Fatal(err)
	}
	res, err := ExecuteSQL(dbPath, "SELECT name, url FROM competitors", 1000)
	if err != nil {
		t.Fatal(err)
	}
	rows := res["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("row lost after re-ensure: %+v", res)
	}
	row := rows[0].([]any)
	if row[0] != "acme" || row[1] != "https://acme.test" {
		t.Fatalf("row changed: %+v", row)
	}
}

// Parity: core/tests/test_tracker_db.py::test_migration_backfills_change_triggers
func TestMigratesV2ToV3AndBackfillsTriggers(t *testing.T) {
	colonyDir := filepath.Join(t.TempDir(), "legacy")
	trackerDir := filepath.Join(colonyDir, "tracker")
	if err := os.MkdirAll(trackerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(trackerDir, "tracker.db")

	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitStatements(bootstrapSchema) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("CREATE TABLE leads (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO _tracker_registry (table_name, write_columns, key_columns, mode, registered_at) VALUES (?, ?, ?, ?, ?)",
		"leads", `["name"]`, `["id"]`, "upsert", "2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureTrackerDB(colonyDir); err != nil {
		t.Fatal(err)
	}

	db = mustOpen(t, dbPath)
	if v := scalarInt(t, db, "PRAGMA user_version"); v != SchemaVersion {
		t.Fatalf("not migrated: user_version = %d", v)
	}
	insTrigger, updTrigger := changeTriggerNames("leads")
	for _, name := range []string{insTrigger, updTrigger} {
		var one int
		if err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='trigger' AND name=?", name).Scan(&one); err != nil {
			t.Fatalf("trigger %s not installed: %v", name, err)
		}
	}
	if _, err := db.Exec("INSERT INTO leads (id) VALUES ('a')"); err != nil {
		t.Fatal(err)
	}
	var tableName, pk, op string
	if err := db.QueryRow("SELECT table_name, pk, op FROM _tracker_changes ORDER BY id DESC LIMIT 1").Scan(&tableName, &pk, &op); err != nil {
		t.Fatal(err)
	}
	if tableName != "leads" || pk != `{"id":"a"}` || op != "insert" {
		t.Fatalf("change row = (%q, %q, %q)", tableName, pk, op)
	}
}

// Parity: core/tests/test_tracker_db.py::test_prune_change_log_caps_entries
func TestPruneChangeLogKeepsNewest(t *testing.T) {
	_, dbPath := newColony(t)
	db := mustOpen(t, dbPath)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare("INSERT INTO _tracker_changes (table_name, pk, op, changed_at) VALUES ('t', '{}', 'insert', '2026-01-01T00:00:00Z')")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ChangeLogMax+50; i++ {
		if _, err := stmt.Exec(); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := PruneChangeLog(db); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, db, "SELECT COUNT(*) FROM _tracker_changes"); n != ChangeLogMax {
		t.Fatalf("count = %d, want %d", n, ChangeLogMax)
	}
	if minID := scalarInt(t, db, "SELECT MIN(id) FROM _tracker_changes"); minID != 51 {
		t.Fatalf("min id = %d, want 51", minID)
	}
}

// Parity: core/tests/test_tracker_db.py::test_validate_sql_allows
func TestValidateSQLAllowed(t *testing.T) {
	allowed := []string{
		"CREATE TABLE t (id INTEGER)",
		"CREATE TABLE IF NOT EXISTS t (id INTEGER)",
		"ALTER TABLE t ADD COLUMN name TEXT",
		"DROP TABLE t",
		"DROP TABLE IF EXISTS t",
		"CREATE INDEX idx ON t(id)",
		"CREATE UNIQUE INDEX uidx ON t(id)",
		"CREATE VIEW v AS SELECT 1",
		"INSERT INTO t VALUES (1)",
		"INSERT OR REPLACE INTO t VALUES (1)",
		"UPDATE t SET id = 1",
		"DELETE FROM t WHERE id = 1",
		"SELECT * FROM t",
		"SELECT * FROM _tracker_registry",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"WITH x AS (SELECT 1) DELETE FROM t WHERE id IN (SELECT * FROM x)",
		"BEGIN; INSERT INTO t VALUES (1); COMMIT",
		"SELECT 'ATTACH database x' AS note",
		"SELECT 1 -- PRAGMA foo",
		"SELECT 1 /* VACUUM */",
	}
	for _, sqlText := range allowed {
		if err := ValidateSQL(sqlText); err != nil {
			t.Errorf("should be allowed: %q: %v", sqlText, err)
		}
	}
}

// Parity: core/tests/test_tracker_db.py::test_validate_sql_rejects_leading_keywords
func TestValidateSQLRejectsLeadingKeywords(t *testing.T) {
	rejected := []string{
		"ATTACH DATABASE 'x' AS y",
		"DETACH DATABASE y",
		"PRAGMA table_info(t)",
		"PRAGMA user_version",
		"VACUUM",
		"REINDEX",
		"-- comment\nPRAGMA user_version",
		"/* hi */ VACUUM",
	}
	for _, sqlText := range rejected {
		err := ValidateSQL(sqlText)
		var denied *DenylistError
		if !errors.As(err, &denied) {
			t.Errorf("should be denied: %q: %v", sqlText, err)
			continue
		}
		if !strings.Contains(err.Error(), "forbidden statement") {
			t.Errorf("wrong message for %q: %v", sqlText, err)
		}
	}
}

// Parity: core/tests/test_tracker_db.py::test_validate_sql_rejects_load_extension
// Parity: core/tests/test_tracker_db.py::test_validate_sql_load_extension_in_string_is_ok
// Parity: core/tests/test_tracker_db.py::test_validate_sql_load_extension_as_substring_is_ok
func TestValidateSQLRejectsLoadExtension(t *testing.T) {
	rejected := []string{
		"SELECT load_extension('x')",
		"SELECT load_extension ('x')",
		"SELECT LOAD_EXTENSION('x')",
		"INSERT INTO t SELECT load_extension('x')",
	}
	for _, sqlText := range rejected {
		err := ValidateSQL(sqlText)
		if err == nil || !strings.Contains(err.Error(), "load_extension") {
			t.Errorf("should be denied: %q: %v", sqlText, err)
		}
	}
	for _, sqlText := range []string{
		"SELECT 'load_extension(' AS s",
		"SELECT my_load_extension(1)",
	} {
		if err := ValidateSQL(sqlText); err != nil {
			t.Errorf("should be allowed: %q: %v", sqlText, err)
		}
	}
}

// Parity: core/tests/test_tracker_db.py::test_validate_sql_rejects_protected_namespace
func TestValidateSQLRejectsFrameworkTables(t *testing.T) {
	rejected := []string{
		"DROP TABLE _tracker_meta",
		"DROP TABLE IF EXISTS _tracker_changes",
		"CREATE TABLE _hidden (id INTEGER)",
		"ALTER TABLE _tracker_meta ADD COLUMN x TEXT",
		"INSERT INTO _tracker_registry VALUES (1)",
		"UPDATE _tracker_meta SET value = 'x'",
		"DELETE FROM _tracker_changes",
		"INSERT INTO [_tracker_registry] VALUES (1)",
		"INSERT INTO `_tracker_registry` VALUES (1)",
		"CREATE INDEX ix ON _tracker_changes (id)",
		"WITH x AS (SELECT 1) DELETE FROM _tracker_meta",
		"WITH x AS (SELECT 1) INSERT INTO _tracker_meta VALUES (1)",
		"WITH x AS (SELECT 1) UPDATE _tracker_meta SET value = '1'",
		"INSERT INTO/**/_tracker_registry VALUES (1)",
		"/* hi */ DROP TABLE _tracker_registry",
		"DROP TRIGGER _tracker_chg__leads__ins",
		`DROP TRIGGER "_tracker_chg__leads__upd"`,
	}
	for _, sqlText := range rejected {
		err := ValidateSQL(sqlText)
		if err == nil || !strings.Contains(err.Error(), "framework table") {
			t.Errorf("should be denied: %q: %v", sqlText, err)
		}
	}
}

// Parity: core/tests/test_tracker_db.py::test_validate_sql_rejects_empty
// Parity: core/tests/test_tracker_db.py::test_validate_sql_rejects_only_comments
// Parity: core/tests/test_tracker_db.py::test_validate_sql_rejects_too_many_statements
// Parity: core/tests/test_tracker_db.py::test_validate_sql_string_with_semicolon_does_not_split
func TestValidateSQLEdgeCases(t *testing.T) {
	cases := []struct {
		sqlText string
		want    string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"-- only a comment\n", "no executable"},
		{strings.Repeat("SELECT 1;", MaxStatementsPerCall+1), "too many statements"},
	}
	for _, tc := range cases {
		err := ValidateSQL(tc.sqlText)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.sqlText, err, tc.want)
		}
	}
	if err := ValidateSQL("INSERT INTO t VALUES (';'); SELECT 1"); err != nil {
		t.Fatalf("semicolon in string rejected: %v", err)
	}
	err := ValidateSQL("CREATE TABLE ok (id INTEGER); ATTACH DATABASE 'x' AS y")
	if err == nil || !strings.Contains(err.Error(), "forbidden statement: ATTACH") {
		t.Fatalf("script not rejected atomically: %v", err)
	}
}

// Parity: core/tests/test_tracker_db.py::test_execute_sql_create_insert_select
func TestExecuteSQLShapes(t *testing.T) {
	_, dbPath := newColony(t)

	res, err := ExecuteSQL(dbPath, "CREATE TABLE competitors (name TEXT, url TEXT)", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if res["kind"] != "exec" {
		t.Fatalf("DDL kind = %v", res["kind"])
	}
	if !numberEq(res["rowcount"], 0) {
		t.Fatalf("DDL rowcount = %v", res["rowcount"])
	}

	res, err = ExecuteSQL(dbPath, "INSERT INTO competitors (name, url) VALUES ('acme', 'https://acme.test')", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if res["kind"] != "exec" || !numberEq(res["rowcount"], 1) || !numberEq(res["last_insert_rowid"], 1) {
		t.Fatalf("insert result = %+v", res)
	}

	res, err = ExecuteSQL(dbPath, "SELECT name, url FROM competitors", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if res["kind"] != "rows" {
		t.Fatalf("select kind = %v", res["kind"])
	}
	columns := res["columns"].([]string)
	if len(columns) != 2 || columns[0] != "name" || columns[1] != "url" {
		t.Fatalf("columns = %v", columns)
	}
	rows := res["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	row := rows[0].([]any)
	if row[0] != "acme" || row[1] != "https://acme.test" {
		t.Fatalf("row = %+v", row)
	}
	if !numberEq(res["rowcount"], 1) || res["truncated"] != false {
		t.Fatalf("rowcount/truncated = %v/%v", res["rowcount"], res["truncated"])
	}
}

// Parity: core/tests/test_tracker_db.py::test_execute_sql_multi_statement_script
func TestExecuteSQLScript(t *testing.T) {
	_, dbPath := newColony(t)
	script := "CREATE TABLE nums (n INTEGER); INSERT INTO nums VALUES (1); INSERT INTO nums VALUES (2); SELECT n FROM nums ORDER BY n"
	res, err := ExecuteSQL(dbPath, script, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if res["kind"] != "script" {
		t.Fatalf("kind = %v", res["kind"])
	}
	results := res["results"].([]any)
	if len(results) != 4 {
		t.Fatalf("results = %d", len(results))
	}
	last := results[3].(map[string]any)
	rows := last["rows"].([]any)
	if len(rows) != 2 || !numberEq(rows[0].([]any)[0], 1) || !numberEq(rows[1].([]any)[0], 2) {
		t.Fatalf("last rows = %+v", rows)
	}
}

// Parity: core/tests/test_tracker_db.py::test_execute_sql_truncation
func TestExecuteSQLTruncation(t *testing.T) {
	_, dbPath := newColony(t)
	if _, err := ExecuteSQL(dbPath, "CREATE TABLE nums (n INTEGER); INSERT INTO nums VALUES (1); INSERT INTO nums VALUES (2); INSERT INTO nums VALUES (3); INSERT INTO nums VALUES (4); INSERT INTO nums VALUES (5)", 1000); err != nil {
		t.Fatal(err)
	}
	res, err := ExecuteSQL(dbPath, "SELECT n FROM nums ORDER BY n", 3)
	if err != nil {
		t.Fatal(err)
	}
	rows := res["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if res["truncated"] != true {
		t.Fatalf("truncated = %v", res["truncated"])
	}
}

// Parity: core/tests/test_tracker_db.py::test_execute_sql_rejects_before_running
func TestExecuteSQLRejectsBeforeRunning(t *testing.T) {
	_, dbPath := newColony(t)
	_, err := ExecuteSQL(dbPath, "CREATE TABLE will_not_be_made (id INTEGER); ATTACH DATABASE 'x' AS y", 1000)
	if err == nil {
		t.Fatal("script should be denied")
	}
	db := mustOpen(t, dbPath)
	var one int
	if err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name='will_not_be_made'").Scan(&one); err == nil {
		t.Fatal("table was created despite denied script")
	}
}

// Parity: core/tests/test_tracker_db.py::test_execute_sql_select_from_protected_works
func TestExecuteSQLSelectsProtectedTable(t *testing.T) {
	_, dbPath := newColony(t)
	res, err := ExecuteSQL(dbPath, "SELECT * FROM _tracker_registry", 1000)
	if err != nil {
		t.Fatal(err)
	}
	rows := res["rows"].([]any)
	if len(rows) != 0 {
		t.Fatalf("registry not empty: %+v", rows)
	}
}

// Parity: core/tests/test_tracker_db.py::test_ensure_tracker_db_patches_existing_worker_config
func TestEnsurePatchesWorkerConfigs(t *testing.T) {
	colonyDir := filepath.Join(t.TempDir(), "colony_a")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worker := map[string]any{
		"system_prompt": "do work",
		"input_data": map[string]any{
			"db_path":         "/old/progress.db",
			"colony_data_dir": "/old",
			"tracker_db_path": "/old/tracker.db",
			"colony_id":       "colony_a",
			"keep_me":         "yes",
		},
	}
	workerRaw, _ := json.Marshal(worker)
	workerPath := filepath.Join(colonyDir, "worker.json")
	if err := os.WriteFile(workerPath, workerRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(colonyDir, "metadata.json")
	triggersPath := filepath.Join(colonyDir, "triggers.json")
	randomPath := filepath.Join(colonyDir, "random.json")
	metadataRaw := []byte(`{"name":"colony_a"}`)
	triggersRaw := []byte(`{"triggers":[]}`)
	randomRaw := []byte(`{"no_system_prompt":true}`)
	for path, raw := range map[string][]byte{metadataPath: metadataRaw, triggersPath: triggersRaw, randomPath: randomRaw} {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dbPath, err := EnsureTrackerDB(colonyDir)
	if err != nil {
		t.Fatal(err)
	}

	var patched map[string]any
	raw, err := os.ReadFile(workerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &patched); err != nil {
		t.Fatal(err)
	}
	inputData := patched["input_data"].(map[string]any)
	binding, ok := inputData["binding"].(map[string]any)
	if !ok {
		t.Fatalf("binding missing: %+v", inputData)
	}
	if binding["name"] != "colony_a" || binding["dir"] != colonyDir || binding["tracker_db"] != dbPath {
		t.Fatalf("binding = %+v", binding)
	}
	for _, legacy := range []string{"db_path", "colony_data_dir", "tracker_db_path", "colony_id"} {
		if _, present := inputData[legacy]; present {
			t.Fatalf("legacy field %s not removed: %+v", legacy, inputData)
		}
	}
	if inputData["keep_me"] != "yes" {
		t.Fatalf("unrelated input_data lost: %+v", inputData)
	}

	for path, want := range map[string][]byte{metadataPath: metadataRaw, triggersPath: triggersRaw, randomPath: randomRaw} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s was modified: %s", filepath.Base(path), got)
		}
	}

	before, err := os.Stat(workerPath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := EnsureTrackerDB(colonyDir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(workerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("worker.json rewritten unnecessarily: %v -> %v", before.ModTime(), after.ModTime())
	}
}

// Parity: core/tests/test_tracker_db.py::test_ensure_all_colony_tracker_dbs_backfill
func TestEnsureAllColonyTrackerDBs(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "gamma"), []byte("not a colony"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := EnsureAllColonyTrackerDBs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("initialized = %v", paths)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}

	missing, err := EnsureAllColonyTrackerDBs(filepath.Join(root, "nope"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing root: %v %v", missing, err)
	}
}

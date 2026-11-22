package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/httpapi"
)

func TestGlobalDBWorkflow(t *testing.T) {
	e := newEnv(t)

	// Free DDL through /sql (Postgres-style types must be accepted).
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "CREATE TABLE leads (lead_id TEXT PRIMARY KEY, name TEXT, score INTEGER, active BOOLEAN)",
	})
	e.decodeJSON(resp, raw, http.StatusOK)

	resp, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "INSERT INTO leads (lead_id, name, score, active) VALUES ('l1', 'Alice', 5, TRUE), ('l2', 'Bob', 7, FALSE)",
	})
	writeBody := e.decodeJSON(resp, raw, http.StatusOK)
	if writeBody["rowCount"].(float64) != 2 {
		t.Fatalf("rowCount = %v", writeBody["rowCount"])
	}
	goldenJSON(t, "globaldb-sql-write", writeBody)

	// Read path via /query.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": "SELECT lead_id, name, score FROM leads ORDER BY lead_id",
	})
	queryBody := e.decodeJSON(resp, raw, http.StatusOK)
	if queryBody["rowCount"].(float64) != 2 || queryBody["truncated"] != false {
		t.Fatalf("query body = %v", queryBody)
	}
	goldenJSON(t, "globaldb-query", queryBody)

	// Upsert creates-on-demand and merges.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/upsert", map[string]any{
		"table": "leads",
		"row":   map[string]any{"lead_id": "l2", "name": "Bobby", "score": 9, "active": false},
		"mode":  "merge",
	})
	upsertBody := e.decodeJSON(resp, raw, http.StatusOK)
	if upsertBody["updated"].(float64) != 1 {
		t.Fatalf("upsert did not merge: %v", upsertBody)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/upsert", map[string]any{
		"table": "leads",
		"row":   map[string]any{"lead_id": "l3", "name": "Carol", "score": 3, "active": true},
	})
	upsertBody = e.decodeJSON(resp, raw, http.StatusOK)
	if upsertBody["inserted"].(float64) != 1 {
		t.Fatalf("upsert did not insert: %v", upsertBody)
	}

	// Import batch.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/import", map[string]any{
		"table": "leads",
		"rows": []map[string]any{
			{"lead_id": "l4", "name": "Dave", "score": 1, "active": true},
			{"lead_id": "l5", "name": "Erin", "score": 2, "active": true},
		},
		"source_colony": "smoke",
	})
	importBody := e.decodeJSON(resp, raw, http.StatusOK)
	if importBody["inserted"].(float64) != 2 || importBody["total"].(float64) != 2 {
		t.Fatalf("import body = %v", importBody)
	}

	// Rows page (shape the runtime's _global_introspect and the UI read).
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/tables/leads/rows?limit=2&offset=0&order_by=score&order_dir=desc", nil)
	rowsBody := e.decodeJSON(resp, raw, http.StatusOK)
	if rowsBody["table"] != "leads" || rowsBody["limit"].(float64) != 2 || rowsBody["offset"].(float64) != 0 {
		t.Fatalf("rows body = %v", rowsBody)
	}
	if rowsBody["total"].(float64) != 5 {
		t.Fatalf("total = %v", rowsBody["total"])
	}
	pk, _ := rowsBody["primary_key"].([]any)
	if len(pk) != 1 || pk[0] != "lead_id" {
		t.Fatalf("primary_key = %v", rowsBody["primary_key"])
	}
	rows := rowsBody["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows len = %d", len(rows))
	}
	if got := rows[0].(map[string]any)["lead_id"]; got != "l2" {
		t.Fatalf("order_by=score desc first row = %v", got)
	}
	goldenJSON(t, "globaldb-rows", rowsBody)

	// Patch one row.
	resp, raw = e.authed(http.MethodPatch, "/v1/global-db/tables/leads/rows", map[string]any{
		"pk":      map[string]any{"lead_id": "l1"},
		"updates": map[string]any{"score": 42},
	})
	patchBody := e.decodeJSON(resp, raw, http.StatusOK)
	if patchBody["updated"].(float64) != 1 {
		t.Fatalf("patch body = %v", patchBody)
	}
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/tables/leads/rows?order_by=lead_id&order_dir=asc", nil)
	rowsBody = e.decodeJSON(resp, raw, http.StatusOK)
	first := rowsBody["rows"].([]any)[0].(map[string]any)
	if first["lead_id"] != "l1" || first["score"].(float64) != 42 {
		t.Fatalf("patch did not apply: %v", first)
	}

	// Patch a missing row → 404.
	resp, raw = e.authed(http.MethodPatch, "/v1/global-db/tables/leads/rows", map[string]any{
		"pk":      map[string]any{"lead_id": "nope"},
		"updates": map[string]any{"score": 1},
	})
	missing := e.decodeJSON(resp, raw, http.StatusNotFound)
	if missing["error"] != "row not found" {
		t.Fatalf("missing row body = %v", missing)
	}

	// Tables catalogue.
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/tables", nil)
	tablesBody := e.decodeJSON(resp, raw, http.StatusOK)
	tables := tablesBody["tables"].([]any)
	if len(tables) != 1 {
		t.Fatalf("tables = %v", tables)
	}
	only := tables[0].(map[string]any)
	if only["name"] != "leads" || only["row_count"].(float64) != 5 {
		t.Fatalf("table overview = %v", only)
	}
	goldenJSON(t, "globaldb-tables", tablesBody)

	// Changes: init returns cursor + covered tables only.
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/changes", nil)
	initBody := e.decodeJSON(resp, raw, http.StatusOK)
	if initBody["cursor"] == nil || len(initBody["tables"].(map[string]any)) != 0 {
		t.Fatalf("changes init = %v", initBody)
	}
	covered := initBody["covered"].([]any)
	if len(covered) != 1 || covered[0] != "leads" {
		t.Fatalf("covered = %v", initBody["covered"])
	}
	goldenJSON(t, "globaldb-changes-init", initBody)

	// Changes since the last cursor → no new rows, cursor advances.
	cursor := initBody["cursor"].(string)
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/changes?since="+cursor, nil)
	sinceBody := e.decodeJSON(resp, raw, http.StatusOK)
	if len(sinceBody["tables"].(map[string]any)) != 0 {
		t.Fatalf("changes since should be empty: %v", sinceBody)
	}

	// Changes from 0 → every recorded op.
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/changes?since=0", nil)
	allBody := e.decodeJSON(resp, raw, http.StatusOK)
	group, ok := allBody["tables"].(map[string]any)["leads"].(map[string]any)
	if !ok {
		t.Fatalf("changes since=0 = %v", allBody)
	}
	if group["count"].(float64) < 4 {
		t.Fatalf("changes count = %v", group["count"])
	}
	goldenJSON(t, "globaldb-changes-since-zero", allBody)

	// Bad cursor is a client error.
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/changes?since=not-a-cursor", nil)
	badCursor := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if !strings.Contains(badCursor["error"].(string), "invalid since cursor") {
		t.Fatalf("bad cursor body = %v", badCursor)
	}

	// Unknown table 404.
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/tables/nope/rows", nil)
	unknown := e.decodeJSON(resp, raw, http.StatusNotFound)
	if unknown["error"] != "unknown table: nope" {
		t.Fatalf("unknown table body = %v", unknown)
	}
}

func TestUpsertInsertModeConflict(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/upsert", map[string]any{
		"table": "leads",
		"row":   map[string]any{"id": "lead-1", "name": "Alice"},
	})
	e.decodeJSON(resp, raw, http.StatusOK)

	resp, raw = e.authed(http.MethodPost, "/v1/global-db/upsert", map[string]any{
		"table": "leads",
		"row":   map[string]any{"id": "lead-1", "name": "Alice again"},
		"mode":  "insert",
	})
	body := e.decodeJSON(resp, raw, http.StatusConflict)
	if body["error"] != "conflict" {
		t.Fatalf("conflict body = %v", body)
	}
	pk := body["pk"].(map[string]any)
	if pk["id"] != "lead-1" {
		t.Fatalf("conflict pk = %v", body["pk"])
	}
}

func TestSQLStatementGate(t *testing.T) {
	e := newEnv(t)
	_, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "CREATE TABLE leads (lead_id TEXT PRIMARY KEY, name TEXT)",
	})
	if !strings.Contains(string(raw), "rowCount") {
		t.Fatalf("setup table: %s", raw)
	}

	cases := []struct {
		name string
		sql  string
		want string
	}{
		{"multiple statements", "SELECT 1; SELECT 2", "only a single SQL statement is allowed"},
		{"attach", "ATTACH DATABASE 'x.db' AS x", "ATTACH is not allowed"},
		{"detach", "DETACH DATABASE x", "DETACH is not allowed"},
		{"pragma", "PRAGMA table_info(leads)", "PRAGMA is not allowed"},
		{"vacuum", "VACUUM", "VACUUM is not allowed"},
		{"reindex", "REINDEX leads", "REINDEX is not allowed"},
		{"load_extension", "SELECT load_extension('x')", "load_extension is not allowed"},
		{"internal table", "SELECT * FROM _cloud_changes", `access to internal object "_cloud_changes" is not allowed`},
		{"internal audit", "DELETE FROM _cloud_audit", `access to internal object "_cloud_audit" is not allowed`},
		{"sqlite internal", "SELECT name FROM sqlite_master", `access to internal object "sqlite_master" is not allowed`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{"sql": tc.sql})
			body := e.decodeJSON(resp, raw, http.StatusBadRequest)
			if body["error"] != tc.want {
				t.Fatalf("error = %q want %q", body["error"], tc.want)
			}
		})
	}

	// Comments and string literals must not trip the keyword scan.
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": "SELECT 'ATTACH PRAGMA; VACUUM' AS note /* PRAGMA VACUUM */",
	})
	body := e.decodeJSON(resp, raw, http.StatusOK)
	if body["rowCount"].(float64) != 1 {
		t.Fatalf("literal body = %v", body)
	}

	// Empty statement.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{"sql": "   "})
	empty := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if empty["error"] != "sql is required" {
		t.Fatalf("empty body = %v", empty)
	}
}

func TestQueryIsReadOnly(t *testing.T) {
	e := newEnv(t)
	_, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "CREATE TABLE t (id TEXT PRIMARY KEY)",
	})
	if !strings.Contains(string(raw), "rowCount") {
		t.Fatalf("setup: %s", raw)
	}
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": "INSERT INTO t (id) VALUES ('x')",
	})
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "only read-only statements are allowed on /query" {
		t.Fatalf("body = %v", body)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": "WITH x AS (SELECT 1) DELETE FROM t",
	})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "only read-only statements are allowed on /query" {
		t.Fatalf("CTE write body = %v", body)
	}
}

func TestRowCapTruncation(t *testing.T) {
	e := newEnv(t)
	_, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "CREATE TABLE nums (n INTEGER PRIMARY KEY)",
	})
	if !strings.Contains(string(raw), "rowCount") {
		t.Fatalf("setup: %s", raw)
	}
	_, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "INSERT INTO nums (n) VALUES (1), (2), (3), (4), (5)",
	})
	if !strings.Contains(string(raw), "rowCount") {
		t.Fatalf("seed: %s", raw)
	}
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql":     "SELECT n FROM nums ORDER BY n",
		"row_cap": 2,
	})
	body := e.decodeJSON(resp, raw, http.StatusOK)
	if body["truncated"] != true {
		t.Fatalf("truncated = %v", body["truncated"])
	}
	if len(body["rows"].([]any)) != 2 {
		t.Fatalf("rows = %v", body["rows"])
	}
}

// TestDialectTranslation drives the grid_query statement shapes through the
// real endpoint: ILIKE/ESCAPE, CAST(... AS TEXT), ::text, TRUE/FALSE, NOW().
func TestDialectTranslation(t *testing.T) {
	e := newEnv(t)
	_, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "CREATE TABLE leads (lead_id TEXT PRIMARY KEY, name TEXT, score INTEGER, active BOOLEAN)",
	})
	if !strings.Contains(string(raw), "rowCount") {
		t.Fatalf("setup: %s", raw)
	}
	_, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "INSERT INTO leads (lead_id, name, score, active) VALUES ('l1', 'Alice', 5, TRUE), ('l2', 'bob', 7, FALSE), ('l3', '50%_off', 1, TRUE)",
	})
	if !strings.Contains(string(raw), "rowCount") {
		t.Fatalf("seed: %s", raw)
	}

	cases := []struct {
		name string
		sql  string
		want int
	}{
		{
			name: "grid_query search",
			sql:  `SELECT * FROM leads WHERE (CAST("name" AS TEXT) ILIKE '%AL%' ESCAPE '\')`,
			want: 1,
		},
		{
			name: "grid_query contains with literal wildcards",
			sql:  `SELECT * FROM leads WHERE CAST("name" AS TEXT) ILIKE '%50\%\_off%' ESCAPE '\'`,
			want: 1,
		},
		{
			name: "postgres text cast",
			sql:  "SELECT lead_id::text FROM leads WHERE active = TRUE",
			want: 2,
		},
		{
			name: "postgres false literal",
			sql:  "SELECT lead_id FROM leads WHERE active = FALSE",
			want: 1,
		},
		{
			name: "now",
			sql:  "SELECT lead_id FROM leads WHERE NOW() IS NOT NULL",
			want: 3,
		},
		{
			name: "grid_query count",
			sql:  "SELECT count(*) AS total FROM leads WHERE CAST(\"name\" AS TEXT) ILIKE '%o%' ESCAPE '\\'",
			want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{"sql": tc.sql})
			body := e.decodeJSON(resp, raw, http.StatusOK)
			if got := len(body["rows"].([]any)); got != tc.want {
				t.Fatalf("rows = %d want %d (body %v)", got, tc.want, body)
			}
		})
	}

	// Quoted identifiers and string literals are never rewritten.
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": `SELECT 'ILIKE TRUE NOW() ' AS "ilike"`,
	})
	body := e.decodeJSON(resp, raw, http.StatusOK)
	row := body["rows"].([]any)[0].(map[string]any)
	if row["ilike"] != "ILIKE TRUE NOW() " {
		t.Fatalf("literal rewritten: %v", row)
	}
}

func TestStatementTimeout(t *testing.T) {
	e := newEnv(t, func(cfg *httpapi.Config) { cfg.StatementTimeout = 150 * time.Millisecond })
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c",
	})
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status %d want 408 (body %s)", resp.StatusCode, raw)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["error"].(string), "statement exceeded") {
		t.Fatalf("timeout body = %v", body)
	}
}

func TestBadJSONBody(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.authed(http.MethodPost, "/v1/global-db/sql", "{not json}")
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "invalid JSON body" {
		t.Fatalf("body = %v", body)
	}
	resp, raw = e.authed(http.MethodPatch, "/v1/global-db/tables/x/rows", map[string]any{"pk": map[string]any{}})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "pk and updates must be objects" {
		t.Fatalf("patch body = %v", body)
	}
}

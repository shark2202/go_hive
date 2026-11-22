// Command globaldb-smoke drives go-hive-cloud with the REAL go_hive Global DB
// client (github.com/shark2202/go_hive/go-hive/internal/globaldb) — the L2a evidence
// layer: not a reimplementation of the client, the client the runtime ships.
//
// It runs every method the client exposes and records the observed responses to
// an evidence file (EVIDENCE_OUT). Exits non-zero when any check fails.
//
// Module note: the import path is deliberately nested under
// github.com/shark2202/go_hive/go-hive/ so Go's internal-package rule allows importing
// the main module's internal/globaldb through the replace directive in go.mod.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
)

type check struct {
	Name   string         `json:"name"`
	OK     bool           `json:"ok"`
	Detail map[string]any `json:"detail,omitempty"`
	Error  string         `json:"error,omitempty"`
}

type evidence struct {
	Harness    string  `json:"harness"`
	BaseURL    string  `json:"base_url"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at"`
	OK         bool    `json:"ok"`
	Checks     []check `json:"checks"`
}

type runner struct {
	client *globaldb.Client
	checks []check
}

func (r *runner) add(name string, ok bool, detail map[string]any, err error) {
	item := check{Name: name, OK: ok, Detail: detail}
	if err != nil {
		item.Error = err.Error()
	}
	r.checks = append(r.checks, item)
	if !ok {
		fmt.Fprintf(os.Stderr, "FAIL %s: %v (detail %v)\n", name, err, detail)
	} else {
		fmt.Fprintf(os.Stderr, "ok   %s %v\n", name, detail)
	}
}

func asMap(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func asList(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return nil
}

func main() {
	base := os.Getenv(globaldb.EnvBase)
	jwt := os.Getenv(globaldb.EnvJWT)
	started := time.Now().UTC()
	report := evidence{Harness: "globaldb-smoke", BaseURL: base, StartedAt: started.Format(time.RFC3339Nano)}

	client, err := globaldb.NewClientFromEnv()
	if err != nil {
		report.OK = false
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.Checks = []check{{Name: "new_client_from_env", OK: false, Error: err.Error()}}
		emit(report)
		fmt.Fprintf(os.Stderr, "harness: NewClientFromEnv: %v\n", err)
		os.Exit(1)
	}
	_ = jwt
	ctx := context.Background()
	r := &runner{client: client}

	// 1. Free DDL through the raw-SQL endpoint.
	res, err := client.SQL(ctx, "CREATE TABLE IF NOT EXISTS leads (lead_id TEXT PRIMARY KEY, name TEXT, score INTEGER, updated_at TEXT)", nil)
	r.add("sql_create_table", err == nil, map[string]any{"rowCount": asMap(res)["rowCount"]}, err)

	// 2. Multi-row INSERT.
	res, err = client.SQL(ctx, "INSERT OR REPLACE INTO leads (lead_id, name, score, updated_at) VALUES ('l1', 'Alice', 5, '2026-01-01T00:00:00Z'), ('l2', 'Bob', 7, '2026-01-02T00:00:00Z')", nil)
	r.add("sql_insert", err == nil && asMap(res)["rowCount"] == float64(2), map[string]any{"rowCount": asMap(res)["rowCount"]}, err)

	// 3. Read-only query (grid_query shape with ILIKE/ESCAPE goes through the
	//    dialect layer server-side).
	res, err = client.Query(ctx, `SELECT lead_id, name FROM leads WHERE CAST("name" AS TEXT) ILIKE '%al%' ESCAPE '\' ORDER BY lead_id`, nil)
	rows := asList(asMap(res)["rows"])
	r.add("query_ilike_search", err == nil && len(rows) == 1, map[string]any{"rows": len(rows)}, err)

	// 4. Upsert merge on an existing pk.
	res, err = client.Upsert(ctx, "leads", map[string]any{"lead_id": "l2", "name": "Bobby", "score": 9, "updated_at": "2026-01-03T00:00:00Z"}, "smoke", "merge")
	merged := asMap(res)
	r.add("upsert_merge", err == nil && merged["updated"] == float64(1), map[string]any{"updated": merged["updated"], "inserted": merged["inserted"]}, err)

	// 5. Upsert insert of a new pk.
	res, err = client.Upsert(ctx, "leads", map[string]any{"lead_id": "l3", "name": "Carol", "score": 3, "updated_at": "2026-01-04T00:00:00Z"}, "smoke", "")
	inserted := asMap(res)
	r.add("upsert_new_row", err == nil && inserted["inserted"] == float64(1), map[string]any{"inserted": inserted["inserted"]}, err)

	// 6. mode=insert must conflict on an existing pk (409).
	res, err = client.Upsert(ctx, "leads", map[string]any{"lead_id": "l3", "name": "Carol again"}, "", "insert")
	var conflictStatus int
	if dbErr, ok := err.(*globaldb.GlobalDbError); ok {
		conflictStatus = dbErr.Status
	}
	r.add("upsert_insert_conflict_409", conflictStatus == 409, map[string]any{"status": conflictStatus, "response": res}, err)

	// 7. Batch import.
	res, err = client.ImportRows(ctx, "leads", []map[string]any{
		{"lead_id": "l4", "name": "Dave", "score": 1, "updated_at": "2026-01-05T00:00:00Z"},
		{"lead_id": "l5", "name": "Erin", "score": 2, "updated_at": "2026-01-06T00:00:00Z"},
	}, "smoke")
	imported := asMap(res)
	r.add("import_rows", err == nil && imported["inserted"] == float64(2), map[string]any{"inserted": imported["inserted"], "total": imported["total"]}, err)

	// 8. Tables catalogue (the runtime's count cache reads name + row_count).
	res, err = client.ListTables(ctx)
	tables := asList(asMap(res)["tables"])
	leadsFound := false
	for _, raw := range tables {
		table := asMap(raw)
		if table["name"] == "leads" && table["row_count"] == float64(5) {
			leadsFound = true
		}
	}
	r.add("list_tables", err == nil && leadsFound, map[string]any{"tables": len(tables)}, err)

	// 9. Changes: init then since the returned cursor.
	res, err = client.ListChanges(ctx, "")
	initChanges := asMap(res)
	covered := asList(initChanges["covered"])
	coveredLeads := false
	for _, raw := range covered {
		if raw == "leads" {
			coveredLeads = true
		}
	}
	cursor, _ := initChanges["cursor"].(string)
	r.add("list_changes_init", err == nil && coveredLeads && cursor != "", map[string]any{"cursor": cursor, "covered": covered}, err)

	res, err = client.ListChanges(ctx, cursor)
	sinceChanges := asMap(res)
	r.add("list_changes_since", err == nil, map[string]any{"cursor": sinceChanges["cursor"], "tables": sinceChanges["tables"]}, err)

	// 10. Rows page: the runtime's _global_introspect calls this with
	//     limit=1&offset=0 and reads columns + primary_key.
	res, err = client.ListRows(ctx, "leads", url.Values{"limit": {"1"}, "offset": {"0"}, "order_by": {"score"}, "order_dir": {"desc"}})
	page := asMap(res)
	pageRows := asList(page["rows"])
	columns := asList(page["columns"])
	pk := asList(page["primary_key"])
	r.add("list_rows_introspect",
		err == nil && page["table"] == "leads" && len(pageRows) == 1 && len(columns) == 4 && len(pk) == 1,
		map[string]any{"table": page["table"], "total": page["total"], "limit": page["limit"], "offset": page["offset"], "columns": len(columns), "primary_key": pk},
		err)

	// 11. Row update by pk.
	res, err = client.UpdateRow(ctx, "leads", map[string]any{"lead_id": "l1"}, map[string]any{"score": 42})
	updated := asMap(res)
	r.add("update_row", err == nil && updated["updated"] == float64(1), map[string]any{"updated": updated["updated"]}, err)

	// 12. Verify the update is visible (and the changes feed advanced).
	res, err = client.ListRows(ctx, "leads", url.Values{"limit": {"10"}, "order_by": {"lead_id"}, "order_dir": {"asc"}})
	page = asMap(res)
	pageRows = asList(page["rows"])
	verified := false
	if len(pageRows) > 0 {
		first := asMap(pageRows[0])
		verified = first["lead_id"] == "l1" && first["score"] == float64(42)
	}
	r.add("update_row_visible", err == nil && verified, map[string]any{"total": page["total"]}, err)

	// 13. Unsigned-out behaviour lives client-side; verify the client raises
	//     NotSignedInError for a 401 by hitting the server without a token.
	anonymous := globaldb.NewClient(base, "clearly-not-a-valid-token")
	_, err = anonymous.ListTables(ctx)
	notSignedIn := false
	if _, ok := err.(*globaldb.NotSignedInError); ok {
		notSignedIn = true
	}
	r.add("client_401_maps_to_not_signed_in", notSignedIn, map[string]any{"error": fmt.Sprint(err)}, nil)

	report.Checks = r.checks
	report.OK = true
	for _, item := range r.checks {
		if !item.OK {
			report.OK = false
		}
	}
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	emit(report)
	if !report.OK {
		fmt.Fprintln(os.Stderr, "harness: FAILED")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "harness: all %d checks passed against %s\n", len(r.checks), base)
}

func emit(report evidence) {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness: marshal evidence: %v\n", err)
		os.Exit(1)
	}
	raw = append(raw, '\n')
	out := os.Getenv("EVIDENCE_OUT")
	if out == "" {
		os.Stdout.Write(raw)
		return
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "harness: mkdir evidence dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "harness: write evidence: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "harness: evidence -> %s\n", out)
}

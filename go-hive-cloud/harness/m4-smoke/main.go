// Command m4-smoke drives the M4 surface with the REAL go_hive global-db client
// (grid_query-generated Postgres SQL compatibility) and raw operator-JWT HTTP
// for the CRM grid, which has no open-source client.
//
// It writes evidence/m4-smoke.json and exits non-zero if any check fails.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	base   string
	jwt    string
	client *http.Client
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

func (r *runner) do(method, path string, body any, token string) (int, map[string]any, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, r.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "jwt "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	var decoded map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &decoded)
	}
	return resp.StatusCode, decoded, nil
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

func asFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func main() {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(globaldb.EnvBase)), "/")
	jwt := strings.TrimSpace(os.Getenv(globaldb.EnvJWT))
	started := time.Now().UTC()
	report := evidence{Harness: "m4-smoke", BaseURL: base, StartedAt: started.Format(time.RFC3339Nano)}
	if base == "" || jwt == "" {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.Checks = []check{{Name: "env", OK: false, Error: "HIVE_CLOUD_BASE / HIVE_CLOUD_JWT must be set"}}
		emit(report)
		os.Exit(1)
	}
	client, err := globaldb.NewClientFromEnv()
	if err != nil {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.Checks = []check{{Name: "new_client", OK: false, Error: err.Error()}}
		emit(report)
		os.Exit(1)
	}
	r := &runner{base: base, jwt: jwt, client: &http.Client{Timeout: 30 * time.Second}}
	ctx := context.Background()

	// --- grid_query-generated Postgres SQL through the real client ---------
	_, err = client.SQL(ctx, "CREATE TABLE leads (lead_id TEXT PRIMARY KEY, name TEXT, status TEXT, amount INTEGER, email TEXT)", nil)
	r.add("sql_create_table", err == nil, nil, err)
	_, err = client.SQL(ctx, "INSERT INTO leads (lead_id, name, status, amount, email) VALUES "+
		"('l1', 'Alice', 'new', 100, 'alice@example.com'), "+
		"('l2', 'Bobby', 'won', 250, 'bob@example.com'), "+
		"('l3', '50%_off', 'new', 50, 'rick@example.com')", nil)
	r.add("sql_insert", err == nil, nil, err)

	res, err := client.Query(ctx, `SELECT * FROM "leads" WHERE "status" = 'new' ORDER BY "amount" DESC LIMIT 50 OFFSET 0`, nil)
	rows := asList(asMap(res)["rows"])
	r.add("grid_query_select_filter_sort", err == nil && len(rows) == 2, map[string]any{"rows": len(rows)}, err)

	res, err = client.Query(ctx, `SELECT count(*) AS total FROM "leads" WHERE CAST("name" AS TEXT) ILIKE '%bob%' ESCAPE '\'`, nil)
	countRows := asList(asMap(res)["rows"])
	total := 0.0
	if len(countRows) > 0 {
		total = asFloat(asMap(countRows[0])["total"])
	}
	r.add("grid_query_count_search", err == nil && total == 1, map[string]any{"total": total}, err)

	res, err = client.Query(ctx, `SELECT "status" AS value, count(*) AS count FROM "leads" GROUP BY "status" ORDER BY count(*) DESC, "status" ASC LIMIT 200`, nil)
	groups := asList(asMap(res)["rows"])
	firstGroup := map[string]any{}
	if len(groups) > 0 {
		firstGroup = asMap(groups[0])
	}
	r.add("grid_query_group_counts", err == nil && len(groups) == 2 && firstGroup["value"] == "new" && asFloat(firstGroup["count"]) == 2,
		map[string]any{"groups": len(groups), "first": firstGroup}, err)

	res, err = client.Query(ctx, `SELECT * FROM "leads" WHERE CAST("name" AS TEXT) ILIKE '%ALICE%' ESCAPE '\'`, nil)
	r.add("grid_query_ilike_case_insensitive", err == nil && len(asList(asMap(res)["rows"])) == 1, nil, err)

	_, err = client.SQL(ctx, "INSERT INTO leads (lead_id, name, status, amount, email) VALUES ('abc''123', 'Quoted', 'new', 1, 'q@example.com')", nil)
	r.add("sql_insert_quoted_literal", err == nil, nil, err)
	_, err = client.SQL(ctx, `DELETE FROM "leads" WHERE "lead_id" = 'abc''123'`, nil)
	r.add("grid_query_delete", err == nil, nil, err)
	_, err = client.Query(ctx, `DELETE FROM "leads" WHERE "lead_id" = 'l1'`, nil)
	status := 0
	if dbErr, ok := err.(*globaldb.GlobalDbError); ok {
		status = dbErr.Status
	}
	r.add("grid_query_readonly_gate", status == http.StatusBadRequest, map[string]any{"status": status}, err)

	// --- CRM grid over raw operator JWT -----------------------------------
	status, body, err := r.do(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{"filter": []any{}}, "")
	r.add("crm_requires_auth", err == nil && status == http.StatusUnauthorized && body["error"] != nil, map[string]any{"status": status}, err)

	status, created, err := r.do(http.MethodPost, "/v1/crm/grid/people", map[string]any{
		"fields":  map[string]any{"name": "Ada Lovelace", "email": "ada@example.com", "title": "Countess", "stage": "new"},
		"because": "m4 smoke",
	}, jwt)
	if err != nil || status != http.StatusOK {
		r.add("crm_create", false, map[string]any{"status": status}, err)
	} else {
		createdRows := asList(created["rows"])
		record := map[string]any{}
		if len(createdRows) > 0 {
			record = asMap(createdRows[0])
		}
		id, _ := record["id"].(string)
		r.add("crm_create", created["table"] == "crm_people" && id != "" && record["name"] == "Ada Lovelace",
			map[string]any{"id": id, "columns": len(asList(created["columns"])), "total": created["total"]}, err)

		status, got, err := r.do(http.MethodGet, "/v1/crm/grid/people/"+id, nil, jwt)
		gotRows := asList(got["rows"])
		r.add("crm_get", err == nil && status == http.StatusOK && len(gotRows) == 1 && asMap(gotRows[0])["id"] == id,
			map[string]any{"status": status, "rows": len(gotRows)}, err)

		status, queried, err := r.do(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{
			"filter":    []any{map[string]any{"column": "stage", "op": "eq", "value": "new"}},
			"search":    "ada",
			"order_by":  "name",
			"order_dir": "asc",
			"limit":     10,
			"offset":    0,
		}, jwt)
		r.add("crm_query_filter_search", err == nil && status == http.StatusOK && asFloat(queried["total"]) == 1 && asFloat(queried["limit"]) == 10,
			map[string]any{"status": status, "total": queried["total"], "limit": queried["limit"]}, err)

		status, groupBody, err := r.do(http.MethodPost, "/v1/crm/grid/people/group-counts", map[string]any{"group_by": "stage"}, jwt)
		groupRows := asList(groupBody["groups"])
		groupOK := len(groupRows) == 1 && asMap(groupRows[0])["value"] == "new" && asFloat(asMap(groupRows[0])["count"]) == 1
		r.add("crm_group_counts", err == nil && status == http.StatusOK && groupOK, map[string]any{"status": status, "groups": groupRows}, err)

		status, patched, err := r.do(http.MethodPatch, "/v1/crm/grid/people/"+id, map[string]any{"fields": map[string]any{"title": "Mathematician"}}, jwt)
		patchedRows := asList(patched["rows"])
		r.add("crm_patch", err == nil && status == http.StatusOK && len(patchedRows) == 1 && asMap(patchedRows[0])["title"] == "Mathematician",
			map[string]any{"status": status}, err)

		status, staged, err := r.do(http.MethodPost, "/v1/crm/grid/people/"+id+"/stage", map[string]any{"value": "won"}, jwt)
		stagedRows := asList(staged["rows"])
		r.add("crm_stage", err == nil && status == http.StatusOK && len(stagedRows) == 1 && asMap(stagedRows[0])["stage"] == "won",
			map[string]any{"status": status}, err)

		status, deleted, err := r.do(http.MethodDelete, "/v1/crm/grid/people/"+id, map[string]any{"because": "m4 smoke"}, jwt)
		r.add("crm_soft_delete", err == nil && status == http.StatusOK && asFloat(deleted["deleted"]) == 1,
			map[string]any{"status": status, "deleted": deleted["deleted"]}, err)

		status, after, err := r.do(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{"filter": []any{}}, jwt)
		r.add("crm_archived_hidden", err == nil && status == http.StatusOK && asFloat(after["total"]) == 0,
			map[string]any{"status": status, "total": after["total"]}, err)

		status, _, err = r.do(http.MethodGet, "/v1/crm/grid/people/"+id, nil, jwt)
		r.add("crm_archived_get_404", err == nil && status == http.StatusNotFound, map[string]any{"status": status}, err)
	}

	status, body, err = r.do(http.MethodPost, "/v1/crm/grid/invoices/query", map[string]any{"filter": []any{}}, jwt)
	r.add("crm_unknown_entity_404", err == nil && status == http.StatusNotFound && body["error"] == "unknown entity: invoices",
		map[string]any{"status": status, "error": body["error"]}, err)

	status, body, err = r.do(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{"filter": "nope"}, jwt)
	r.add("crm_invalid_filter_400", err == nil && status == http.StatusBadRequest && body["error"] == "filter must be a list",
		map[string]any{"status": status, "error": body["error"]}, err)

	status, body, err = r.do(http.MethodPost, "/v1/crm/grid/people", map[string]any{
		"fields":     map[string]any{"name": "Grace"},
		"identities": map[string]any{"email": "grace@example.com"},
	}, jwt)
	identitiesRejected := false
	if text, ok := body["error"].(string); ok && strings.Contains(text, "identities is not supported") {
		identitiesRejected = true
	}
	r.add("crm_identities_rejected", err == nil && status == http.StatusBadRequest && identitiesRejected,
		map[string]any{"status": status, "error": body["error"]}, err)

	status, tables, err := r.do(http.MethodGet, "/v1/global-db/tables", nil, jwt)
	crmLeaked := false
	for _, item := range asList(tables["tables"]) {
		if name, _ := asMap(item)["name"].(string); strings.HasPrefix(name, "crm_") {
			crmLeaked = true
		}
	}
	r.add("crm_tables_hidden_from_global_db", err == nil && status == http.StatusOK && !crmLeaked,
		map[string]any{"status": status, "tables": len(asList(tables["tables"]))}, err)

	for _, probe := range []struct {
		name   string
		method string
		path   string
	}{
		{"crm_schema_planned_404", http.MethodGet, "/v1/crm/grid/schema"},
		{"crm_claim_planned_404", http.MethodPost, "/v1/crm/claim"},
		{"crm_release_planned_404", http.MethodPost, "/v1/crm/release"},
	} {
		status, _, err = r.do(probe.method, probe.path, map[string]any{"ids": []any{"p1"}}, jwt)
		// schema collides with POST /v1/crm/grid/{entity}, so ServeMux answers
		// 405 there; both 404 and 405 mean "not served".
		unregistered := status == http.StatusNotFound || status == http.StatusMethodNotAllowed
		r.add(probe.name, err == nil && unregistered, map[string]any{"status": status}, err)
	}

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

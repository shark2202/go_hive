package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestCRMGridContractWorkflow(t *testing.T) {
	e := newEnv(t)

	// The CRM surface uses the same operator JWT as the global DB; a missing
	// session is the signed-out 401 with the client's exact message.
	resp, raw := e.do(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{"filter": []any{}}, "", "")
	body := e.decodeJSON(resp, raw, http.StatusUnauthorized)
	if body["error"] != "Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)." {
		t.Fatalf("signed-out body = %v", body)
	}

	// Create: the client body is {fields, identities?, because?}.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people", map[string]any{
		"fields": map[string]any{
			"name":  "Ada Lovelace",
			"email": "ada@example.com",
			"title": "Countess",
			"stage": "new",
		},
		"because": "contract test",
	})
	created := e.decodeJSON(resp, raw, http.StatusOK)
	if created["table"] != "crm_people" || created["total"].(float64) != 1 {
		t.Fatalf("create body = %v", created)
	}
	for _, key := range []string{"table", "columns", "primary_key", "rows", "total", "limit", "offset"} {
		if _, ok := created[key]; !ok {
			t.Fatalf("create body missing %q: %v", key, created)
		}
	}
	rows := created["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("create rows = %v", created["rows"])
	}
	record := rows[0].(map[string]any)
	id, _ := record["id"].(string)
	if id == "" || record["name"] != "Ada Lovelace" || record["stage"] != "new" {
		t.Fatalf("created record = %v", record)
	}
	pk := created["primary_key"].([]any)
	if len(pk) != 1 || pk[0] != "id" {
		t.Fatalf("primary_key = %v", created["primary_key"])
	}

	// Get one record.
	resp, raw = e.authed(http.MethodGet, "/v1/crm/grid/people/"+id, nil)
	got := e.decodeJSON(resp, raw, http.StatusOK)
	if len(got["rows"].([]any)) != 1 || got["rows"].([]any)[0].(map[string]any)["id"] != id {
		t.Fatalf("get body = %v", got)
	}

	// Query with the generic grid body (filter/search/order/pagination).
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{
		"filter": []any{
			map[string]any{"column": "stage", "op": "eq", "value": "new"},
			map[string]any{"column": "name", "op": "contains", "value": "ada"},
		},
		"search":    "lovelace",
		"order_by":  "name",
		"order_dir": "asc",
		"limit":     10,
		"offset":    0,
	})
	queried := e.decodeJSON(resp, raw, http.StatusOK)
	if queried["total"].(float64) != 1 || len(queried["rows"].([]any)) != 1 {
		t.Fatalf("query body = %v", queried)
	}
	if queried["limit"].(float64) != 10 || queried["offset"].(float64) != 0 {
		t.Fatalf("query pagination = %v", queried)
	}

	// Group counts: {table, group_by, groups:[{value,count}]}.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/group-counts", map[string]any{
		"group_by": "stage",
	})
	groups := e.decodeJSON(resp, raw, http.StatusOK)
	if groups["table"] != "crm_people" || groups["group_by"] != "stage" {
		t.Fatalf("group counts = %v", groups)
	}
	groupRows := groups["groups"].([]any)
	if len(groupRows) != 1 || groupRows[0].(map[string]any)["value"] != "new" || groupRows[0].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("groups = %v", groupRows)
	}

	// Patch {fields?, identities?, because?}.
	resp, raw = e.authed(http.MethodPatch, "/v1/crm/grid/people/"+id, map[string]any{
		"fields": map[string]any{"title": "Mathematician"},
	})
	patched := e.decodeJSON(resp, raw, http.StatusOK)
	if patched["rows"].([]any)[0].(map[string]any)["title"] != "Mathematician" {
		t.Fatalf("patched = %v", patched)
	}

	// Stage move {value, because?}.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/"+id+"/stage", map[string]any{
		"value":   "won",
		"because": "contract test",
	})
	staged := e.decodeJSON(resp, raw, http.StatusOK)
	if staged["rows"].([]any)[0].(map[string]any)["stage"] != "won" {
		t.Fatalf("staged = %v", staged)
	}

	// Delete is a soft delete and returns the generic {deleted: n}.
	resp, raw = e.authed(http.MethodDelete, "/v1/crm/grid/people/"+id, nil)
	deleted := e.decodeJSON(resp, raw, http.StatusOK)
	if deleted["deleted"].(float64) != 1 {
		t.Fatalf("delete body = %v", deleted)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{"filter": []any{}})
	after := e.decodeJSON(resp, raw, http.StatusOK)
	if after["total"].(float64) != 0 {
		t.Fatalf("archived row still visible: %v", after)
	}
	resp, raw = e.authed(http.MethodGet, "/v1/crm/grid/people/"+id, nil)
	missing := e.decodeJSON(resp, raw, http.StatusNotFound)
	if missing["error"] != "row not found" {
		t.Fatalf("missing get body = %v", missing)
	}

	// Planned endpoints remain unregistered: no empty 200s.
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/v1/crm/grid/schema"},
		{http.MethodPost, "/v1/crm/claim"},
		{http.MethodPost, "/v1/crm/release"},
	} {
		resp, _ = e.authed(probe.method, probe.path, map[string]any{"ids": []any{"p1"}})
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s = %d, want 404/405", probe.method, probe.path, resp.StatusCode)
		}
	}

	// CRM storage must not leak into the global-db table catalogue.
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/tables", nil)
	tables := e.decodeJSON(resp, raw, http.StatusOK)
	for _, item := range tables["tables"].([]any) {
		name, _ := item.(map[string]any)["name"].(string)
		if strings.HasPrefix(name, "crm_") {
			t.Fatalf("CRM table leaked into global-db tables: %v", tables)
		}
	}
}

func TestCRMGridErrors(t *testing.T) {
	e := newEnv(t)

	// Malformed body.
	resp, raw := e.authed(http.MethodPost, "/v1/crm/grid/people", "not json")
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "invalid JSON body" {
		t.Fatalf("invalid body = %v", body)
	}

	// Unknown entity is 404, not an empty page.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/invoices/query", map[string]any{"filter": []any{}})
	if e.decodeJSON(resp, raw, http.StatusNotFound)["error"] != "unknown entity: invoices" {
		t.Fatalf("unknown entity body = %v", body)
	}

	// Missing fields on create.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people", map[string]any{})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "fields must be a non-empty object" {
		t.Fatalf("missing fields = %v", body)
	}

	// identities is a recovered key, but its shape/dedupe semantics are not;
	// a non-empty value is rejected instead of being guessed at.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people", map[string]any{
		"fields":     map[string]any{"name": "Ada"},
		"identities": map[string]any{"email": "ada@example.com"},
	})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if !strings.Contains(body["error"].(string), "identities is not supported") {
		t.Fatalf("identities body = %v", body)
	}

	// Query validation.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{"filter": "nope"})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "filter must be a list" {
		t.Fatalf("filter body = %v", body)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{
		"filter": []any{map[string]any{"column": "name; DROP TABLE crm_people", "op": "eq", "value": 1}},
	})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if !strings.Contains(body["error"].(string), "invalid identifier") {
		t.Fatalf("injection body = %v", body)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/query", map[string]any{
		"filter": []any{map[string]any{"column": "secret_col", "op": "eq", "value": 1}},
	})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if !strings.Contains(body["error"].(string), "unknown column") {
		t.Fatalf("unknown column body = %v", body)
	}

	// Group counts requires group_by.
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/group-counts", map[string]any{})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "group_by is required" {
		t.Fatalf("group_by body = %v", body)
	}

	// Missing rows and stage values.
	resp, raw = e.authed(http.MethodPatch, "/v1/crm/grid/people/missing", map[string]any{
		"fields": map[string]any{"name": "x"},
	})
	body = e.decodeJSON(resp, raw, http.StatusNotFound)
	if body["error"] != "row not found" {
		t.Fatalf("missing patch body = %v", body)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/crm/grid/people/missing/stage", map[string]any{})
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "value is required" {
		t.Fatalf("stage body = %v", body)
	}

	// A DELETE with a non-JSON body is tolerated (the desktop proxy forwards
	// a null body for DELETE).
	resp, raw = e.authed(http.MethodDelete, "/v1/crm/grid/people/missing", "not json")
	deleted := e.decodeJSON(resp, raw, http.StatusOK)
	if deleted["deleted"].(float64) != 0 {
		t.Fatalf("delete body = %v", deleted)
	}
}

func TestGridQuerySQLCompatibility(t *testing.T) {
	e := newEnv(t)

	resp, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "CREATE TABLE leads (lead_id TEXT PRIMARY KEY, name TEXT, status TEXT, amount INTEGER, email TEXT)",
	})
	e.decodeJSON(resp, raw, http.StatusOK)
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "INSERT INTO leads (lead_id, name, status, amount, email) VALUES " +
			"('l1', 'Alice', 'new', 100, 'alice@example.com'), " +
			"('l2', 'Bobby', 'won', 250, 'bob@example.com'), " +
			"('l3', '50%_off', 'new', 50, 'rick@example.com')",
	})
	e.decodeJSON(resp, raw, http.StatusOK)

	// Exact statements the runtime's grid_query.build_select/build_count/
	// build_group_counts emit for a filtered grid page.
	queries := []struct {
		name      string
		sql       string
		wantRows  float64
		wantValue string
	}{
		{
			name:     "select_filters_sort_pagination",
			sql:      `SELECT * FROM "leads" WHERE "status" = 'new' ORDER BY "amount" DESC LIMIT 50 OFFSET 0`,
			wantRows: 2,
		},
		{
			name:     "select_ilike_search_escape",
			sql:      `SELECT * FROM "leads" WHERE CAST("name" AS TEXT) ILIKE '%50\%\_off%' ESCAPE '\'`,
			wantRows: 1,
		},
		{
			name:     "count_with_search",
			sql:      `SELECT count(*) AS total FROM "leads" WHERE CAST("name" AS TEXT) ILIKE '%bob%' ESCAPE '\'`,
			wantRows: 1,
		},
		{
			name:      "group_counts",
			sql:       `SELECT "status" AS value, count(*) AS count FROM "leads" GROUP BY "status" ORDER BY count(*) DESC, "status" ASC LIMIT 200`,
			wantRows:  2,
			wantValue: "new",
		},
	}
	for _, tc := range queries {
		resp, raw = e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{"sql": tc.sql})
		result := e.decodeJSON(resp, raw, http.StatusOK)
		rows := result["rows"].([]any)
		if float64(len(rows)) != tc.wantRows {
			t.Fatalf("%s rows = %d, want %.0f (%v)", tc.name, len(rows), tc.wantRows, result)
		}
		if tc.wantValue != "" && rows[0].(map[string]any)["value"] != tc.wantValue {
			t.Fatalf("%s first value = %v", tc.name, rows[0])
		}
	}

	// Postgres literals/casts the runtime emits elsewhere must survive the
	// dialect layer.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": `SELECT lead_id, name::text AS label FROM "leads" WHERE "amount" >= 100 AND "status" <> 'won' ORDER BY lead_id`,
	})
	cast := e.decodeJSON(resp, raw, http.StatusOK)
	if len(cast["rows"].([]any)) != 1 || cast["rows"].([]any)[0].(map[string]any)["label"] != "Alice" {
		t.Fatalf("cast query = %v", cast)
	}

	// grid_query.build_delete's statement (the desktop proxy sends it to
	// /v1/global-db/sql, not /query).
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": "INSERT INTO leads (lead_id, name, status, amount, email) VALUES ('abc''123', 'Quoted', 'new', 1, 'q@example.com')",
	})
	e.decodeJSON(resp, raw, http.StatusOK)
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{
		"sql": `DELETE FROM "leads" WHERE "lead_id" = 'abc''123'`,
	})
	e.decodeJSON(resp, raw, http.StatusOK)

	// The read-only escape hatch must not accept grid_query's DELETE.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": `DELETE FROM "leads" WHERE "lead_id" = 'l1'`,
	})
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "only read-only statements are allowed on /query" {
		t.Fatalf("readonly body = %v", body)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/query", map[string]any{
		"sql": `SELECT * FROM "leads"`,
	})
	if len(e.decodeJSON(resp, raw, http.StatusOK)["rows"].([]any)) != 3 {
		t.Fatalf("readonly DELETE leaked through")
	}
}

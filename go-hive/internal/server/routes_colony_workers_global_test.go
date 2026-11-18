package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/globaldb"
)

var globalLeadsMeta = map[string]any{
	"table": "leads",
	"columns": []any{
		map[string]any{"name": "lead_id", "type": "text", "notnull": true, "pk": 1, "dflt_value": nil},
		map[string]any{"name": "name", "type": "text", "notnull": false, "pk": 0, "dflt_value": nil},
		map[string]any{"name": "email", "type": "text", "notnull": false, "pk": 0, "dflt_value": nil},
		map[string]any{"name": "linkedin_url", "type": "text", "notnull": false, "pk": 0, "dflt_value": nil},
	},
	"primary_key": []any{"lead_id"},
	"rows":        []any{},
	"total":       0,
	"limit":       1,
	"offset":      0,
}

var globalChangesPayload = map[string]any{
	"cursor":    "2026-07-20 01:02:03.000004+00",
	"covered":   []any{"interactions", "leads"},
	"truncated": false,
	"tables": map[string]any{
		"leads": map[string]any{"count": 1, "rows": []any{map[string]any{"pk": map[string]any{"lead_id": "x"}, "op": "update"}}},
	},
}

type globalBackendRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

type fakeGlobalBackend struct {
	mu           sync.Mutex
	requests     []globalBackendRequest
	conflict     bool
	errorStatus  int
	errorMessage string
	inserted     any
	total        any
	queryRows    []any
	fkRows       []any
	tableList    any
	crmStatus    any
}

func (f *fakeGlobalBackend) recorded() []globalBackendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]globalBackendRequest(nil), f.requests...)
}

func (f *fakeGlobalBackend) lastRequest(t *testing.T) globalBackendRequest {
	t.Helper()
	requests := f.recorded()
	if len(requests) == 0 {
		t.Fatal("backend received no request")
	}
	return requests[len(requests)-1]
}

func (f *fakeGlobalBackend) requestBody(t *testing.T, request globalBackendRequest) map[string]any {
	t.Helper()
	var decoded map[string]any
	if len(request.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(request.Body, &decoded); err != nil {
		t.Fatalf("decode request body %q: %v", string(request.Body), err)
	}
	return decoded
}

func (f *fakeGlobalBackend) serve(w http.ResponseWriter, r *http.Request) {
	raw := []byte{}
	if r.Body != nil {
		read, _ := io.ReadAll(r.Body)
		raw = read
	}
	f.mu.Lock()
	f.requests = append(f.requests, globalBackendRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Body:   raw,
	})
	f.mu.Unlock()

	write := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		encoded, _ := json.Marshal(body)
		_, _ = w.Write(encoded)
	}

	if f.errorStatus != 0 {
		write(f.errorStatus, map[string]any{"error": map[string]any{"code": "boom", "message": f.errorMessage}})
		return
	}

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/v1/global-db/tables":
		body := f.tableList
		if body == nil {
			body = map[string]any{"tables": []any{map[string]any{"name": "leads", "row_count": 3}}}
		}
		write(http.StatusOK, body)
	case r.Method == http.MethodGet && path == "/v1/global-db/changes":
		write(http.StatusOK, globalChangesPayload)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/rows"):
		write(http.StatusOK, globalLeadsMeta)
	case r.Method == http.MethodPost && path == "/v1/global-db/upsert":
		if f.conflict {
			write(http.StatusConflict, map[string]any{"error": map[string]any{
				"code":    "conflict",
				"message": "A row with this key already exists in 'leads'.",
			}})
			return
		}
		inserted := f.inserted
		if inserted == nil {
			inserted = 1
		}
		write(http.StatusOK, map[string]any{"success": true, "inserted": inserted})
	case r.Method == http.MethodPost && path == "/v1/global-db/query":
		var payload struct {
			SQL string `json:"sql"`
		}
		_ = json.Unmarshal(raw, &payload)
		if strings.Contains(payload.SQL, " AS total ") || strings.HasSuffix(payload.SQL, " AS total") {
			total := f.total
			if total == nil {
				total = 1
			}
			write(http.StatusOK, map[string]any{"rows": []any{map[string]any{"total": total}}})
			return
		}
		if strings.Contains(payload.SQL, "information_schema") {
			rows := f.fkRows
			if rows == nil {
				rows = []any{}
			}
			write(http.StatusOK, map[string]any{"rows": rows})
			return
		}
		rows := f.queryRows
		if rows == nil {
			rows = []any{map[string]any{"id": 1, "name": "Jane"}}
		}
		write(http.StatusOK, map[string]any{"rows": rows})
	case r.Method == http.MethodPost && path == "/v1/global-db/sql":
		write(http.StatusOK, map[string]any{"rowCount": 1})
	case r.Method == http.MethodPatch && strings.HasSuffix(path, "/rows"):
		write(http.StatusOK, map[string]any{"updated": 1})
	case r.Method == http.MethodGet && path == "/v1/crm/grid/schema":
		write(http.StatusOK, f.crmStatusOr(map[string]any{"entities": []any{"people", "organizations"}}))
	case strings.HasPrefix(path, "/v1/crm/"):
		write(http.StatusOK, map[string]any{"proxied": path})
	default:
		write(http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": path}})
	}
}

func (f *fakeGlobalBackend) crmStatusOr(fallback any) any {
	if f.crmStatus != nil {
		return f.crmStatus
	}
	return fallback
}

func newFakeGlobalBackend(t *testing.T) (*fakeGlobalBackend, ColonyWorkerDeps) {
	t.Helper()
	backend := &fakeGlobalBackend{}
	server := httptest.NewServer(http.HandlerFunc(backend.serve))
	t.Cleanup(server.Close)
	deps := ColonyWorkerDeps{
		Manager: NewSessionManager(),
		GlobalDB: func() (*globaldb.Client, error) {
			return globaldb.NewClient(server.URL, "test-jwt"), nil
		},
	}
	return backend, deps
}

// Parity: framework/server/tests/test_global_data_routes.py::test_insert_lead_derives_pk_and_uses_insert_mode
func TestInsertLeadRouteDerivesPKAndUsesInsertMode(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodPost, "/api/global/data/tables/leads/rows", map[string]any{
		"row": map[string]any{"name": "Jane", "linkedin_url": "https://www.linkedin.com/in/jane/"},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body struct {
		Inserted int            `json:"inserted"`
		PK       map[string]any `json:"pk"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body.Inserted != 1 {
		t.Errorf("inserted = %d, want 1", body.Inserted)
	}
	if body.PK["lead_id"] != "linkedin.com/in/jane" {
		t.Errorf("pk = %v, want derived linkedin slug", body.PK)
	}

	upsert := backend.lastRequest(t)
	if upsert.Path != "/v1/global-db/upsert" {
		t.Fatalf("path = %q, want /v1/global-db/upsert", upsert.Path)
	}
	payload := backend.requestBody(t, upsert)
	if payload["mode"] != "insert" {
		t.Errorf("mode = %v, want insert", payload["mode"])
	}
	row, _ := payload["row"].(map[string]any)
	if row["lead_id"] != "linkedin.com/in/jane" {
		t.Errorf("row lead_id = %v, want derived slug", row["lead_id"])
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_insert_lead_without_identifier_400s_before_backend
func TestInsertLeadWithoutIdentifierRoute400s(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodPost, "/api/global/data/tables/leads/rows", map[string]any{
		"row": map[string]any{"name": "No Id"},
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	message, _ := body["error"].(string)
	if !strings.Contains(message, "email or LinkedIn") {
		t.Errorf("error = %v, want the identifier message", body["error"])
	}
	for _, request := range backend.recorded() {
		if request.Path == "/v1/global-db/upsert" {
			t.Fatalf("upsert reached the backend; the request must be rejected locally")
		}
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_insert_conflict_maps_to_409_with_pk
func TestInsertConflictRouteMapsTo409WithPK(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)
	backend.conflict = true

	recorder, raw := colonyWorkerDo(t, deps, http.MethodPost, "/api/global/data/tables/leads/rows", map[string]any{
		"row": map[string]any{"name": "Dup", "email": "dup@x.com"},
	})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", recorder.Code, string(raw))
	}
	var body struct {
		Error string         `json:"error"`
		PK    map[string]any `json:"pk"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body.Error != "conflict" {
		t.Errorf("error = %q, want conflict", body.Error)
	}
	if body.PK["lead_id"] != "dup@x.com" {
		t.Errorf("pk = %v, want attempted lead_id", body.PK)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_changes_init_omits_since
func TestChangesRouteInitOmitsSince(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/global/data/changes", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["cursor"] != globalChangesPayload["cursor"] {
		t.Errorf("cursor = %v, want passthrough", body["cursor"])
	}
	if _, ok := body["tables"]; !ok {
		t.Errorf("body = %v, want the changes envelope passthrough", body)
	}
	request := backend.lastRequest(t)
	if _, ok := request.Query["since"]; ok {
		t.Errorf("query = %v, want no since on the init call", request.Query)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_changes_passes_cursor_through
func TestChangesRoutePassesCursorThrough(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet,
		"/api/global/data/changes?since=2026-07-20+01%3A02%3A03.000004%2B00", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	request := backend.lastRequest(t)
	if request.Query.Get("since") != "2026-07-20 01:02:03.000004+00" {
		t.Errorf("since = %q, want the cursor verbatim", request.Query.Get("since"))
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_changes_signed_out_maps_to_401
func TestChangesRouteSignedOutMapsTo401(t *testing.T) {
	deps := ColonyWorkerDeps{
		Manager: NewSessionManager(),
		GlobalDB: func() (*globaldb.Client, error) {
			return nil, &globaldb.NotSignedInError{Message: "sign in first"}
		},
	}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/global/data/changes", nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["signed_out"] != true {
		t.Errorf("signed_out = %v, want true", body["signed_out"])
	}
	if body["error"] != "sign in first" {
		t.Errorf("error = %v, want sign in first", body["error"])
	}
}

// Parity-exempt: no upstream test drives GET /api/global/data/tables; pins the
// backend passthrough and the best-effort count-cache refresh.
func TestGlobalListTablesRoutePassthrough(t *testing.T) {
	_, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/global/data/tables", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		Tables []map[string]any `json:"tables"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if len(payload.Tables) != 1 || payload.Tables[0]["name"] != "leads" {
		t.Errorf("tables = %v, want the backend payload passthrough", payload.Tables)
	}
}

// Parity-exempt: no upstream test drives GET /api/global/data/tables/{table}/rows;
// pins the four forwarded grid params.
func TestGlobalTableRowsRouteForwardsParams(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet,
		"/api/global/data/tables/leads/rows?limit=5&offset=2&order_by=name&order_dir=desc&ignored=1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	request := backend.lastRequest(t)
	if request.Query.Get("limit") != "5" || request.Query.Get("offset") != "2" ||
		request.Query.Get("order_by") != "name" || request.Query.Get("order_dir") != "desc" {
		t.Errorf("query = %v, want the four grid params forwarded", request.Query)
	}
	if _, ok := request.Query["ignored"]; ok {
		t.Errorf("query = %v, want unknown params dropped", request.Query)
	}
}

// Parity-exempt: no upstream test drives PATCH /api/global/data/tables/{table}/rows;
// pins the {pk, updates} passthrough.
func TestGlobalUpdateRowRoutePassthrough(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodPatch, "/api/global/data/tables/leads/rows", map[string]any{
		"pk":      map[string]any{"lead_id": "x"},
		"updates": map[string]any{"name": "Renamed"},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	request := backend.lastRequest(t)
	payload := backend.requestBody(t, request)
	if request.Method != http.MethodPatch || request.Path != "/v1/global-db/tables/leads/rows" {
		t.Errorf("target = %s %s, want PATCH /v1/global-db/tables/leads/rows", request.Method, request.Path)
	}
	updates, _ := payload["updates"].(map[string]any)
	if updates["name"] != "Renamed" {
		t.Errorf("updates = %v, want the body passthrough", payload["updates"])
	}
}

// Parity-exempt: no upstream test drives POST /api/global/data/tables/{table}/query;
// pins the grid envelope, the order_by allowlist and the total extraction.
func TestGlobalTableQueryRoute(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)
	backend.total = 7

	recorder, raw := colonyWorkerDo(t, deps, http.MethodPost, "/api/global/data/tables/leads/query", map[string]any{
		"filter":    []any{map[string]any{"column": "name", "op": "eq", "value": "Jane"}},
		"search":    "Jan",
		"order_by":  "ghost",
		"order_dir": "desc",
		"limit":     500,
		"offset":    3,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		Table      string           `json:"table"`
		Columns    []map[string]any `json:"columns"`
		PrimaryKey []string         `json:"primary_key"`
		Rows       []map[string]any `json:"rows"`
		Total      int              `json:"total"`
		Limit      int              `json:"limit"`
		Offset     int              `json:"offset"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if payload.Table != "leads" || payload.Total != 7 || payload.Limit != 500 || payload.Offset != 3 {
		t.Fatalf("envelope = %+v, want leads/7/500/3", payload)
	}
	if len(payload.Columns) != 4 || len(payload.PrimaryKey) != 1 || payload.PrimaryKey[0] != "lead_id" {
		t.Errorf("columns/pk = %v/%v, want introspected metadata", payload.Columns, payload.PrimaryKey)
	}
	if len(payload.Rows) != 1 {
		t.Errorf("rows = %v, want the backend rows passthrough", payload.Rows)
	}

	queries := []string{}
	for _, request := range backend.recorded() {
		if request.Path != "/v1/global-db/query" {
			continue
		}
		var decoded struct {
			SQL string `json:"sql"`
		}
		_ = json.Unmarshal(request.Body, &decoded)
		queries = append(queries, decoded.SQL)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %v, want a select plus a count", queries)
	}
	if strings.Contains(queries[0], "ORDER BY") {
		t.Errorf("select = %q, want an unknown order_by dropped", queries[0])
	}
	if !strings.Contains(queries[0], "LIMIT 500 OFFSET 3") {
		t.Errorf("select = %q, want the clamped limit/offset", queries[0])
	}
}

// Parity-exempt: no upstream test drives POST .../group-counts; pins the
// group_by requirement and the {value, count} projection.
func TestGlobalGroupCountsRoute(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)
	backend.queryRows = []any{map[string]any{"value": "won", "count": 4}, map[string]any{"value": "lost", "count": 1}}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodPost, "/api/global/data/tables/leads/group-counts", map[string]any{
		"group_by": "name",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		Table   string `json:"table"`
		GroupBy string `json:"group_by"`
		Groups  []struct {
			Value any `json:"value"`
			Count int `json:"count"`
		} `json:"groups"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if payload.Table != "leads" || payload.GroupBy != "name" {
		t.Errorf("envelope = %+v, want leads/name", payload)
	}
	if len(payload.Groups) != 2 || payload.Groups[0].Value != "won" || payload.Groups[0].Count != 4 {
		t.Errorf("groups = %+v, want the backend rows projected", payload.Groups)
	}

	missing, rawMissing := colonyWorkerDo(t, deps, http.MethodPost, "/api/global/data/tables/leads/group-counts", map[string]any{})
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", missing.Code, string(rawMissing))
	}
	var missingBody map[string]any
	decodeColonyWorkerBody(t, rawMissing, &missingBody)
	if missingBody["error"] != "group_by is required" {
		t.Errorf("error = %v, want group_by is required", missingBody["error"])
	}
}

// Parity-exempt: no upstream test drives DELETE /api/global/data/tables/{table}/rows;
// pins the pk requirement, the composite-pk validation and the {"deleted": n} envelope.
func TestGlobalDeleteRowRoute(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodDelete, "/api/global/data/tables/leads/rows", map[string]any{
		"pk": map[string]any{"lead_id": "x"},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body struct {
		Deleted any `json:"deleted"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body.Deleted != float64(1) {
		t.Errorf("deleted = %v, want 1", body.Deleted)
	}
	deleteRequest := backend.lastRequest(t)
	if deleteRequest.Path != "/v1/global-db/sql" {
		t.Errorf("path = %q, want the raw-SQL endpoint", deleteRequest.Path)
	}
	var deletePayload struct {
		SQL string `json:"sql"`
	}
	_ = json.Unmarshal(deleteRequest.Body, &deletePayload)
	if !strings.Contains(deletePayload.SQL, `DELETE FROM "leads" WHERE "lead_id" = 'x'`) {
		t.Errorf("sql = %q, want the built delete", deletePayload.SQL)
	}

	for _, testCase := range []struct {
		name string
		body map[string]any
		want string
	}{
		{name: "empty pk", body: map[string]any{"pk": map[string]any{}}, want: "pk must be a non-empty object"},
		{name: "missing pk column", body: map[string]any{"pk": map[string]any{"name": "x"}}, want: "missing primary key columns: ['lead_id']"},
	} {
		response, payloadRaw := colonyWorkerDo(t, deps, http.MethodDelete, "/api/global/data/tables/leads/rows", testCase.body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", testCase.name, response.Code)
			continue
		}
		var failure map[string]any
		decodeColonyWorkerBody(t, payloadRaw, &failure)
		if failure["error"] != testCase.want {
			t.Errorf("%s: error = %v, want %s", testCase.name, failure["error"], testCase.want)
		}
	}
}

// Parity-exempt: no upstream test drives GET /api/global/data/foreign-keys;
// pins the information_schema projection.
func TestGlobalForeignKeysRoute(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)
	backend.fkRows = []any{map[string]any{
		"table_name":  "interactions",
		"column_name": "lead_id",
		"ref_table":   "leads",
		"ref_column":  "lead_id",
	}}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/global/data/foreign-keys", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		ForeignKeys []map[string]any `json:"foreign_keys"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if len(payload.ForeignKeys) != 1 {
		t.Fatalf("foreign_keys = %v, want one entry", payload.ForeignKeys)
	}
	entry := payload.ForeignKeys[0]
	if entry["table"] != "interactions" || entry["column"] != "lead_id" ||
		entry["ref_table"] != "leads" || entry["ref_column"] != "lead_id" {
		t.Errorf("entry = %v, want the mapped keys", entry)
	}
}

// Parity-exempt: framework.crm ships with the desktop build only; upstream has
// no HTTP test for the 501 degradation.
func TestGlobalCRMStatusUnavailable(t *testing.T) {
	deps := ColonyWorkerDeps{Manager: NewSessionManager()}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/global/crm/status", nil)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["code"] != "crm_unavailable" {
		t.Errorf("code = %v, want crm_unavailable", body["code"])
	}
}

// Parity-exempt: no upstream test drives the CRM status error mapping; pins the
// exit-code to status ladder.
func TestGlobalCRMStatusMapsErrors(t *testing.T) {
	cases := []struct {
		exitCode  int
		status    int
		signedOut bool
	}{
		{exitCode: crmExitNotSignedIn, status: http.StatusUnauthorized, signedOut: true},
		{exitCode: crmExitPermission, status: http.StatusForbidden},
		{exitCode: crmExitNotFound, status: http.StatusNotFound},
		{exitCode: 99, status: http.StatusBadRequest},
	}
	for _, testCase := range cases {
		exitCode := testCase.exitCode
		deps := ColonyWorkerDeps{
			Manager: NewSessionManager(),
			CRMStatus: func() (any, error) {
				return nil, &CRMError{Message: "crm failed", Code: "crm_code", ExitCode: exitCode}
			},
		}
		recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/global/crm/status", nil)
		if recorder.Code != testCase.status {
			t.Errorf("exit %d: status = %d, want %d (%s)", exitCode, recorder.Code, testCase.status, string(raw))
			continue
		}
		var body map[string]any
		decodeColonyWorkerBody(t, raw, &body)
		if body["error"] != "crm failed" || body["code"] != "crm_code" || body["signed_out"] != testCase.signedOut {
			t.Errorf("exit %d: body = %v, want the CRM envelope", exitCode, body)
		}
	}

	okDeps := ColonyWorkerDeps{
		Manager:   NewSessionManager(),
		CRMStatus: func() (any, error) { return map[string]any{"configured": true}, nil },
	}
	recorder, raw := colonyWorkerDo(t, okDeps, http.MethodGet, "/api/global/crm/status", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["configured"] != true {
		t.Errorf("body = %v, want the setup_status passthrough", body)
	}
}

// Parity-exempt: no upstream test drives the CRM grid proxy; pins path quoting,
// method forwarding and the body passthrough.
func TestCRMGridProxyRoutes(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/crm/grid/schema", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("schema status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	if backend.lastRequest(t).Path != "/v1/crm/grid/schema" {
		t.Errorf("path = %q, want /v1/crm/grid/schema", backend.lastRequest(t).Path)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodPost, "/api/crm/grid/people/query", map[string]any{"filter": []any{}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("query status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	request := backend.lastRequest(t)
	if request.Path != "/v1/crm/grid/people/query" || request.Method != http.MethodPost {
		t.Errorf("target = %s %s, want POST /v1/crm/grid/people/query", request.Method, request.Path)
	}
	if payload := backend.requestBody(t, request); payload == nil || payload["filter"] == nil {
		t.Errorf("body = %v, want the request body forwarded", payload)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodPatch, "/api/crm/grid/people/p-1", map[string]any{"fields": map[string]any{"name": "X"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	if request := backend.lastRequest(t); request.Path != "/v1/crm/grid/people/p-1" || request.Method != http.MethodPatch {
		t.Errorf("target = %s %s, want PATCH /v1/crm/grid/people/p-1", request.Method, request.Path)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodPost, "/api/crm/grid/people/p-1/stage", map[string]any{"value": "won"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("stage status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	if path := backend.lastRequest(t).Path; path != "/v1/crm/grid/people/p-1/stage" {
		t.Errorf("path = %q, want the stage path", path)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodDelete, "/api/crm/grid/people/p-1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	if request := backend.lastRequest(t); request.Method != http.MethodDelete || request.Path != "/v1/crm/grid/people/p-1" {
		t.Errorf("target = %s %s, want DELETE /v1/crm/grid/people/p-1", request.Method, request.Path)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodPost, "/api/crm/claim", map[string]any{"ids": []any{"p-1"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	if path := backend.lastRequest(t).Path; path != "/v1/crm/claim" {
		t.Errorf("path = %q, want /v1/crm/claim", path)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodPost, "/api/crm/release", map[string]any{"ids": []any{"p-1"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("release status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	if path := backend.lastRequest(t).Path; path != "/v1/crm/release" {
		t.Errorf("path = %q, want /v1/crm/release", path)
	}
}

// Parity-exempt: no upstream test drives the CRM proxy's invalid-body branch;
// pins the 400 for a non-DELETE method and the tolerated DELETE body.
func TestCRMGridProxyBodyHandling(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)

	mux := http.NewServeMux()
	RegisterColonyWorkerRoutes(mux, deps)
	request := httptest.NewRequest(http.MethodPost, "/api/crm/grid/people", strings.NewReader("not json"))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	decodeColonyWorkerBody(t, recorder.Body.Bytes(), &body)
	if body["error"] != "invalid JSON body" {
		t.Errorf("error = %v, want invalid JSON body", body["error"])
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/crm/grid/people/p-1", strings.NewReader("not json"))
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	if path := backend.lastRequest(t).Path; path != "/v1/crm/grid/people/p-1" {
		t.Errorf("path = %q, want the delete forwarded with a null body", path)
	}
}

// Parity-exempt: no upstream test drives the global-db error envelope; pins the
// GlobalDbError status passthrough and the generic 502 fallback.
func TestGlobalRoutesMapBackendErrors(t *testing.T) {
	backend, deps := newFakeGlobalBackend(t)
	backend.errorStatus = http.StatusNotFound
	backend.errorMessage = "no such table"

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/global/data/tables", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["error"] != "no such table" {
		t.Errorf("error = %v, want no such table", body["error"])
	}

	fallback := ColonyWorkerDeps{
		Manager: NewSessionManager(),
		GlobalDB: func() (*globaldb.Client, error) {
			return nil, errors.New("unexpected")
		},
	}
	recorder, raw = colonyWorkerDo(t, fallback, http.MethodGet, "/api/global/data/tables", nil)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", recorder.Code, string(raw))
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body["error"] != "global_db_proxy_error" {
		t.Errorf("error = %v, want global_db_proxy_error", body["error"])
	}
}

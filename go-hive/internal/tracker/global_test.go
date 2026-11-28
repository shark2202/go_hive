package tracker

// Parity: core/framework/tools/tracker_tools.py::_global_sql
// Parity: core/framework/tools/tracker_tools.py::_global_upsert
// Parity: core/framework/tools/tracker_tools.py::_global_register_info

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
)

func globalTools(t *testing.T, b *Binding, provider GlobalDBProvider) map[string]Tool {
	t.Helper()
	tools := map[string]Tool{}
	for _, tool := range BuildTrackerToolsWithGlobalDB(func() (*Binding, error) { return b, nil }, provider) {
		tools[tool.Name] = tool
	}
	return tools
}

// Parity: core/framework/tools/tracker_tools.py::_global_sql — the cloud
// {columns, rows(objects), rowCount, truncated} envelope is reshaped to the
// colony tracker's positional-rows contract.
func TestGlobalScopeSQLShapesBackendResponse(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "jwt test-jwt" {
			t.Errorf("Authorization = %q, want jwt test-jwt", got)
		}
		paths = append(paths, r.URL.Path)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["sql"] != "SELECT id, name FROM people" {
			t.Errorf("sql = %v", body["sql"])
		}
		if body["row_cap"] != float64(250) {
			t.Errorf("row_cap = %v, want 250", body["row_cap"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"columns":["id","name"],"rows":[{"id":1,"name":"Ada"}],"rowCount":2,"truncated":true}`))
	}))
	defer server.Close()

	tools := globalTools(t, testBinding(t), func() (*globaldb.Client, error) {
		return globaldb.NewClient(server.URL, "test-jwt"), nil
	})

	res := call(t, tools, "tracker_sql", map[string]any{
		"sql": "SELECT id, name FROM people", "row_cap": 250, "scope": "global",
	})
	requireSuccess(t, res)
	if res["kind"] != "rows" {
		t.Fatalf("kind = %v, want rows", res["kind"])
	}
	cols, ok := res["columns"].([]string)
	if !ok || strings.Join(cols, ",") != "id,name" {
		t.Fatalf("columns = %#v", res["columns"])
	}
	requireRows(t, res, [][]any{{float64(1), "Ada"}})
	if !numberEq(res["rowcount"], 2) || res["truncated"] != true {
		t.Fatalf("rowcount/truncated = %v/%v", res["rowcount"], res["truncated"])
	}

	res = call(t, tools, "tracker_query", map[string]any{
		"sql": "SELECT id, name FROM people", "row_cap": 250, "scope": "global",
	})
	requireSuccess(t, res)
	if len(paths) != 2 || paths[0] != "/v1/global-db/sql" || paths[1] != "/v1/global-db/query" {
		t.Fatalf("paths = %v", paths)
	}
}

// Parity: core/framework/tools/tracker_tools.py::_global_upsert — the
// ambient colony name travels as source_colony and `inserted` becomes
// rowcount.
func TestGlobalScopeUpsertSendsSourceColony(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/global-db/upsert" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"inserted":3}`))
	}))
	defer server.Close()

	b := &Binding{Name: "queen-colony"}
	tools := globalTools(t, b, func() (*globaldb.Client, error) {
		return globaldb.NewClient(server.URL, "test-jwt"), nil
	})
	res := call(t, tools, "tracker_upsert", map[string]any{
		"table": "people", "row": map[string]any{"id": 1, "name": "Ada"}, "scope": "global",
	})
	requireSuccess(t, res)
	if res["table"] != "people" || !numberEq(res["rowcount"], 3) {
		t.Fatalf("res = %+v", res)
	}
	if body["source_colony"] != "queen-colony" {
		t.Fatalf("source_colony = %v", body["source_colony"])
	}
	row, _ := body["row"].(map[string]any)
	if row["name"] != "Ada" {
		t.Fatalf("row = %v", body["row"])
	}
}

// Parity: core/framework/tools/tracker_tools.py::_global_sql — without
// HIVE_CLOUD_JWT/HIVE_CLOUD_BASE the call fails with the Python client's
// sign-in message instead of pretending a local global DB exists.
func TestGlobalScopeUnsignedHostReturnsSignInError(t *testing.T) {
	t.Setenv(globaldb.EnvJWT, "")
	t.Setenv(globaldb.EnvBase, "")
	tools := globalTools(t, nil, nil)
	for _, name := range []string{"tracker_sql", "tracker_query"} {
		res := call(t, tools, name, map[string]any{"sql": "SELECT 1", "scope": "global"})
		requireFailure(t, res, "Sign in to use the shared global DB", "HIVE_CLOUD_JWT")
	}
	res := call(t, tools, "tracker_upsert", map[string]any{
		"table": "people", "row": map[string]any{"id": 1}, "scope": "global",
	})
	requireFailure(t, res, "Sign in to use the shared global DB", "HIVE_CLOUD_JWT")
}

// Parity: core/framework/tools/tracker_tools.py::_global_sql — backend
// non-2xx errors are wrapped with Python's "global DB error:" prefix.
func TestGlobalScopeBackendError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"relation \"nope\" does not exist"}}`))
	}))
	defer server.Close()

	tools := globalTools(t, nil, func() (*globaldb.Client, error) {
		return globaldb.NewClient(server.URL, "test-jwt"), nil
	})
	res := call(t, tools, "tracker_query", map[string]any{"sql": "SELECT * FROM nope", "scope": "global"})
	requireFailure(t, res, `global DB error: relation "nope" does not exist`)
}

// Parity: core/framework/tools/tracker_tools.py::_global_sql — field
// validation happens before the cloud client is touched.
func TestGlobalScopeValidation(t *testing.T) {
	called := false
	tools := globalTools(t, nil, func() (*globaldb.Client, error) {
		called = true
		return nil, &globaldb.NotSignedInError{Message: "should not be reached"}
	})
	requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": "  ", "scope": "global"}), "'sql' is required")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{
		"table": "", "row": map[string]any{"id": 1}, "scope": "global",
	}), "table is required")
	requireFailure(t, call(t, tools, "tracker_upsert", map[string]any{
		"table": "people", "row": map[string]any{}, "scope": "global",
	}), "row must be a non-empty object")
	if called {
		t.Fatal("cloud client built before input validation")
	}
}

// Parity: core/framework/tools/tracker_tools.py — every executor checks
// _scope_of(inputs) before _require_binding, so a queen working outside a
// colony still reaches the signed-in cloud DB. The colony binding only
// supplies the ambient source_colony on upserts (Python's
// _ambient_colony_name), and its absence/failure must not block global calls.
func TestGlobalScopeDoesNotRequireColonyBinding(t *testing.T) {
	var upsertBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/global-db/sql", "/v1/global-db/query":
			_, _ = w.Write([]byte(`{"columns":["n"],"rows":[{"n":1}],"rowCount":1}`))
		case "/v1/global-db/upsert":
			if err := json.NewDecoder(r.Body).Decode(&upsertBody); err != nil {
				t.Errorf("decode upsert body: %v", err)
			}
			_, _ = w.Write([]byte(`{"inserted":1}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	resolveCalls := 0
	resolveErr := errors.New("colony binding unavailable")
	tools := map[string]Tool{}
	for _, tool := range BuildTrackerToolsWithGlobalDB(func() (*Binding, error) {
		resolveCalls++
		return nil, resolveErr
	}, func() (*globaldb.Client, error) {
		return globaldb.NewClient(server.URL, "test-jwt"), nil
	}) {
		tools[tool.Name] = tool
	}

	// Python normalizes scope case/whitespace via _scope_of; global calls
	// must not even ask for a colony binding.
	for _, name := range []string{"tracker_sql", "tracker_query"} {
		res := call(t, tools, name, map[string]any{"sql": "SELECT 1", "scope": " Global "})
		requireSuccess(t, res)
		if res["kind"] != "rows" {
			t.Fatalf("%s kind = %v, want rows", name, res["kind"])
		}
	}
	if resolveCalls != 0 {
		t.Fatalf("global sql/query resolved a colony binding %d times, want 0", resolveCalls)
	}

	requireSuccess(t, call(t, tools, "tracker_register_writable", map[string]any{
		"table": "people", "write_columns": []any{"name"}, "key_columns": []any{"id"}, "scope": "global",
	}))
	if resolveCalls != 0 {
		t.Fatalf("global register resolved a colony binding %d times, want 0", resolveCalls)
	}

	// Upsert reads the ambient binding for source_colony, but a resolver
	// failure just means "no source colony" — not a failed call.
	requireSuccess(t, call(t, tools, "tracker_upsert", map[string]any{
		"table": "people", "row": map[string]any{"id": 1}, "scope": "global",
	}))
	if _, present := upsertBody["source_colony"]; present {
		t.Fatalf("source_colony = %v, want omitted without an ambient binding", upsertBody["source_colony"])
	}
	if resolveCalls != 1 {
		t.Fatalf("global upsert resolved the ambient binding %d times, want 1", resolveCalls)
	}

	// Colony scope still surfaces the resolver failure instead of falling
	// back to a fake local DB.
	requireFailure(t, call(t, tools, "tracker_sql", map[string]any{"sql": "SELECT 1"}), "colony binding unavailable")
}

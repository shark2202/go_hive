package integrations

import (
	"net/http"
	"strings"
	"testing"
)

func batch5SupabaseRegistry(t *testing.T, baseURL, key string) *Registry {
	t.Helper()
	options := []Option{WithBaseURL("supabase_tool", baseURL)}
	if key != "" {
		options = append(options, WithCredential("supabase_tool", key))
	}
	options = append(options, WithGetenv(func(string) string { return "" }))
	return newBatch5Lane2Registry(options...)
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_select
func TestBatch5Lane2SupabaseSelect(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(request *http.Request, record batch5HTTPRecord) (int, string) {
		if record.Path != "/rest/v1/users" {
			return http.StatusNotFound, `{"message":"not found"}`
		}
		return http.StatusOK, `[{"id":1,"name":"Alice"},{"id":2,"name":"Bob"}]`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action":  "supabase_select",
		"table":   "users",
		"columns": "id,name",
		"filters": "status=eq.active&role=eq.admin",
		"order":   "created_at.desc",
		"limit":   50,
		"offset":  10,
	})
	batch5AssertNoError(t, result)
	if result["table"] != "users" || result["count"] != 2 {
		t.Fatalf("result = %v", result)
	}
	record := recorder.last()
	if record.Method != http.MethodGet {
		t.Fatalf("method = %s", record.Method)
	}
	query := record.Query
	if query.Get("select") != "id,name" || query.Get("limit") != "50" || query.Get("offset") != "10" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("status") != "eq.active" || query.Get("role") != "eq.admin" || query.Get("order") != "created_at.desc" {
		t.Fatalf("filters/order = %v", query)
	}
	if record.Header.Get("apikey") != "anon-key" || record.Header.Get("Authorization") != "Bearer anon-key" {
		t.Fatalf("headers = %v", record.Header)
	}
	if record.Header.Get("Prefer") != "return=representation" {
		t.Fatalf("prefer = %q", record.Header.Get("Prefer"))
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_with_filters
func TestBatch5Lane2SupabaseSelectLimitClamp(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `[]`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_select", "table": "users", "limit": 5000,
	})
	batch5AssertNoError(t, result)
	if recorder.last().Query.Get("limit") != "1000" {
		t.Fatalf("limit = %q", recorder.last().Query.Get("limit"))
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_insert
func TestBatch5Lane2SupabaseInsert(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusCreated, `[{"id":3,"name":"Carol"}]`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_insert", "table": "users", "rows": `{"name":"Carol"}`,
	})
	batch5AssertNoError(t, result)
	record := recorder.last()
	if record.Method != http.MethodPost || record.Path != "/rest/v1/users" {
		t.Fatalf("request = %s %s", record.Method, record.Path)
	}
	if record.Body["name"] != "Carol" {
		t.Fatalf("body = %v", record.Body)
	}
	inserted, _ := result["inserted"].([]any)
	if len(inserted) != 1 {
		t.Fatalf("inserted = %v", result["inserted"])
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_update
func TestBatch5Lane2SupabaseUpdate(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `[{"id":1,"status":"done"}]`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_update", "table": "tasks", "filters": "id=eq.1", "data": `{"status":"done"}`,
	})
	batch5AssertNoError(t, result)
	record := recorder.last()
	if record.Method != http.MethodPatch {
		t.Fatalf("method = %s", record.Method)
	}
	if record.Query.Get("id") != "eq.1" || record.Body["status"] != "done" {
		t.Fatalf("query/body = %v / %v", record.Query, record.Body)
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_delete
func TestBatch5Lane2SupabaseDelete(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `[{"id":1}]`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_delete", "table": "tasks", "filters": "id=eq.1",
	})
	batch5AssertNoError(t, result)
	if recorder.last().Method != http.MethodDelete {
		t.Fatalf("method = %s", recorder.last().Method)
	}
	deleted, _ := result["deleted"].([]any)
	if len(deleted) != 1 {
		t.Fatalf("deleted = %v", result["deleted"])
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_missing_credentials
func TestBatch5Lane2SupabaseMissingConfig(t *testing.T) {
	registry := newBatch5Lane2Registry(WithGetenv(func(string) string { return "" }))
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_select", "table": "users",
	})
	batch5AssertErrorContains(t, result, "SUPABASE_ANON_KEY or SUPABASE_URL not set")
	if help, _ := result["help"].(string); !strings.Contains(help, "supabase.com/dashboard") {
		t.Fatalf("help = %v", result["help"])
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_invalid_json
func TestBatch5Lane2SupabaseValidation(t *testing.T) {
	server, _ := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `[]`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	cases := []struct {
		action string
		input  map[string]any
		want   string
	}{
		{action: "supabase_select", input: map[string]any{}, want: "table is required"},
		{action: "supabase_insert", input: map[string]any{"table": "u"}, want: "table and rows are required"},
		{action: "supabase_insert", input: map[string]any{"table": "u", "rows": "{bad"}, want: "Invalid JSON in rows"},
		{action: "supabase_update", input: map[string]any{"table": "u", "filters": "id=eq.1"}, want: "table, filters, and data are required"},
		{action: "supabase_update", input: map[string]any{"table": "u", "filters": "id=eq.1", "data": "{bad"}, want: "Invalid JSON in data"},
		{action: "supabase_delete", input: map[string]any{"table": "u"}, want: "table and filters are required"},
		{action: "supabase_auth_signup", input: map[string]any{"email": "a@b.c"}, want: "email and password are required"},
		{action: "supabase_auth_signup", input: map[string]any{"email": "a@b.c", "password": "12345"}, want: "password must be at least 6 characters"},
		{action: "supabase_auth_signin", input: map[string]any{}, want: "email and password are required"},
		{action: "supabase_edge_invoke", input: map[string]any{}, want: "function_name is required"},
	}
	for _, testCase := range cases {
		input := map[string]any{"action": testCase.action}
		for key, value := range testCase.input {
			input[key] = value
		}
		result := batch5ExecuteRegistry(t, registry, "supabase", input)
		batch5AssertErrorContains(t, result, testCase.want)
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_signup
func TestBatch5Lane2SupabaseAuthSignup(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{"user":{"id":"u1","email":"a@b.c","confirmed_at":"2026-01-01T00:00:00Z"}}`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_auth_signup", "email": "a@b.c", "password": "secret1",
	})
	batch5AssertNoError(t, result)
	if result["user_id"] != "u1" || result["email"] != "a@b.c" || result["confirmed"] != true {
		t.Fatalf("result = %v", result)
	}
	record := recorder.last()
	if record.Path != "/auth/v1/signup" || record.Body["email"] != "a@b.c" || record.Body["password"] != "secret1" {
		t.Fatalf("request = %s %v", record.Path, record.Body)
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_signin
func TestBatch5Lane2SupabaseAuthSignin(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{"user":{"id":"u1","email":"a@b.c"},"expires_in":3600}`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_auth_signin", "email": "a@b.c", "password": "secret1",
	})
	batch5AssertNoError(t, result)
	if result["authenticated"] != true || result["user_id"] != "u1" || result["expires_in"] != 3600 {
		t.Fatalf("result = %v", result)
	}
	if recorder.last().Query.Get("grant_type") != "password" {
		t.Fatalf("query = %v", recorder.last().Query)
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_invoke
func TestBatch5Lane2SupabaseEdgeInvoke(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(request *http.Request, record batch5HTTPRecord) (int, string) {
		if record.Path != "/functions/v1/hello" {
			return http.StatusNotFound, `{"message":"missing"}`
		}
		if request.Method == http.MethodGet {
			return http.StatusOK, `{"greeting":"hi"}`
		}
		return http.StatusOK, `{"ok":true}`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_edge_invoke", "function_name": "hello", "body": `{"name":"Ada"}`,
	})
	batch5AssertNoError(t, result)
	if result["status_code"] != 200 {
		t.Fatalf("result = %v", result)
	}
	response, _ := result["response"].(map[string]any)
	if response["ok"] != true {
		t.Fatalf("response = %v", result["response"])
	}
	if recorder.last().Body["name"] != "Ada" {
		t.Fatalf("body = %v", recorder.last().Body)
	}
	getResult := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_edge_invoke", "function_name": "hello", "method": "GET",
	})
	batch5AssertNoError(t, getResult)
	if recorder.last().Method != http.MethodGet {
		t.Fatalf("method = %s", recorder.last().Method)
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_successful_invoke
func TestBatch5Lane2SupabaseEdgeError(t *testing.T) {
	server, _ := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusInternalServerError, `{"message":"boom"}`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_edge_invoke", "function_name": "hello",
	})
	batch5AssertErrorContains(t, result, "Edge function error 500")
	if result["response"] == nil {
		t.Fatalf("response missing: %v", result)
	}
}

// Parity: tools/tests/tools/test_supabase_tool.py::test_invalid_body_json
func TestBatch5Lane2SupabaseEdgeInvalidBody(t *testing.T) {
	server, _ := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{}`
	})
	registry := batch5SupabaseRegistry(t, server.URL, "anon-key")
	result := batch5ExecuteRegistry(t, registry, "supabase", map[string]any{
		"action": "supabase_edge_invoke", "function_name": "hello", "body": "{bad",
	})
	batch5AssertErrorContains(t, result, "Invalid JSON in body")
}

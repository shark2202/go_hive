package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// --------------------------------------------------------------- test kit ---

type batch6LoggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

// batch6Recorder wraps a fake upstream and keeps every request for assertions.
type batch6Recorder struct {
	mu       sync.Mutex
	requests []batch6LoggedRequest
}

func (rec *batch6Recorder) wrap(next func(http.ResponseWriter, *http.Request, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch6LoggedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		})
		rec.mu.Unlock()
		next(w, r, body)
	}
}

func (rec *batch6Recorder) at(t *testing.T, index int) batch6LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch6Recorder) last(t *testing.T) batch6LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch6Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch6TestRegistry builds a lane-1 registry pointed at one fake upstream.
func batch6TestRegistry(t *testing.T, module string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch6Recorder) {
	t.Helper()
	recorder := &batch6Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
	}
	return newBatch6Lane1Registry(append(base, opts...)...), recorder
}

func batch6Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

func batch6WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch6ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

// batch6Int accepts the int a handler returns in-process and the float64 a
// JSON round-trip would produce.
func batch6Int(t *testing.T, value any) int {
	t.Helper()
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		t.Fatalf("%v is %T, want a number", value, value)
		return 0
	}
}

func batch6BodyMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%v is %T, want map", value, value)
	}
	return object
}

// batch6Route is one fake upstream endpoint keyed by "METHOD /path".
type batch6Route struct {
	Status int
	Body   any
}

// batch6Routes answers by method+path and fails loudly on an unexpected call.
func batch6Routes(t *testing.T, routes map[string]batch6Route) func(http.ResponseWriter, *http.Request, map[string]any) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		route, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			batch6WriteJSON(w, http.StatusNotFound, map[string]any{
				"message": "unexpected " + r.Method + " " + r.URL.Path,
			})
			return
		}
		status := route.Status
		if status == 0 {
			status = http.StatusOK
		}
		batch6WriteJSON(w, status, route.Body)
	}
}

// ---------------------------------------------------------------- catalog ---

// Parity: tools/tests/tools/test_attio_tool.py::TestToolRegistration::test_tool_count
func TestBatch6Lane1CatalogIntegrity(t *testing.T) {
	registry := newBatch6Lane1Registry()
	if got := len(registry.Names()); got != 4 {
		t.Fatalf("tools = %d, want 4", got)
	}
	if got := registry.ActionCount(); got != 43 {
		t.Fatalf("actions = %d, want 43", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	want := map[string]int{"salesforce": 9, "pipedrive": 13, "zoho_crm": 6, "attio": 15}
	for _, tool := range registry.Tools() {
		if want[tool.Name] != len(tool.Actions) {
			t.Fatalf("%s has %d actions, want %d", tool.Name, len(tool.Actions), want[tool.Name])
		}
		for _, action := range tool.Actions {
			if registry.impls[tool.Name][action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", tool.Name, action.Name)
			}
		}
	}
}

// ------------------------------------------------------------- salesforce ---

// batch6SalesforceServer answers the versioned Salesforce REST paths.
func batch6SalesforceServer(t *testing.T, responses map[string]any) func(http.ResponseWriter, *http.Request, map[string]any) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		payload, ok := responses[r.URL.Path]
		if !ok {
			batch6WriteJSON(w, http.StatusNotFound, map[string]any{"message": "unexpected path " + r.URL.Path})
			return
		}
		if status, isStatus := payload.(int); isStatus {
			batch6WriteJSON(w, status, map[string]any{"message": "failure"})
			return
		}
		batch6WriteJSON(w, http.StatusOK, payload)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceSOQLQuery::test_successful_query
func TestBatch6SalesforceSOQLQuery(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/query/": map[string]any{
			"totalSize":      2,
			"done":           true,
			"records":        []any{map[string]any{"Id": "003-1", "Name": "Acme"}},
			"nextRecordsUrl": "/services/data/v62.0/query/01g-2000",
		},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_soql_query",
		"query":  "SELECT Id, Name FROM Lead LIMIT 10",
	})
	if batch6Int(t, result["total_size"]) != 2 || result["done"] != true {
		t.Fatalf("result = %v", result)
	}
	if records := result["records"].([]any); len(records) != 1 {
		t.Fatalf("records = %v", records)
	}
	if result["next_records_url"] != "/services/data/v62.0/query/01g-2000" {
		t.Fatalf("next_records_url = %v", result["next_records_url"])
	}
	request := recorder.last(t)
	if request.Method != http.MethodGet || request.Query.Get("q") != "SELECT Id, Name FROM Lead LIMIT 10" {
		t.Fatalf("request = %+v", request)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceSOQLQuery::test_pagination
func TestBatch6SalesforceSOQLPagination(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/query/01g-2000": map[string]any{
			"totalSize": 2, "done": true,
			"records": []any{map[string]any{"Id": "003-2"}},
		},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action":           "salesforce_soql_query",
		"next_records_url": "/services/data/v62.0/query/01g-2000",
	})
	if batch6Int(t, result["total_size"]) != 2 {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Path; got != "/services/data/v62.0/query/01g-2000" {
		t.Fatalf("path = %q", got)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceSOQLQuery::test_missing_query
func TestBatch6SalesforceSOQLRequiresQueryOrPage(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, nil))
	result := batch6Execute(t, registry, "salesforce", map[string]any{"action": "salesforce_soql_query"})
	if got := batch6ErrorText(t, result); got != "Either query or next_records_url is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceGetRecord::test_successful_get
func TestBatch6SalesforceGetRecord(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/sobjects/Lead/00Q-1": map[string]any{"Id": "00Q-1", "LastName": "Doe"},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_get_record", "object_type": "Lead", "record_id": "00Q-1", "fields": "Id,LastName",
	})
	if result["LastName"] != "Doe" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Query.Get("fields") != "Id,LastName" || request.Method != http.MethodGet {
		t.Fatalf("request = %+v", request)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceGetRecord::test_missing_params
func TestBatch6SalesforceGetRecordRequiresIDs(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, nil))
	result := batch6Execute(t, registry, "salesforce", map[string]any{"action": "salesforce_get_record", "object_type": "Lead"})
	if got := batch6ErrorText(t, result); got != "object_type and record_id are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceCreateRecord::test_successful_create
func TestBatch6SalesforceCreateRecord(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/sobjects/Lead": map[string]any{"id": "00Q-9", "success": true, "errors": []any{}},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_create_record", "object_type": "Lead",
		"fields": map[string]any{"LastName": "Doe", "Company": "Acme"},
	})
	if result["success"] != true || result["id"] != "00Q-9" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost {
		t.Fatalf("method = %s", request.Method)
	}
	if request.Body["LastName"] != "Doe" || request.Body["Company"] != "Acme" {
		t.Fatalf("body = %v", request.Body)
	}
	if len(request.Body) != 2 {
		t.Fatalf("body must be the fields dict itself: %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceCreateRecord::test_missing_fields
func TestBatch6SalesforceCreateRecordRequiresFields(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, nil))
	result := batch6Execute(t, registry, "salesforce", map[string]any{"action": "salesforce_create_record", "object_type": "Lead"})
	if got := batch6ErrorText(t, result); got != "fields dict is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceUpdateRecord::test_successful_update
func TestBatch6SalesforceUpdateRecord(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/sobjects/Lead/00Q-1": map[string]any{},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_update_record", "object_type": "Lead", "record_id": "00Q-1",
		"fields": map[string]any{"Status": "Contacted"},
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPatch || request.Body["Status"] != "Contacted" {
		t.Fatalf("request = %+v", request)
	}
}

// Parity-exempt: upstream test_salesforce_tool.py has no delete coverage; the
// 204 success contract is asserted here against the shared record path.
func TestBatch6SalesforceDeleteRecord(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_delete_record", "object_type": "Lead", "record_id": "00Q-1",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Method != http.MethodDelete {
		t.Fatalf("method = %s", recorder.last(t).Method)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceDescribeObject::test_successful_describe
func TestBatch6SalesforceDescribeObject(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/sobjects/Lead/describe": map[string]any{
			"name": "Lead", "label": "Lead", "keyPrefix": "00Q", "createable": true, "updateable": true,
			"fields": []any{
				map[string]any{
					"name": "LastName", "label": "Last Name", "type": "string",
					"nillable": false, "createable": true,
				},
				map[string]any{
					"name": "Status", "label": "Status", "type": "picklist",
					"nillable": true, "createable": true,
					"picklistValues": []any{
						map[string]any{"value": "Open", "active": true},
						map[string]any{"value": "Closed", "active": false},
					},
				},
			},
		},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_describe_object", "object_type": "Lead",
	})
	if result["name"] != "Lead" || result["key_prefix"] != "00Q" || batch6Int(t, result["field_count"]) != 2 {
		t.Fatalf("result = %v", result)
	}
	fields := result["fields"].([]any)
	first := fields[0].(map[string]any)
	if first["required"] != true {
		t.Fatalf("first = %v", first)
	}
	second := fields[1].(map[string]any)
	picklist := second["picklist_values"].([]any)
	if len(picklist) != 1 || picklist[0] != "Open" {
		t.Fatalf("picklist = %v", picklist)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceListObjects::test_successful_list
func TestBatch6SalesforceListObjects(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/sobjects/": map[string]any{
			"sobjects": []any{
				map[string]any{"name": "Lead", "label": "Lead", "keyPrefix": "00Q", "queryable": true, "createable": true, "custom": false},
				map[string]any{"name": "Widget__c", "label": "Widget", "keyPrefix": "a00", "queryable": true, "createable": true, "custom": true},
			},
		},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{"action": "salesforce_list_objects"})
	if batch6Int(t, result["count"]) != 2 {
		t.Fatalf("result = %v", result)
	}
	objects := result["sobjects"].([]any)
	if objects[1].(map[string]any)["custom"] != true {
		t.Fatalf("sobjects = %v", objects)
	}
}

// Parity-exempt: upstream test_salesforce_tool.py has no SOSL coverage; the
// search-response projection is asserted here.
func TestBatch6SalesforceSearchRecords(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/search/": []any{
			map[string]any{"Id": "003-1", "Name": "John Smith"},
		},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_search_records", "search_query": "FIND {John Smith}",
	})
	if batch6Int(t, result["count"]) != 1 {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Query.Get("q") != "FIND {John Smith}" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity-exempt: upstream test_salesforce_tool.py has no count coverage; the
// SELECT COUNT() query string is asserted here.
func TestBatch6SalesforceGetRecordCount(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "salesforce_tool", batch6SalesforceServer(t, map[string]any{
		"/services/data/v62.0/query/": map[string]any{"totalSize": 42, "done": true},
	}))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_get_record_count", "object_type": "Lead",
	})
	if batch6Int(t, result["total_size"]) != 42 || result["object_type"] != "Lead" {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Query.Get("q"); got != "SELECT COUNT() FROM Lead" {
		t.Fatalf("q = %q", got)
	}
}

// Parity: tools/tests/tools/test_salesforce_tool.py::TestSalesforceSOQLQuery::test_missing_credentials
func TestBatch6SalesforceMissingCredentials(t *testing.T) {
	registry := newBatch6Lane1Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Execute(t, registry, "salesforce", map[string]any{
		"action": "salesforce_soql_query", "query": "SELECT Id FROM Lead",
	})
	if got := batch6ErrorText(t, result); got != "Salesforce credentials not configured" {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity-exempt: upstream has no status matrix for Salesforce; the 401/404/429
// strings come from _handle_response and are asserted here.
func TestBatch6SalesforceHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid or expired Salesforce access token"},
		{http.StatusNotFound, "Salesforce resource not found"},
		{http.StatusTooManyRequests, "Salesforce API error (HTTP 429)"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch6TestRegistry(t, "salesforce_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch6WriteJSON(w, tc.status, map[string]any{"message": "failure"})
			})
			result := batch6Execute(t, registry, "salesforce", map[string]any{"action": "salesforce_list_objects"})
			if got := batch6ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}

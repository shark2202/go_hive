package crm

import (
	"net/http"
	"strings"
	"testing"
)

// ----------------------------------------------------------------- attio ---

// Parity: tools/tests/tools/test_attio_tool.py::TestRecordTools::test_record_list
func TestBatch6AttioRecordList(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"POST /objects/people/records/query": {Body: map[string]any{
			"data": []any{map[string]any{"id": map[string]any{"record_id": "rec-1"}}},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_record_list", "object_handle": "people",
	})
	if batch6Int(t, result["total"]) != 1 {
		t.Fatalf("result = %v", result)
	}
	records := result["records"].([]any)
	if batch6BodyMap(t, records[0])["id"].(map[string]any)["record_id"] != "rec-1" {
		t.Fatalf("records = %v", records)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost {
		t.Fatalf("method = %s", request.Method)
	}
	if batch6Int(t, request.Body["limit"]) != 50 || batch6Int(t, request.Body["offset"]) != 0 {
		t.Fatalf("body = %v", request.Body)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestRecordTools::test_record_list_with_filter_json
func TestBatch6AttioRecordListWithFilter(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"POST /objects/people/records/query": {Body: map[string]any{"data": []any{}}},
	}))
	batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_record_list", "object_handle": "people",
		"filter_json": `{"email_addresses":{"email_address":"jane@example.com"}}`,
		"sorts_json":  `[{"attribute":"created_at","direction":"desc"}]`,
	})
	body := recorder.last(t).Body
	filter := batch6BodyMap(t, body["filter"])
	if batch6BodyMap(t, filter["email_addresses"])["email_address"] != "jane@example.com" {
		t.Fatalf("filter = %v", filter)
	}
	if sorts, ok := body["sorts"].([]any); !ok || len(sorts) != 1 {
		t.Fatalf("sorts = %v", body["sorts"])
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestRecordTools::test_record_list_invalid_filter_json
func TestBatch6AttioRecordListInvalidFilter(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "attio_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_record_list", "object_handle": "people", "filter_json": "{not json",
	})
	if got := batch6ErrorText(t, result); !strings.Contains(got, "Invalid filter_json") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestRecordTools::test_record_get
func TestBatch6AttioRecordGet(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"GET /objects/people/records/rec-1": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"record_id": "rec-1"}, "values": map[string]any{}},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_record_get", "object_handle": "people", "record_id": "rec-1",
	})
	if batch6BodyMap(t, result["id"])["record_id"] != "rec-1" {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Method != http.MethodGet {
		t.Fatalf("method = %s", recorder.last(t).Method)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestRecordTools::test_record_create
func TestBatch6AttioRecordCreate(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"POST /objects/people/records": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"record_id": "rec-2"}},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_record_create", "object_handle": "people",
		"values": map[string]any{"email_addresses": []any{map[string]any{"email_address": "jane@example.com"}}},
	})
	if batch6BodyMap(t, result["id"])["record_id"] != "rec-2" {
		t.Fatalf("result = %v", result)
	}
	data := batch6BodyMap(t, recorder.last(t).Body["data"])
	values := batch6BodyMap(t, data["values"])
	if _, present := values["email_addresses"]; !present {
		t.Fatalf("body = %v", recorder.last(t).Body)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestRecordTools::test_record_update
func TestBatch6AttioRecordUpdate(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"PATCH /objects/people/records/rec-1": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"record_id": "rec-1"}},
		}},
	}))
	batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_record_update", "object_handle": "people", "record_id": "rec-1",
		"values": map[string]any{"name": []any{map[string]any{"first_name": "Jane"}}},
	})
	request := recorder.last(t)
	if request.Method != http.MethodPatch {
		t.Fatalf("method = %s", request.Method)
	}
	if _, present := batch6BodyMap(t, request.Body["data"])["values"]; !present {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestRecordTools::test_record_assert
func TestBatch6AttioRecordAssert(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"PUT /objects/people/records": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"record_id": "rec-1"}},
		}},
	}))
	batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_record_assert", "object_handle": "people",
		"matching_attribute": "email_addresses",
		"values":             map[string]any{"email_addresses": []any{map[string]any{"email_address": "jane@example.com"}}},
	})
	request := recorder.last(t)
	if request.Method != http.MethodPut {
		t.Fatalf("method = %s", request.Method)
	}
	if request.Query.Get("matching_attribute") != "email_addresses" {
		t.Fatalf("query = %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestListTools::test_list_lists
func TestBatch6AttioListLists(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"GET /lists": {Body: map[string]any{
			"data": []any{
				map[string]any{"id": map[string]any{"list_id": "list-1"}, "name": "Sales Pipeline"},
				map[string]any{"id": map[string]any{"list_id": "list-2"}, "name": "Hiring"},
			},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_list_lists"})
	if batch6Int(t, result["total"]) != 2 {
		t.Fatalf("result = %v", result)
	}
	lists := result["lists"].([]any)
	if batch6BodyMap(t, lists[0])["name"] != "Sales Pipeline" {
		t.Fatalf("lists = %v", lists)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestListTools::test_list_entries_get
func TestBatch6AttioListEntriesGet(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"POST /lists/list-1/entries/query": {Body: map[string]any{
			"data": []any{map[string]any{"id": map[string]any{"entry_id": "entry-1"}}},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_list_entries_get", "list_id": "list-1", "limit": 10,
	})
	if batch6Int(t, result["total"]) != 1 {
		t.Fatalf("result = %v", result)
	}
	if batch6Int(t, recorder.last(t).Body["limit"]) != 10 {
		t.Fatalf("body = %v", recorder.last(t).Body)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestListTools::test_list_entry_create
func TestBatch6AttioListEntryCreate(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"POST /lists/list-1/entries": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"entry_id": "entry-9"}},
		}},
	}))
	batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_list_entry_create", "list_id": "list-1", "parent_record_id": "rec-1",
		"entry_values": map[string]any{"stage": "qualified"},
	})
	data := batch6BodyMap(t, recorder.last(t).Body["data"])
	if data["parent_record_id"] != "rec-1" || data["parent_object"] != "people" {
		t.Fatalf("data = %v", data)
	}
	if batch6BodyMap(t, data["entry_values"])["stage"] != "qualified" {
		t.Fatalf("data = %v", data)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestListTools::test_list_entry_delete
func TestBatch6AttioListEntryDelete(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_list_entry_delete", "list_id": "list-1", "entry_id": "entry-1",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Path != "/lists/list-1/entries/entry-1" {
		t.Fatalf("path = %q", recorder.last(t).Path)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestTaskTools::test_task_create
func TestBatch6AttioTaskCreate(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"POST /tasks": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"task_id": "task-1"}, "content": "Follow up"},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{
		"action": "attio_task_create", "content": "Follow up",
		"linked_records": []any{map[string]any{"target_object": "people", "target_record_id": "rec-1"}},
		"deadline_at":    "2026-03-15T00:00:00Z",
	})
	if result["content"] != "Follow up" {
		t.Fatalf("result = %v", result)
	}
	data := batch6BodyMap(t, recorder.last(t).Body["data"])
	if data["format"] != "plaintext" || data["is_completed"] != false {
		t.Fatalf("data = %v", data)
	}
	if records, ok := data["linked_records"].([]any); !ok || len(records) != 1 {
		t.Fatalf("linked_records = %v", data["linked_records"])
	}
	if data["deadline_at"] != "2026-03-15T00:00:00Z" {
		t.Fatalf("data = %v", data)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestTaskTools::test_task_list
func TestBatch6AttioTaskList(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"GET /tasks": {Body: map[string]any{
			"data": []any{
				map[string]any{"id": map[string]any{"task_id": "task-1"}},
				map[string]any{"id": map[string]any{"task_id": "task-2"}},
			},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_task_list"})
	if batch6Int(t, result["total"]) != 2 {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Query.Get("limit") != "50" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestTaskTools::test_task_get
func TestBatch6AttioTaskGet(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"GET /tasks/task-1": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"task_id": "task-1"}, "content": "Follow up"},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_task_get", "task_id": "task-1"})
	if result["content"] != "Follow up" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestTaskTools::test_task_delete
func TestBatch6AttioTaskDelete(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "attio_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_task_delete", "task_id": "task-1"})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Method != http.MethodDelete {
		t.Fatalf("method = %s", recorder.last(t).Method)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestMemberTools::test_members_list
func TestBatch6AttioMembersList(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"GET /workspace_members": {Body: map[string]any{
			"data": []any{
				map[string]any{"id": map[string]any{"workspace_member_id": "m-1"}, "first_name": "Jane"},
				map[string]any{"id": map[string]any{"workspace_member_id": "m-2"}, "first_name": "John"},
			},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_members_list"})
	if batch6Int(t, result["total"]) != 2 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestMemberTools::test_member_get
func TestBatch6AttioMemberGet(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "attio_tool", batch6Routes(t, map[string]batch6Route{
		"GET /workspace_members/m-1": {Body: map[string]any{
			"data": map[string]any{"id": map[string]any{"workspace_member_id": "m-1"}, "first_name": "Jane"},
		}},
	}))
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_member_get", "member_id": "m-1"})
	if result["first_name"] != "Jane" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestCredentialRetrieval::test_no_credentials_returns_error
func TestBatch6AttioMissingCredentials(t *testing.T) {
	registry := NewCRMRegistry(WithGetenv(func(string) string { return "" }))
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_record_list", "object_handle": "people"})
	if got := batch6ErrorText(t, result); got != "Attio credentials not configured" {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestAttioClient::test_handle_response_errors
func TestBatch6AttioHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid or expired Attio API key"},
		{http.StatusNotFound, "Attio API error (HTTP 404)"},
		{http.StatusTooManyRequests, "Attio rate limit exceeded. Try again later."},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch6TestRegistry(t, "attio_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch6WriteJSON(w, tc.status, map[string]any{"message": "failure"})
			})
			result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_list_lists"})
			if got := batch6ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}

// Parity: tools/tests/tools/test_attio_tool.py::TestToolErrorHandling::test_network_error
func TestBatch6AttioNetworkError(t *testing.T) {
	registry := NewCRMRegistry(
		WithCredential("attio_tool", "test-token"),
		WithBaseURL("attio_tool", "http://127.0.0.1:1/v2"),
	)
	result := batch6Execute(t, registry, "attio", map[string]any{"action": "attio_list_lists"})
	if got := batch6ErrorText(t, result); !strings.Contains(got, "Network error") {
		t.Fatalf("error = %q", got)
	}
}

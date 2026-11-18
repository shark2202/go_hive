package integrations

import (
	"net/http"
	"strings"
	"testing"
)

// ------------------------------------------------------------------ zoho ---

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmListRecords::test_successful_list
func TestBatch6ZohoListRecords(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, map[string]batch6Route{
		"GET /Leads": {Body: map[string]any{
			"data": []any{map[string]any{"id": "123", "Last_Name": "Doe"}},
			"info": map[string]any{"count": 1, "more_records": true, "page": 1},
		}},
	}))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_list_records", "module": "Leads",
	})
	if result["module"] != "Leads" || batch6Int(t, result["count"]) != 1 || result["more_records"] != true {
		t.Fatalf("result = %v", result)
	}
	if records := result["records"].([]any); len(records) != 1 {
		t.Fatalf("records = %v", records)
	}
	request := recorder.last(t)
	if request.Method != http.MethodGet || request.Query.Get("page") != "1" || request.Query.Get("per_page") != "50" {
		t.Fatalf("request = %+v", request)
	}
	if got := request.Header.Get("Authorization"); got != "Zoho-oauthtoken test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmListRecords::test_missing_module
func TestBatch6ZohoListRecordsRequiresModule(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{"action": "zoho_crm_list_records"})
	if got := batch6ErrorText(t, result); got != "module is required (e.g. Leads, Contacts, Deals)" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmGetRecord::test_successful_get
func TestBatch6ZohoGetRecord(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, map[string]batch6Route{
		"GET /Leads/123": {Body: map[string]any{
			"data": []any{map[string]any{"id": "123", "Last_Name": "Doe", "Company": "Acme"}},
		}},
	}))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_get_record", "module": "Leads", "record_id": "123",
	})
	if result["module"] != "Leads" {
		t.Fatalf("result = %v", result)
	}
	record := batch6BodyMap(t, result["record"])
	if record["Last_Name"] != "Doe" {
		t.Fatalf("record = %v", record)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmGetRecord::test_missing_params
func TestBatch6ZohoGetRecordNotFound(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, map[string]batch6Route{
		"GET /Leads/404": {Body: map[string]any{"data": []any{}}},
	}))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_get_record", "module": "Leads", "record_id": "404",
	})
	if got := batch6ErrorText(t, result); got != "Record not found" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmCreateRecord::test_successful_create
func TestBatch6ZohoCreateRecord(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, map[string]batch6Route{
		"POST /Leads": {Status: http.StatusCreated, Body: map[string]any{
			"data": []any{map[string]any{
				"code":    "SUCCESS",
				"details": map[string]any{"id": "456"},
				"message": "record added",
				"status":  "success",
			}},
		}},
	}))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_create_record", "module": "Leads",
		"record_data": map[string]any{"Last_Name": "Doe", "Company": "Acme"},
	})
	if result["id"] != "456" || result["status"] != "success" || result["message"] != "record added" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	rows, ok := body["data"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("body = %v", body)
	}
	if rows[0].(map[string]any)["Last_Name"] != "Doe" {
		t.Fatalf("row = %v", rows[0])
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmCreateRecord::test_missing_data
func TestBatch6ZohoCreateRecordRequiresData(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_create_record", "module": "Leads",
	})
	if got := batch6ErrorText(t, result); got != "record_data dict is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmSearchRecords::test_successful_search
func TestBatch6ZohoSearchRecords(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, map[string]batch6Route{
		"GET /Leads/search": {Body: map[string]any{
			"data": []any{map[string]any{"id": "123", "Email": "doe@example.com"}},
		}},
	}))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_search_records", "module": "Leads", "email": "doe@example.com",
	})
	if batch6Int(t, result["count"]) != 1 {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Query.Get("email") != "doe@example.com" || request.Query.Get("page") != "1" {
		t.Fatalf("query = %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmSearchRecords::test_no_search_params
func TestBatch6ZohoSearchRecordsRequiresParameter(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_search_records", "module": "Leads",
	})
	if got := batch6ErrorText(t, result); !strings.Contains(got, "At least one search parameter") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmListModules::test_successful_list
func TestBatch6ZohoListModules(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, map[string]batch6Route{
		"GET /settings/modules": {Body: map[string]any{
			"modules": []any{
				map[string]any{"api_name": "Leads", "module_name": "Leads", "plural_label": "Leads", "editable": true},
				map[string]any{"api_name": "Deals", "module_name": "Deals", "plural_label": "Deals", "editable": false},
			},
		}},
	}))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{"action": "zoho_crm_list_modules"})
	modules := result["modules"].([]any)
	if len(modules) != 2 || modules[0].(map[string]any)["api_name"] != "Leads" {
		t.Fatalf("modules = %v", modules)
	}
	if modules[1].(map[string]any)["editable"] != false {
		t.Fatalf("modules = %v", modules)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmAddNote::test_successful_add
func TestBatch6ZohoAddNote(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, map[string]batch6Route{
		"POST /Leads/123/Notes": {Status: http.StatusCreated, Body: map[string]any{
			"data": []any{map[string]any{
				"code":    "SUCCESS",
				"details": map[string]any{"id": "789"},
				"status":  "success",
			}},
		}},
	}))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_add_note", "module": "Leads", "record_id": "123",
		"title": "Call notes", "content": "Spoke with the customer",
	})
	if result["id"] != "789" || result["status"] != "success" {
		t.Fatalf("result = %v", result)
	}
	row := recorder.last(t).Body["data"].([]any)[0].(map[string]any)
	if row["Note_Title"] != "Call notes" || row["Note_Content"] != "Spoke with the customer" {
		t.Fatalf("row = %v", row)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmAddNote::test_missing_content
func TestBatch6ZohoAddNoteRequiresContent(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "zoho_crm_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{
		"action": "zoho_crm_add_note", "module": "Leads", "record_id": "123", "title": "t",
	})
	if got := batch6ErrorText(t, result); got != "content is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zoho_crm_tool.py::TestZohoCrmListRecords::test_missing_token
func TestBatch6ZohoMissingToken(t *testing.T) {
	registry := newBatch6Lane1Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Execute(t, registry, "zoho_crm", map[string]any{"action": "zoho_crm_list_modules"})
	if got := batch6ErrorText(t, result); got != "ZOHO_CRM_ACCESS_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream zoho tests have no status matrix; the 401/404/429
// strings from the shared _get/_post helpers are asserted here.
func TestBatch6ZohoHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Unauthorized. Check your ZOHO_CRM_ACCESS_TOKEN (may need refresh)."},
		{http.StatusNotFound, "Zoho CRM API error 404"},
		{http.StatusTooManyRequests, "Zoho CRM API error 429"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch6TestRegistry(t, "zoho_crm_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch6WriteJSON(w, tc.status, map[string]any{"code": "INVALID"})
			})
			result := batch6Execute(t, registry, "zoho_crm", map[string]any{
				"action": "zoho_crm_list_records", "module": "Leads",
			})
			if got := batch6ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}

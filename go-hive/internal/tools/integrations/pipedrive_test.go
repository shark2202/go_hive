package integrations

import (
	"net/http"
	"strings"
	"testing"
)

// -------------------------------------------------------------- pipedrive ---

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveListDeals::test_successful_list
func TestBatch6PipedriveListDeals(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /deals": {Body: map[string]any{
			"success": true,
			"data": []any{map[string]any{
				"id": 1, "title": "Big Deal", "value": 10000, "currency": "USD", "status": "open",
				"person_id": map[string]any{"name": "John Doe"},
				"org_id":    map[string]any{"name": "Acme Corp"},
				"stage_id":  1, "add_time": "2024-01-01",
			}},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_list_deals"})
	if batch6Int(t, result["count"]) != 1 {
		t.Fatalf("result = %v", result)
	}
	deal := result["deals"].([]any)[0].(map[string]any)
	if deal["title"] != "Big Deal" || deal["person_name"] != "John Doe" || deal["org_name"] != "Acme Corp" {
		t.Fatalf("deal = %v", deal)
	}
	if deal["value"] != float64(10000) {
		t.Fatalf("value = %v", deal["value"])
	}
	request := recorder.last(t)
	if request.Query.Get("status") != "open" || request.Query.Get("limit") != "50" {
		t.Fatalf("query = %v", request.Query)
	}
	if request.Query.Get("api_token") != "test-token" {
		t.Fatalf("api_token = %q", request.Query.Get("api_token"))
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveListDeals::test_missing_token
func TestBatch6PipedriveMissingToken(t *testing.T) {
	registry := newBatch6Lane1Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_list_deals"})
	if got := batch6ErrorText(t, result); got != "PIPEDRIVE_API_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveGetDeal::test_successful_get
func TestBatch6PipedriveGetDeal(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /deals/1": {Body: map[string]any{
			"success": true,
			"data": map[string]any{
				"id": 1, "title": "Big Deal", "value": 10000, "currency": "USD", "status": "open",
				"person_id": map[string]any{"name": "John Doe"},
				"org_id":    map[string]any{"name": "Acme Corp"},
				"stage_id":  1, "pipeline_id": 1, "add_time": "2024-01-01",
				"expected_close_date": "2024-06-01", "probability": 75,
			},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_get_deal", "deal_id": 1})
	if result["title"] != "Big Deal" || result["probability"] != float64(75) {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Path != "/deals/1" {
		t.Fatalf("path = %q", recorder.last(t).Path)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveGetDeal::test_missing_id
func TestBatch6PipedriveGetDealRequiresID(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_get_deal"})
	if got := batch6ErrorText(t, result); got != "deal_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveCreateDeal::test_successful_create
func TestBatch6PipedriveCreateDeal(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"POST /deals": {Status: http.StatusCreated, Body: map[string]any{
			"success": true, "data": map[string]any{"id": 42, "title": "New Deal"},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_create_deal", "title": "New Deal", "value": 5000,
		"person_id": 7, "stage_id": 3,
	})
	if result["status"] != "created" || batch6Int(t, result["id"]) != 42 {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["title"] != "New Deal" || body["value"] != float64(5000) {
		t.Fatalf("body = %v", body)
	}
	if body["currency"] != "USD" {
		t.Fatalf("currency default missing: %v", body)
	}
	if batch6Int(t, body["person_id"]) != 7 || batch6Int(t, body["stage_id"]) != 3 {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveCreateDeal::test_missing_title
func TestBatch6PipedriveCreateDealRequiresTitle(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_create_deal"})
	if got := batch6ErrorText(t, result); got != "title is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveListPersons::test_successful_list
func TestBatch6PipedriveListPersons(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /persons": {Body: map[string]any{
			"success": true,
			"data": []any{map[string]any{
				"id": 5, "name": "Jane Roe",
				"email":  []any{map[string]any{"value": "jane@example.com", "primary": true}},
				"phone":  []any{map[string]any{"value": "+15550100", "primary": true}},
				"org_id": map[string]any{"name": "Acme"}, "open_deals_count": 3,
			}},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_list_persons"})
	person := result["persons"].([]any)[0].(map[string]any)
	if person["email"] != "jane@example.com" || person["phone"] != "+15550100" || person["org_name"] != "Acme" {
		t.Fatalf("person = %v", person)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveSearchPersons::test_successful_search
func TestBatch6PipedriveSearchPersons(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /persons/search": {Body: map[string]any{
			"success": true,
			"data": map[string]any{"items": []any{map[string]any{
				"item": map[string]any{
					"id": 5, "name": "Jane Roe",
					"emails": []any{"jane@example.com"}, "phones": []any{"+15550100"},
					"organization": map[string]any{"name": "Acme"},
				},
			}}},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_search_persons", "query": "Jane",
	})
	if result["query"] != "Jane" {
		t.Fatalf("result = %v", result)
	}
	found := result["results"].([]any)[0].(map[string]any)
	if found["email"] != "jane@example.com" || found["org_name"] != "Acme" {
		t.Fatalf("result = %v", found)
	}
	request := recorder.last(t)
	if request.Query.Get("term") != "Jane" || request.Query.Get("limit") != "20" {
		t.Fatalf("query = %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveSearchPersons::test_empty_query
func TestBatch6PipedriveSearchPersonsRequiresQuery(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_search_persons"})
	if got := batch6ErrorText(t, result); got != "query is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveListOrganizations::test_successful_list
func TestBatch6PipedriveListOrganizations(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /organizations": {Body: map[string]any{
			"success": true,
			"data": []any{map[string]any{
				"id": 3, "name": "Acme", "address": "1 Main St",
				"open_deals_count": 2, "people_count": 8,
			}},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_list_organizations"})
	org := result["organizations"].([]any)[0].(map[string]any)
	if org["name"] != "Acme" || batch6Int(t, org["people_count"]) != 8 {
		t.Fatalf("org = %v", org)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveListActivities::test_successful_list
func TestBatch6PipedriveListActivities(t *testing.T) {
	longNote := strings.Repeat("x", 250)
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /activities": {Body: map[string]any{
			"success": true,
			"data": []any{map[string]any{
				"id": 11, "subject": "Call back", "type": "call", "done": false,
				"due_date": "2024-02-01", "due_time": "09:00",
				"deal_title": "Big Deal", "person_name": "John Doe", "org_name": "Acme",
				"note": longNote,
			}},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_list_activities", "done": "0", "activity_type": "call",
	})
	activity := result["activities"].([]any)[0].(map[string]any)
	if activity["type"] != "call" || activity["done"] != false || activity["deal_title"] != "Big Deal" {
		t.Fatalf("activity = %v", activity)
	}
	if got := activity["note"].(string); len(got) != 200 {
		t.Fatalf("note length = %d", len(got))
	}
	request := recorder.last(t)
	if request.Query.Get("done") != "0" || request.Query.Get("type") != "call" {
		t.Fatalf("query = %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveListPipelines::test_successful_list
func TestBatch6PipedriveListPipelines(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /pipelines": {Body: map[string]any{
			"success": true,
			"data": []any{
				map[string]any{"id": 1, "name": "Sales", "active": true, "deal_probability": true, "order_nr": 1},
				map[string]any{"id": 2, "name": "Renewals", "active": false, "deal_probability": false, "order_nr": 2},
			},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_list_pipelines"})
	pipelines := result["pipelines"].([]any)
	if len(pipelines) != 2 || pipelines[0].(map[string]any)["active"] != true {
		t.Fatalf("pipelines = %v", pipelines)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveListStages::test_successful_list
func TestBatch6PipedriveListStages(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"GET /stages": {Body: map[string]any{
			"success": true,
			"data": []any{
				map[string]any{"id": 1, "name": "Qualified", "pipeline_id": 3, "order_nr": 1},
				map[string]any{"id": 2, "name": "Won", "pipeline_id": 3, "order_nr": 2, "active_flag": false},
			},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_list_stages", "pipeline_id": 3,
	})
	stages := result["stages"].([]any)
	if stages[0].(map[string]any)["active_flag"] != true {
		t.Fatalf("default active_flag missing: %v", stages[0])
	}
	if stages[1].(map[string]any)["active_flag"] != false {
		t.Fatalf("explicit active_flag lost: %v", stages[1])
	}
	if recorder.last(t).Query.Get("pipeline_id") != "3" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveAddNote::test_successful_add
func TestBatch6PipedriveAddNote(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"POST /notes": {Status: http.StatusCreated, Body: map[string]any{
			"success": true, "data": map[string]any{"id": 77},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_add_note", "content": "<p>Hello</p>", "deal_id": 9,
	})
	if result["status"] != "created" || batch6Int(t, result["id"]) != 77 {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["content"] != "<p>Hello</p>" || batch6Int(t, body["deal_id"]) != 9 {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_pipedrive_tool.py::TestPipedriveAddNote::test_missing_target
func TestBatch6PipedriveAddNoteRequiresTarget(t *testing.T) {
	registry, _ := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, nil))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_add_note", "content": "note",
	})
	if got := batch6ErrorText(t, result); got != "At least one of deal_id, person_id, or org_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream test_pipedrive_tool.py has no update_deal coverage;
// the PUT body and "updated" projection are asserted here.
func TestBatch6PipedriveUpdateDeal(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"PUT /deals/9": {Body: map[string]any{
			"success": true, "data": map[string]any{"id": 9, "title": "Renamed", "status": "won"},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_update_deal", "deal_id": 9, "title": "Renamed",
		"status": "won", "stage_id": 4,
	})
	if result["result"] != "updated" || result["title"] != "Renamed" || result["status"] != "won" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["title"] != "Renamed" || body["status"] != "won" || batch6Int(t, body["stage_id"]) != 4 {
		t.Fatalf("body = %v", body)
	}
	if _, present := body["currency"]; present {
		t.Fatalf("empty optionals must be omitted: %v", body)
	}
}

// Parity-exempt: upstream test_pipedrive_tool.py has no create_person coverage;
// the primary-email/phone body shape is asserted here.
func TestBatch6PipedriveCreatePerson(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"POST /persons": {Status: http.StatusCreated, Body: map[string]any{
			"success": true, "data": map[string]any{"id": 12, "name": "Jane Roe"},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_create_person", "name": "Jane Roe", "email": "jane@example.com", "org_id": 3,
	})
	if result["status"] != "created" || result["name"] != "Jane Roe" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	emails := body["email"].([]any)
	first := emails[0].(map[string]any)
	if first["value"] != "jane@example.com" || first["primary"] != true || first["label"] != "work" {
		t.Fatalf("email = %v", first)
	}
	if _, present := body["phone"]; present {
		t.Fatalf("empty phone must be omitted: %v", body)
	}
}

// Parity-exempt: upstream test_pipedrive_tool.py has no create_activity
// coverage; the activity_type -> type mapping is asserted here.
func TestBatch6PipedriveCreateActivity(t *testing.T) {
	registry, recorder := batch6TestRegistry(t, "pipedrive_tool", batch6Routes(t, map[string]batch6Route{
		"POST /activities": {Status: http.StatusCreated, Body: map[string]any{
			"success": true,
			"data":    map[string]any{"id": 21, "subject": "Follow up", "type": "task"},
		}},
	}))
	result := batch6Execute(t, registry, "pipedrive", map[string]any{
		"action": "pipedrive_create_activity", "subject": "Follow up", "due_date": "2024-03-01", "person_id": 5,
	})
	if result["status"] != "created" || result["type"] != "task" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["subject"] != "Follow up" || body["type"] != "task" || body["due_date"] != "2024-03-01" {
		t.Fatalf("body = %v", body)
	}
	if _, present := body["note"]; present {
		t.Fatalf("empty note must be omitted: %v", body)
	}
}

// Parity-exempt: upstream test_pipedrive_tool.py asserts error strings inside
// per-action tests but has no status matrix; 401/404/429 are asserted here.
func TestBatch6PipedriveHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Unauthorized. Check your PIPEDRIVE_API_TOKEN."},
		{http.StatusNotFound, "Not found"},
		{http.StatusTooManyRequests, "Pipedrive API error 429"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch6TestRegistry(t, "pipedrive_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch6WriteJSON(w, tc.status, map[string]any{"success": false, "error": "failure"})
			})
			result := batch6Execute(t, registry, "pipedrive", map[string]any{"action": "pipedrive_list_deals"})
			if got := batch6ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}

// Parity-exempt: base-URL derivation is a Go-side wiring concern (upstream
// reads the same env vars inside _base_url); asserted directly here.
func TestBatch6BaseURLResolution(t *testing.T) {
	env := map[string]string{
		"SALESFORCE_INSTANCE_URL": "acme.my.salesforce.com/",
		"PIPEDRIVE_DOMAIN":        "acme.pipedrive.com/",
		"ZOHO_CRM_DOMAIN":         "www.zohoapis.eu",
	}
	runtime := &Runtime{Modules: crmModules(), Getenv: func(name string) string { return env[name] }}
	cases := map[string]string{
		"salesforce_tool": "https://acme.my.salesforce.com",
		"pipedrive_tool":  "https://acme.pipedrive.com/api/v1",
		"zoho_crm_tool":   "https://www.zohoapis.eu/crm/v7",
		"attio_tool":      "https://api.attio.com/v2",
	}
	for module, want := range cases {
		if got := crmBase(runtime, module); got != want {
			t.Fatalf("%s base = %q, want %q", module, got, want)
		}
	}
	if got := crmBase(&Runtime{Modules: map[string]*ModuleConfig{}}, "zoho_crm_tool"); got != "https://www.zohoapis.com/crm/v7" {
		t.Fatalf("zoho default = %q", got)
	}
	if got := crmBase(&Runtime{Modules: map[string]*ModuleConfig{}}, "pipedrive_tool"); got != "https://api.pipedrive.com/v1" {
		t.Fatalf("pipedrive default = %q", got)
	}
	if got := crmBase(&Runtime{Modules: map[string]*ModuleConfig{}}, "salesforce_tool"); got != "" {
		t.Fatalf("salesforce without instance = %q", got)
	}
}

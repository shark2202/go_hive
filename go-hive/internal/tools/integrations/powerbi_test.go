package integrations

import (
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIListWorkspaces::test_missing_credentials
func TestBatch6Lane4PowerBIMissingCredentials(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "powerbi", map[string]any{"action": "powerbi_list_workspaces"})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "POWERBI_ACCESS_TOKEN") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIListWorkspaces::test_successful_list
func TestBatch6Lane4PowerBIListWorkspaces(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "powerbi_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/groups" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"id":                    "f089354e-8366-4e18-aea3-4cb4a3a50b48",
					"name":                  "Marketing",
					"isReadOnly":            false,
					"isOnDedicatedCapacity": true,
				},
			}})
		})
	result := batch4Execute(t, registry, "powerbi", map[string]any{
		"action": "powerbi_list_workspaces", "search": "Market",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	workspace := batch4BodyMap(t, batch4BodySlice(t, result["workspaces"])[0])
	if workspace["name"] != "Marketing" || workspace["is_on_dedicated_capacity"] != true || workspace["is_read_only"] != false {
		t.Fatalf("workspace = %v", workspace)
	}
	query := recorder.last(t).Query
	if query.Get("$top") != "100" || query.Get("$skip") != "0" || query.Get("$filter") != "contains(name,'Market')" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIListDatasets::test_missing_workspace
func TestBatch6Lane4PowerBIListDatasetsMissingWorkspace(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "powerbi_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "powerbi", map[string]any{"action": "powerbi_list_datasets", "workspace_id": ""})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIListDatasets::test_successful_list
func TestBatch6Lane4PowerBIListDatasets(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "powerbi_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/groups/ws-123/datasets" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"id":            "cfafbeb1-8037-4d0c-896e-a46fb27ff229",
					"name":          "SalesMarketing",
					"configuredBy":  "john@contoso.com",
					"isRefreshable": true,
					"createdDate":   "2024-01-15T10:30:00Z",
					"description":   "Sales data",
					"webUrl":        "https://app.powerbi.com/...",
				},
			}})
		})
	result := batch4Execute(t, registry, "powerbi", map[string]any{
		"action": "powerbi_list_datasets", "workspace_id": "ws-123",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	dataset := batch4BodyMap(t, batch4BodySlice(t, result["datasets"])[0])
	if dataset["name"] != "SalesMarketing" || dataset["is_refreshable"] != true || dataset["configured_by"] != "john@contoso.com" {
		t.Fatalf("dataset = %v", dataset)
	}
}

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIListReports::test_successful_list
func TestBatch6Lane4PowerBIListReports(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "powerbi_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/groups/ws-123/reports" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"id":          "5b218778-e7a5-4d73-8187-f10824047715",
					"name":        "SalesReport",
					"datasetId":   "cfafbeb1-8037-4d0c-896e-a46fb27ff229",
					"reportType":  "PowerBIReport",
					"webUrl":      "https://app.powerbi.com/...",
					"description": "Sales overview",
				},
			}})
		})
	result := batch4Execute(t, registry, "powerbi", map[string]any{
		"action": "powerbi_list_reports", "workspace_id": "ws-123",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	report := batch4BodyMap(t, batch4BodySlice(t, result["reports"])[0])
	if report["name"] != "SalesReport" || report["report_type"] != "PowerBIReport" {
		t.Fatalf("report = %v", report)
	}
}

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIRefreshDataset::test_missing_params
func TestBatch6Lane4PowerBIRefreshMissingParams(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "powerbi_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "powerbi", map[string]any{
		"action": "powerbi_refresh_dataset", "workspace_id": "", "dataset_id": "",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIRefreshDataset::test_successful_refresh
func TestBatch6Lane4PowerBIRefreshDataset(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "powerbi_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/groups/ws-123/datasets/ds-456/refreshes" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("x-ms-request-id", "req-123")
			w.WriteHeader(http.StatusAccepted)
		})
	result := batch4Execute(t, registry, "powerbi", map[string]any{
		"action": "powerbi_refresh_dataset", "workspace_id": "ws-123", "dataset_id": "ds-456",
	})
	if result["result"] != "accepted" || result["request_id"] != "req-123" {
		t.Fatalf("result = %v", result)
	}
	if request := recorder.last(t); request.Body["notifyOption"] != "NoNotification" {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_powerbi_tool.py::TestPowerBIGetRefreshHistory::test_successful_get
func TestBatch6Lane4PowerBIGetRefreshHistory(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "powerbi_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/groups/ws-123/datasets/ds-456/refreshes" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"requestId":   "req-123",
					"refreshType": "ViaApi",
					"status":      "Completed",
					"startTime":   "2024-01-15T09:25:43Z",
					"endTime":     "2024-01-15T09:31:43Z",
				},
			}})
		})
	result := batch4Execute(t, registry, "powerbi", map[string]any{
		"action": "powerbi_get_refresh_history", "workspace_id": "ws-123", "dataset_id": "ds-456", "top": 5,
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	refresh := batch4BodyMap(t, batch4BodySlice(t, result["refreshes"])[0])
	if refresh["status"] != "Completed" || refresh["refresh_type"] != "ViaApi" {
		t.Fatalf("refresh = %v", refresh)
	}
	if got := recorder.last(t).Query.Get("$top"); got != "5" {
		t.Fatalf("$top = %q", got)
	}
}

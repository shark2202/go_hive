package integrations

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// batch7Lane3TestRegistry builds a lane-3 registry pointed at one fake
// upstream, with environment injection for multi-field credentials.
func batch7Lane3TestRegistry(t *testing.T, module string, env map[string]string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	options := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
	}
	return newBatch7Lane3Registry(append(options, opts...)...), recorder
}

// batch7Lane3StatusRegistry answers every request with one status code, for
// the 401/404/429 matrix asserted by each module's error-mapping test.
func batch7Lane3StatusRegistry(t *testing.T, module string, env map[string]string, status int) *Registry {
	t.Helper()
	registry, _ := batch7Lane3TestRegistry(t, module, env, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"boom","error":"boom"}`))
	})
	return registry
}

var (
	batch7Lane3TerraformEnv = map[string]string{"TFC_TOKEN": "test-token"}
	batch7Lane3PagerDutyEnv = map[string]string{
		"PAGERDUTY_API_KEY":    "test-api-key",
		"PAGERDUTY_FROM_EMAIL": "agent@example.com",
	}
	batch7Lane3PrometheusEnv = map[string]string{
		"PROMETHEUS_BASE_URL": "http://fake-prometheus:9090",
		"PROMETHEUS_TOKEN":    "prom-token",
	}
	batch7Lane3KafkaEnv = map[string]string{
		"KAFKA_REST_URL":   "https://kafka.example.com",
		"KAFKA_CLUSTER_ID": "cluster-abc",
	}
	batch7Lane3N8nEnv = map[string]string{
		"N8N_API_KEY":  "test-api-key-123",
		"N8N_BASE_URL": "https://my-n8n.example.com",
	}
	batch7Lane3TinesEnv = map[string]string{
		"TINES_DOMAIN":  "acme.tines.com",
		"TINES_API_KEY": "tines-key",
	}
)

// Parity-exempt: registry/catalog invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not this registry surface.
func TestBatch7Lane3Catalog(t *testing.T) {
	registry := newBatch7Lane3Registry()
	names := registry.Names()
	wantNames := []string{"terraform", "pagerduty", "prometheus", "kafka", "n8n", "tines"}
	if len(names) != len(wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
	for index, name := range wantNames {
		if names[index] != name {
			t.Fatalf("tools = %v, want %v", names, wantNames)
		}
	}
	if got := registry.ActionCount(); got != 32 {
		t.Fatalf("actions = %d, want 32", got)
	}
	if got := registry.ImplementedActions(); got != 32 {
		t.Fatalf("implemented = %d, want 32", got)
	}
	for _, tool := range registry.Tools() {
		for _, action := range tool.Actions {
			if registry.Impls(tool.Name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", tool.Name, action.Name)
			}
		}
	}
}

// -------------------------------------------------------------- terraform ---

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformListWorkspaces::test_missing_credentials
func TestBatch7Lane3TerraformMissingCredentials(t *testing.T) {
	registry := newBatch7Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_list_workspaces", "organization": "my-org",
	})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "TFC_TOKEN is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformListWorkspaces::test_missing_organization
func TestBatch7Lane3TerraformListWorkspacesMissingOrganization(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_list_workspaces", "organization": "",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "organization is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformListWorkspaces::test_successful_list
func TestBatch7Lane3TerraformListWorkspaces(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v2/organizations/my-org/workspaces" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/vnd.api+json" {
				t.Fatalf("Content-Type = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{
					"id": "ws-abc123",
					"attributes": map[string]any{
						"name":              "production",
						"terraform-version": "1.9.0",
						"execution-mode":    "remote",
						"auto-apply":        false,
						"locked":            false,
						"resource-count":    42,
						"created-at":        "2024-01-15T10:30:00Z",
						"updated-at":        "2024-01-15T10:30:00Z",
					},
				}},
				"meta": map[string]any{"pagination": map[string]any{"total-count": 1, "total-pages": 1}},
			})
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_list_workspaces", "organization": "my-org", "search": "prod", "page_size": 10,
	})
	if result["count"] != 1 || batch7Lane3Int(result, "total_count", -1) != 1 {
		t.Fatalf("result = %v", result)
	}
	workspace := batch4BodyMap(t, batch4BodySlice(t, result["workspaces"])[0])
	if workspace["name"] != "production" || workspace["id"] != "ws-abc123" ||
		batch7Lane3Int(workspace, "resource_count", -1) != 42 {
		t.Fatalf("workspace = %v", workspace)
	}
	if workspace["terraform_version"] != "1.9.0" || workspace["execution_mode"] != "remote" {
		t.Fatalf("workspace = %v", workspace)
	}
	query := recorder.last(t).Query
	if query.Get("page[size]") != "10" || query.Get("page[number]") != "1" || query.Get("search[name]") != "prod" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformGetWorkspace::test_missing_id
func TestBatch7Lane3TerraformGetWorkspaceMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_get_workspace", "workspace_id": "",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "workspace_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformGetWorkspace::test_successful_get
func TestBatch7Lane3TerraformGetWorkspace(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v2/workspaces/ws-abc123" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"id": "ws-abc123",
				"attributes": map[string]any{
					"name":              "production",
					"description":       "Production infra",
					"terraform-version": "1.9.0",
					"execution-mode":    "remote",
					"auto-apply":        true,
					"locked":            false,
					"resource-count":    42,
					"vcs-repo":          map[string]any{"identifier": "org/repo", "branch": "main"},
					"working-directory": "infra/",
					"created-at":        "2024-01-15T10:30:00Z",
					"updated-at":        "2024-01-15T10:30:00Z",
				},
			}})
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_get_workspace", "workspace_id": "ws-abc123",
	})
	if result["name"] != "production" || result["description"] != "Production infra" || result["working_directory"] != "infra/" {
		t.Fatalf("result = %v", result)
	}
	if repo := batch4BodyMap(t, result["vcs_repo"]); repo["branch"] != "main" {
		t.Fatalf("vcs_repo = %v", repo)
	}
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformListRuns::test_missing_workspace
func TestBatch7Lane3TerraformListRunsMissingWorkspace(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_list_runs", "workspace_id": "",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformListRuns::test_successful_list
func TestBatch7Lane3TerraformListRuns(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v2/workspaces/ws-abc123/runs" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{
					"id": "run-xyz789",
					"attributes": map[string]any{
						"status":         "applied",
						"message":        "Deploy v2",
						"source":         "tfe-api",
						"trigger-reason": "manual",
						"is-destroy":     false,
						"plan-only":      false,
						"has-changes":    true,
						"auto-apply":     true,
						"created-at":     "2024-01-15T11:00:00Z",
					},
				}},
				"meta": map[string]any{"pagination": map[string]any{"total-count": 1, "total-pages": 1}},
			})
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_list_runs", "workspace_id": "ws-abc123", "status": "applied",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	run := batch4BodyMap(t, batch4BodySlice(t, result["runs"])[0])
	if run["status"] != "applied" || run["message"] != "Deploy v2" || run["has_changes"] != true {
		t.Fatalf("run = %v", run)
	}
	if got := recorder.last(t).Query.Get("filter[status]"); got != "applied" {
		t.Fatalf("filter[status] = %q", got)
	}
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformGetRun::test_missing_id
func TestBatch7Lane3TerraformGetRunMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_get_run", "run_id": "",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformGetRun::test_successful_get
func TestBatch7Lane3TerraformGetRun(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v2/runs/run-xyz789" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"id": "run-xyz789",
				"attributes": map[string]any{
					"status":            "planned",
					"message":           "Plan only",
					"source":            "tfe-ui",
					"trigger-reason":    "manual",
					"is-destroy":        false,
					"plan-only":         true,
					"has-changes":       true,
					"auto-apply":        false,
					"created-at":        "2024-01-15T11:00:00Z",
					"status-timestamps": map[string]any{"plan-queued-at": "2024-01-15T11:00:01Z"},
					"permissions":       map[string]any{"can-apply": true},
				},
			}})
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_get_run", "run_id": "run-xyz789",
	})
	if result["status"] != "planned" || result["plan_only"] != true {
		t.Fatalf("result = %v", result)
	}
	plan := batch4BodyMap(t, result["plan_and_apply"])
	if plan["resource_additions"] != "2024-01-15T11:00:01Z" {
		t.Fatalf("plan_and_apply = %v", plan)
	}
	if permissions := batch4BodyMap(t, result["permissions"]); permissions["can-apply"] != true {
		t.Fatalf("permissions = %v", permissions)
	}
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformCreateRun::test_missing_workspace
func TestBatch7Lane3TerraformCreateRunMissingWorkspace(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_create_run", "workspace_id": "",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_terraform_tool.py::TestTerraformCreateRun::test_successful_create
func TestBatch7Lane3TerraformCreateRun(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "terraform_tool", batch7Lane3TerraformEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v2/runs" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{
				"id": "run-new123",
				"attributes": map[string]any{
					"status":     "pending",
					"message":    "Deploy via API",
					"auto-apply": true,
					"created-at": "2024-01-15T12:00:00Z",
				},
			}})
		})
	result := batch4Execute(t, registry, "terraform", map[string]any{
		"action": "terraform_create_run", "workspace_id": "ws-abc123", "message": "Deploy via API", "auto_apply": true,
	})
	if result["id"] != "run-new123" || result["status"] != "pending" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	data := batch4BodyMap(t, body["data"])
	attributes := batch4BodyMap(t, data["attributes"])
	if data["type"] != "runs" || attributes["message"] != "Deploy via API" || attributes["auto-apply"] != true {
		t.Fatalf("payload data = %v", data)
	}
	relationships := batch4BodyMap(t, data["relationships"])
	workspace := batch4BodyMap(t, relationships["workspace"])
	if reference := batch4BodyMap(t, workspace["data"]); reference["id"] != "ws-abc123" {
		t.Fatalf("workspace reference = %v", reference)
	}
}

// Parity-exempt: upstream terraform tests have no status matrix; the port
// keeps the shared "HTTP <code>: <body>" mapping and asserts it here.
func TestBatch7Lane3TerraformStatusMatrix(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		registry := batch7Lane3StatusRegistry(t, "terraform_tool", batch7Lane3TerraformEnv, status)
		result := batch4Execute(t, registry, "terraform", map[string]any{
			"action": "terraform_list_workspaces", "organization": "my-org",
		})
		got := batch4ErrorText(t, result)
		if !strings.HasPrefix(got, fmt.Sprintf("HTTP %d: ", status)) {
			t.Fatalf("status %d: error = %q", status, got)
		}
	}
}

// -------------------------------------------------------------- pagerduty ---

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyListIncidents::test_missing_credentials
func TestBatch7Lane3PagerDutyMissingCredentials(t *testing.T) {
	registry := newBatch7Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "pagerduty", map[string]any{"action": "pagerduty_list_incidents"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "PAGERDUTY_API_KEY is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyListIncidents::test_successful_list
func TestBatch7Lane3PagerDutyListIncidents(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/incidents" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Token token=test-api-key" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"incidents": []any{map[string]any{
					"id":              "PT4KHLK",
					"incident_number": 1234,
					"title":           "Server is on fire",
					"status":          "triggered",
					"urgency":         "high",
					"created_at":      "2024-01-15T10:00:00Z",
					"html_url":        "https://acme.pagerduty.com/incidents/PT4KHLK",
					"service":         map[string]any{"id": "PWIXJZS", "summary": "Web Service"},
					"assignments":     []any{map[string]any{"assignee": map[string]any{"summary": "John Doe"}}},
				}},
				"more": false,
			})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_list_incidents", "status": "triggered,acknowledged", "urgency": "high", "limit": 10,
	})
	if result["count"] != 1 || result["more"] != false {
		t.Fatalf("result = %v", result)
	}
	incident := batch4BodyMap(t, batch4BodySlice(t, result["incidents"])[0])
	if incident["title"] != "Server is on fire" || incident["service"] != "Web Service" || incident["service_id"] != "PWIXJZS" {
		t.Fatalf("incident = %v", incident)
	}
	assignments := batch4BodySlice(t, incident["assignments"])
	if len(assignments) != 1 || assignments[0] != "John Doe" {
		t.Fatalf("assignments = %v", assignments)
	}
	query := recorder.last(t).Query
	statuses := query["statuses[]"]
	if len(statuses) != 2 || statuses[0] != "triggered" || statuses[1] != "acknowledged" {
		t.Fatalf("statuses = %v", statuses)
	}
	if query.Get("limit") != "10" || query.Get("urgencies[]") != "high" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyGetIncident::test_missing_id
func TestBatch7Lane3PagerDutyGetIncidentMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_get_incident", "incident_id": "",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyGetIncident::test_successful_get
func TestBatch7Lane3PagerDutyGetIncident(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/incidents/PT4KHLK" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"incident": map[string]any{
				"id":          "PT4KHLK",
				"title":       "Server is on fire",
				"status":      "triggered",
				"urgency":     "high",
				"service":     map[string]any{"id": "PWIXJZS", "summary": "Web Service"},
				"assignments": []any{},
				"body":        map[string]any{"details": "CPU at 100%"},
			}})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_get_incident", "incident_id": "PT4KHLK",
	})
	if result["title"] != "Server is on fire" || result["details"] != "CPU at 100%" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyCreateIncident::test_missing_params
func TestBatch7Lane3PagerDutyCreateIncidentMissingParams(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_create_incident", "title": "", "service_id": "",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "title and service_id are required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyCreateIncident::test_successful_create
func TestBatch7Lane3PagerDutyCreateIncident(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/incidents" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("From"); got != "agent@example.com" {
				t.Fatalf("From = %q", got)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{"incident": map[string]any{
				"id":          "PT4KHLK",
				"title":       "Server is on fire",
				"status":      "triggered",
				"urgency":     "high",
				"service":     map[string]any{"id": "PWIXJZS", "summary": "Web Service"},
				"assignments": []any{},
			}})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_create_incident", "title": "Server is on fire", "service_id": "PWIXJZS",
	})
	if result["result"] != "created" || result["id"] != "PT4KHLK" {
		t.Fatalf("result = %v", result)
	}
	incident := batch4BodyMap(t, recorder.last(t).Body["incident"])
	if incident["type"] != "incident" || incident["urgency"] != "high" {
		t.Fatalf("incident payload = %v", incident)
	}
	if service := batch4BodyMap(t, incident["service"]); service["id"] != "PWIXJZS" || service["type"] != "service_reference" {
		t.Fatalf("service = %v", service)
	}
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyUpdateIncident::test_missing_status
func TestBatch7Lane3PagerDutyUpdateIncidentMissingStatus(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_update_incident", "incident_id": "PT4KHLK", "status": "",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "status is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyUpdateIncident::test_successful_acknowledge
func TestBatch7Lane3PagerDutyUpdateIncident(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPut || r.URL.Path != "/incidents/PT4KHLK" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"incident": map[string]any{
				"id": "PT4KHLK", "title": "Server is on fire", "status": "acknowledged",
				"service": map[string]any{"id": "PWIXJZS", "summary": "Web Service"}, "assignments": []any{},
			}})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_update_incident", "incident_id": "PT4KHLK", "status": "acknowledged",
	})
	if result["status"] != "acknowledged" {
		t.Fatalf("result = %v", result)
	}
	incident := batch4BodyMap(t, recorder.last(t).Body["incident"])
	if incident["type"] != "incident_reference" || incident["status"] != "acknowledged" {
		t.Fatalf("incident payload = %v", incident)
	}
	if _, present := incident["resolution"]; present {
		t.Fatalf("resolution should only be sent when resolving: %v", incident)
	}
}

// Parity: tools/tests/tools/test_pagerduty_tool.py::TestPagerdutyListServices::test_successful_list
func TestBatch7Lane3PagerDutyListServices(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/services" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"services": []any{map[string]any{
				"id":                      "PWIXJZS",
				"name":                    "Web Service",
				"description":             "Customer facing",
				"status":                  "active",
				"html_url":                "https://acme.pagerduty.com/services/PWIXJZS",
				"created_at":              "2024-01-01T00:00:00Z",
				"last_incident_timestamp": "2024-01-15T10:00:00Z",
			}}})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_list_services", "query": "Web", "limit": 5,
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	service := batch4BodyMap(t, batch4BodySlice(t, result["services"])[0])
	if service["name"] != "Web Service" || service["status"] != "active" {
		t.Fatalf("service = %v", service)
	}
	if query := recorder.last(t).Query; query.Get("query") != "Web" || query.Get("limit") != "5" {
		t.Fatalf("query = %v", query)
	}
}

// Parity-exempt: upstream pagerduty tests do not cover list_oncalls; the port
// preserves the projection and the []-suffix filters it sends.
func TestBatch7Lane3PagerDutyListOncalls(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/oncalls" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"oncalls": []any{map[string]any{
				"user":              map[string]any{"id": "PUSER1", "summary": "John Doe"},
				"schedule":          map[string]any{"id": "PSCHED1", "summary": "Primary"},
				"escalation_policy": map[string]any{"summary": "Default"},
				"escalation_level":  1,
				"start":             "2024-01-15T10:00:00Z",
				"end":               "2024-01-15T18:00:00Z",
			}}})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_list_oncalls", "schedule_id": "PSCHED1", "escalation_policy_id": "PEP1",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	oncall := batch4BodyMap(t, batch4BodySlice(t, result["oncalls"])[0])
	if oncall["user_name"] != "John Doe" || oncall["schedule_name"] != "Primary" ||
		oncall["escalation_policy"] != "Default" || batch7Lane3Int(oncall, "escalation_level", -1) != 1 {
		t.Fatalf("oncall = %v", oncall)
	}
	query := recorder.last(t).Query
	if query.Get("schedule_ids[]") != "PSCHED1" || query.Get("escalation_policy_ids[]") != "PEP1" {
		t.Fatalf("query = %v", query)
	}
}

// Parity-exempt: upstream pagerduty tests do not cover add_incident_note; the
// port pins the upstream note envelope and projection.
func TestBatch7Lane3PagerDutyAddIncidentNote(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/incidents/PT4KHLK/notes" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{"note": map[string]any{
				"id": "PNOTE1", "content": "Investigating", "created_at": "2024-01-15T11:00:00Z",
				"user": map[string]any{"summary": "John Doe"},
			}})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_add_incident_note", "incident_id": "PT4KHLK", "content": "Investigating",
	})
	if result["id"] != "PNOTE1" || result["content"] != "Investigating" || result["status"] != "created" {
		t.Fatalf("result = %v", result)
	}
	note := batch4BodyMap(t, recorder.last(t).Body["note"])
	if note["content"] != "Investigating" {
		t.Fatalf("note payload = %v", note)
	}
}

// Parity-exempt: upstream pagerduty tests do not cover escalation policies;
// the port preserves the projection and team-summary mapping.
func TestBatch7Lane3PagerDutyListEscalationPolicies(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/escalation_policies" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"escalation_policies": []any{map[string]any{
				"id":               "PEP1",
				"name":             "Default",
				"description":      "Default policy",
				"num_loops":        2,
				"teams":            []any{map[string]any{"summary": "SRE"}},
				"escalation_rules": []any{map[string]any{"id": "RULE1"}, map[string]any{"id": "RULE2"}},
			}}})
		})
	result := batch4Execute(t, registry, "pagerduty", map[string]any{
		"action": "pagerduty_list_escalation_policies", "query": "Default",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	policy := batch4BodyMap(t, batch4BodySlice(t, result["escalation_policies"])[0])
	if policy["name"] != "Default" || batch7Lane3Int(policy, "num_loops", -1) != 2 ||
		batch7Lane3Int(policy, "escalation_rules_count", -1) != 2 {
		t.Fatalf("policy = %v", policy)
	}
	if teams := batch4BodySlice(t, policy["teams"]); len(teams) != 1 || teams[0] != "SRE" {
		t.Fatalf("teams = %v", teams)
	}
}

// Parity-exempt: upstream pagerduty tests have no status matrix; the port
// keeps the shared "HTTP <code>: <body>" mapping and asserts it here.
func TestBatch7Lane3PagerDutyStatusMatrix(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		registry := batch7Lane3StatusRegistry(t, "pagerduty_tool", batch7Lane3PagerDutyEnv, status)
		result := batch4Execute(t, registry, "pagerduty", map[string]any{"action": "pagerduty_list_services"})
		got := batch4ErrorText(t, result)
		if !strings.HasPrefix(got, fmt.Sprintf("HTTP %d: ", status)) {
			t.Fatalf("status %d: error = %q", status, got)
		}
	}
}

// ------------------------------------------------------------- prometheus ---

// Parity: tools/tests/tools/test_prometheus_tool.py::test_prometheus_query_validation
func TestBatch7Lane3PrometheusQueryValidation(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "prometheus_tool", batch7Lane3PrometheusEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "prometheus", map[string]any{"action": "prometheus_query", "query": ""})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "Query must be between 1 and 1000 characters") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_prometheus_tool.py::test_prometheus_query_success
func TestBatch7Lane3PrometheusQuery(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "prometheus_tool", batch7Lane3PrometheusEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/query" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer prom-token" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"data":   map[string]any{"result": []any{map[string]any{"metric": map[string]any{}, "value": []any{123, "1"}}}},
			})
		})
	result := batch4Execute(t, registry, "prometheus", map[string]any{"action": "prometheus_query", "query": "up"})
	if result["success"] != true || result["query"] != "up" {
		t.Fatalf("result = %v", result)
	}
	if items := batch4BodySlice(t, result["result"]); len(items) != 1 {
		t.Fatalf("result items = %v", items)
	}
	if query := recorder.last(t).Query; query.Get("query") != "up" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_prometheus_tool.py::test_missing_base_url
func TestBatch7Lane3PrometheusMissingBaseURL(t *testing.T) {
	registry := newBatch7Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "prometheus", map[string]any{"action": "prometheus_query", "query": "up"})
	if result["success"] != false {
		t.Fatalf("result = %v", result)
	}
	if got := batch4ErrorText(t, result); !strings.Contains(got, "Missing required credential") {
		t.Fatalf("error = %q", got)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "PROMETHEUS_BASE_URL") {
		t.Fatalf("help = %q", help)
	}
}

// Parity: tools/tests/tools/test_prometheus_tool.py::test_prometheus_query_range_success
func TestBatch7Lane3PrometheusQueryRange(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "prometheus_tool", batch7Lane3PrometheusEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/query_range" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"data":   map[string]any{"result": []any{map[string]any{"values": []any{[]any{123, "1"}}}}},
			})
		})
	result := batch4Execute(t, registry, "prometheus", map[string]any{
		"action": "prometheus_query_range", "query": "up",
		"start": "2026-01-01T00:00:00Z", "end": "2026-01-01T01:00:00Z",
	})
	if result["success"] != true || result["step"] != "60s" {
		t.Fatalf("result = %v", result)
	}
	query := recorder.last(t).Query
	if query.Get("query") != "up" || query.Get("start") != "2026-01-01T00:00:00Z" ||
		query.Get("end") != "2026-01-01T01:00:00Z" || query.Get("step") != "60s" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_prometheus_tool.py::test_prometheus_non_200
func TestBatch7Lane3PrometheusNon200(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "prometheus_tool", batch7Lane3PrometheusEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Internal error"))
		})
	result := batch4Execute(t, registry, "prometheus", map[string]any{"action": "prometheus_query", "query": "up"})
	if got := batch4ErrorText(t, result); got != "Prometheus returned status 500" {
		t.Fatalf("error = %q", got)
	}
	if result["details"] != "Internal error" {
		t.Fatalf("details = %v", result["details"])
	}
}

// Parity: tools/tests/tools/test_prometheus_tool.py::test_timeout
func TestBatch7Lane3PrometheusTimeout(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "prometheus_tool", batch7Lane3PrometheusEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			time.Sleep(1500 * time.Millisecond)
			batch4WriteJSON(w, http.StatusOK, map[string]any{"status": "success", "data": map[string]any{"result": []any{}}})
		})
	result := batch4Execute(t, registry, "prometheus", map[string]any{
		"action": "prometheus_query", "query": "up", "timeout": 1,
	})
	if got := batch4ErrorText(t, result); !strings.Contains(strings.ToLower(got), "timed out") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_prometheus_tool.py::test_prometheus_connection_error
func TestBatch7Lane3PrometheusConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	registry := newBatch7Lane3Registry(
		WithBaseURL("prometheus_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(string) string { return "" }),
	)
	result := batch4Execute(t, registry, "prometheus", map[string]any{"action": "prometheus_query", "query": "up"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "Failed to connect to Prometheus") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream prometheus covers only a 500 response; 401/404/429
// share the same "Prometheus returned status <code>" branch and are pinned here.
func TestBatch7Lane3PrometheusStatusMatrix(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		registry := batch7Lane3StatusRegistry(t, "prometheus_tool", batch7Lane3PrometheusEnv, status)
		result := batch4Execute(t, registry, "prometheus", map[string]any{"action": "prometheus_query", "query": "up"})
		got := batch4ErrorText(t, result)
		if got != fmt.Sprintf("Prometheus returned status %d", status) {
			t.Fatalf("status %d: error = %q", status, got)
		}
	}
}

// ------------------------------------------------------------------ kafka ---

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaListTopics::test_missing_credentials
func TestBatch7Lane3KafkaMissingCredentials(t *testing.T) {
	registry := newBatch7Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "kafka", map[string]any{"action": "kafka_list_topics"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "KAFKA_REST_URL is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaListTopics::test_successful_list
func TestBatch7Lane3KafkaListTopics(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/v3/clusters/cluster-abc/topics" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"data": []any{
				map[string]any{"topic_name": "orders", "partitions_count": 6, "replication_factor": 3, "is_internal": false},
				map[string]any{"topic_name": "events", "partitions_count": 3, "replication_factor": 3, "is_internal": false},
			}})
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{"action": "kafka_list_topics"})
	if result["count"] != 2 {
		t.Fatalf("result = %v", result)
	}
	topic := batch4BodyMap(t, batch4BodySlice(t, result["topics"])[0])
	if topic["name"] != "orders" || batch7Lane3Int(topic, "partitions_count", -1) != 6 {
		t.Fatalf("topic = %v", topic)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaGetTopic::test_missing_name
func TestBatch7Lane3KafkaGetTopicMissingName(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{"action": "kafka_get_topic", "topic_name": ""})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "topic_name is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaGetTopic::test_successful_get
func TestBatch7Lane3KafkaGetTopic(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/v3/clusters/cluster-abc/topics/orders" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"topic_name": "orders", "partitions_count": 6, "replication_factor": 3,
				"is_internal": false, "cluster_id": "cluster-abc",
			})
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{"action": "kafka_get_topic", "topic_name": "orders"})
	if result["name"] != "orders" || result["cluster_id"] != "cluster-abc" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaCreateTopic::test_missing_name
func TestBatch7Lane3KafkaCreateTopicMissingName(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{"action": "kafka_create_topic", "topic_name": ""})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaCreateTopic::test_successful_create
func TestBatch7Lane3KafkaCreateTopic(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/v3/clusters/cluster-abc/topics" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{
				"topic_name": "new-topic", "partitions_count": 3, "replication_factor": 3,
			})
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{
		"action": "kafka_create_topic", "topic_name": "new-topic", "partitions_count": 3,
	})
	if result["name"] != "new-topic" || batch7Lane3Int(result, "partitions_count", -1) != 3 {
		t.Fatalf("result = %v", result)
	}
	payload := recorder.last(t).Body
	if payload["topic_name"] != "new-topic" || batch7Lane3Int(payload, "partitions_count", -1) != 3 ||
		batch7Lane3Int(payload, "replication_factor", -1) != 3 {
		t.Fatalf("payload = %v", payload)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaProduceMessage::test_missing_params
func TestBatch7Lane3KafkaProduceMessageMissingParams(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{
		"action": "kafka_produce_message", "topic_name": "", "value": "",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "topic_name and value are required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaProduceMessage::test_successful_produce
func TestBatch7Lane3KafkaProduceMessage(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/v3/clusters/cluster-abc/topics/orders/records" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"topic_name": "orders", "partition_id": 0, "offset": 42, "timestamp": "2024-01-15T12:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{
		"action": "kafka_produce_message", "topic_name": "orders",
		"value": `{"order_id": 123}`, "key": "order-123",
	})
	if result["topic"] != "orders" || batch7Lane3Int(result, "offset", -1) != 42 {
		t.Fatalf("result = %v", result)
	}
	payload := recorder.last(t).Body
	value := batch4BodyMap(t, payload["value"])
	if value["type"] != "JSON" || value["data"] != `{"order_id": 123}` {
		t.Fatalf("value = %v", value)
	}
	key := batch4BodyMap(t, payload["key"])
	if key["type"] != "STRING" || key["data"] != "order-123" {
		t.Fatalf("key = %v", key)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaListConsumerGroups::test_successful_list
func TestBatch7Lane3KafkaListConsumerGroups(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/v3/clusters/cluster-abc/consumer-groups" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"consumer_group_id": "my-group", "is_simple": false, "state": "STABLE",
				"coordinator": map[string]any{"related": "broker-1"},
			}}})
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{"action": "kafka_list_consumer_groups"})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	group := batch4BodyMap(t, batch4BodySlice(t, result["consumer_groups"])[0])
	if group["id"] != "my-group" || group["state"] != "STABLE" || group["coordinator_id"] != "broker-1" {
		t.Fatalf("group = %v", group)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaGetConsumerGroupLag::test_missing_id
func TestBatch7Lane3KafkaGetConsumerGroupLagMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{
		"action": "kafka_get_consumer_group_lag", "consumer_group_id": "",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "consumer_group_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_kafka_tool.py::TestKafkaGetConsumerGroupLag::test_successful_get
func TestBatch7Lane3KafkaGetConsumerGroupLag(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "kafka_tool", batch7Lane3KafkaEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/v3/clusters/cluster-abc/consumer-groups/my-group/lag-summary" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"consumer_group_id": "my-group", "max_lag": 100, "max_lag_topic_name": "orders",
				"max_lag_partition_id": 2, "max_lag_consumer_id": "consumer-1", "total_lag": 250,
			})
		})
	result := batch4Execute(t, registry, "kafka", map[string]any{
		"action": "kafka_get_consumer_group_lag", "consumer_group_id": "my-group",
	})
	if batch7Lane3Int(result, "max_lag", -1) != 100 || batch7Lane3Int(result, "total_lag", -1) != 250 ||
		result["max_lag_topic"] != "orders" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream kafka tests have no status matrix; the port keeps
// the shared "HTTP <code>: <body>" mapping and asserts it here.
func TestBatch7Lane3KafkaStatusMatrix(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		registry := batch7Lane3StatusRegistry(t, "kafka_tool", batch7Lane3KafkaEnv, status)
		result := batch4Execute(t, registry, "kafka", map[string]any{"action": "kafka_list_topics"})
		got := batch4ErrorText(t, result)
		if !strings.HasPrefix(got, fmt.Sprintf("HTTP %d: ", status)) {
			t.Fatalf("status %d: error = %q", status, got)
		}
	}
}

// -------------------------------------------------------------------- n8n ---

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nListWorkflows::test_missing_credentials
func TestBatch7Lane3N8nMissingCredentials(t *testing.T) {
	registry := newBatch7Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_list_workflows"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "n8n credentials not configured") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nListWorkflows::test_successful_list
func TestBatch7Lane3N8nListWorkflows(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/workflows" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("X-N8N-API-KEY"); got != "test-api-key-123" {
				t.Fatalf("X-N8N-API-KEY = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{
					"id":        "wf1",
					"name":      "Email Workflow",
					"active":    true,
					"createdAt": "2025-01-10T11:00:00Z",
					"updatedAt": "2025-01-11T12:00:00Z",
					"tags":      []any{map[string]any{"name": "production"}},
					"nodes":     []any{map[string]any{"name": "Start"}, map[string]any{"name": "Email"}},
				}},
				"nextCursor": "cursor-2",
			})
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{
		"action": "n8n_list_workflows", "active": "true", "tags": "production", "name": "Email",
	})
	if result["count"] != 1 || result["next_cursor"] != "cursor-2" {
		t.Fatalf("result = %v", result)
	}
	workflow := batch4BodyMap(t, batch4BodySlice(t, result["workflows"])[0])
	if workflow["name"] != "Email Workflow" || workflow["active"] != true || batch7Lane3Int(workflow, "node_count", -1) != 2 {
		t.Fatalf("workflow = %v", workflow)
	}
	if tags := batch4BodySlice(t, workflow["tags"]); len(tags) != 1 || tags[0] != "production" {
		t.Fatalf("tags = %v", tags)
	}
	query := recorder.last(t).Query
	if query.Get("limit") != "100" || query.Get("active") != "true" ||
		query.Get("tags") != "production" || query.Get("name") != "Email" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nListWorkflows::test_pagination
func TestBatch7Lane3N8nListWorkflowsPagination(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusOK, map[string]any{"data": []any{}, "nextCursor": "next-page"})
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_list_workflows", "cursor": "page-1"})
	if result["count"] != 0 || result["next_cursor"] != "next-page" {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Query.Get("cursor"); got != "page-1" {
		t.Fatalf("cursor = %q", got)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nGetWorkflow::test_missing_id
func TestBatch7Lane3N8nGetWorkflowMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_get_workflow", "workflow_id": ""})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "workflow_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nGetWorkflow::test_successful_get
func TestBatch7Lane3N8nGetWorkflow(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/workflows/wf1" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "wf1", "name": "Email Workflow", "active": true,
				"createdAt": "2025-01-10T11:00:00Z", "updatedAt": "2025-01-11T12:00:00Z",
				"tags":  []any{map[string]any{"name": "production"}},
				"nodes": []any{map[string]any{"name": "Start", "type": "n8n-nodes-base.start", "position": []any{100, 200}}},
			})
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_get_workflow", "workflow_id": "wf1"})
	if result["name"] != "Email Workflow" || batch7Lane3Int(result, "node_count", -1) != 1 {
		t.Fatalf("result = %v", result)
	}
	node := batch4BodyMap(t, batch4BodySlice(t, result["nodes"])[0])
	if node["type"] != "n8n-nodes-base.start" {
		t.Fatalf("node = %v", node)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nActivateWorkflow::test_missing_id
func TestBatch7Lane3N8nActivateWorkflowMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_activate_workflow", "workflow_id": ""})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nActivateWorkflow::test_successful_activate
func TestBatch7Lane3N8nActivateWorkflow(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/workflows/wf1/activate" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "wf1", "name": "Email Workflow", "active": true})
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_activate_workflow", "workflow_id": "wf1"})
	if result["id"] != "wf1" || result["active"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nDeactivateWorkflow::test_successful_deactivate
func TestBatch7Lane3N8nDeactivateWorkflow(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/workflows/wf1/deactivate" {
				t.Fatalf("%s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "wf1", "name": "Email Workflow", "active": false})
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_deactivate_workflow", "workflow_id": "wf1"})
	if result["active"] != false {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Path != "/api/v1/workflows/wf1/deactivate" {
		t.Fatalf("path = %s", recorder.last(t).Path)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nListExecutions::test_successful_list
func TestBatch7Lane3N8nListExecutions(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/executions" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "exec1", "workflowId": "wf1", "status": "success", "mode": "trigger",
				"finished": true, "startedAt": "2025-01-11T12:00:00Z", "stoppedAt": "2025-01-11T12:00:05Z",
			}}})
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{
		"action": "n8n_list_executions", "workflow_id": "wf1", "status": "success",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	execution := batch4BodyMap(t, batch4BodySlice(t, result["executions"])[0])
	if execution["status"] != "success" || execution["workflow_id"] != "wf1" {
		t.Fatalf("execution = %v", execution)
	}
	query := recorder.last(t).Query
	if query.Get("workflowId") != "wf1" || query.Get("status") != "success" || query.Get("limit") != "100" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nGetExecution::test_missing_id
func TestBatch7Lane3N8nGetExecutionMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_get_execution", "execution_id": ""})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "execution_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_n8n_tool.py::TestN8nGetExecution::test_successful_get
func TestBatch7Lane3N8nGetExecution(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "n8n_tool", batch7Lane3N8nEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/executions/exec1" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "exec1", "workflowId": "wf1", "status": "success", "mode": "trigger",
				"finished": true, "startedAt": "2025-01-11T12:00:00Z", "stoppedAt": "2025-01-11T12:00:05Z",
				"retryOf": "exec0", "retrySuccessId": "exec2",
				"data": map[string]any{"resultData": map[string]any{"runData": map[string]any{}}},
			})
		})
	result := batch4Execute(t, registry, "n8n", map[string]any{
		"action": "n8n_get_execution", "execution_id": "exec1", "include_data": true,
	})
	if result["retry_of"] != "exec0" || result["retry_success_id"] != "exec2" {
		t.Fatalf("result = %v", result)
	}
	if _, present := result["data"]; !present {
		t.Fatalf("data missing: %v", result)
	}
	if got := recorder.last(t).Query.Get("includeData"); got != "true" {
		t.Fatalf("includeData = %q", got)
	}
}

// Parity-exempt: upstream n8n tests have no status matrix; the port pins the
// upstream 401/403/404 wording and the generic 429 branch.
func TestBatch7Lane3N8nStatusMatrix(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Invalid n8n API key"},
		{http.StatusNotFound, "n8n resource not found"},
		{http.StatusTooManyRequests, "n8n API error (HTTP 429): boom"},
	}
	for _, testCase := range cases {
		registry := batch7Lane3StatusRegistry(t, "n8n_tool", batch7Lane3N8nEnv, testCase.status)
		result := batch4Execute(t, registry, "n8n", map[string]any{"action": "n8n_list_workflows"})
		if got := batch4ErrorText(t, result); got != testCase.want {
			t.Fatalf("status %d: error = %q, want %q", testCase.status, got, testCase.want)
		}
	}
}

// ------------------------------------------------------------------ tines ---

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesListStories::test_missing_credentials
func TestBatch7Lane3TinesMissingCredentials(t *testing.T) {
	registry := newBatch7Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "tines", map[string]any{"action": "tines_list_stories"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "TINES_DOMAIN and TINES_API_KEY are required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesListStories::test_successful_list
func TestBatch7Lane3TinesListStories(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/stories" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tines-key" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"stories": []any{map[string]any{
				"id": 1, "name": "Phishing triage", "description": "Auto triage",
				"disabled": false, "mode": "live", "team_id": 2,
				"tags": []any{"security"}, "created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-02T00:00:00Z",
			}}})
		})
	result := batch4Execute(t, registry, "tines", map[string]any{
		"action": "tines_list_stories", "team_id": 2, "search": "Phishing",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	story := batch4BodyMap(t, batch4BodySlice(t, result["stories"])[0])
	if story["name"] != "Phishing triage" || story["mode"] != "live" {
		t.Fatalf("story = %v", story)
	}
	if tags := batch4BodySlice(t, story["tags"]); len(tags) != 1 || tags[0] != "security" {
		t.Fatalf("tags = %v", tags)
	}
	query := recorder.last(t).Query
	if query.Get("team_id") != "2" || query.Get("search") != "Phishing" || query.Get("per_page") != "20" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesGetStory::test_missing_id
func TestBatch7Lane3TinesGetStoryMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "tines", map[string]any{"action": "tines_get_story", "story_id": 0})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "story_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesGetStory::test_successful_get
func TestBatch7Lane3TinesGetStory(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/stories/7" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": 7, "name": "Phishing triage", "description": "Auto triage",
				"disabled": false, "mode": "live", "team_id": 2, "folder_id": 3,
				"tags": []any{"security"}, "send_to_story_enabled": true, "entry_agent_id": 11,
				"exit_agents": []any{12}, "created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-02T00:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "tines", map[string]any{"action": "tines_get_story", "story_id": 7})
	if result["name"] != "Phishing triage" || batch7Lane3Int(result, "folder_id", -1) != 3 ||
		result["send_to_story_enabled"] != true {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesListActions::test_successful_list
func TestBatch7Lane3TinesListActions(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/actions" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"agents": []any{map[string]any{
				"id": 21, "name": "Fetch headers", "type": "HTTPRequestAgent", "story_id": 7,
				"disabled": false, "created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-02T00:00:00Z",
			}}})
		})
	result := batch4Execute(t, registry, "tines", map[string]any{
		"action": "tines_list_actions", "story_id": 7, "action_type": "HTTPRequestAgent",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	action := batch4BodyMap(t, batch4BodySlice(t, result["actions"])[0])
	if action["name"] != "Fetch headers" || action["type"] != "HTTPRequestAgent" {
		t.Fatalf("action = %v", action)
	}
	query := recorder.last(t).Query
	if query.Get("story_id") != "7" || query.Get("action_type") != "HTTPRequestAgent" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesGetAction::test_missing_id
func TestBatch7Lane3TinesGetActionMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "tines", map[string]any{"action": "tines_get_action", "action_id": 0})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "action_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesGetAction::test_successful_get
func TestBatch7Lane3TinesGetAction(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/actions/21" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": 21, "name": "Fetch headers", "type": "HTTPRequestAgent", "description": "Fetch",
				"story_id": 7, "disabled": false, "sources": []any{1}, "receivers": []any{2},
				"options":    map[string]any{"url": "https://example.com"},
				"created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-02T00:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "tines", map[string]any{"action": "tines_get_action", "action_id": 21})
	if result["name"] != "Fetch headers" || result["description"] != "Fetch" {
		t.Fatalf("result = %v", result)
	}
	if options := batch4BodyMap(t, result["options"]); options["url"] != "https://example.com" {
		t.Fatalf("options = %v", options)
	}
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesGetActionLogs::test_missing_id
func TestBatch7Lane3TinesGetActionLogsMissingID(t *testing.T) {
	registry, _ := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "tines", map[string]any{"action": "tines_get_action_logs", "action_id": 0})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_tines_tool.py::TestTinesGetActionLogs::test_successful_get
func TestBatch7Lane3TinesGetActionLogs(t *testing.T) {
	registry, recorder := batch7Lane3TestRegistry(t, "tines_tool", batch7Lane3TinesEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/v1/actions/21/logs" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"action_logs": []any{map[string]any{
				"id": 31, "level": 4, "message": "boom", "created_at": "2024-01-01T00:00:00Z",
			}}})
		})
	result := batch4Execute(t, registry, "tines", map[string]any{
		"action": "tines_get_action_logs", "action_id": 21, "level": 4,
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	entry := batch4BodyMap(t, batch4BodySlice(t, result["logs"])[0])
	if entry["message"] != "boom" || batch7Lane3Int(entry, "level", -1) != 4 {
		t.Fatalf("log = %v", entry)
	}
	if query := recorder.last(t).Query; query.Get("level") != "4" || query.Get("per_page") != "20" {
		t.Fatalf("query = %v", query)
	}
}

// Parity-exempt: upstream tines tests have no status matrix; the port keeps
// the shared "HTTP <code>: <body>" mapping and asserts it here.
func TestBatch7Lane3TinesStatusMatrix(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		registry := batch7Lane3StatusRegistry(t, "tines_tool", batch7Lane3TinesEnv, status)
		result := batch4Execute(t, registry, "tines", map[string]any{"action": "tines_list_stories"})
		got := batch4ErrorText(t, result)
		if !strings.HasPrefix(got, fmt.Sprintf("HTTP %d: ", status)) {
			t.Fatalf("status %d: error = %q", status, got)
		}
	}
}

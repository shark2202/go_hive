package integrations

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// batch4GraphQLOperation extracts the operation name from a GraphQL document.
func batch4GraphQLOperation(query string) string {
	fields := strings.Fields(query)
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] != "query" && fields[index] != "mutation" {
			continue
		}
		name := fields[index+1]
		if cut := strings.IndexByte(name, '('); cut >= 0 {
			name = name[:cut]
		}
		if name != "" {
			return name
		}
	}
	return ""
}

// batch4LinearTestRegistry answers every GraphQL operation from responses and
// records the request bodies for assertions.
func batch4LinearTestRegistry(t *testing.T, responses map[string]any, opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	return batch4TestRegistry(t, "linear_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		query, _ := body["query"].(string)
		operation := batch4GraphQLOperation(query)
		payload, ok := responses[operation]
		if !ok {
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"errors": []any{map[string]any{"message": "unexpected operation " + operation}},
			})
			return
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"data": payload})
	}, opts...)
}

func batch4LinearVariables(t *testing.T, recorder *batch4Recorder) map[string]any {
	t.Helper()
	return batch4BodyMap(t, recorder.last(t).Body["variables"])
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_create
func TestBatch4LinearIssueCreate(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"IssueCreate": map[string]any{"issueCreate": map[string]any{
			"success": true,
			"issue":   map[string]any{"id": "issue-456", "identifier": "ENG-123", "title": "Test Issue", "url": "https://linear.app/team/issue/ENG-123"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_issue_create", "title": "Test Issue", "team_id": "team-123",
		"description": "Test description", "priority": 2, "label_ids": []any{"label-1"},
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	issue := batch4BodyMap(t, result["issue"])
	if issue["identifier"] != "ENG-123" {
		t.Fatalf("issue = %v", issue)
	}
	variables := batch4LinearVariables(t, recorder)
	input := batch4BodyMap(t, variables["input"])
	if input["title"] != "Test Issue" || input["teamId"] != "team-123" || input["priority"] != float64(2) {
		t.Fatalf("input = %v", input)
	}
	if labels, _ := input["labelIds"].([]any); len(labels) != 1 || labels[0] != "label-1" {
		t.Fatalf("labels = %v", input["labelIds"])
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_get
func TestBatch4LinearIssueGet(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"Issue": map[string]any{"issue": map[string]any{
			"id": "issue-123", "identifier": "ENG-123", "title": "Test Issue",
			"state": map[string]any{"name": "In Progress"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_issue_get", "issue_id": "ENG-123"})
	if result["identifier"] != "ENG-123" || batch4BodyMap(t, result["state"])["name"] != "In Progress" {
		t.Fatalf("result = %v", result)
	}
	if variables := batch4LinearVariables(t, recorder); variables["id"] != "ENG-123" {
		t.Fatalf("variables = %v", variables)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_update
func TestBatch4LinearIssueUpdate(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"IssueUpdate": map[string]any{"issueUpdate": map[string]any{
			"success": true,
			"issue":   map[string]any{"id": "issue-123", "title": "Updated Title"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_issue_update", "issue_id": "issue-123", "title": "Updated Title", "priority": 1,
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	input := batch4BodyMap(t, batch4LinearVariables(t, recorder)["input"])
	if input["title"] != "Updated Title" || input["priority"] != float64(1) {
		t.Fatalf("input = %v", input)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_delete
func TestBatch4LinearIssueDelete(t *testing.T) {
	registry, _ := batch4LinearTestRegistry(t, map[string]any{
		"IssueDelete": map[string]any{"issueDelete": map[string]any{"success": true}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_issue_delete", "issue_id": "issue-123"})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_search
func TestBatch4LinearIssueSearch(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"Issues": map[string]any{"issues": map[string]any{
			"nodes": []any{
				map[string]any{"id": "1", "identifier": "ENG-1", "title": "Issue 1"},
				map[string]any{"id": "2", "identifier": "ENG-2", "title": "Issue 2"},
			},
			"pageInfo": map[string]any{"hasNextPage": true},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_issue_search", "query": "bug", "team_id": "team-123", "limit": 10,
		"label_ids": []any{"label-1"},
	})
	if result["total"] != 2 || result["hasNextPage"] != true {
		t.Fatalf("result = %v", result)
	}
	issues := result["issues"].([]any)
	if len(issues) != 2 || issues[0].(map[string]any)["identifier"] != "ENG-1" {
		t.Fatalf("issues = %v", issues)
	}
	variables := batch4LinearVariables(t, recorder)
	if variables["first"] != float64(10) {
		t.Fatalf("first = %v", variables["first"])
	}
	filter := batch4BodyMap(t, variables["filter"])
	if _, present := filter["or"]; !present {
		t.Fatalf("filter = %v", filter)
	}
	if batch4BodyMap(t, filter["team"])["id"].(map[string]any)["eq"] != "team-123" {
		t.Fatalf("filter = %v", filter)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_add_comment
func TestBatch4LinearIssueAddComment(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"CommentCreate": map[string]any{"commentCreate": map[string]any{
			"success": true,
			"comment": map[string]any{"id": "comment-123", "body": "Test comment"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_issue_add_comment", "issue_id": "issue-123", "body": "Test comment",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	input := batch4BodyMap(t, batch4LinearVariables(t, recorder)["input"])
	if input["issueId"] != "issue-123" || input["body"] != "Test comment" {
		t.Fatalf("input = %v", input)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestProjectTools::test_linear_project_create
func TestBatch4LinearProjectCreate(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"ProjectCreate": map[string]any{"projectCreate": map[string]any{
			"success": true,
			"project": map[string]any{"id": "project-123", "name": "Q1 Roadmap"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_project_create", "name": "Q1 Roadmap", "team_ids": []any{"team-123"},
		"description": "Q1 goals", "state": "planned",
	})
	if result["success"] != true || batch4BodyMap(t, result["project"])["name"] != "Q1 Roadmap" {
		t.Fatalf("result = %v", result)
	}
	input := batch4BodyMap(t, batch4LinearVariables(t, recorder)["input"])
	if teams, _ := input["teamIds"].([]any); len(teams) != 1 || teams[0] != "team-123" {
		t.Fatalf("teamIds = %v", input["teamIds"])
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestProjectTools::test_linear_project_get
func TestBatch4LinearProjectGet(t *testing.T) {
	registry, _ := batch4LinearTestRegistry(t, map[string]any{
		"Project": map[string]any{"project": map[string]any{"id": "project-123", "name": "Q1 Roadmap", "progress": 0.5}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_project_get", "project_id": "project-123"})
	if result["name"] != "Q1 Roadmap" || result["progress"] != float64(0.5) {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestProjectTools::test_linear_project_update
func TestBatch4LinearProjectUpdate(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"ProjectUpdate": map[string]any{"projectUpdate": map[string]any{
			"success": true,
			"project": map[string]any{"id": "project-123", "name": "New Name"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_project_update", "project_id": "project-123", "name": "New Name",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	input := batch4BodyMap(t, batch4LinearVariables(t, recorder)["input"])
	if input["name"] != "New Name" {
		t.Fatalf("input = %v", input)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestProjectTools::test_linear_project_list
func TestBatch4LinearProjectList(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"Projects": map[string]any{"projects": map[string]any{
			"nodes":    []any{map[string]any{"id": "1", "name": "Project 1"}, map[string]any{"id": "2", "name": "Project 2"}},
			"pageInfo": map[string]any{"hasNextPage": false},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_project_list", "team_id": "team-1", "state": "started"})
	if result["total"] != 2 || result["hasNextPage"] != false {
		t.Fatalf("result = %v", result)
	}
	filter := batch4BodyMap(t, batch4LinearVariables(t, recorder)["filter"])
	if _, present := filter["accessibleTeams"]; !present {
		t.Fatalf("filter = %v", filter)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestTeamTools::test_linear_teams_list
func TestBatch4LinearTeamsList(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"Teams": map[string]any{"teams": map[string]any{"nodes": []any{
			map[string]any{"id": "team-1", "name": "Engineering", "key": "ENG"},
			map[string]any{"id": "team-2", "name": "Design", "key": "DES"},
		}}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_teams_list"})
	if result["total"] != 2 {
		t.Fatalf("result = %v", result)
	}
	teams := result["teams"].([]any)
	if teams[0].(map[string]any)["key"] != "ENG" {
		t.Fatalf("teams = %v", teams)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestTeamTools::test_linear_team_get
func TestBatch4LinearTeamGet(t *testing.T) {
	registry, _ := batch4LinearTestRegistry(t, map[string]any{
		"Team": map[string]any{"team": map[string]any{"id": "t1", "name": "Eng", "key": "ENG"}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_team_get", "team_id": "t1"})
	if result["key"] != "ENG" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestTeamTools::test_linear_workflow_states_get
func TestBatch4LinearWorkflowStatesGet(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"WorkflowStates": map[string]any{"workflowStates": map[string]any{"nodes": []any{
			map[string]any{"id": "state-1", "name": "Backlog", "type": "backlog"},
			map[string]any{"id": "state-2", "name": "In Progress", "type": "started"},
			map[string]any{"id": "state-3", "name": "Done", "type": "completed"},
		}}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_workflow_states_get", "team_id": "t1"})
	if result["total"] != 3 {
		t.Fatalf("result = %v", result)
	}
	if batch4LinearVariables(t, recorder)["teamId"] != "t1" {
		t.Fatalf("variables = %v", batch4LinearVariables(t, recorder))
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestLabelTools::test_linear_label_create
func TestBatch4LinearLabelCreate(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"IssueLabelCreate": map[string]any{"issueLabelCreate": map[string]any{
			"success":    true,
			"issueLabel": map[string]any{"id": "label-123", "name": "bug", "color": "#FF0000"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_label_create", "name": "bug", "team_id": "team-123", "color": "#FF0000",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	input := batch4BodyMap(t, batch4LinearVariables(t, recorder)["input"])
	if input["name"] != "bug" || input["teamId"] != "team-123" || input["color"] != "#FF0000" {
		t.Fatalf("input = %v", input)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestLabelTools::test_linear_labels_list
func TestBatch4LinearLabelsList(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"IssueLabels": map[string]any{"issueLabels": map[string]any{"nodes": []any{
			map[string]any{"id": "label-1", "name": "bug"},
			map[string]any{"id": "label-2", "name": "feature"},
		}}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_labels_list", "team_id": "team-123"})
	if result["total"] != 2 {
		t.Fatalf("result = %v", result)
	}
	variables := batch4LinearVariables(t, recorder)
	filter := batch4BodyMap(t, variables["filter"])
	if batch4BodyMap(t, filter["team"])["id"].(map[string]any)["eq"] != "team-123" {
		t.Fatalf("filter = %v", filter)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestUserTools::test_linear_users_list
func TestBatch4LinearUsersList(t *testing.T) {
	registry, _ := batch4LinearTestRegistry(t, map[string]any{
		"Users": map[string]any{"users": map[string]any{"nodes": []any{
			map[string]any{"id": "user-1", "name": "Alice", "email": "alice@example.com"},
			map[string]any{"id": "user-2", "name": "Bob", "email": "bob@example.com"},
		}}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_users_list"})
	if result["total"] != 2 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestUserTools::test_linear_user_get
func TestBatch4LinearUserGet(t *testing.T) {
	registry, _ := batch4LinearTestRegistry(t, map[string]any{
		"User": map[string]any{"user": map[string]any{"id": "u1", "name": "Alice"}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_user_get", "user_id": "u1"})
	if result["name"] != "Alice" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestUserTools::test_linear_viewer
func TestBatch4LinearViewer(t *testing.T) {
	registry, _ := batch4LinearTestRegistry(t, map[string]any{
		"Viewer": map[string]any{"viewer": map[string]any{"id": "me", "name": "Current User"}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_viewer"})
	if result["name"] != "Current User" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestToolRegistration::test_register_tools_registers_all_tools
func TestBatch4LinearCyclesList(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"Cycles": map[string]any{"cycles": map[string]any{
			"nodes":    []any{map[string]any{"id": "cycle-1", "number": 1, "name": "Sprint 1"}},
			"pageInfo": map[string]any{"hasNextPage": true},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_cycles_list", "team_id": "team-123", "limit": 10})
	if result["total"] != 1 || result["hasNextPage"] != true {
		t.Fatalf("result = %v", result)
	}
	variables := batch4LinearVariables(t, recorder)
	if variables["first"] != float64(10) {
		t.Fatalf("first = %v", variables["first"])
	}
	filter := batch4BodyMap(t, variables["filter"])
	if batch4BodyMap(t, filter["team"])["id"].(map[string]any)["eq"] != "team-123" {
		t.Fatalf("filter = %v", filter)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestToolRegistration::test_register_tools_registers_all_tools
func TestBatch4LinearIssueCommentsList(t *testing.T) {
	registry, _ := batch4LinearTestRegistry(t, map[string]any{
		"Issue": map[string]any{"issue": map[string]any{"comments": map[string]any{"nodes": []any{
			map[string]any{"id": "c1", "body": "first"},
			map[string]any{"id": "c2", "body": "second"},
		}}}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_issue_comments_list", "issue_id": "issue-1"})
	if result["total"] != 2 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestToolRegistration::test_register_tools_registers_all_tools
func TestBatch4LinearIssueRelationCreate(t *testing.T) {
	registry, recorder := batch4LinearTestRegistry(t, map[string]any{
		"IssueRelationCreate": map[string]any{"issueRelationCreate": map[string]any{
			"success":       true,
			"issueRelation": map[string]any{"id": "relation-1", "type": "blocks"},
		}},
	})
	result := batch4Execute(t, registry, "linear", map[string]any{
		"action": "linear_issue_relation_create", "issue_id": "issue-1", "related_issue_id": "issue-2", "relation_type": "blocks",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	input := batch4BodyMap(t, batch4LinearVariables(t, recorder)["input"])
	if input["issueId"] != "issue-1" || input["relatedIssueId"] != "issue-2" || input["type"] != "blocks" {
		t.Fatalf("input = %v", input)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestLinearClient::test_handle_response_errors
func TestBatch4LinearHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid or expired"},
		{http.StatusForbidden, "Insufficient permissions"},
		{http.StatusTooManyRequests, "rate limit"},
		{http.StatusNotFound, "Linear API error (HTTP 404)"},
		{http.StatusInternalServerError, "Linear API error (HTTP 500)"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch4TestRegistry(t, "linear_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch4WriteJSON(w, tc.status, map[string]any{"message": "failure"})
			})
			result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_teams_list"})
			if got := batch4ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestLinearClient::test_handle_response_graphql_error
func TestBatch4LinearGraphQLError(t *testing.T) {
	registry, _ := batch4TestRegistry(t, "linear_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"errors": []any{map[string]any{"message": "Issue not found"}},
		})
	})
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_issue_get", "issue_id": "nope"})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "GraphQL error") || !strings.Contains(got, "Issue not found") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestToolRegistration::test_no_credentials_returns_error
func TestBatch4LinearMissingCredentials(t *testing.T) {
	registry := newBatch4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_teams_list"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "not configured") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_create_timeout
func TestBatch4LinearTimeout(t *testing.T) {
	registry := newBatch4Registry(
		WithCredential("linear_tool", "test-token"),
		WithHTTPClient(&http.Client{Transport: batch4TimeoutTransport{}}),
	)
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_teams_list"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "timed out") {
		t.Fatalf("error = %q", got)
	}
}

type batch4NetworkTransport struct{}

func (batch4NetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection failed")
}

// Parity: tools/tests/tools/test_linear_tool.py::TestIssueTools::test_linear_issue_get_network_error
func TestBatch4LinearNetworkError(t *testing.T) {
	registry := newBatch4Registry(
		WithCredential("linear_tool", "test-token"),
		WithHTTPClient(&http.Client{Transport: batch4NetworkTransport{}}),
	)
	result := batch4Execute(t, registry, "linear", map[string]any{"action": "linear_issue_get", "issue_id": "1"})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "Network error") || !strings.Contains(got, "connection failed") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_linear_tool.py::TestToolRegistration::test_credentials_from_env_var
func TestBatch4LinearCredentialsFromEnvironment(t *testing.T) {
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"teams": map[string]any{"nodes": []any{}}},
		})
	}))
	t.Cleanup(server.Close)
	registry := newBatch4Registry(
		WithBaseURL("linear_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string {
			if name == "LINEAR_API_KEY" {
				return "lin_api_env_key"
			}
			return ""
		}),
	)
	batch4Execute(t, registry, "linear", map[string]any{"action": "linear_teams_list"})
	if got := recorder.last(t).Header.Get("Authorization"); got != "lin_api_env_key" {
		t.Fatalf("Authorization = %q", got)
	}
}

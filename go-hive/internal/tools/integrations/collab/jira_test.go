package collab

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func batch5Lane1JiraRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch5Lane1Recorder) {
	t.Helper()
	env := map[string]string{
		"JIRA_DOMAIN": "test.atlassian.net",
		"JIRA_EMAIL":  "user@test.com",
	}
	return batch5Lane1TestRegistry(t, "jira_tool", env, handler, opts...)
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraSearchIssues::test_successful_search
func TestBatch5Lane1JiraSearchIssuesSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"issues": []any{
				map[string]any{
					"key": "TEST-1",
					"fields": map[string]any{
						"summary":   "Fix login bug",
						"status":    map[string]any{"name": "In Progress"},
						"assignee":  map[string]any{"displayName": "John Doe"},
						"priority":  map[string]any{"name": "High"},
						"issuetype": map[string]any{"name": "Bug"},
					},
				},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action": "jira_search_issues",
		"jql":    "project = TEST",
	})
	if result["count"] != 1 {
		t.Fatalf("count = %v, want 1", result["count"])
	}
	issues := batch5Lane1SliceField(t, result, "issues")
	issue := issues[0].(map[string]any)
	if issue["key"] != "TEST-1" || issue["status"] != "In Progress" || issue["assignee"] != "John Doe" {
		t.Fatalf("issue = %v", issue)
	}
	request := recorder.last(t)
	if request.Method != http.MethodGet || request.Path != "/search/jql" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	if request.Query.Get("jql") != "project = TEST" {
		t.Fatalf("jql query = %q", request.Query.Get("jql"))
	}
	if request.Query.Get("maxResults") != "25" {
		t.Fatalf("maxResults = %q, want 25", request.Query.Get("maxResults"))
	}
	if request.Query.Get("fields") != "summary,status,assignee,priority,issuetype" {
		t.Fatalf("fields = %q", request.Query.Get("fields"))
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@test.com:test-token"))
	if got := request.Header.Get("Authorization"); got != wantAuth {
		t.Fatalf("Authorization = %q, want %q", got, wantAuth)
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraSearchIssues::test_missing_jql
func TestBatch5Lane1JiraSearchIssuesValidation(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action": "jira_search_issues",
		"jql":    "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "jql is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraGetIssue::test_successful_get
func TestBatch5Lane1JiraGetIssueSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"key": "TEST-1",
			"fields": map[string]any{
				"summary": "Fix login bug",
				"description": map[string]any{
					"type":    "doc",
					"version": 1,
					"content": []any{
						map[string]any{
							"type":    "paragraph",
							"content": []any{map[string]any{"type": "text", "text": "Login fails"}},
						},
					},
				},
				"status":    map[string]any{"name": "In Progress"},
				"assignee":  map[string]any{"displayName": "John"},
				"reporter":  map[string]any{"displayName": "Jane"},
				"priority":  map[string]any{"name": "High"},
				"issuetype": map[string]any{"name": "Bug"},
				"project":   map[string]any{"name": "Test Project"},
				"labels":    []any{"backend"},
				"created":   "2024-01-01T00:00:00Z",
				"updated":   "2024-01-15T00:00:00Z",
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":    "jira_get_issue",
		"issue_key": "TEST-1",
	})
	if result["summary"] != "Fix login bug" || result["description"] != "Login fails" {
		t.Fatalf("result = %v", result)
	}
	if result["project"] != "Test Project" || result["reporter"] != "Jane" {
		t.Fatalf("result = %v", result)
	}
	labels := batch5Lane1SliceField(t, result, "labels")
	if len(labels) != 1 || labels[0] != "backend" {
		t.Fatalf("labels = %v", labels)
	}
	if request := recorder.last(t); request.Path != "/issue/TEST-1" || request.Method != http.MethodGet {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraGetIssue::test_missing_issue_key
func TestBatch5Lane1JiraGetIssueValidation(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":    "jira_get_issue",
		"issue_key": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "issue_key is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraCreateIssue::test_successful_create
func TestBatch5Lane1JiraCreateIssueSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusCreated, map[string]any{"key": "TEST-2", "id": "10002"})
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":      "jira_create_issue",
		"project_key": "TEST",
		"summary":     "New task",
		"description": "Details",
		"priority":    "High",
		"labels":      "backend, urgent",
	})
	if result["key"] != "TEST-2" || result["status"] != "created" {
		t.Fatalf("result = %v", result)
	}
	if result["url"] != "https://test.atlassian.net/browse/TEST-2" {
		t.Fatalf("url = %v", result["url"])
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost || request.Path != "/issue" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	fields := batch5Lane1MapField(t, request.Body, "fields")
	project := batch5Lane1MapField(t, fields, "project")
	if project["key"] != "TEST" || fields["summary"] != "New task" {
		t.Fatalf("fields = %v", fields)
	}
	issueType := batch5Lane1MapField(t, fields, "issuetype")
	if issueType["name"] != "Task" {
		t.Fatalf("issuetype = %v", issueType)
	}
	if fields["priority"].(map[string]any)["name"] != "High" {
		t.Fatalf("priority = %v", fields["priority"])
	}
	labels := batch5Lane1SliceField(t, fields, "labels")
	if len(labels) != 2 || labels[0] != "backend" || labels[1] != "urgent" {
		t.Fatalf("labels = %v", labels)
	}
	description := batch5Lane1MapField(t, fields, "description")
	content := batch5Lane1SliceField(t, description, "content")
	paragraph := content[0].(map[string]any)
	inline := batch5Lane1SliceField(t, paragraph, "content")
	if inline[0].(map[string]any)["text"] != "Details" {
		t.Fatalf("description = %v", description)
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraCreateIssue::test_missing_params
func TestBatch5Lane1JiraCreateIssueValidation(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":      "jira_create_issue",
		"project_key": "",
		"summary":     "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "project_key and summary are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraListProjects::test_successful_list
func TestBatch5Lane1JiraListProjectsSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"values": []any{
				map[string]any{"key": "TEST", "name": "Test Project", "id": "10000", "projectTypeKey": "software"},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":      "jira_list_projects",
		"max_results": 500,
		"query":       "Test",
	})
	if result["count"] != 1 {
		t.Fatalf("count = %v", result["count"])
	}
	projects := batch5Lane1SliceField(t, result, "projects")
	project := projects[0].(map[string]any)
	if project["key"] != "TEST" || project["project_type"] != "software" {
		t.Fatalf("project = %v", project)
	}
	request := recorder.last(t)
	if request.Path != "/project/search" {
		t.Fatalf("path = %q", request.Path)
	}
	if request.Query.Get("maxResults") != "100" {
		t.Fatalf("maxResults = %q, want clamp to 100", request.Query.Get("maxResults"))
	}
	if request.Query.Get("query") != "Test" {
		t.Fatalf("query = %q", request.Query.Get("query"))
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraGetProject::test_successful_get
func TestBatch5Lane1JiraGetProjectSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"key":            "TEST",
			"name":           "Test Project",
			"id":             "10000",
			"description":    "A test project",
			"lead":           map[string]any{"displayName": "Jane"},
			"projectTypeKey": "software",
			"issueTypes": []any{
				map[string]any{"name": "Bug", "subtask": false},
				map[string]any{"name": "Task", "subtask": false},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":      "jira_get_project",
		"project_key": "TEST",
	})
	if result["name"] != "Test Project" || result["lead"] != "Jane" {
		t.Fatalf("result = %v", result)
	}
	issueTypes := batch5Lane1SliceField(t, result, "issue_types")
	if len(issueTypes) != 2 || issueTypes[0].(map[string]any)["name"] != "Bug" {
		t.Fatalf("issue_types = %v", issueTypes)
	}
	if request := recorder.last(t); request.Query.Get("expand") != "description,lead,issueTypes" {
		t.Fatalf("expand = %q", request.Query.Get("expand"))
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraGetProject::test_missing_key
func TestBatch5Lane1JiraGetProjectValidation(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":      "jira_get_project",
		"project_key": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "project_key is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraAddComment::test_successful_add
func TestBatch5Lane1JiraAddCommentSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusCreated, map[string]any{
			"id":      "100",
			"author":  map[string]any{"displayName": "John"},
			"created": "2024-01-15T00:00:00Z",
		})
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":    "jira_add_comment",
		"issue_key": "TEST-1",
		"body":      "Great work!",
	})
	if result["status"] != "created" || result["author"] != "John" || result["id"] != "100" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost || request.Path != "/issue/TEST-1/comment" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	body := batch5Lane1MapField(t, request.Body, "body")
	if body["type"] != "doc" {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraAddComment::test_missing_params
func TestBatch5Lane1JiraAddCommentValidation(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":    "jira_add_comment",
		"issue_key": "",
		"body":      "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "issue_key and body are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream jira_tool defines jira_update_issue, but
// test_jira_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1JiraUpdateIssueSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":              "jira_update_issue",
		"issue_key":           "TEST-1",
		"summary":             "Renamed",
		"labels":              "one, two",
		"assignee_account_id": "acc-9",
	})
	if result["status"] != "updated" || result["key"] != "TEST-1" {
		t.Fatalf("result = %v", result)
	}
	if result["url"] != "https://test.atlassian.net/browse/TEST-1" {
		t.Fatalf("url = %v", result["url"])
	}
	request := recorder.last(t)
	if request.Method != http.MethodPut || request.Path != "/issue/TEST-1" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	fields := batch5Lane1MapField(t, request.Body, "fields")
	if fields["summary"] != "Renamed" {
		t.Fatalf("fields = %v", fields)
	}
	assignee := batch5Lane1MapField(t, fields, "assignee")
	if assignee["accountId"] != "acc-9" {
		t.Fatalf("assignee = %v", assignee)
	}
}

// Parity-exempt: upstream jira_tool defines jira_update_issue, but
// test_jira_tool.py has no unit test for its validation branch.
func TestBatch5Lane1JiraUpdateIssueValidation(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":    "jira_update_issue",
		"issue_key": "TEST-1",
	})
	if got := batch5Lane1ErrorText(t, result); got != "At least one field to update is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream jira_tool defines jira_list_transitions, but
// test_jira_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1JiraListTransitionsSuccess(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"transitions": []any{
				map[string]any{"id": "31", "name": "Done", "to": map[string]any{"name": "Done"}},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":    "jira_list_transitions",
		"issue_key": "TEST-1",
	})
	if result["count"] != 1 {
		t.Fatalf("count = %v", result["count"])
	}
	transitions := batch5Lane1SliceField(t, result, "transitions")
	transition := transitions[0].(map[string]any)
	if transition["id"] != "31" || transition["to_status"] != "Done" {
		t.Fatalf("transition = %v", transition)
	}
}

// Parity-exempt: upstream jira_tool defines jira_transition_issue, but
// test_jira_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1JiraTransitionIssueSuccess(t *testing.T) {
	registry, recorder := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":        "jira_transition_issue",
		"issue_key":     "TEST-1",
		"transition_id": "31",
		"comment":       "Moving to done",
	})
	if result["status"] != "transitioned" || result["key"] != "TEST-1" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	transition := batch5Lane1MapField(t, request.Body, "transition")
	if transition["id"] != "31" {
		t.Fatalf("transition = %v", transition)
	}
	update := batch5Lane1MapField(t, request.Body, "update")
	comments := batch5Lane1SliceField(t, update, "comment")
	add := batch5Lane1MapField(t, comments[0].(map[string]any), "add")
	if body := batch5Lane1MapField(t, add, "body"); body["type"] != "doc" {
		t.Fatalf("comment body = %v", body)
	}
}

// Parity-exempt: upstream jira_tool defines jira_transition_issue, but
// test_jira_tool.py has no unit test for its validation branch.
func TestBatch5Lane1JiraTransitionIssueValidation(t *testing.T) {
	registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action":        "jira_transition_issue",
		"issue_key":     "TEST-1",
		"transition_id": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "issue_key and transition_id are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream jira_tool._request has no Python unit tests for
// HTTP status branches; these assertions lock its 401/403/404/429 mapping.
func TestBatch5Lane1JiraHTTPErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, "Unauthorized. Check your Jira credentials."},
		{"forbidden", http.StatusForbidden, "Forbidden. Check your Jira permissions."},
		{"not_found", http.StatusNotFound, "Not found."},
		{"rate_limited", http.StatusTooManyRequests, "Rate limited. Try again shortly."},
		{"server_error", http.StatusInternalServerError, "Jira API error 500: boom"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, _ := batch5Lane1JiraRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte("boom"))
			})
			result := batch5Lane1Execute(t, registry, "jira", map[string]any{
				"action":    "jira_get_issue",
				"issue_key": "TEST-1",
			})
			if got := batch5Lane1ErrorText(t, result); got != testCase.want {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Parity: tools/tests/tools/test_jira_tool.py::TestJiraSearchIssues::test_missing_credentials, tools/tests/tools/test_jira_tool.py::TestJiraListProjects::test_missing_credentials
func TestBatch5Lane1JiraMissingCredentials(t *testing.T) {
	registry := NewAtlassianRegistry(
		WithBaseURL("jira_tool", "https://example.invalid"),
		WithGetenv(func(string) string { return "" }),
	)
	result := batch5Lane1Execute(t, registry, "jira", map[string]any{
		"action": "jira_search_issues",
		"jql":    "project = TEST",
	})
	errorText := batch5Lane1ErrorText(t, result)
	if !strings.Contains(errorText, "JIRA_DOMAIN") {
		t.Fatalf("error = %q", errorText)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "api-tokens") {
		t.Fatalf("help = %v", result["help"])
	}
}

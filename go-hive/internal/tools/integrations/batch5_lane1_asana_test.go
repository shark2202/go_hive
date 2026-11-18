package integrations

import (
	"net/http"
	"strings"
	"testing"
)

func batch5Lane1AsanaRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch5Lane1Recorder) {
	t.Helper()
	return batch5Lane1TestRegistry(t, "asana_tool", nil, handler, opts...)
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaListWorkspaces::test_successful_list
func TestBatch5Lane1AsanaListWorkspacesSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"data": []any{map[string]any{"gid": "ws-1", "name": "My Workspace"}},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action": "asana_list_workspaces",
	})
	workspaces := batch5Lane1SliceField(t, result, "workspaces")
	if len(workspaces) != 1 || workspaces[0].(map[string]any)["name"] != "My Workspace" {
		t.Fatalf("workspaces = %v", workspaces)
	}
	request := recorder.last(t)
	if request.Method != http.MethodGet || request.Path != "/workspaces" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaListProjects::test_successful_list
func TestBatch5Lane1AsanaListProjectsSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"data": []any{
				map[string]any{"gid": "proj-1", "name": "Website Redesign", "color": "blue", "archived": false},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":        "asana_list_projects",
		"workspace_gid": "ws-1",
		"limit":         500,
	})
	projects := batch5Lane1SliceField(t, result, "projects")
	if len(projects) != 1 || projects[0].(map[string]any)["name"] != "Website Redesign" {
		t.Fatalf("projects = %v", projects)
	}
	request := recorder.last(t)
	if request.Query.Get("workspace") != "ws-1" || request.Query.Get("limit") != "100" {
		t.Fatalf("query = %v", request.Query)
	}
	if request.Query.Get("opt_fields") != "name,color,archived,created_at" {
		t.Fatalf("opt_fields = %q", request.Query.Get("opt_fields"))
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaListProjects::test_missing_workspace
func TestBatch5Lane1AsanaListProjectsValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":        "asana_list_projects",
		"workspace_gid": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "workspace_gid is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaListTasks::test_successful_list
func TestBatch5Lane1AsanaListTasksSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"data": []any{
				map[string]any{
					"gid": "task-1", "name": "Design homepage", "completed": false,
					"due_on": "2024-06-15", "assignee": map[string]any{"name": "Alice"},
				},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":      "asana_list_tasks",
		"project_gid": "proj-1",
	})
	tasks := batch5Lane1SliceField(t, result, "tasks")
	if len(tasks) != 1 {
		t.Fatalf("tasks = %v", tasks)
	}
	task := tasks[0].(map[string]any)
	if task["name"] != "Design homepage" || task["assignee_name"] != "Alice" {
		t.Fatalf("task = %v", task)
	}
	request := recorder.last(t)
	if request.Path != "/tasks" || request.Query.Get("project") != "proj-1" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	if request.Query.Get("opt_fields") != "name,completed,due_on,assignee.name" {
		t.Fatalf("opt_fields = %q", request.Query.Get("opt_fields"))
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaListTasks::test_missing_params
func TestBatch5Lane1AsanaListTasksValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action": "asana_list_tasks",
	})
	if got := batch5Lane1ErrorText(t, result); got != "Either project_gid or workspace_gid is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaGetTask::test_successful_get
func TestBatch5Lane1AsanaGetTaskSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"gid": "task-1", "name": "Design homepage", "notes": "Create the new homepage design",
				"completed": false, "due_on": "2024-06-15", "assignee": map[string]any{"name": "Alice"},
				"projects":    []any{map[string]any{"name": "Website Redesign"}},
				"tags":        []any{map[string]any{"name": "urgent"}},
				"created_at":  "2024-01-01T00:00:00Z",
				"modified_at": "2024-06-01T00:00:00Z",
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":   "asana_get_task",
		"task_gid": "task-1",
	})
	if result["name"] != "Design homepage" {
		t.Fatalf("result = %v", result)
	}
	projects := result["projects"].([]string)
	tags := result["tags"].([]string)
	if len(projects) != 1 || projects[0] != "Website Redesign" || len(tags) != 1 || tags[0] != "urgent" {
		t.Fatalf("projects = %v, tags = %v", projects, tags)
	}
	if request := recorder.last(t); request.Path != "/tasks/task-1" {
		t.Fatalf("path = %q", request.Path)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaGetTask::test_missing_gid
func TestBatch5Lane1AsanaGetTaskValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":   "asana_get_task",
		"task_gid": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "task_gid is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaCreateTask::test_successful_create
func TestBatch5Lane1AsanaCreateTaskSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{"gid": "task-new", "name": "New Task"},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":        "asana_create_task",
		"workspace_gid": "ws-1",
		"name":          "New Task",
		"project_gid":   "proj-1",
		"assignee":      "me",
		"due_on":        "2024-07-01",
	})
	if result["status"] != "created" || result["gid"] != "task-new" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost || request.Path != "/tasks" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	body := batch5Lane1MapField(t, request.Body, "data")
	if body["workspace"] != "ws-1" || body["name"] != "New Task" || body["due_on"] != "2024-07-01" {
		t.Fatalf("body = %v", body)
	}
	projects := batch5Lane1SliceField(t, body, "projects")
	if len(projects) != 1 || projects[0] != "proj-1" {
		t.Fatalf("projects = %v", projects)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaCreateTask::test_missing_name
func TestBatch5Lane1AsanaCreateTaskValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":        "asana_create_task",
		"workspace_gid": "ws-1",
		"name":          "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "workspace_gid and name are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaSearchTasks::test_successful_search
func TestBatch5Lane1AsanaSearchTasksSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"data": []any{
				map[string]any{
					"gid": "task-1", "name": "Design homepage",
					"completed": false, "due_on": "2024-06-15",
				},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":        "asana_search_tasks",
		"workspace_gid": "ws-1",
		"query":         "design",
		"limit":         500,
	})
	if result["query"] != "design" {
		t.Fatalf("query = %v", result["query"])
	}
	tasks := batch5Lane1SliceField(t, result, "tasks")
	if len(tasks) != 1 {
		t.Fatalf("tasks = %v", tasks)
	}
	request := recorder.last(t)
	if request.Path != "/workspaces/ws-1/tasks/search" {
		t.Fatalf("path = %q", request.Path)
	}
	if request.Query.Get("text") != "design" || request.Query.Get("limit") != "100" {
		t.Fatalf("query = %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaSearchTasks::test_missing_params
func TestBatch5Lane1AsanaSearchTasksValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":        "asana_search_tasks",
		"workspace_gid": "",
		"query":         "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "workspace_gid and query are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream asana_tool defines asana_update_task, but
// test_asana_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1AsanaUpdateTaskSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"gid": "task-1", "name": "Renamed", "completed": false},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":    "asana_update_task",
		"task_gid":  "task-1",
		"name":      "Renamed",
		"completed": false,
	})
	if result["status"] != "updated" || result["gid"] != "task-1" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPut || request.Path != "/tasks/task-1" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	body := batch5Lane1MapField(t, request.Body, "data")
	if body["name"] != "Renamed" {
		t.Fatalf("body = %v", body)
	}
	if completed, ok := body["completed"]; !ok || completed != false {
		t.Fatalf("completed = %v (present %v), want explicit false", completed, ok)
	}
}

// Parity-exempt: upstream asana_tool defines asana_update_task, but
// test_asana_tool.py has no unit test for its validation branch.
func TestBatch5Lane1AsanaUpdateTaskValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":   "asana_update_task",
		"task_gid": "task-1",
	})
	if got := batch5Lane1ErrorText(t, result); got != "At least one field to update is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream asana_tool defines asana_add_comment, but
// test_asana_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1AsanaAddCommentSuccess(t *testing.T) {
	longText := strings.Repeat("x", 600)
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{
				"gid": "story-1", "text": longText, "created_at": "2024-06-01T00:00:00Z",
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":   "asana_add_comment",
		"task_gid": "task-1",
		"text":     "Looks good",
	})
	if result["status"] != "created" || result["gid"] != "story-1" {
		t.Fatalf("result = %v", result)
	}
	if got := result["text"].(string); len(got) != 500 {
		t.Fatalf("text length = %d, want 500", len(got))
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost || request.Path != "/tasks/task-1/stories" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	body := batch5Lane1MapField(t, request.Body, "data")
	if body["text"] != "Looks good" {
		t.Fatalf("body = %v", body)
	}
}

// Parity-exempt: upstream asana_tool defines asana_add_comment, but
// test_asana_tool.py has no unit test for its validation branch.
func TestBatch5Lane1AsanaAddCommentValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":   "asana_add_comment",
		"task_gid": "task-1",
		"text":     "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "task_gid and text are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream asana_tool defines asana_create_subtask, but
// test_asana_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1AsanaCreateSubtaskSuccess(t *testing.T) {
	registry, recorder := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{"gid": "subtask-1", "name": "Write tests"},
		})
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":          "asana_create_subtask",
		"parent_task_gid": "task-1",
		"name":            "Write tests",
		"notes":           "Cover the parser",
		"assignee":        "me",
		"due_on":          "2024-08-01",
	})
	if result["status"] != "created" || result["gid"] != "subtask-1" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost || request.Path != "/tasks/task-1/subtasks" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	body := batch5Lane1MapField(t, request.Body, "data")
	if body["name"] != "Write tests" || body["notes"] != "Cover the parser" || body["assignee"] != "me" {
		t.Fatalf("body = %v", body)
	}
}

// Parity-exempt: upstream asana_tool defines asana_create_subtask, but
// test_asana_tool.py has no unit test for its validation branch.
func TestBatch5Lane1AsanaCreateSubtaskValidation(t *testing.T) {
	registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":          "asana_create_subtask",
		"parent_task_gid": "task-1",
		"name":            "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "parent_task_gid and name are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream asana_tool._get has no Python unit tests for HTTP
// status branches; these assertions lock its 401/403/404/429 mapping.
func TestBatch5Lane1AsanaHTTPErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, "Unauthorized. Check your ASANA_ACCESS_TOKEN."},
		{"forbidden", http.StatusForbidden, "Forbidden: boom"},
		{"not_found", http.StatusNotFound, "Not found"},
		{"rate_limited", http.StatusTooManyRequests, "Asana API error 429: boom"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, _ := batch5Lane1AsanaRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte("boom"))
			})
			result := batch5Lane1Execute(t, registry, "asana", map[string]any{
				"action":   "asana_get_task",
				"task_gid": "task-1",
			})
			if got := batch5Lane1ErrorText(t, result); got != testCase.want {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Parity: tools/tests/tools/test_asana_tool.py::TestAsanaListWorkspaces::test_missing_token
func TestBatch5Lane1AsanaMissingToken(t *testing.T) {
	registry := newBatch5Lane1Registry(
		WithBaseURL("asana_tool", "https://example.invalid"),
		WithGetenv(func(string) string { return "" }),
	)
	result := batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action": "asana_list_workspaces",
	})
	if got := batch5Lane1ErrorText(t, result); got != "ASANA_ACCESS_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "my-apps") {
		t.Fatalf("help = %v", result["help"])
	}
	// Upstream resolves the token before validating required arguments.
	result = batch5Lane1Execute(t, registry, "asana", map[string]any{
		"action":        "asana_list_projects",
		"workspace_gid": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "ASANA_ACCESS_TOKEN not set" {
		t.Fatalf("missing-argument precedence error = %q", got)
	}
}

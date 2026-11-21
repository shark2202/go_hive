package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type batch7Lane1Request struct {
	Method string
	Path   string
	URI    string
	Query  url.Values
	Header http.Header
	Body   map[string]any
	Raw    string
}

type batch7Lane1Recorder struct {
	mu       sync.Mutex
	requests []batch7Lane1Request
}

func (rec *batch7Lane1Recorder) wrap(
	next func(http.ResponseWriter, *http.Request, map[string]any),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw := ""
		if r.Body != nil {
			content, _ := io.ReadAll(r.Body)
			raw = string(content)
			if len(content) > 0 {
				_ = json.Unmarshal(content, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch7Lane1Request{
			Method: r.Method,
			Path:   r.URL.Path,
			URI:    r.RequestURI,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
			Raw:    raw,
		})
		rec.mu.Unlock()
		next(w, r, body)
	}
}

func (rec *batch7Lane1Recorder) last(t *testing.T) batch7Lane1Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch7Lane1Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch7Lane1TestRegistry builds a lane-1 registry pointed at one fake
// upstream, with per-module env values for multi-part credentials.
func batch7Lane1TestRegistry(
	t *testing.T,
	module string,
	env map[string]string,
	handler func(http.ResponseWriter, *http.Request, map[string]any),
) (*Registry, *batch7Lane1Recorder) {
	t.Helper()
	recorder := &batch7Lane1Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	opts := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
		WithGetenv(func(name string) string { return env[name] }),
	}
	return newBatch7Lane1Registry(opts...), recorder
}

func batch7Lane1Execute(
	t *testing.T,
	registry *Registry,
	tool string,
	input map[string]any,
) map[string]any {
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

func batch7Lane1WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch7Lane1ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch7Lane1Field(t *testing.T, source map[string]any, name string) any {
	t.Helper()
	value, present := source[name]
	if !present {
		t.Fatalf("field %q missing (%v)", name, source)
	}
	return value
}

func batch7Lane1FieldText(t *testing.T, source map[string]any, name string) string {
	t.Helper()
	return fmt.Sprint(batch7Lane1Field(t, source, name))
}

func batch7Lane1FieldString(t *testing.T, source map[string]any, name string) string {
	t.Helper()
	value := batch7Lane1Field(t, source, name)
	text, ok := value.(string)
	if !ok {
		t.Fatalf("field %q = %T, want string (%v)", name, value, source)
	}
	return text
}

func batch7Lane1FieldMap(t *testing.T, source map[string]any, name string) map[string]any {
	t.Helper()
	value := batch7Lane1Field(t, source, name)
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("field %q = %T, want map (%v)", name, value, source)
	}
	return object
}

func batch7Lane1FieldSlice(t *testing.T, source map[string]any, name string) []any {
	t.Helper()
	value := batch7Lane1Field(t, source, name)
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice {
		t.Fatalf("field %q = %T, want array (%v)", name, value, source)
	}
	out := make([]any, 0, rv.Len())
	for index := 0; index < rv.Len(); index++ {
		out = append(out, rv.Index(index).Interface())
	}
	return out
}

func batch7Lane1ContainsAny(text string, wants []string) bool {
	for _, want := range wants {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

// batch7Lane1StatusHandler answers every request with one status and body.
func batch7Lane1StatusHandler(status int, body any) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch7Lane1WriteJSON(w, status, body)
	}
}

// batch7Lane1InputWith merges an action selector into one action input.
func batch7Lane1InputWith(action string, input map[string]any) map[string]any {
	out := map[string]any{"action": action}
	for name, value := range input {
		out[name] = value
	}
	return out
}

// batch7Lane1AssertStatusTable runs every supplied action against each failing
// status and checks that the upstream-shaped error message surfaces.
func batch7Lane1AssertStatusTable(
	t *testing.T,
	module, tool string,
	env map[string]string,
	statuses []int,
	wants map[int][]string,
	actions []struct {
		name  string
		input map[string]any
	},
) {
	t.Helper()
	for _, status := range statuses {
		status := status
		for _, action := range actions {
			action := action
			t.Run(fmt.Sprintf("%s/%d", action.name, status), func(t *testing.T) {
				registry, _ := batch7Lane1TestRegistry(
					t, module, env,
					batch7Lane1StatusHandler(status, map[string]any{"message": "provider failure"}),
				)
				result := batch7Lane1Execute(t, registry, tool, batch7Lane1InputWith(action.name, action.input))
				text := batch7Lane1ErrorText(t, result)
				if !batch7Lane1ContainsAny(text, wants[status]) {
					t.Fatalf("%s status %d error = %q, want one of %v", action.name, status, text, wants[status])
				}
			})
		}
	}
}

// ------------------------------------------------------------------ GitLab ---

// Parity: tools/tests/tools/test_gitlab_tool.py::TestGitlabListProjects::test_successful_list
func TestBatch7Lane1GitLabListProjects(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, []any{
				map[string]any{
					"id":                  1,
					"name":                "demo",
					"path_with_namespace": "group/demo",
					"description":         "Demo project",
					"visibility":          "private",
					"default_branch":      "main",
					"web_url":             "https://gitlab.com/group/demo",
					"star_count":          3,
					"last_activity_at":    "2024-01-02T03:04:05Z",
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action": "gitlab_list_projects",
		"search": "demo",
		"owned":  true,
	})
	if got := batch7Lane1FieldText(t, result, "count"); got != "1" {
		t.Fatalf("count = %s, want 1", got)
	}
	projects := batch7Lane1FieldSlice(t, result, "projects")
	project := projects[0].(map[string]any)
	if got := batch7Lane1FieldText(t, project, "id"); got != "1" {
		t.Fatalf("id = %s, want 1", got)
	}
	if got := batch7Lane1FieldString(t, project, "path_with_namespace"); got != "group/demo" {
		t.Fatalf("path_with_namespace = %q", got)
	}
	request := recorder.last(t)
	if request.Path != "/api/v4/projects" {
		t.Fatalf("path = %q", request.Path)
	}
	if got := request.Header.Get("PRIVATE-TOKEN"); got != "test-token" {
		t.Fatalf("PRIVATE-TOKEN = %q", got)
	}
	if got := request.Query.Get("search"); got != "demo" {
		t.Fatalf("search = %q", got)
	}
	if got := request.Query.Get("owned"); got != "true" {
		t.Fatalf("owned = %q", got)
	}
	if got := request.Query.Get("membership"); got != "true" {
		t.Fatalf("membership = %q", got)
	}
}

// Parity: tools/tests/tools/test_gitlab_tool.py::TestGitlabGetProject::test_successful_get
func TestBatch7Lane1GitLabGetProject(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"id":                  42,
				"name":                "demo",
				"path_with_namespace": "group/demo",
				"description":         "Demo project",
				"visibility":          "private",
				"default_branch":      "main",
				"web_url":             "https://gitlab.com/group/demo",
				"star_count":          5,
				"forks_count":         2,
				"open_issues_count":   7,
				"statistics":          map[string]any{"commit_count": 17},
				"created_at":          "2023-05-01T00:00:00Z",
				"last_activity_at":    "2024-01-02T03:04:05Z",
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":     "gitlab_get_project",
		"project_id": "group%2Fdemo",
	})
	if got := batch7Lane1FieldText(t, result, "commit_count"); got != "17" {
		t.Fatalf("commit_count = %s, want 17", got)
	}
	if got := batch7Lane1FieldText(t, result, "open_issues_count"); got != "7" {
		t.Fatalf("open_issues_count = %s, want 7", got)
	}
	request := recorder.last(t)
	if request.URI != "/api/v4/projects/group%2Fdemo?statistics=true" {
		t.Fatalf("request URI = %q", request.URI)
	}
}

// Parity: tools/tests/tools/test_gitlab_tool.py::TestGitlabListIssues::test_successful_list
func TestBatch7Lane1GitLabListIssues(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, []any{
				map[string]any{
					"iid":        7,
					"title":      "Bug",
					"state":      "opened",
					"labels":     []any{"bug", "p1"},
					"assignees":  []any{map[string]any{"username": "alice"}},
					"author":     map[string]any{"username": "bob"},
					"created_at": "2024-01-01T00:00:00Z",
					"updated_at": "2024-01-02T00:00:00Z",
					"web_url":    "https://gitlab.com/group/demo/-/issues/7",
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":     "gitlab_list_issues",
		"project_id": "42",
		"labels":     "bug",
	})
	issues := batch7Lane1FieldSlice(t, result, "issues")
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	issue := issues[0].(map[string]any)
	if got := batch7Lane1FieldText(t, issue, "iid"); got != "7" {
		t.Fatalf("iid = %s", got)
	}
	if labels := batch7Lane1FieldSlice(t, issue, "labels"); len(labels) != 2 {
		t.Fatalf("labels = %v", labels)
	}
	assignees := batch7Lane1FieldSlice(t, issue, "assignees")
	if assignees[0] != "alice" {
		t.Fatalf("assignees = %v", assignees)
	}
	if got := batch7Lane1FieldString(t, issue, "author"); got != "bob" {
		t.Fatalf("author = %q", got)
	}
	request := recorder.last(t)
	if request.Path != "/api/v4/projects/42/issues" {
		t.Fatalf("path = %q", request.Path)
	}
	if got := request.Query.Get("state"); got != "opened" {
		t.Fatalf("state = %q", got)
	}
	if got := request.Query.Get("labels"); got != "bug" {
		t.Fatalf("labels = %q", got)
	}
}

// Parity: tools/tests/tools/test_gitlab_tool.py::TestGitlabGetIssue::test_successful_get
func TestBatch7Lane1GitLabGetIssue(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"iid":         7,
				"title":       "Bug",
				"description": strings.Repeat("d", 1200),
				"state":       "opened",
				"labels":      []any{"bug"},
				"assignees":   []any{map[string]any{"username": "alice"}},
				"author":      map[string]any{"username": "bob"},
				"milestone":   map[string]any{"title": "v1"},
				"due_date":    "2024-02-01",
				"web_url":     "https://gitlab.com/group/demo/-/issues/7",
				"created_at":  "2024-01-01T00:00:00Z",
				"updated_at":  "2024-01-02T00:00:00Z",
				"closed_at":   nil,
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":     "gitlab_get_issue",
		"project_id": "42",
		"issue_iid":  7,
	})
	if got := batch7Lane1FieldString(t, result, "milestone"); got != "v1" {
		t.Fatalf("milestone = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "due_date"); got != "2024-02-01" {
		t.Fatalf("due_date = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "description"); len(got) != 1000 {
		t.Fatalf("description length = %d, want 1000", len(got))
	}
	if result["closed_at"] != nil {
		t.Fatalf("closed_at = %v, want nil", result["closed_at"])
	}
}

// Parity: tools/tests/tools/test_gitlab_tool.py::TestGitlabCreateIssue::test_successful_create
func TestBatch7Lane1GitLabCreateIssue(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusCreated, map[string]any{
				"iid":     9,
				"title":   "New issue",
				"web_url": "https://gitlab.com/group/demo/-/issues/9",
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":       "gitlab_create_issue",
		"project_id":   "42",
		"title":        "New issue",
		"labels":       "bug",
		"assignee_ids": "3, 4",
	})
	if got := batch7Lane1FieldText(t, result, "iid"); got != "9" {
		t.Fatalf("iid = %s", got)
	}
	if got := batch7Lane1FieldString(t, result, "status"); got != "created" {
		t.Fatalf("status = %q", got)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost {
		t.Fatalf("method = %q", request.Method)
	}
	if got := batch7Lane1FieldString(t, request.Body, "title"); got != "New issue" {
		t.Fatalf("body.title = %q", got)
	}
	ids := batch7Lane1FieldSlice(t, request.Body, "assignee_ids")
	if fmt.Sprint(ids[0]) != "3" || fmt.Sprint(ids[1]) != "4" {
		t.Fatalf("assignee_ids = %v", ids)
	}
}

// Parity: tools/tests/tools/test_gitlab_tool.py::TestGitlabListMergeRequests::test_successful_list
func TestBatch7Lane1GitLabListMergeRequests(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, []any{
				map[string]any{
					"iid":           2,
					"title":         "Add feature",
					"state":         "opened",
					"source_branch": "feature",
					"target_branch": "main",
					"author":        map[string]any{"username": "bob"},
					"web_url":       "https://gitlab.com/group/demo/-/merge_requests/2",
					"created_at":    "2024-01-01T00:00:00Z",
					"updated_at":    "2024-01-02T00:00:00Z",
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":     "gitlab_list_merge_requests",
		"project_id": "42",
		"state":      "all",
	})
	if got := batch7Lane1FieldText(t, result, "count"); got != "1" {
		t.Fatalf("count = %s", got)
	}
	requests := batch7Lane1FieldSlice(t, result, "merge_requests")
	request := requests[0].(map[string]any)
	if got := batch7Lane1FieldString(t, request, "source_branch"); got != "feature" {
		t.Fatalf("source_branch = %q", got)
	}
}

// Parity-exempt: upstream gitlab_tool has no update_issue test.
func TestBatch7Lane1GitLabUpdateIssue(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"iid":     7,
				"title":   "Closed bug",
				"state":   "closed",
				"web_url": "https://gitlab.com/group/demo/-/issues/7",
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":      "gitlab_update_issue",
		"project_id":  "42",
		"issue_iid":   7,
		"state_event": "close",
	})
	if got := batch7Lane1FieldString(t, result, "status"); got != "updated" {
		t.Fatalf("status = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "state"); got != "closed" {
		t.Fatalf("state = %q", got)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPut {
		t.Fatalf("method = %q", request.Method)
	}
	if got := batch7Lane1FieldString(t, request.Body, "state_event"); got != "close" {
		t.Fatalf("body.state_event = %q", got)
	}
}

// Parity-exempt: upstream gitlab_tool has no get_merge_request test.
func TestBatch7Lane1GitLabGetMergeRequest(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"iid":           2,
				"title":         "Add feature",
				"description":   "Body",
				"state":         "opened",
				"source_branch": "feature",
				"target_branch": "main",
				"author":        map[string]any{"username": "bob"},
				"reviewers":     []any{map[string]any{"username": "carol"}},
				"merge_status":  "can_be_merged",
				"has_conflicts": false,
				"changes_count": "3",
				"web_url":       "https://gitlab.com/group/demo/-/merge_requests/2",
				"created_at":    "2024-01-01T00:00:00Z",
				"updated_at":    "2024-01-02T00:00:00Z",
				"merged_at":     nil,
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":            "gitlab_get_merge_request",
		"project_id":        "42",
		"merge_request_iid": 2,
	})
	if got := batch7Lane1FieldString(t, result, "merge_status"); got != "can_be_merged" {
		t.Fatalf("merge_status = %q", got)
	}
	reviewers := batch7Lane1FieldSlice(t, result, "reviewers")
	if reviewers[0] != "carol" {
		t.Fatalf("reviewers = %v", reviewers)
	}
	if got := batch7Lane1FieldText(t, result, "changes_count"); got != "3" {
		t.Fatalf("changes_count = %s", got)
	}
}

// Parity-exempt: upstream gitlab_tool has no merge-request note test.
func TestBatch7Lane1GitLabCreateMergeRequestNote(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "gitlab_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusCreated, map[string]any{
				"id":         55,
				"body":       "LGTM",
				"author":     map[string]any{"username": "alice"},
				"created_at": "2024-01-03T00:00:00Z",
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action":            "gitlab_create_merge_request_note",
		"project_id":        "42",
		"merge_request_iid": 2,
		"body":              "LGTM",
	})
	if got := batch7Lane1FieldText(t, result, "id"); got != "55" {
		t.Fatalf("id = %s", got)
	}
	if got := batch7Lane1FieldString(t, result, "author"); got != "alice" {
		t.Fatalf("author = %q", got)
	}
	request := recorder.last(t)
	if request.Path != "/api/v4/projects/42/merge_requests/2/notes" {
		t.Fatalf("path = %q", request.Path)
	}
	if got := batch7Lane1FieldString(t, request.Body, "body"); got != "LGTM" {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_gitlab_tool.py::TestGitlabListProjects::test_missing_credentials
func TestBatch7Lane1GitLabMissingCredentials(t *testing.T) {
	registry := newBatch7Lane1Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane1Execute(t, registry, "gitlab", map[string]any{
		"action": "gitlab_list_projects",
	})
	if got := batch7Lane1ErrorText(t, result); got != "GITLAB_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream gitlab_tool has no HTTP status-mapping test; the
// 401/403/404/429 payloads mirror _get/_post/_put line by line.
func TestBatch7Lane1GitLabStatusErrors(t *testing.T) {
	batch7Lane1AssertStatusTable(t, "gitlab_tool", "gitlab", nil,
		[]int{401, 403, 404, 429},
		map[int][]string{
			401: {"Unauthorized. Check your GitLab token."},
			403: {"Forbidden. Insufficient permissions."},
			404: {"Not found.", "404"},
			429: {"Rate limited. Try again shortly.", "429"},
		},
		[]struct {
			name  string
			input map[string]any
		}{
			{"gitlab_list_projects", map[string]any{}},
			{"gitlab_get_project", map[string]any{"project_id": "42"}},
			{"gitlab_list_issues", map[string]any{"project_id": "42"}},
			{"gitlab_get_issue", map[string]any{"project_id": "42", "issue_iid": 1}},
			{"gitlab_create_issue", map[string]any{"project_id": "42", "title": "t"}},
			{"gitlab_list_merge_requests", map[string]any{"project_id": "42"}},
			{"gitlab_update_issue", map[string]any{"project_id": "42", "issue_iid": 1, "title": "t"}},
			{"gitlab_get_merge_request", map[string]any{"project_id": "42", "merge_request_iid": 2}},
			{"gitlab_create_merge_request_note", map[string]any{"project_id": "42", "merge_request_iid": 2, "body": "hi"}},
		},
	)
}

// ----------------------------------------------------------------- AWS S3 ---

// batch7Lane1S3Env is the credential bundle every S3 test uses.
func batch7Lane1S3Env() map[string]string {
	return map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIDTEST",
		"AWS_SECRET_ACCESS_KEY": "secret",
		"AWS_REGION":            "us-east-1",
	}
}

// Parity: tools/tests/tools/test_aws_s3_tool.py::TestS3ListBuckets::test_successful_list
func TestBatch7Lane1S3ListBuckets(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Owner><ID>owner</ID></Owner>
  <Buckets>
    <Bucket><Name>alpha</Name><CreationDate>2024-01-01T00:00:00.000Z</CreationDate></Bucket>
    <Bucket><Name>beta</Name><CreationDate>2024-02-01T00:00:00.000Z</CreationDate></Bucket>
  </Buckets>
</ListAllMyBucketsResult>`)
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{"action": "s3_list_buckets"})
	if got := batch7Lane1FieldText(t, result, "count"); got != "2" {
		t.Fatalf("count = %s, want 2", got)
	}
	buckets := batch7Lane1FieldSlice(t, result, "buckets")
	first := buckets[0].(map[string]any)
	if got := batch7Lane1FieldString(t, first, "name"); got != "alpha" {
		t.Fatalf("name = %q", got)
	}
	request := recorder.last(t)
	if request.Path != "/" {
		t.Fatalf("path = %q", request.Path)
	}
	auth := request.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/") {
		t.Fatalf("Authorization = %q", auth)
	}
	if got := request.Header.Get("x-amz-date"); got == "" {
		t.Fatal("x-amz-date missing")
	}
}

// Parity: tools/tests/tools/test_aws_s3_tool.py::TestS3ListObjects::test_successful_list
func TestBatch7Lane1S3ListObjects(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>reports</Name>
  <IsTruncated>true</IsTruncated>
  <NextContinuationToken>tok-2</NextContinuationToken>
  <Contents><Key>photos/a.jpg</Key><LastModified>2024-02-01T10:00:00.000Z</LastModified><Size>2048</Size></Contents>
  <Contents><Key>photos/b.jpg</Key><LastModified>2024-02-02T10:00:00.000Z</LastModified><Size>42</Size></Contents>
  <CommonPrefixes><Prefix>photos/2023/</Prefix></CommonPrefixes>
</ListBucketResult>`)
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{
		"action":   "s3_list_objects",
		"bucket":   "reports",
		"prefix":   "photos/",
		"max_keys": 50,
	})
	if got := batch7Lane1FieldText(t, result, "count"); got != "2" {
		t.Fatalf("count = %s, want 2", got)
	}
	objects := batch7Lane1FieldSlice(t, result, "objects")
	first := objects[0].(map[string]any)
	if got := batch7Lane1FieldText(t, first, "size"); got != "2048" {
		t.Fatalf("size = %s", got)
	}
	prefixes := batch7Lane1FieldSlice(t, result, "common_prefixes")
	if prefixes[0] != "photos/2023/" {
		t.Fatalf("common_prefixes = %v", prefixes)
	}
	if got := batch7Lane1FieldString(t, result, "next_continuation_token"); got != "tok-2" {
		t.Fatalf("next_continuation_token = %q", got)
	}
	request := recorder.last(t)
	if got := request.Query.Get("list-type"); got != "2" {
		t.Fatalf("list-type = %q", got)
	}
	if got := request.Query.Get("max-keys"); got != "50" {
		t.Fatalf("max-keys = %q", got)
	}
	if got := request.Query.Get("delimiter"); got != "/" {
		t.Fatalf("delimiter = %q", got)
	}
}

// Parity: tools/tests/tools/test_aws_s3_tool.py::TestS3GetObject::test_successful_get_text
func TestBatch7Lane1S3GetObject(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "11")
			w.Header().Set("ETag", `"etag-1"`)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "hello world")
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{
		"action":    "s3_get_object",
		"bucket":    "reports",
		"key":       "notes/a.txt",
		"max_bytes": 1000,
	})
	if got := batch7Lane1FieldString(t, result, "content"); got != "hello world" {
		t.Fatalf("content = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "etag"); got != `"etag-1"` {
		t.Fatalf("etag = %q", got)
	}
	request := recorder.last(t)
	if request.Path != "/notes/a.txt" {
		t.Fatalf("path = %q", request.Path)
	}
	if got := request.Header.Get("Range"); got != "bytes=0-999" {
		t.Fatalf("Range = %q", got)
	}
}

// Parity: tools/tests/tools/test_aws_s3_tool.py::TestS3PutObject::test_successful_put
func TestBatch7Lane1S3PutObject(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("ETag", `"etag-9"`)
			w.WriteHeader(http.StatusOK)
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{
		"action":  "s3_put_object",
		"bucket":  "reports",
		"key":     "notes.txt",
		"content": "hello world",
	})
	if got := batch7Lane1FieldString(t, result, "result"); got != "uploaded" {
		t.Fatalf("result = %q", got)
	}
	if got := batch7Lane1FieldText(t, result, "size"); got != "11" {
		t.Fatalf("size = %s", got)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPut {
		t.Fatalf("method = %q", request.Method)
	}
	if request.Raw != "hello world" {
		t.Fatalf("body = %q", request.Raw)
	}
	if got := request.Header.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q", got)
	}
}

// Parity: tools/tests/tools/test_aws_s3_tool.py::TestS3DeleteObject::test_successful_delete
func TestBatch7Lane1S3DeleteObject(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusNoContent)
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{
		"action": "s3_delete_object",
		"bucket": "reports",
		"key":    "notes.txt",
	})
	if got := batch7Lane1FieldString(t, result, "result"); got != "deleted" {
		t.Fatalf("result = %q", got)
	}
	request := recorder.last(t)
	if request.Method != http.MethodDelete {
		t.Fatalf("method = %q", request.Method)
	}
}

// Parity-exempt: upstream aws_s3_tool has no copy_object test.
func TestBatch7Lane1S3CopyObject(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"etag-copy"</ETag></CopyObjectResult>`)
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{
		"action":        "s3_copy_object",
		"source_bucket": "reports",
		"source_key":    "a.txt",
		"dest_bucket":   "archive",
		"dest_key":      "b.txt",
	})
	if got := batch7Lane1FieldString(t, result, "result"); got != "copied" {
		t.Fatalf("result = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "source"); got != "reports/a.txt" {
		t.Fatalf("source = %q", got)
	}
	request := recorder.last(t)
	if got := request.Header.Get("x-amz-copy-source"); got != "/reports/a.txt" {
		t.Fatalf("x-amz-copy-source = %q", got)
	}
	if request.Path != "/b.txt" {
		t.Fatalf("path = %q", request.Path)
	}
}

// Parity-exempt: upstream aws_s3_tool has no get_object_metadata test.
func TestBatch7Lane1S3GetObjectMetadata(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "12")
			w.Header().Set("x-amz-storage-class", "STANDARD_IA")
			w.Header().Set("x-amz-meta-owner", "ops")
			w.WriteHeader(http.StatusOK)
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{
		"action": "s3_get_object_metadata",
		"bucket": "reports",
		"key":    "data.json",
	})
	if got := batch7Lane1FieldString(t, result, "storage_class"); got != "STANDARD_IA" {
		t.Fatalf("storage_class = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "meta_owner"); got != "ops" {
		t.Fatalf("meta_owner = %q", got)
	}
	request := recorder.last(t)
	if request.Method != http.MethodHead {
		t.Fatalf("method = %q", request.Method)
	}
}

// Parity-exempt: upstream aws_s3_tool has no presigned-URL test; this action
// only signs a URL (no HTTP call), so there is no upstream status to fake.
func TestBatch7Lane1S3GeneratePresignedURL(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "aws_s3_tool", batch7Lane1S3Env(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{})
		},
	)
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{
		"action":     "s3_generate_presigned_url",
		"bucket":     "reports",
		"key":        "notes.txt",
		"expires_in": 900,
	})
	link := batch7Lane1FieldString(t, result, "url")
	for _, want := range []string{
		"/notes.txt?",
		"X-Amz-Algorithm=AWS4-HMAC-SHA256",
		"X-Amz-Credential=AKIDTEST%2F",
		"X-Amz-Expires=900",
		"X-Amz-Signature=",
	} {
		if !strings.Contains(link, want) {
			t.Fatalf("url = %q, missing %q", link, want)
		}
	}
	if got := batch7Lane1FieldText(t, result, "expires_in"); got != "900" {
		t.Fatalf("expires_in = %s", got)
	}
	if got := recorder.count(); got != 0 {
		t.Fatalf("presigned action made %d requests, want 0", got)
	}
}

// Parity: tools/tests/tools/test_aws_s3_tool.py::TestS3ListBuckets::test_missing_credentials
func TestBatch7Lane1S3MissingCredentials(t *testing.T) {
	registry := newBatch7Lane1Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane1Execute(t, registry, "aws_s3", map[string]any{"action": "s3_list_buckets"})
	want := "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required"
	if got := batch7Lane1ErrorText(t, result); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// Parity-exempt: upstream aws_s3_tool has no HTTP status-mapping test.
// s3_generate_presigned_url is absent by design: it never calls the API.
func TestBatch7Lane1S3StatusErrors(t *testing.T) {
	batch7Lane1AssertStatusTable(t, "aws_s3_tool", "aws_s3", batch7Lane1S3Env(),
		[]int{401, 403, 404, 429},
		map[int][]string{
			401: {"401"},
			403: {"403"},
			404: {"404", "Object not found"},
			429: {"429"},
		},
		[]struct {
			name  string
			input map[string]any
		}{
			{"s3_list_buckets", map[string]any{}},
			{"s3_list_objects", map[string]any{"bucket": "b"}},
			{"s3_get_object", map[string]any{"bucket": "b", "key": "k"}},
			{"s3_put_object", map[string]any{"bucket": "b", "key": "k", "content": "x"}},
			{"s3_delete_object", map[string]any{"bucket": "b", "key": "k"}},
			{"s3_copy_object", map[string]any{"source_bucket": "b", "source_key": "k", "dest_bucket": "b2", "dest_key": "k2"}},
			{"s3_get_object_metadata", map[string]any{"bucket": "b", "key": "k"}},
		},
	)
}

// ----------------------------------------------------------------- Vercel ---

// Parity: tools/tests/tools/test_vercel_tool.py::TestVercelListDeployments::test_successful_list
func TestBatch7Lane1VercelListDeployments(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "vercel_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"deployments": []any{
					map[string]any{
						"uid":     "dpl_1",
						"name":    "app",
						"url":     "app.vercel.app",
						"state":   "READY",
						"created": 1700000000000,
						"target":  "production",
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{
		"action":     "vercel_list_deployments",
		"project_id": "prj_1",
		"limit":      5,
		"state":      "READY",
	})
	deployments := batch7Lane1FieldSlice(t, result, "deployments")
	first := deployments[0].(map[string]any)
	if got := batch7Lane1FieldString(t, first, "uid"); got != "dpl_1" {
		t.Fatalf("uid = %q", got)
	}
	if got := batch7Lane1FieldText(t, first, "created"); got != "1700000000000" {
		t.Fatalf("created = %s", got)
	}
	request := recorder.last(t)
	if request.Path != "/v6/deployments" {
		t.Fatalf("path = %q", request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Query.Get("limit"); got != "5" {
		t.Fatalf("limit = %q", got)
	}
	if got := request.Query.Get("projectId"); got != "prj_1" {
		t.Fatalf("projectId = %q", got)
	}
}

// Parity: tools/tests/tools/test_vercel_tool.py::TestVercelGetDeployment::test_successful_get
func TestBatch7Lane1VercelGetDeployment(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "vercel_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"id":         "dpl_1",
				"name":       "app",
				"url":        "app.vercel.app",
				"readyState": "READY",
				"target":     "production",
				"createdAt":  1700,
				"ready":      1800,
				"creator":    map[string]any{"username": "alice"},
				"meta":       map[string]any{"githubCommitRef": "main"},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{
		"action":        "vercel_get_deployment",
		"deployment_id": "dpl_1",
	})
	if got := batch7Lane1FieldString(t, result, "uid"); got != "dpl_1" {
		t.Fatalf("uid = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "state"); got != "READY" {
		t.Fatalf("state = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "creator"); got != "alice" {
		t.Fatalf("creator = %q", got)
	}
	meta := batch7Lane1FieldMap(t, result, "meta")
	if got := batch7Lane1FieldString(t, meta, "githubCommitRef"); got != "main" {
		t.Fatalf("meta.githubCommitRef = %q", got)
	}
}

// Parity: tools/tests/tools/test_vercel_tool.py::TestVercelListProjects::test_successful_list
func TestBatch7Lane1VercelListProjects(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "vercel_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"projects": []any{
					map[string]any{
						"id":                "prj_1",
						"name":              "app",
						"framework":         "nextjs",
						"updatedAt":         123,
						"latestDeployments": []any{map[string]any{"url": "app-123.vercel.app"}},
					},
					map[string]any{"id": "prj_2", "name": "empty"},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{
		"action": "vercel_list_projects",
	})
	projects := batch7Lane1FieldSlice(t, result, "projects")
	first := projects[0].(map[string]any)
	if got := batch7Lane1FieldString(t, first, "latestDeploymentUrl"); got != "app-123.vercel.app" {
		t.Fatalf("latestDeploymentUrl = %q", got)
	}
	second := projects[1].(map[string]any)
	if got := batch7Lane1FieldString(t, second, "latestDeploymentUrl"); got != "" {
		t.Fatalf("empty latestDeploymentUrl = %q", got)
	}
}

// Parity-exempt: upstream vercel_tool has no get_project test.
func TestBatch7Lane1VercelGetProject(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "vercel_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"id":          "prj_1",
				"name":        "app",
				"framework":   "nextjs",
				"nodeVersion": "20.x",
				"updatedAt":   77,
				"env":         []any{map[string]any{"key": "A"}, map[string]any{"key": "B"}},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{
		"action":     "vercel_get_project",
		"project_id": "prj_1",
	})
	if got := batch7Lane1FieldText(t, result, "env_count"); got != "2" {
		t.Fatalf("env_count = %s", got)
	}
	if got := batch7Lane1FieldString(t, result, "nodeVersion"); got != "20.x" {
		t.Fatalf("nodeVersion = %q", got)
	}
}

// Parity: tools/tests/tools/test_vercel_tool.py::TestVercelListProjectDomains::test_successful_list
func TestBatch7Lane1VercelListProjectDomains(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "vercel_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"domains": []any{
					map[string]any{
						"name":      "example.com",
						"redirect":  "www.example.com",
						"gitBranch": "main",
						"verified":  true,
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{
		"action":     "vercel_list_project_domains",
		"project_id": "prj_1",
	})
	domains := batch7Lane1FieldSlice(t, result, "domains")
	domain := domains[0].(map[string]any)
	if got := batch7Lane1FieldString(t, domain, "gitBranch"); got != "main" {
		t.Fatalf("gitBranch = %q", got)
	}
	if got := batch7Lane1FieldText(t, domain, "verified"); got != "true" {
		t.Fatalf("verified = %s", got)
	}
	if recorder.last(t).Path != "/v9/projects/prj_1/domains" {
		t.Fatalf("path = %q", recorder.last(t).Path)
	}
}

// Parity: tools/tests/tools/test_vercel_tool.py::TestVercelEnvVars::test_list_env_vars
func TestBatch7Lane1VercelListEnvVars(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "vercel_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"envs": []any{
					map[string]any{
						"id":     "env_1",
						"key":    "API_KEY",
						"target": []any{"production", "preview"},
						"type":   "encrypted",
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{
		"action":     "vercel_list_env_vars",
		"project_id": "prj_1",
	})
	envVars := batch7Lane1FieldSlice(t, result, "env_vars")
	envVar := envVars[0].(map[string]any)
	if got := batch7Lane1FieldString(t, envVar, "key"); got != "API_KEY" {
		t.Fatalf("key = %q", got)
	}
	if targets := batch7Lane1FieldSlice(t, envVar, "target"); len(targets) != 2 {
		t.Fatalf("target = %v", targets)
	}
}

// Parity: tools/tests/tools/test_vercel_tool.py::TestVercelEnvVars::test_create_env_var_success
func TestBatch7Lane1VercelCreateEnvVar(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "vercel_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusCreated, map[string]any{"id": "env_9"})
		},
	)
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{
		"action":     "vercel_create_env_var",
		"project_id": "prj_1",
		"key":        "API_KEY",
		"value":      "secret-value",
		"target":     "production, preview",
	})
	if got := batch7Lane1FieldString(t, result, "id"); got != "env_9" {
		t.Fatalf("id = %q", got)
	}
	if got := batch7Lane1FieldString(t, result, "status"); got != "created" {
		t.Fatalf("status = %q", got)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost {
		t.Fatalf("method = %q", request.Method)
	}
	if request.Path != "/v10/projects/prj_1/env" {
		t.Fatalf("path = %q", request.Path)
	}
	targets := batch7Lane1FieldSlice(t, request.Body, "target")
	if len(targets) != 2 || targets[0] != "production" || targets[1] != "preview" {
		t.Fatalf("target = %v", targets)
	}
	if got := batch7Lane1FieldString(t, request.Body, "type"); got != "encrypted" {
		t.Fatalf("type = %q", got)
	}
}

// Parity: tools/tests/tools/test_vercel_tool.py::TestVercelListDeployments::test_missing_token
func TestBatch7Lane1VercelMissingToken(t *testing.T) {
	registry := newBatch7Lane1Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane1Execute(t, registry, "vercel", map[string]any{"action": "vercel_list_projects"})
	if got := batch7Lane1ErrorText(t, result); got != "VERCEL_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream vercel_tool has no HTTP status-mapping test.
func TestBatch7Lane1VercelStatusErrors(t *testing.T) {
	batch7Lane1AssertStatusTable(t, "vercel_tool", "vercel", nil,
		[]int{401, 403, 404, 429},
		map[int][]string{
			401: {"Unauthorized. Check your VERCEL_TOKEN."},
			403: {"Forbidden"},
			404: {"404"},
			429: {"429"},
		},
		[]struct {
			name  string
			input map[string]any
		}{
			{"vercel_list_deployments", map[string]any{}},
			{"vercel_get_deployment", map[string]any{"deployment_id": "dpl_1"}},
			{"vercel_list_projects", map[string]any{}},
			{"vercel_get_project", map[string]any{"project_id": "prj_1"}},
			{"vercel_list_project_domains", map[string]any{"project_id": "prj_1"}},
			{"vercel_list_env_vars", map[string]any{"project_id": "prj_1"}},
			{"vercel_create_env_var", map[string]any{"project_id": "prj_1", "key": "K", "value": "V"}},
		},
	)
}

// ------------------------------------------------------------- Docker Hub ---

// Parity: tools/tests/tools/test_docker_hub_tool.py::TestDockerHubSearch::test_successful_search
func TestBatch7Lane1DockerHubSearch(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "docker_hub_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"repo_name":         "nginx",
						"short_description": "Official build of Nginx.",
						"star_count":        19000,
						"is_official":       true,
						"is_automated":      false,
						"pull_count":        1000000,
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action":      "docker_hub_search",
		"query":       "nginx",
		"max_results": 5,
	})
	results := batch7Lane1FieldSlice(t, result, "results")
	first := results[0].(map[string]any)
	if got := batch7Lane1FieldString(t, first, "repo_name"); got != "nginx" {
		t.Fatalf("repo_name = %q", got)
	}
	if got := batch7Lane1FieldText(t, first, "pull_count"); got != "1000000" {
		t.Fatalf("pull_count = %s", got)
	}
	request := recorder.last(t)
	if request.Path != "/search/repositories" {
		t.Fatalf("path = %q", request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Query.Get("query"); got != "nginx" {
		t.Fatalf("query = %q", got)
	}
	if got := request.Query.Get("page_size"); got != "5" {
		t.Fatalf("page_size = %q", got)
	}
}

// Parity: tools/tests/tools/test_docker_hub_tool.py::TestDockerHubListRepos::test_successful_list
func TestBatch7Lane1DockerHubListRepos(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "docker_hub_tool", map[string]string{"DOCKER_HUB_USERNAME": "acme"},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"name":         "app",
						"namespace":    "acme",
						"description":  "Our app",
						"star_count":   1,
						"pull_count":   2,
						"last_updated": "2024-01-01T00:00:00Z",
						"is_private":   true,
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action": "docker_hub_list_repos",
	})
	if got := batch7Lane1FieldString(t, result, "namespace"); got != "acme" {
		t.Fatalf("namespace = %q", got)
	}
	repos := batch7Lane1FieldSlice(t, result, "repos")
	repo := repos[0].(map[string]any)
	if got := batch7Lane1FieldText(t, repo, "is_private"); got != "true" {
		t.Fatalf("is_private = %s", got)
	}
	if got := recorder.last(t).Path; got != "/repositories/acme" {
		t.Fatalf("path = %q", got)
	}
}

// Parity: tools/tests/tools/test_docker_hub_tool.py::TestDockerHubListTags::test_successful_list
func TestBatch7Lane1DockerHubListTags(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "docker_hub_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"name":         "latest",
						"full_size":    100,
						"last_updated": "2024-01-01T00:00:00Z",
						"images":       []any{map[string]any{"digest": "sha256:abc"}},
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action":     "docker_hub_list_tags",
		"repository": "acme/app",
	})
	tags := batch7Lane1FieldSlice(t, result, "tags")
	tag := tags[0].(map[string]any)
	if got := batch7Lane1FieldString(t, tag, "digest"); got != "sha256:abc" {
		t.Fatalf("digest = %q", got)
	}
	if got := batch7Lane1FieldText(t, tag, "full_size"); got != "100" {
		t.Fatalf("full_size = %s", got)
	}
	request := recorder.last(t)
	if got := request.Query.Get("ordering"); got != "-last_updated" {
		t.Fatalf("ordering = %q", got)
	}
	if got := request.Path; got != "/repositories/acme/app/tags" {
		t.Fatalf("path = %q", got)
	}
}

// Parity: tools/tests/tools/test_docker_hub_tool.py::TestDockerHubGetRepo::test_successful_get
func TestBatch7Lane1DockerHubGetRepo(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "docker_hub_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"name":             "app",
				"namespace":        "acme",
				"description":      "Our app",
				"star_count":       3,
				"pull_count":       4,
				"last_updated":     "2024-01-01T00:00:00Z",
				"is_private":       true,
				"full_description": strings.Repeat("r", 2100),
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action":     "docker_hub_get_repo",
		"repository": "acme/app",
	})
	description := batch7Lane1FieldString(t, result, "full_description")
	if len(description) != 2003 {
		t.Fatalf("full_description length = %d, want 2003", len(description))
	}
	if !strings.HasSuffix(description, "...") {
		t.Fatalf("full_description = %q", description[len(description)-10:])
	}
}

// Parity-exempt: upstream docker_hub_tool has no get_tag_detail test.
func TestBatch7Lane1DockerHubGetTagDetail(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "docker_hub_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"name":                  "v1.0",
				"full_size":             100,
				"last_updated":          "2024-01-01T00:00:00Z",
				"last_updater_username": "acme",
				"images": []any{
					map[string]any{
						"architecture": "amd64",
						"os":           "linux",
						"size":         50,
						"digest":       "sha256:x",
						"status":       "active",
						"last_pushed":  "2024-01-01T00:00:00Z",
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action":     "docker_hub_get_tag_detail",
		"repository": "acme/app",
		"tag":        "v1.0",
	})
	if got := batch7Lane1FieldText(t, result, "image_count"); got != "1" {
		t.Fatalf("image_count = %s", got)
	}
	images := batch7Lane1FieldSlice(t, result, "images")
	image := images[0].(map[string]any)
	if got := batch7Lane1FieldString(t, image, "architecture"); got != "amd64" {
		t.Fatalf("architecture = %q", got)
	}
}

// Parity-exempt: upstream docker_hub_tool has no delete_tag test.
func TestBatch7Lane1DockerHubDeleteTag(t *testing.T) {
	registry, recorder := batch7Lane1TestRegistry(
		t, "docker_hub_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusNoContent)
		},
	)
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action":     "docker_hub_delete_tag",
		"repository": "acme/app",
		"tag":        "old",
	})
	if got := batch7Lane1FieldString(t, result, "status"); got != "deleted" {
		t.Fatalf("status = %q", got)
	}
	request := recorder.last(t)
	if request.Method != http.MethodDelete {
		t.Fatalf("method = %q", request.Method)
	}
	if request.Path != "/repositories/acme/app/tags/old" {
		t.Fatalf("path = %q", request.Path)
	}
}

// Parity-exempt: upstream docker_hub_tool has no list_webhooks test.
func TestBatch7Lane1DockerHubListWebhooks(t *testing.T) {
	registry, _ := batch7Lane1TestRegistry(
		t, "docker_hub_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane1WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"id":                    "wh_1",
						"name":                  "hook",
						"active":                true,
						"expect_final_callback": false,
						"webhooks":              []any{map[string]any{"hook_url": "https://example.com/hook"}},
						"created_date":          "2024-01-01T00:00:00Z",
					},
				},
			})
		},
	)
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action":     "docker_hub_list_webhooks",
		"repository": "acme/app",
	})
	webhooks := batch7Lane1FieldSlice(t, result, "webhooks")
	webhook := webhooks[0].(map[string]any)
	urls := batch7Lane1FieldSlice(t, webhook, "hook_urls")
	if urls[0] != "https://example.com/hook" {
		t.Fatalf("hook_urls = %v", urls)
	}
	if got := batch7Lane1FieldString(t, webhook, "created_at"); got != "2024-01-01T00:00:00Z" {
		t.Fatalf("created_at = %q", got)
	}
}

// Parity: tools/tests/tools/test_docker_hub_tool.py::TestDockerHubSearch::test_missing_token
func TestBatch7Lane1DockerHubMissingToken(t *testing.T) {
	registry := newBatch7Lane1Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane1Execute(t, registry, "docker_hub", map[string]any{
		"action": "docker_hub_search",
		"query":  "nginx",
	})
	if got := batch7Lane1ErrorText(t, result); got != "DOCKER_HUB_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream docker_hub_tool has no HTTP status-mapping test.
func TestBatch7Lane1DockerHubStatusErrors(t *testing.T) {
	batch7Lane1AssertStatusTable(t, "docker_hub_tool", "docker_hub", nil,
		[]int{401, 403, 404, 429},
		map[int][]string{
			401: {"Unauthorized. Check your DOCKER_HUB_TOKEN."},
			403: {"403"},
			404: {"Not found"},
			429: {"429"},
		},
		[]struct {
			name  string
			input map[string]any
		}{
			{"docker_hub_search", map[string]any{"query": "nginx"}},
			{"docker_hub_list_repos", map[string]any{"namespace": "acme"}},
			{"docker_hub_list_tags", map[string]any{"repository": "acme/app"}},
			{"docker_hub_get_repo", map[string]any{"repository": "acme/app"}},
			{"docker_hub_get_tag_detail", map[string]any{"repository": "acme/app", "tag": "latest"}},
			{"docker_hub_delete_tag", map[string]any{"repository": "acme/app", "tag": "old"}},
			{"docker_hub_list_webhooks", map[string]any{"repository": "acme/app"}},
		},
	)
}

// ---------------------------------------------------------------- catalog ---

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not the registry surface.
func TestBatch7Lane1CatalogIntegrity(t *testing.T) {
	registry := newBatch7Lane1Registry()
	if got := len(registry.Names()); got != 4 {
		t.Fatalf("tools = %d, want 4 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 31 {
		t.Fatalf("actions = %d, want 31", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	expected := map[string]int{
		"gitlab":     9,
		"aws_s3":     8,
		"vercel":     7,
		"docker_hub": 7,
	}
	for name, want := range expected {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if got := len(tool.Actions); got != want {
			t.Fatalf("%s actions = %d, want %d", name, got, want)
		}
		if tool.ID == "" || tool.Summary == "" || tool.DisplayName == "" {
			t.Fatalf("%s missing identity: %+v", name, tool)
		}
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", name, action.Name)
			}
			if registry.Impls(name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallBatch7Lane1(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch7Lane1(shared); installed != 4 {
		t.Fatalf("installed = %d, want 4", installed)
	}
	for _, name := range []string{"gitlab", "aws_s3", "vercel", "docker_hub"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("gitlab")
	if !ok {
		t.Fatal("gitlab tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", tool.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("gitlab missing action selector: %v", properties)
	}
	if enum, _ := action["enum"].([]string); len(enum) != 9 {
		t.Fatalf("gitlab action enum = %v", action["enum"])
	}
	if tool.Description == "" {
		t.Fatal("gitlab empty description")
	}
}

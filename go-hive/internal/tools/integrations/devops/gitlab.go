package devops

// GitLab: catalog, nine REST v4 actions and the shared call helper.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// gitlabTool publishes the nine GitLab REST v4 actions.
var gitlabTool = &Tool{
	ID:          "gitlab_tool",
	Name:        "gitlab",
	DisplayName: "GitLab",
	Summary:     "GitLab projects, issues and merge requests (9 actions).",
	Actions: []Action{
		{
			Name:    "gitlab_list_projects",
			Summary: "List GitLab projects.",
			Source:  "gitlab_tool/gitlab_tool.py",
			Params: map[string]any{
				"search":     integrationkit.ParamDefault("string", ""),
				"owned":      integrationkit.ParamDefault("boolean", false),
				"membership": integrationkit.ParamDefault("boolean", true),
				"per_page":   integrationkit.ParamDefault("integer", 20),
				"page":       integrationkit.ParamDefault("integer", 1),
			},
		},
		{
			Name:     "gitlab_get_project",
			Summary:  "Get details about a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
			},
		},
		{
			Name:     "gitlab_list_issues",
			Summary:  "List issues in a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
				"state":      integrationkit.ParamDefault("string", "opened"),
				"labels":     integrationkit.ParamDefault("string", ""),
				"search":     integrationkit.ParamDefault("string", ""),
				"per_page":   integrationkit.ParamDefault("integer", 20),
				"page":       integrationkit.ParamDefault("integer", 1),
			},
		},
		{
			Name:     "gitlab_get_issue",
			Summary:  "Get details about a specific GitLab issue.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "issue_iid"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
				"issue_iid":  integrationkit.Param("integer"),
			},
		},
		{
			Name:     "gitlab_create_issue",
			Summary:  "Create a new issue in a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "title"},
			Params: map[string]any{
				"project_id":   integrationkit.Param("string"),
				"title":        integrationkit.Param("string"),
				"description":  integrationkit.ParamDefault("string", ""),
				"labels":       integrationkit.ParamDefault("string", ""),
				"assignee_ids": integrationkit.ParamDefault("string", ""),
			},
		},
		{
			Name:     "gitlab_list_merge_requests",
			Summary:  "List merge requests in a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
				"state":      integrationkit.ParamDefault("string", "opened"),
				"per_page":   integrationkit.ParamDefault("integer", 20),
				"page":       integrationkit.ParamDefault("integer", 1),
			},
		},
		{
			Name:     "gitlab_update_issue",
			Summary:  "Update an existing GitLab issue.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "issue_iid"},
			Params: map[string]any{
				"project_id":   integrationkit.Param("string"),
				"issue_iid":    integrationkit.Param("integer"),
				"title":        integrationkit.ParamDefault("string", ""),
				"description":  integrationkit.ParamDefault("string", ""),
				"state_event":  integrationkit.ParamDefault("string", ""),
				"labels":       integrationkit.ParamDefault("string", ""),
				"assignee_ids": integrationkit.ParamDefault("string", ""),
			},
		},
		{
			Name:     "gitlab_get_merge_request",
			Summary:  "Get details about a specific GitLab merge request.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "merge_request_iid"},
			Params: map[string]any{
				"project_id":        integrationkit.Param("string"),
				"merge_request_iid": integrationkit.Param("integer"),
			},
		},
		{
			Name:     "gitlab_create_merge_request_note",
			Summary:  "Add a comment (note) to a GitLab merge request.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "merge_request_iid", "body"},
			Params: map[string]any{
				"project_id":        integrationkit.Param("string"),
				"merge_request_iid": integrationkit.Param("integer"),
				"body":              integrationkit.Param("string"),
			},
		},
	},
}

func gitlabImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"gitlab_list_projects":             gitlabListProjects,
		"gitlab_get_project":               gitlabGetProject,
		"gitlab_list_issues":               gitlabListIssues,
		"gitlab_get_issue":                 gitlabGetIssue,
		"gitlab_create_issue":              gitlabCreateIssue,
		"gitlab_list_merge_requests":       gitlabListMergeRequests,
		"gitlab_update_issue":              gitlabUpdateIssue,
		"gitlab_get_merge_request":         gitlabGetMergeRequest,
		"gitlab_create_merge_request_note": gitlabCreateMergeRequestNote,
	}
}

// ------------------------------------------------------------------ GitLab ---

// gitlabBase resolves the instance root; WithBaseURL wins over
// GITLAB_URL, which wins over gitlab.com.
func gitlabBase(rt *Runtime) string {
	if base := moduleURL(rt, "gitlab_tool"); base != "" && base != gitlabDefaultBase {
		return base
	}
	if env := strings.TrimSpace(runtimeGetenv(rt)("GITLAB_URL")); env != "" {
		return strings.TrimRight(env, "/")
	}
	return gitlabDefaultBase
}

// gitlabCall performs one /api/v4 call and maps HTTP failures to the
// upstream error dicts. Upstream's _post omits the 404/429 branches, so the
// method decides whether those two mappings apply. The boolean reports success.
func gitlabCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := moduleToken(rt, "gitlab_tool")
	if token == "" {
		return map[string]any{
			"error": "GITLAB_TOKEN not set",
			"help":  "Create a personal access token at https://gitlab.com/-/user_settings/personal_access_tokens",
		}, false
	}
	target := gitlabBase(rt) + "/api/v4" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{"PRIVATE-TOKEN": token}
	if payload != nil {
		headers["Content-Type"] = "application/json"
	}
	reply, err := jsonRequest(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to GitLab timed out"}, false
		}
		return map[string]any{"error": "GitLab request failed: " + err.Error()}, false
	}
	richErrors := method != http.MethodPost
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your GitLab token."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Insufficient permissions."}, false
	case http.StatusNotFound:
		if richErrors {
			return map[string]any{"error": "Not found."}, false
		}
	case http.StatusTooManyRequests:
		if richErrors {
			return map[string]any{"error": "Rate limited. Try again shortly."}, false
		}
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"GitLab API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "GitLab request failed: invalid JSON response"}, false
	}
	return value, true
}

// gitlabPage renders the clamped per_page/page pair.
func gitlabPage(input map[string]any) (string, string) {
	perPage := clampInt(integrationkit.IntArg(input, "per_page", 20), 1, 100)
	page := integrationkit.IntArg(input, "page", 1)
	if page < 1 {
		page = 1
	}
	return strconv.Itoa(perPage), strconv.Itoa(page)
}

func gitlabListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	perPage, page := gitlabPage(input)
	query := url.Values{
		"per_page":   {perPage},
		"page":       {page},
		"membership": {strconv.FormatBool(integrationkit.BoolArg(input, "membership", true))},
	}
	if search := integrationkit.StringArg(input, "search"); search != "" {
		query.Set("search", search)
	}
	if integrationkit.BoolArg(input, "owned", false) {
		query.Set("owned", "true")
	}
	value, ok := gitlabCall(ctx, rt, http.MethodGet, "/projects", query, nil)
	if !ok {
		return value, nil
	}
	projects := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		project := asMap(raw)
		projects = append(projects, map[string]any{
			"id":                  asNumber(project["id"]),
			"name":                asString(project["name"]),
			"path_with_namespace": asString(project["path_with_namespace"]),
			"description":         truncateText(asString(project["description"]), 200),
			"visibility":          asString(project["visibility"]),
			"default_branch":      asString(project["default_branch"]),
			"web_url":             asString(project["web_url"]),
			"star_count":          intValue(project["star_count"], 0),
			"last_activity_at":    asString(project["last_activity_at"]),
		})
	}
	return map[string]any{"projects": projects, "count": len(projects)}, nil
}

func gitlabGetProject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	query := url.Values{"statistics": {"true"}}
	value, ok := gitlabCall(
		ctx, rt, http.MethodGet, "/projects/"+projectID, query, nil,
	)
	if !ok {
		return value, nil
	}
	project, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	stats := asMap(project["statistics"])
	return map[string]any{
		"id":                  asNumber(project["id"]),
		"name":                asString(project["name"]),
		"path_with_namespace": asString(project["path_with_namespace"]),
		"description":         truncateText(asString(project["description"]), 500),
		"visibility":          asString(project["visibility"]),
		"default_branch":      asString(project["default_branch"]),
		"web_url":             asString(project["web_url"]),
		"star_count":          intValue(project["star_count"], 0),
		"forks_count":         intValue(project["forks_count"], 0),
		"open_issues_count":   intValue(project["open_issues_count"], 0),
		"commit_count":        intValue(stats["commit_count"], 0),
		"created_at":          asString(project["created_at"]),
		"last_activity_at":    asString(project["last_activity_at"]),
	}, nil
}

func gitlabListIssues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	perPage, page := gitlabPage(input)
	state := integrationkit.StringArg(input, "state")
	if state == "" {
		state = "opened"
	}
	query := url.Values{"state": {state}, "per_page": {perPage}, "page": {page}}
	if labels := integrationkit.StringArg(input, "labels"); labels != "" {
		query.Set("labels", labels)
	}
	if search := integrationkit.StringArg(input, "search"); search != "" {
		query.Set("search", search)
	}
	value, ok := gitlabCall(
		ctx, rt, http.MethodGet, "/projects/"+projectID+"/issues", query, nil,
	)
	if !ok {
		return value, nil
	}
	issues := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		issue := asMap(raw)
		issues = append(issues, map[string]any{
			"iid":        asNumber(issue["iid"]),
			"title":      asString(issue["title"]),
			"state":      asString(issue["state"]),
			"labels":     stringList(issue["labels"]),
			"assignees":  usernames(issue["assignees"]),
			"author":     asString(asMap(issue["author"])["username"]),
			"created_at": asString(issue["created_at"]),
			"updated_at": asString(issue["updated_at"]),
			"web_url":    asString(issue["web_url"]),
		})
	}
	return map[string]any{"issues": issues, "count": len(issues)}, nil
}

func gitlabGetIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	issueIID := integrationkit.IntArg(input, "issue_iid", 0)
	if projectID == "" || issueIID == 0 {
		return map[string]any{"error": "project_id and issue_iid are required"}, nil
	}
	value, ok := gitlabCall(
		ctx, rt, http.MethodGet,
		fmt.Sprintf("/projects/%s/issues/%d", projectID, issueIID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	issue, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":         asNumber(issue["iid"]),
		"title":       asString(issue["title"]),
		"description": truncateText(asString(issue["description"]), 1000),
		"state":       asString(issue["state"]),
		"labels":      stringList(issue["labels"]),
		"assignees":   usernames(issue["assignees"]),
		"author":      asString(asMap(issue["author"])["username"]),
		"milestone":   asString(asMap(issue["milestone"])["title"]),
		"due_date":    issue["due_date"],
		"web_url":     asString(issue["web_url"]),
		"created_at":  asString(issue["created_at"]),
		"updated_at":  asString(issue["updated_at"]),
		"closed_at":   issue["closed_at"],
	}, nil
}

func gitlabCreateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	title := integrationkit.StringArg(input, "title")
	if projectID == "" || title == "" {
		return map[string]any{"error": "project_id and title are required"}, nil
	}
	body := map[string]any{"title": title}
	if description := integrationkit.StringArg(input, "description"); description != "" {
		body["description"] = description
	}
	if labels := integrationkit.StringArg(input, "labels"); labels != "" {
		body["labels"] = labels
	}
	if raw := integrationkit.StringArg(input, "assignee_ids"); raw != "" {
		ids, ok := splitIDs(raw)
		if !ok {
			return map[string]any{"error": "assignee_ids must be a comma-separated list of user IDs"}, nil
		}
		body["assignee_ids"] = ids
	}
	value, ok := gitlabCall(
		ctx, rt, http.MethodPost, "/projects/"+projectID+"/issues", nil, body,
	)
	if !ok {
		return value, nil
	}
	issue, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":     asNumber(issue["iid"]),
		"title":   asString(issue["title"]),
		"web_url": asString(issue["web_url"]),
		"status":  "created",
	}, nil
}

func gitlabListMergeRequests(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	perPage, page := gitlabPage(input)
	state := integrationkit.StringArg(input, "state")
	if state == "" {
		state = "opened"
	}
	query := url.Values{"state": {state}, "per_page": {perPage}, "page": {page}}
	value, ok := gitlabCall(
		ctx, rt, http.MethodGet, "/projects/"+projectID+"/merge_requests", query, nil,
	)
	if !ok {
		return value, nil
	}
	requests := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		request := asMap(raw)
		requests = append(requests, map[string]any{
			"iid":           asNumber(request["iid"]),
			"title":         asString(request["title"]),
			"state":         asString(request["state"]),
			"source_branch": asString(request["source_branch"]),
			"target_branch": asString(request["target_branch"]),
			"author":        asString(asMap(request["author"])["username"]),
			"web_url":       asString(request["web_url"]),
			"created_at":    asString(request["created_at"]),
			"updated_at":    asString(request["updated_at"]),
		})
	}
	return map[string]any{"merge_requests": requests, "count": len(requests)}, nil
}

func gitlabUpdateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	issueIID := integrationkit.IntArg(input, "issue_iid", 0)
	if projectID == "" || issueIID == 0 {
		return map[string]any{"error": "project_id and issue_iid are required"}, nil
	}
	body := map[string]any{}
	if title := integrationkit.StringArg(input, "title"); title != "" {
		body["title"] = title
	}
	if description := integrationkit.StringArg(input, "description"); description != "" {
		body["description"] = description
	}
	if event := integrationkit.StringArg(input, "state_event"); event != "" {
		body["state_event"] = event
	}
	if labels := integrationkit.StringArg(input, "labels"); labels != "" {
		body["labels"] = labels
	}
	if raw := integrationkit.StringArg(input, "assignee_ids"); raw != "" {
		ids, ok := splitIDs(raw)
		if !ok {
			return map[string]any{"error": "assignee_ids must be a comma-separated list of user IDs"}, nil
		}
		body["assignee_ids"] = ids
	}
	if len(body) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	value, ok := gitlabCall(
		ctx, rt, http.MethodPut,
		fmt.Sprintf("/projects/%s/issues/%d", projectID, issueIID), nil, body,
	)
	if !ok {
		return value, nil
	}
	issue, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":     asNumber(issue["iid"]),
		"title":   asString(issue["title"]),
		"state":   asString(issue["state"]),
		"web_url": asString(issue["web_url"]),
		"status":  "updated",
	}, nil
}

func gitlabGetMergeRequest(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	requestIID := integrationkit.IntArg(input, "merge_request_iid", 0)
	if projectID == "" || requestIID == 0 {
		return map[string]any{"error": "project_id and merge_request_iid are required"}, nil
	}
	value, ok := gitlabCall(
		ctx, rt, http.MethodGet,
		fmt.Sprintf("/projects/%s/merge_requests/%d", projectID, requestIID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	request, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":           asNumber(request["iid"]),
		"title":         asString(request["title"]),
		"description":   truncateText(asString(request["description"]), 1000),
		"state":         asString(request["state"]),
		"source_branch": asString(request["source_branch"]),
		"target_branch": asString(request["target_branch"]),
		"author":        asString(asMap(request["author"])["username"]),
		"reviewers":     usernames(request["reviewers"]),
		"merge_status":  asString(request["merge_status"]),
		"has_conflicts": asBool(request["has_conflicts"], false),
		"changes_count": request["changes_count"],
		"web_url":       asString(request["web_url"]),
		"created_at":    asString(request["created_at"]),
		"updated_at":    asString(request["updated_at"]),
		"merged_at":     request["merged_at"],
	}, nil
}

func gitlabCreateMergeRequestNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	requestIID := integrationkit.IntArg(input, "merge_request_iid", 0)
	body := integrationkit.StringArg(input, "body")
	if projectID == "" || requestIID == 0 || body == "" {
		return map[string]any{"error": "project_id, merge_request_iid, and body are required"}, nil
	}
	value, ok := gitlabCall(
		ctx, rt, http.MethodPost,
		fmt.Sprintf("/projects/%s/merge_requests/%d/notes", projectID, requestIID), nil,
		map[string]any{"body": body},
	)
	if !ok {
		return value, nil
	}
	note, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"id":         asNumber(note["id"]),
		"body":       truncateText(asString(note["body"]), 500),
		"author":     asString(asMap(note["author"])["username"]),
		"created_at": asString(note["created_at"]),
		"status":     "created",
	}, nil
}

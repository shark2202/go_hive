// Asana integration (upstream asana_tool, 9 actions). Part of batch 5 lane 1;
// see atlassian_asana.go for the registry and shared helpers.
package collab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const asanaDefaultBase = "https://app.asana.com/api/1.0"

var asanaTool = &Tool{
	ID:          "asana_tool",
	Name:        "asana",
	DisplayName: "Asana",
	Summary:     "Asana integration (9 actions).",
	Actions: []Action{
		{
			Name:    "asana_list_workspaces",
			Summary: "List all workspaces accessible to the authenticated user.",
			Source:  "asana_tool/asana_tool.py",
		},
		{
			Name:     "asana_list_projects",
			Summary:  "List projects in an Asana workspace.",
			Source:   "asana_tool/asana_tool.py",
			Required: []string{"workspace_gid"},
			Params: map[string]any{
				"workspace_gid": param("string"),
				"limit":         paramDefault("integer", 50),
			},
		},
		{
			Name:    "asana_list_tasks",
			Summary: "List tasks from Asana, filtered by project or assignee.",
			Source:  "asana_tool/asana_tool.py",
			Params: map[string]any{
				"project_gid":   paramDefault("string", ""),
				"assignee":      paramDefault("string", "me"),
				"workspace_gid": paramDefault("string", ""),
				"limit":         paramDefault("integer", 50),
			},
		},
		{
			Name:     "asana_get_task",
			Summary:  "Get details of a specific Asana task.",
			Source:   "asana_tool/asana_tool.py",
			Required: []string{"task_gid"},
			Params: map[string]any{
				"task_gid": param("string"),
			},
		},
		{
			Name:     "asana_create_task",
			Summary:  "Create a new task in Asana.",
			Source:   "asana_tool/asana_tool.py",
			Required: []string{"workspace_gid", "name"},
			Params: map[string]any{
				"workspace_gid": param("string"),
				"name":          param("string"),
				"notes":         paramDefault("string", ""),
				"project_gid":   paramDefault("string", ""),
				"assignee":      paramDefault("string", ""),
				"due_on":        paramDefault("string", ""),
			},
		},
		{
			Name:     "asana_search_tasks",
			Summary:  "Search tasks in an Asana workspace.",
			Source:   "asana_tool/asana_tool.py",
			Required: []string{"workspace_gid", "query"},
			Params: map[string]any{
				"workspace_gid": param("string"),
				"query":         param("string"),
				"limit":         paramDefault("integer", 20),
			},
		},
		{
			Name:     "asana_update_task",
			Summary:  "Update an existing Asana task.",
			Source:   "asana_tool/asana_tool.py",
			Required: []string{"task_gid"},
			Params: map[string]any{
				"task_gid":  param("string"),
				"name":      paramDefault("string", ""),
				"notes":     paramDefault("string", ""),
				"completed": param("boolean"),
				"due_on":    paramDefault("string", ""),
				"assignee":  paramDefault("string", ""),
			},
		},
		{
			Name:     "asana_add_comment",
			Summary:  "Add a comment (story) to an Asana task.",
			Source:   "asana_tool/asana_tool.py",
			Required: []string{"task_gid", "text"},
			Params: map[string]any{
				"task_gid": param("string"),
				"text":     param("string"),
			},
		},
		{
			Name:     "asana_create_subtask",
			Summary:  "Create a subtask under an existing Asana task.",
			Source:   "asana_tool/asana_tool.py",
			Required: []string{"parent_task_gid", "name"},
			Params: map[string]any{
				"parent_task_gid": param("string"),
				"name":            param("string"),
				"notes":           paramDefault("string", ""),
				"assignee":        paramDefault("string", ""),
				"due_on":          paramDefault("string", ""),
			},
		},
	},
}

func asanaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"asana_list_workspaces": asanaAuthFirst(asanaListWorkspaces),
		"asana_list_projects":   asanaAuthFirst(asanaListProjects),
		"asana_list_tasks":      asanaAuthFirst(asanaListTasks),
		"asana_get_task":        asanaAuthFirst(asanaGetTask),
		"asana_create_task":     asanaAuthFirst(asanaCreateTask),
		"asana_search_tasks":    asanaAuthFirst(asanaSearchTasks),
		"asana_update_task":     asanaAuthFirst(asanaUpdateTask),
		"asana_add_comment":     asanaAuthFirst(asanaAddComment),
		"asana_create_subtask":  asanaAuthFirst(asanaCreateSubtask),
	}
}

// asanaAuthFirst enforces upstream's credentials-before-arguments
// order: every Asana tool resolves the PAT before validating required fields.
func asanaAuthFirst(fn ActionFunc) ActionFunc {
	return func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		if moduleToken(rt, "asana_tool") == "" {
			return asanaAuthError(), nil
		}
		return fn(ctx, rt, input)
	}
}

func asanaAuthError() map[string]any {
	return map[string]any{
		"error": "ASANA_ACCESS_TOKEN not set",
		"help":  "Create a PAT at https://app.asana.com/0/my-apps",
	}
}

// asanaClient resolves the API base and personal access token.
func asanaClient(rt *Runtime) (base, token string, ok bool) {
	base = moduleBaseURL(rt, "asana_tool")
	if base == "" {
		base = asanaDefaultBase
	}
	token = moduleToken(rt, "asana_tool")
	return base, token, token != ""
}

// asanaRequest performs one Asana API call. Reads and writes have
// different upstream error mappings: writes only special-case 401.
func asanaRequest(ctx context.Context, rt *Runtime, method, path string, query url.Values, payload any, write bool) map[string]any {
	base, token, ok := asanaClient(rt)
	if !ok {
		return asanaAuthError()
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	resp, err := do(ctx, rt, method, base+path, query, payload, headers)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Asana timed out"}
		}
		return map[string]any{"error": "Asana request failed: " + err.Error()}
	}
	if resp.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your ASANA_ACCESS_TOKEN."}
	}
	if write {
		if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
			return map[string]any{
				"error": fmt.Sprintf("Asana API error %d: %s", resp.Status, truncateTextClamped(string(resp.Body), 500)),
			}
		}
	} else {
		switch resp.Status {
		case http.StatusForbidden:
			return map[string]any{"error": "Forbidden: " + truncateTextClamped(string(resp.Body), 300)}
		case http.StatusNotFound:
			return map[string]any{"error": "Not found"}
		case http.StatusOK:
			// Success: fall through to decoding.
		default:
			return map[string]any{
				"error": fmt.Sprintf("Asana API error %d: %s", resp.Status, truncateTextClamped(string(resp.Body), 500)),
			}
		}
	}
	object, err := decode(resp.Body)
	if err != nil {
		return map[string]any{"error": "Asana request failed: " + err.Error()}
	}
	return object
}

// asanaWrite wraps a request body in Asana's {"data": ...} envelope.
func asanaWrite(ctx context.Context, rt *Runtime, method, path string, body map[string]any) map[string]any {
	if body == nil {
		body = map[string]any{}
	}
	return asanaRequest(ctx, rt, method, path, nil, map[string]any{"data": body}, true)
}

func asanaListWorkspaces(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	data := asanaRequest(ctx, rt, http.MethodGet, "/workspaces", nil, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	workspaces := make([]map[string]any, 0)
	for _, rawWorkspace := range asSlice(data["data"]) {
		workspace := asMap(rawWorkspace)
		workspaces = append(workspaces, map[string]any{
			"gid":  atlassianText(workspace, "gid"),
			"name": atlassianText(workspace, "name"),
		})
	}
	return map[string]any{"workspaces": workspaces}, nil
}

func asanaListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	workspaceGID := stringArg(input, "workspace_gid")
	if workspaceGID == "" {
		return map[string]any{"error": "workspace_gid is required"}, nil
	}
	query := url.Values{}
	query.Set("workspace", workspaceGID)
	query.Set("limit", strconv.Itoa(clampInt(intArg(input, "limit", 50), 1, 100)))
	query.Set("opt_fields", "name,color,archived,created_at")
	data := asanaRequest(ctx, rt, http.MethodGet, "/projects", query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	projects := make([]map[string]any, 0)
	for _, rawProject := range asSlice(data["data"]) {
		project := asMap(rawProject)
		projects = append(projects, map[string]any{
			"gid":      atlassianText(project, "gid"),
			"name":     atlassianText(project, "name"),
			"color":    atlassianText(project, "color"),
			"archived": atlassianBool(project, "archived"),
		})
	}
	return map[string]any{"projects": projects}, nil
}

func asanaListTasks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectGID := stringArg(input, "project_gid")
	workspaceGID := stringArg(input, "workspace_gid")
	if projectGID == "" && workspaceGID == "" {
		return map[string]any{"error": "Either project_gid or workspace_gid is required"}, nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(clampInt(intArg(input, "limit", 50), 1, 100)))
	query.Set("opt_fields", "name,completed,due_on,assignee.name")
	if projectGID != "" {
		query.Set("project", projectGID)
	} else {
		query.Set("workspace", workspaceGID)
		query.Set("assignee", stringArg(input, "assignee"))
	}
	data := asanaRequest(ctx, rt, http.MethodGet, "/tasks", query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	tasks := make([]map[string]any, 0)
	for _, rawTask := range asSlice(data["data"]) {
		task := asMap(rawTask)
		tasks = append(tasks, map[string]any{
			"gid":           atlassianText(task, "gid"),
			"name":          atlassianText(task, "name"),
			"completed":     atlassianBool(task, "completed"),
			"due_on":        atlassianText(task, "due_on"),
			"assignee_name": atlassianText(asMap(task["assignee"]), "name"),
		})
	}
	return map[string]any{"tasks": tasks, "count": len(tasks)}, nil
}

func asanaGetTask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	taskGID := stringArg(input, "task_gid")
	if taskGID == "" {
		return map[string]any{"error": "task_gid is required"}, nil
	}
	query := url.Values{}
	query.Set("opt_fields", "name,notes,completed,due_on,assignee.name,projects.name,tags.name,created_at,modified_at")
	data := asanaRequest(ctx, rt, http.MethodGet, "/tasks/"+taskGID, query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := asMap(data["data"])
	projects := make([]string, 0)
	for _, rawProject := range asSlice(task["projects"]) {
		projects = append(projects, atlassianText(asMap(rawProject), "name"))
	}
	tags := make([]string, 0)
	for _, rawTag := range asSlice(task["tags"]) {
		tags = append(tags, atlassianText(asMap(rawTag), "name"))
	}
	return map[string]any{
		"gid":           atlassianText(task, "gid"),
		"name":          atlassianText(task, "name"),
		"notes":         truncateTextClamped(optionalText(task, "notes"), 500),
		"completed":     atlassianBool(task, "completed"),
		"due_on":        atlassianText(task, "due_on"),
		"assignee_name": atlassianText(asMap(task["assignee"]), "name"),
		"projects":      projects,
		"tags":          tags,
		"created_at":    atlassianText(task, "created_at"),
		"modified_at":   atlassianText(task, "modified_at"),
	}, nil
}

func asanaCreateTask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	workspaceGID := stringArg(input, "workspace_gid")
	name := stringArg(input, "name")
	if workspaceGID == "" || name == "" {
		return map[string]any{"error": "workspace_gid and name are required"}, nil
	}
	body := map[string]any{"workspace": workspaceGID, "name": name}
	if notes := stringArg(input, "notes"); notes != "" {
		body["notes"] = notes
	}
	if projectGID := stringArg(input, "project_gid"); projectGID != "" {
		body["projects"] = []string{projectGID}
	}
	if assignee := stringArg(input, "assignee"); assignee != "" {
		body["assignee"] = assignee
	}
	if dueOn := stringArg(input, "due_on"); dueOn != "" {
		body["due_on"] = dueOn
	}
	data := asanaWrite(ctx, rt, http.MethodPost, "/tasks", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := asMap(data["data"])
	return map[string]any{
		"gid":    atlassianText(task, "gid"),
		"name":   atlassianText(task, "name"),
		"status": "created",
	}, nil
}

func asanaSearchTasks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	workspaceGID := stringArg(input, "workspace_gid")
	searchText := stringArg(input, "query")
	if workspaceGID == "" || searchText == "" {
		return map[string]any{"error": "workspace_gid and query are required"}, nil
	}
	query := url.Values{}
	query.Set("text", searchText)
	query.Set("limit", strconv.Itoa(clampInt(intArg(input, "limit", 20), 1, 100)))
	query.Set("opt_fields", "name,completed,due_on")
	data := asanaRequest(ctx, rt, http.MethodGet, "/workspaces/"+workspaceGID+"/tasks/search", query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	tasks := make([]map[string]any, 0)
	for _, rawTask := range asSlice(data["data"]) {
		task := asMap(rawTask)
		tasks = append(tasks, map[string]any{
			"gid":       atlassianText(task, "gid"),
			"name":      atlassianText(task, "name"),
			"completed": atlassianBool(task, "completed"),
			"due_on":    atlassianText(task, "due_on"),
		})
	}
	return map[string]any{"query": searchText, "tasks": tasks}, nil
}

func asanaUpdateTask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	taskGID := stringArg(input, "task_gid")
	if taskGID == "" {
		return map[string]any{"error": "task_gid is required"}, nil
	}
	body := map[string]any{}
	if name := stringArg(input, "name"); name != "" {
		body["name"] = name
	}
	if notes := stringArg(input, "notes"); notes != "" {
		body["notes"] = notes
	}
	if completed, present := optionalBool(input, "completed"); present {
		body["completed"] = completed
	}
	if dueOn := stringArg(input, "due_on"); dueOn != "" {
		body["due_on"] = dueOn
	}
	if assignee := stringArg(input, "assignee"); assignee != "" {
		body["assignee"] = assignee
	}
	if len(body) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	data := asanaWrite(ctx, rt, http.MethodPut, "/tasks/"+taskGID, body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := asMap(data["data"])
	return map[string]any{
		"gid":       atlassianText(task, "gid"),
		"name":      atlassianText(task, "name"),
		"completed": atlassianBool(task, "completed"),
		"status":    "updated",
	}, nil
}

func asanaAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	taskGID := stringArg(input, "task_gid")
	text := stringArg(input, "text")
	if taskGID == "" || text == "" {
		return map[string]any{"error": "task_gid and text are required"}, nil
	}
	data := asanaWrite(ctx, rt, http.MethodPost, "/tasks/"+taskGID+"/stories", map[string]any{"text": text})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	story := asMap(data["data"])
	return map[string]any{
		"gid":        atlassianText(story, "gid"),
		"text":       truncateTextClamped(optionalText(story, "text"), 500),
		"created_at": atlassianText(story, "created_at"),
		"status":     "created",
	}, nil
}

func asanaCreateSubtask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	parentTaskGID := stringArg(input, "parent_task_gid")
	name := stringArg(input, "name")
	if parentTaskGID == "" || name == "" {
		return map[string]any{"error": "parent_task_gid and name are required"}, nil
	}
	body := map[string]any{"name": name}
	if notes := stringArg(input, "notes"); notes != "" {
		body["notes"] = notes
	}
	if assignee := stringArg(input, "assignee"); assignee != "" {
		body["assignee"] = assignee
	}
	if dueOn := stringArg(input, "due_on"); dueOn != "" {
		body["due_on"] = dueOn
	}
	data := asanaWrite(ctx, rt, http.MethodPost, "/tasks/"+parentTaskGID+"/subtasks", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := asMap(data["data"])
	return map[string]any{
		"gid":    atlassianText(task, "gid"),
		"name":   atlassianText(task, "name"),
		"status": "created",
	}, nil
}

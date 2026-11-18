// Asana integration (upstream asana_tool, 9 actions). Part of batch 5 lane 1;
// see batch5_lane1.go for the registry and shared helpers.
package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const batch5Lane1AsanaDefaultBase = "https://app.asana.com/api/1.0"

var batch5Lane1AsanaTool = &Tool{
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

func batch5Lane1AsanaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"asana_list_workspaces": batch5Lane1AsanaAuthFirst(batch5Lane1AsanaListWorkspaces),
		"asana_list_projects":   batch5Lane1AsanaAuthFirst(batch5Lane1AsanaListProjects),
		"asana_list_tasks":      batch5Lane1AsanaAuthFirst(batch5Lane1AsanaListTasks),
		"asana_get_task":        batch5Lane1AsanaAuthFirst(batch5Lane1AsanaGetTask),
		"asana_create_task":     batch5Lane1AsanaAuthFirst(batch5Lane1AsanaCreateTask),
		"asana_search_tasks":    batch5Lane1AsanaAuthFirst(batch5Lane1AsanaSearchTasks),
		"asana_update_task":     batch5Lane1AsanaAuthFirst(batch5Lane1AsanaUpdateTask),
		"asana_add_comment":     batch5Lane1AsanaAuthFirst(batch5Lane1AsanaAddComment),
		"asana_create_subtask":  batch5Lane1AsanaAuthFirst(batch5Lane1AsanaCreateSubtask),
	}
}

// batch5Lane1AsanaAuthFirst enforces upstream's credentials-before-arguments
// order: every Asana tool resolves the PAT before validating required fields.
func batch5Lane1AsanaAuthFirst(fn ActionFunc) ActionFunc {
	return func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		if batch5Lane1ModuleToken(rt, "asana_tool") == "" {
			return batch5Lane1AsanaAuthError(), nil
		}
		return fn(ctx, rt, input)
	}
}

func batch5Lane1AsanaAuthError() map[string]any {
	return map[string]any{
		"error": "ASANA_ACCESS_TOKEN not set",
		"help":  "Create a PAT at https://app.asana.com/0/my-apps",
	}
}

// batch5Lane1AsanaClient resolves the API base and personal access token.
func batch5Lane1AsanaClient(rt *Runtime) (base, token string, ok bool) {
	base = batch5Lane1ModuleBase(rt, "asana_tool")
	if base == "" {
		base = batch5Lane1AsanaDefaultBase
	}
	token = batch5Lane1ModuleToken(rt, "asana_tool")
	return base, token, token != ""
}

// batch5Lane1AsanaRequest performs one Asana API call. Reads and writes have
// different upstream error mappings: writes only special-case 401.
func batch5Lane1AsanaRequest(ctx context.Context, rt *Runtime, method, path string, query url.Values, payload any, write bool) map[string]any {
	base, token, ok := batch5Lane1AsanaClient(rt)
	if !ok {
		return batch5Lane1AsanaAuthError()
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	resp, err := batch5Lane1Do(ctx, rt, method, base+path, query, payload, headers)
	if err != nil {
		if batch5Lane1IsTimeout(ctx, err) {
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
				"error": fmt.Sprintf("Asana API error %d: %s", resp.Status, batch5Lane1Truncate(string(resp.Body), 500)),
			}
		}
	} else {
		switch resp.Status {
		case http.StatusForbidden:
			return map[string]any{"error": "Forbidden: " + batch5Lane1Truncate(string(resp.Body), 300)}
		case http.StatusNotFound:
			return map[string]any{"error": "Not found"}
		case http.StatusOK:
			// Success: fall through to decoding.
		default:
			return map[string]any{
				"error": fmt.Sprintf("Asana API error %d: %s", resp.Status, batch5Lane1Truncate(string(resp.Body), 500)),
			}
		}
	}
	object, err := batch5Lane1Decode(resp.Body)
	if err != nil {
		return map[string]any{"error": "Asana request failed: " + err.Error()}
	}
	return object
}

// batch5Lane1AsanaWrite wraps a request body in Asana's {"data": ...} envelope.
func batch5Lane1AsanaWrite(ctx context.Context, rt *Runtime, method, path string, body map[string]any) map[string]any {
	if body == nil {
		body = map[string]any{}
	}
	return batch5Lane1AsanaRequest(ctx, rt, method, path, nil, map[string]any{"data": body}, true)
}

func batch5Lane1AsanaListWorkspaces(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	data := batch5Lane1AsanaRequest(ctx, rt, http.MethodGet, "/workspaces", nil, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	workspaces := make([]map[string]any, 0)
	for _, rawWorkspace := range batch5Lane1Slice(data["data"]) {
		workspace := batch5Lane1Map(rawWorkspace)
		workspaces = append(workspaces, map[string]any{
			"gid":  batch5Lane1Text(workspace, "gid"),
			"name": batch5Lane1Text(workspace, "name"),
		})
	}
	return map[string]any{"workspaces": workspaces}, nil
}

func batch5Lane1AsanaListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	workspaceGID := stringArg(input, "workspace_gid")
	if workspaceGID == "" {
		return map[string]any{"error": "workspace_gid is required"}, nil
	}
	query := url.Values{}
	query.Set("workspace", workspaceGID)
	query.Set("limit", strconv.Itoa(batch5Lane1Clamp(intArg(input, "limit", 50), 1, 100)))
	query.Set("opt_fields", "name,color,archived,created_at")
	data := batch5Lane1AsanaRequest(ctx, rt, http.MethodGet, "/projects", query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	projects := make([]map[string]any, 0)
	for _, rawProject := range batch5Lane1Slice(data["data"]) {
		project := batch5Lane1Map(rawProject)
		projects = append(projects, map[string]any{
			"gid":      batch5Lane1Text(project, "gid"),
			"name":     batch5Lane1Text(project, "name"),
			"color":    batch5Lane1Text(project, "color"),
			"archived": batch5Lane1Bool(project, "archived"),
		})
	}
	return map[string]any{"projects": projects}, nil
}

func batch5Lane1AsanaListTasks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectGID := stringArg(input, "project_gid")
	workspaceGID := stringArg(input, "workspace_gid")
	if projectGID == "" && workspaceGID == "" {
		return map[string]any{"error": "Either project_gid or workspace_gid is required"}, nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(batch5Lane1Clamp(intArg(input, "limit", 50), 1, 100)))
	query.Set("opt_fields", "name,completed,due_on,assignee.name")
	if projectGID != "" {
		query.Set("project", projectGID)
	} else {
		query.Set("workspace", workspaceGID)
		query.Set("assignee", stringArg(input, "assignee"))
	}
	data := batch5Lane1AsanaRequest(ctx, rt, http.MethodGet, "/tasks", query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	tasks := make([]map[string]any, 0)
	for _, rawTask := range batch5Lane1Slice(data["data"]) {
		task := batch5Lane1Map(rawTask)
		tasks = append(tasks, map[string]any{
			"gid":           batch5Lane1Text(task, "gid"),
			"name":          batch5Lane1Text(task, "name"),
			"completed":     batch5Lane1Bool(task, "completed"),
			"due_on":        batch5Lane1Text(task, "due_on"),
			"assignee_name": batch5Lane1Text(batch5Lane1Map(task["assignee"]), "name"),
		})
	}
	return map[string]any{"tasks": tasks, "count": len(tasks)}, nil
}

func batch5Lane1AsanaGetTask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	taskGID := stringArg(input, "task_gid")
	if taskGID == "" {
		return map[string]any{"error": "task_gid is required"}, nil
	}
	query := url.Values{}
	query.Set("opt_fields", "name,notes,completed,due_on,assignee.name,projects.name,tags.name,created_at,modified_at")
	data := batch5Lane1AsanaRequest(ctx, rt, http.MethodGet, "/tasks/"+taskGID, query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := batch5Lane1Map(data["data"])
	projects := make([]string, 0)
	for _, rawProject := range batch5Lane1Slice(task["projects"]) {
		projects = append(projects, batch5Lane1Text(batch5Lane1Map(rawProject), "name"))
	}
	tags := make([]string, 0)
	for _, rawTag := range batch5Lane1Slice(task["tags"]) {
		tags = append(tags, batch5Lane1Text(batch5Lane1Map(rawTag), "name"))
	}
	return map[string]any{
		"gid":           batch5Lane1Text(task, "gid"),
		"name":          batch5Lane1Text(task, "name"),
		"notes":         batch5Lane1Truncate(batch5Lane1OptionalText(task, "notes"), 500),
		"completed":     batch5Lane1Bool(task, "completed"),
		"due_on":        batch5Lane1Text(task, "due_on"),
		"assignee_name": batch5Lane1Text(batch5Lane1Map(task["assignee"]), "name"),
		"projects":      projects,
		"tags":          tags,
		"created_at":    batch5Lane1Text(task, "created_at"),
		"modified_at":   batch5Lane1Text(task, "modified_at"),
	}, nil
}

func batch5Lane1AsanaCreateTask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	data := batch5Lane1AsanaWrite(ctx, rt, http.MethodPost, "/tasks", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := batch5Lane1Map(data["data"])
	return map[string]any{
		"gid":    batch5Lane1Text(task, "gid"),
		"name":   batch5Lane1Text(task, "name"),
		"status": "created",
	}, nil
}

func batch5Lane1AsanaSearchTasks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	workspaceGID := stringArg(input, "workspace_gid")
	searchText := stringArg(input, "query")
	if workspaceGID == "" || searchText == "" {
		return map[string]any{"error": "workspace_gid and query are required"}, nil
	}
	query := url.Values{}
	query.Set("text", searchText)
	query.Set("limit", strconv.Itoa(batch5Lane1Clamp(intArg(input, "limit", 20), 1, 100)))
	query.Set("opt_fields", "name,completed,due_on")
	data := batch5Lane1AsanaRequest(ctx, rt, http.MethodGet, "/workspaces/"+workspaceGID+"/tasks/search", query, nil, false)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	tasks := make([]map[string]any, 0)
	for _, rawTask := range batch5Lane1Slice(data["data"]) {
		task := batch5Lane1Map(rawTask)
		tasks = append(tasks, map[string]any{
			"gid":       batch5Lane1Text(task, "gid"),
			"name":      batch5Lane1Text(task, "name"),
			"completed": batch5Lane1Bool(task, "completed"),
			"due_on":    batch5Lane1Text(task, "due_on"),
		})
	}
	return map[string]any{"query": searchText, "tasks": tasks}, nil
}

func batch5Lane1AsanaUpdateTask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	if completed, present := batch5Lane1OptionalBool(input, "completed"); present {
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
	data := batch5Lane1AsanaWrite(ctx, rt, http.MethodPut, "/tasks/"+taskGID, body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := batch5Lane1Map(data["data"])
	return map[string]any{
		"gid":       batch5Lane1Text(task, "gid"),
		"name":      batch5Lane1Text(task, "name"),
		"completed": batch5Lane1Bool(task, "completed"),
		"status":    "updated",
	}, nil
}

func batch5Lane1AsanaAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	taskGID := stringArg(input, "task_gid")
	text := stringArg(input, "text")
	if taskGID == "" || text == "" {
		return map[string]any{"error": "task_gid and text are required"}, nil
	}
	data := batch5Lane1AsanaWrite(ctx, rt, http.MethodPost, "/tasks/"+taskGID+"/stories", map[string]any{"text": text})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	story := batch5Lane1Map(data["data"])
	return map[string]any{
		"gid":        batch5Lane1Text(story, "gid"),
		"text":       batch5Lane1Truncate(batch5Lane1OptionalText(story, "text"), 500),
		"created_at": batch5Lane1Text(story, "created_at"),
		"status":     "created",
	}, nil
}

func batch5Lane1AsanaCreateSubtask(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	data := batch5Lane1AsanaWrite(ctx, rt, http.MethodPost, "/tasks/"+parentTaskGID+"/subtasks", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	task := batch5Lane1Map(data["data"])
	return map[string]any{
		"gid":    batch5Lane1Text(task, "gid"),
		"name":   batch5Lane1Text(task, "name"),
		"status": "created",
	}, nil
}

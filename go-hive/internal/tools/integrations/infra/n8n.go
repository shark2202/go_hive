package infra

// n8n: catalog, six workflow/execution actions and the shared response
// handling.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var n8nTool = &Tool{
	ID:          "n8n_tool",
	Name:        "n8n",
	DisplayName: "n8n",
	Summary:     "n8n workflow and execution management (6 actions).",
	Actions: []Action{
		{
			Name:    "n8n_list_workflows",
			Summary: "List n8n workflows with optional filters.",
			Source:  "n8n_tool/n8n_tool.py",
			Params: map[string]any{
				"active": paramDefault("string", ""),
				"tags":   paramDefault("string", ""),
				"name":   paramDefault("string", ""),
				"limit":  paramDefault("integer", 100),
				"cursor": paramDefault("string", ""),
			},
		},
		{
			Name:     "n8n_get_workflow",
			Summary:  "Get details of a specific n8n workflow.",
			Source:   "n8n_tool/n8n_tool.py",
			Required: []string{"workflow_id"},
			Params: map[string]any{
				"workflow_id": param("string"),
			},
		},
		{
			Name:     "n8n_activate_workflow",
			Summary:  "Activate (publish) an n8n workflow.",
			Source:   "n8n_tool/n8n_tool.py",
			Required: []string{"workflow_id"},
			Params: map[string]any{
				"workflow_id": param("string"),
			},
		},
		{
			Name:     "n8n_deactivate_workflow",
			Summary:  "Deactivate an n8n workflow.",
			Source:   "n8n_tool/n8n_tool.py",
			Required: []string{"workflow_id"},
			Params: map[string]any{
				"workflow_id": param("string"),
			},
		},
		{
			Name:    "n8n_list_executions",
			Summary: "List n8n workflow executions with optional filters.",
			Source:  "n8n_tool/n8n_tool.py",
			Params: map[string]any{
				"workflow_id": paramDefault("string", ""),
				"status":      paramDefault("string", ""),
				"limit":       paramDefault("integer", 100),
				"cursor":      paramDefault("string", ""),
			},
		},
		{
			Name:     "n8n_get_execution",
			Summary:  "Get details of a specific n8n execution.",
			Source:   "n8n_tool/n8n_tool.py",
			Required: []string{"execution_id"},
			Params: map[string]any{
				"execution_id": param("string"),
				"include_data": paramDefault("boolean", false),
			},
		},
	},
}

func n8nImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"n8n_list_workflows":      n8nListWorkflows,
		"n8n_get_workflow":        n8nGetWorkflow,
		"n8n_activate_workflow":   n8nActivateWorkflow,
		"n8n_deactivate_workflow": n8nDeactivateWorkflow,
		"n8n_list_executions":     n8nListExecutions,
		"n8n_get_execution":       n8nGetExecution,
	}
}

// n8nConfig mirrors upstream _get_creds.
func n8nConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	apiKey := moduleToken(rt, "n8n_tool")
	baseURL := endpoint(rt, "n8n_tool", "N8N_BASE_URL", "")
	if apiKey == "" || baseURL == "" {
		return "", nil, map[string]any{
			"error": "n8n credentials not configured",
			"help":  "Set N8N_API_KEY and N8N_BASE_URL environment variables or configure via credential store",
		}
	}
	return strings.TrimRight(baseURL, "/"), map[string]string{
		"X-N8N-API-KEY": apiKey,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}, nil
}

// n8nResult mirrors upstream _handle_response.
func n8nResult(resp *httpResponse) map[string]any {
	switch resp.Status {
	case http.StatusNoContent:
		return map[string]any{"success": true}
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid n8n API key"}
	case http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions for this n8n resource"}
	case http.StatusNotFound:
		return map[string]any{"error": "n8n resource not found"}
	}
	if resp.Status >= 400 {
		detail := string(resp.Body)
		if body, ok := jsonObject(resp.Body); ok {
			if message, present := body["message"].(string); present {
				detail = message
			}
		}
		return map[string]any{"error": fmt.Sprintf("n8n API error (HTTP %d): %s", resp.Status, detail)}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": fmt.Sprintf("n8n API error (HTTP %d): invalid JSON response", resp.Status)}
	}
	return data
}

// n8nTransport renders the upstream network-error split.
func n8nTransport(ctx context.Context, err error) map[string]any {
	if isTimeout(ctx, err) {
		return map[string]any{"error": "Request timed out"}
	}
	return map[string]any{"error": "Network error: " + err.Error()}
}

// n8nCall performs one request and merges transport/status errors.
func n8nCall(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, params url.Values) (map[string]any, bool) {
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	resp, err := doJSON(ctx, rt, method, endpoint, headers, payload, defaultTimeout)
	if err != nil {
		return n8nTransport(ctx, err), false
	}
	result := n8nResult(resp)
	if failed(result) {
		return result, false
	}
	return result, true
}

// n8nTags collects tag names.
func n8nTags(value any) []any {
	tags := make([]any, 0)
	for _, raw := range listOrEmpty(value) {
		tags = append(tags, stringOr(asMap(raw), "name", ""))
	}
	return tags
}

func n8nListWorkflows(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := n8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(minInt(intArg(input, "limit", 100), 250))}}
	for _, name := range []string{"active", "tags", "name", "cursor"} {
		if value := stringArg(input, name); value != "" {
			params.Set(name, value)
		}
	}
	result, ok := n8nCall(ctx, rt, http.MethodGet, addPrefix(base, "/api/v1/workflows"), headers, nil, params)
	if !ok {
		return result, nil
	}
	workflows := listOrEmpty(result["data"])
	out := make([]any, 0, len(workflows))
	for _, raw := range workflows {
		workflow := asMap(raw)
		out = append(out, map[string]any{
			"id":         workflow["id"],
			"name":       workflow["name"],
			"active":     workflow["active"],
			"created_at": workflow["createdAt"],
			"updated_at": workflow["updatedAt"],
			"tags":       n8nTags(workflow["tags"]),
			"node_count": len(listOrEmpty(workflow["nodes"])),
		})
	}
	output := map[string]any{"count": len(out), "workflows": out}
	if cursor := stringOr(result, "nextCursor", ""); cursor != "" {
		output["next_cursor"] = cursor
	}
	return output, nil
}

func n8nGetWorkflow(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := n8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workflowID := stringArg(input, "workflow_id")
	if workflowID == "" {
		return map[string]any{"error": "workflow_id is required"}, nil
	}
	path := "/api/v1/workflows/" + pathEscape(workflowID)
	result, ok := n8nCall(ctx, rt, http.MethodGet, addPrefix(base, path), headers, nil, nil)
	if !ok {
		return result, nil
	}
	nodes := make([]any, 0)
	for _, raw := range listOrEmpty(result["nodes"]) {
		node := asMap(raw)
		nodes = append(nodes, map[string]any{
			"name":     node["name"],
			"type":     node["type"],
			"position": node["position"],
		})
	}
	return map[string]any{
		"id":         result["id"],
		"name":       result["name"],
		"active":     result["active"],
		"created_at": result["createdAt"],
		"updated_at": result["updatedAt"],
		"tags":       n8nTags(result["tags"]),
		"nodes":      nodes,
		"node_count": len(nodes),
	}, nil
}

// n8nToggle implements activate/deactivate, which differ only by
// the upstream path suffix.
func n8nToggle(ctx context.Context, rt *Runtime, input map[string]any, suffix string) (any, error) {
	base, headers, failure := n8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workflowID := stringArg(input, "workflow_id")
	if workflowID == "" {
		return map[string]any{"error": "workflow_id is required"}, nil
	}
	path := "/api/v1/workflows/" + pathEscape(workflowID) + suffix
	result, ok := n8nCall(ctx, rt, http.MethodPost, addPrefix(base, path), headers, nil, nil)
	if !ok {
		return result, nil
	}
	return map[string]any{
		"id":     result["id"],
		"name":   result["name"],
		"active": result["active"],
	}, nil
}

func n8nActivateWorkflow(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return n8nToggle(ctx, rt, input, "/activate")
}

func n8nDeactivateWorkflow(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return n8nToggle(ctx, rt, input, "/deactivate")
}

func n8nListExecutions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := n8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(minInt(intArg(input, "limit", 100), 250))}}
	if workflowID := stringArg(input, "workflow_id"); workflowID != "" {
		params.Set("workflowId", workflowID)
	}
	if status := stringArg(input, "status"); status != "" {
		params.Set("status", status)
	}
	if cursor := stringArg(input, "cursor"); cursor != "" {
		params.Set("cursor", cursor)
	}
	result, ok := n8nCall(ctx, rt, http.MethodGet, addPrefix(base, "/api/v1/executions"), headers, nil, params)
	if !ok {
		return result, nil
	}
	executions := listOrEmpty(result["data"])
	out := make([]any, 0, len(executions))
	for _, raw := range executions {
		execution := asMap(raw)
		out = append(out, map[string]any{
			"id":          execution["id"],
			"workflow_id": execution["workflowId"],
			"status":      execution["status"],
			"mode":        execution["mode"],
			"finished":    execution["finished"],
			"started_at":  execution["startedAt"],
			"stopped_at":  execution["stoppedAt"],
		})
	}
	output := map[string]any{"count": len(out), "executions": out}
	if cursor := stringOr(result, "nextCursor", ""); cursor != "" {
		output["next_cursor"] = cursor
	}
	return output, nil
}

func n8nGetExecution(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := n8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	executionID := stringArg(input, "execution_id")
	if executionID == "" {
		return map[string]any{"error": "execution_id is required"}, nil
	}
	includeData := boolArg(input, "include_data", false)
	var params url.Values
	if includeData {
		params = url.Values{"includeData": []string{"true"}}
	}
	path := "/api/v1/executions/" + pathEscape(executionID)
	result, ok := n8nCall(ctx, rt, http.MethodGet, addPrefix(base, path), headers, nil, params)
	if !ok {
		return result, nil
	}
	output := map[string]any{
		"id":               result["id"],
		"workflow_id":      result["workflowId"],
		"status":           result["status"],
		"mode":             result["mode"],
		"finished":         result["finished"],
		"started_at":       result["startedAt"],
		"stopped_at":       result["stoppedAt"],
		"retry_of":         result["retryOf"],
		"retry_success_id": result["retrySuccessId"],
	}
	if includeData {
		if data, present := result["data"]; present {
			output["data"] = data
		}
	}
	return output, nil
}

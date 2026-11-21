package infra

// Terraform Cloud: catalog, five API v2 actions and the JSON:API
// helpers (config, workspace/run extractors, pagination).

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var terraformTool = &Tool{
	ID:          "terraform_tool",
	Name:        "terraform",
	DisplayName: "Terraform Cloud",
	Summary:     "Terraform Cloud / HCP Terraform workspace and run management (5 actions).",
	Actions: []Action{
		{
			Name:     "terraform_list_workspaces",
			Summary:  "List workspaces in a Terraform Cloud organization.",
			Source:   "terraform_tool/terraform_tool.py",
			Required: []string{"organization"},
			Params: map[string]any{
				"organization": param("string"),
				"search":       paramDefault("string", ""),
				"page_size":    paramDefault("integer", 20),
				"page_number":  paramDefault("integer", 1),
			},
		},
		{
			Name:     "terraform_get_workspace",
			Summary:  "Get details of a specific Terraform Cloud workspace.",
			Source:   "terraform_tool/terraform_tool.py",
			Required: []string{"workspace_id"},
			Params: map[string]any{
				"workspace_id": param("string"),
			},
		},
		{
			Name:     "terraform_list_runs",
			Summary:  "List runs for a Terraform Cloud workspace.",
			Source:   "terraform_tool/terraform_tool.py",
			Required: []string{"workspace_id"},
			Params: map[string]any{
				"workspace_id": param("string"),
				"status":       paramDefault("string", ""),
				"page_size":    paramDefault("integer", 20),
				"page_number":  paramDefault("integer", 1),
			},
		},
		{
			Name:     "terraform_get_run",
			Summary:  "Get details of a specific Terraform Cloud run.",
			Source:   "terraform_tool/terraform_tool.py",
			Required: []string{"run_id"},
			Params: map[string]any{
				"run_id": param("string"),
			},
		},
		{
			Name:     "terraform_create_run",
			Summary:  "Trigger a new run in a Terraform Cloud workspace.",
			Source:   "terraform_tool/terraform_tool.py",
			Required: []string{"workspace_id"},
			Params: map[string]any{
				"workspace_id": param("string"),
				"message":      paramDefault("string", "Triggered via API"),
				"auto_apply":   paramDefault("boolean", false),
				"is_destroy":   paramDefault("boolean", false),
				"plan_only":    paramDefault("boolean", false),
			},
		},
	},
}

func terraformImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"terraform_list_workspaces": terraformListWorkspaces,
		"terraform_get_workspace":   terraformGetWorkspace,
		"terraform_list_runs":       terraformListRuns,
		"terraform_get_run":         terraformGetRun,
		"terraform_create_run":      terraformCreateRun,
	}
}

// terraformConfig mirrors upstream _get_config: Bearer token plus
// the JSON:API content type, with the base URL defaulting to Terraform Cloud.
func terraformConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	token := moduleToken(rt, "terraform_tool")
	if token == "" {
		return "", nil, map[string]any{
			"error": "TFC_TOKEN is required",
			"help":  "Set TFC_TOKEN environment variable",
		}
	}
	root := endpoint(rt, "terraform_tool", "TFC_URL", defaultTerraformURL)
	base := strings.TrimRight(root, "/")
	if !strings.HasSuffix(base, "/api/v2") {
		base += "/api/v2"
	}
	return base, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/vnd.api+json",
	}, nil
}

// terraformWorkspace mirrors upstream _extract_workspace.
func terraformWorkspace(raw any) map[string]any {
	workspace := asMap(raw)
	attrs := asMap(workspace["attributes"])
	return map[string]any{
		"id":                workspace["id"],
		"name":              attrs["name"],
		"terraform_version": attrs["terraform-version"],
		"execution_mode":    attrs["execution-mode"],
		"auto_apply":        attrs["auto-apply"],
		"locked":            attrs["locked"],
		"resource_count":    attrs["resource-count"],
		"created_at":        attrs["created-at"],
		"updated_at":        attrs["updated-at"],
	}
}

// terraformRun mirrors upstream _extract_run.
func terraformRun(raw any) map[string]any {
	run := asMap(raw)
	attrs := asMap(run["attributes"])
	return map[string]any{
		"id":             run["id"],
		"status":         attrs["status"],
		"message":        attrs["message"],
		"source":         attrs["source"],
		"trigger_reason": attrs["trigger-reason"],
		"is_destroy":     attrs["is-destroy"],
		"plan_only":      attrs["plan-only"],
		"has_changes":    attrs["has-changes"],
		"auto_apply":     attrs["auto-apply"],
		"created_at":     attrs["created-at"],
	}
}

// terraformPagination mirrors upstream meta.pagination reads.
func terraformPagination(data map[string]any) (any, any) {
	pagination := asMap(asMap(data["meta"])["pagination"])
	return pagination["total-count"], pagination["total-pages"]
}

func terraformListWorkspaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := terraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	organization := stringArg(input, "organization")
	if organization == "" {
		return map[string]any{"error": "organization is required"}, nil
	}
	params := url.Values{
		"page[size]":   []string{strconv.Itoa(minInt(intArg(input, "page_size", 20), 100))},
		"page[number]": []string{strconv.Itoa(intArg(input, "page_number", 1))},
	}
	if search := stringArg(input, "search"); search != "" {
		params.Set("search[name]", search)
	}
	path := "/organizations/" + pathEscape(organization) + "/workspaces"
	data, ok := getJSON(ctx, rt, "Terraform", addPrefix(base, path), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	workspaces := listOrEmpty(data["data"])
	out := make([]any, 0, len(workspaces))
	for _, raw := range workspaces {
		out = append(out, terraformWorkspace(raw))
	}
	totalCount, totalPages := terraformPagination(data)
	return map[string]any{
		"count":       len(out),
		"total_count": totalCount,
		"total_pages": totalPages,
		"workspaces":  out,
	}, nil
}

func terraformGetWorkspace(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := terraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	if workspaceID == "" {
		return map[string]any{"error": "workspace_id is required"}, nil
	}
	path := "/workspaces/" + pathEscape(workspaceID)
	data, ok := getJSON(ctx, rt, "Terraform", addPrefix(base, path), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	workspace := asMap(data["data"])
	result := terraformWorkspace(workspace)
	attrs := asMap(workspace["attributes"])
	result["description"] = attrs["description"]
	result["vcs_repo"] = attrs["vcs-repo"]
	result["working_directory"] = attrs["working-directory"]
	return result, nil
}

func terraformListRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := terraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	if workspaceID == "" {
		return map[string]any{"error": "workspace_id is required"}, nil
	}
	params := url.Values{
		"page[size]":   []string{strconv.Itoa(minInt(intArg(input, "page_size", 20), 100))},
		"page[number]": []string{strconv.Itoa(intArg(input, "page_number", 1))},
	}
	if status := stringArg(input, "status"); status != "" {
		params.Set("filter[status]", status)
	}
	path := "/workspaces/" + pathEscape(workspaceID) + "/runs"
	data, ok := getJSON(ctx, rt, "Terraform", addPrefix(base, path), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	runs := listOrEmpty(data["data"])
	out := make([]any, 0, len(runs))
	for _, raw := range runs {
		out = append(out, terraformRun(raw))
	}
	totalCount, totalPages := terraformPagination(data)
	return map[string]any{
		"count":       len(out),
		"total_count": totalCount,
		"total_pages": totalPages,
		"runs":        out,
	}, nil
}

func terraformGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := terraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	path := "/runs/" + pathEscape(runID)
	data, ok := getJSON(ctx, rt, "Terraform", addPrefix(base, path), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	run := asMap(data["data"])
	result := terraformRun(run)
	attrs := asMap(run["attributes"])
	// Upstream reuses this key for the plan-queued timestamp.
	result["plan_and_apply"] = map[string]any{
		"resource_additions": asMap(attrs["status-timestamps"])["plan-queued-at"],
	}
	result["permissions"] = attrs["permissions"]
	return result, nil
}

func terraformCreateRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := terraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	if workspaceID == "" {
		return map[string]any{"error": "workspace_id is required"}, nil
	}
	payload := map[string]any{
		"data": map[string]any{
			"type": "runs",
			"attributes": map[string]any{
				"message":    stringArg(input, "message"),
				"auto-apply": boolArg(input, "auto_apply", false),
				"is-destroy": boolArg(input, "is_destroy", false),
				"plan-only":  boolArg(input, "plan_only", false),
			},
			"relationships": map[string]any{
				"workspace": map[string]any{
					"data": map[string]any{"type": "workspaces", "id": workspaceID},
				},
			},
		},
	}
	resp, err := doJSON(ctx, rt, http.MethodPost, addPrefix(base, "/runs"), headers, payload, defaultTimeout)
	if err != nil {
		return transportError(ctx, "Terraform", err), nil
	}
	if resp.Status >= 400 {
		return httpError(resp), nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Terraform API error: invalid JSON response"}, nil
	}
	return terraformRun(data["data"]), nil
}

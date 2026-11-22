package devops

// Vercel: catalog, seven REST actions and the shared call helper.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// vercelTool publishes the seven Vercel REST actions.
var vercelTool = &Tool{
	ID:          "vercel_tool",
	Name:        "vercel",
	DisplayName: "Vercel",
	Summary:     "Vercel deployments, projects, domains and env vars (7 actions).",
	Actions: []Action{
		{
			Name:    "vercel_list_deployments",
			Summary: "List Vercel deployments, optionally filtered by project.",
			Source:  "vercel_tool/vercel_tool.py",
			Params: map[string]any{
				"project_id": integrationkit.ParamDefault("string", ""),
				"limit":      integrationkit.ParamDefault("integer", 20),
				"state":      integrationkit.ParamDefault("string", ""),
			},
		},
		{
			Name:     "vercel_get_deployment",
			Summary:  "Get details of a specific Vercel deployment.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"deployment_id"},
			Params: map[string]any{
				"deployment_id": integrationkit.Param("string"),
			},
		},
		{
			Name:    "vercel_list_projects",
			Summary: "List all Vercel projects.",
			Source:  "vercel_tool/vercel_tool.py",
			Params: map[string]any{
				"limit": integrationkit.ParamDefault("integer", 20),
			},
		},
		{
			Name:     "vercel_get_project",
			Summary:  "Get details of a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
			},
		},
		{
			Name:     "vercel_list_project_domains",
			Summary:  "List domains configured for a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
			},
		},
		{
			Name:     "vercel_list_env_vars",
			Summary:  "List environment variables for a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
			},
		},
		{
			Name:     "vercel_create_env_var",
			Summary:  "Create an environment variable for a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id", "key", "value"},
			Params: map[string]any{
				"project_id": integrationkit.Param("string"),
				"key":        integrationkit.Param("string"),
				"value":      integrationkit.Param("string"),
				"target":     integrationkit.ParamDefault("string", "production,preview,development"),
				"env_type":   integrationkit.ParamDefault("string", "encrypted"),
			},
		},
	},
}

func vercelImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"vercel_list_deployments":     vercelListDeployments,
		"vercel_get_deployment":       vercelGetDeployment,
		"vercel_list_projects":        vercelListProjects,
		"vercel_get_project":          vercelGetProject,
		"vercel_list_project_domains": vercelListProjectDomains,
		"vercel_list_env_vars":        vercelListEnvVars,
		"vercel_create_env_var":       vercelCreateEnvVar,
	}
}

// ----------------------------------------------------------------- Vercel ---

// vercelCall performs one Vercel API call and maps HTTP failures to
// the upstream `_get` / `_post` error dicts.
func vercelCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := moduleToken(rt, "vercel_tool")
	if token == "" {
		return map[string]any{
			"error": "VERCEL_TOKEN not set",
			"help":  "Get a token at https://vercel.com/account/tokens",
		}, false
	}
	target := moduleURL(rt, "vercel_tool") + "/" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	reply, err := jsonRequest(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Vercel timed out"}, false
		}
		return map[string]any{"error": "Vercel request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your VERCEL_TOKEN."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden: " + truncateText(string(reply.Body), 300)}, false
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Vercel API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Vercel request failed: invalid JSON response"}, false
	}
	return value, true
}

func vercelListDeployments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit": {strconv.Itoa(clampInt(integrationkit.IntArg(input, "limit", 20), 1, 100))},
	}
	if projectID := integrationkit.StringArg(input, "project_id"); projectID != "" {
		query.Set("projectId", projectID)
	}
	if state := integrationkit.StringArg(input, "state"); state != "" {
		query.Set("state", state)
	}
	value, ok := vercelCall(ctx, rt, http.MethodGet, "v6/deployments", query, nil)
	if !ok {
		return value, nil
	}
	object := asMap(value)
	deployments := make([]map[string]any, 0)
	for _, raw := range asSlice(object["deployments"]) {
		deployment := asMap(raw)
		deployments = append(deployments, map[string]any{
			"uid":     asString(deployment["uid"]),
			"name":    asString(deployment["name"]),
			"url":     asString(deployment["url"]),
			"state":   asString(deployment["state"]),
			"created": asNumber(deployment["created"]),
			"target":  asString(deployment["target"]),
		})
	}
	return map[string]any{"deployments": deployments}, nil
}

func vercelGetDeployment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	deploymentID := integrationkit.StringArg(input, "deployment_id")
	if deploymentID == "" {
		return map[string]any{"error": "deployment_id is required"}, nil
	}
	value, ok := vercelCall(
		ctx, rt, http.MethodGet, "v13/deployments/"+deploymentID, nil, nil,
	)
	if !ok {
		return value, nil
	}
	deployment := asMap(value)
	return map[string]any{
		"uid":     asString(deployment["id"]),
		"name":    asString(deployment["name"]),
		"url":     asString(deployment["url"]),
		"state":   asString(deployment["readyState"]),
		"target":  asString(deployment["target"]),
		"created": asNumber(deployment["createdAt"]),
		"ready":   asNumber(deployment["ready"]),
		"creator": asString(asMap(deployment["creator"])["username"]),
		"meta":    asMap(deployment["meta"]),
	}, nil
}

func vercelListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit": {strconv.Itoa(clampInt(integrationkit.IntArg(input, "limit", 20), 1, 100))},
	}
	value, ok := vercelCall(ctx, rt, http.MethodGet, "v9/projects", query, nil)
	if !ok {
		return value, nil
	}
	projects := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["projects"]) {
		project := asMap(raw)
		latestURL := ""
		latest := asSlice(project["latestDeployments"])
		if len(latest) > 0 {
			latestURL = asString(asMap(latest[0])["url"])
		}
		projects = append(projects, map[string]any{
			"id":                  asString(project["id"]),
			"name":                asString(project["name"]),
			"framework":           asString(project["framework"]),
			"updatedAt":           asNumber(project["updatedAt"]),
			"latestDeploymentUrl": latestURL,
		})
	}
	return map[string]any{"projects": projects}, nil
}

func vercelGetProject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	value, ok := vercelCall(ctx, rt, http.MethodGet, "v9/projects/"+projectID, nil, nil)
	if !ok {
		return value, nil
	}
	project := asMap(value)
	return map[string]any{
		"id":          asString(project["id"]),
		"name":        asString(project["name"]),
		"framework":   asString(project["framework"]),
		"nodeVersion": asString(project["nodeVersion"]),
		"updatedAt":   asNumber(project["updatedAt"]),
		"env_count":   len(asSlice(project["env"])),
	}, nil
}

func vercelListProjectDomains(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	value, ok := vercelCall(
		ctx, rt, http.MethodGet, "v9/projects/"+projectID+"/domains", nil, nil,
	)
	if !ok {
		return value, nil
	}
	domains := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["domains"]) {
		domain := asMap(raw)
		domains = append(domains, map[string]any{
			"name":      asString(domain["name"]),
			"redirect":  asString(domain["redirect"]),
			"gitBranch": asString(domain["gitBranch"]),
			"verified":  asBool(domain["verified"], false),
		})
	}
	return map[string]any{"project_id": projectID, "domains": domains}, nil
}

func vercelListEnvVars(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	value, ok := vercelCall(
		ctx, rt, http.MethodGet, "v9/projects/"+projectID+"/env", nil, nil,
	)
	if !ok {
		return value, nil
	}
	envVars := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["envs"]) {
		envVar := asMap(raw)
		envVars = append(envVars, map[string]any{
			"id":     asString(envVar["id"]),
			"key":    asString(envVar["key"]),
			"target": stringList(envVar["target"]),
			"type":   asString(envVar["type"]),
		})
	}
	return map[string]any{"project_id": projectID, "env_vars": envVars}, nil
}

func vercelCreateEnvVar(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := integrationkit.StringArg(input, "project_id")
	key := integrationkit.StringArg(input, "key")
	value := integrationkit.StringArg(input, "value")
	if projectID == "" || key == "" || value == "" {
		return map[string]any{"error": "project_id, key, and value are required"}, nil
	}
	target := integrationkit.StringArg(input, "target")
	if target == "" {
		target = "production,preview,development"
	}
	envType := integrationkit.StringArg(input, "env_type")
	if envType == "" {
		envType = "encrypted"
	}
	targets := make([]string, 0)
	for _, part := range strings.Split(target, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			targets = append(targets, trimmed)
		}
	}
	body := map[string]any{"key": key, "value": value, "target": targets, "type": envType}
	created, ok := vercelCall(
		ctx, rt, http.MethodPost, "v10/projects/"+projectID+"/env", nil, body,
	)
	if !ok {
		return created, nil
	}
	return map[string]any{
		"id":     asString(asMap(created)["id"]),
		"key":    key,
		"status": "created",
	}, nil
}

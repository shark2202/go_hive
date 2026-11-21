package ai

// Weights & Biases experiment tracking over the GraphQL API.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ------------------------------------------------------------------- wandb ---

var wandbTool = &Tool{
	ID:          "wandb_tool",
	Name:        "wandb",
	DisplayName: "Weights & Biases",
	Summary:     "Weights & Biases experiment tracking over the GraphQL API (6 actions).",
	Actions: []Action{
		{
			Name:     "wandb_list_projects",
			Summary:  "List all projects for a Weights & Biases entity (user or organization).",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity"},
			Params: map[string]any{
				"entity": param("string"),
			},
		},
		{
			Name:     "wandb_list_runs",
			Summary:  "List runs in a Weights & Biases project.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project"},
			Params: map[string]any{
				"entity":   param("string"),
				"project":  param("string"),
				"filters":  paramDefault("string", ""),
				"per_page": paramDefault("integer", 50),
			},
		},
		{
			Name:     "wandb_get_run",
			Summary:  "Get details of a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":  param("string"),
				"project": param("string"),
				"run_id":  param("string"),
			},
		},
		{
			Name:     "wandb_get_run_metrics",
			Summary:  "Get sampled metrics history for a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":      param("string"),
				"project":     param("string"),
				"run_id":      param("string"),
				"metric_keys": paramDefault("string", ""),
			},
		},
		{
			Name:     "wandb_list_artifacts",
			Summary:  "List artifacts logged by a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":  param("string"),
				"project": param("string"),
				"run_id":  param("string"),
			},
		},
		{
			Name:     "wandb_get_summary",
			Summary:  "Get summary metrics for a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":  param("string"),
				"project": param("string"),
				"run_id":  param("string"),
			},
		},
	},
}

func wandbImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"wandb_list_projects":   wandbListProjects,
		"wandb_list_runs":       wandbListRuns,
		"wandb_get_run":         wandbGetRun,
		"wandb_get_run_metrics": wandbGetRunMetrics,
		"wandb_list_artifacts":  wandbListArtifacts,
		"wandb_get_summary":     wandbGetSummary,
	}
}

// wandbAuth returns the API key or the upstream credentials error.
func wandbAuth(rt *Runtime) (string, map[string]any) {
	token := moduleToken(rt, "wandb_tool")
	if token == "" {
		return "", map[string]any{
			"error": "Weights & Biases credentials not configured",
			"help":  "Set WANDB_API_KEY environment variable or configure via credential store. Get your API key at https://wandb.ai/authorize",
		}
	}
	return token, nil
}

// wandbCall executes one GraphQL query and unwraps the data
// envelope the way _graphql does. The boolean reports success.
func wandbCall(
	ctx context.Context,
	rt *Runtime,
	token, query string,
	variables map[string]any,
) (map[string]any, bool) {
	if variables == nil {
		variables = map[string]any{}
	}
	target := moduleBaseURL(rt, "wandb_tool")
	if target == "" {
		target = wandbEndpoint
	}
	reply, err := doRequest(
		ctx, rt, http.MethodPost, target,
		map[string]string{
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
		},
		map[string]any{"query": query, "variables": variables},
	)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Weights & Biases API key"}, false
	case http.StatusForbidden:
		return map[string]any{
			"error": "Insufficient permissions for this Weights & Biases resource",
		}, false
	}
	if reply.Status >= 400 {
		detail := string(reply.Body)
		if payload, ok := decodeObject(reply.Body); ok {
			if errorsList := asSlice(payload["errors"]); len(errorsList) > 0 {
				if message := asString(asMap(errorsList[0])["message"]); message != "" {
					detail = message
				}
			}
		}
		return map[string]any{
			"error": fmt.Sprintf(
				"Weights & Biases API error (HTTP %d): %s", reply.Status, detail,
			),
		}, false
	}
	payload, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Network error: invalid JSON response"}, false
	}
	if errorsList := asSlice(payload["errors"]); len(errorsList) > 0 {
		message := asString(asMap(errorsList[0])["message"])
		if message == "" {
			message = fmt.Sprint(payload["errors"])
		}
		return map[string]any{"error": "Weights & Biases GraphQL error: " + message}, false
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data == nil {
		return map[string]any{}, true
	}
	return data, true
}

// wandbObject decodes a JSONString field, defaulting to {}.
func wandbObject(value any) map[string]any {
	text, ok := value.(string)
	if !ok {
		return map[string]any{}
	}
	decoded, ok := decodeObject([]byte(text))
	if !ok {
		return map[string]any{}
	}
	return decoded
}

func wandbListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := wandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	entity := stringArg(input, "entity")
	query := `
        query ListProjects($entity: String!) {
          projects(entityName: $entity) {
            edges {
              node {
                name
                description
                createdAt
              }
            }
          }
        }
        `
	data, ok := wandbCall(ctx, rt, token, query, map[string]any{"entity": entity})
	if !ok {
		return data, nil
	}
	edges := asSlice(asMap(data["projects"])["edges"])
	projects := make([]map[string]any, 0, len(edges))
	for _, raw := range edges {
		node := asMap(asMap(raw)["node"])
		projects = append(projects, map[string]any{
			"name":        asString(node["name"]),
			"description": asString(node["description"]),
			"created_at":  asString(node["createdAt"]),
		})
	}
	return map[string]any{"entity": entity, "projects": projects}, nil
}

func wandbListRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	var parsedFilters any
	if raw := stringArg(input, "filters"); raw != "" {
		decoded, ok := decodeAny([]byte(raw))
		if !ok {
			return map[string]any{"error": "filters must be a valid JSON string"}, nil
		}
		parsedFilters = decoded
	}
	token, problem := wandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	perPage := intArg(input, "per_page", 50)
	query := `
        query ListRuns($project: String!, $entity: String!, $perPage: Int!, $filters: JSONString) {
          project(name: $project, entityName: $entity) {
            runs(first: $perPage, filters: $filters) {
              edges {
                node {
                  name
                  id
                  state
                  createdAt
                  config
                  summaryMetrics
                }
              }
            }
          }
        }
        `
	variables := map[string]any{"project": project, "entity": entity, "perPage": perPage}
	if parsedFilters != nil {
		variables["filters"] = parsedFilters
	}
	data, ok := wandbCall(ctx, rt, token, query, variables)
	if !ok {
		return data, nil
	}
	edges := asSlice(
		asMap(asMap(data["project"])["runs"])["edges"],
	)
	runs := make([]map[string]any, 0, len(edges))
	for _, raw := range edges {
		node := asMap(asMap(raw)["node"])
		runs = append(runs, map[string]any{
			"id":           asString(node["name"]),
			"display_name": asString(node["id"]),
			"state":        asString(node["state"]),
			"created_at":   asString(node["createdAt"]),
			"config":       wandbObject(node["config"]),
		})
	}
	return map[string]any{"entity": entity, "project": project, "runs": runs}, nil
}

func wandbGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	token, problem := wandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	query := `
        query GetRun($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              name
              id
              state
              createdAt
              config
              summaryMetrics
              tags
              notes
            }
          }
        }
        `
	data, ok := wandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := asMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	return map[string]any{
		"id":           asString(node["name"]),
		"display_name": asString(node["id"]),
		"state":        asString(node["state"]),
		"created_at":   asString(node["createdAt"]),
		"config":       wandbObject(node["config"]),
		"summary":      wandbObject(node["summaryMetrics"]),
		"tags":         anyList(node["tags"]),
		"notes":        asString(node["notes"]),
	}, nil
}

func wandbGetRunMetrics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := wandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	metricKeys := stringArg(input, "metric_keys")
	if metricKeys == "" {
		return map[string]any{
			"error": "metric_keys is required (comma-separated, e.g. 'loss,accuracy')",
		}, nil
	}
	keys := splitCSV(metricKeys)
	if len(keys) == 0 {
		return map[string]any{"error": "metric_keys must include at least one non-empty key"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	specs := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		specs = append(specs, map[string]any{"key": key})
	}
	encodedSpecs, err := json.Marshal(specs)
	if err != nil {
		return map[string]any{"error": "metric_keys must include at least one non-empty key"}, nil
	}
	query := fmt.Sprintf(`
        query GetRunMetrics($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              sampledHistory(specs: %s)
            }
          }
        }
        `, string(encodedSpecs))
	data, ok := wandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := asMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	return map[string]any{
		"run_id":      runID,
		"metric_keys": keys,
		"history":     anyList(node["sampledHistory"]),
	}, nil
}

func wandbListArtifacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	token, problem := wandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	query := `
        query ListArtifacts($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              outputArtifacts {
                edges {
                  node {
                    name
                    type
                    description
                    createdAt
                  }
                }
              }
            }
          }
        }
        `
	data, ok := wandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := asMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	edges := asSlice(asMap(node["outputArtifacts"])["edges"])
	artifacts := make([]map[string]any, 0, len(edges))
	for _, raw := range edges {
		artifact := asMap(asMap(raw)["node"])
		artifacts = append(artifacts, map[string]any{
			"name":        asString(artifact["name"]),
			"type":        asString(artifact["type"]),
			"description": asString(artifact["description"]),
			"created_at":  asString(artifact["createdAt"]),
		})
	}
	return map[string]any{"run_id": runID, "artifacts": artifacts}, nil
}

func wandbGetSummary(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	token, problem := wandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	query := `
        query GetSummary($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              summaryMetrics
            }
          }
        }
        `
	data, ok := wandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := asMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	summary := map[string]any{}
	for key, value := range wandbObject(node["summaryMetrics"]) {
		if strings.HasPrefix(key, "_") {
			continue
		}
		summary[key] = value
	}
	return map[string]any{"run_id": runID, "summary": summary}, nil
}

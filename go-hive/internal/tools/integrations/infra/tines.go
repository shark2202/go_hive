package infra

// Tines: catalog and five read-only story/action/log actions.

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

var tinesTool = &Tool{
	ID:          "tines_tool",
	Name:        "tines",
	DisplayName: "Tines",
	Summary:     "Tines security automation stories, actions and logs (5 actions).",
	Actions: []Action{
		{
			Name:    "tines_list_stories",
			Summary: "List Tines stories (workflows).",
			Source:  "tines_tool/tines_tool.py",
			Params: map[string]any{
				"team_id":  paramDefault("integer", 0),
				"search":   paramDefault("string", ""),
				"per_page": paramDefault("integer", 20),
			},
		},
		{
			Name:     "tines_get_story",
			Summary:  "Get details of a specific Tines story.",
			Source:   "tines_tool/tines_tool.py",
			Required: []string{"story_id"},
			Params: map[string]any{
				"story_id": param("integer"),
			},
		},
		{
			Name:    "tines_list_actions",
			Summary: "List Tines actions (agents) in stories.",
			Source:  "tines_tool/tines_tool.py",
			Params: map[string]any{
				"story_id":    paramDefault("integer", 0),
				"action_type": paramDefault("string", ""),
				"per_page":    paramDefault("integer", 20),
			},
		},
		{
			Name:     "tines_get_action",
			Summary:  "Get details of a specific Tines action (agent).",
			Source:   "tines_tool/tines_tool.py",
			Required: []string{"action_id"},
			Params: map[string]any{
				"action_id": param("integer"),
			},
		},
		{
			Name:     "tines_get_action_logs",
			Summary:  "Get logs for a Tines action.",
			Source:   "tines_tool/tines_tool.py",
			Required: []string{"action_id"},
			Params: map[string]any{
				"action_id": param("integer"),
				"level":     paramDefault("integer", 0),
				"per_page":  paramDefault("integer", 20),
			},
		},
	},
}

func tinesImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"tines_list_stories":    tinesListStories,
		"tines_get_story":       tinesGetStory,
		"tines_list_actions":    tinesListActions,
		"tines_get_action":      tinesGetAction,
		"tines_get_action_logs": tinesGetActionLogs,
	}
}

// tinesConfig mirrors upstream _get_config: TINES_DOMAIN plus
// Bearer API key against https://<domain>/api/v1.
func tinesConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	apiKey := moduleToken(rt, "tines_tool")
	root := moduleBaseURL(rt, "tines_tool")
	if root == "" {
		domain := strings.TrimRight(strings.TrimSpace(runtimeEnv(rt, "TINES_DOMAIN")), "/")
		if domain != "" && !strings.Contains(domain, "://") {
			domain = "https://" + domain
		}
		root = domain
	}
	if root == "" || apiKey == "" {
		return "", nil, map[string]any{
			"error": "TINES_DOMAIN and TINES_API_KEY are required",
			"help":  "Set TINES_DOMAIN and TINES_API_KEY environment variables",
		}
	}
	base := strings.TrimRight(root, "/")
	if !strings.HasSuffix(base, "/api/v1") {
		base += "/api/v1"
	}
	return base, map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Content-Type":  "application/json",
	}, nil
}

func tinesListStories(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := tinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"per_page": []string{strconv.Itoa(minInt(intArg(input, "per_page", 20), 500))}}
	if teamID := intArg(input, "team_id", 0); teamID > 0 {
		params.Set("team_id", strconv.Itoa(teamID))
	}
	if search := stringArg(input, "search"); search != "" {
		params.Set("search", search)
	}
	data, ok := getJSON(ctx, rt, "Tines", addPrefix(base, "/stories"), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	stories := listOrEmpty(data["stories"])
	out := make([]any, 0, len(stories))
	for _, raw := range stories {
		story := asMap(raw)
		out = append(out, map[string]any{
			"id":          story["id"],
			"name":        story["name"],
			"description": story["description"],
			"disabled":    story["disabled"],
			"mode":        story["mode"],
			"team_id":     story["team_id"],
			"tags":        listOrEmpty(story["tags"]),
			"created_at":  story["created_at"],
			"updated_at":  story["updated_at"],
		})
	}
	return map[string]any{"count": len(out), "stories": out}, nil
}

func tinesGetStory(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := tinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	storyID := intArg(input, "story_id", 0)
	if storyID <= 0 {
		return map[string]any{"error": "story_id is required"}, nil
	}
	path := "/stories/" + strconv.Itoa(storyID)
	data, ok := getJSON(ctx, rt, "Tines", addPrefix(base, path), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"id":                    data["id"],
		"name":                  data["name"],
		"description":           data["description"],
		"disabled":              data["disabled"],
		"mode":                  data["mode"],
		"team_id":               data["team_id"],
		"folder_id":             data["folder_id"],
		"tags":                  listOrEmpty(data["tags"]),
		"send_to_story_enabled": data["send_to_story_enabled"],
		"entry_agent_id":        data["entry_agent_id"],
		"exit_agents":           listOrEmpty(data["exit_agents"]),
		"created_at":            data["created_at"],
		"updated_at":            data["updated_at"],
	}, nil
}

func tinesListActions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := tinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"per_page": []string{strconv.Itoa(minInt(intArg(input, "per_page", 20), 500))}}
	if storyID := intArg(input, "story_id", 0); storyID > 0 {
		params.Set("story_id", strconv.Itoa(storyID))
	}
	if actionType := stringArg(input, "action_type"); actionType != "" {
		params.Set("action_type", actionType)
	}
	data, ok := getJSON(ctx, rt, "Tines", addPrefix(base, "/actions"), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	agents := listOrEmpty(data["agents"])
	out := make([]any, 0, len(agents))
	for _, raw := range agents {
		agent := asMap(raw)
		out = append(out, map[string]any{
			"id":         agent["id"],
			"name":       agent["name"],
			"type":       agent["type"],
			"story_id":   agent["story_id"],
			"disabled":   agent["disabled"],
			"created_at": agent["created_at"],
			"updated_at": agent["updated_at"],
		})
	}
	return map[string]any{"count": len(out), "actions": out}, nil
}

func tinesGetAction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := tinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	actionID := intArg(input, "action_id", 0)
	if actionID <= 0 {
		return map[string]any{"error": "action_id is required"}, nil
	}
	path := "/actions/" + strconv.Itoa(actionID)
	data, ok := getJSON(ctx, rt, "Tines", addPrefix(base, path), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"id":          data["id"],
		"name":        data["name"],
		"type":        data["type"],
		"description": data["description"],
		"story_id":    data["story_id"],
		"disabled":    data["disabled"],
		"sources":     listOrEmpty(data["sources"]),
		"receivers":   listOrEmpty(data["receivers"]),
		"options":     asMap(data["options"]),
		"created_at":  data["created_at"],
		"updated_at":  data["updated_at"],
	}, nil
}

func tinesGetActionLogs(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := tinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	actionID := intArg(input, "action_id", 0)
	if actionID <= 0 {
		return map[string]any{"error": "action_id is required"}, nil
	}
	params := url.Values{"per_page": []string{strconv.Itoa(intArg(input, "per_page", 20))}}
	if level := intArg(input, "level", 0); level > 0 {
		params.Set("level", strconv.Itoa(level))
	}
	path := "/actions/" + strconv.Itoa(actionID) + "/logs"
	data, ok := getJSON(ctx, rt, "Tines", addPrefix(base, path), headers, params, defaultTimeout)
	if !ok {
		return data, nil
	}
	logs := listOrEmpty(data["action_logs"])
	out := make([]any, 0, len(logs))
	for _, raw := range logs {
		entry := asMap(raw)
		out = append(out, map[string]any{
			"id":         entry["id"],
			"level":      entry["level"],
			"message":    entry["message"],
			"created_at": entry["created_at"],
		})
	}
	return map[string]any{"count": len(out), "logs": out}, nil
}

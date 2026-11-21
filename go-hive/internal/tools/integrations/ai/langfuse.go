package ai

// Langfuse LLM observability: traces, scores and prompts.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- langfuse ---

var langfuseTool = &Tool{
	ID:          "langfuse_tool",
	Name:        "langfuse",
	DisplayName: "Langfuse",
	Summary:     "Langfuse LLM observability: traces, scores and prompts (6 actions).",
	Actions: []Action{
		{
			Name:    "langfuse_list_traces",
			Summary: "List traces from Langfuse with optional filters.",
			Source:  "langfuse_tool/langfuse_tool.py",
			Params: map[string]any{
				"name":       paramDefault("string", ""),
				"user_id":    paramDefault("string", ""),
				"session_id": paramDefault("string", ""),
				"tags":       paramDefault("string", ""),
				"page":       paramDefault("integer", 1),
				"limit":      paramDefault("integer", 50),
			},
		},
		{
			Name:     "langfuse_get_trace",
			Summary:  "Get full details of a specific Langfuse trace.",
			Source:   "langfuse_tool/langfuse_tool.py",
			Required: []string{"trace_id"},
			Params: map[string]any{
				"trace_id": param("string"),
			},
		},
		{
			Name:    "langfuse_list_scores",
			Summary: "List scores from Langfuse with optional filters.",
			Source:  "langfuse_tool/langfuse_tool.py",
			Params: map[string]any{
				"trace_id":  paramDefault("string", ""),
				"name":      paramDefault("string", ""),
				"source":    paramDefault("string", ""),
				"data_type": paramDefault("string", ""),
				"page":      paramDefault("integer", 1),
				"limit":     paramDefault("integer", 50),
			},
		},
		{
			Name:     "langfuse_create_score",
			Summary:  "Create a score for a Langfuse trace or observation.",
			Source:   "langfuse_tool/langfuse_tool.py",
			Required: []string{"trace_id", "name", "value"},
			Params: map[string]any{
				"trace_id":       param("string"),
				"name":           param("string"),
				"value":          param("number"),
				"data_type":      paramDefault("string", "NUMERIC"),
				"comment":        paramDefault("string", ""),
				"observation_id": paramDefault("string", ""),
			},
		},
		{
			Name:    "langfuse_list_prompts",
			Summary: "List prompts from Langfuse prompt management.",
			Source:  "langfuse_tool/langfuse_tool.py",
			Params: map[string]any{
				"name":  paramDefault("string", ""),
				"label": paramDefault("string", ""),
				"tag":   paramDefault("string", ""),
				"page":  paramDefault("integer", 1),
				"limit": paramDefault("integer", 50),
			},
		},
		{
			Name:     "langfuse_get_prompt",
			Summary:  "Get a specific Langfuse prompt by name.",
			Source:   "langfuse_tool/langfuse_tool.py",
			Required: []string{"prompt_name"},
			Params: map[string]any{
				"prompt_name": param("string"),
				"version":     paramDefault("integer", 0),
				"label":       paramDefault("string", ""),
			},
		},
	},
}

func langfuseImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"langfuse_list_traces":  langfuseListTraces,
		"langfuse_get_trace":    langfuseGetTrace,
		"langfuse_list_scores":  langfuseListScores,
		"langfuse_create_score": langfuseCreateScore,
		"langfuse_list_prompts": langfuseListPrompts,
		"langfuse_get_prompt":   langfuseGetPrompt,
	}
}

// langfuseAuth resolves the public/secret/host bundle.
func langfuseAuth(rt *Runtime) (string, string, string, map[string]any) {
	getenv := runtimeGetenv(rt)
	publicKey := strings.TrimSpace(getenv("LANGFUSE_PUBLIC_KEY"))
	secretKey := strings.TrimSpace(getenv("LANGFUSE_SECRET_KEY"))
	if publicKey == "" || secretKey == "" {
		return "", "", "", map[string]any{
			"error": "Langfuse credentials not configured",
			"help":  "Set LANGFUSE_PUBLIC_KEY and LANGFUSE_SECRET_KEY environment variables or configure via credential store",
		}
	}
	host := moduleBaseURL(rt, "langfuse_tool")
	if host == "" {
		host = strings.TrimSpace(getenv("LANGFUSE_HOST"))
	}
	if host == "" {
		host = langfuseHost
	}
	return publicKey, secretKey, strings.TrimRight(host, "/"), nil
}

// langfuseCall performs one request and maps HTTP failures the way
// _handle_response does. The boolean reports success.
func langfuseCall(
	ctx context.Context,
	rt *Runtime,
	publicKey, secretKey, host, method, path string,
	query url.Values,
	payload any,
) (map[string]any, bool) {
	target := host + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": basicAuth(publicKey, secretKey),
		"Content-Type":  "application/json",
	}
	reply, err := doRequest(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Langfuse API keys"}, false
	case http.StatusForbidden:
		return map[string]any{
			"error": "Insufficient permissions for this Langfuse resource",
		}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Langfuse resource not found"}, false
	case http.StatusTooManyRequests:
		return map[string]any{
			"error": "Langfuse rate limit exceeded. Try again later.",
		}, false
	}
	if reply.Status >= 400 {
		detail := string(reply.Body)
		if payload, ok := decodeObject(reply.Body); ok {
			if message := asString(payload["message"]); message != "" {
				detail = message
			} else if message := asString(payload["error"]); message != "" {
				detail = message
			}
		}
		return map[string]any{
			"error": fmt.Sprintf("Langfuse API error (HTTP %d): %s", reply.Status, detail),
		}, false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Network error: invalid JSON response"}, false
	}
	return value, true
}

func langfuseListTraces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := langfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	page := intArg(input, "page", 1)
	limit := intArg(input, "limit", 50)
	query := url.Values{
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(limit)},
	}
	if name := stringArg(input, "name"); name != "" {
		query.Set("name", name)
	}
	if userID := stringArg(input, "user_id"); userID != "" {
		query.Set("userId", userID)
	}
	if sessionID := stringArg(input, "session_id"); sessionID != "" {
		query.Set("sessionId", sessionID)
	}
	for _, tag := range splitCSV(stringArg(input, "tags")) {
		query.Add("tags", tag)
	}
	data, ok := langfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet, "/api/public/traces", query, nil,
	)
	if !ok {
		return data, nil
	}
	traces := make([]map[string]any, 0)
	for _, raw := range asSlice(data["data"]) {
		trace := asMap(raw)
		traces = append(traces, map[string]any{
			"id":                trace["id"],
			"name":              trace["name"],
			"timestamp":         trace["timestamp"],
			"user_id":           trace["userId"],
			"session_id":        trace["sessionId"],
			"tags":              anyList(trace["tags"]),
			"latency":           trace["latency"],
			"total_cost":        trace["totalCost"],
			"observation_count": len(asSlice(trace["observations"])),
		})
	}
	meta := asMap(data["meta"])
	return map[string]any{
		"count":       len(traces),
		"total_items": jsonNumber(intOr(meta["totalItems"], 0)),
		"page":        jsonNumber(intOr(meta["page"], page)),
		"total_pages": jsonNumber(intOr(meta["totalPages"], 0)),
		"traces":      traces,
	}, nil
}

func langfuseGetTrace(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := langfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	traceID := stringArg(input, "trace_id")
	if traceID == "" {
		return map[string]any{"error": "trace_id is required"}, nil
	}
	data, ok := langfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet,
		"/api/public/traces/"+url.PathEscape(traceID), nil, nil,
	)
	if !ok {
		return data, nil
	}
	observations := make([]map[string]any, 0)
	for _, raw := range asSlice(data["observations"]) {
		observation := asMap(raw)
		observations = append(observations, map[string]any{
			"id":         observation["id"],
			"type":       observation["type"],
			"name":       observation["name"],
			"model":      observation["model"],
			"start_time": observation["startTime"],
			"end_time":   observation["endTime"],
			"usage":      observation["usage"],
		})
	}
	scores := make([]map[string]any, 0)
	for _, raw := range asSlice(data["scores"]) {
		score := asMap(raw)
		scores = append(scores, map[string]any{
			"id":        score["id"],
			"name":      score["name"],
			"value":     score["value"],
			"data_type": score["dataType"],
			"source":    score["source"],
			"comment":   score["comment"],
		})
	}
	return map[string]any{
		"id":           data["id"],
		"name":         data["name"],
		"timestamp":    data["timestamp"],
		"user_id":      data["userId"],
		"session_id":   data["sessionId"],
		"tags":         anyList(data["tags"]),
		"latency":      data["latency"],
		"total_cost":   data["totalCost"],
		"input":        data["input"],
		"output":       data["output"],
		"observations": observations,
		"scores":       scores,
	}, nil
}

func langfuseListScores(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := langfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	page := intArg(input, "page", 1)
	limit := intArg(input, "limit", 50)
	query := url.Values{
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(limit)},
	}
	if traceID := stringArg(input, "trace_id"); traceID != "" {
		query.Set("traceId", traceID)
	}
	if name := stringArg(input, "name"); name != "" {
		query.Set("name", name)
	}
	if source := stringArg(input, "source"); source != "" {
		query.Set("source", source)
	}
	if dataType := stringArg(input, "data_type"); dataType != "" {
		query.Set("dataType", dataType)
	}
	data, ok := langfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet, "/api/public/v2/scores", query, nil,
	)
	if !ok {
		return data, nil
	}
	scores := make([]map[string]any, 0)
	for _, raw := range asSlice(data["data"]) {
		score := asMap(raw)
		scores = append(scores, map[string]any{
			"id":             score["id"],
			"trace_id":       score["traceId"],
			"observation_id": score["observationId"],
			"name":           score["name"],
			"value":          score["value"],
			"data_type":      score["dataType"],
			"source":         score["source"],
			"comment":        score["comment"],
			"timestamp":      score["timestamp"],
		})
	}
	meta := asMap(data["meta"])
	return map[string]any{
		"count":       len(scores),
		"total_items": jsonNumber(intOr(meta["totalItems"], 0)),
		"page":        jsonNumber(intOr(meta["page"], page)),
		"total_pages": jsonNumber(intOr(meta["totalPages"], 0)),
		"scores":      scores,
	}, nil
}

func langfuseCreateScore(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := langfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	traceID := stringArg(input, "trace_id")
	name := stringArg(input, "name")
	if traceID == "" || name == "" {
		return map[string]any{"error": "trace_id and name are required"}, nil
	}
	body := map[string]any{
		"traceId":  traceID,
		"name":     name,
		"value":    floatArg(input, "value", 0),
		"dataType": stringDefault(input["data_type"], "NUMERIC"),
	}
	if comment := stringArg(input, "comment"); comment != "" {
		body["comment"] = comment
	}
	if observationID := stringArg(input, "observation_id"); observationID != "" {
		body["observationId"] = observationID
	}
	data, ok := langfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodPost, "/api/public/scores", nil, body,
	)
	if !ok {
		return data, nil
	}
	return data, nil
}

func langfuseListPrompts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := langfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	page := intArg(input, "page", 1)
	limit := intArg(input, "limit", 50)
	query := url.Values{
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(limit)},
	}
	if name := stringArg(input, "name"); name != "" {
		query.Set("name", name)
	}
	if label := stringArg(input, "label"); label != "" {
		query.Set("label", label)
	}
	if tag := stringArg(input, "tag"); tag != "" {
		query.Set("tag", tag)
	}
	data, ok := langfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet, "/api/public/v2/prompts", query, nil,
	)
	if !ok {
		return data, nil
	}
	prompts := make([]map[string]any, 0)
	for _, raw := range asSlice(data["data"]) {
		prompt := asMap(raw)
		prompts = append(prompts, map[string]any{
			"name":            prompt["name"],
			"versions":        anyList(prompt["versions"]),
			"labels":          anyList(prompt["labels"]),
			"tags":            anyList(prompt["tags"]),
			"last_updated_at": prompt["lastUpdatedAt"],
		})
	}
	meta := asMap(data["meta"])
	return map[string]any{
		"count":       len(prompts),
		"total_items": jsonNumber(intOr(meta["totalItems"], 0)),
		"page":        jsonNumber(intOr(meta["page"], page)),
		"total_pages": jsonNumber(intOr(meta["totalPages"], 0)),
		"prompts":     prompts,
	}, nil
}

func langfuseGetPrompt(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := langfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	promptName := stringArg(input, "prompt_name")
	if promptName == "" {
		return map[string]any{"error": "prompt_name is required"}, nil
	}
	query := url.Values{}
	if version := intArg(input, "version", 0); version > 0 {
		query.Set("version", strconv.Itoa(version))
	}
	if label := stringArg(input, "label"); label != "" {
		query.Set("label", label)
	}
	data, ok := langfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet,
		"/api/public/v2/prompts/"+url.PathEscape(promptName), query, nil,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"name":    data["name"],
		"version": data["version"],
		"type":    data["type"],
		"prompt":  data["prompt"],
		"config":  data["config"],
		"labels":  anyList(data["labels"]),
		"tags":    anyList(data["tags"]),
	}, nil
}

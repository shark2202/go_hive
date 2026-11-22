package enrichment

// Apify: catalog, six actor/dataset/run actions and the shared call
// helpers.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

var apifyTool = &Tool{
	ID:          "apify_tool",
	Name:        "apify",
	DisplayName: "Apify",
	Summary:     "Apify web scraping and automation platform (6 actions).",
	Actions: []Action{
		{
			Name:    "apify_run_actor",
			Summary: "Run an Apify Actor with optional input.",
			Source:  "apify_tool/apify_tool.py",
			Params: map[string]any{
				"actor_id":      integrationkit.Param("string"),
				"input_data":    integrationkit.Param("object"),
				"memory_mbytes": integrationkit.ParamDefault("integer", 0),
				"timeout_secs":  integrationkit.ParamDefault("integer", 0),
				"build":         integrationkit.ParamDefault("string", ""),
			},
			Required: []string{"actor_id"},
		},
		{
			Name:     "apify_get_run",
			Summary:  "Get status and details of an Actor run.",
			Source:   "apify_tool/apify_tool.py",
			Required: []string{"actor_id", "run_id"},
			Params: map[string]any{
				"actor_id": integrationkit.Param("string"),
				"run_id":   integrationkit.Param("string"),
			},
		},
		{
			Name:     "apify_get_dataset_items",
			Summary:  "Retrieve items from an Apify dataset (Actor output).",
			Source:   "apify_tool/apify_tool.py",
			Required: []string{"dataset_id"},
			Params: map[string]any{
				"dataset_id": integrationkit.Param("string"),
				"limit":      integrationkit.ParamDefault("integer", 100),
				"offset":     integrationkit.ParamDefault("integer", 0),
				"format":     integrationkit.ParamDefault("string", "json"),
			},
		},
		{
			Name:    "apify_list_actors",
			Summary: "List Actors in your Apify account.",
			Source:  "apify_tool/apify_tool.py",
			Params: map[string]any{
				"limit":  integrationkit.ParamDefault("integer", 50),
				"offset": integrationkit.ParamDefault("integer", 0),
			},
		},
		{
			Name:    "apify_list_runs",
			Summary: "List recent Actor runs.",
			Source:  "apify_tool/apify_tool.py",
			Params: map[string]any{
				"actor_id": integrationkit.ParamDefault("string", ""),
				"limit":    integrationkit.ParamDefault("integer", 50),
				"offset":   integrationkit.ParamDefault("integer", 0),
			},
		},
		{
			Name:     "apify_get_kv_store_record",
			Summary:  "Get a record from an Apify key-value store.",
			Source:   "apify_tool/apify_tool.py",
			Required: []string{"store_id", "key"},
			Params: map[string]any{
				"store_id": integrationkit.Param("string"),
				"key":      integrationkit.Param("string"),
			},
		},
	},
}

func apifyImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"apify_run_actor":           apifyRunActor,
		"apify_get_run":             apifyGetRun,
		"apify_get_dataset_items":   apifyGetDatasetItems,
		"apify_list_actors":         apifyListActors,
		"apify_list_runs":           apifyListRuns,
		"apify_get_kv_store_record": apifyGetKVStoreRecord,
	}
}

// apifyAuthError mirrors the upstream missing-token payload.
func apifyAuthError() map[string]any {
	return map[string]any{
		"error": "APIFY_API_TOKEN not set",
		"help":  "Get your token at https://console.apify.com/account/integrations",
	}
}

// apifyHeaders renders the bearer header bundle.
func apifyHeaders(token string, withContentType bool) map[string]string {
	headers := map[string]string{"Authorization": "Bearer " + token}
	if withContentType {
		headers["Content-Type"] = "application/json"
	}
	return headers
}

// apifyBaseURL resolves the API root.
func apifyBaseURL(rt *Runtime) string {
	return baseURL(rt, "apify_tool", "https://api.apify.com/v2")
}

// apifyCall performs one Apify call and maps HTTP failures to the
// upstream error dicts. The boolean reports success.
func apifyCall(
	ctx context.Context,
	rt *Runtime,
	method, endpoint string,
	query url.Values,
	payload any,
) (any, bool) {
	token := moduleToken(rt, "apify_tool")
	if token == "" {
		return apifyAuthError(), false
	}
	target := apifyBaseURL(rt) + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	reply, err := doJSON(
		ctx, rt, method, target,
		apifyHeaders(token, !strings.Contains(endpoint, "/records/")),
		payload,
	)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Apify timed out"}, false
		}
		return map[string]any{"error": "Apify request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your APIFY_API_TOKEN."}, false
	}
	if method == http.MethodGet && reply.Status == http.StatusNotFound && !strings.Contains(endpoint, "/records/") {
		return map[string]any{"error": "Not found"}, false
	}
	if reply.Status != http.StatusOK {
		return apifyAPIError(reply), false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Apify request failed: invalid JSON response"}, false
	}
	return value, true
}

// apifyPostCall performs one POST and expects 200/201.
func apifyPostCall(
	ctx context.Context,
	rt *Runtime,
	endpoint string,
	query url.Values,
	payload any,
) (map[string]any, bool) {
	token := moduleToken(rt, "apify_tool")
	if token == "" {
		return apifyAuthError(), false
	}
	target := apifyBaseURL(rt) + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	reply, err := doJSON(ctx, rt, http.MethodPost, target, apifyHeaders(token, true), payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Apify timed out"}, false
		}
		return map[string]any{"error": "Apify request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your APIFY_API_TOKEN."}, false
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return apifyAPIError(reply), false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Apify request failed: invalid JSON response"}, false
	}
	return value, true
}

// apifyAPIError renders the upstream generic Apify error.
func apifyAPIError(reply httpReply) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf(
			"Apify API error %d: %s",
			reply.Status,
			integrationkit.TruncateText(string(reply.Body), maxErrorBytes),
		),
	}
}

func apifyRunActor(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if moduleToken(rt, "apify_tool") == "" {
		return apifyAuthError(), nil
	}
	actorID := integrationkit.StringArg(input, "actor_id")
	if actorID == "" {
		return map[string]any{"error": "actor_id is required"}, nil
	}
	query := url.Values{}
	if memory := integrationkit.IntArg(input, "memory_mbytes", 0); memory != 0 {
		query.Set("memory", strconv.Itoa(memory))
	}
	if timeout := integrationkit.IntArg(input, "timeout_secs", 0); timeout != 0 {
		query.Set("timeout", strconv.Itoa(timeout))
	}
	if build := integrationkit.StringArg(input, "build"); build != "" {
		query.Set("build", build)
	}
	payload := map[string]any{}
	if data := asMap(input["input_data"]); data != nil {
		payload = data
	}
	value, ok := apifyPostCall(ctx, rt, "/acts/"+actorID+"/runs", query, payload)
	if !ok {
		return value, nil
	}
	data := asMap(value["data"])
	return map[string]any{
		"run_id":      asString(data["id"]),
		"status":      asString(data["status"]),
		"dataset_id":  asString(data["defaultDatasetId"]),
		"kv_store_id": asString(data["defaultKeyValueStoreId"]),
		"started_at":  asString(data["startedAt"]),
	}, nil
}

func apifyGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if moduleToken(rt, "apify_tool") == "" {
		return apifyAuthError(), nil
	}
	actorID := integrationkit.StringArg(input, "actor_id")
	runID := integrationkit.StringArg(input, "run_id")
	if actorID == "" || runID == "" {
		return map[string]any{"error": "actor_id and run_id are required"}, nil
	}
	value, ok := apifyCall(
		ctx, rt, http.MethodGet, "/acts/"+actorID+"/runs/"+runID, nil, nil,
	)
	if !ok {
		return value, nil
	}
	run := asMap(asMap(value)["data"])
	usage := asMap(run["usage"])
	usageUSD := any(0)
	if units, present := usage["ACTOR_COMPUTE_UNITS"]; present {
		usageUSD = jsonNumber(units)
	}
	return map[string]any{
		"run_id":      asString(run["id"]),
		"status":      asString(run["status"]),
		"started_at":  asString(run["startedAt"]),
		"finished_at": asString(run["finishedAt"]),
		"dataset_id":  asString(run["defaultDatasetId"]),
		"kv_store_id": asString(run["defaultKeyValueStoreId"]),
		"usage_usd":   usageUSD,
	}, nil
}

func apifyGetDatasetItems(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if moduleToken(rt, "apify_tool") == "" {
		return apifyAuthError(), nil
	}
	datasetID := integrationkit.StringArg(input, "dataset_id")
	if datasetID == "" {
		return map[string]any{"error": "dataset_id is required"}, nil
	}
	query := url.Values{
		"limit":  {strconv.Itoa(integrationkit.ClampInt(integrationkit.IntArg(input, "limit", 100), 1, 250000))},
		"offset": {strconv.Itoa(integrationkit.IntArg(input, "offset", 0))},
		"format": {integrationkit.StringArg(input, "format")},
	}
	token := moduleToken(rt, "apify_tool")
	target := apifyBaseURL(rt) + "/datasets/" + datasetID + "/items?" + query.Encode()
	reply, err := doJSON(ctx, rt, http.MethodGet, target, apifyHeaders(token, true), nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Apify timed out"}, nil
		}
		return map[string]any{"error": "Apify request failed: " + err.Error()}, nil
	}
	if reply.Status != http.StatusOK {
		return apifyAPIError(reply), nil
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Apify request failed: invalid JSON response"}, nil
	}
	if items, isList := value.([]any); isList {
		return map[string]any{"items": items, "count": len(items)}, nil
	}
	return map[string]any{"items": []any{value}, "count": 1}, nil
}

func apifyListActors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit":  {strconv.Itoa(integrationkit.ClampInt(integrationkit.IntArg(input, "limit", 50), 1, 1000))},
		"offset": {strconv.Itoa(integrationkit.IntArg(input, "offset", 0))},
	}
	value, ok := apifyCall(ctx, rt, http.MethodGet, "/acts", query, nil)
	if !ok {
		return value, nil
	}
	actors := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(asMap(value)["data"])["items"]) {
		actor := asMap(raw)
		stats := asMap(actor["stats"])
		totalRuns := any(0)
		if runs, present := stats["totalRuns"]; present {
			totalRuns = jsonNumber(runs)
		}
		actors = append(actors, map[string]any{
			"id":          asString(actor["id"]),
			"name":        asString(actor["name"]),
			"title":       asString(actor["title"]),
			"description": integrationkit.TruncateText(asString(actor["description"]), 200),
			"total_runs":  totalRuns,
		})
	}
	return map[string]any{"actors": actors, "count": len(actors)}, nil
}

func apifyListRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit":  {strconv.Itoa(integrationkit.ClampInt(integrationkit.IntArg(input, "limit", 50), 1, 1000))},
		"offset": {strconv.Itoa(integrationkit.IntArg(input, "offset", 0))},
	}
	endpoint := "/actor-runs"
	if actorID := integrationkit.StringArg(input, "actor_id"); actorID != "" {
		endpoint = "/acts/" + actorID + "/runs"
	}
	value, ok := apifyCall(ctx, rt, http.MethodGet, endpoint, query, nil)
	if !ok {
		return value, nil
	}
	runs := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(asMap(value)["data"])["items"]) {
		run := asMap(raw)
		runs = append(runs, map[string]any{
			"run_id":      asString(run["id"]),
			"actor_id":    asString(run["actId"]),
			"status":      asString(run["status"]),
			"started_at":  asString(run["startedAt"]),
			"finished_at": asString(run["finishedAt"]),
			"dataset_id":  asString(run["defaultDatasetId"]),
		})
	}
	return map[string]any{"runs": runs, "count": len(runs)}, nil
}

func apifyGetKVStoreRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if moduleToken(rt, "apify_tool") == "" {
		return apifyAuthError(), nil
	}
	storeID := integrationkit.StringArg(input, "store_id")
	key := integrationkit.StringArg(input, "key")
	if storeID == "" || key == "" {
		return map[string]any{"error": "store_id and key are required"}, nil
	}
	token := moduleToken(rt, "apify_tool")
	target := apifyBaseURL(rt) + "/key-value-stores/" + storeID + "/records/" + key
	reply, err := doJSON(ctx, rt, http.MethodGet, target, apifyHeaders(token, false), nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Apify timed out"}, nil
		}
		return map[string]any{"error": "Apify request failed: " + err.Error()}, nil
	}
	if reply.Status == http.StatusNotFound {
		return map[string]any{"error": fmt.Sprintf("Key '%s' not found in store %s", key, storeID)}, nil
	}
	if reply.Status != http.StatusOK {
		return apifyAPIError(reply), nil
	}
	if value, ok := decodeAny(reply.Body); ok {
		return map[string]any{"key": key, "value": value}, nil
	}
	return map[string]any{
		"key":   key,
		"value": integrationkit.TruncateText(string(reply.Body), maxValueBytes),
	}, nil
}

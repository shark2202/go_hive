// Batch 7 lane 5 of the integration port: the enrichment/search long tail —
// Apify (6 actions), Apollo.io (7), Lusha (8), SerpAPI (8) and Exa (7).
//
// Upstream reference:
// tools/src/aden_tools/tools/{apify,apollo,lusha,serpapi,exa_search}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Every module resolves its credential from WithCredential or the
//     environment (APIFY_API_TOKEN, APOLLO_API_KEY, LUSHA_API_KEY,
//     SERPAPI_API_KEY, EXA_API_KEY), matching the upstream credential split.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server with WithBaseURL.
//   - HTTP failures mirror the upstream helpers, including the exact 401/429
//     messages that upstream special-cases. Statuses upstream does not name
//     (402 among them) fall through to the provider's generic error branch.
//   - Exa retries 429 with the upstream 1s/2s/4s backoff; the sleep is a
//     package hook so tests can exercise the retry path without waiting.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane5MaxBody  = 8 << 20
	batch7Lane5MaxError = 500
	batch7Lane5MaxValue = 5000
	batch7Lane5MaxQuery = 500
)

// batch7Lane5RetrySleep backs the Exa 429 backoff; tests swap it for a no-op.
var batch7Lane5RetrySleep = func(delay time.Duration) { time.Sleep(delay) }

// Batch7Lane5Install publishes the batch-7 lane-5 tools (apify, apollo, lusha,
// serpapi, exa_search) into a shared registry and returns the tool count.
func Batch7Lane5Install(target *tools.ToolRegistry) int {
	return batch7Lane5NewRegistry().Install(target)
}

// batch7Lane5NewRegistry builds a registry containing only the lane-5 tools,
// sharing the Runtime surface used by the rest of the port.
func batch7Lane5NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane5Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, item := range []*Tool{
		batch7Lane5ApifyTool,
		batch7Lane5ApolloTool,
		batch7Lane5LushaTool,
		batch7Lane5SerpapiTool,
		batch7Lane5ExaTool,
	} {
		registry.AddTool(item)
	}
	for name, impls := range map[string]map[string]ActionFunc{
		"apify":      batch7Lane5ApifyImpls(),
		"apollo":     batch7Lane5ApolloImpls(),
		"lusha":      batch7Lane5LushaImpls(),
		"serpapi":    batch7Lane5SerpapiImpls(),
		"exa_search": batch7Lane5ExaImpls(),
	} {
		for action, impl := range impls {
			registry.AddImpl(name, action, impl)
		}
	}
	return registry
}

// batch7Lane5Modules declares the public API roots and credential variables.
func batch7Lane5Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"apify_tool": {
			ID: "apify_tool", BaseURL: "https://api.apify.com/v2",
			EnvVars: []string{"APIFY_API_TOKEN"},
		},
		"apollo_tool": {
			ID: "apollo_tool", BaseURL: "https://api.apollo.io/api/v1",
			EnvVars: []string{"APOLLO_API_KEY"},
		},
		"lusha_tool": {
			ID: "lusha_tool", BaseURL: "https://api.lusha.com",
			EnvVars: []string{"LUSHA_API_KEY"},
		},
		"serpapi_tool": {
			ID: "serpapi_tool", BaseURL: "https://serpapi.com",
			EnvVars: []string{"SERPAPI_API_KEY"},
		},
		"exa_search_tool": {
			ID: "exa_search_tool", BaseURL: "https://api.exa.ai",
			EnvVars: []string{"EXA_API_KEY"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch7Lane5BaseURL resolves a module root with a hard-coded fallback.
func batch7Lane5BaseURL(rt *Runtime, moduleID, fallback string) string {
	if base := moduleBaseURL(rt, moduleID); base != "" {
		return base
	}
	return fallback
}

// batch7Lane5ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func batch7Lane5ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.ResolvedToken(runtimeGetenv(rt))
	return strings.TrimSpace(token)
}

// batch7Lane5Reply is the status/body pair every module helper inspects.
type batch7Lane5Reply struct {
	Status int
	Body   []byte
}

// batch7Lane5Do performs one JSON request and reads a bounded body.
func batch7Lane5Do(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (batch7Lane5Reply, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return batch7Lane5Reply{}, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return batch7Lane5Reply{}, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return batch7Lane5Reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane5MaxBody))
	if err != nil {
		return batch7Lane5Reply{}, err
	}
	return batch7Lane5Reply{Status: resp.StatusCode, Body: raw}, nil
}

// batch7Lane5ValidQuery mirrors the upstream 1..500 character check.
func batch7Lane5ValidQuery(query string) bool {
	length := utf8.RuneCountInString(query)
	return length >= 1 && length <= batch7Lane5MaxQuery
}

// batch7Lane5ErrorDetail mirrors the upstream
// `response.json().get("error", response.text)` fallback chain.
func batch7Lane5ErrorDetail(raw []byte) string {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		if value, present := object["error"]; present {
			if text, ok := value.(string); ok {
				return text
			}
			return jsonString(value)
		}
	}
	return string(raw)
}

// batch7Lane5SliceOr keeps a decoded array, defaulting to an empty array.
func batch7Lane5SliceOr(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return []any{}
}

// batch7Lane5FirstNonEmpty mirrors Python's `a or b` for optional string
// fields: an empty string falls through to the next candidate.
func batch7Lane5FirstNonEmpty(values ...any) any {
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			continue
		case string:
			if typed != "" {
				return typed
			}
		default:
			return value
		}
	}
	return nil
}

// batch7Lane5InterfaceSlice renders []string as []any for JSON encoding that
// matches the upstream list payloads.
func batch7Lane5InterfaceSlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// ------------------------------------------------------------------ apify ---

var batch7Lane5ApifyTool = &Tool{
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
				"actor_id":      param("string"),
				"input_data":    param("object"),
				"memory_mbytes": paramDefault("integer", 0),
				"timeout_secs":  paramDefault("integer", 0),
				"build":         paramDefault("string", ""),
			},
			Required: []string{"actor_id"},
		},
		{
			Name:     "apify_get_run",
			Summary:  "Get status and details of an Actor run.",
			Source:   "apify_tool/apify_tool.py",
			Required: []string{"actor_id", "run_id"},
			Params: map[string]any{
				"actor_id": param("string"),
				"run_id":   param("string"),
			},
		},
		{
			Name:     "apify_get_dataset_items",
			Summary:  "Retrieve items from an Apify dataset (Actor output).",
			Source:   "apify_tool/apify_tool.py",
			Required: []string{"dataset_id"},
			Params: map[string]any{
				"dataset_id": param("string"),
				"limit":      paramDefault("integer", 100),
				"offset":     paramDefault("integer", 0),
				"format":     paramDefault("string", "json"),
			},
		},
		{
			Name:    "apify_list_actors",
			Summary: "List Actors in your Apify account.",
			Source:  "apify_tool/apify_tool.py",
			Params: map[string]any{
				"limit":  paramDefault("integer", 50),
				"offset": paramDefault("integer", 0),
			},
		},
		{
			Name:    "apify_list_runs",
			Summary: "List recent Actor runs.",
			Source:  "apify_tool/apify_tool.py",
			Params: map[string]any{
				"actor_id": paramDefault("string", ""),
				"limit":    paramDefault("integer", 50),
				"offset":   paramDefault("integer", 0),
			},
		},
		{
			Name:     "apify_get_kv_store_record",
			Summary:  "Get a record from an Apify key-value store.",
			Source:   "apify_tool/apify_tool.py",
			Required: []string{"store_id", "key"},
			Params: map[string]any{
				"store_id": param("string"),
				"key":      param("string"),
			},
		},
	},
}

func batch7Lane5ApifyImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"apify_run_actor":           batch7Lane5ApifyRunActor,
		"apify_get_run":             batch7Lane5ApifyGetRun,
		"apify_get_dataset_items":   batch7Lane5ApifyGetDatasetItems,
		"apify_list_actors":         batch7Lane5ApifyListActors,
		"apify_list_runs":           batch7Lane5ApifyListRuns,
		"apify_get_kv_store_record": batch7Lane5ApifyGetKVStoreRecord,
	}
}

// batch7Lane5ApifyAuthError mirrors the upstream missing-token payload.
func batch7Lane5ApifyAuthError() map[string]any {
	return map[string]any{
		"error": "APIFY_API_TOKEN not set",
		"help":  "Get your token at https://console.apify.com/account/integrations",
	}
}

// batch7Lane5ApifyHeaders renders the bearer header bundle.
func batch7Lane5ApifyHeaders(token string, withContentType bool) map[string]string {
	headers := map[string]string{"Authorization": "Bearer " + token}
	if withContentType {
		headers["Content-Type"] = "application/json"
	}
	return headers
}

// batch7Lane5ApifyBaseURL resolves the API root.
func batch7Lane5ApifyBaseURL(rt *Runtime) string {
	return batch7Lane5BaseURL(rt, "apify_tool", "https://api.apify.com/v2")
}

// batch7Lane5ApifyCall performs one Apify call and maps HTTP failures to the
// upstream error dicts. The boolean reports success.
func batch7Lane5ApifyCall(
	ctx context.Context,
	rt *Runtime,
	method, endpoint string,
	query url.Values,
	payload any,
) (any, bool) {
	token := batch7Lane5ModuleToken(rt, "apify_tool")
	if token == "" {
		return batch7Lane5ApifyAuthError(), false
	}
	target := batch7Lane5ApifyBaseURL(rt) + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	reply, err := batch7Lane5Do(
		ctx, rt, method, target,
		batch7Lane5ApifyHeaders(token, !strings.Contains(endpoint, "/records/")),
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
		return batch7Lane5ApifyAPIError(reply), false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Apify request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane5ApifyPostCall performs one POST and expects 200/201.
func batch7Lane5ApifyPostCall(
	ctx context.Context,
	rt *Runtime,
	endpoint string,
	query url.Values,
	payload any,
) (map[string]any, bool) {
	token := batch7Lane5ModuleToken(rt, "apify_tool")
	if token == "" {
		return batch7Lane5ApifyAuthError(), false
	}
	target := batch7Lane5ApifyBaseURL(rt) + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	reply, err := batch7Lane5Do(ctx, rt, http.MethodPost, target, batch7Lane5ApifyHeaders(token, true), payload)
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
		return batch7Lane5ApifyAPIError(reply), false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Apify request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane5ApifyAPIError renders the upstream generic Apify error.
func batch7Lane5ApifyAPIError(reply batch7Lane5Reply) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf(
			"Apify API error %d: %s",
			reply.Status,
			truncateText(string(reply.Body), batch7Lane5MaxError),
		),
	}
}

func batch7Lane5ApifyRunActor(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane5ModuleToken(rt, "apify_tool") == "" {
		return batch7Lane5ApifyAuthError(), nil
	}
	actorID := stringArg(input, "actor_id")
	if actorID == "" {
		return map[string]any{"error": "actor_id is required"}, nil
	}
	query := url.Values{}
	if memory := intArg(input, "memory_mbytes", 0); memory != 0 {
		query.Set("memory", strconv.Itoa(memory))
	}
	if timeout := intArg(input, "timeout_secs", 0); timeout != 0 {
		query.Set("timeout", strconv.Itoa(timeout))
	}
	if build := stringArg(input, "build"); build != "" {
		query.Set("build", build)
	}
	payload := map[string]any{}
	if data := asMap(input["input_data"]); data != nil {
		payload = data
	}
	value, ok := batch7Lane5ApifyPostCall(ctx, rt, "/acts/"+actorID+"/runs", query, payload)
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

func batch7Lane5ApifyGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane5ModuleToken(rt, "apify_tool") == "" {
		return batch7Lane5ApifyAuthError(), nil
	}
	actorID := stringArg(input, "actor_id")
	runID := stringArg(input, "run_id")
	if actorID == "" || runID == "" {
		return map[string]any{"error": "actor_id and run_id are required"}, nil
	}
	value, ok := batch7Lane5ApifyCall(
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

func batch7Lane5ApifyGetDatasetItems(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane5ModuleToken(rt, "apify_tool") == "" {
		return batch7Lane5ApifyAuthError(), nil
	}
	datasetID := stringArg(input, "dataset_id")
	if datasetID == "" {
		return map[string]any{"error": "dataset_id is required"}, nil
	}
	query := url.Values{
		"limit":  {strconv.Itoa(clampInt(intArg(input, "limit", 100), 1, 250000))},
		"offset": {strconv.Itoa(intArg(input, "offset", 0))},
		"format": {stringArg(input, "format")},
	}
	token := batch7Lane5ModuleToken(rt, "apify_tool")
	target := batch7Lane5ApifyBaseURL(rt) + "/datasets/" + datasetID + "/items?" + query.Encode()
	reply, err := batch7Lane5Do(ctx, rt, http.MethodGet, target, batch7Lane5ApifyHeaders(token, true), nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Apify timed out"}, nil
		}
		return map[string]any{"error": "Apify request failed: " + err.Error()}, nil
	}
	if reply.Status != http.StatusOK {
		return batch7Lane5ApifyAPIError(reply), nil
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

func batch7Lane5ApifyListActors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit":  {strconv.Itoa(clampInt(intArg(input, "limit", 50), 1, 1000))},
		"offset": {strconv.Itoa(intArg(input, "offset", 0))},
	}
	value, ok := batch7Lane5ApifyCall(ctx, rt, http.MethodGet, "/acts", query, nil)
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
			"description": truncateText(asString(actor["description"]), 200),
			"total_runs":  totalRuns,
		})
	}
	return map[string]any{"actors": actors, "count": len(actors)}, nil
}

func batch7Lane5ApifyListRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit":  {strconv.Itoa(clampInt(intArg(input, "limit", 50), 1, 1000))},
		"offset": {strconv.Itoa(intArg(input, "offset", 0))},
	}
	endpoint := "/actor-runs"
	if actorID := stringArg(input, "actor_id"); actorID != "" {
		endpoint = "/acts/" + actorID + "/runs"
	}
	value, ok := batch7Lane5ApifyCall(ctx, rt, http.MethodGet, endpoint, query, nil)
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

func batch7Lane5ApifyGetKVStoreRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane5ModuleToken(rt, "apify_tool") == "" {
		return batch7Lane5ApifyAuthError(), nil
	}
	storeID := stringArg(input, "store_id")
	key := stringArg(input, "key")
	if storeID == "" || key == "" {
		return map[string]any{"error": "store_id and key are required"}, nil
	}
	token := batch7Lane5ModuleToken(rt, "apify_tool")
	target := batch7Lane5ApifyBaseURL(rt) + "/key-value-stores/" + storeID + "/records/" + key
	reply, err := batch7Lane5Do(ctx, rt, http.MethodGet, target, batch7Lane5ApifyHeaders(token, false), nil)
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
		return batch7Lane5ApifyAPIError(reply), nil
	}
	if value, ok := decodeAny(reply.Body); ok {
		return map[string]any{"key": key, "value": value}, nil
	}
	return map[string]any{
		"key":   key,
		"value": truncateText(string(reply.Body), batch7Lane5MaxValue),
	}, nil
}

// ----------------------------------------------------------------- apollo ---

var batch7Lane5ApolloTool = &Tool{
	ID:          "apollo_tool",
	Name:        "apollo",
	DisplayName: "Apollo.io",
	Summary:     "Apollo.io contact and company data enrichment (7 actions).",
	Actions: []Action{
		{
			Name:    "apollo_enrich_person",
			Summary: "Enrich a person by email, LinkedIn URL, or name and domain.",
			Source:  "apollo_tool/apollo_tool.py",
			Params: map[string]any{
				"email":                  paramDefault("string", ""),
				"linkedin_url":           paramDefault("string", ""),
				"first_name":             paramDefault("string", ""),
				"last_name":              paramDefault("string", ""),
				"name":                   paramDefault("string", ""),
				"domain":                 paramDefault("string", ""),
				"reveal_personal_emails": paramDefault("boolean", false),
				"reveal_phone_number":    paramDefault("boolean", false),
			},
		},
		{
			Name:     "apollo_enrich_company",
			Summary:  "Enrich a company by domain.",
			Source:   "apollo_tool/apollo_tool.py",
			Required: []string{"domain"},
			Params: map[string]any{
				"domain": param("string"),
			},
		},
		{
			Name:    "apollo_search_people",
			Summary: "Search for contacts with titles, seniority, location and firmographic filters.",
			Source:  "apollo_tool/apollo_tool.py",
			Params: map[string]any{
				"titles":        param("array"),
				"seniorities":   param("array"),
				"locations":     param("array"),
				"company_sizes": param("array"),
				"industries":    param("array"),
				"technologies":  param("array"),
				"limit":         paramDefault("integer", 10),
			},
		},
		{
			Name:     "apollo_get_person_activities",
			Summary:  "Get activity history for a person (emails, calls, tasks).",
			Source:   "apollo_tool/apollo_tool.py",
			Required: []string{"person_id"},
			Params: map[string]any{
				"person_id": param("string"),
			},
		},
		{
			Name:    "apollo_list_email_accounts",
			Summary: "List email accounts connected to Apollo.",
			Source:  "apollo_tool/apollo_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "apollo_bulk_enrich_people",
			Summary:  "Bulk enrich up to 10 people at once by email or domain+name.",
			Source:   "apollo_tool/apollo_tool.py",
			Required: []string{"details_json"},
			Params: map[string]any{
				"details_json": param("string"),
			},
		},
		{
			Name:    "apollo_search_companies",
			Summary: "Search for companies with industry, size, location and technology filters.",
			Source:  "apollo_tool/apollo_tool.py",
			Params: map[string]any{
				"industries":      param("array"),
				"employee_counts": param("array"),
				"locations":       param("array"),
				"technologies":    param("array"),
				"limit":           paramDefault("integer", 10),
			},
		},
	},
}

func batch7Lane5ApolloImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"apollo_enrich_person":         batch7Lane5ApolloEnrichPerson,
		"apollo_enrich_company":        batch7Lane5ApolloEnrichCompany,
		"apollo_search_people":         batch7Lane5ApolloSearchPeople,
		"apollo_get_person_activities": batch7Lane5ApolloGetPersonActivities,
		"apollo_list_email_accounts":   batch7Lane5ApolloListEmailAccounts,
		"apollo_bulk_enrich_people":    batch7Lane5ApolloBulkEnrichPeople,
		"apollo_search_companies":      batch7Lane5ApolloSearchCompanies,
	}
}

// batch7Lane5ApolloAuthError mirrors the upstream missing-key payload.
func batch7Lane5ApolloAuthError() map[string]any {
	return map[string]any{
		"error": "Apollo credentials not configured",
		"help": "Set APOLLO_API_KEY environment variable " +
			"or configure via credential store. " +
			"Get your API key at https://app.apollo.io/#/settings/integrations/api",
	}
}

// batch7Lane5ApolloBaseURL resolves the API root.
func batch7Lane5ApolloBaseURL(rt *Runtime) string {
	return batch7Lane5BaseURL(rt, "apollo_tool", "https://api.apollo.io/api/v1")
}

// batch7Lane5ApolloCall performs one Apollo call and maps HTTP failures to the
// upstream error dicts. The boolean reports success.
func batch7Lane5ApolloCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := batch7Lane5ModuleToken(rt, "apollo_tool")
	if token == "" {
		return batch7Lane5ApolloAuthError(), false
	}
	target := batch7Lane5ApolloBaseURL(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"Cache-Control": "no-cache",
		"X-Api-Key":     token,
	}
	reply, err := batch7Lane5Do(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	if failure := batch7Lane5ApolloStatus(reply); failure != nil {
		return failure, false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Apollo request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane5ApolloStatus maps the upstream _handle_response status table.
func batch7Lane5ApolloStatus(reply batch7Lane5Reply) map[string]any {
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Apollo API key"}
	case http.StatusForbidden:
		return map[string]any{
			"error": "Insufficient credits or permissions. Check your Apollo plan.",
			"help":  "Apollo uses export credits for enrichment. Visit https://app.apollo.io/#/settings/plans",
		}
	case http.StatusNotFound:
		return map[string]any{"error": "Resource not found"}
	case http.StatusUnprocessableEntity:
		return map[string]any{"error": "Invalid parameters: " + batch7Lane5ErrorDetail(reply.Body)}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Apollo rate limit exceeded. Try again later."}
	}
	if reply.Status >= 400 {
		return map[string]any{
			"error": fmt.Sprintf(
				"Apollo API error (HTTP %d): %s",
				reply.Status,
				batch7Lane5ErrorDetail(reply.Body),
			),
		}
	}
	return nil
}

// batch7Lane5ApolloPerson flattens a person the way enrich_person does.
func batch7Lane5ApolloPerson(person map[string]any) map[string]any {
	organization := asMap(person["organization"])
	phoneNumbers := batch7Lane5SliceOr(person["phone_numbers"])
	return map[string]any{
		"id":            person["id"],
		"first_name":    person["first_name"],
		"last_name":     person["last_name"],
		"name":          person["name"],
		"title":         person["title"],
		"email":         person["email"],
		"email_status":  person["email_status"],
		"phone_numbers": phoneNumbers,
		"linkedin_url":  person["linkedin_url"],
		"twitter_url":   person["twitter_url"],
		"city":          person["city"],
		"state":         person["state"],
		"country":       person["country"],
		"organization": map[string]any{
			"id":             organization["id"],
			"name":           organization["name"],
			"domain":         organization["primary_domain"],
			"industry":       organization["industry"],
			"employee_count": organization["estimated_num_employees"],
		},
	}
}

// batch7Lane5ApolloOrganization flattens a company the way enrich_company does.
func batch7Lane5ApolloOrganization(source map[string]any) map[string]any {
	return map[string]any{
		"id":                        source["id"],
		"name":                      source["name"],
		"domain":                    source["primary_domain"],
		"website_url":               source["website_url"],
		"linkedin_url":              source["linkedin_url"],
		"twitter_url":               source["twitter_url"],
		"facebook_url":              source["facebook_url"],
		"industry":                  source["industry"],
		"keywords":                  batch7Lane5SliceOr(source["keywords"]),
		"employee_count":            source["estimated_num_employees"],
		"employee_count_range":      source["employee_count_range"],
		"annual_revenue":            source["annual_revenue"],
		"annual_revenue_printed":    source["annual_revenue_printed"],
		"total_funding":             source["total_funding"],
		"total_funding_printed":     source["total_funding_printed"],
		"latest_funding_round_date": source["latest_funding_round_date"],
		"latest_funding_stage":      source["latest_funding_stage"],
		"founded_year":              source["founded_year"],
		"phone":                     source["phone"],
		"city":                      source["city"],
		"state":                     source["state"],
		"country":                   source["country"],
		"street_address":            source["street_address"],
		"technologies":              batch7Lane5SliceOr(source["technologies"]),
		"short_description":         source["short_description"],
	}
}

// batch7Lane5ApolloScope flattens a search result's organization block.
func batch7Lane5ApolloScope(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{"id": nil, "name": nil, "domain": nil}
	}
	return map[string]any{
		"id":     source["id"],
		"name":   source["name"],
		"domain": source["primary_domain"],
	}
}

// batch7Lane5ApolloPagination reads the upstream pagination defaults.
func batch7Lane5ApolloPagination(result map[string]any, total, limit int) (any, any, any) {
	pagination := asMap(result["pagination"])
	totalEntries := any(total)
	if value, present := pagination["total_entries"]; present && value != nil {
		totalEntries = jsonNumber(value)
	}
	page := any(1)
	if value, present := pagination["page"]; present && value != nil {
		page = jsonNumber(value)
	}
	perPage := any(limit)
	if value, present := pagination["per_page"]; present && value != nil {
		perPage = jsonNumber(value)
	}
	return totalEntries, page, perPage
}

// batch7Lane5ApolloFilters appends the non-empty list filters upstream sends.
func batch7Lane5ApolloFilters(input map[string]any, body map[string]any, pairs map[string]string) {
	for name, field := range pairs {
		values := stringSliceArg(input, name)
		if len(values) > 0 {
			body[field] = batch7Lane5InterfaceSlice(values)
		}
	}
}

func batch7Lane5ApolloEnrichPerson(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	email := stringArg(input, "email")
	linkedinURL := stringArg(input, "linkedin_url")
	firstName := stringArg(input, "first_name")
	lastName := stringArg(input, "last_name")
	name := stringArg(input, "name")
	domain := stringArg(input, "domain")
	hasEmailOrLinkedIn := email != "" || linkedinURL != ""
	hasNameAndDomain := (firstName != "" && lastName != "" && domain != "") || (name != "" && domain != "")
	if !hasEmailOrLinkedIn && !hasNameAndDomain {
		return map[string]any{
			"error": "Invalid search criteria. Provide either (email), (linkedin_url), or (name/first_name+last_name AND domain).",
		}, nil
	}
	body := map[string]any{
		"reveal_personal_emails": boolArg(input, "reveal_personal_emails", false),
		"reveal_phone_number":    boolArg(input, "reveal_phone_number", false),
	}
	for field, value := range map[string]string{
		"email":        email,
		"linkedin_url": linkedinURL,
		"first_name":   firstName,
		"last_name":    lastName,
		"name":         name,
		"domain":       domain,
	} {
		if value != "" {
			body[field] = value
		}
	}
	var query url.Values
	if !hasEmailOrLinkedIn {
		query = url.Values{}
		for field, value := range body {
			switch typed := value.(type) {
			case string:
				query.Set(field, typed)
			case bool:
				query.Set(field, strconv.FormatBool(typed))
			default:
				query.Set(field, fmt.Sprint(typed))
			}
		}
	}
	value, ok := batch7Lane5ApolloCall(ctx, rt, http.MethodPost, "/people/match", query, body)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	person, present := result["person"]
	if !present || person == nil {
		return map[string]any{"match_found": false, "message": "No matching person found"}, nil
	}
	return map[string]any{
		"match_found": true,
		"person":      batch7Lane5ApolloPerson(asMap(person)),
	}, nil
}

func batch7Lane5ApolloEnrichCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	domain := stringArg(input, "domain")
	value, ok := batch7Lane5ApolloCall(
		ctx, rt, http.MethodPost, "/organizations/enrich", nil,
		map[string]any{"domain": domain},
	)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	organization, present := result["organization"]
	if !present || organization == nil {
		return map[string]any{"match_found": false, "message": "No matching company found"}, nil
	}
	return map[string]any{
		"match_found":  true,
		"organization": batch7Lane5ApolloOrganization(asMap(organization)),
	}, nil
}

func batch7Lane5ApolloSearchPeople(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := intArg(input, "limit", 10)
	body := map[string]any{
		"per_page": minInt(limit, 100),
		"page":     1,
	}
	batch7Lane5ApolloFilters(input, body, map[string]string{
		"titles":        "person_titles",
		"seniorities":   "person_seniorities",
		"locations":     "person_locations",
		"company_sizes": "organization_num_employees_ranges",
		"industries":    "organization_industry_tag_ids",
		"technologies":  "currently_using_any_of_technology_uids",
	})
	value, ok := batch7Lane5ApolloCall(ctx, rt, http.MethodPost, "/mixed_people/search", nil, body)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	people := asSlice(result["people"])
	total, page, perPage := batch7Lane5ApolloPagination(result, len(people), limit)
	results := make([]map[string]any, 0, len(people))
	for _, raw := range people {
		person := asMap(raw)
		var organization map[string]any
		if scope, present := person["organization"]; present && scope != nil {
			organization = batch7Lane5ApolloScope(asMap(scope))
		} else {
			organization = batch7Lane5ApolloScope(nil)
		}
		results = append(results, map[string]any{
			"id":           person["id"],
			"first_name":   person["first_name"],
			"last_name":    person["last_name"],
			"name":         person["name"],
			"title":        person["title"],
			"email":        person["email"],
			"email_status": person["email_status"],
			"linkedin_url": person["linkedin_url"],
			"city":         person["city"],
			"state":        person["state"],
			"country":      person["country"],
			"seniority":    person["seniority"],
			"organization": organization,
		})
	}
	return map[string]any{
		"total":    total,
		"page":     page,
		"per_page": perPage,
		"results":  results,
	}, nil
}

func batch7Lane5ApolloGetPersonActivities(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	personID := stringArg(input, "person_id")
	if personID == "" {
		return map[string]any{"error": "person_id is required"}, nil
	}
	value, ok := batch7Lane5ApolloCall(
		ctx, rt, http.MethodGet, "/activities",
		url.Values{"contact_id": {personID}}, nil,
	)
	if !ok {
		return value, nil
	}
	activities := asSlice(asMap(value)["activities"])
	limit := len(activities)
	if limit > 50 {
		limit = 50
	}
	out := make([]map[string]any, 0, limit)
	for _, raw := range activities[:limit] {
		activity := asMap(raw)
		out = append(out, map[string]any{
			"id":           activity["id"],
			"type":         activity["type"],
			"subject":      activity["subject"],
			"body":         truncateText(asString(activity["body"]), batch7Lane5MaxError),
			"created_at":   activity["created_at"],
			"completed_at": activity["completed_at"],
			"status":       activity["status"],
			"priority":     activity["priority"],
		})
	}
	return map[string]any{
		"contact_id": personID,
		"count":      len(out),
		"activities": out,
	}, nil
}

func batch7Lane5ApolloListEmailAccounts(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	value, ok := batch7Lane5ApolloCall(ctx, rt, http.MethodGet, "/email_accounts", nil, nil)
	if !ok {
		return value, nil
	}
	accounts := asSlice(asMap(value)["email_accounts"])
	out := make([]map[string]any, 0, len(accounts))
	for _, raw := range accounts {
		account := asMap(raw)
		out = append(out, map[string]any{
			"id":                  account["id"],
			"email":               account["email"],
			"type":                account["type"],
			"active":              account["active"],
			"default":             account["default"],
			"last_synced_at":      account["last_synced_at"],
			"sending_daily_limit": account["sending_daily_limit"],
			"emails_sent_today":   account["emails_sent_today"],
		})
	}
	return map[string]any{"count": len(out), "email_accounts": out}, nil
}

func batch7Lane5ApolloBulkEnrichPeople(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	raw := stringArg(input, "details_json")
	if raw == "" {
		return map[string]any{"error": "details_json is required"}, nil
	}
	var details []any
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		return map[string]any{"error": "details_json must be valid JSON"}, nil
	}
	if len(details) == 0 {
		return map[string]any{"error": "details_json must be a non-empty JSON array"}, nil
	}
	if len(details) > 10 {
		return map[string]any{"error": "maximum 10 people per bulk request"}, nil
	}
	value, ok := batch7Lane5ApolloCall(
		ctx, rt, http.MethodPost, "/people/bulk_match", nil,
		map[string]any{"details": details},
	)
	if !ok {
		return value, nil
	}
	matches := asSlice(asMap(value)["matches"])
	results := make([]map[string]any, 0, len(matches))
	for _, raw := range matches {
		match := asMap(raw)
		if match == nil {
			results = append(results, map[string]any{"match_found": false})
			continue
		}
		results = append(results, map[string]any{
			"match_found":       true,
			"id":                match["id"],
			"name":              match["name"],
			"title":             match["title"],
			"email":             match["email"],
			"email_status":      match["email_status"],
			"linkedin_url":      match["linkedin_url"],
			"organization_name": asMap(match["organization"])["name"],
		})
	}
	return map[string]any{"count": len(results), "results": results}, nil
}

func batch7Lane5ApolloSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := intArg(input, "limit", 10)
	body := map[string]any{
		"per_page": minInt(limit, 100),
		"page":     1,
	}
	batch7Lane5ApolloFilters(input, body, map[string]string{
		"industries":      "organization_industry_tag_ids",
		"employee_counts": "organization_num_employees_ranges",
		"locations":       "organization_locations",
		"technologies":    "currently_using_any_of_technology_uids",
	})
	value, ok := batch7Lane5ApolloCall(ctx, rt, http.MethodPost, "/mixed_companies/search", nil, body)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	organizations := asSlice(result["organizations"])
	total, page, perPage := batch7Lane5ApolloPagination(result, len(organizations), limit)
	results := make([]map[string]any, 0, len(organizations))
	for _, raw := range organizations {
		organization := asMap(raw)
		results = append(results, map[string]any{
			"id":                     organization["id"],
			"name":                   organization["name"],
			"domain":                 organization["primary_domain"],
			"website_url":            organization["website_url"],
			"linkedin_url":           organization["linkedin_url"],
			"industry":               organization["industry"],
			"employee_count":         organization["estimated_num_employees"],
			"employee_count_range":   organization["employee_count_range"],
			"annual_revenue_printed": organization["annual_revenue_printed"],
			"city":                   organization["city"],
			"state":                  organization["state"],
			"country":                organization["country"],
			"short_description":      organization["short_description"],
		})
	}
	return map[string]any{
		"total":    total,
		"page":     page,
		"per_page": perPage,
		"results":  results,
	}, nil
}

// ------------------------------------------------------------------ lusha ---

var batch7Lane5LushaTool = &Tool{
	ID:          "lusha_tool",
	Name:        "lusha",
	DisplayName: "Lusha",
	Summary:     "Lusha B2B contact and company enrichment (8 actions).",
	Actions: []Action{
		{
			Name:    "lusha_enrich_person",
			Summary: "Enrich a person with Lusha data (emails, phones, job info).",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"first_name":     paramDefault("string", ""),
				"last_name":      paramDefault("string", ""),
				"company_domain": paramDefault("string", ""),
				"email":          paramDefault("string", ""),
				"linkedin_url":   paramDefault("string", ""),
			},
		},
		{
			Name:    "lusha_enrich_company",
			Summary: "Enrich a company with Lusha firmographic data.",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"domain":       paramDefault("string", ""),
				"company_name": paramDefault("string", ""),
			},
		},
		{
			Name:    "lusha_search_contacts",
			Summary: "Search for B2B contacts using Lusha prospecting filters.",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"seniorities":     paramDefault("string", ""),
				"departments":     paramDefault("string", ""),
				"company_names":   paramDefault("string", ""),
				"company_domains": paramDefault("string", ""),
				"country":         paramDefault("string", ""),
				"page":            paramDefault("integer", 0),
				"page_size":       paramDefault("integer", 20),
			},
		},
		{
			Name:    "lusha_search_companies",
			Summary: "Search for companies using Lusha prospecting filters.",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"company_names": paramDefault("string", ""),
				"domains":       paramDefault("string", ""),
				"country":       paramDefault("string", ""),
				"min_employees": paramDefault("integer", 0),
				"max_employees": paramDefault("integer", 0),
				"page":          paramDefault("integer", 0),
				"page_size":     paramDefault("integer", 20),
			},
		},
		{
			Name:    "lusha_get_usage",
			Summary: "Get Lusha API credit usage statistics.",
			Source:  "lusha_tool/lusha_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "lusha_bulk_enrich_persons",
			Summary:  "Bulk enrich multiple persons in a single request.",
			Source:   "lusha_tool/lusha_tool.py",
			Required: []string{"details_json"},
			Params: map[string]any{
				"details_json": param("string"),
			},
		},
		{
			Name:     "lusha_get_technologies",
			Summary:  "Get the technology stack used by a company.",
			Source:   "lusha_tool/lusha_tool.py",
			Required: []string{"domain"},
			Params: map[string]any{
				"domain": param("string"),
			},
		},
		{
			Name:     "lusha_search_decision_makers",
			Summary:  "Search for decision makers (VP, C-level, Director) at companies.",
			Source:   "lusha_tool/lusha_tool.py",
			Required: []string{"company_domains"},
			Params: map[string]any{
				"company_domains": param("string"),
				"country":         paramDefault("string", ""),
				"page":            paramDefault("integer", 0),
				"page_size":       paramDefault("integer", 20),
			},
		},
	},
}

func batch7Lane5LushaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"lusha_enrich_person":          batch7Lane5LushaEnrichPerson,
		"lusha_enrich_company":         batch7Lane5LushaEnrichCompany,
		"lusha_search_contacts":        batch7Lane5LushaSearchContacts,
		"lusha_search_companies":       batch7Lane5LushaSearchCompanies,
		"lusha_get_usage":              batch7Lane5LushaGetUsage,
		"lusha_bulk_enrich_persons":    batch7Lane5LushaBulkEnrichPersons,
		"lusha_get_technologies":       batch7Lane5LushaGetTechnologies,
		"lusha_search_decision_makers": batch7Lane5LushaSearchDecisionMakers,
	}
}

// batch7Lane5LushaAuthError mirrors the upstream missing-key payload.
func batch7Lane5LushaAuthError() map[string]any {
	return map[string]any{
		"error": "LUSHA_API_KEY is required",
		"help":  "Set LUSHA_API_KEY environment variable",
	}
}

// batch7Lane5LushaBaseURL resolves the API root.
func batch7Lane5LushaBaseURL(rt *Runtime) string {
	return batch7Lane5BaseURL(rt, "lusha_tool", "https://api.lusha.com")
}

// batch7Lane5LushaCall performs one Lusha call; every 4xx/5xx maps to the
// upstream "HTTP <status>: <text>" error dict.
func batch7Lane5LushaCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := batch7Lane5ModuleToken(rt, "lusha_tool")
	if token == "" {
		return batch7Lane5LushaAuthError(), false
	}
	target := batch7Lane5LushaBaseURL(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{"api_key": token, "Content-Type": "application/json"}
	reply, err := batch7Lane5Do(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Lusha timed out"}, false
		}
		return map[string]any{"error": "Lusha request failed: " + err.Error()}, false
	}
	if reply.Status >= 400 {
		return map[string]any{
			"error": fmt.Sprintf(
				"HTTP %d: %s",
				reply.Status,
				truncateText(string(reply.Body), batch7Lane5MaxError),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Lusha request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane5LushaPerson extracts the upstream person fields.
func batch7Lane5LushaPerson(source map[string]any) map[string]any {
	return map[string]any{
		"first_name":      source["firstName"],
		"last_name":       source["lastName"],
		"full_name":       source["fullName"],
		"job_title":       source["jobTitle"],
		"company":         source["company"],
		"email_addresses": batch7Lane5SliceOr(source["emailAddresses"]),
		"phone_numbers":   batch7Lane5SliceOr(source["phoneNumbers"]),
		"linkedin_url":    source["linkedinUrl"],
		"location":        source["location"],
	}
}

// batch7Lane5LushaCompany extracts the upstream company fields.
func batch7Lane5LushaCompany(source map[string]any) map[string]any {
	name := batch7Lane5FirstNonEmpty(source["name"], source["companyName"])
	domain := batch7Lane5FirstNonEmpty(source["domain"], source["companyDomain"])
	return map[string]any{
		"name":           name,
		"domain":         domain,
		"industry":       source["industry"],
		"employee_count": source["employeeCount"],
		"revenue":        source["revenue"],
		"location":       source["location"],
		"description":    source["description"],
		"founded_year":   source["foundedYear"],
		"technologies":   batch7Lane5SliceOr(source["technologies"]),
	}
}

// batch7Lane5LushaContacts flattens a prospecting contact page.
func batch7Lane5LushaContacts(data map[string]any) map[string]any {
	contacts := asSlice(data["data"])
	out := make([]map[string]any, 0, len(contacts))
	for _, raw := range contacts {
		contact := asMap(raw)
		out = append(out, map[string]any{
			"id":             contact["contactId"],
			"first_name":     contact["firstName"],
			"last_name":      contact["lastName"],
			"job_title":      contact["jobTitle"],
			"seniority":      contact["seniority"],
			"department":     contact["department"],
			"company_name":   contact["companyName"],
			"company_domain": contact["companyDomain"],
			"location":       contact["location"],
		})
	}
	total := any(nil)
	if value, present := data["total"]; present {
		total = jsonNumber(value)
	}
	return map[string]any{"count": len(out), "total": total, "contacts": out}
}

func batch7Lane5LushaEnrichPerson(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	email := stringArg(input, "email")
	linkedinURL := stringArg(input, "linkedin_url")
	firstName := stringArg(input, "first_name")
	lastName := stringArg(input, "last_name")
	query := url.Values{}
	switch {
	case email != "":
		query.Set("email", email)
	case linkedinURL != "":
		query.Set("linkedinUrl", linkedinURL)
	case firstName != "" && lastName != "":
		query.Set("firstName", firstName)
		query.Set("lastName", lastName)
		if domain := stringArg(input, "company_domain"); domain != "" {
			query.Set("companyDomain", domain)
		}
	default:
		return map[string]any{"error": "Provide email, linkedinUrl, or firstName+lastName"}, nil
	}
	value, ok := batch7Lane5LushaCall(ctx, rt, http.MethodGet, "/v2/person", query, nil)
	if !ok {
		return value, nil
	}
	return batch7Lane5LushaPerson(asMap(value)), nil
}

func batch7Lane5LushaEnrichCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{}
	if domain := stringArg(input, "domain"); domain != "" {
		query.Set("domain", domain)
	} else if name := stringArg(input, "company_name"); name != "" {
		query.Set("companyName", name)
	} else {
		return map[string]any{"error": "Provide domain or companyName"}, nil
	}
	value, ok := batch7Lane5LushaCall(ctx, rt, http.MethodGet, "/v2/company", query, nil)
	if !ok {
		return value, nil
	}
	return batch7Lane5LushaCompany(asMap(value)), nil
}

func batch7Lane5LushaSearchContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	contactsInclude := map[string]any{}
	companiesInclude := map[string]any{}
	if raw := stringArg(input, "seniorities"); raw != "" {
		contactsInclude["seniorities"] = batch7Lane5InterfaceSlice(splitCSV(raw))
	}
	if raw := stringArg(input, "departments"); raw != "" {
		contactsInclude["departments"] = batch7Lane5InterfaceSlice(splitCSV(raw))
	}
	if country := stringArg(input, "country"); country != "" {
		contactsInclude["locations"] = []any{map[string]any{"country": country}}
	}
	if raw := stringArg(input, "company_names"); raw != "" {
		companiesInclude["names"] = batch7Lane5InterfaceSlice(splitCSV(raw))
	}
	if raw := stringArg(input, "company_domains"); raw != "" {
		companiesInclude["domains"] = batch7Lane5InterfaceSlice(splitCSV(raw))
	}
	if len(contactsInclude) == 0 && len(companiesInclude) == 0 {
		return map[string]any{"error": "At least one filter is required"}, nil
	}
	filters := map[string]any{}
	if len(contactsInclude) > 0 {
		filters["contacts"] = map[string]any{"include": contactsInclude}
	}
	if len(companiesInclude) > 0 {
		filters["companies"] = map[string]any{"include": companiesInclude}
	}
	payload := map[string]any{
		"pages":   map[string]any{"page": intArg(input, "page", 0), "size": minInt(intArg(input, "page_size", 20), 100)},
		"filters": filters,
	}
	value, ok := batch7Lane5LushaCall(ctx, rt, http.MethodPost, "/prospecting/contact/search", nil, payload)
	if !ok {
		return value, nil
	}
	return batch7Lane5LushaContacts(asMap(value)), nil
}

func batch7Lane5LushaSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	include := map[string]any{}
	if raw := stringArg(input, "company_names"); raw != "" {
		include["names"] = batch7Lane5InterfaceSlice(splitCSV(raw))
	}
	if raw := stringArg(input, "domains"); raw != "" {
		include["domains"] = batch7Lane5InterfaceSlice(splitCSV(raw))
	}
	if country := stringArg(input, "country"); country != "" {
		include["locations"] = []any{map[string]any{"country": country}}
	}
	minimum := intArg(input, "min_employees", 0)
	maximum := intArg(input, "max_employees", 0)
	if minimum > 0 || maximum > 0 {
		sizes := map[string]any{}
		if minimum > 0 {
			sizes["min"] = minimum
		}
		if maximum > 0 {
			sizes["max"] = maximum
		}
		include["sizes"] = []any{sizes}
	}
	if len(include) == 0 {
		return map[string]any{"error": "At least one filter is required"}, nil
	}
	payload := map[string]any{
		"pages": map[string]any{
			"page": intArg(input, "page", 0),
			"size": minInt(intArg(input, "page_size", 20), 100),
		},
		"filters": map[string]any{"companies": map[string]any{"include": include}},
	}
	value, ok := batch7Lane5LushaCall(ctx, rt, http.MethodPost, "/prospecting/company/search", nil, payload)
	if !ok {
		return value, nil
	}
	data := asMap(value)
	companies := asSlice(data["data"])
	out := make([]map[string]any, 0, len(companies))
	for _, raw := range companies {
		out = append(out, batch7Lane5LushaCompany(asMap(raw)))
	}
	total := any(nil)
	if totalRaw, present := data["total"]; present {
		total = jsonNumber(totalRaw)
	}
	return map[string]any{"count": len(out), "total": total, "companies": out}, nil
}

func batch7Lane5LushaGetUsage(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	value, _ := batch7Lane5LushaCall(ctx, rt, http.MethodGet, "/account/usage", nil, nil)
	return value, nil
}

func batch7Lane5LushaBulkEnrichPersons(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane5ModuleToken(rt, "lusha_tool") == "" {
		return batch7Lane5LushaAuthError(), nil
	}
	raw := stringArg(input, "details_json")
	var persons []any
	if err := json.Unmarshal([]byte(raw), &persons); err != nil {
		return map[string]any{"error": "Invalid JSON: " + err.Error()}, nil
	}
	if len(persons) == 0 {
		return map[string]any{"error": "details_json must be a non-empty JSON array"}, nil
	}
	if len(persons) > 50 {
		return map[string]any{"error": "Maximum 50 persons per request"}, nil
	}
	value, ok := batch7Lane5LushaCall(
		ctx, rt, http.MethodPost, "/v2/person/bulk", nil,
		map[string]any{"contacts": persons},
	)
	if !ok {
		return value, nil
	}
	data := asMap(value)
	contacts := asSlice(data["data"])
	if contacts == nil {
		contacts = asSlice(data["contacts"])
	}
	results := make([]map[string]any, 0, len(contacts))
	for _, raw := range contacts {
		results = append(results, batch7Lane5LushaPerson(asMap(raw)))
	}
	return map[string]any{"results": results, "count": len(results)}, nil
}

func batch7Lane5LushaGetTechnologies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	domain := stringArg(input, "domain")
	if domain == "" {
		return map[string]any{"error": "domain is required"}, nil
	}
	value, ok := batch7Lane5LushaCall(
		ctx, rt, http.MethodGet, "/v2/company",
		url.Values{"domain": {domain}}, nil,
	)
	if !ok {
		return value, nil
	}
	data := asMap(value)
	name := batch7Lane5FirstNonEmpty(data["name"], data["companyName"])
	if name == nil {
		name = ""
	}
	industry := data["industry"]
	if industry == nil {
		industry = ""
	}
	return map[string]any{
		"domain":       domain,
		"company_name": name,
		"technologies": batch7Lane5SliceOr(data["technologies"]),
		"industry":     industry,
	}, nil
}

func batch7Lane5LushaSearchDecisionMakers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	rawDomains := stringArg(input, "company_domains")
	if rawDomains == "" {
		return map[string]any{"error": "company_domains is required"}, nil
	}
	contactsInclude := map[string]any{
		"seniorities": []any{"4", "5", "6", "7"},
	}
	if country := stringArg(input, "country"); country != "" {
		contactsInclude["locations"] = []any{map[string]any{"country": country}}
	}
	companiesInclude := map[string]any{
		"domains": batch7Lane5InterfaceSlice(splitCSV(rawDomains)),
	}
	payload := map[string]any{
		"pages": map[string]any{
			"page": intArg(input, "page", 0),
			"size": minInt(intArg(input, "page_size", 20), 100),
		},
		"filters": map[string]any{
			"contacts":  map[string]any{"include": contactsInclude},
			"companies": map[string]any{"include": companiesInclude},
		},
	}
	value, ok := batch7Lane5LushaCall(ctx, rt, http.MethodPost, "/prospecting/contact/search", nil, payload)
	if !ok {
		return value, nil
	}
	return batch7Lane5LushaContacts(asMap(value)), nil
}

// ---------------------------------------------------------------- serpapi ---

var batch7Lane5SerpapiTool = &Tool{
	ID:          "serpapi_tool",
	Name:        "serpapi",
	DisplayName: "SerpAPI",
	Summary:     "Google Scholar, Google Patents and Google web search via SerpAPI (8 actions).",
	Actions: []Action{
		{
			Name:     "scholar_search",
			Summary:  "Search Google Scholar for academic papers, articles, and citations.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        param("string"),
				"num_results":  paramDefault("integer", 10),
				"start":        paramDefault("integer", 0),
				"year_low":     param("integer"),
				"year_high":    param("integer"),
				"sort_by_date": paramDefault("boolean", false),
			},
		},
		{
			Name:     "scholar_get_citations",
			Summary:  "Get formatted citations for a Google Scholar paper.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"result_id"},
			Params: map[string]any{
				"result_id": param("string"),
			},
		},
		{
			Name:     "scholar_get_author",
			Summary:  "Get a Google Scholar author profile with metrics and articles.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"author_id"},
			Params: map[string]any{
				"author_id":    param("string"),
				"num_articles": paramDefault("integer", 20),
				"start":        paramDefault("integer", 0),
				"sort_by":      paramDefault("string", "citedby"),
			},
		},
		{
			Name:     "patents_search",
			Summary:  "Search Google Patents for patents and patent applications.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       param("string"),
				"page":        paramDefault("integer", 1),
				"country":     param("string"),
				"status":      param("string"),
				"before_date": param("string"),
				"after_date":  param("string"),
			},
		},
		{
			Name:     "patents_get_details",
			Summary:  "Get detailed information for a specific patent.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"patent_id"},
			Params: map[string]any{
				"patent_id": param("string"),
			},
		},
		{
			Name:     "scholar_cited_by",
			Summary:  "Get papers that cite a specific Google Scholar paper.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"cites_id"},
			Params: map[string]any{
				"cites_id":    param("string"),
				"num_results": paramDefault("integer", 10),
				"start":       paramDefault("integer", 0),
			},
		},
		{
			Name:     "scholar_search_profiles",
			Summary:  "Search for Google Scholar author profiles by name or affiliation.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       param("string"),
				"num_results": paramDefault("integer", 10),
			},
		},
		{
			Name:     "serpapi_google_search",
			Summary:  "Search Google web results via SerpAPI.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       param("string"),
				"num_results": paramDefault("integer", 10),
				"country":     param("string"),
			},
		},
	},
}

func batch7Lane5SerpapiImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"scholar_search":          batch7Lane5SerpapiScholarSearch,
		"scholar_get_citations":   batch7Lane5SerpapiScholarCitations,
		"scholar_get_author":      batch7Lane5SerpapiScholarAuthor,
		"patents_search":          batch7Lane5SerpapiPatentsSearch,
		"patents_get_details":     batch7Lane5SerpapiPatentsDetails,
		"scholar_cited_by":        batch7Lane5SerpapiScholarCitedBy,
		"scholar_search_profiles": batch7Lane5SerpapiScholarProfiles,
		"serpapi_google_search":   batch7Lane5SerpapiGoogleSearch,
	}
}

// batch7Lane5SerpapiAuthError mirrors the upstream missing-key payload.
func batch7Lane5SerpapiAuthError() map[string]any {
	return map[string]any{
		"error": "SerpAPI credentials not configured",
		"help": "Set SERPAPI_API_KEY environment variable or configure via " +
			"credential store. Get a key at https://serpapi.com/manage-api-key",
	}
}

// batch7Lane5SerpapiBaseURL resolves the API root.
func batch7Lane5SerpapiBaseURL(rt *Runtime) string {
	return batch7Lane5BaseURL(rt, "serpapi_tool", "https://serpapi.com")
}

// batch7Lane5SerpapiCall performs one search.json request. timeoutLabel is the
// upstream per-action timeout message.
func batch7Lane5SerpapiCall(
	ctx context.Context,
	rt *Runtime,
	params url.Values,
	timeoutLabel string,
) (map[string]any, bool) {
	token := batch7Lane5ModuleToken(rt, "serpapi_tool")
	if token == "" {
		return batch7Lane5SerpapiAuthError(), false
	}
	params.Set("api_key", token)
	target := batch7Lane5SerpapiBaseURL(rt) + "/search.json?" + params.Encode()
	reply, err := batch7Lane5Do(ctx, rt, http.MethodGet, target, nil, nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": timeoutLabel}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{
			"error": "Invalid SerpAPI API key",
			"help":  "Check your key at https://serpapi.com/manage-api-key",
		}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "SerpAPI rate limit exceeded. Try again later."}, false
	}
	if reply.Status >= 400 {
		return map[string]any{
			"error": fmt.Sprintf(
				"SerpAPI error (HTTP %d): %s",
				reply.Status,
				batch7Lane5ErrorDetail(reply.Body),
			),
		}, false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "SerpAPI request failed: invalid JSON response"}, false
	}
	if rawError, present := value["error"]; present {
		return map[string]any{"error": "SerpAPI error: " + asString(rawError)}, false
	}
	return value, true
}

// batch7Lane5SerpapiAuthors renders the optional publication authors list.
func batch7Lane5SerpapiAuthors(item map[string]any) []any {
	publication := asMap(item["publication_info"])
	rawAuthors := asSlice(publication["authors"])
	if len(rawAuthors) == 0 {
		return nil
	}
	authors := make([]any, 0, len(rawAuthors))
	for _, raw := range rawAuthors {
		author := asMap(raw)
		authors = append(authors, map[string]any{
			"name":      asString(author["name"]),
			"author_id": asString(author["author_id"]),
		})
	}
	return authors
}

// batch7Lane5SerpapiCitedBy reads inline_links.cited_by defaults.
func batch7Lane5SerpapiCitedBy(item map[string]any) (any, string) {
	citedBy := asMap(asMap(item["inline_links"])["cited_by"])
	total := any(0)
	if value, present := citedBy["total"]; present {
		total = jsonNumber(value)
	}
	return total, asString(citedBy["cites_id"])
}

func batch7Lane5SerpapiScholarSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	params := url.Values{
		"engine": {"google_scholar"},
		"q":      {query},
		"num":    {strconv.Itoa(minInt(intArg(input, "num_results", 10), 20))},
		"start":  {strconv.Itoa(intArg(input, "start", 0))},
	}
	if year, present := optionalInt(input, "year_low"); present {
		params.Set("as_ylo", strconv.Itoa(year))
	}
	if year, present := optionalInt(input, "year_high"); present {
		params.Set("as_yhi", strconv.Itoa(year))
	}
	if boolArg(input, "sort_by_date", false) {
		params.Set("scisbd", "1")
	}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Search request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		item := asMap(raw)
		total, citesID := batch7Lane5SerpapiCitedBy(item)
		result := map[string]any{
			"title":            asString(item["title"]),
			"link":             asString(item["link"]),
			"snippet":          asString(item["snippet"]),
			"result_id":        asString(item["result_id"]),
			"publication_info": asString(asMap(item["publication_info"])["summary"]),
			"cited_by_count":   total,
			"cites_id":         citesID,
		}
		if authors := batch7Lane5SerpapiAuthors(item); authors != nil {
			result["authors"] = authors
		}
		if resources := asSlice(item["resources"]); len(resources) > 0 {
			result["pdf_link"] = asString(asMap(resources[0])["link"])
		}
		results = append(results, result)
	}
	totalResults := any(0)
	if value, present := asMap(data["search_information"])["total_results"]; present {
		totalResults = jsonNumber(value)
	}
	return map[string]any{
		"query":         query,
		"total_results": totalResults,
		"results":       results,
		"count":         len(results),
	}, nil
}

func batch7Lane5SerpapiScholarCitations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	resultID := stringArg(input, "result_id")
	if resultID == "" {
		return map[string]any{"error": "result_id is required"}, nil
	}
	params := url.Values{"engine": {"google_scholar_cite"}, "q": {resultID}}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Citation request timed out")
	if !ok {
		return data, nil
	}
	citations := data["citations"]
	if citations == nil {
		citations = []any{}
	}
	links := data["links"]
	if links == nil {
		links = []any{}
	}
	return map[string]any{
		"result_id": resultID,
		"citations": citations,
		"links":     links,
	}, nil
}

func batch7Lane5SerpapiScholarAuthor(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	authorID := stringArg(input, "author_id")
	if authorID == "" {
		return map[string]any{"error": "author_id is required"}, nil
	}
	sortBy := stringArg(input, "sort_by")
	if sortBy == "" {
		sortBy = "citedby"
	}
	params := url.Values{
		"engine":    {"google_scholar_author"},
		"author_id": {authorID},
		"start":     {strconv.Itoa(intArg(input, "start", 0))},
		"num":       {strconv.Itoa(minInt(intArg(input, "num_articles", 20), 100))},
		"sort":      {sortBy},
	}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Author lookup timed out")
	if !ok {
		return data, nil
	}
	author := asMap(data["author"])
	citedBy := asMap(data["cited_by"])
	metrics := map[string]any{}
	for _, raw := range asSlice(citedBy["table"]) {
		for key, value := range asMap(raw) {
			metrics[key] = value
		}
	}
	interests := make([]string, 0)
	for _, raw := range asSlice(author["interests"]) {
		interests = append(interests, asString(asMap(raw)["title"]))
	}
	articles := make([]map[string]any, 0)
	for _, raw := range asSlice(data["articles"]) {
		article := asMap(raw)
		citedByCount := any(0)
		if value, present := asMap(article["cited_by"])["value"]; present {
			citedByCount = jsonNumber(value)
		}
		articles = append(articles, map[string]any{
			"title":          asString(article["title"]),
			"authors":        asString(article["authors"]),
			"publication":    asString(article["publication"]),
			"year":           asString(article["year"]),
			"cited_by_count": citedByCount,
			"citation_id":    asString(article["citation_id"]),
		})
	}
	return map[string]any{
		"author_id":     authorID,
		"name":          asString(author["name"]),
		"affiliations":  asString(author["affiliations"]),
		"email":         asString(author["email"]),
		"interests":     interests,
		"thumbnail":     asString(author["thumbnail"]),
		"metrics":       metrics,
		"articles":      articles,
		"article_count": len(articles),
	}, nil
}

// batch7Lane5SerpapiPatent flattens one patent result.
func batch7Lane5SerpapiPatent(item map[string]any) map[string]any {
	return map[string]any{
		"title":              asString(item["title"]),
		"snippet":            asString(item["snippet"]),
		"link":               asString(item["link"]),
		"patent_id":          asString(item["patent_id"]),
		"publication_number": asString(item["publication_number"]),
		"inventor":           asString(item["inventor"]),
		"assignee":           asString(item["assignee"]),
		"filing_date":        asString(item["filing_date"]),
		"grant_date":         item["grant_date"],
		"publication_date":   asString(item["publication_date"]),
		"priority_date":      asString(item["priority_date"]),
		"pdf":                asString(item["pdf"]),
	}
}

func batch7Lane5SerpapiPatentsSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	page := intArg(input, "page", 1)
	params := url.Values{
		"engine": {"google_patents"},
		"q":      {query},
		"page":   {strconv.Itoa(page)},
	}
	if country := stringArg(input, "country"); country != "" {
		params.Set("country", country)
	}
	if status := stringArg(input, "status"); status != "" {
		params.Set("status", status)
	}
	if before := stringArg(input, "before_date"); before != "" {
		params.Set("before", "priority:"+before)
	}
	if after := stringArg(input, "after_date"); after != "" {
		params.Set("after", "priority:"+after)
	}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Patent search timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		results = append(results, batch7Lane5SerpapiPatent(asMap(raw)))
	}
	totalResults := any(0)
	if value, present := asMap(data["search_information"])["total_results"]; present {
		totalResults = jsonNumber(value)
	}
	return map[string]any{
		"query":         query,
		"total_results": totalResults,
		"results":       results,
		"count":         len(results),
		"page":          page,
	}, nil
}

func batch7Lane5SerpapiPatentsDetails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	patentID := stringArg(input, "patent_id")
	if patentID == "" {
		return map[string]any{"error": "patent_id is required"}, nil
	}
	params := url.Values{"engine": {"google_patents"}, "q": {patentID}}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Patent detail request timed out")
	if !ok {
		return data, nil
	}
	results := asSlice(data["organic_results"])
	if len(results) == 0 {
		return map[string]any{"error": "No patent found for ID: " + patentID}, nil
	}
	patent := batch7Lane5SerpapiPatent(asMap(results[0]))
	patent["patent_id"] = patentID
	classifications := any(map[string]any{})
	if value, present := asMap(results[0])["classifications"]; present {
		classifications = value
	}
	patent["classifications"] = classifications
	return patent, nil
}

func batch7Lane5SerpapiScholarCitedBy(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	citesID := stringArg(input, "cites_id")
	if citesID == "" {
		return map[string]any{"error": "cites_id is required"}, nil
	}
	params := url.Values{
		"engine": {"google_scholar"},
		"cites":  {citesID},
		"num":    {strconv.Itoa(minInt(intArg(input, "num_results", 10), 20))},
		"start":  {strconv.Itoa(intArg(input, "start", 0))},
	}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Cited-by request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		item := asMap(raw)
		total, _ := batch7Lane5SerpapiCitedBy(item)
		result := map[string]any{
			"title":            asString(item["title"]),
			"link":             asString(item["link"]),
			"snippet":          asString(item["snippet"]),
			"result_id":        asString(item["result_id"]),
			"publication_info": asString(asMap(item["publication_info"])["summary"]),
			"cited_by_count":   total,
		}
		if authors := batch7Lane5SerpapiAuthors(item); authors != nil {
			result["authors"] = authors
		}
		results = append(results, result)
	}
	return map[string]any{"cites_id": citesID, "results": results, "count": len(results)}, nil
}

func batch7Lane5SerpapiScholarProfiles(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	params := url.Values{
		"engine":   {"google_scholar_profiles"},
		"mauthors": {query},
		"num":      {strconv.Itoa(minInt(intArg(input, "num_results", 10), 20))},
	}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Profile search timed out")
	if !ok {
		return data, nil
	}
	profiles := make([]map[string]any, 0)
	for _, raw := range asSlice(data["profiles"]) {
		profile := asMap(raw)
		interests := make([]string, 0)
		for _, interest := range asSlice(profile["interests"]) {
			interests = append(interests, asString(asMap(interest)["title"]))
		}
		citedBy := any(0)
		if value, present := profile["cited_by"]; present {
			citedBy = jsonNumber(value)
		}
		profiles = append(profiles, map[string]any{
			"name":         asString(profile["name"]),
			"author_id":    asString(profile["author_id"]),
			"affiliations": asString(profile["affiliations"]),
			"email":        asString(profile["email"]),
			"cited_by":     citedBy,
			"interests":    interests,
			"thumbnail":    asString(profile["thumbnail"]),
		})
	}
	return map[string]any{"query": query, "profiles": profiles, "count": len(profiles)}, nil
}

func batch7Lane5SerpapiGoogleSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	params := url.Values{
		"engine": {"google"},
		"q":      {query},
		"num":    {strconv.Itoa(minInt(intArg(input, "num_results", 10), 20))},
	}
	if country := stringArg(input, "country"); country != "" {
		params.Set("gl", country)
	}
	data, ok := batch7Lane5SerpapiCall(ctx, rt, params, "Google search timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		item := asMap(raw)
		results = append(results, map[string]any{
			"title":          asString(item["title"]),
			"link":           asString(item["link"]),
			"snippet":        asString(item["snippet"]),
			"displayed_link": asString(item["displayed_link"]),
			"position":       item["position"],
		})
	}
	output := map[string]any{"query": query, "results": results, "count": len(results)}
	if answerBox := asMap(data["answer_box"]); answerBox != nil {
		answer := answerBox["answer"]
		if answer == nil {
			answer = answerBox["snippet"]
		}
		output["answer_box"] = map[string]any{
			"type":   asString(answerBox["type"]),
			"title":  asString(answerBox["title"]),
			"answer": answer,
		}
	}
	if graph := asMap(data["knowledge_graph"]); graph != nil {
		output["knowledge_graph"] = map[string]any{
			"title":       asString(graph["title"]),
			"type":        asString(graph["type"]),
			"description": asString(graph["description"]),
		}
	}
	return output, nil
}

// -------------------------------------------------------------------- exa ---

var batch7Lane5ExaTool = &Tool{
	ID:          "exa_search_tool",
	Name:        "exa_search",
	DisplayName: "Exa Search",
	Summary:     "Exa AI-powered web search, similar-page discovery and content extraction (7 actions).",
	Actions: []Action{
		{
			Name:     "exa_search",
			Summary:  "Search the web using Exa's AI-powered search engine.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":                param("string"),
				"num_results":          paramDefault("integer", 10),
				"search_type":          paramEnumDefault("string", "auto", []string{"auto", "neural", "keyword"}),
				"include_domains":      paramItems("array", "string"),
				"exclude_domains":      paramItems("array", "string"),
				"start_published_date": param("string"),
				"end_published_date":   param("string"),
				"include_text":         paramDefault("boolean", true),
				"include_highlights":   paramDefault("boolean", false),
				"category":             param("string"),
			},
		},
		{
			Name:     "exa_find_similar",
			Summary:  "Find web pages similar to a given URL.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"url"},
			Params: map[string]any{
				"url":             param("string"),
				"num_results":     paramDefault("integer", 10),
				"include_domains": paramItems("array", "string"),
				"exclude_domains": paramItems("array", "string"),
				"include_text":    paramDefault("boolean", true),
			},
		},
		{
			Name:     "exa_get_contents",
			Summary:  "Extract content from one or more URLs using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"urls"},
			Params: map[string]any{
				"urls":               paramItems("array", "string"),
				"include_text":       paramDefault("boolean", true),
				"include_highlights": paramDefault("boolean", false),
			},
		},
		{
			Name:     "exa_answer",
			Summary:  "Get an answer to a question with citations from web sources.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":             param("string"),
				"include_citations": paramDefault("boolean", true),
			},
		},
		{
			Name:     "exa_search_news",
			Summary:  "Search recent news articles using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        param("string"),
				"num_results":  paramDefault("integer", 10),
				"days_back":    paramDefault("integer", 7),
				"include_text": paramDefault("boolean", true),
			},
		},
		{
			Name:     "exa_search_papers",
			Summary:  "Search for research papers and academic content using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        param("string"),
				"num_results":  paramDefault("integer", 10),
				"year_start":   param("integer"),
				"include_text": paramDefault("boolean", false),
			},
		},
		{
			Name:     "exa_search_companies",
			Summary:  "Search for companies and startups using Exa.",
			Source:   "exa_search_tool/exa_search_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        param("string"),
				"num_results":  paramDefault("integer", 10),
				"include_text": paramDefault("boolean", true),
			},
		},
	},
}

func batch7Lane5ExaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"exa_search":           batch7Lane5ExaSearch,
		"exa_find_similar":     batch7Lane5ExaFindSimilar,
		"exa_get_contents":     batch7Lane5ExaGetContents,
		"exa_answer":           batch7Lane5ExaAnswer,
		"exa_search_news":      batch7Lane5ExaSearchNews,
		"exa_search_papers":    batch7Lane5ExaSearchPapers,
		"exa_search_companies": batch7Lane5ExaSearchCompanies,
	}
}

// batch7Lane5ExaAuthError mirrors the upstream missing-key payload.
func batch7Lane5ExaAuthError() map[string]any {
	return map[string]any{
		"error": "Exa credentials not configured",
		"help":  "Set EXA_API_KEY environment variable",
	}
}

// batch7Lane5ExaBaseURL resolves the API root.
func batch7Lane5ExaBaseURL(rt *Runtime) string {
	return batch7Lane5BaseURL(rt, "exa_search_tool", "https://api.exa.ai")
}

// batch7Lane5ExaCall performs one Exa call with the upstream 429 backoff.
func batch7Lane5ExaCall(
	ctx context.Context,
	rt *Runtime,
	endpoint string,
	payload map[string]any,
	timeoutLabel string,
) (map[string]any, bool) {
	token := batch7Lane5ModuleToken(rt, "exa_search_tool")
	if token == "" {
		return batch7Lane5ExaAuthError(), false
	}
	target := batch7Lane5ExaBaseURL(rt) + endpoint
	headers := map[string]string{
		"x-api-key":    token,
		"Content-Type": "application/json",
	}
	for attempt := 0; attempt <= 3; attempt++ {
		reply, err := batch7Lane5Do(ctx, rt, http.MethodPost, target, headers, payload)
		if err != nil {
			if isTimeout(ctx, err) {
				return map[string]any{"error": timeoutLabel}, false
			}
			return map[string]any{"error": "Network error: " + err.Error()}, false
		}
		if reply.Status == http.StatusTooManyRequests && attempt < 3 {
			batch7Lane5RetrySleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		switch reply.Status {
		case http.StatusUnauthorized:
			return map[string]any{"error": "Invalid Exa API key"}, false
		case http.StatusTooManyRequests:
			return map[string]any{"error": "Exa rate limit exceeded. Try again later."}, false
		}
		if reply.Status != http.StatusOK {
			return map[string]any{
				"error": fmt.Sprintf("Exa API request failed: HTTP %d", reply.Status),
			}, false
		}
		value, ok := decodeObject(reply.Body)
		if !ok {
			return map[string]any{"error": "Exa API request failed: invalid JSON response"}, false
		}
		return value, true
	}
	return map[string]any{"error": "Exa rate limit exceeded. Try again later."}, false
}

// batch7Lane5ExaResult flattens one Exa result with optional text/highlights.
func batch7Lane5ExaResult(item map[string]any, includeText, includeHighlights bool) map[string]any {
	result := map[string]any{
		"title":          asString(item["title"]),
		"url":            asString(item["url"]),
		"published_date": asString(item["publishedDate"]),
		"author":         asString(item["author"]),
	}
	if includeText {
		if text, present := item["text"]; present {
			result["text"] = text
		}
	}
	if includeHighlights {
		if highlights, present := item["highlights"]; present {
			result["highlights"] = highlights
		}
	}
	return result
}

// batch7Lane5ExaResults flattens a result page with the standard envelope.
func batch7Lane5ExaResults(data map[string]any, includeText, includeHighlights bool) ([]map[string]any, int) {
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		results = append(results, batch7Lane5ExaResult(asMap(raw), includeText, includeHighlights))
	}
	return results, len(results)
}

func batch7Lane5ExaSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	numResults := clampInt(intArg(input, "num_results", 10), 1, 20)
	includeText := boolArg(input, "include_text", true)
	includeHighlights := boolArg(input, "include_highlights", false)
	payload := map[string]any{
		"query":      query,
		"numResults": numResults,
		"contents":   map[string]any{},
	}
	if searchType := stringArg(input, "search_type"); searchType != "" && searchType != "auto" {
		payload["type"] = searchType
	}
	if domains := stringSliceArg(input, "include_domains"); len(domains) > 0 {
		payload["includeDomains"] = batch7Lane5InterfaceSlice(domains)
	}
	if domains := stringSliceArg(input, "exclude_domains"); len(domains) > 0 {
		payload["excludeDomains"] = batch7Lane5InterfaceSlice(domains)
	}
	if date := stringArg(input, "start_published_date"); date != "" {
		payload["startPublishedDate"] = date
	}
	if date := stringArg(input, "end_published_date"); date != "" {
		payload["endPublishedDate"] = date
	}
	if category := stringArg(input, "category"); category != "" {
		payload["category"] = category
	}
	contents := asMap(payload["contents"])
	if includeText {
		contents["text"] = true
	}
	if includeHighlights {
		contents["highlights"] = true
	}
	data, ok := batch7Lane5ExaCall(ctx, rt, "/search", payload, "Exa search request timed out")
	if !ok {
		return data, nil
	}
	results, total := batch7Lane5ExaResults(data, includeText, includeHighlights)
	return map[string]any{
		"query":    query,
		"results":  results,
		"total":    total,
		"provider": "exa",
	}, nil
}

func batch7Lane5ExaFindSimilar(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sourceURL := stringArg(input, "url")
	if sourceURL == "" {
		return map[string]any{"error": "URL is required"}, nil
	}
	includeText := boolArg(input, "include_text", true)
	payload := map[string]any{
		"url":        sourceURL,
		"numResults": clampInt(intArg(input, "num_results", 10), 1, 20),
		"contents":   map[string]any{},
	}
	if domains := stringSliceArg(input, "include_domains"); len(domains) > 0 {
		payload["includeDomains"] = batch7Lane5InterfaceSlice(domains)
	}
	if domains := stringSliceArg(input, "exclude_domains"); len(domains) > 0 {
		payload["excludeDomains"] = batch7Lane5InterfaceSlice(domains)
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	data, ok := batch7Lane5ExaCall(ctx, rt, "/findSimilar", payload, "Exa find similar request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		result := map[string]any{
			"title":          asString(item["title"]),
			"url":            asString(item["url"]),
			"published_date": asString(item["publishedDate"]),
		}
		if includeText {
			if text, present := item["text"]; present {
				result["text"] = text
			}
		}
		results = append(results, result)
	}
	return map[string]any{
		"source_url": sourceURL,
		"results":    results,
		"total":      len(results),
		"provider":   "exa",
	}, nil
}

func batch7Lane5ExaGetContents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	urls := stringSliceArg(input, "urls")
	if len(urls) == 0 {
		return map[string]any{"error": "At least one URL is required"}, nil
	}
	if len(urls) > 10 {
		return map[string]any{"error": "Maximum 10 URLs per request"}, nil
	}
	includeText := boolArg(input, "include_text", true)
	includeHighlights := boolArg(input, "include_highlights", false)
	payload := map[string]any{
		"ids":      batch7Lane5InterfaceSlice(urls),
		"contents": map[string]any{},
	}
	contents := asMap(payload["contents"])
	if includeText {
		contents["text"] = true
	}
	if includeHighlights {
		contents["highlights"] = true
	}
	data, ok := batch7Lane5ExaCall(ctx, rt, "/contents", payload, "Exa content extraction request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		result := map[string]any{
			"url":   asString(item["url"]),
			"title": asString(item["title"]),
		}
		if includeText {
			if text, present := item["text"]; present {
				result["text"] = text
			}
		}
		if includeHighlights {
			if highlights, present := item["highlights"]; present {
				result["highlights"] = highlights
			}
		}
		results = append(results, result)
	}
	return map[string]any{"results": results, "total": len(results), "provider": "exa"}, nil
}

func batch7Lane5ExaAnswer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	data, ok := batch7Lane5ExaCall(ctx, rt, "/answer", map[string]any{"query": query}, "Exa answer request timed out")
	if !ok {
		return data, nil
	}
	result := map[string]any{
		"query":    query,
		"answer":   asString(data["answer"]),
		"provider": "exa",
	}
	if boolArg(input, "include_citations", true) {
		citations := make([]map[string]any, 0)
		for _, raw := range asSlice(data["citations"]) {
			source := asMap(raw)
			citations = append(citations, map[string]any{
				"title":          asString(source["title"]),
				"url":            asString(source["url"]),
				"published_date": asString(source["publishedDate"]),
			})
		}
		result["citations"] = citations
	}
	return result, nil
}

func batch7Lane5ExaSearchNews(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	daysBack := intArg(input, "days_back", 7)
	now := time.Now
	if rt != nil && rt.Now != nil {
		now = rt.Now
	}
	startDate := now().UTC().AddDate(0, 0, -daysBack).Format("2006-01-02") + "T00:00:00.000Z"
	includeText := boolArg(input, "include_text", true)
	payload := map[string]any{
		"query":              query,
		"numResults":         clampInt(intArg(input, "num_results", 10), 1, 20),
		"category":           "news",
		"startPublishedDate": startDate,
		"contents":           map[string]any{"highlights": true},
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	data, ok := batch7Lane5ExaCall(ctx, rt, "/search", payload, "Exa news search timed out")
	if !ok {
		return data, nil
	}
	results, total := batch7Lane5ExaResults(data, includeText, true)
	return map[string]any{
		"query":     query,
		"days_back": daysBack,
		"results":   results,
		"total":     total,
		"provider":  "exa",
	}, nil
}

func batch7Lane5ExaSearchPapers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	includeText := boolArg(input, "include_text", false)
	payload := map[string]any{
		"query":      query,
		"numResults": clampInt(intArg(input, "num_results", 10), 1, 20),
		"category":   "research paper",
		"contents":   map[string]any{"highlights": true},
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	if year, present := optionalInt(input, "year_start"); present && year != 0 {
		payload["startPublishedDate"] = fmt.Sprintf("%d-01-01T00:00:00.000Z", year)
	}
	data, ok := batch7Lane5ExaCall(ctx, rt, "/search", payload, "Exa paper search timed out")
	if !ok {
		return data, nil
	}
	results, total := batch7Lane5ExaResults(data, includeText, true)
	return map[string]any{"query": query, "results": results, "total": total, "provider": "exa"}, nil
}

func batch7Lane5ExaSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if !batch7Lane5ValidQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	includeText := boolArg(input, "include_text", true)
	payload := map[string]any{
		"query":      query,
		"numResults": clampInt(intArg(input, "num_results", 10), 1, 20),
		"category":   "company",
		"contents":   map[string]any{"highlights": true},
	}
	if includeText {
		asMap(payload["contents"])["text"] = true
	}
	data, ok := batch7Lane5ExaCall(ctx, rt, "/search", payload, "Exa company search timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		result := map[string]any{
			"title":          asString(item["title"]),
			"url":            asString(item["url"]),
			"published_date": asString(item["publishedDate"]),
		}
		if highlights, present := item["highlights"]; present {
			result["highlights"] = highlights
		}
		if includeText {
			if text, present := item["text"]; present {
				result["text"] = text
			}
		}
		results = append(results, result)
	}
	return map[string]any{"query": query, "results": results, "total": len(results), "provider": "exa"}, nil
}

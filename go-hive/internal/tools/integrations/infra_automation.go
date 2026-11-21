// Batch 7 lane 3 of the integration port: infrastructure automation services.
//
// Covers terraform (5 actions), pagerduty (8), prometheus (2), kafka (6),
// n8n (6) and tines (5) — 32 actions total. Every module is a REST wrapper
// around the same public API the upstream Python module targets.
//
// Requests run on Runtime.HTTP so each action is exercisable against an
// httptest server. Credentials resolve from WithCredential, the module's
// environment variables, or WithGetenv. Modules that need more than one
// configuration field (terraform base URL, kafka REST URL + cluster id,
// n8n base URL, tines domain, prometheus base URL / basic auth) read the
// extra fields from the environment; WithBaseURL overrides the endpoint for
// tests.
//
// Deliberate limits, stated per module:
//   - kafka: REST Proxy v3 only. The upstream module also speaks only REST
//     Proxy, so no native broker protocol, no SASL/TLS listener config, no
//     record consumption/streaming and no Schema Registry or ACL management
//     are reproduced. Produce is one fire-and-forget record per call.
//   - terraform: Terraform Cloud / Enterprise API v2 only; run apply/discard/
//     cancel are not exposed because upstream does not expose them either.
//   - prometheus: no retries and no query-length rewriting; an out-of-range
//     timeout falls back to the upstream 5s default.
//   - n8n: read + activate/deactivate only; upstream has no workflow
//     create/update/delete action, so neither does this port.
//   - tines: read-only, matching upstream.
//   - pagerduty: no retries; the write-API "From" header comes from
//     PAGERDUTY_FROM_EMAIL and is omitted when unset, like upstream.
package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	batch7Lane3MaxBody              = 8 << 20
	batch7Lane3DefaultTerraformURL  = "https://app.terraform.io"
	batch7Lane3DefaultTimeout       = 30 * time.Second
	batch7Lane3DefaultPrometheusSec = 5
)

// batch7Lane3PrometheusHelp mirrors the credential spec instructions upstream
// returns with a missing-base-URL response.
const batch7Lane3PrometheusHelp = "To configure Prometheus access:\n\n" +
	"1. Set your Prometheus base URL:\n   export PROMETHEUS_BASE_URL=http://localhost:9090\n\n" +
	"Optional authentication:\n\n" +
	"2. For Bearer Token:\n   export PROMETHEUS_TOKEN=your-token\n\n" +
	"3. For Basic Auth:\n   export PROMETHEUS_USERNAME=admin\n   export PROMETHEUS_PASSWORD=secret\n\n" +
	"Notes:\n- PROMETHEUS_BASE_URL is required\n- Authentication is optional (most local setups don’t need it)\n"

// InstallBatch7Lane3 publishes the batch-7 lane-3 infrastructure tools into a
// shared registry and returns the number of tools registered.
func InstallBatch7Lane3(target *tools.ToolRegistry) int {
	return newBatch7Lane3Registry().Install(target)
}

// newBatch7Lane3Registry builds a registry containing only the lane-3 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane3Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane3Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	catalog := []*Tool{
		batch7Lane3TerraformTool,
		batch7Lane3PagerDutyTool,
		batch7Lane3PrometheusTool,
		batch7Lane3KafkaTool,
		batch7Lane3N8nTool,
		batch7Lane3TinesTool,
	}
	for _, tool := range catalog {
		registry.AddTool(tool)
	}
	impls := []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"terraform", batch7Lane3TerraformImpls()},
		{"pagerduty", batch7Lane3PagerDutyImpls()},
		{"prometheus", batch7Lane3PrometheusImpls()},
		{"kafka", batch7Lane3KafkaImpls()},
		{"n8n", batch7Lane3N8nImpls()},
		{"tines", batch7Lane3TinesImpls()},
	}
	for _, entry := range impls {
		for action, impl := range entry.funcs {
			registry.AddImpl(entry.tool, action, impl)
		}
	}
	return registry
}

// batch7Lane3Modules declares the credential variables for the six modules.
// Base URLs that upstream derives from environment variables stay empty here
// and are resolved per call; WithBaseURL overrides them in tests.
func batch7Lane3Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"terraform_tool":  {ID: "terraform_tool", EnvVars: []string{"TFC_TOKEN"}},
		"pagerduty_tool":  {ID: "pagerduty_tool", BaseURL: "https://api.pagerduty.com", EnvVars: []string{"PAGERDUTY_API_KEY"}},
		"prometheus_tool": {ID: "prometheus_tool"},
		"kafka_tool":      {ID: "kafka_tool"},
		"n8n_tool":        {ID: "n8n_tool", EnvVars: []string{"N8N_API_KEY"}},
		"tines_tool":      {ID: "tines_tool", EnvVars: []string{"TINES_API_KEY"}},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch7Lane3Getenv resolves an environment variable through the runtime.
func batch7Lane3Getenv(rt *Runtime, name string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv(name)
	}
	return os.Getenv(name)
}

// batch7Lane3ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func batch7Lane3ModuleToken(rt *Runtime, moduleID string) string {
	return batch4ModuleToken(rt, moduleID)
}

// batch7Lane3ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch7Lane3ModuleURL(rt *Runtime, moduleID string) string {
	return batch4ModuleURL(rt, moduleID)
}

// batch7Lane3Endpoint resolves a module endpoint: WithBaseURL first, then the
// module's environment variable, then the given default.
func batch7Lane3Endpoint(rt *Runtime, moduleID, envName, fallback string) string {
	if base := batch7Lane3ModuleURL(rt, moduleID); base != "" {
		return base
	}
	if value := strings.TrimRight(strings.TrimSpace(batch7Lane3Getenv(rt, envName)), "/"); value != "" {
		return value
	}
	return fallback
}

// batch7Lane3Response is one completed HTTP exchange.
type batch7Lane3Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// batch7Lane3Send performs one HTTP request with an optional context timeout.
// Transport failures are returned as Go errors; HTTP status handling belongs
// to the caller.
func batch7Lane3Send(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, body []byte, timeout time.Duration) (*batch7Lane3Response, error) {
	requestCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := batch4HTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane3MaxBody))
	if err != nil {
		return nil, err
	}
	return &batch7Lane3Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

// batch7Lane3DoJSON marshals payload as JSON and sends it.
func batch7Lane3DoJSON(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, timeout time.Duration) (*batch7Lane3Response, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	return batch7Lane3Send(ctx, rt, method, endpoint, headers, body, timeout)
}

// batch7Lane3JSONObject decodes a JSON object body.
func batch7Lane3JSONObject(raw []byte) (map[string]any, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

// batch7Lane3HTTPError renders the upstream "HTTP <code>: <body[:500]>" error.
func batch7Lane3HTTPError(resp *batch7Lane3Response) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", resp.Status, batch4TruncateText(string(resp.Body), 500)),
	}
}

// batch7Lane3TransportError renders a labelled transport failure.
func batch7Lane3TransportError(ctx context.Context, label string, err error) map[string]any {
	if batch4IsTimeout(ctx, err) {
		return map[string]any{"error": "Request to " + label + " timed out"}
	}
	return map[string]any{"error": label + " request failed: " + err.Error()}
}

// batch7Lane3Get performs a GET and decodes the JSON object, or returns the
// upstream HTTP error dict.
func batch7Lane3Get(ctx context.Context, rt *Runtime, label, endpoint string, headers map[string]string, params url.Values, timeout time.Duration) (map[string]any, bool) {
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	resp, err := batch7Lane3DoJSON(ctx, rt, http.MethodGet, endpoint, headers, nil, timeout)
	if err != nil {
		return batch7Lane3TransportError(ctx, label, err), false
	}
	if resp.Status >= 400 {
		return batch7Lane3HTTPError(resp), false
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": label + " API error: invalid JSON response"}, false
	}
	return data, true
}

// batch7Lane3Failed reports whether a result carries an upstream error.
func batch7Lane3Failed(result map[string]any) bool {
	_, failed := result["error"]
	return failed
}

// batch7Lane3MinInt returns the smaller of value and limit, matching the
// upstream "min(x, max)" clamps.
func batch7Lane3MinInt(value, limit int) int {
	if value < limit {
		return value
	}
	return limit
}

// batch7Lane3Text reads a string field with a fallback.
func batch7Lane3Text(object map[string]any, name, fallback string) string {
	if object == nil {
		return fallback
	}
	if text, ok := object[name].(string); ok {
		return text
	}
	return fallback
}

// batch7Lane3Int reads a numeric JSON field with a fallback.
func batch7Lane3Int(object map[string]any, name string, fallback int) int {
	if object == nil {
		return fallback
	}
	switch typed := object[name].(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

// batch7Lane3ListOrEmpty views a decoded JSON array, defaulting to empty.
func batch7Lane3ListOrEmpty(value any) []any {
	if items := batch4AsSlice(value); items != nil {
		return items
	}
	return []any{}
}

// batch7Lane3PathEscape escapes one URL path segment.
func batch7Lane3PathEscape(value string) string {
	return url.PathEscape(value)
}

// batch7Lane3AddPrefix joins a base URL and an absolute path.
func batch7Lane3AddPrefix(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// -------------------------------------------------------------- terraform ---

var batch7Lane3TerraformTool = &Tool{
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

func batch7Lane3TerraformImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"terraform_list_workspaces": batch7Lane3TerraformListWorkspaces,
		"terraform_get_workspace":   batch7Lane3TerraformGetWorkspace,
		"terraform_list_runs":       batch7Lane3TerraformListRuns,
		"terraform_get_run":         batch7Lane3TerraformGetRun,
		"terraform_create_run":      batch7Lane3TerraformCreateRun,
	}
}

// batch7Lane3TerraformConfig mirrors upstream _get_config: Bearer token plus
// the JSON:API content type, with the base URL defaulting to Terraform Cloud.
func batch7Lane3TerraformConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	token := batch7Lane3ModuleToken(rt, "terraform_tool")
	if token == "" {
		return "", nil, map[string]any{
			"error": "TFC_TOKEN is required",
			"help":  "Set TFC_TOKEN environment variable",
		}
	}
	root := batch7Lane3Endpoint(rt, "terraform_tool", "TFC_URL", batch7Lane3DefaultTerraformURL)
	base := strings.TrimRight(root, "/")
	if !strings.HasSuffix(base, "/api/v2") {
		base += "/api/v2"
	}
	return base, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/vnd.api+json",
	}, nil
}

// batch7Lane3TerraformWorkspace mirrors upstream _extract_workspace.
func batch7Lane3TerraformWorkspace(raw any) map[string]any {
	workspace := batch4AsMap(raw)
	attrs := batch4AsMap(workspace["attributes"])
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

// batch7Lane3TerraformRun mirrors upstream _extract_run.
func batch7Lane3TerraformRun(raw any) map[string]any {
	run := batch4AsMap(raw)
	attrs := batch4AsMap(run["attributes"])
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

// batch7Lane3TerraformPagination mirrors upstream meta.pagination reads.
func batch7Lane3TerraformPagination(data map[string]any) (any, any) {
	pagination := batch4AsMap(batch4AsMap(data["meta"])["pagination"])
	return pagination["total-count"], pagination["total-pages"]
}

func batch7Lane3TerraformListWorkspaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TerraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	organization := stringArg(input, "organization")
	if organization == "" {
		return map[string]any{"error": "organization is required"}, nil
	}
	params := url.Values{
		"page[size]":   []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "page_size", 20), 100))},
		"page[number]": []string{strconv.Itoa(intArg(input, "page_number", 1))},
	}
	if search := stringArg(input, "search"); search != "" {
		params.Set("search[name]", search)
	}
	path := "/organizations/" + batch7Lane3PathEscape(organization) + "/workspaces"
	data, ok := batch7Lane3Get(ctx, rt, "Terraform", batch7Lane3AddPrefix(base, path), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	workspaces := batch7Lane3ListOrEmpty(data["data"])
	out := make([]any, 0, len(workspaces))
	for _, raw := range workspaces {
		out = append(out, batch7Lane3TerraformWorkspace(raw))
	}
	totalCount, totalPages := batch7Lane3TerraformPagination(data)
	return map[string]any{
		"count":       len(out),
		"total_count": totalCount,
		"total_pages": totalPages,
		"workspaces":  out,
	}, nil
}

func batch7Lane3TerraformGetWorkspace(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TerraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	if workspaceID == "" {
		return map[string]any{"error": "workspace_id is required"}, nil
	}
	path := "/workspaces/" + batch7Lane3PathEscape(workspaceID)
	data, ok := batch7Lane3Get(ctx, rt, "Terraform", batch7Lane3AddPrefix(base, path), headers, nil, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	workspace := batch4AsMap(data["data"])
	result := batch7Lane3TerraformWorkspace(workspace)
	attrs := batch4AsMap(workspace["attributes"])
	result["description"] = attrs["description"]
	result["vcs_repo"] = attrs["vcs-repo"]
	result["working_directory"] = attrs["working-directory"]
	return result, nil
}

func batch7Lane3TerraformListRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TerraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workspaceID := stringArg(input, "workspace_id")
	if workspaceID == "" {
		return map[string]any{"error": "workspace_id is required"}, nil
	}
	params := url.Values{
		"page[size]":   []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "page_size", 20), 100))},
		"page[number]": []string{strconv.Itoa(intArg(input, "page_number", 1))},
	}
	if status := stringArg(input, "status"); status != "" {
		params.Set("filter[status]", status)
	}
	path := "/workspaces/" + batch7Lane3PathEscape(workspaceID) + "/runs"
	data, ok := batch7Lane3Get(ctx, rt, "Terraform", batch7Lane3AddPrefix(base, path), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	runs := batch7Lane3ListOrEmpty(data["data"])
	out := make([]any, 0, len(runs))
	for _, raw := range runs {
		out = append(out, batch7Lane3TerraformRun(raw))
	}
	totalCount, totalPages := batch7Lane3TerraformPagination(data)
	return map[string]any{
		"count":       len(out),
		"total_count": totalCount,
		"total_pages": totalPages,
		"runs":        out,
	}, nil
}

func batch7Lane3TerraformGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TerraformConfig(rt)
	if failure != nil {
		return failure, nil
	}
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	path := "/runs/" + batch7Lane3PathEscape(runID)
	data, ok := batch7Lane3Get(ctx, rt, "Terraform", batch7Lane3AddPrefix(base, path), headers, nil, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	run := batch4AsMap(data["data"])
	result := batch7Lane3TerraformRun(run)
	attrs := batch4AsMap(run["attributes"])
	// Upstream reuses this key for the plan-queued timestamp.
	result["plan_and_apply"] = map[string]any{
		"resource_additions": batch4AsMap(attrs["status-timestamps"])["plan-queued-at"],
	}
	result["permissions"] = attrs["permissions"]
	return result, nil
}

func batch7Lane3TerraformCreateRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TerraformConfig(rt)
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
	resp, err := batch7Lane3DoJSON(ctx, rt, http.MethodPost, batch7Lane3AddPrefix(base, "/runs"), headers, payload, batch7Lane3DefaultTimeout)
	if err != nil {
		return batch7Lane3TransportError(ctx, "Terraform", err), nil
	}
	if resp.Status >= 400 {
		return batch7Lane3HTTPError(resp), nil
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Terraform API error: invalid JSON response"}, nil
	}
	return batch7Lane3TerraformRun(data["data"]), nil
}

// -------------------------------------------------------------- pagerduty ---

var batch7Lane3PagerDutyTool = &Tool{
	ID:          "pagerduty_tool",
	Name:        "pagerduty",
	DisplayName: "PagerDuty",
	Summary:     "PagerDuty incident management and service listing (8 actions).",
	Actions: []Action{
		{
			Name:    "pagerduty_list_incidents",
			Summary: "List PagerDuty incidents with optional filters.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"status":     paramDefault("string", ""),
				"since":      paramDefault("string", ""),
				"until":      paramDefault("string", ""),
				"service_id": paramDefault("string", ""),
				"urgency":    paramDefault("string", ""),
				"limit":      paramDefault("integer", 25),
			},
		},
		{
			Name:     "pagerduty_get_incident",
			Summary:  "Get details of a specific PagerDuty incident.",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"incident_id"},
			Params: map[string]any{
				"incident_id": param("string"),
			},
		},
		{
			Name:     "pagerduty_create_incident",
			Summary:  "Create a new PagerDuty incident.",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"title", "service_id"},
			Params: map[string]any{
				"title":      param("string"),
				"service_id": param("string"),
				"urgency":    paramDefault("string", "high"),
				"details":    paramDefault("string", ""),
			},
		},
		{
			Name:     "pagerduty_update_incident",
			Summary:  "Update a PagerDuty incident (acknowledge, resolve, etc.).",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"incident_id"},
			Params: map[string]any{
				"incident_id": param("string"),
				"status":      paramDefault("string", ""),
				"resolution":  paramDefault("string", ""),
			},
		},
		{
			Name:    "pagerduty_list_services",
			Summary: "List PagerDuty services.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"query": paramDefault("string", ""),
				"limit": paramDefault("integer", 25),
			},
		},
		{
			Name:    "pagerduty_list_oncalls",
			Summary: "List current on-call entries.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"schedule_id":          paramDefault("string", ""),
				"escalation_policy_id": paramDefault("string", ""),
				"since":                paramDefault("string", ""),
				"until":                paramDefault("string", ""),
				"limit":                paramDefault("integer", 25),
			},
		},
		{
			Name:     "pagerduty_add_incident_note",
			Summary:  "Add a note to a PagerDuty incident.",
			Source:   "pagerduty_tool/pagerduty_tool.py",
			Required: []string{"incident_id", "content"},
			Params: map[string]any{
				"incident_id": param("string"),
				"content":     param("string"),
			},
		},
		{
			Name:    "pagerduty_list_escalation_policies",
			Summary: "List PagerDuty escalation policies.",
			Source:  "pagerduty_tool/pagerduty_tool.py",
			Params: map[string]any{
				"query": paramDefault("string", ""),
				"limit": paramDefault("integer", 25),
			},
		},
	},
}

func batch7Lane3PagerDutyImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pagerduty_list_incidents":           batch7Lane3PagerDutyListIncidents,
		"pagerduty_get_incident":             batch7Lane3PagerDutyGetIncident,
		"pagerduty_create_incident":          batch7Lane3PagerDutyCreateIncident,
		"pagerduty_update_incident":          batch7Lane3PagerDutyUpdateIncident,
		"pagerduty_list_services":            batch7Lane3PagerDutyListServices,
		"pagerduty_list_oncalls":             batch7Lane3PagerDutyListOncalls,
		"pagerduty_add_incident_note":        batch7Lane3PagerDutyAddIncidentNote,
		"pagerduty_list_escalation_policies": batch7Lane3PagerDutyListEscalationPolicies,
	}
}

// batch7Lane3PagerDutyHeaders mirrors upstream _get_headers.
func batch7Lane3PagerDutyHeaders(rt *Runtime, write bool) (map[string]string, map[string]any) {
	apiKey := batch7Lane3ModuleToken(rt, "pagerduty_tool")
	if apiKey == "" {
		return nil, map[string]any{
			"error": "PAGERDUTY_API_KEY is required",
			"help":  "Set PAGERDUTY_API_KEY environment variable",
		}
	}
	headers := map[string]string{
		"Authorization": "Token token=" + apiKey,
		"Accept":        "application/vnd.pagerduty+json;version=2",
		"Content-Type":  "application/json",
	}
	if write {
		if from := strings.TrimSpace(batch7Lane3Getenv(rt, "PAGERDUTY_FROM_EMAIL")); from != "" {
			headers["From"] = from
		}
	}
	return headers, nil
}

// batch7Lane3PagerDutyIncident mirrors upstream _extract_incident.
func batch7Lane3PagerDutyIncident(raw any) map[string]any {
	incident := batch4AsMap(raw)
	service := batch4AsMap(incident["service"])
	assignments := make([]any, 0)
	for _, rawAssignment := range batch7Lane3ListOrEmpty(incident["assignments"]) {
		assignment := batch4AsMap(rawAssignment)
		assignments = append(assignments, batch4AsMap(assignment["assignee"])["summary"])
	}
	return map[string]any{
		"id":              incident["id"],
		"incident_number": incident["incident_number"],
		"title":           incident["title"],
		"status":          incident["status"],
		"urgency":         incident["urgency"],
		"created_at":      incident["created_at"],
		"html_url":        incident["html_url"],
		"service":         service["summary"],
		"service_id":      service["id"],
		"assignments":     assignments,
	}
}

// batch7Lane3PagerDutyBase resolves the fixed PagerDuty API base.
func batch7Lane3PagerDutyBase(rt *Runtime) string {
	return batch7Lane3Endpoint(rt, "pagerduty_tool", "PAGERDUTY_BASE_URL", "https://api.pagerduty.com")
}

func batch7Lane3PagerDutyListIncidents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "limit", 25), 100))}}
	if status := stringArg(input, "status"); status != "" {
		for _, part := range strings.Split(status, ",") {
			params.Add("statuses[]", strings.TrimSpace(part))
		}
	}
	for name, key := range map[string]string{"since": "since", "until": "until"} {
		if value := stringArg(input, name); value != "" {
			params.Set(key, value)
		}
	}
	if serviceID := stringArg(input, "service_id"); serviceID != "" {
		params.Set("service_ids[]", serviceID)
	}
	if urgency := stringArg(input, "urgency"); urgency != "" {
		params.Set("urgencies[]", urgency)
	}
	data, ok := batch7Lane3Get(ctx, rt, "PagerDuty", batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), "/incidents"), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	incidents := batch7Lane3ListOrEmpty(data["incidents"])
	out := make([]any, 0, len(incidents))
	for _, raw := range incidents {
		out = append(out, batch7Lane3PagerDutyIncident(raw))
	}
	return map[string]any{
		"count":     len(out),
		"more":      batch4AsBool(data["more"]),
		"incidents": out,
	}, nil
}

func batch7Lane3PagerDutyGetIncident(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	incidentID := stringArg(input, "incident_id")
	if incidentID == "" {
		return map[string]any{"error": "incident_id is required"}, nil
	}
	data, ok := batch7Lane3Get(ctx, rt, "PagerDuty",
		batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), "/incidents/"+batch7Lane3PathEscape(incidentID)), headers, nil, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	incident := batch4AsMap(data["incident"])
	result := batch7Lane3PagerDutyIncident(incident)
	if body := batch4AsMap(incident["body"]); len(body) > 0 {
		result["details"] = body["details"]
	}
	return result, nil
}

func batch7Lane3PagerDutyCreateIncident(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, true)
	if failure != nil {
		return failure, nil
	}
	title := stringArg(input, "title")
	serviceID := stringArg(input, "service_id")
	if title == "" || serviceID == "" {
		return map[string]any{"error": "title and service_id are required"}, nil
	}
	incident := map[string]any{
		"type":    "incident",
		"title":   title,
		"service": map[string]any{"id": serviceID, "type": "service_reference"},
		"urgency": stringArg(input, "urgency"),
	}
	if details := stringArg(input, "details"); details != "" {
		incident["body"] = map[string]any{"type": "incident_body", "details": details}
	}
	resp, err := batch7Lane3DoJSON(ctx, rt, http.MethodPost,
		batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), "/incidents"), headers,
		map[string]any{"incident": incident}, batch7Lane3DefaultTimeout)
	if err != nil {
		return batch7Lane3TransportError(ctx, "PagerDuty", err), nil
	}
	if resp.Status >= 400 {
		return batch7Lane3HTTPError(resp), nil
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "PagerDuty API error: invalid JSON response"}, nil
	}
	result := batch7Lane3PagerDutyIncident(data["incident"])
	result["result"] = "created"
	return result, nil
}

func batch7Lane3PagerDutyUpdateIncident(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, true)
	if failure != nil {
		return failure, nil
	}
	incidentID := stringArg(input, "incident_id")
	if incidentID == "" {
		return map[string]any{"error": "incident_id is required"}, nil
	}
	status := stringArg(input, "status")
	if status == "" {
		return map[string]any{"error": "status is required (acknowledged or resolved)"}, nil
	}
	incident := map[string]any{"type": "incident_reference", "status": status}
	if resolution := stringArg(input, "resolution"); resolution != "" && status == "resolved" {
		incident["resolution"] = resolution
	}
	path := "/incidents/" + batch7Lane3PathEscape(incidentID)
	resp, err := batch7Lane3DoJSON(ctx, rt, http.MethodPut,
		batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), path), headers,
		map[string]any{"incident": incident}, batch7Lane3DefaultTimeout)
	if err != nil {
		return batch7Lane3TransportError(ctx, "PagerDuty", err), nil
	}
	if resp.Status >= 400 {
		return batch7Lane3HTTPError(resp), nil
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "PagerDuty API error: invalid JSON response"}, nil
	}
	return batch7Lane3PagerDutyIncident(data["incident"]), nil
}

func batch7Lane3PagerDutyListServices(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "limit", 25), 100))}}
	if query := stringArg(input, "query"); query != "" {
		params.Set("query", query)
	}
	data, ok := batch7Lane3Get(ctx, rt, "PagerDuty", batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), "/services"), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	services := batch7Lane3ListOrEmpty(data["services"])
	out := make([]any, 0, len(services))
	for _, raw := range services {
		service := batch4AsMap(raw)
		out = append(out, map[string]any{
			"id":                      service["id"],
			"name":                    service["name"],
			"description":             service["description"],
			"status":                  service["status"],
			"html_url":                service["html_url"],
			"created_at":              service["created_at"],
			"last_incident_timestamp": service["last_incident_timestamp"],
		})
	}
	return map[string]any{"count": len(out), "services": out}, nil
}

func batch7Lane3PagerDutyListOncalls(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "limit", 25), 100))}}
	if scheduleID := stringArg(input, "schedule_id"); scheduleID != "" {
		params.Set("schedule_ids[]", scheduleID)
	}
	if policyID := stringArg(input, "escalation_policy_id"); policyID != "" {
		params.Set("escalation_policy_ids[]", policyID)
	}
	if since := stringArg(input, "since"); since != "" {
		params.Set("since", since)
	}
	if until := stringArg(input, "until"); until != "" {
		params.Set("until", until)
	}
	data, ok := batch7Lane3Get(ctx, rt, "PagerDuty", batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), "/oncalls"), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	oncalls := batch7Lane3ListOrEmpty(data["oncalls"])
	out := make([]any, 0, len(oncalls))
	for _, raw := range oncalls {
		oncall := batch4AsMap(raw)
		user := batch4AsMap(oncall["user"])
		schedule := batch4AsMap(oncall["schedule"])
		policy := batch4AsMap(oncall["escalation_policy"])
		out = append(out, map[string]any{
			"user_name":         batch7Lane3Text(user, "summary", ""),
			"user_id":           batch7Lane3Text(user, "id", ""),
			"schedule_name":     batch7Lane3Text(schedule, "summary", ""),
			"schedule_id":       batch7Lane3Text(schedule, "id", ""),
			"escalation_policy": batch7Lane3Text(policy, "summary", ""),
			"escalation_level":  batch7Lane3Int(oncall, "escalation_level", 0),
			"start":             batch7Lane3Text(oncall, "start", ""),
			"end":               batch7Lane3Text(oncall, "end", ""),
		})
	}
	return map[string]any{"count": len(out), "oncalls": out}, nil
}

func batch7Lane3PagerDutyAddIncidentNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, true)
	if failure != nil {
		return failure, nil
	}
	incidentID := stringArg(input, "incident_id")
	content := stringArg(input, "content")
	if incidentID == "" || content == "" {
		return map[string]any{"error": "incident_id and content are required"}, nil
	}
	path := "/incidents/" + batch7Lane3PathEscape(incidentID) + "/notes"
	resp, err := batch7Lane3DoJSON(ctx, rt, http.MethodPost,
		batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), path), headers,
		map[string]any{"note": map[string]any{"content": content}}, batch7Lane3DefaultTimeout)
	if err != nil {
		return batch7Lane3TransportError(ctx, "PagerDuty", err), nil
	}
	if resp.Status >= 400 {
		return batch7Lane3HTTPError(resp), nil
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "PagerDuty API error: invalid JSON response"}, nil
	}
	note := batch4AsMap(data["note"])
	return map[string]any{
		"id":         batch7Lane3Text(note, "id", ""),
		"content":    batch7Lane3Text(note, "content", ""),
		"created_at": batch7Lane3Text(note, "created_at", ""),
		"user":       batch7Lane3Text(batch4AsMap(note["user"]), "summary", ""),
		"status":     "created",
	}, nil
}

func batch7Lane3PagerDutyListEscalationPolicies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, failure := batch7Lane3PagerDutyHeaders(rt, false)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "limit", 25), 100))}}
	if query := stringArg(input, "query"); query != "" {
		params.Set("query", query)
	}
	data, ok := batch7Lane3Get(ctx, rt, "PagerDuty",
		batch7Lane3AddPrefix(batch7Lane3PagerDutyBase(rt), "/escalation_policies"), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	policies := batch7Lane3ListOrEmpty(data["escalation_policies"])
	out := make([]any, 0, len(policies))
	for _, raw := range policies {
		policy := batch4AsMap(raw)
		teams := make([]any, 0)
		for _, rawTeam := range batch7Lane3ListOrEmpty(policy["teams"]) {
			teams = append(teams, batch7Lane3Text(batch4AsMap(rawTeam), "summary", ""))
		}
		out = append(out, map[string]any{
			"id":                     batch7Lane3Text(policy, "id", ""),
			"name":                   batch7Lane3Text(policy, "name", ""),
			"description":            batch7Lane3Text(policy, "description", ""),
			"num_loops":              batch7Lane3Int(policy, "num_loops", 0),
			"teams":                  teams,
			"escalation_rules_count": len(batch7Lane3ListOrEmpty(policy["escalation_rules"])),
		})
	}
	return map[string]any{"count": len(out), "escalation_policies": out}, nil
}

// ------------------------------------------------------------- prometheus ---

var batch7Lane3PrometheusTool = &Tool{
	ID:          "prometheus_tool",
	Name:        "prometheus",
	DisplayName: "Prometheus",
	Summary:     "Prometheus PromQL instant and range queries (2 actions).",
	Actions: []Action{
		{
			Name:     "prometheus_query",
			Summary:  "Query Prometheus using PromQL.",
			Source:   "prometheus_tool/prometheus_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":   param("string"),
				"timeout": paramDefault("integer", 5),
			},
		},
		{
			Name:     "prometheus_query_range",
			Summary:  "Query Prometheus over a time range using PromQL.",
			Source:   "prometheus_tool/prometheus_tool.py",
			Required: []string{"query", "start", "end"},
			Params: map[string]any{
				"query":   param("string"),
				"start":   param("string"),
				"end":     param("string"),
				"step":    paramDefault("string", "60s"),
				"timeout": paramDefault("integer", 5),
			},
		},
	},
}

func batch7Lane3PrometheusImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"prometheus_query":       batch7Lane3PrometheusQuery,
		"prometheus_query_range": batch7Lane3PrometheusQueryRange,
	}
}

// batch7Lane3PrometheusAuth mirrors upstream _get_auth: Bearer token wins,
// then optional basic auth, otherwise unauthenticated.
func batch7Lane3PrometheusAuth(rt *Runtime) map[string]string {
	if token := strings.TrimSpace(batch7Lane3Getenv(rt, "PROMETHEUS_TOKEN")); token != "" {
		return map[string]string{"Authorization": "Bearer " + token}
	}
	username := batch7Lane3Getenv(rt, "PROMETHEUS_USERNAME")
	password := batch7Lane3Getenv(rt, "PROMETHEUS_PASSWORD")
	if username != "" && password != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		return map[string]string{"Authorization": "Basic " + encoded}
	}
	return nil
}

// batch7Lane3PrometheusBase mirrors upstream _get_prometheus_base_url:
// credential store (WithBaseURL) wins over PROMETHEUS_BASE_URL.
func batch7Lane3PrometheusBase(rt *Runtime) string {
	return strings.TrimRight(batch7Lane3Endpoint(rt, "prometheus_tool", "PROMETHEUS_BASE_URL", ""), "/")
}

// batch7Lane3PrometheusTimeout clamps to the upstream 1..30 second window.
func batch7Lane3PrometheusTimeout(input map[string]any) int {
	timeout := intArg(input, "timeout", batch7Lane3DefaultPrometheusSec)
	if timeout < 1 || timeout > 30 {
		return batch7Lane3DefaultPrometheusSec
	}
	return timeout
}

// batch7Lane3PrometheusTransport maps transport failures the way upstream
// distinguishes timeout, connection failure and everything else.
func batch7Lane3PrometheusTransport(ctx context.Context, err error) map[string]any {
	if batch4IsTimeout(ctx, err) {
		return map[string]any{"error": "Request to Prometheus timed out"}
	}
	var netErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &netErr) || errors.As(err, &dnsErr) {
		return map[string]any{
			"error": "Failed to connect to Prometheus",
			"help":  "Check if Prometheus is running and base_url is correct",
		}
	}
	return map[string]any{"error": "Unexpected error: " + err.Error()}
}

// batch7Lane3PrometheusMissing renders the upstream missing-credential dict.
func batch7Lane3PrometheusMissing() map[string]any {
	return map[string]any{
		"error":   "Missing required credential: Base URL of Prometheus server",
		"help":    batch7Lane3PrometheusHelp,
		"success": false,
	}
}

// batch7Lane3PrometheusRun performs one query and shapes the response. label
// is the upstream failure noun ("Prometheus query" / "Prometheus range query").
// The boolean reports whether the query succeeded.
func batch7Lane3PrometheusRun(ctx context.Context, rt *Runtime, endpoint string, params url.Values, timeoutSeconds int, label string) (map[string]any, bool) {
	base := batch7Lane3PrometheusBase(rt)
	if base == "" {
		return batch7Lane3PrometheusMissing(), false
	}
	resp, err := batch7Lane3Send(ctx, rt, http.MethodGet, batch7Lane3AddPrefix(base, endpoint)+"?"+params.Encode(),
		batch7Lane3PrometheusAuth(rt), nil, time.Duration(timeoutSeconds)*time.Second)
	if err != nil {
		return batch7Lane3PrometheusTransport(ctx, err), false
	}
	if resp.Status != http.StatusOK {
		return map[string]any{
			"error":   fmt.Sprintf("Prometheus returned status %d", resp.Status),
			"details": string(resp.Body),
		}, false
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Unexpected error: invalid JSON response"}, false
	}
	if batch4AsString(data["status"]) != "success" {
		return map[string]any{"error": label + " failed", "details": data}, false
	}
	return map[string]any{
		"success": true,
		"result":  batch7Lane3ListOrEmpty(batch4AsMap(data["data"])["result"]),
		"raw":     data,
	}, true
}

func batch7Lane3PrometheusQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" || utf8.RuneCountInString(query) > 1000 {
		return map[string]any{"error": "Query must be between 1 and 1000 characters"}, nil
	}
	timeout := batch7Lane3PrometheusTimeout(input)
	result, ok := batch7Lane3PrometheusRun(ctx, rt, "/api/v1/query",
		url.Values{"query": []string{query}}, timeout, "Prometheus query")
	if ok {
		result["query"] = query
	}
	return result, nil
}

func batch7Lane3PrometheusQueryRange(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" || utf8.RuneCountInString(query) > 1000 {
		return map[string]any{"error": "Query must be between 1 and 1000 characters"}, nil
	}
	start := stringArg(input, "start")
	end := stringArg(input, "end")
	if start == "" || end == "" {
		return map[string]any{"error": "start and end time are required"}, nil
	}
	step := stringArg(input, "step")
	timeout := batch7Lane3PrometheusTimeout(input)
	params := url.Values{
		"query": []string{query},
		"start": []string{start},
		"end":   []string{end},
		"step":  []string{step},
	}
	result, ok := batch7Lane3PrometheusRun(ctx, rt, "/api/v1/query_range", params, timeout, "Prometheus range query")
	if ok {
		result["query"] = query
		result["start"] = start
		result["end"] = end
		result["step"] = step
	}
	return result, nil
}

// ------------------------------------------------------------------ kafka ---

var batch7Lane3KafkaTool = &Tool{
	ID:          "kafka_tool",
	Name:        "kafka",
	DisplayName: "Apache Kafka (REST Proxy)",
	Summary:     "Kafka topic, produce and consumer-group management via Confluent REST Proxy v3 (6 actions).",
	Actions: []Action{
		{
			Name:    "kafka_list_topics",
			Summary: "List all Kafka topics in the cluster.",
			Source:  "kafka_tool/kafka_tool.py",
		},
		{
			Name:     "kafka_get_topic",
			Summary:  "Get metadata for a specific Kafka topic.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"topic_name"},
			Params: map[string]any{
				"topic_name": param("string"),
			},
		},
		{
			Name:     "kafka_create_topic",
			Summary:  "Create a new Kafka topic.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"topic_name"},
			Params: map[string]any{
				"topic_name":         param("string"),
				"partitions_count":   paramDefault("integer", 1),
				"replication_factor": paramDefault("integer", 3),
			},
		},
		{
			Name:     "kafka_produce_message",
			Summary:  "Produce a message to a Kafka topic.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"topic_name", "value"},
			Params: map[string]any{
				"topic_name": param("string"),
				"value":      param("string"),
				"key":        paramDefault("string", ""),
				"value_type": paramDefault("string", "JSON"),
			},
		},
		{
			Name:    "kafka_list_consumer_groups",
			Summary: "List all consumer groups in the Kafka cluster.",
			Source:  "kafka_tool/kafka_tool.py",
		},
		{
			Name:     "kafka_get_consumer_group_lag",
			Summary:  "Get lag summary for a Kafka consumer group.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"consumer_group_id"},
			Params: map[string]any{
				"consumer_group_id": param("string"),
			},
		},
	},
}

func batch7Lane3KafkaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"kafka_list_topics":            batch7Lane3KafkaListTopics,
		"kafka_get_topic":              batch7Lane3KafkaGetTopic,
		"kafka_create_topic":           batch7Lane3KafkaCreateTopic,
		"kafka_produce_message":        batch7Lane3KafkaProduceMessage,
		"kafka_list_consumer_groups":   batch7Lane3KafkaListConsumerGroups,
		"kafka_get_consumer_group_lag": batch7Lane3KafkaGetConsumerGroupLag,
	}
}

// batch7Lane3KafkaConfig mirrors upstream _get_config. Limit: REST Proxy v3
// endpoints only — no native broker protocol or SASL listener setup.
func batch7Lane3KafkaConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	restURL := strings.TrimRight(batch7Lane3Endpoint(rt, "kafka_tool", "KAFKA_REST_URL", ""), "/")
	if restURL == "" {
		return "", nil, map[string]any{
			"error": "KAFKA_REST_URL is required",
			"help":  "Set KAFKA_REST_URL environment variable",
		}
	}
	clusterID := strings.TrimSpace(batch7Lane3Getenv(rt, "KAFKA_CLUSTER_ID"))
	if clusterID == "" {
		return "", nil, map[string]any{
			"error": "KAFKA_CLUSTER_ID is required",
			"help":  "Set KAFKA_CLUSTER_ID environment variable",
		}
	}
	headers := map[string]string{"Content-Type": "application/json"}
	apiKey := batch7Lane3Getenv(rt, "KAFKA_API_KEY")
	apiSecret := batch7Lane3Getenv(rt, "KAFKA_API_SECRET")
	if apiKey != "" && apiSecret != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(apiKey + ":" + apiSecret))
		headers["Authorization"] = "Basic " + encoded
	}
	return restURL + "/v3/clusters/" + batch7Lane3PathEscape(clusterID), headers, nil
}

// batch7Lane3KafkaTopic projects one REST Proxy topic resource.
func batch7Lane3KafkaTopic(topic map[string]any) map[string]any {
	return map[string]any{
		"name":               topic["topic_name"],
		"partitions_count":   topic["partitions_count"],
		"replication_factor": topic["replication_factor"],
		"is_internal":        topic["is_internal"],
	}
}

func batch7Lane3KafkaListTopics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3KafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data, ok := batch7Lane3Get(ctx, rt, "Kafka", batch7Lane3AddPrefix(base, "/topics"), headers, nil, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	topics := batch7Lane3ListOrEmpty(data["data"])
	out := make([]any, 0, len(topics))
	for _, raw := range topics {
		out = append(out, batch7Lane3KafkaTopic(batch4AsMap(raw)))
	}
	return map[string]any{"count": len(out), "topics": out}, nil
}

func batch7Lane3KafkaGetTopic(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3KafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	topicName := stringArg(input, "topic_name")
	if topicName == "" {
		return map[string]any{"error": "topic_name is required"}, nil
	}
	path := "/topics/" + batch7Lane3PathEscape(topicName)
	data, ok := batch7Lane3Get(ctx, rt, "Kafka", batch7Lane3AddPrefix(base, path), headers, nil, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	result := batch7Lane3KafkaTopic(data)
	result["cluster_id"] = data["cluster_id"]
	return result, nil
}

func batch7Lane3KafkaCreateTopic(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3KafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	topicName := stringArg(input, "topic_name")
	if topicName == "" {
		return map[string]any{"error": "topic_name is required"}, nil
	}
	payload := map[string]any{
		"topic_name":         topicName,
		"partitions_count":   intArg(input, "partitions_count", 1),
		"replication_factor": intArg(input, "replication_factor", 3),
	}
	resp, err := batch7Lane3DoJSON(ctx, rt, http.MethodPost, batch7Lane3AddPrefix(base, "/topics"), headers, payload, batch7Lane3DefaultTimeout)
	if err != nil {
		return batch7Lane3TransportError(ctx, "Kafka", err), nil
	}
	if resp.Status >= 400 {
		return batch7Lane3HTTPError(resp), nil
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Kafka API error: invalid JSON response"}, nil
	}
	return map[string]any{
		"name":               data["topic_name"],
		"partitions_count":   data["partitions_count"],
		"replication_factor": data["replication_factor"],
	}, nil
}

func batch7Lane3KafkaProduceMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3KafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	topicName := stringArg(input, "topic_name")
	value := stringArg(input, "value")
	if topicName == "" || value == "" {
		return map[string]any{"error": "topic_name and value are required"}, nil
	}
	payload := map[string]any{
		"value": map[string]any{"type": stringArg(input, "value_type"), "data": value},
	}
	if key := stringArg(input, "key"); key != "" {
		payload["key"] = map[string]any{"type": "STRING", "data": key}
	}
	path := "/topics/" + batch7Lane3PathEscape(topicName) + "/records"
	resp, err := batch7Lane3DoJSON(ctx, rt, http.MethodPost, batch7Lane3AddPrefix(base, path), headers, payload, batch7Lane3DefaultTimeout)
	if err != nil {
		return batch7Lane3TransportError(ctx, "Kafka", err), nil
	}
	if resp.Status >= 400 {
		return batch7Lane3HTTPError(resp), nil
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Kafka API error: invalid JSON response"}, nil
	}
	return map[string]any{
		"topic":     data["topic_name"],
		"partition": data["partition_id"],
		"offset":    data["offset"],
		"timestamp": data["timestamp"],
	}, nil
}

func batch7Lane3KafkaListConsumerGroups(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3KafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data, ok := batch7Lane3Get(ctx, rt, "Kafka", batch7Lane3AddPrefix(base, "/consumer-groups"), headers, nil, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	groups := batch7Lane3ListOrEmpty(data["data"])
	out := make([]any, 0, len(groups))
	for _, raw := range groups {
		group := batch4AsMap(raw)
		coordinator := batch4AsMap(group["coordinator"])
		out = append(out, map[string]any{
			"id":             group["consumer_group_id"],
			"is_simple":      group["is_simple"],
			"state":          group["state"],
			"coordinator_id": coordinator["related"],
		})
	}
	return map[string]any{"count": len(out), "consumer_groups": out}, nil
}

func batch7Lane3KafkaGetConsumerGroupLag(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3KafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	groupID := stringArg(input, "consumer_group_id")
	if groupID == "" {
		return map[string]any{"error": "consumer_group_id is required"}, nil
	}
	path := "/consumer-groups/" + batch7Lane3PathEscape(groupID) + "/lag-summary"
	data, ok := batch7Lane3Get(ctx, rt, "Kafka", batch7Lane3AddPrefix(base, path), headers, nil, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"consumer_group_id":   data["consumer_group_id"],
		"max_lag":             data["max_lag"],
		"max_lag_topic":       data["max_lag_topic_name"],
		"max_lag_partition":   data["max_lag_partition_id"],
		"max_lag_consumer_id": data["max_lag_consumer_id"],
		"total_lag":           data["total_lag"],
	}, nil
}

// -------------------------------------------------------------------- n8n ---

var batch7Lane3N8nTool = &Tool{
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

func batch7Lane3N8nImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"n8n_list_workflows":      batch7Lane3N8nListWorkflows,
		"n8n_get_workflow":        batch7Lane3N8nGetWorkflow,
		"n8n_activate_workflow":   batch7Lane3N8nActivateWorkflow,
		"n8n_deactivate_workflow": batch7Lane3N8nDeactivateWorkflow,
		"n8n_list_executions":     batch7Lane3N8nListExecutions,
		"n8n_get_execution":       batch7Lane3N8nGetExecution,
	}
}

// batch7Lane3N8nConfig mirrors upstream _get_creds.
func batch7Lane3N8nConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	apiKey := batch7Lane3ModuleToken(rt, "n8n_tool")
	baseURL := batch7Lane3Endpoint(rt, "n8n_tool", "N8N_BASE_URL", "")
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

// batch7Lane3N8nResult mirrors upstream _handle_response.
func batch7Lane3N8nResult(resp *batch7Lane3Response) map[string]any {
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
		if body, ok := batch7Lane3JSONObject(resp.Body); ok {
			if message, present := body["message"].(string); present {
				detail = message
			}
		}
		return map[string]any{"error": fmt.Sprintf("n8n API error (HTTP %d): %s", resp.Status, detail)}
	}
	data, ok := batch7Lane3JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": fmt.Sprintf("n8n API error (HTTP %d): invalid JSON response", resp.Status)}
	}
	return data
}

// batch7Lane3N8nTransport renders the upstream network-error split.
func batch7Lane3N8nTransport(ctx context.Context, err error) map[string]any {
	if batch4IsTimeout(ctx, err) {
		return map[string]any{"error": "Request timed out"}
	}
	return map[string]any{"error": "Network error: " + err.Error()}
}

// batch7Lane3N8nCall performs one request and merges transport/status errors.
func batch7Lane3N8nCall(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, params url.Values) (map[string]any, bool) {
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	resp, err := batch7Lane3DoJSON(ctx, rt, method, endpoint, headers, payload, batch7Lane3DefaultTimeout)
	if err != nil {
		return batch7Lane3N8nTransport(ctx, err), false
	}
	result := batch7Lane3N8nResult(resp)
	if batch7Lane3Failed(result) {
		return result, false
	}
	return result, true
}

// batch7Lane3N8nTags collects tag names.
func batch7Lane3N8nTags(value any) []any {
	tags := make([]any, 0)
	for _, raw := range batch7Lane3ListOrEmpty(value) {
		tags = append(tags, batch7Lane3Text(batch4AsMap(raw), "name", ""))
	}
	return tags
}

func batch7Lane3N8nListWorkflows(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3N8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "limit", 100), 250))}}
	for _, name := range []string{"active", "tags", "name", "cursor"} {
		if value := stringArg(input, name); value != "" {
			params.Set(name, value)
		}
	}
	result, ok := batch7Lane3N8nCall(ctx, rt, http.MethodGet, batch7Lane3AddPrefix(base, "/api/v1/workflows"), headers, nil, params)
	if !ok {
		return result, nil
	}
	workflows := batch7Lane3ListOrEmpty(result["data"])
	out := make([]any, 0, len(workflows))
	for _, raw := range workflows {
		workflow := batch4AsMap(raw)
		out = append(out, map[string]any{
			"id":         workflow["id"],
			"name":       workflow["name"],
			"active":     workflow["active"],
			"created_at": workflow["createdAt"],
			"updated_at": workflow["updatedAt"],
			"tags":       batch7Lane3N8nTags(workflow["tags"]),
			"node_count": len(batch7Lane3ListOrEmpty(workflow["nodes"])),
		})
	}
	output := map[string]any{"count": len(out), "workflows": out}
	if cursor := batch7Lane3Text(result, "nextCursor", ""); cursor != "" {
		output["next_cursor"] = cursor
	}
	return output, nil
}

func batch7Lane3N8nGetWorkflow(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3N8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workflowID := stringArg(input, "workflow_id")
	if workflowID == "" {
		return map[string]any{"error": "workflow_id is required"}, nil
	}
	path := "/api/v1/workflows/" + batch7Lane3PathEscape(workflowID)
	result, ok := batch7Lane3N8nCall(ctx, rt, http.MethodGet, batch7Lane3AddPrefix(base, path), headers, nil, nil)
	if !ok {
		return result, nil
	}
	nodes := make([]any, 0)
	for _, raw := range batch7Lane3ListOrEmpty(result["nodes"]) {
		node := batch4AsMap(raw)
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
		"tags":       batch7Lane3N8nTags(result["tags"]),
		"nodes":      nodes,
		"node_count": len(nodes),
	}, nil
}

// batch7Lane3N8nToggle implements activate/deactivate, which differ only by
// the upstream path suffix.
func batch7Lane3N8nToggle(ctx context.Context, rt *Runtime, input map[string]any, suffix string) (any, error) {
	base, headers, failure := batch7Lane3N8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	workflowID := stringArg(input, "workflow_id")
	if workflowID == "" {
		return map[string]any{"error": "workflow_id is required"}, nil
	}
	path := "/api/v1/workflows/" + batch7Lane3PathEscape(workflowID) + suffix
	result, ok := batch7Lane3N8nCall(ctx, rt, http.MethodPost, batch7Lane3AddPrefix(base, path), headers, nil, nil)
	if !ok {
		return result, nil
	}
	return map[string]any{
		"id":     result["id"],
		"name":   result["name"],
		"active": result["active"],
	}, nil
}

func batch7Lane3N8nActivateWorkflow(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane3N8nToggle(ctx, rt, input, "/activate")
}

func batch7Lane3N8nDeactivateWorkflow(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane3N8nToggle(ctx, rt, input, "/deactivate")
}

func batch7Lane3N8nListExecutions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3N8nConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"limit": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "limit", 100), 250))}}
	if workflowID := stringArg(input, "workflow_id"); workflowID != "" {
		params.Set("workflowId", workflowID)
	}
	if status := stringArg(input, "status"); status != "" {
		params.Set("status", status)
	}
	if cursor := stringArg(input, "cursor"); cursor != "" {
		params.Set("cursor", cursor)
	}
	result, ok := batch7Lane3N8nCall(ctx, rt, http.MethodGet, batch7Lane3AddPrefix(base, "/api/v1/executions"), headers, nil, params)
	if !ok {
		return result, nil
	}
	executions := batch7Lane3ListOrEmpty(result["data"])
	out := make([]any, 0, len(executions))
	for _, raw := range executions {
		execution := batch4AsMap(raw)
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
	if cursor := batch7Lane3Text(result, "nextCursor", ""); cursor != "" {
		output["next_cursor"] = cursor
	}
	return output, nil
}

func batch7Lane3N8nGetExecution(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3N8nConfig(rt)
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
	path := "/api/v1/executions/" + batch7Lane3PathEscape(executionID)
	result, ok := batch7Lane3N8nCall(ctx, rt, http.MethodGet, batch7Lane3AddPrefix(base, path), headers, nil, params)
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

// ------------------------------------------------------------------ tines ---

var batch7Lane3TinesTool = &Tool{
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

func batch7Lane3TinesImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"tines_list_stories":    batch7Lane3TinesListStories,
		"tines_get_story":       batch7Lane3TinesGetStory,
		"tines_list_actions":    batch7Lane3TinesListActions,
		"tines_get_action":      batch7Lane3TinesGetAction,
		"tines_get_action_logs": batch7Lane3TinesGetActionLogs,
	}
}

// batch7Lane3TinesConfig mirrors upstream _get_config: TINES_DOMAIN plus
// Bearer API key against https://<domain>/api/v1.
func batch7Lane3TinesConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	apiKey := batch7Lane3ModuleToken(rt, "tines_tool")
	root := batch7Lane3ModuleURL(rt, "tines_tool")
	if root == "" {
		domain := strings.TrimRight(strings.TrimSpace(batch7Lane3Getenv(rt, "TINES_DOMAIN")), "/")
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

func batch7Lane3TinesListStories(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"per_page": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "per_page", 20), 500))}}
	if teamID := intArg(input, "team_id", 0); teamID > 0 {
		params.Set("team_id", strconv.Itoa(teamID))
	}
	if search := stringArg(input, "search"); search != "" {
		params.Set("search", search)
	}
	data, ok := batch7Lane3Get(ctx, rt, "Tines", batch7Lane3AddPrefix(base, "/stories"), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	stories := batch7Lane3ListOrEmpty(data["stories"])
	out := make([]any, 0, len(stories))
	for _, raw := range stories {
		story := batch4AsMap(raw)
		out = append(out, map[string]any{
			"id":          story["id"],
			"name":        story["name"],
			"description": story["description"],
			"disabled":    story["disabled"],
			"mode":        story["mode"],
			"team_id":     story["team_id"],
			"tags":        batch7Lane3ListOrEmpty(story["tags"]),
			"created_at":  story["created_at"],
			"updated_at":  story["updated_at"],
		})
	}
	return map[string]any{"count": len(out), "stories": out}, nil
}

func batch7Lane3TinesGetStory(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	storyID := intArg(input, "story_id", 0)
	if storyID <= 0 {
		return map[string]any{"error": "story_id is required"}, nil
	}
	path := "/stories/" + strconv.Itoa(storyID)
	data, ok := batch7Lane3Get(ctx, rt, "Tines", batch7Lane3AddPrefix(base, path), headers, nil, batch7Lane3DefaultTimeout)
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
		"tags":                  batch7Lane3ListOrEmpty(data["tags"]),
		"send_to_story_enabled": data["send_to_story_enabled"],
		"entry_agent_id":        data["entry_agent_id"],
		"exit_agents":           batch7Lane3ListOrEmpty(data["exit_agents"]),
		"created_at":            data["created_at"],
		"updated_at":            data["updated_at"],
	}, nil
}

func batch7Lane3TinesListActions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"per_page": []string{strconv.Itoa(batch7Lane3MinInt(intArg(input, "per_page", 20), 500))}}
	if storyID := intArg(input, "story_id", 0); storyID > 0 {
		params.Set("story_id", strconv.Itoa(storyID))
	}
	if actionType := stringArg(input, "action_type"); actionType != "" {
		params.Set("action_type", actionType)
	}
	data, ok := batch7Lane3Get(ctx, rt, "Tines", batch7Lane3AddPrefix(base, "/actions"), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	agents := batch7Lane3ListOrEmpty(data["agents"])
	out := make([]any, 0, len(agents))
	for _, raw := range agents {
		agent := batch4AsMap(raw)
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

func batch7Lane3TinesGetAction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TinesConfig(rt)
	if failure != nil {
		return failure, nil
	}
	actionID := intArg(input, "action_id", 0)
	if actionID <= 0 {
		return map[string]any{"error": "action_id is required"}, nil
	}
	path := "/actions/" + strconv.Itoa(actionID)
	data, ok := batch7Lane3Get(ctx, rt, "Tines", batch7Lane3AddPrefix(base, path), headers, nil, batch7Lane3DefaultTimeout)
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
		"sources":     batch7Lane3ListOrEmpty(data["sources"]),
		"receivers":   batch7Lane3ListOrEmpty(data["receivers"]),
		"options":     batch4AsMap(data["options"]),
		"created_at":  data["created_at"],
		"updated_at":  data["updated_at"],
	}, nil
}

func batch7Lane3TinesGetActionLogs(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := batch7Lane3TinesConfig(rt)
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
	data, ok := batch7Lane3Get(ctx, rt, "Tines", batch7Lane3AddPrefix(base, path), headers, params, batch7Lane3DefaultTimeout)
	if !ok {
		return data, nil
	}
	logs := batch7Lane3ListOrEmpty(data["action_logs"])
	out := make([]any, 0, len(logs))
	for _, raw := range logs {
		entry := batch4AsMap(raw)
		out = append(out, map[string]any{
			"id":         entry["id"],
			"level":      entry["level"],
			"message":    entry["message"],
			"created_at": entry["created_at"],
		})
	}
	return map[string]any{"count": len(out), "logs": out}, nil
}

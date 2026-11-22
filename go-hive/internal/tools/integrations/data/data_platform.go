// Batch 6 lane 4 of the integration port: data-platform services.
//
// Covers snowflake (3 actions), bigquery (2), databricks (8), redshift (5),
// azure_sql (5), powerbi (5) and sap (5) — 33 actions total. Every module is
// a REST/connector wrapper around the same public API the upstream Python
// module targets; no database driver is used.
//
// Requests run on Runtime.HTTP so each action is exercisable against an
// httptest server. Credentials resolve from WithCredential, the module's
// environment variables, or WithGetenv. Modules that need more than one
// credential field (redshift, sap, azure_sql subscription, bigquery project)
// read the extra fields from the environment; the single-token
// WithCredential shortcut only supplies the bearer/access-key slot.
//
// Deliberate limits, stated per module:
//   - bigquery: upstream uses google-cloud-bigquery. This port calls the
//     BigQuery REST API directly and can mint an OAuth token from a service
//     account JSON (GOOGLE_APPLICATION_CREDENTIALS), but does not read gcloud
//     ADC files. BIGQUERY_ACCESS_TOKEN may be supplied instead.
//   - snowflake: the base URL is derived from SNOWFLAKE_ACCOUNT; with
//     WithBaseURL set (tests) the account is not required.
//   - redshift: SigV4 is implemented natively; only long-term IAM access keys
//     are supported (no STS/session-token credentials).
//   - sap: upstream also supports a credential-store adapter; this port reads
//     SAP_BASE_URL/SAP_USERNAME/SAP_PASSWORD (or WithBaseURL plus env) only.
package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const platformMaxBody = 8 << 20

// newBatch6Lane4Registry builds a registry containing only the lane-4 tools,
// sharing the Runtime surface used by the rest of the port.
func NewPlatformRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: platformModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := newRegistry(runtime)
	tools := []*Tool{
		snowflakeTool,
		bigQueryTool,
		databricksTool,
		redshiftTool,
		azureSQLTool,
		powerBITool,
		sapTool,
	}
	for _, tool := range tools {
		registry.AddTool(tool)
	}
	impls := []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"snowflake", snowflakeImpls()},
		{"bigquery", bigQueryImpls()},
		{"databricks", databricksImpls()},
		{"redshift", redshiftImpls()},
		{"azure_sql", azureSQLImpls()},
		{"powerbi", powerBIToolImpls()},
		{"sap", sapImpls()},
	}
	for _, entry := range impls {
		for action, impl := range entry.funcs {
			registry.AddImpl(entry.tool, action, impl)
		}
	}
	return registry
}

// platformModules declares the public endpoints and credential variables
// for the seven modules. Tests override BaseURL with WithBaseURL.
func platformModules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"snowflake_tool": {
			ID:      "snowflake_tool",
			EnvVars: []string{"SNOWFLAKE_TOKEN"},
		},
		"bigquery_tool": {
			ID:      "bigquery_tool",
			BaseURL: "https://bigquery.googleapis.com/bigquery/v2",
			EnvVars: []string{"BIGQUERY_ACCESS_TOKEN"},
		},
		"databricks_tool": {
			ID:      "databricks_tool",
			EnvVars: []string{"DATABRICKS_TOKEN"},
		},
		"redshift_tool": {
			ID:      "redshift_tool",
			EnvVars: []string{"AWS_ACCESS_KEY_ID"},
		},
		"azure_sql_tool": {
			ID:      "azure_sql_tool",
			BaseURL: "https://management.azure.com",
			EnvVars: []string{"AZURE_SQL_ACCESS_TOKEN"},
		},
		"powerbi_tool": {
			ID:      "powerbi_tool",
			BaseURL: "https://api.powerbi.com/v1.0/myorg",
			EnvVars: []string{"POWERBI_ACCESS_TOKEN"},
		},
		"sap_tool": {
			ID:      "sap_tool",
			EnvVars: []string{"SAP_USERNAME"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// platformResponse is one completed HTTP exchange.
type platformResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// send performs one HTTP request with an optional context timeout.
// Transport failures are returned as Go errors; HTTP status handling belongs
// to the caller.
func send(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, body []byte, timeout time.Duration) (*platformResponse, error) {
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
	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, platformMaxBody))
	if err != nil {
		return nil, err
	}
	return &platformResponse{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

// doJSON marshals payload as JSON and sends it.
func doJSON(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, timeout time.Duration) (*platformResponse, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	return send(ctx, rt, method, endpoint, headers, body, timeout)
}

// doForm posts an application/x-www-form-urlencoded body.
func doForm(ctx context.Context, rt *Runtime, endpoint string, form url.Values, timeout time.Duration) (*platformResponse, error) {
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	return send(ctx, rt, http.MethodPost, endpoint, headers, []byte(form.Encode()), timeout)
}

// httpError renders the upstream "HTTP <code>: <body[:500]>" error.
func httpError(resp *platformResponse) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", resp.Status, truncateText(string(resp.Body), 500)),
	}
}

// intOr reads an integer field with a fallback.
func intOr(object map[string]any, name string, fallback int64) int64 {
	if parsed, ok := int64Value(object[name]); ok {
		return parsed
	}
	return fallback
}

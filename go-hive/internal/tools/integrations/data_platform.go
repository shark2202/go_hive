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
package integrations

import (
	"bytes"
	"context"
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

	"github.com/aden-hive/hive-go/internal/tools"
)

const batch6Lane4MaxBody = 8 << 20

// InstallBatch6Lane4 publishes the batch-6 lane-4 data-platform tools into a
// shared registry and returns the number of tools registered.
func InstallBatch6Lane4(target *tools.ToolRegistry) int {
	return newBatch6Lane4Registry().Install(target)
}

// newBatch6Lane4Registry builds a registry containing only the lane-4 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane4Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch6Lane4Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	tools := []*Tool{
		batch6Lane4SnowflakeTool,
		batch6Lane4BigQueryTool,
		batch6Lane4DatabricksTool,
		batch6Lane4RedshiftTool,
		batch6Lane4AzureSQLTool,
		batch6Lane4PowerBITool,
		batch6Lane4SAPTool,
	}
	for _, tool := range tools {
		registry.AddTool(tool)
	}
	impls := []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"snowflake", batch6Lane4SnowflakeImpls()},
		{"bigquery", batch6Lane4BigQueryImpls()},
		{"databricks", batch6Lane4DatabricksImpls()},
		{"redshift", batch6Lane4RedshiftImpls()},
		{"azure_sql", batch6Lane4AzureSQLImpls()},
		{"powerbi", batch6Lane4PowerBIToolImpls()},
		{"sap", batch6Lane4SAPImpls()},
	}
	for _, entry := range impls {
		for action, impl := range entry.funcs {
			registry.AddImpl(entry.tool, action, impl)
		}
	}
	return registry
}

// batch6Lane4Modules declares the public endpoints and credential variables
// for the seven modules. Tests override BaseURL with WithBaseURL.
func batch6Lane4Modules() map[string]*ModuleConfig {
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

// batch6Lane4Getenv resolves an environment variable through the runtime.
func batch6Lane4Getenv(rt *Runtime, name string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv(name)
	}
	return os.Getenv(name)
}

// batch6Lane4ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func batch6Lane4ModuleToken(rt *Runtime, moduleID string) string {
	return batch4ModuleToken(rt, moduleID)
}

// batch6Lane4ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch6Lane4ModuleURL(rt *Runtime, moduleID string) string {
	return batch4ModuleURL(rt, moduleID)
}

// batch6Lane4Response is one completed HTTP exchange.
type batch6Lane4Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// batch6Lane4Send performs one HTTP request with an optional context timeout.
// Transport failures are returned as Go errors; HTTP status handling belongs
// to the caller.
func batch6Lane4Send(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, body []byte, timeout time.Duration) (*batch6Lane4Response, error) {
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch6Lane4MaxBody))
	if err != nil {
		return nil, err
	}
	return &batch6Lane4Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

// batch6Lane4DoJSON marshals payload as JSON and sends it.
func batch6Lane4DoJSON(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, timeout time.Duration) (*batch6Lane4Response, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	return batch6Lane4Send(ctx, rt, method, endpoint, headers, body, timeout)
}

// batch6Lane4DoForm posts an application/x-www-form-urlencoded body.
func batch6Lane4DoForm(ctx context.Context, rt *Runtime, endpoint string, form url.Values, timeout time.Duration) (*batch6Lane4Response, error) {
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	return batch6Lane4Send(ctx, rt, http.MethodPost, endpoint, headers, []byte(form.Encode()), timeout)
}

// batch6Lane4JSONObject decodes a JSON object body.
func batch6Lane4JSONObject(raw []byte) (map[string]any, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

// batch6Lane4HTTPError renders the upstream "HTTP <code>: <body[:500]>" error.
func batch6Lane4HTTPError(resp *batch6Lane4Response) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", resp.Status, batch4TruncateText(string(resp.Body), 500)),
	}
}

// batch6Lane4TransportError renders a labelled transport failure.
func batch6Lane4TransportError(ctx context.Context, label string, err error) map[string]any {
	if batch4IsTimeout(ctx, err) {
		return map[string]any{"error": "Request to " + label + " timed out"}
	}
	return map[string]any{"error": label + " request failed: " + err.Error()}
}

// batch6Lane4StringOr reads a string field with a fallback.
func batch6Lane4StringOr(object map[string]any, name, fallback string) string {
	if object == nil {
		return fallback
	}
	if text, ok := object[name].(string); ok {
		return text
	}
	return fallback
}

// batch6Lane4Int64 converts common JSON numeric representations.
func batch6Lane4Int64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// batch6Lane4IntOr reads an integer field with a fallback.
func batch6Lane4IntOr(object map[string]any, name string, fallback int64) int64 {
	if parsed, ok := batch6Lane4Int64(object[name]); ok {
		return parsed
	}
	return fallback
}

// batch6Lane4Bool reads a decoded JSON boolean.
func batch6Lane4Bool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// batch6Lane4Clamp bounds an integer to [low, high].
func batch6Lane4Clamp(value, low, high int) int {
	return batch4Clamp(value, low, high)
}

// batch6Lane4IsTimeout reports whether err is a deadline or a net timeout.
func batch6Lane4IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch6Lane4PathEscape escapes one URL path segment.
func batch6Lane4PathEscape(value string) string {
	return url.PathEscape(value)
}

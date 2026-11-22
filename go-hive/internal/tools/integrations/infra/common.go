package infra

// Shared request/decoding helpers: one bounded HTTP path (send/doJSON), the
// reply triple and the decoded-JSON accessors every action uses. The kit seam
// helpers below keep the historical unqualified names on top of
// integrationkit, so provider code reads as it did in the integrations root.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// ------------------------------------------------------------ kit seam ---

// runtimeEnv reads one environment variable through the runtime lookup.
func runtimeEnv(rt *Runtime, name string) string { return rt.EnvOrOS(name) }

// moduleBaseURL resolves a module base URL, honouring WithBaseURL.
func moduleBaseURL(rt *Runtime, moduleID string) string { return rt.ModuleBaseURL(moduleID) }

// runtimeHTTP returns the runtime transport, falling back to a default client.
func runtimeHTTP(rt *Runtime) *httpClient { return rt.HTTPClientOrNew() }

// moduleToken resolves the module's own credential from WithCredential or the
// runtime environment lookup.
func moduleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	token, _ := cfg.ResolvedToken(getenv)
	return strings.TrimSpace(token)
}

// truncateText caps provider error/description text the way upstream slices
// response.text[:500].
func truncateText(text string, limit int) string {
	return integrationkit.TruncateText(text, limit)
}

// transportError renders the upstream timeout/request-failed split.
func transportError(ctx context.Context, label string, err error) map[string]any {
	return integrationkit.TransportError(ctx, label, err)
}

// jsonObject decodes a JSON object body.
func jsonObject(raw []byte) (map[string]any, bool) { return integrationkit.JSONObject(raw) }

// asMap safely views a decoded JSON object.
func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

// asSlice safely views a decoded JSON array.
func asSlice(value any) []any { return integrationkit.AsSlice(value) }

// asString reads a decoded JSON string.
func asString(value any) string { return integrationkit.AsString(value) }

// asBool reads a decoded JSON boolean.
func asBool(value any) bool { return integrationkit.AsBool(value) }

// stringOr reads a decoded JSON string with the upstream fallback.
func stringOr(object map[string]any, name, fallback string) string {
	return integrationkit.StringOr(object, name, fallback)
}

// pathEscape escapes one URL path segment.
func pathEscape(value string) string { return integrationkit.PathEscape(value) }

// isTimeout reports whether err is a deadline or a net timeout.
func isTimeout(_ context.Context, err error) bool { return integrationkit.IsTimeout(err) }

// ------------------------------------------------------------ shared bits ---

// endpoint resolves a module endpoint: WithBaseURL first, then the
// module's environment variable, then the given default.
func endpoint(rt *Runtime, moduleID, envName, fallback string) string {
	if base := moduleBaseURL(rt, moduleID); base != "" {
		return base
	}
	if value := strings.TrimRight(strings.TrimSpace(runtimeEnv(rt, envName)), "/"); value != "" {
		return value
	}
	return fallback
}

// httpResponse is one completed HTTP exchange.
type httpResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// send performs one HTTP request with an optional context timeout.
// Transport failures are returned as Go errors; HTTP status handling belongs
// to the caller.
func send(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, body []byte, timeout time.Duration) (*httpResponse, error) {
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return &httpResponse{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

// doJSON marshals payload as JSON and sends it.
func doJSON(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any, timeout time.Duration) (*httpResponse, error) {
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

// httpError renders the upstream "HTTP <code>: <body[:500]>" error.
func httpError(resp *httpResponse) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", resp.Status, truncateText(string(resp.Body), 500)),
	}
}

// getJSON performs a GET and decodes the JSON object, or returns the
// upstream HTTP error dict.
func getJSON(ctx context.Context, rt *Runtime, label, endpoint string, headers map[string]string, params url.Values, timeout time.Duration) (map[string]any, bool) {
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	resp, err := doJSON(ctx, rt, http.MethodGet, endpoint, headers, nil, timeout)
	if err != nil {
		return transportError(ctx, label, err), false
	}
	if resp.Status >= 400 {
		return httpError(resp), false
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": label + " API error: invalid JSON response"}, false
	}
	return data, true
}

// failed reports whether a result carries an upstream error.
func failed(result map[string]any) bool {
	_, failed := result["error"]
	return failed
}

// minInt returns the smaller of value and limit, matching the
// upstream "min(x, max)" clamps.
func minInt(value, limit int) int {
	if value < limit {
		return value
	}
	return limit
}

// intField reads a numeric JSON field with a fallback.
func intField(object map[string]any, name string, fallback int) int {
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

// listOrEmpty views a decoded JSON array, defaulting to empty.
func listOrEmpty(value any) []any {
	if items := asSlice(value); items != nil {
		return items
	}
	return []any{}
}

// addPrefix joins a base URL and an absolute path.
func addPrefix(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

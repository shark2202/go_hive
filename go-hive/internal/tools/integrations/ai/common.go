package ai

// Shared request/decoding helpers used by every provider file: one bounded
// HTTP path (doRequest), the reply triple and the decoded-JSON accessors
// every action uses. The accessors forward to integrationkit so there is a
// single implementation shared with the root integrations package.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// ---- integrationkit accessors ----------------------------------------------

func isTimeout(_ context.Context, err error) bool { return integrationkit.IsTimeout(err) }

func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

func asSlice(value any) []any { return integrationkit.AsSlice(value) }

func asString(value any) string { return integrationkit.AsString(value) }

func boolOr(value any, fallback bool) bool { return integrationkit.BoolOr(value, fallback) }

func mapOrEmpty(value any) map[string]any { return integrationkit.MapOrEmpty(value) }

func jsonNumber(value any) any { return integrationkit.JSONNumber(value) }

func decodeObject(raw []byte) (map[string]any, bool) { return integrationkit.DecodeObject(raw) }

func decodeAny(raw []byte) (any, bool) { return integrationkit.DecodeAny(raw) }

func clampInt(value, low, high int) int { return integrationkit.ClampInt(value, low, high) }

func truncateText(text string, limit int) string { return integrationkit.TruncateText(text, limit) }

func splitCSV(raw string) []string { return integrationkit.SplitCSV(raw) }

func basicAuth(user, password string) string { return integrationkit.BasicAuth(user, password) }

func stringArg(input map[string]any, name string) string {
	return integrationkit.StringArg(input, name)
}

func intArg(input map[string]any, name string, fallback int) int {
	return integrationkit.IntArg(input, name, fallback)
}

func floatArg(input map[string]any, name string, fallback float64) float64 {
	return integrationkit.FloatArg(input, name, fallback)
}

func boolArg(input map[string]any, name string, fallback bool) bool {
	return integrationkit.BoolArg(input, name, fallback)
}

func stringID(value any) string { return integrationkit.StringID(value) }

func param(typ string) map[string]any { return integrationkit.Param(typ) }

func paramDefault(typ string, value any) map[string]any {
	return integrationkit.ParamDefault(typ, value)
}

func moduleBaseURL(rt *Runtime, moduleID string) string { return rt.ModuleBaseURL(moduleID) }

func runtimeHTTP(rt *Runtime) *httpClient { return rt.HTTPClientOrNew() }

func runtimeGetenv(rt *Runtime) func(string) string { return rt.GetenvFunc() }

// ---- shared request and value helpers --------------------------------------

// moduleToken resolves a single-token module credential from
// WithCredential first, then the configured environment lookup.
func moduleToken(rt *Runtime, moduleID string) string {
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

// httpReply is the status/body pair every module helper inspects.
type httpReply struct {
	Status      int
	Body        []byte
	ContentType string
}

// doRequest performs one JSON request and reads a bounded body.
func doRequest(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (httpReply, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return httpReply{}, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return httpReply{}, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return httpReply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return httpReply{}, err
	}
	return httpReply{
		Status:      resp.StatusCode,
		Body:        raw,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// anyList renders a decoded JSON array, normalising a missing field
// to an empty (non-nil) slice so it encodes as [].
func anyList(value any) []any {
	items, ok := value.([]any)
	if !ok || items == nil {
		return []any{}
	}
	return items
}

// floatOr reads a number with a fallback.
func floatOr(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
	}
	return fallback
}

// intOr reads an integer with a fallback.
func intOr(value any, fallback int) int {
	switch typed := value.(type) {
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

// stringList renders a decoded JSON array of names as []string. A
// missing field produces an empty (non-nil) slice so it encodes as [].
func stringList(value any) []string {
	items := asSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asString(item))
	}
	return out
}

// firstN bounds a name list the way upstream tags[:10] does.
func firstN(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

// round3 mirrors Python round(value, 3).
func round3(value float64) float64 {
	return math.Round(value*1000) / 1000
}

// ------------------------------------------------------------ tiny helpers ---

// stringDefault reads a string value with a fallback.
func stringDefault(value any, fallback string) string {
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return fallback
}

// toStrings renders a decoded JSON array as []string.
func toStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, stringID(value))
	}
	return out
}

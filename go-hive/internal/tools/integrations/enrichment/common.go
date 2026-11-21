package enrichment

// Shared request/decoding helpers: one bounded JSON request path (doJSON),
// the module base-URL and credential resolvers, and the decoded-JSON
// accessors every provider action uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// baseURL resolves a module root with a hard-coded fallback.
func baseURL(rt *Runtime, moduleID, fallback string) string {
	if base := moduleURL(rt, moduleID); base != "" {
		return base
	}
	return fallback
}

// moduleToken resolves the module credential from WithCredential or
// the configured environment lookup.
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
	Status int
	Body   []byte
}

// doJSON performs one JSON request and reads a bounded body.
func doJSON(
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
	return httpReply{Status: resp.StatusCode, Body: raw}, nil
}

// validQuery mirrors the upstream 1..500 character check.
func validQuery(query string) bool {
	length := utf8.RuneCountInString(query)
	return length >= 1 && length <= maxQueryChars
}

// errorDetail mirrors the upstream
// `response.json().get("error", response.text)` fallback chain.
func errorDetail(raw []byte) string {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		if value, present := object["error"]; present {
			if text, ok := value.(string); ok {
				return text
			}
			return integrationkit.JSONString(value)
		}
	}
	return string(raw)
}

// sliceOr keeps a decoded array, defaulting to an empty array.
func sliceOr(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return []any{}
}

// firstNonEmpty mirrors Python's `a or b` for optional string
// fields: an empty string falls through to the next candidate.
func firstNonEmpty(values ...any) any {
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

// interfaceSlice renders []string as []any for JSON encoding that
// matches the upstream list payloads.
func interfaceSlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// runtimeGetenv resolves the runtime environment lookup.
func runtimeGetenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// moduleURL resolves a module base URL, honouring WithBaseURL.
func moduleURL(rt *Runtime, moduleID string) string {
	if rt == nil {
		return ""
	}
	return rt.ModuleBaseURL(moduleID)
}

// runtimeHTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func runtimeHTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return integrationkit.NewHTTPClient(nil)
}

// asMap safely views a decoded JSON object.
func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

// asSlice safely views a decoded JSON array.
func asSlice(value any) []any { return integrationkit.AsSlice(value) }

// asString reads a decoded JSON string.
func asString(value any) string { return integrationkit.AsString(value) }

// decodeAny decodes a JSON body of any shape.
func decodeAny(raw []byte) (any, bool) { return integrationkit.DecodeAny(raw) }

// decodeObject decodes a JSON object body.
func decodeObject(raw []byte) (map[string]any, bool) {
	return integrationkit.DecodeObject(raw)
}

// isTimeout reports whether err is a deadline or a net timeout.
func isTimeout(_ context.Context, err error) bool { return integrationkit.IsTimeout(err) }

// jsonNumber turns integral float64 values back into integers so responses
// render like Python ints.
func jsonNumber(value any) any { return integrationkit.JSONNumber(value) }

// minInt returns the smaller of two integers.
func minInt(left, right int) int { return integrationkit.MinInt(left, right) }

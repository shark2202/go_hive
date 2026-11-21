package webintel

// Shared request/decoding helpers: one bounded HTTP path (doHTTP), the
// decoded-JSON accessors and the module base-URL/credential resolvers every
// action uses.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// moduleToken resolves the module credential from WithCredential or the
// configured environment lookup.
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

// moduleBaseURL resolves a module base URL, honouring WithBaseURL.
func moduleBaseURL(rt *Runtime, moduleID string) string {
	return rt.ModuleBaseURL(moduleID)
}

// runtimeNow resolves the runtime clock.
func runtimeNow(rt *Runtime) time.Time {
	return rt.NowOrWall()
}

// asMap/asSlice/asString are the JSON accessors the action projections use.
func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

func asSlice(value any) []any { return integrationkit.AsSlice(value) }

func asString(value any) string { return integrationkit.AsString(value) }

// isTimeout reports whether a transport error hit the client timeout.
func isTimeout(_ context.Context, err error) bool {
	return integrationkit.IsTimeout(err)
}

// ------------------------------------------------------------ shared bits ---

// truncateText caps text at limit runes, matching upstream slicing on
// Python strings.
func truncateText(text string, limit int) string {
	if len([]rune(text)) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// mapOrEmpty views a decoded object, substituting {} when absent.
func mapOrEmpty(value any) map[string]any {
	if object := asMap(value); object != nil {
		return object
	}
	return map[string]any{}
}

// sliceOrEmpty views a decoded array, substituting [] when absent.
func sliceOrEmpty(value any) []any {
	if items := asSlice(value); items != nil {
		return items
	}
	return []any{}
}

// stringOr reads a string field with a fallback alternative: the
// second value wins only when the first is an empty string, mirroring the
// upstream `a or b` projections.
func stringOr(primary, fallback any) string {
	if text := asString(primary); text != "" {
		return text
	}
	return asString(fallback)
}

// httpResponse is one completed HTTP exchange with its decoded
// JSON body (when the body is valid JSON).
type httpResponse struct {
	Status  int
	Body    []byte
	Parsed  any
	Decoded bool
}

// doHTTP performs one request through the runtime transport, reading
// the body under the shared size cap. Transport failures are returned as Go
// errors; HTTP status handling belongs to the caller.
func doHTTP(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any) (*httpResponse, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	client := integrationkit.NewHTTPClient(nil)
	if rt != nil && rt.HTTP != nil {
		client = rt.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	out := &httpResponse{Status: resp.StatusCode, Body: raw}
	if len(raw) > 0 {
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err == nil {
			out.Parsed = parsed
			out.Decoded = true
		}
	}
	return out, nil
}

// joinQuery appends a non-empty query string to a URL.
func joinQuery(endpoint string, query url.Values) string {
	if encoded := query.Encode(); encoded != "" {
		return endpoint + "?" + encoded
	}
	return endpoint
}

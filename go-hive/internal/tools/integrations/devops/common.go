package devops

// Shared request/decoding helpers: one bounded HTTP path (doRequest), the
// reply triple and the decoded-JSON accessors every action uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// runtimeGetenv resolves the runtime environment lookup.
func runtimeGetenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// moduleURL resolves a module base URL, honouring WithBaseURL.
func moduleURL(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
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

// runtimeHTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func runtimeHTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return integrationkit.NewHTTPClient(nil)
}

// runtimeNow resolves the runtime clock.
func runtimeNow(rt *Runtime) time.Time {
	if rt != nil && rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// httpReply is the status/body/header triple the module helpers inspect.
type httpReply struct {
	Status int
	Body   []byte
	Header http.Header
}

// doRequest performs one request with a raw body and reads a bounded body.
func doRequest(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	body []byte,
) (httpReply, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return httpReply{}, err
	}
	for name, value := range headers {
		// net/http derives Host from the URL; setting the header is a no-op.
		if strings.EqualFold(name, "host") {
			continue
		}
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
	return httpReply{Status: resp.StatusCode, Body: raw, Header: resp.Header.Clone()}, nil
}

// jsonRequest performs one JSON request.
func jsonRequest(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (httpReply, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return httpReply{}, err
		}
		body = encoded
	}
	return doRequest(ctx, rt, method, target, headers, body)
}

// isTimeout reports whether err is a deadline or a net timeout.
func isTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// truncateText caps provider error/description text the same way
// upstream slices response.text[:500].
func truncateText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// clampInt bounds an integer to [low, high].
func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// asMap safely views a decoded JSON object.
func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// asSlice safely views a decoded JSON array.
func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// asString reads a decoded JSON string.
func asString(value any) string {
	text, _ := value.(string)
	return text
}

// asBool reads a decoded JSON boolean with the upstream
// `.get(key, fallback)` default applied when the field is absent.
func asBool(value any, fallback bool) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}

// asNumber turns integral float64 values (the form encoding/json
// produces) back into integers so responses render like Python ints.
func asNumber(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
		return typed
	default:
		return value
	}
}

// intValue reads a decoded JSON number with a default.
func intValue(value any, fallback int) int {
	switch typed := asNumber(value).(type) {
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

// stringList renders a decoded JSON array of strings. A missing
// field produces an empty (non-nil) slice so it encodes as [].
func stringList(value any) []string {
	items := asSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asString(item))
	}
	return out
}

// usernames renders a decoded JSON array of user objects as the
// upstream `[a.get("username", "") for a in ...]` list.
func usernames(value any) []string {
	items := asSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asString(asMap(item)["username"]))
	}
	return out
}

// decodeAny decodes a JSON body of any shape.
func decodeAny(raw []byte) (any, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

// decodeObject decodes a JSON object body.
func decodeObject(raw []byte) (map[string]any, bool) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, true
}

// splitIDs parses the upstream comma-separated integer list used by
// gitlab_create_issue / gitlab_update_issue.
func splitIDs(raw string) ([]int, bool) {
	out := make([]int, 0)
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return nil, false
		}
		out = append(out, parsed)
	}
	return out, true
}

// headerOrNil renders a response header the way Python's
// resp.headers.get(name) does: the value or None.
func headerOrNil(header http.Header, name string) any {
	if value := header.Get(name); value != "" {
		return value
	}
	return nil
}

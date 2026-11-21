package integrationkit

// HTTP helpers shared by the network-backed integrations: one bounded GET/POST
// with the runtime transport, JSON decoding, and the model-facing error
// mapping used when a provider call fails.
//
// Errors are values: a transport failure, a non-2xx status or a malformed body
// becomes a {"error": ...} map the model can read, never a Go error — the same
// convention the registry uses for action failures.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Errorf builds the model-facing error envelope.
func Errorf(format string, args ...any) map[string]any {
	return map[string]any{"error": fmt.Sprintf(format, args...)}
}

// GetBytes performs one GET and returns the status, body and transport error.
// Redirect handling is left to the caller's http.Client.
func (rt *Runtime) GetBytes(ctx context.Context, target string, headers map[string]string) (int, []byte, error) {
	status, _, body, err := rt.GetBytesWithHeaders(ctx, target, headers)
	return status, body, err
}

// GetBytesWithHeaders is GetBytes plus the response headers (content type).
func (rt *Runtime) GetBytesWithHeaders(ctx context.Context, target string, headers map[string]string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := rt.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if readErr != nil {
		return resp.StatusCode, resp.Header, nil, readErr
	}
	return resp.StatusCode, resp.Header, body, nil
}

// GetJSON fetches a JSON document. A non-2xx status or malformed body is
// reported as a model-facing error map rather than a Go error.
func (rt *Runtime) GetJSON(ctx context.Context, target string, headers map[string]string) (any, map[string]any) {
	status, body, err := rt.GetBytes(ctx, target, headers)
	if err != nil {
		if ctx.Err() != nil {
			return nil, Errorf("Request timed out")
		}
		return nil, Errorf("Network error: %s", err.Error())
	}
	if status < 200 || status >= 300 {
		return nil, Errorf("HTTP %d: %s", status, Snippet(body))
	}
	var parsed any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, Errorf("Invalid JSON response: %s", err.Error())
		}
	}
	return parsed, nil
}

// GetJSONObject is GetJSON narrowed to a JSON object.
func (rt *Runtime) GetJSONObject(ctx context.Context, target string, headers map[string]string) (map[string]any, map[string]any) {
	parsed, problem := rt.GetJSON(ctx, target, headers)
	if problem != nil {
		return nil, problem
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, Errorf("Unexpected response shape: expected a JSON object")
	}
	return object, nil
}

// PostForm posts an application/x-www-form-urlencoded body.
func (rt *Runtime) PostForm(ctx context.Context, target string, form url.Values, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := rt.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if readErr != nil {
		return resp.StatusCode, nil, readErr
	}
	return resp.StatusCode, body, nil
}

// ErrTimeout reports whether an error (or a wrapped cause) is a timeout.
func ErrTimeout(err error) bool {
	for err != nil {
		if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// DecodeJSONObject decodes a JSON object body, reporting malformed documents
// as a model-facing error.
func DecodeJSONObject(body []byte) (map[string]any, map[string]any) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, Errorf("Invalid JSON response: %s", err.Error())
	}
	if parsed == nil {
		return map[string]any{}, nil
	}
	return parsed, nil
}

// Snippet trims a response body for error messages.
func Snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200]
	}
	if text == "" {
		return "(empty body)"
	}
	return text
}

// Object reads a nested object, returning an empty map when absent.
func Object(root map[string]any, key string) map[string]any {
	if value, ok := root[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

// Array reads a nested array, returning nil when absent.
func Array(root map[string]any, key string) []any {
	if value, ok := root[key].([]any); ok {
		return value
	}
	return nil
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// StripHTMLTags mirrors wikipedia_tool's regex-based tag removal.
func StripHTMLTags(text string) string {
	return tagPattern.ReplaceAllString(text, "")
}

// EntityPattern matches the HTML entities UnescapeHTML decodes. Provider
// files that post-process scraped markup with the same rule use it directly.
var EntityPattern = regexp.MustCompile(`&(?:#(\d+)|#[xX]([0-9a-fA-F]+)|([a-zA-Z]+));`)

// UnescapeHTML decodes the small entity set Wikipedia and DuckDuckGo emit.
func UnescapeHTML(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	named := map[string]string{
		"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": " ",
		"hellip": "…", "mdash": "—", "ndash": "–", "rsquo": "’", "lsquo": "‘",
		"ldquo": "“", "rdquo": "”",
	}
	return EntityPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := EntityPattern.FindStringSubmatch(match)
		if parts[1] != "" {
			if code, err := strconv.Atoi(parts[1]); err == nil && code > 0 && code < 0x110000 {
				return string(rune(code))
			}
			return match
		}
		if parts[2] != "" {
			if code, err := strconv.ParseInt(parts[2], 16, 32); err == nil && code > 0 && code < 0x110000 {
				return string(rune(code))
			}
			return match
		}
		if value, ok := named[strings.ToLower(parts[3])]; ok {
			return value
		}
		return match
	})
}

// CleanHTMLText strips tags, decodes entities and collapses whitespace.
func CleanHTMLText(text string) string {
	return strings.Join(strings.Fields(UnescapeHTML(StripHTMLTags(text))), " ")
}

// Number coerces a JSON number (or numeric string) to a float64.
func Number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// Round rounds to two decimals, matching Python's round(x, 2).
func Round(value float64) float64 {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 2, 64), 64)
	return rounded
}

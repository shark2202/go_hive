package integrations

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

// batch2Get performs one GET and returns the status, body and transport error.
// Redirect handling is left to the caller's http.Client.
func batch2Get(ctx context.Context, rt *Runtime, target string, headers map[string]string) (int, []byte, error) {
	status, _, body, err := batch2GetWithHeaders(ctx, rt, target, headers)
	return status, body, err
}

// batch2GetWithHeaders is batch2Get plus the response headers (content type).
func batch2GetWithHeaders(ctx context.Context, rt *Runtime, target string, headers map[string]string) (int, http.Header, []byte, error) {
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

// batch2GetJSON fetches a JSON document. A non-2xx status or malformed body is
// reported as a model-facing error map rather than a Go error.
func batch2GetJSON(ctx context.Context, rt *Runtime, target string, headers map[string]string) (any, map[string]any) {
	status, body, err := batch2Get(ctx, rt, target, headers)
	if err != nil {
		if ctx.Err() != nil {
			return nil, map[string]any{"error": "Request timed out"}
		}
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	if status < 200 || status >= 300 {
		return nil, map[string]any{"error": fmt.Sprintf("HTTP %d: %s", status, batch2Snippet(body))}
	}
	var parsed any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, map[string]any{"error": "Invalid JSON response: " + err.Error()}
		}
	}
	return parsed, nil
}

// batch2GetObject is batch2GetJSON narrowed to a JSON object.
func batch2GetObject(ctx context.Context, rt *Runtime, target string, headers map[string]string) (map[string]any, map[string]any) {
	parsed, problem := batch2GetJSON(ctx, rt, target, headers)
	if problem != nil {
		return nil, problem
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, map[string]any{"error": "Unexpected response shape: expected a JSON object"}
	}
	return object, nil
}

// batch2PostForm posts an application/x-www-form-urlencoded body.
func batch2PostForm(ctx context.Context, rt *Runtime, target string, form url.Values, headers map[string]string) (int, []byte, error) {
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

// errTimeout reports whether an error (or a wrapped cause) is a timeout.
func errTimeout(err error) bool {
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

// batch2DecodeJSONObject decodes a JSON object body, reporting malformed
// documents as a model-facing error.
func batch2DecodeJSONObject(body []byte) (map[string]any, map[string]any) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, map[string]any{"error": "Invalid JSON response: " + err.Error()}
	}
	if parsed == nil {
		return map[string]any{}, nil
	}
	return parsed, nil
}

// batch2Snippet trims a response body for error messages.
func batch2Snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200]
	}
	if text == "" {
		return "(empty body)"
	}
	return text
}

// batch2Object reads a nested object, returning an empty map when absent.
func batch2Object(root map[string]any, key string) map[string]any {
	if value, ok := root[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

// batch2Array reads a nested array, returning nil when absent.
func batch2Array(root map[string]any, key string) []any {
	if value, ok := root[key].([]any); ok {
		return value
	}
	return nil
}

var batch2TagPattern = regexp.MustCompile(`<[^>]*>`)

// stripHTMLTags mirrors wikipedia_tool's regex-based tag removal.
func stripHTMLTags(text string) string {
	return batch2TagPattern.ReplaceAllString(text, "")
}

var batch2EntityPattern = regexp.MustCompile(`&(?:#(\d+)|#[xX]([0-9a-fA-F]+)|([a-zA-Z]+));`)

// htmlUnescape decodes the small entity set Wikipedia and DuckDuckGo emit.
func htmlUnescape(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	named := map[string]string{
		"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": " ",
		"hellip": "…", "mdash": "—", "ndash": "–", "rsquo": "’", "lsquo": "‘",
		"ldquo": "“", "rdquo": "”",
	}
	return batch2EntityPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := batch2EntityPattern.FindStringSubmatch(match)
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

// cleanHTMLText strips tags, decodes entities and collapses whitespace.
func cleanHTMLText(text string) string {
	return strings.Join(strings.Fields(htmlUnescape(stripHTMLTags(text))), " ")
}

// batch2Number coerces a JSON number (or numeric string) to a float64.
func batch2Number(value any) (float64, bool) {
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

// batch2Round rounds to two decimals, matching Python's round(x, 2).
func batch2Round(value float64) float64 {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 2, 64), 64)
	return rounded
}

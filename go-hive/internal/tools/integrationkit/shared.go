// Shared primitives converged here in phase 2 of the integrations
// restructure: the wave lanes had copied these helpers into every provider
// file under batch-prefixed names. Each function below is the single
// implementation; internal/tools/integrations keeps short seam wrappers so
// provider call sites stay unchanged in shape.
package integrationkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// IsTimeout reports whether err is a deadline or a net timeout.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// AsMap returns value as a map[string]any, or nil when it is not one.
func AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// AsSlice returns value as a []any, or nil when it is not one.
func AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// AsString returns value as a string, or "" when it is not one.
func AsString(value any) string {
	text, _ := value.(string)
	return text
}

// AsBool returns value as a bool, or false when it is not one.
func AsBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// BoolOr returns value when it is a bool, else fallback.
func BoolOr(value any, fallback bool) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}

// SliceOrEmpty returns value as a non-nil []any.
func SliceOrEmpty(value any) []any {
	if items, ok := value.([]any); ok && items != nil {
		return items
	}
	return []any{}
}

// MapOrEmpty returns value as a non-nil map[string]any.
func MapOrEmpty(value any) map[string]any {
	if object, ok := value.(map[string]any); ok && object != nil {
		return object
	}
	return map[string]any{}
}

// JSONNumber normalizes whole floats and json.Number to int64 so encoded
// payloads keep upstream's integer spelling.
func JSONNumber(value any) any {
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

// DecodeObject unmarshals raw into a non-nil object; false when the body is
// not a JSON object.
func DecodeObject(raw []byte) (map[string]any, bool) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, true
}

// JSONObject unmarshals raw into a JSON object; false when the body is empty
// or not an object (nil object stays nil).
func JSONObject(raw []byte) (map[string]any, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

// DecodeAny unmarshals raw into any JSON value.
func DecodeAny(raw []byte) (any, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

// Int64 coerces a JSON scalar (number, numeric string) to int64.
func Int64(value any) (int64, bool) {
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

// OptionalInt reads an optional integer input.
func OptionalInt(input map[string]any, name string) (int, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// OptionalFloat reads an optional float input.
func OptionalFloat(input map[string]any, name string) (float64, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// OptionalString reads an optional string input, stringifying scalars.
func OptionalString(input map[string]any, name string) (string, bool) {
	value, present := input[name]
	if !present || value == nil {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value), true
	}
	return text, true
}

// StringID renders a JSON id value the way upstream does (integers without a
// decimal part, everything else via fmt.Sprint).
func StringID(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

// StringOr reads a nested string, falling back when absent or not a string.
func StringOr(object map[string]any, name, fallback string) string {
	if object == nil {
		return fallback
	}
	if text, ok := object[name].(string); ok {
		return text
	}
	return fallback
}

// ValueOr returns object[key] when present, else fallback.
func ValueOr(object map[string]any, key string, fallback any) any {
	if value, present := object[key]; present {
		return value
	}
	return fallback
}

// ClampInt caps value to [low, high].
func ClampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// ClampFloat caps value to [low, high].
func ClampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// TruncateText caps text at limit runes, like Python slicing.
func TruncateText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// TruncateTextClamped is TruncateText with negative limits clamped to zero.
func TruncateTextClamped(text string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// SplitCSV splits a comma list, trimming entries and dropping empties.
func SplitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// PathEscape escapes one URL path segment.
func PathEscape(value string) string { return url.PathEscape(value) }

// BasicAuth builds the HTTP Basic authorization header value.
func BasicAuth(user, password string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	return "Basic " + encoded
}

// MinInt returns the smaller of two ints.
func MinInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// Failure reports an upstream error payload by looking for the "error" key.
func Failure(result map[string]any) (map[string]any, bool) {
	if _, failed := result["error"]; failed {
		return result, true
	}
	return nil, false
}

// TransportError renders the shared transport failure payload.
func TransportError(_ context.Context, label string, err error) map[string]any {
	if IsTimeout(err) {
		return map[string]any{"error": "Request to " + label + " timed out"}
	}
	return map[string]any{"error": label + " request failed: " + err.Error()}
}

// BearerJSONHeaders is the standard bearer-token JSON request header set.
func BearerJSONHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
}

// ModuleBaseURL returns the module's configured base URL with surrounding
// space and trailing slashes trimmed; empty when unconfigured.
func (rt *Runtime) ModuleBaseURL(moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// HTTPClientOrNew returns the runtime transport, building a default one when
// none was attached.
func (rt *Runtime) HTTPClientOrNew() *HTTPClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return NewHTTPClient(nil)
}

// GetenvFunc returns the runtime's environment lookup, defaulting to
// os.Getenv.
func (rt *Runtime) GetenvFunc() func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// NowOrWall returns the runtime clock reading, defaulting to time.Now.
func (rt *Runtime) NowOrWall() time.Time {
	if rt != nil && rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// EnvOrOS reads one environment variable through the runtime, defaulting to
// os.Getenv.
func (rt *Runtime) EnvOrOS(name string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv(name)
	}
	return os.Getenv(name)
}

// EnvTrimmed is EnvOrOS with surrounding space trimmed.
func (rt *Runtime) EnvTrimmed(name string) string {
	getenv := os.Getenv
	if rt != nil && rt.Getenv != nil {
		getenv = rt.Getenv
	}
	return strings.TrimSpace(getenv(name))
}

package social

// Shared request/decoding helpers: one bounded HTTP path (doRequest), the
// provider request/response pair and the JSON accessors every action uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// runtimeGetenv resolves the runtime environment lookup.
func runtimeGetenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// moduleBaseURL resolves a module base URL, honouring WithBaseURL.
func moduleBaseURL(rt *Runtime, moduleID string) string {
	return rt.ModuleBaseURL(moduleID)
}

// asMap/asSlice/asString are the JSON accessors the action projections use.
func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

func asSlice(value any) []any { return integrationkit.AsSlice(value) }

func asString(value any) string { return integrationkit.AsString(value) }

func asBool(value any) bool { return integrationkit.AsBool(value) }

func mapOrEmpty(value any) map[string]any { return integrationkit.MapOrEmpty(value) }

func sliceOrEmpty(value any) []any { return integrationkit.SliceOrEmpty(value) }

func clampInt(value, low, high int) int { return integrationkit.ClampInt(value, low, high) }

func truncateTextClamped(text string, limit int) string {
	return integrationkit.TruncateTextClamped(text, limit)
}

// ------------------------------------------------------------- shared HTTP ---

// providerRequest is one provider call after auth and URL resolution.
type providerRequest struct {
	Method string
	URL    string
	Query  url.Values
	Header map[string]string
	Form   url.Values
	JSON   any
}

// providerResponse is a decoded provider response.
type providerResponse struct {
	Status int
	Body   []byte
	Parsed any
}

// doRequest executes one HTTP request through the runtime transport.
func doRequest(ctx context.Context, rt *Runtime, spec providerRequest) (providerResponse, error) {
	var out providerResponse
	if ctx == nil {
		ctx = context.Background()
	}
	target := spec.URL
	if encoded := spec.Query.Encode(); encoded != "" {
		if strings.Contains(target, "?") {
			target += "&" + encoded
		} else {
			target += "?" + encoded
		}
	}
	var body io.Reader
	contentType := ""
	switch {
	case spec.Form != nil:
		body = strings.NewReader(spec.Form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case spec.JSON != nil:
		encoded, err := json.Marshal(spec.JSON)
		if err != nil {
			return out, err
		}
		body = bytes.NewReader(encoded)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, spec.Method, target, body)
	if err != nil {
		return out, err
	}
	for name, value := range spec.Header {
		req.Header.Set(name, value)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := integrationkit.NewHTTPClient(nil)
	if rt != nil && rt.HTTP != nil {
		client = rt.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return out, err
	}
	out.Status = resp.StatusCode
	out.Body = raw
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.Parsed)
	}
	return out, nil
}

// envValue reads a trimmed environment value through the runtime lookup.
func envValue(rt *Runtime, name string) string {
	return strings.TrimSpace(runtimeGetenv(rt)(name))
}

// moduleToken resolves a single-value module credential.
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

// injectedToken returns only the WithCredential value, ignoring
// environment fallbacks (Reddit uses it as a pre-acquired OAuth token).
func injectedToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.ConfiguredToken())
}

// isTimeout reports whether a transport failure was a timeout.
func isTimeout(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// networkFailure maps a transport failure onto the module wording.
func networkFailure(moduleID string, ctx context.Context, err error) map[string]any {
	timedOut := isTimeout(ctx, err)
	switch moduleID {
	case youtubeModuleID:
		if timedOut {
			return map[string]any{"error": "Request to YouTube API timed out"}
		}
		return map[string]any{"error": "YouTube API request failed: " + err.Error()}
	case redditModuleID:
		if timedOut {
			return map[string]any{"error": "Request to Reddit timed out"}
		}
		return map[string]any{"error": "Reddit request failed: " + err.Error()}
	case twitterModuleID:
		if timedOut {
			return map[string]any{"error": "Request to Twitter timed out"}
		}
		return map[string]any{"error": "Twitter request failed: " + err.Error()}
	case twilioModuleID:
		if timedOut {
			return map[string]any{"error": "Request to Twilio timed out"}
		}
		return map[string]any{"error": "Twilio request failed: " + err.Error()}
	default:
		if timedOut {
			return map[string]any{"error": "Request to Pushover timed out"}
		}
		return map[string]any{"error": "Pushover request failed: " + err.Error()}
	}
}

// ---------------------------------------------------------------- helpers ---

// valueOrDefault mirrors Python's `dict.get(key, fallback)` for fields
// whose absence must surface as the upstream default rather than null.
func valueOrDefault(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

// intValue reads a decoded JSON number as an int.
func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// digValue walks nested JSON objects.
func digValue(root any, path ...string) any {
	current := root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

// optionalInt distinguishes an absent argument from zero.
func optionalInt(input map[string]any, name string) (int, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	return intArg(input, name, 0), true
}

// joinErrors joins Pushover's `errors` array the upstream way.
func joinErrors(value any) string {
	parts := make([]string, 0)
	for _, item := range asSlice(value) {
		if text := asString(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, ", ")
}

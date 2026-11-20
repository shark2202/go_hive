// Batch 6 lane 2 of the integration port: the support/ATS group — Intercom
// (11 actions), Zendesk (8), Freshdesk (17) and Greenhouse (9).
//
// Upstream reference:
// tools/src/aden_tools/tools/{intercom,zendesk,freshdesk,greenhouse}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Intercom, Zendesk, Freshdesk and Greenhouse resolve credentials from the
//     environment (INTERCOM_ACCESS_TOKEN; ZENDESK_SUBDOMAIN, ZENDESK_EMAIL,
//     ZENDESK_API_TOKEN; FRESHDESK_API_KEY, FRESHDESK_DOMAIN;
//     GREENHOUSE_API_TOKEN). WithCredential covers the single-token modules;
//     multi-value bundles stay environment-only, like the upstream
//     CredentialStoreAdapter split entries.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server with WithBaseURL.
//   - HTTP failures mirror the upstream `_handle_response` / `_request`
//     helpers, including the exact 401/403/404/429 messages.
//   - Optional integer arguments keep the upstream None/absent distinction;
//     intercom_list_conversations clamps per_page to 1..150 where upstream only
//     caps the upper bound.
package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch6Lane2MaxBody  = 8 << 20
	batch6Lane2MaxError = 500
	batch6Lane2MaxItems = 150
)

// InstallBatch6Lane2 publishes the batch-6 lane-2 tools (intercom, zendesk,
// freshdesk, greenhouse) into a shared registry and returns the tool count.
func InstallBatch6Lane2(target *tools.ToolRegistry) int {
	return newBatch6Lane2Registry().Install(target)
}

// newBatch6Lane2Registry builds a registry containing only the lane-2 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane2Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch6Lane2Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range []*Tool{
		batch6Lane2IntercomTool,
		batch6Lane2ZendeskTool,
		batch6Lane2FreshdeskTool,
		batch6Lane2GreenhouseTool,
	} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	for action, impl := range batch6Lane2IntercomImpls() {
		registry.addImpl("intercom", action, impl)
	}
	for action, impl := range batch6Lane2ZendeskImpls() {
		registry.addImpl("zendesk", action, impl)
	}
	for action, impl := range batch6Lane2FreshdeskImpls() {
		registry.addImpl("freshdesk", action, impl)
	}
	for action, impl := range batch6Lane2GreenhouseImpls() {
		registry.addImpl("greenhouse", action, impl)
	}
	return registry
}

// batch6Lane2Modules declares the public endpoints and credential variables.
// Zendesk and Freshdesk derive their host from account credentials, so their
// BaseURL stays empty until WithBaseURL (tests) or the credential fills it in.
func batch6Lane2Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"intercom_tool": {
			ID: "intercom_tool", BaseURL: "https://api.intercom.io",
			EnvVars: []string{"INTERCOM_ACCESS_TOKEN"},
		},
		"zendesk_tool": {
			ID: "zendesk_tool",
			EnvVars: []string{
				"ZENDESK_SUBDOMAIN",
				"ZENDESK_EMAIL",
				"ZENDESK_API_TOKEN",
			},
		},
		"freshdesk_tool": {
			ID: "freshdesk_tool", EnvVars: []string{"FRESHDESK_API_KEY"},
		},
		"greenhouse_tool": {
			ID: "greenhouse_tool", BaseURL: "https://harvest.greenhouse.io/v1",
			EnvVars: []string{"GREENHOUSE_API_TOKEN"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch6Lane2Getenv resolves the runtime environment lookup.
func batch6Lane2Getenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// batch6Lane2ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch6Lane2ModuleURL(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch6Lane2ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func batch6Lane2ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.ResolvedToken(batch6Lane2Getenv(rt))
	return strings.TrimSpace(token)
}

// batch6Lane2HTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func batch6Lane2HTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return newHTTPClient(nil)
}

// batch6Lane2Reply is the status/body pair every module helper inspects.
type batch6Lane2Reply struct {
	Status int
	Body   []byte
}

// batch6Lane2Do performs one JSON request and reads a bounded body.
func batch6Lane2Do(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (batch6Lane2Reply, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return batch6Lane2Reply{}, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return batch6Lane2Reply{}, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := batch6Lane2HTTP(rt).Do(req)
	if err != nil {
		return batch6Lane2Reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch6Lane2MaxBody))
	if err != nil {
		return batch6Lane2Reply{}, err
	}
	return batch6Lane2Reply{Status: resp.StatusCode, Body: raw}, nil
}

// batch6Lane2IsTimeout reports whether err is a deadline or a net timeout.
func batch6Lane2IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch6Lane2BasicAuth renders an HTTP Basic header value.
func batch6Lane2BasicAuth(user, password string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	return "Basic " + encoded
}

// batch6Lane2TruncateText caps provider error/description text the same way
// upstream slices response.text[:500].
func batch6Lane2TruncateText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// batch6Lane2Clamp bounds an integer to [low, high].
func batch6Lane2Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch6Lane2AsMap safely views a decoded JSON object.
func batch6Lane2AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch6Lane2AsSlice safely views a decoded JSON array.
func batch6Lane2AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch6Lane2AsString reads a decoded JSON string.
func batch6Lane2AsString(value any) string {
	text, _ := value.(string)
	return text
}

// batch6Lane2BoolOr reads a boolean with the upstream `.get(key, fallback)`
// default applied when the field is absent.
func batch6Lane2BoolOr(value any, fallback bool) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}

// batch6Lane2JSONNumber turns integral float64 values (the form
// encoding/json produces) back into integers so responses render like Python
// ints instead of 456.0.
func batch6Lane2JSONNumber(value any) any {
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

// batch6Lane2StringID renders an identifier the way Python's str() does.
func batch6Lane2StringID(value any) string {
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

// batch6Lane2StringList renders a decoded JSON array of names as []string. A
// missing field produces an empty (non-nil) slice so it encodes as [].
func batch6Lane2StringList(value any) []string {
	items := batch6Lane2AsSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, batch6Lane2AsString(item))
	}
	return out
}

// batch6Lane2NumberList renders a decoded JSON array of identifiers, keeping
// ints as ints.
func batch6Lane2NumberList(value any) []any {
	items := batch6Lane2AsSlice(value)
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, batch6Lane2JSONNumber(item))
	}
	return out
}

// batch6Lane2DecodeAny decodes a JSON body of any shape.
func batch6Lane2DecodeAny(raw []byte) (any, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

// batch6Lane2DecodeObject decodes a JSON object body.
func batch6Lane2DecodeObject(raw []byte) (map[string]any, bool) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, true
}

// batch6Lane2OptionalString distinguishes an absent/null argument from "".
func batch6Lane2OptionalString(input map[string]any, name string) (string, bool) {
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

// batch6Lane2OptionalInt distinguishes an absent/null argument from 0.
func batch6Lane2OptionalInt(input map[string]any, name string) (int, bool) {
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

// batch6Lane2SplitTags splits a comma-separated tag list the upstream way:
// strip each entry and drop blanks.
func batch6Lane2SplitTags(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// batch6Lane2ParseISO mirrors datetime.fromisoformat for the timestamps these
// tools accept, defaulting naive values to UTC.
func batch6Lane2ParseISO(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			if parsed.Location() == time.UTC {
				return parsed, true
			}
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

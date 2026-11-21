// Batch 5, lane 1 of the integration port: Jira (upstream jira_tool, 9
// actions), Confluence (upstream confluence_tool, 8 actions) and Asana
// (upstream asana_tool, 9 actions).
//
// The three modules follow the upstream action names, parameter names,
// defaults and response projections. Jira and Confluence use Atlassian Basic
// auth (email + API token) and accept WithBaseURL for httptest upstreams; Asana
// uses a Bearer PAT. Every action is exercised against a fake server in
// batch5_lane1_*_test.go.
//
// Deliberate limits:
//   - the runtime holds one credential per module (like batch 4); upstream
//     CredentialStoreAdapter aliases are not reproduced.
//   - Jira Cloud only, REST API v3, API-token Basic auth (no OAuth).
//   - no automatic retries: upstream has none, so 429 is surfaced as-is.
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
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const batch5Lane1MaxBody = 8 << 20

// InstallBatch5Lane1 publishes the batch-5 lane-1 SaaS tools (jira,
// confluence and asana) into a shared registry and returns the number of tools
// registered.
func InstallBatch5Lane1(target *tools.ToolRegistry) int {
	return newBatch5Lane1Registry().Install(target)
}

// newBatch5Lane1Registry builds a registry containing only the batch-5 lane-1
// tools, sharing the Runtime surface used by the rest of the port.
func newBatch5Lane1Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch5Lane1Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{batch5Lane1JiraTool, batch5Lane1ConfluenceTool, batch5Lane1AsanaTool} {
		registry.AddTool(tool)
	}
	for action, impl := range batch5Lane1JiraImpls() {
		registry.AddImpl("jira", action, impl)
	}
	for action, impl := range batch5Lane1ConfluenceImpls() {
		registry.AddImpl("confluence", action, impl)
	}
	for action, impl := range batch5Lane1AsanaImpls() {
		registry.AddImpl("asana", action, impl)
	}
	return registry
}

// batch5Lane1Modules declares the public endpoints and credential variables for
// the three modules. Jira and Confluence derive their API base from the domain
// env vars, so BaseURL stays empty unless a test injects WithBaseURL.
func batch5Lane1Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"jira_tool":       {ID: "jira_tool", EnvVars: []string{"JIRA_API_TOKEN"}},
		"confluence_tool": {ID: "confluence_tool", EnvVars: []string{"CONFLUENCE_API_TOKEN"}},
		"asana_tool": {
			ID:      "asana_tool",
			BaseURL: "https://app.asana.com/api/1.0",
			EnvVars: []string{"ASANA_ACCESS_TOKEN"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch5Lane1Getenv resolves one environment value, tolerating hand-built
// runtimes without a Getenv hook.
func batch5Lane1Getenv(rt *Runtime, name string) string {
	if rt == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return strings.TrimSpace(getenv(name))
}

// batch5Lane1ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func batch5Lane1ModuleToken(rt *Runtime, moduleID string) string {
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

// batch5Lane1ModuleBase resolves a module base URL, honouring WithBaseURL.
func batch5Lane1ModuleBase(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch5Lane1HTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func batch5Lane1HTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return newHTTPClient(nil)
}

// batch5Lane1IsTimeout reports whether err is a deadline or a net timeout.
func batch5Lane1IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch5Lane1BasicAuth renders the Atlassian Basic header (email + API token).
func batch5Lane1BasicAuth(email, token string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(email + ":" + token))
	return "Basic " + encoded
}

// batch5Lane1RawResponse is the status and body of one fake-able HTTP call.
type batch5Lane1RawResponse struct {
	Status int
	Body   []byte
}

// batch5Lane1Do performs one HTTP call with optional query and JSON payload.
func batch5Lane1Do(ctx context.Context, rt *Runtime, method, rawURL string, query url.Values, payload any, headers map[string]string) (*batch5Lane1RawResponse, error) {
	if len(query) > 0 {
		if strings.Contains(rawURL, "?") {
			rawURL += "&" + query.Encode()
		} else {
			rawURL += "?" + query.Encode()
		}
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := batch5Lane1HTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch5Lane1MaxBody))
	if err != nil {
		return nil, err
	}
	return &batch5Lane1RawResponse{Status: resp.StatusCode, Body: raw}, nil
}

// batch5Lane1Decode unmarshals a JSON object body.
func batch5Lane1Decode(raw []byte) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		object = map[string]any{}
	}
	return object, nil
}

// batch5Lane1Map safely views a decoded JSON object.
func batch5Lane1Map(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch5Lane1Slice safely views a decoded JSON array.
func batch5Lane1Slice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch5Lane1Text reads a decoded JSON string field.
func batch5Lane1Text(object map[string]any, key string) string {
	text, _ := object[key].(string)
	return text
}

// batch5Lane1OptionalText reads a field that may be absent, null or non-string
// and renders it the way Python's `value or ""` / slicing would.
func batch5Lane1OptionalText(object map[string]any, key string) string {
	value, present := object[key]
	if !present || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// batch5Lane1Bool reads a decoded JSON boolean field.
func batch5Lane1Bool(object map[string]any, key string) bool {
	flag, _ := object[key].(bool)
	return flag
}

// batch5Lane1Int reads a decoded JSON number field as an int.
func batch5Lane1Int(object map[string]any, key string, fallback int) int {
	value, present := object[key]
	if !present || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed)
		}
	}
	return fallback
}

// batch5Lane1Truncate caps text by runes, matching Python string slicing.
func batch5Lane1Truncate(text string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// batch5Lane1Clamp bounds an integer to [low, high].
func batch5Lane1Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch5Lane1OptionalBool distinguishes an absent/null argument from false.
func batch5Lane1OptionalBool(input map[string]any, name string) (bool, bool) {
	value, present := input[name]
	if !present || value == nil {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

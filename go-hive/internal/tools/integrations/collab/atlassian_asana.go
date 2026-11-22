// Batch 5, lane 1 of the integration port: Jira (upstream jira_tool, 9
// actions), Confluence (upstream confluence_tool, 8 actions) and Asana
// (upstream asana_tool, 9 actions).
//
// The three modules follow the upstream action names, parameter names,
// defaults and response projections. Jira and Confluence use Atlassian Basic
// auth (email + API token) and accept WithBaseURL for httptest upstreams; Asana
// uses a Bearer PAT. Every action is exercised against a fake server in
// _lane1_*_test.go.
//
// Deliberate limits:
//   - the runtime holds one credential per module (like batch 4); upstream
//     CredentialStoreAdapter aliases are not reproduced.
//   - Jira Cloud only, REST API v3, API-token Basic auth (no OAuth).
//   - no automatic retries: upstream has none, so 429 is surfaced as-is.
package collab

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxBody = 8 << 20

// newBatch5Lane1Registry builds a registry containing only the batch-5 lane-1
// tools, sharing the Runtime surface used by the rest of the port.
func NewAtlassianRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := newRegistry(runtime)
	for _, tool := range []*Tool{jiraTool, confluenceTool, asanaTool} {
		registry.AddTool(tool)
	}
	for action, impl := range jiraImpls() {
		registry.AddImpl("jira", action, impl)
	}
	for action, impl := range confluenceImpls() {
		registry.AddImpl("confluence", action, impl)
	}
	for action, impl := range asanaImpls() {
		registry.AddImpl("asana", action, impl)
	}
	return registry
}

// modules declares the public endpoints and credential variables for
// the three modules. Jira and Confluence derive their API base from the domain
// env vars, so BaseURL stays empty unless a test injects WithBaseURL.
func modules() map[string]*ModuleConfig {
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

// getenv resolves one environment value, tolerating hand-built
// runtimes without a Getenv hook.
func getenv(rt *Runtime, name string) string {
	if rt == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return strings.TrimSpace(getenv(name))
}

// basicAuth renders the Atlassian Basic header (email + API token).
func basicAuth(email, token string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(email + ":" + token))
	return "Basic " + encoded
}

// rawResponse is the status and body of one fake-able HTTP call.
type rawResponse struct {
	Status int
	Body   []byte
}

// do performs one HTTP call with optional query and JSON payload.
func do(ctx context.Context, rt *Runtime, method, rawURL string, query url.Values, payload any, headers map[string]string) (*rawResponse, error) {
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
	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return &rawResponse{Status: resp.StatusCode, Body: raw}, nil
}

// decode unmarshals a JSON object body.
func decode(raw []byte) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		object = map[string]any{}
	}
	return object, nil
}

// text reads a decoded JSON string field.
func atlassianText(object map[string]any, key string) string {
	text, _ := object[key].(string)
	return text
}

// optionalText reads a field that may be absent, null or non-string
// and renders it the way Python's `value or ""` / slicing would.
func optionalText(object map[string]any, key string) string {
	value, present := object[key]
	if !present || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// atlassianBool reads a decoded JSON boolean field.
func atlassianBool(object map[string]any, key string) bool {
	flag, _ := object[key].(bool)
	return flag
}

// atlassianInt reads a decoded JSON number field as an int.
func atlassianInt(object map[string]any, key string, fallback int) int {
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

// optionalBool distinguishes an absent/null argument from false.
func optionalBool(input map[string]any, name string) (bool, bool) {
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

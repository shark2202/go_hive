package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"time"
)

// httpClient wraps net/http with a bounded body read and a default timeout.
type httpClient struct {
	inner *http.Client
}

func newHTTPClient(client *http.Client) *httpClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &httpClient{inner: client}
}

func (c *httpClient) Do(req *http.Request) (*http.Response, error) { return c.inner.Do(req) }

// RestSpec is a declarative description of one upstream REST action.
type RestSpec struct {
	Method string
	// Path is appended to the module base URL. "{param}" segments are filled
	// from the action input and removed from the query/body payload.
	Path string
	// Query names parameters sent in the query string. When empty, every
	// non-path parameter is sent in the query for GET/DELETE and in the body
	// for other methods.
	Query []string
	// Body names parameters sent in the body. When empty, every non-path,
	// non-query parameter is sent in the body.
	Body []string
	// Rename maps input names to provider field names.
	Rename map[string]string
	// Constants are merged into the body (or query for GET/DELETE).
	Constants map[string]any
	// Unwrap returns the named response field instead of the envelope
	// (Telegram's {"ok":true,"result":...}).
	Unwrap string
	// ListKey reshapes {"data":[...]} into {"has_more":bool,ListKey:[...]}.
	ListKey string
	// HasMore includes the provider's has_more flag with ListKey.
	HasMore bool
	// FirstOf returns the first element of the named provider array
	// (customer lookup by email, subscription status).
	FirstOf string
	// JSONParams lists string inputs whose contents are JSON and must be
	// embedded structurally (Stripe line_items).
	JSONParams []string
	// JoinLists lists array inputs that the provider expects as a
	// comma-separated string (Slack user_ids).
	JoinLists []string
	// Build optionally overrides the derived body/query payload.
	Build func(input map[string]any) map[string]any
}

// moduleConfigs declares credentials and wire conventions per module.
func moduleConfigs() map[string]*ModuleConfig {
	bearer := func(token string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + token}
	}
	return map[string]*ModuleConfig{
		"stripe_tool": {
			ID: "stripe_tool", BaseURL: "https://api.stripe.com", EnvVars: []string{"STRIPE_API_KEY"},
			Headers: bearer, Form: true,
		},
		"slack_tool": {
			ID: "slack_tool", BaseURL: "https://slack.com/api", EnvVars: []string{"SLACK_BOT_TOKEN"},
			Headers: bearer,
		},
		"github_tool": {
			ID: "github_tool", BaseURL: "https://api.github.com", EnvVars: []string{"GITHUB_TOKEN"},
			Headers: func(token string) map[string]string {
				return map[string]string{
					"Authorization":        "Bearer " + token,
					"Accept":               "application/vnd.github+json",
					"X-GitHub-Api-Version": "2022-11-28",
				}
			},
		},
		"telegram_tool": {
			ID: "telegram_tool", BaseURL: "https://api.telegram.org/bot{token}", EnvVars: []string{"TELEGRAM_BOT_TOKEN"},
		},
		"trello_tool": {
			ID: "trello_tool", BaseURL: "https://api.trello.com/1", EnvVars: []string{"TRELLO_API_KEY", "TRELLO_API_TOKEN"},
			Form: true,
			ResolveToken: func(getenv func(string) string) (string, []string) {
				key, token := getenv("TRELLO_API_KEY"), getenv("TRELLO_API_TOKEN")
				if key == "" || token == "" {
					var missing []string
					if key == "" {
						missing = append(missing, "TRELLO_API_KEY")
					}
					if token == "" {
						missing = append(missing, "TRELLO_API_TOKEN")
					}
					return "", missing
				}
				return key + "|" + token, nil
			},
			Query: func(token string) map[string]string {
				parts := strings.SplitN(token, "|", 2)
				if len(parts) != 2 {
					return nil
				}
				return map[string]string{"key": parts[0], "token": parts[1]}
			},
		},
		"airtable_tool": {
			ID: "airtable_tool", BaseURL: "https://api.airtable.com/v0", EnvVars: []string{"AIRTABLE_PAT"},
			Headers: bearer,
		},
	}
}

// ModuleConfig describes how one module authenticates and where it lives.
type ModuleConfig struct {
	ID      string
	BaseURL string
	EnvVars []string
	// ResolveToken overrides the default "first non-empty env var" rule.
	ResolveToken func(getenv func(string) string) (string, []string)
	Headers      func(token string) map[string]string
	Query        func(token string) map[string]string
	Form         bool
	token        string
}

func (c *ModuleConfig) resolveToken(getenv func(string) string) (string, []string) {
	if c.token != "" {
		return c.token, nil
	}
	if c.ResolveToken != nil {
		return c.ResolveToken(getenv)
	}
	for _, name := range c.EnvVars {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value, nil
		}
	}
	return "", append([]string(nil), c.EnvVars...)
}

// restClient is a per-module request builder sharing the runtime transport.
type restClient struct {
	cfg *ModuleConfig
	rt  *Runtime
}

func (rt *Runtime) clientFor(moduleID string) *restClient {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.clients == nil {
		rt.clients = map[string]*restClient{}
	}
	if client, ok := rt.clients[moduleID]; ok {
		return client
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		cfg = &ModuleConfig{ID: moduleID}
		rt.Modules[moduleID] = cfg
	}
	client := &restClient{cfg: cfg, rt: rt}
	rt.clients[moduleID] = client
	return client
}

// callREST builds and executes one spec against the module endpoint.
func (rt *Runtime) callREST(ctx context.Context, tool *Tool, action Action, spec RestSpec, input map[string]any) (any, error) {
	client := rt.clientFor(tool.ID)
	token, missing := client.cfg.resolveToken(rt.Getenv)
	if len(missing) > 0 {
		return map[string]any{
			"error": fmt.Sprintf("%s credentials not configured", tool.DisplayName),
			"help":  "Set " + strings.Join(missing, " or ") + " environment variable, or configure the credential store.",
		}, nil
	}
	for _, name := range action.Required {
		if _, present := input[name]; present {
			continue
		}
		return map[string]any{"error": fmt.Sprintf("%s is required", name)}, nil
	}

	path, consumed, err := fillPath(spec.Path, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	query := url.Values{}
	body := map[string]any{}

	queryNames := map[string]bool{}
	for _, name := range spec.Query {
		queryNames[name] = true
	}
	bodyNames := map[string]bool{}
	for _, name := range spec.Body {
		bodyNames[name] = true
	}
	explicitQuery := spec.Query != nil
	explicitBody := spec.Body != nil

	for name, value := range input {
		switch name {
		case "action", "account":
			continue
		}
		if consumed[name] {
			continue
		}
		if value == nil {
			continue
		}
		wire := wireName(spec, name)
		toQuery := queryNames[name]
		toBody := bodyNames[name]
		if !explicitQuery && !explicitBody {
			toQuery = spec.Method == http.MethodGet || spec.Method == http.MethodDelete
			toBody = !toQuery
		} else if !explicitQuery && !explicitBody && spec.Query == nil && spec.Body == nil {
			toBody = true
		}
		if toQuery {
			for _, v := range queryValues(value) {
				query.Add(wire, v)
			}
		} else if toBody {
			body[wire] = value
		}
	}
	for name, value := range spec.Constants {
		if spec.Method == http.MethodGet || spec.Method == http.MethodDelete {
			query.Set(name, fmt.Sprint(value))
		} else {
			body[name] = value
		}
	}
	for _, name := range spec.JSONParams {
		raw, ok := body[name].(string)
		if !ok {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			body[name] = decoded
		}
	}
	if spec.Build != nil {
		for name, value := range spec.Build(input) {
			body[name] = value
		}
	}
	for _, name := range spec.JoinLists {
		if value, present := input[name]; present {
			body[name] = strings.Join(stringSliceArg(map[string]any{name: value}, name), ",")
		}
	}
	if client.cfg.Query != nil {
		for name, value := range client.cfg.Query(token) {
			query.Set(name, value)
		}
	}

	base := strings.ReplaceAll(client.cfg.BaseURL, "{token}", url.PathEscape(token))
	target := strings.TrimRight(base, "/") + path
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}

	var reader io.Reader
	contentType := ""
	if spec.Method != http.MethodGet && spec.Method != http.MethodDelete {
		if client.cfg.Form {
			reader = strings.NewReader(encodeForm(body))
			contentType = "application/x-www-form-urlencoded"
		} else {
			payload, err := json.Marshal(body)
			if err != nil {
				return map[string]any{"error": "failed to encode request: " + err.Error()}, nil
			}
			reader = strings.NewReader(string(payload))
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, spec.Method, target, reader)
	if err != nil {
		return map[string]any{"error": "failed to build request: " + err.Error()}, nil
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if client.cfg.Headers != nil {
		for name, value := range client.cfg.Headers(token) {
			req.Header.Set(name, value)
		}
	}
	resp, err := rt.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return map[string]any{"error": "Failed to read response: " + readErr.Error()}, nil
	}
	parsed := any(nil)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &parsed)
	}
	if resp.StatusCode >= 400 {
		return providerError(tool, resp.StatusCode, parsed, raw), nil
	}
	if object, ok := parsed.(map[string]any); ok && tool.ID == "slack_tool" {
		if success, present := object["ok"].(bool); present && !success {
			code := fmt.Sprint(object["error"])
			return map[string]any{"error": slackErrorMessage(code), "error_code": code}, nil
		}
	}
	return shapeResponse(spec, parsed, raw), nil
}

func wireName(spec RestSpec, name string) string {
	if wire, ok := spec.Rename[name]; ok {
		return wire
	}
	return name
}

// callHelper executes an ad-hoc spec for a composite action. It shares the
// module's credential resolution, transport and error mapping.
func (rt *Runtime) callHelper(ctx context.Context, moduleID, display string, spec RestSpec, input map[string]any) (any, error) {
	return rt.callREST(ctx, &Tool{ID: moduleID, DisplayName: display}, Action{Name: "helper"}, spec, input)
}

// fillPath substitutes "{name}" segments and reports the consumed keys.
func fillPath(template string, input map[string]any) (string, map[string]bool, error) {
	consumed := map[string]bool{}
	var missing string
	out := template
	for {
		start := strings.Index(out, "{")
		if start < 0 {
			break
		}
		end := strings.Index(out[start:], "}")
		if end < 0 {
			break
		}
		end += start
		name := out[start+1 : end]
		value, ok := input[name]
		text := ""
		if ok && value != nil {
			text = fmt.Sprint(value)
		}
		if text == "" {
			missing = name
			break
		}
		out = out[:start] + url.PathEscape(text) + out[end+1:]
		consumed[name] = true
	}
	if missing != "" {
		return "", nil, fmt.Errorf("%s is required", missing)
	}
	return out, consumed, nil
}

// queryValues flattens a value into repeated query/form values.
func queryValues(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, scalarString(item))
		}
		return out
	case []map[string]any:
		encoded, _ := json.Marshal(typed)
		return []string{string(encoded)}
	case map[string]any:
		encoded, _ := json.Marshal(typed)
		return []string{string(encoded)}
	default:
		return []string{scalarString(value)}
	}
}

func scalarString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(value)
	}
}

// providerError maps non-2xx responses onto upstream error dicts.
func providerError(tool *Tool, status int, parsed any, raw []byte) map[string]any {
	if object, ok := parsed.(map[string]any); ok {
		switch tool.ID {
		case "stripe_tool":
			if detail, ok := object["error"].(map[string]any); ok {
				message := fmt.Sprint(detail["message"])
				out := map[string]any{"error": message}
				if kind, ok := detail["type"].(string); ok && kind != "" {
					out["type"] = kind
				}
				return out
			}
		case "slack_tool":
			code := fmt.Sprint(object["error"])
			if code != "" && code != "<nil>" {
				return map[string]any{"error": slackErrorMessage(code), "error_code": code}
			}
		case "github_tool":
			switch status {
			case http.StatusUnauthorized:
				return map[string]any{"error": "Invalid or expired GitHub token"}
			case http.StatusForbidden:
				return map[string]any{"error": "Forbidden - check token permissions or rate limit"}
			case http.StatusNotFound:
				return map[string]any{"error": "Resource not found"}
			}
			if message, ok := object["message"].(string); ok && message != "" {
				return map[string]any{"error": message}
			}
		case "telegram_tool":
			if description, ok := object["description"].(string); ok && description != "" {
				return map[string]any{"error": description}
			}
		}
	}
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	if snippet == "" {
		return map[string]any{"error": fmt.Sprintf("HTTP %d from %s", status, tool.DisplayName)}
	}
	return map[string]any{"error": fmt.Sprintf("HTTP %d: %s", status, snippet)}
}

// shapeResponse applies the spec's envelope rules.
func shapeResponse(spec RestSpec, parsed any, raw []byte) any {
	if spec.Unwrap != "" {
		if object, ok := parsed.(map[string]any); ok {
			if value, ok := object[spec.Unwrap]; ok {
				return value
			}
		}
	}
	if spec.FirstOf != "" {
		if object, ok := parsed.(map[string]any); ok {
			if items, ok := object[spec.FirstOf].([]any); ok {
				if len(items) == 0 {
					return map[string]any{"error": "No matching record found"}
				}
				return items[0]
			}
		}
	}
	if spec.ListKey != "" {
		if object, ok := parsed.(map[string]any); ok {
			out := map[string]any{spec.ListKey: object["data"]}
			if spec.HasMore {
				out["has_more"] = object["has_more"]
			}
			return out
		}
	}
	if parsed != nil {
		return parsed
	}
	return strings.TrimSpace(string(raw))
}

// encodeForm flattens nested maps and arrays into bracket notation, which is
// what Stripe and other form-encoded APIs expect (metadata[a]=b,
// items[0][price]=price_123).
func encodeForm(body map[string]any) string {
	values := url.Values{}
	for key, value := range body {
		flattenForm(values, key, value)
	}
	return values.Encode()
}

func flattenForm(values url.Values, prefix string, value any) {
	switch typed := value.(type) {
	case nil:
		return
	case map[string]any:
		for key, item := range typed {
			flattenForm(values, prefix+"["+key+"]", item)
		}
	case []any:
		for i, item := range typed {
			flattenForm(values, fmt.Sprintf("%s[%d]", prefix, i), item)
		}
	case []string:
		for i, item := range typed {
			values.Add(fmt.Sprintf("%s[%d]", prefix, i), item)
		}
	default:
		values.Add(prefix, scalarString(value))
	}
}

// slackErrorMessage mirrors the upstream human-readable error map.
func slackErrorMessage(code string) string {
	messages := map[string]string{
		"channel_not_found": "Channel not found",
		"not_in_channel":    "Bot is not a member of this channel",
		"is_archived":       "Channel is archived",
		"msg_too_long":      "Message text is too long",
		"ratelimited":       "Rate limit exceeded. Try again later.",
	}
	if message, ok := messages[code]; ok {
		return message
	}
	return "Slack API error: " + code
}

// stringArg reads a string input.
func stringArg(input map[string]any, name string) string {
	value, ok := input[name]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(value)
	}
}

// intArg reads an integer input with a fallback.
func intArg(input map[string]any, name string, fallback int) int {
	value, ok := input[name]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

// floatArg reads a float input with a fallback.
func floatArg(input map[string]any, name string, fallback float64) float64 {
	value, ok := input[name]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

// boolArg reads a boolean input with a fallback.
func boolArg(input map[string]any, name string, fallback bool) bool {
	value, ok := input[name]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

// stringSliceArg reads a list of strings from a string or array input.
func stringSliceArg(input map[string]any, name string) []string {
	value, ok := input[name]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		parts := strings.Split(typed, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}

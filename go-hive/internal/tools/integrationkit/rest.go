package integrationkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

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
func (rt *Runtime) CallREST(ctx context.Context, tool *Tool, action Action, spec RestSpec, input map[string]any) (any, error) {
	client := rt.clientFor(tool.ID)
	token, missing := client.cfg.ResolvedToken(rt.Getenv)
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
			body[name] = strings.Join(StringSliceArg(map[string]any{name: value}, name), ",")
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

func (rt *Runtime) CallHelper(ctx context.Context, moduleID, display string, spec RestSpec, input map[string]any) (any, error) {
	return rt.CallREST(ctx, &Tool{ID: moduleID, DisplayName: display}, Action{Name: "helper"}, spec, input)
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

// ScalarString renders one input value the way the REST engine encodes it.
func ScalarString(value any) string { return scalarString(value) }

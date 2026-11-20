package integrationkit

import (
	"context"
	"encoding/json"
	"errors"
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

// callREST builds and executes one spec against the module endpoint. When the
// spec declares pagination the walk is followed; when the provider answers 401
// and the module has an OAuth2 refresh source, the request is retried once
// with a freshly minted token.
func (rt *Runtime) CallREST(ctx context.Context, tool *Tool, action Action, spec RestSpec, input map[string]any) (any, error) {
	for _, name := range action.Required {
		if _, present := input[name]; present {
			continue
		}
		return map[string]any{"error": fmt.Sprintf("%s is required", name)}, nil
	}
	if spec.Pagination != nil {
		return rt.callRESTPages(ctx, tool, spec, input)
	}
	result, _, err := rt.callRESTOnce(ctx, tool, spec, input, false)
	return result, err
}

// callRESTPages walks the provider cursor, concatenating the item arrays and
// returning the first page's envelope with the merged list in place.
func (rt *Runtime) callRESTPages(ctx context.Context, tool *Tool, spec RestSpec, input map[string]any) (any, error) {
	pagination := spec.Pagination
	current := input
	var envelope map[string]any
	var merged []any
	seen := map[string]bool{}
	for page := 1; ; page++ {
		result, status, err := rt.callRESTStep(ctx, tool, spec, current)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return result, nil
		}
		object, ok := result.(map[string]any)
		if !ok {
			return result, nil
		}
		items, found := pathItems(object, pagination.ItemsPath)
		if !found {
			// The provider did not return the declared shape; surface the page
			// untouched rather than inventing an empty list.
			return result, nil
		}
		if envelope == nil {
			envelope = object
		}
		merged = append(merged, items...)
		next := strings.TrimSpace(scalarString(pathValue(object, pagination.NextCursorPath)))
		if next == "" || pagination.CursorParam == "" || seen[next] || page >= pagination.maxPages() {
			break
		}
		seen[next] = true
		nextInput := make(map[string]any, len(input)+1)
		for name, value := range input {
			nextInput[name] = value
		}
		nextInput[pagination.CursorParam] = next
		current = nextInput
	}
	setPathValue(envelope, pagination.ItemsPath, merged)
	clearPathValue(envelope, pagination.NextCursorPath)
	return envelope, nil
}

// callRESTStep performs one request and resolves a 401 through the module's
// OAuth refresh source: either one retry with a fresh token or an explicit
// value error naming the credential that cannot be refreshed.
func (rt *Runtime) callRESTStep(ctx context.Context, tool *Tool, spec RestSpec, input map[string]any) (any, int, error) {
	result, status, err := rt.callRESTOnce(ctx, tool, spec, input, false)
	if err != nil || status != http.StatusUnauthorized || !rt.OAuthConfigured(tool.ID) {
		return result, status, err
	}
	resolution := rt.ResolveUnauthorized(ctx, tool.ID, tool.DisplayName)
	if !resolution.Retry {
		if resolution.Failure != nil {
			return resolution.Failure, status, nil
		}
		return result, status, nil
	}
	return rt.callRESTOnce(ctx, tool, spec, input, true)
}

// callRESTOnce is one pass over the spec with a token resolved either through
// the cache (force false) or through a forced refresh (force true).
func (rt *Runtime) callRESTOnce(ctx context.Context, tool *Tool, spec RestSpec, input map[string]any, force bool) (any, int, error) {
	client := rt.clientFor(tool.ID)
	token, missing, tokenErr := rt.restToken(ctx, client.cfg, force)
	if tokenErr != nil {
		var refreshErr *OAuthRefreshError
		if errors.As(tokenErr, &refreshErr) {
			return refreshErr.ValueError(), http.StatusUnauthorized, nil
		}
		return map[string]any{"error": tokenErr.Error()}, 0, nil
	}
	if len(missing) > 0 {
		return map[string]any{
			"error": fmt.Sprintf("%s credentials not configured", tool.DisplayName),
			"help":  "Set " + strings.Join(missing, " or ") + " environment variable, or configure the credential store.",
		}, 0, nil
	}

	path, consumed, err := fillPath(spec.Path, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, 0, nil
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
				return map[string]any{"error": "failed to encode request: " + err.Error()}, 0, nil
			}
			reader = strings.NewReader(string(payload))
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, spec.Method, target, reader)
	if err != nil {
		return map[string]any{"error": "failed to build request: " + err.Error()}, 0, nil
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
			return nil, 0, ctx.Err()
		}
		return map[string]any{"error": "Network error: " + err.Error()}, 0, nil
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return map[string]any{"error": "Failed to read response: " + readErr.Error()}, resp.StatusCode, nil
	}
	parsed := any(nil)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &parsed)
	}
	if resp.StatusCode >= 400 {
		return providerError(tool, resp.StatusCode, parsed, raw), resp.StatusCode, nil
	}
	if object, ok := parsed.(map[string]any); ok && tool.ID == "slack_tool" {
		if success, present := object["ok"].(bool); present && !success {
			code := fmt.Sprint(object["error"])
			return map[string]any{"error": slackErrorMessage(code), "error_code": code}, resp.StatusCode, nil
		}
	}
	return shapeResponse(spec, parsed, raw), resp.StatusCode, nil
}

// restToken resolves the request token: the cached/static path normally, a
// forced refresh for the 401 retry.
func (rt *Runtime) restToken(ctx context.Context, cfg *ModuleConfig, force bool) (string, []string, error) {
	if cfg == nil {
		return "", nil, nil
	}
	if force {
		token, err := rt.RefreshAccessToken(ctx, cfg.ID)
		return token, nil, err
	}
	return rt.ModuleToken(ctx, cfg)
}

// pathValue walks a dot-separated path through decoded JSON.
func pathValue(object map[string]any, path string) any {
	if path == "" {
		return object
	}
	var current any = object
	for _, segment := range strings.Split(path, ".") {
		container, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = container[segment]
		if !ok {
			return nil
		}
	}
	return current
}

// pathItems reads the item array at path. found reports whether the path
// resolved to a list (an empty list is found, a missing key is not).
func pathItems(object map[string]any, path string) ([]any, bool) {
	value := pathValue(object, path)
	if value == nil {
		return nil, false
	}
	items, ok := value.([]any)
	return items, ok
}

// setPathValue replaces the value at a dot path inside a decoded payload.
func setPathValue(object map[string]any, path string, value any) {
	segments := strings.Split(path, ".")
	current := object
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	current[segments[len(segments)-1]] = value
}

// clearPathValue removes the leaf at a dot path, so a fully walked response no
// longer advertises a next page.
func clearPathValue(object map[string]any, path string) {
	if path == "" {
		return
	}
	segments := strings.Split(path, ".")
	current := object
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, segments[len(segments)-1])
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

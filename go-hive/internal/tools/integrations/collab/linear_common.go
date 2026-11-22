// Linear transport helpers shared by every linear_* action: credentials,
// GraphQL execution, error translation and connection unwrapping.
package collab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func linearAuthError() map[string]any {
	return map[string]any{
		"error": "Linear credentials not configured",
		"help":  "Set LINEAR_API_KEY environment variable or configure via credential store. Get an API key at https://linear.app/settings/api",
	}
}

// linearExecute posts one GraphQL document. Transport failures are
// returned as Go errors so the action wrappers can apply the upstream
// timeout/network messages; API failures are returned as error dicts.
func linearExecute(ctx context.Context, rt *Runtime, query string, variables map[string]any) (map[string]any, error) {
	token := moduleToken(rt, "linear_tool")
	if token == "" {
		return linearAuthError(), nil
	}
	base := moduleBaseURL(rt, "linear_tool")
	if base == "" {
		base = "https://api.linear.app"
	}
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"error": "Linear request failed: " + err.Error()}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/graphql", bytes.NewReader(encoded))
	if err != nil {
		return map[string]any{"error": "Linear request failed: " + err.Error()}, nil
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, linearMaxBody))
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return map[string]any{"error": "Invalid or expired Linear API key"}, nil
	case resp.StatusCode == http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions. Check your Linear API key scopes."}, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return map[string]any{"error": "Linear rate limit exceeded. Try again later."}, nil
	case resp.StatusCode >= 400:
		return map[string]any{
			"error": fmt.Sprintf("Linear API error (HTTP %d): %s", resp.StatusCode, linearErrorDetail(raw)),
		}, nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return map[string]any{
			"error": fmt.Sprintf("Linear API error (HTTP %d): %s", resp.StatusCode, linearErrorDetail(raw)),
		}, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return map[string]any{"error": "Linear API error: invalid JSON response"}, nil
	}
	if errors, present := envelope["errors"]; present {
		messages := make([]string, 0)
		for _, rawError := range asSlice(errors) {
			object := asMap(rawError)
			if message, ok := object["message"].(string); ok {
				messages = append(messages, message)
				continue
			}
			messages = append(messages, fmt.Sprint(rawError))
		}
		return map[string]any{"error": "GraphQL error: " + strings.Join(messages, "; ")}, nil
	}
	if data, ok := envelope["data"].(map[string]any); ok {
		return data, nil
	}
	return envelope, nil
}

// linearErrorDetail mirrors response.json().get("message", response.text).
func linearErrorDetail(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if message, ok := payload["message"].(string); ok {
			return message
		}
	}
	return string(raw)
}

// linearCall runs one document and translates transport errors.
func linearCall(ctx context.Context, rt *Runtime, query string, variables map[string]any) (map[string]any, error) {
	result, err := linearExecute(ctx, rt, query, variables)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, nil
		}
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	return result, nil
}

// linearUnwrap returns the named payload or the whole data map, matching
// Python's result.get(key, result).
func linearUnwrap(result map[string]any, key string) any {
	if value, present := result[key]; present {
		return value
	}
	return result
}

// linearNodes reads a connection's nodes, defaulting to an empty list.
func linearNodes(result map[string]any, connection string) []any {
	payload := asMap(result[connection])
	nodes := asSlice(payload["nodes"])
	if nodes == nil {
		return []any{}
	}
	return nodes
}

// linearHasNextPage reads connection.pageInfo.hasNextPage.
func linearHasNextPage(result map[string]any, connection string) bool {
	payload := asMap(result[connection])
	return asBool(asMap(payload["pageInfo"])["hasNextPage"])
}

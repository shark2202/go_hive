package cloudflare

// Wire: one bounded, authenticated request path (doRequest) and the reply
// mapping that mirrors the upstream _make_request branches.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// apiReply carries a decoded Cloudflare envelope or an error payload.
type apiReply struct {
	status   int
	result   any
	envelope map[string]any
	failure  map[string]any
}

func (reply apiReply) ok() bool { return reply.failure == nil }

// value returns the failure payload when the call failed, mirroring the
// upstream habit of assigning an error dict to a result variable.
func (reply apiReply) value() any {
	if reply.failure != nil {
		return reply.failure
	}
	return reply.result
}

// apiToken resolves CLOUDFLARE_API_TOKEN from WithCredential or env.
func apiToken(rt *Runtime) string {
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

// baseURL resolves the API root, honouring WithBaseURL.
func baseURL(rt *Runtime) string {
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			if base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"); base != "" {
				return base
			}
		}
	}
	return defaultBaseURL
}

// doRequest performs one authenticated request and maps the response.
func doRequest(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) apiReply {
	token := apiToken(rt)
	if token == "" {
		return apiReply{failure: map[string]any{
			"error": "CLOUDFLARE_API_TOKEN is required",
			"help":  "Set CLOUDFLARE_API_TOKEN env var or configure credential store",
		}}
	}
	target := baseURL(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return failureReply("Unexpected error: " + err.Error())
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return failureReply("Unexpected error: " + err.Error())
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := runtimeHTTP(rt).Do(request)
	if err != nil {
		if isTimeout(ctx, err) {
			return failureReply("Request timed out")
		}
		return failureReply("Request failed: " + err.Error())
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return failureReply("Request failed: " + err.Error())
	}
	return decodeReply(response.StatusCode, response.Header.Get("Retry-After"), raw)
}

// failureReply builds an error reply.
func failureReply(message string) apiReply {
	return apiReply{failure: map[string]any{"error": message}}
}

// decodeReply maps one HTTP response to a reply, matching the
// upstream _make_request branches.
func decodeReply(status int, retryAfter string, raw []byte) apiReply {
	switch status {
	case http.StatusUnauthorized:
		return apiReply{status: status, failure: map[string]any{
			"error":       "Unauthorized - invalid or missing CLOUDFLARE_API_TOKEN",
			"status_code": status,
		}}
	case http.StatusForbidden:
		return apiReply{status: status, failure: map[string]any{
			"error":       "Forbidden - token lacks required permissions (Zone/DNS read or edit)",
			"status_code": status,
		}}
	case http.StatusNotFound:
		return apiReply{status: status, failure: map[string]any{
			"error":       "Not found",
			"status_code": status,
		}}
	case http.StatusTooManyRequests:
		var hint any
		if retryAfter != "" {
			hint = retryAfter
		}
		return apiReply{status: status, failure: map[string]any{
			"error":       "Too many requests - rate limited",
			"status_code": status,
			"retry_after": hint,
		}}
	case http.StatusNoContent:
		return apiReply{status: status, result: map[string]any{
			"success": true,
			"message": "Operation completed successfully",
		}}
	}
	if status >= 400 {
		message := providerError(raw)
		return apiReply{status: status, failure: map[string]any{
			"error":       fmt.Sprintf("HTTP %d: %s", status, message),
			"status_code": status,
		}}
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return failureReply("Unexpected error: invalid JSON response")
	}
	if success, _ := envelope["success"].(bool); !success {
		message := "Unknown error"
		if detail := envelopeError(envelope); detail != "" {
			message = detail
		}
		return failureReply(message)
	}
	result, present := envelope["result"]
	if !present {
		result = map[string]any{}
	}
	return apiReply{status: status, result: result, envelope: envelope}
}

// envelopeError reads errors[0].message from a decoded envelope.
func envelopeError(envelope map[string]any) string {
	errorsList, _ := envelope["errors"].([]any)
	if len(errorsList) == 0 {
		return ""
	}
	first, _ := errorsList[0].(map[string]any)
	return textAt(first, "message")
}

// providerError renders the provider message for a >=400 response,
// falling back to the upstream `response.text[:500]` slice.
func providerError(raw []byte) string {
	if envelope, ok := decodeObject(raw); ok {
		if message := envelopeError(envelope); message != "" {
			return message
		}
	}
	return truncateText(string(raw), maxErrorBytes)
}

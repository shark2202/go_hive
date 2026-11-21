package cloudflare

// Dispatch: turns a catalog row into an ActionFunc. Required-argument
// checks run before anything else; rows with a Run override bypass the
// declarative single-request flow. Path substitution and the upstream
// zone/domain validation live here too.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// actionImpl turns a catalog row into an ActionFunc.
func actionImpl(action actionSpec) ActionFunc {
	return func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		for _, name := range action.Required {
			if _, present := input[name]; !present {
				return map[string]any{"error": name + " is required"}, nil
			}
		}
		if action.Run != nil {
			return action.Run(ctx, rt, input)
		}
		return dispatchSingle(ctx, rt, action, input)
	}
}

// dispatchSingle executes the declarative single-request flow.
func dispatchSingle(
	ctx context.Context,
	rt *Runtime,
	action actionSpec,
	input map[string]any,
) (any, error) {
	if strings.Contains(action.Path, "{zone_id}") {
		if failure := validateZoneID(stringArg(input, "zone_id")); failure != nil {
			return failure, nil
		}
	}
	path, fillErr := fillPath(action.Path, input)
	if fillErr != nil {
		return map[string]any{"error": fillErr.Error()}, nil
	}
	query := url.Values{}
	for _, name := range action.Query {
		if value := strings.TrimSpace(stringArg(input, name)); value != "" {
			query.Set(name, value)
		}
	}
	payload := map[string]any{}
	for field, source := range action.BodyMap {
		value, present := input[source]
		if !present || value == nil {
			continue
		}
		payload[field] = value
	}
	for field, value := range action.Consts {
		payload[field] = value
	}
	var body any
	if len(payload) > 0 {
		body = payload
	}
	reply := doRequest(ctx, rt, action.Method, path, query, body)
	if !reply.ok() {
		if action.WrapErrors && action.Wrap != "" {
			return map[string]any{action.Wrap: reply.failure}, nil
		}
		return reply.failure, nil
	}
	if action.Shape != nil {
		return action.Shape(reply, input), nil
	}
	if action.Wrap != "" {
		return map[string]any{action.Wrap: reply.result}, nil
	}
	return reply.result, nil
}

// fillPath substitutes {token} placeholders from the action input.
func fillPath(template string, input map[string]any) (string, error) {
	path := template
	for _, token := range []string{
		"zone_id", "account_id", "record_id", "setting_id",
		"rule_id", "member_id", "application_id",
	} {
		placeholder := "{" + token + "}"
		if !strings.Contains(path, placeholder) {
			continue
		}
		value := strings.TrimSpace(stringArg(input, token))
		if value == "" {
			return "", fmt.Errorf("%s is required", token)
		}
		path = strings.ReplaceAll(path, placeholder, url.PathEscape(value))
	}
	return path, nil
}

// validateZoneID mirrors upstream _validate_zone_id.
func validateZoneID(zoneID string) map[string]any {
	if zoneID == "" {
		return map[string]any{"error": "zone_id must be a non-empty string"}
	}
	lowered := strings.ToLower(zoneID)
	valid := len(zoneID) == 32
	if valid {
		for _, char := range lowered {
			if !strings.ContainsRune("0123456789abcdef", char) {
				valid = false
				break
			}
		}
	}
	if !valid {
		return map[string]any{"error": "Invalid zone_id format: " + zoneID}
	}
	return nil
}

// validateDomain mirrors upstream _validate_domain.
func validateDomain(domain string) map[string]any {
	if domain == "" {
		return map[string]any{"error": "domain must be a non-empty string"}
	}
	lowered := strings.ToLower(strings.TrimSpace(domain))
	if len(lowered) < 3 || len(lowered) > 255 {
		return map[string]any{"error": "Domain length must be 3-255 characters: " + lowered}
	}
	for _, char := range lowered {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789.-", char) {
			return map[string]any{"error": "Invalid domain format: " + lowered}
		}
	}
	return nil
}

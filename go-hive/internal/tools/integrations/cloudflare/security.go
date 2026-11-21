package cloudflare

// Security face: firewall events and rules, page rules, and the security
// settings summary.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// shapePageRules mirrors cloudflare_list_page_rules.
func shapePageRules(reply apiReply, _ map[string]any) any {
	rules := make([]map[string]any, 0)
	for _, raw := range sliceAt(map[string]any{"rules": reply.result}, "rules") {
		source := asMap(raw)
		rules = append(rules, map[string]any{
			"id":       source["id"],
			"targets":  source["targets"],
			"actions":  source["actions"],
			"priority": source["priority"],
			"status":   textAt(source, "status"),
		})
	}
	return map[string]any{"rules": rules}
}

// shapeFirewallRules mirrors cloudflare_list_firewall_rules.
func shapeFirewallRules(reply apiReply, _ map[string]any) any {
	entrypoint := asMap(reply.result)
	return map[string]any{"firewall_rules": sliceAt(entrypoint, "rules")}
}

// listFirewallEvents mirrors cloudflare_list_firewall_events.
func listFirewallEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	limit := clampInt(intArg(input, "limit", 10), 0, 1<<31-1)
	query := url.Values{"per_page": {strconv.Itoa(clampInt(limit, 0, 50))}}
	reply := doRequest(ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/firewall/events", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	events := sliceValue(reply.result)
	if events == nil {
		events = sliceAt(asMap(reply.result), "events")
	}
	if limit < len(events) {
		events = events[:limit]
	}
	return map[string]any{"events": events}, nil
}

// getSecuritySettings mirrors cloudflare_get_security_settings.
func getSecuritySettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	level, _ := settingValue(doRequest(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/security_level", nil, nil,
	))
	ssl, _ := settingValue(doRequest(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/ssl", nil, nil,
	))
	wafValue, wafIsObject := settingValue(doRequest(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/waf", nil, nil,
	))
	var wafEnabled any
	if wafIsObject {
		text, _ := wafValue.(string)
		wafEnabled = text == "on"
	}
	return map[string]any{
		"security_level": level,
		"ssl_mode":       ssl,
		"waf_enabled":    wafEnabled,
	}, nil
}

// createFirewallRule mirrors cloudflare_create_firewall_rule,
// including the PUT fallback when the phase entrypoint does not exist yet.
func createFirewallRule(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	ruleAction := strings.ToLower(strings.TrimSpace(stringArg(input, "rule_action")))
	if ruleAction == "" {
		return map[string]any{"error": "rule_action is required"}, nil
	}
	expression := stringArg(input, "expression")
	if expression == "" {
		return map[string]any{"error": "expression is required"}, nil
	}
	description := stringArg(input, "description")
	if description == "" {
		description = "Rule created via MCP"
	}
	rule := map[string]any{
		"action":      ruleAction,
		"expression":  expression,
		"description": description,
		"enabled":     true,
	}
	base := "/zones/" + url.PathEscape(zoneID) + "/rulesets/phases/http_request_firewall_custom/entrypoint"
	reply := doRequest(ctx, rt, http.MethodPost, base+"/rules", nil, rule)
	if reply.ok() {
		return map[string]any{"created_rules": reply.result}, nil
	}
	if reply.status != http.StatusNotFound {
		return map[string]any{"created_rules": reply.failure}, nil
	}
	reply = doRequest(ctx, rt, http.MethodPut, base, nil, map[string]any{"rules": []any{rule}})
	if !reply.ok() {
		return map[string]any{"created_rules": reply.failure}, nil
	}
	return map[string]any{"created_rules": reply.result}, nil
}

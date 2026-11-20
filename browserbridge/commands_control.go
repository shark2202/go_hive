package browserbridge

import (
	"context"
	"fmt"
	"strings"
)

// commands_control.go exposes the L2 bridge-method surface over the same
// Execute/HTTP command channel. These names mirror upstream BeelineBridge
// methods and the control-RPC allowlist; they are not upstream MCP tool names.
var bridgeCommandTable = map[string]commandFunc{
	"create_context":       cmdCreateContext,
	"register_context":     cmdRegisterContext,
	"destroy_context":      cmdDestroyContext,
	"list_contexts":        cmdListContexts,
	"adopt_tab":            cmdAdoptTab,
	"release_tab":          cmdReleaseTab,
	"audit_tab":            cmdAuditTab,
	"tab_health":           cmdTabHealth,
	"get_tab_blockers":     cmdGetTabBlockers,
	"record_action":        cmdRecordAction,
	"get_tab_actions":      cmdGetTabActions,
	"get_pending_dialog":   cmdGetPendingDialog,
	"list_pending_dialogs": cmdListPendingDialogs,
}

func cmdCreateContext(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	agentID, _ := paramStringAny(params, "agent_id", "agentId")
	if agentID == "" {
		agentID = profile
	}
	agentID = ResolveSession(agentID)
	displayName, _ := paramStringAny(params, "display_name", "displayName")
	if strings.TrimSpace(displayName) == "" {
		displayName = ResolveDisplayName()
	}
	browserProfile, _ := paramString(params, "browser_profile")
	return b.CreateContext(ctx, agentID, displayName, browserProfile)
}

func cmdRegisterContext(_ context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	profile, _ := paramStringAny(params, "profile", "agent_id", "agentId")
	groupID := paramIntAny(params, 0, "group_id", "groupId")
	name, _ := paramString(params, "name")
	browserProfile, _ := paramString(params, "browser_profile")
	return b.RegisterContext(profile, groupID, name, browserProfile), nil
}

func cmdDestroyContext(ctx context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	groupID := paramIntAny(params, 0, "group_id", "groupId")
	browserProfile, _ := paramString(params, "browser_profile")
	return b.DestroyContext(ctx, groupID, browserProfile)
}

func cmdListContexts(ctx context.Context, b *Bridge, _ string, _ map[string]any) (any, error) {
	return b.ListContexts(ctx), nil
}

func cmdAdoptTab(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	agent, _ := paramStringAny(params, "profile", "agent_id", "agentId")
	if agent == "" {
		agent = profile
	}
	agent = ResolveSession(agent)
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	fromUser := paramBoolAny(params, false, "from_user", "fromUser")
	return b.AdoptTab(ctx, agent, tabID, fromUser)
}

func cmdReleaseTab(ctx context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	fromUser := paramBoolAny(params, false, "from_user", "fromUser")
	return b.ReleaseTab(ctx, tabID, fromUser)
}

func cmdAuditTab(ctx context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	return b.AuditTab(ctx, tabID)
}

func cmdTabHealth(ctx context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	force := paramBoolAny(params, false, "force_audit", "forceAudit", "force")
	return b.TabHealth(ctx, tabID, force), nil
}

func cmdGetTabBlockers(_ context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	return b.GetTabBlockers(tabID), nil
}

func cmdRecordAction(_ context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	verb, _ := paramString(params, "verb")
	target, _ := paramString(params, "target")
	ok := paramBoolAny(params, true, "ok")
	var tsMS int64
	if raw, exists := params["ts_ms"]; exists && raw != nil {
		if n, valid := toInt64(raw); valid {
			tsMS = n
		}
	}
	b.recordAction(tabID, verb, target, ok, tsMS)
	return map[string]any{"ok": true}, nil
}

func cmdGetTabActions(_ context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	limit := paramIntAny(params, 8, "limit")
	var since *int64
	if v, exists := params["since_ms"]; exists && v != nil {
		if n, ok := toInt64(v); ok {
			since = &n
		}
	}
	return b.TabActions(tabID, limit, since), nil
}

func cmdGetPendingDialog(_ context.Context, b *Bridge, _ string, params map[string]any) (any, error) {
	tabID := paramIntAny(params, 0, "tab_id", "tabId")
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	return b.dialogSnapshot(tabID), nil
}

func cmdListPendingDialogs(_ context.Context, b *Bridge, _ string, _ map[string]any) (any, error) {
	dialogs := b.PendingDialogs()
	out := make([]map[string]any, 0, len(dialogs))
	for tab, d := range dialogs {
		out = append(out, map[string]any{
			"tab_id":         tab,
			"type":           d["type"],
			"message":        d["message"],
			"default_prompt": d["default_prompt"],
			"url":            d["url"],
			"opened_at_ms":   d["opened_at_ms"],
		})
	}
	return out, nil
}

// actionForCommand maps a completed tool call to the side panel's verb/target
// shape. Observation tools are intentionally absent (upstream logs them to
// JSONL but does not push action-history rows).
func actionForCommand(typ string, params map[string]any) (string, string, bool) {
	switch typ {
	case "browser_open":
		u, _ := paramString(params, "url")
		return "open", u, true
	case "browser_navigate":
		u, _ := paramString(params, "url")
		return "navigate", u, true
	case "browser_click":
		s, _ := paramString(params, "selector")
		return "click", s, true
	case "browser_type":
		s, _ := paramString(params, "selector")
		return "type", s, true
	case "browser_type_focused":
		return "type", "", true
	case "browser_press":
		k, _ := paramString(params, "key")
		return "press", k, true
	case "browser_scroll":
		d, _ := paramString(params, "direction")
		if d == "" {
			d = strings.ToLower(paramStringOr(params, "direction", "down"))
		}
		return "scroll", d, true
	case "browser_evaluate":
		s, _ := paramString(params, "script")
		return "evaluate", truncateLine(s, 120), true
	case "browser_reload":
		return "reload", "", true
	case "browser_interact":
		return interactHistoryAction(params)
	case "browser_select":
		selector, _ := paramString(params, "selector")
		values, _ := paramStringList(params, "values")
		target := selector
		if len(values) > 0 {
			target = selector + " = " + strings.Join(values, ", ")
		}
		return "select", truncateLine(target, 80), true
	}
	return "", "", false
}

// interactHistoryAction mirrors interact._interact_action: pure observation
// actions (screenshot, zoom) produce no history row.
func interactHistoryAction(params map[string]any) (string, string, bool) {
	action, _ := paramString(params, "action")
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "left_click", "right_click", "middle_click", "double_click", "triple_click":
		verb := "click"
		if selector, ok := paramString(params, "selector"); ok && selector != "" {
			return verb, truncateLine(selector, 80), true
		}
		if coord, ok, _ := paramFloatList(params, "coordinate"); ok && len(coord) == 2 {
			return verb, fmt.Sprintf("(%.2f, %.2f)", coord[0], coord[1]), true
		}
		return verb, "", true
	case "hover":
		if selector, ok := paramString(params, "selector"); ok && selector != "" {
			return "hover", truncateLine(selector, 80), true
		}
		if coord, ok, _ := paramFloatList(params, "coordinate"); ok && len(coord) == 2 {
			return "hover", fmt.Sprintf("(%.2f, %.2f)", coord[0], coord[1]), true
		}
		return "hover", "", true
	case "type":
		text, _ := paramString(params, "text")
		return "type", truncateLine(text, 80), true
	case "key":
		text, _ := paramString(params, "text")
		return "key", text, true
	case "scroll":
		return "scroll", paramStringDefault(params, "scroll_direction", "down"), true
	case "drag":
		start, _ := paramString(params, "start_selector")
		end, _ := paramString(params, "selector")
		switch {
		case start != "" && end != "":
			return "drag", start + " → " + end, true
		case start != "":
			return "drag", start, true
		default:
			return "drag", end, true
		}
	case "wait":
		if selector, ok := paramString(params, "wait_for_selector"); ok && selector != "" {
			return "wait", "selector=" + truncateLine(selector, 80), true
		}
		if text, ok := paramString(params, "wait_for_text"); ok && text != "" {
			return "wait", "text=" + truncateLine(text, 80), true
		}
		if duration, ok := params["duration"]; ok && duration != nil {
			return "wait", fmt.Sprintf("%vs", duration), true
		}
		return "wait", "", true
	}
	return "", "", false
}

func resultFailed(result any) bool {
	if m, ok := result.(map[string]any); ok {
		if v, exists := m["ok"].(bool); exists {
			return !v
		}
	}
	return false
}

func paramStringOr(params map[string]any, key, def string) string {
	if v, ok := paramString(params, key); ok && v != "" {
		return v
	}
	return def
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case string:
		var out int64
		if _, err := fmt.Sscan(n, &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

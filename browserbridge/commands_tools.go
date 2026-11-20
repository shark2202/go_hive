package browserbridge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The handlers in this file implement the upstream MCP tool surface for the
// L0/L1 subset. Parameter names, defaults and result field names follow
// tools/src/gcu/browser/tools/*.py; see PARITY.md for the per-tool status.

func cmdBrowserTabs(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	res, err := cmdTabsList(ctx, b, routeProfile(profile, params), params)
	if err != nil {
		return nil, err
	}
	m, _ := res.(map[string]any)
	tabs, _ := m["tabs"].([]any)
	out := map[string]any{
		"ok":          true,
		"total":       len(tabs),
		"activeTabId": nil,
		"tabs":        tabs,
	}
	if len(tabs) > 3 {
		out["tabsHint"] = fmt.Sprintf(
			"You have %d tabs open in this group. Close tabs you no longer need with browser_close(tab_id=...) before opening or navigating more — too many open tabs slow the browser, consume memory, and have empirically caused workers to fail their task.",
			len(tabs))
	}
	return out, nil
}

func cmdBrowserOpen(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	url, _ := paramString(params, "url")
	if strings.TrimSpace(url) == "" {
		return nil, badRequest("missing required parameter 'url'")
	}
	background := paramBool(params, "background", false)
	label := routeProfile(profile, params)
	groupID := paramIntAny(params, 0, "group_id", "groupId")
	var conn *Connection
	var err error
	if groupID > 0 {
		conn, err = b.resolveForGroup(groupID, label)
	} else {
		conn, err = b.resolve(label, nil)
	}
	if err != nil {
		return nil, err
	}
	label = conn.Label()

	internal := map[string]any{"url": url}
	if gid, ok := params["group_id"]; ok {
		internal["groupId"] = gid
	} else if gid, ok := params["groupId"]; ok {
		internal["groupId"] = gid
	}
	created, err := cmdTabOpen(ctx, b, label, internal)
	if err != nil {
		return nil, err
	}
	m, _ := created.(map[string]any)
	tabID, _ := toInt(m["tabId"])
	if tabID <= 0 {
		return nil, newError(CodeExtensionError, "tab.create returned no tabId", false)
	}
	if !background {
		if _, err := conn.Call(ctx, "tab.activate", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout); err != nil {
			return nil, err
		}
	}
	title := ""
	if !isPrivilegedScheme(url) {
		nav, navErr := cmdNavigate(ctx, b, label, map[string]any{
			"tabId": tabID, "url": url, "waitUntil": "load", "timeoutMs": 30000,
		})
		if navErr != nil {
			return map[string]any{
				"ok": false, "error": navErr.Error(), "tabId": tabID,
				"hint": "A tab was created before this error. Call browser_tabs to verify state, or browser_close to discard it, before retrying browser_open.",
			}, nil
		}
		if nm, ok := nav.(map[string]any); ok {
			if u, ok := nm["url"].(string); ok && u != "" {
				url = u
			}
			if t, ok := nm["title"].(string); ok {
				title = t
			}
			if okFlag, exists := nm["ok"].(bool); exists && !okFlag {
				return map[string]any{
					"ok": false, "error": nm["error"], "tabId": tabID, "url": url,
					"hint": "The tab exists; inspect it with browser_tabs before retrying.",
				}, nil
			}
		}
	}
	b.recordAction(tabID, "open", url, true, 0)
	return map[string]any{
		"ok":              true,
		"tabId":           tabID,
		"url":             url,
		"title":           title,
		"background":      background,
		"browser_profile": label,
	}, nil
}

func cmdBrowserClose(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, ok, err := paramTabIDOptional(params)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(CodeInvalidState,
			"No tab to close: tab_id is required because active-tab context tracking is not ported", false)
	}
	if _, err := cmdTabClose(ctx, b, routeProfile(profile, params), map[string]any{"tabId": tabID}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "closed": tabID}, nil
}

func cmdBrowserActivateTab(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Call(ctx, "tab.activate", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "tabId": tabID}, nil
}

func cmdBrowserNavigate(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	url, _ := paramString(params, "url")
	if strings.TrimSpace(url) == "" {
		return nil, badRequest("missing required parameter 'url'")
	}
	waitUntil, _ := paramString(params, "wait_until")
	if waitUntil == "" {
		waitUntil, _ = paramString(params, "waitUntil")
	}
	if waitUntil == "" {
		waitUntil = "load"
	}
	label := routeProfile(profile, params)
	conn, err := b.resolve(label, &tabID)
	if err != nil {
		return nil, err
	}
	plan := b.planProfileView(ctx, conn, tabID, url)
	if plan != nil && plan.blocked != nil {
		return plan.blocked, nil
	}
	nav, err := cmdNavigate(ctx, b, label, map[string]any{
		"tabId": tabID, "url": url, "waitUntil": waitUntil, "timeoutMs": 30000,
	})
	if err != nil {
		return nil, err
	}
	m, _ := nav.(map[string]any)
	if okFlag, exists := m["ok"].(bool); exists && !okFlag {
		m["tabId"] = tabID
		return m, nil
	}
	if plan != nil {
		plan.record(b.rateLimiter)
	}
	return map[string]any{
		"ok":              true,
		"tabId":           tabID,
		"url":             m["url"],
		"title":           m["title"],
		"browser_profile": label,
	}, nil
}

func cmdBrowserClick(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	selector, _ := paramString(params, "selector")
	if strings.TrimSpace(selector) == "" {
		return nil, badRequest("missing required parameter 'selector'")
	}
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	clickCount := 1
	if paramBool(params, "double_click", false) {
		clickCount = 2
	}
	res, err := cmdClick(ctx, b, routeProfile(profile, params), map[string]any{
		"tabId":      tabID,
		"selector":   selector,
		"button":     paramStringDefault(params, "button", "left"),
		"clickCount": clickCount,
		"timeoutMs":  paramIntAny(params, 5000, "timeout_ms", "timeoutMs"),
	})
	if err != nil {
		return nil, err
	}
	return attachAutoSnapshot(ctx, b, tabID, res, paramStringDefault(params, "auto_snapshot_mode", "simple"))
}

func cmdBrowserType(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	selector, _ := paramString(params, "selector")
	if strings.TrimSpace(selector) == "" {
		return nil, badRequest("missing required parameter 'selector'")
	}
	return runBrowserType(ctx, b, profile, params, selector)
}

func cmdBrowserTypeFocused(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	return runBrowserType(ctx, b, profile, params, "")
}

func runBrowserType(ctx context.Context, b *Bridge, profile string, params map[string]any, selector string) (any, error) {
	text, ok := paramString(params, "text")
	if !ok {
		return nil, badRequest("missing required parameter 'text'")
	}
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	internal := map[string]any{
		"tabId":         tabID,
		"text":          text,
		"clearFirst":    paramBoolAny(params, true, "clear_first", "clearFirst"),
		"delayMs":       paramIntAny(params, 1, "delay_ms", "delayMs"),
		"useInsertText": paramBoolAny(params, true, "use_insert_text", "useInsertText"),
		"timeoutMs":     paramIntAny(params, 30000, "timeout_ms", "timeoutMs"),
	}
	if selector != "" {
		internal["selector"] = selector
	}
	res, err := cmdType(ctx, b, routeProfile(profile, params), internal)
	if err != nil {
		return nil, err
	}
	return attachAutoSnapshot(ctx, b, tabID, res, paramStringDefault(params, "auto_snapshot_mode", "simple"))
}

func cmdBrowserPress(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	key, _ := paramString(params, "key")
	if key == "" {
		return nil, badRequest("missing required parameter 'key'")
	}
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	internal := map[string]any{"tabId": tabID, "key": key}
	if sel, ok := paramString(params, "selector"); ok {
		internal["selector"] = sel
	}
	if mods, ok := params["modifiers"]; ok {
		internal["modifiers"] = mods
	}
	return cmdKeypress(ctx, b, routeProfile(profile, params), internal)
}

func cmdBrowserScroll(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	internal := map[string]any{
		"tabId":     tabID,
		"direction": paramStringDefault(params, "direction", "down"),
		"amount":    paramInt(params, "amount", 500),
	}
	if sel, ok := paramString(params, "selector"); ok {
		internal["selector"] = sel
	}
	res, err := cmdScroll(ctx, b, routeProfile(profile, params), internal)
	if err != nil {
		return nil, err
	}
	return attachAutoSnapshot(ctx, b, tabID, res, paramStringDefault(params, "auto_snapshot_mode", "simple"))
}

func cmdBrowserSnapshot(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	internal := map[string]any{
		"tabId": tabID,
		"mode":  paramStringDefault(params, "mode", "default"),
	}
	if max, ok := params["maxNodes"]; ok {
		internal["maxNodes"] = max
	}
	return cmdSnapshot(ctx, b, routeProfile(profile, params), internal)
}

func cmdBrowserScreenshot(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	internal := map[string]any{
		"tabId":    tabID,
		"fullPage": paramBoolAny(params, false, "full_page", "fullPage"),
	}
	if sel, ok := paramString(params, "selector"); ok {
		internal["selector"] = sel
	}
	if _, ok := paramString(params, "intent"); !ok {
		return nil, badRequest("missing required parameter 'intent'")
	}
	return cmdScreenshot(ctx, b, routeProfile(profile, params), internal)
}

func cmdBrowserEvaluate(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	script, ok := paramString(params, "script")
	if !ok {
		return nil, badRequest("missing required parameter 'script'")
	}
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	internal := map[string]any{"tabId": tabID, "script": script}
	return cmdEval(ctx, b, routeProfile(profile, params), internal)
}

func cmdBrowserReload(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	_, _ = b.cdp(ctx, conn, tabID, "Page.enable", nil, 10*time.Second)
	if _, err := b.cdp(ctx, conn, tabID, "Page.reload", nil, b.cfg.CommandTimeout); err != nil {
		return nil, err
	}
	v, _ := b.evalJS(ctx, conn, tabID, "window.location.href", 10*time.Second)
	url, _ := v.(string)
	return map[string]any{"ok": true, "action": "reload", "url": url, "tabId": tabID}, nil
}

func attachAutoSnapshot(ctx context.Context, b *Bridge, tabID int, result any, mode string) (any, error) {
	m, ok := result.(map[string]any)
	if !ok {
		return result, nil
	}
	if okFlag, _ := m["ok"].(bool); !okFlag || mode == "off" {
		return m, nil
	}
	switch mode {
	case "default", "simple", "interactive":
	default:
		return nil, badRequest("'auto_snapshot_mode' must be default, simple, interactive or off")
	}
	select {
	case <-ctx.Done():
		m["snapshot_error"] = "cancelled before auto-snapshot"
		return m, nil
	case <-time.After(500 * time.Millisecond):
	}
	snap, err := cmdSnapshot(ctx, b, "", map[string]any{"tabId": tabID, "mode": mode})
	if err != nil {
		m["snapshot_error"] = err.Error()
		return m, nil
	}
	m["snapshot"] = snap
	return m, nil
}

func paramStringDefault(params map[string]any, key, def string) string {
	if s, ok := paramString(params, key); ok && s != "" {
		return s
	}
	return def
}

func isPrivilegedScheme(url string) bool {
	for _, prefix := range []string{
		"chrome://", "chrome-extension://", "devtools://", "view-source:",
		"chrome-search://", "chrome-error://", "chrome-untrusted://",
	} {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

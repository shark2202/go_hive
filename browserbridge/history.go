package browserbridge

// history.go ports the bridge methods the control-RPC allowlist exposes but the
// earlier Go layers did not need: history navigation (go_back / go_forward),
// reveal_tab, get_attribute and cdp_detach.
//
// They live together because they share one theme: the side panel and the
// out-of-process tool clients reach them directly through 14831, not through
// an MCP tool, so nothing else in the port exercises them.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// pendingDialogResult mirrors bridge._pending_dialog_result: the stable
// {ok:false, pending_dialog:{...}} envelope navigate/reload/history use when a
// native dialog blocks the operation.
func (b *Bridge) pendingDialogResult(tabID int, action string) map[string]any {
	dialog := b.PendingDialog(tabID)
	if dialog == nil {
		return nil
	}
	envelope := map[string]any{
		"tab_id":         tabID,
		"type":           dialog["type"],
		"message":        dialog["message"],
		"default_prompt": dialog["default_prompt"],
		"url":            dialog["url"],
	}
	return map[string]any{
		"ok":             false,
		"error":          action + " blocked by native browser dialog",
		"action":         action,
		"pending_dialog": envelope,
	}
}

// RevealTab activates a tab AND raises its Chrome window — the user-initiated
// jump-to-tab path. Kept separate from activate_tab so agent actions never
// steal the user's window focus. Degrades to a plain activate when the
// installed extension predates tab.reveal (tagged with fallback:"activate" so
// callers can tell the two apart).
func (b *Bridge) RevealTab(ctx context.Context, tabID int) (map[string]any, error) {
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	conn, err := b.resolve("", &tabID)
	if err != nil {
		return nil, err
	}
	raw, err := conn.Call(ctx, "tab.reveal", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout)
	if err == nil {
		result, mapErr := resultMap(raw)
		if mapErr != nil {
			return nil, mapErr
		}
		result["tabId"] = tabID
		return result, nil
	}
	if !strings.Contains(strings.ToLower(AsError(err).Message), "unknown command") {
		return nil, err
	}
	b.logf("reveal_tab: extension lacks tab.reveal; falling back to tab.activate")
	raw, err = conn.Call(ctx, "tab.activate", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	result, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	result["tabId"] = tabID
	result["fallback"] = "activate"
	return result, nil
}

// GoBackHistory navigates back via history.back() (modern Chrome CDP has no
// Page.goBack; navigateToHistoryEntry needs the history list first).
func (b *Bridge) GoBackHistory(ctx context.Context, tabID int) (map[string]any, error) {
	return b.historyStep(ctx, tabID, "back", "history.back()")
}

// GoForwardHistory is the forward twin of GoBackHistory.
func (b *Bridge) GoForwardHistory(ctx context.Context, tabID int) (map[string]any, error) {
	return b.historyStep(ctx, tabID, "forward", "history.forward()")
}

func (b *Bridge) historyStep(ctx context.Context, tabID int, action, expression string) (map[string]any, error) {
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	conn, err := b.resolve("", &tabID)
	if err != nil {
		return nil, err
	}
	_, _ = b.cdp(ctx, conn, tabID, "Page.enable", nil, 10*time.Second)
	if _, err := b.cdp(ctx, conn, tabID, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true,
	}, 10*time.Second); err != nil {
		return nil, err
	}
	// Give the browser a beat to commit the navigation before reading the URL.
	select {
	case <-ctx.Done():
		return nil, newError(CodeTimeout, "history navigation cancelled", false)
	case <-time.After(300 * time.Millisecond):
	}
	// A beforeunload handler can block the step with a native dialog. Surface
	// it instead of returning the old URL as if the step succeeded.
	if envelope := b.pendingDialogResult(tabID, action); envelope != nil {
		return envelope, nil
	}
	value, err := b.evalJS(ctx, conn, tabID, "window.location.href", 10*time.Second)
	if err != nil {
		return nil, err
	}
	url, _ := value.(string)
	return map[string]any{"ok": true, "action": action, "url": url}, nil
}

// GetAttribute polls an element's attribute value until it exists
// (bridge.get_attribute).
func (b *Bridge) GetAttribute(ctx context.Context, tabID int, selector, attribute string, timeout time.Duration) (map[string]any, error) {
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	if strings.TrimSpace(selector) == "" {
		return nil, badRequest("missing required parameter 'selector'")
	}
	if strings.TrimSpace(attribute) == "" {
		return nil, badRequest("missing required parameter 'attribute'")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	conn, err := b.resolve("", &tabID)
	if err != nil {
		return nil, err
	}
	expr := fmt.Sprintf("(function(){ const el = document.querySelector(%s); return el ? el.getAttribute(%s) : null; })()",
		jsString(selector), jsString(attribute))
	start := time.Now()
	for {
		value, err := b.evalJS(ctx, conn, tabID, expr, 10*time.Second)
		if err != nil {
			return nil, err
		}
		if value != nil {
			return map[string]any{
				"ok": true, "selector": selector, "attribute": attribute, "value": value,
			}, nil
		}
		if time.Since(start) >= timeout {
			return map[string]any{"ok": false, "error": "Element not found: " + selector}, nil
		}
		select {
		case <-ctx.Done():
			return nil, newError(CodeTimeout, "get_attribute cancelled", false)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// CDPDetach detaches the debugger and drops the health state that belonged to
// that attach session (bridge.cdp_detach).
func (b *Bridge) CDPDetach(ctx context.Context, tabID int) (map[string]any, error) {
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	conn, err := b.resolve("", &tabID)
	if err != nil {
		return nil, err
	}
	raw, err := conn.Call(ctx, "cdp.detach", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	conn.forgetAttached(tabID)
	b.clearTabHealth(tabID)
	result, mapErr := resultMap(raw)
	if mapErr != nil {
		return nil, mapErr
	}
	result["tabId"] = tabID
	return result, nil
}

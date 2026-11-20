package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

type commandFunc func(context.Context, *Bridge, string, map[string]any) (any, error)

var commandTable = map[string]commandFunc{
	// Upstream MCP tool names — the canonical surface.
	"browser_setup":          cmdBrowserSetup,
	"browser_status":         cmdBrowserStatus,
	"browser_stop":           cmdBrowserStop,
	"browser_tabs":           cmdBrowserTabs,
	"browser_open":           cmdBrowserOpen,
	"browser_close":          cmdBrowserClose,
	"browser_activate_tab":   cmdBrowserActivateTab,
	"browser_navigate":       cmdBrowserNavigate,
	"browser_reload":         cmdBrowserReload,
	"browser_interact":       cmdBrowserInteract,
	"browser_select":         cmdBrowserSelect,
	"browser_get_text":       cmdBrowserGetText,
	"browser_resize":         cmdBrowserResize,
	"browser_upload":         cmdBrowserUpload,
	"browser_dialog_respond": cmdBrowserDialogRespond,
	"browser_console":        cmdBrowserConsole,
	"browser_html":           cmdBrowserHTML,
	"browser_shadow_query":   cmdBrowserShadowQuery,
	"browser_script":         cmdBrowserScript,
	"browser_click":          cmdBrowserClick,
	"browser_type":           cmdBrowserType,
	"browser_type_focused":   cmdBrowserTypeFocused,
	"browser_press":          cmdBrowserPress,
	"browser_scroll":         cmdBrowserScroll,
	"browser_snapshot":       cmdBrowserSnapshot,
	"browser_screenshot":     cmdBrowserScreenshot,
	"browser_evaluate":       cmdBrowserEvaluate,
	// Legacy aliases from the first migration brief. They are documented as
	// compatibility shims only; upstream names are canonical.
	"tabs.list":       cmdBrowserTabs,
	"tab.list":        cmdBrowserTabs,
	"tab.open":        cmdBrowserOpen,
	"tab.create":      cmdBrowserOpen,
	"tab.close":       cmdBrowserClose,
	"tab.navigate":    cmdBrowserNavigate,
	"page.navigate":   cmdBrowserNavigate,
	"page.click":      cmdBrowserClick,
	"page.type":       cmdBrowserType,
	"page.keypress":   cmdBrowserPress,
	"page.scroll":     cmdBrowserScroll,
	"page.snapshot":   cmdBrowserSnapshot,
	"page.screenshot": cmdBrowserScreenshot,
	"page.eval":       cmdBrowserEvaluate,
}

var canonicalCommands = []string{
	"browser_setup", "browser_status", "browser_stop",
	"browser_tabs", "browser_open", "browser_close", "browser_activate_tab",
	"browser_navigate", "browser_reload",
	"browser_interact", "browser_select",
	"browser_get_text", "browser_resize", "browser_upload", "browser_dialog_respond",
	"browser_console", "browser_html", "browser_shadow_query", "browser_script",
	"browser_click", "browser_type", "browser_type_focused", "browser_press",
	"browser_scroll", "browser_snapshot", "browser_screenshot", "browser_evaluate",
}

// SupportedCommands lists the MVP command names.
func SupportedCommands() []string {
	out := append([]string(nil), canonicalCommands...)
	sort.Strings(out)
	return out
}

// Execute runs one high-level command against the resolved extension
// connection. params are the command's flat parameter object.
func (b *Bridge) Execute(ctx context.Context, profile, typ string, params map[string]any) (any, error) {
	if typ == "" {
		return nil, badRequest("missing command type")
	}
	run, ok := commandTable[typ]
	if !ok {
		run, ok = bridgeCommandTable[typ]
	}
	if !ok {
		return nil, newError(CodeUnknownCommand,
			fmt.Sprintf("unknown command %q (supported: %v)", typ, SupportedCommands()), false)
	}
	if params == nil {
		params = map[string]any{}
	}
	tabID, hasTab, _ := paramTabIDOptional(params)
	if hasTab && tabID > 0 {
		b.touchTab(tabID)
		if owner := b.tabOwnerOf(tabID); owner != "" {
			finish := b.beginContextCommand(owner)
			defer finish()
		}
	}
	start := time.Now()
	result, err := run(ctx, b, profile, params)
	telemetryProfile := profile
	if hasTab && tabID > 0 {
		if owner := b.tabOwnerOf(tabID); owner != "" {
			telemetryProfile = owner
		}
	}
	b.telemetry.ToolCall(ctx, telemetryProfile, typ, params, result, err, time.Since(start))
	if hasTab && tabID > 0 && err == nil {
		if verb, target, ok := actionForCommand(typ, params); ok {
			b.recordAction(tabID, verb, target, !resultFailed(result), 0)
		}
	}
	return result, err
}

func rawResult(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, newError(CodeExtensionError, "extension returned invalid JSON: "+err.Error(), false)
	}
	if v == nil {
		return map[string]any{}, nil
	}
	return v, nil
}

func resultMap(raw json.RawMessage) (map[string]any, error) {
	v, err := rawResult(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, newError(CodeExtensionError, "extension returned a non-object result", false)
	}
	return m, nil
}

func paramTabID(params map[string]any) (int, error) {
	for _, key := range []string{"tab_id", "tabId"} {
		v, ok := params[key]
		if !ok || v == nil {
			continue
		}
		n, ok := toInt(v)
		if !ok || n <= 0 {
			return 0, badRequest("'%s' must be a positive integer", key)
		}
		return n, nil
	}
	return 0, badRequest("missing required integer parameter 'tab_id'")
}

func paramTabIDOptional(params map[string]any) (int, bool, error) {
	for _, key := range []string{"tab_id", "tabId"} {
		v, ok := params[key]
		if !ok || v == nil {
			continue
		}
		n, ok := toInt(v)
		if !ok || n <= 0 {
			return 0, true, badRequest("'%s' must be a positive integer", key)
		}
		return n, true, nil
	}
	return 0, false, nil
}

func paramStringAny(params map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if s, ok := paramString(params, key); ok {
			return s, true
		}
	}
	return "", false
}

func paramIntAny(params map[string]any, def int, keys ...string) int {
	for _, key := range keys {
		if _, ok := params[key]; ok {
			return paramInt(params, key, def)
		}
	}
	return def
}

func paramBoolAny(params map[string]any, def bool, keys ...string) bool {
	for _, key := range keys {
		if _, ok := params[key]; ok {
			return paramBool(params, key, def)
		}
	}
	return def
}

// routeProfile selects the Chrome extension label. Upstream tool schemas use
// `browser_profile` for that; the HTTP envelope's top-level `profile` is kept
// as a convenience fallback.
func routeProfile(envelopeProfile string, params map[string]any) string {
	if bp, ok := paramString(params, "browser_profile"); ok && bp != "" {
		return bp
	}
	return envelopeProfile
}

// toolTabID maps the upstream `tab_id` parameter (and the legacy `tabId`
// alias) into the internal command parameter name.
func toolTabID(params map[string]any, required bool) (map[string]any, int, bool, error) {
	out := make(map[string]any, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	id, ok, err := paramTabIDOptional(params)
	if err != nil {
		return nil, 0, false, err
	}
	if !ok && required {
		return nil, 0, false, badRequest("missing required integer parameter 'tab_id'")
	}
	if ok {
		out["tabId"] = id
	}
	return out, id, ok, nil
}

func paramString(params map[string]any, key string) (string, bool) {
	v, ok := params[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func paramInt(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	n, ok := toInt(v)
	if !ok {
		return def
	}
	return n
}

func paramFloat(params map[string]any, key string, def float64) float64 {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return def
}

func paramBool(params map[string]any, key string, def bool) bool {
	v, ok := params[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	case json.Number:
		i, err := strconv.Atoi(n.String())
		return i, err == nil
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	}
	return 0, false
}

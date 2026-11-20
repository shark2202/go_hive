package browserbridge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// commands_read.go ports the read-only inspection tools: browser_get_text,
// browser_html, browser_console and browser_shadow_query.

// cmdBrowserGetText ports bridge.get_text: element textContent, polled until it
// exists (30 s default upstream).
func cmdBrowserGetText(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	selector, _ := paramString(params, "selector")
	if strings.TrimSpace(selector) == "" {
		return nil, badRequest("missing required parameter 'selector'")
	}
	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(paramIntAny(params, 30000, "timeout_ms", "timeoutMs")) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	expr := fmt.Sprintf("(function(){ const el = document.querySelector(%s); return el ? el.textContent : null; })()", jsString(selector))
	start := time.Now()
	for {
		v, err := b.evalJS(ctx, conn, tabID, expr, 10*time.Second)
		if err != nil {
			return nil, err
		}
		if text, ok := v.(string); ok {
			return map[string]any{"ok": true, "selector": selector, "text": text}, nil
		}
		if time.Since(start) >= timeout {
			return map[string]any{"ok": false, "error": "Element not found: " + selector}, nil
		}
		select {
		case <-ctx.Done():
			return nil, newError(CodeTimeout, "get_text cancelled", false)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// cmdBrowserHTML ports tools/inspection.py browser_html: outerHTML of the page
// or of `selector`. The payload is spilled to an artifact file (upstream does
// the same to keep multi-KB HTML out of the model's context) and only a
// pointer is returned. A selector that matches nothing returns html=null.
func cmdBrowserHTML(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	selector, hasSelector := paramString(params, "selector")
	if hasSelector && strings.TrimSpace(selector) == "" {
		hasSelector = false
	}
	expr := "document.documentElement.outerHTML"
	if hasSelector {
		expr = fmt.Sprintf("(function(){ const el = document.querySelector(%s); return el ? el.outerHTML : null; })()", jsString(selector))
	}
	v, err := b.evalJS(ctx, conn, tabID, expr, 30*time.Second)
	if err != nil {
		return nil, err
	}
	html, isString := v.(string)
	if !isString {
		out := map[string]any{"ok": true, "tabId": tabID, "html": nil}
		if hasSelector {
			out["selector"] = selector
		}
		return out, nil
	}
	path, err := b.writeArtifact("browser_html", tabID, html, ".html")
	if err != nil {
		return map[string]any{"ok": false, "error": "Failed to write html artifact: " + err.Error()}, nil
	}
	out := map[string]any{
		"ok": true, "tabId": tabID, "length": len(html), "saved_to": path,
	}
	if hasSelector {
		out["selector"] = selector
	} else {
		out["selector"] = nil
	}
	return out, nil
}

// cmdBrowserConsole ports the upstream browser_console tool, which is a stub:
// the Python bridge does not capture console output either. Kept byte-identical
// so callers that branch on `message` behave the same.
func cmdBrowserConsole(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	return map[string]any{
		"ok":         true,
		"message":    "Console capture not yet implemented",
		"suggestion": "Use browser_evaluate to check specific values or errors",
	}, nil
}

// cmdBrowserShadowQuery ports bridge.shadow_query plus the tool wrapper: '>>>'
// pierces shadow roots and the rect is returned as viewport fractions so it can
// be fed straight back as a `coordinate`.
func cmdBrowserShadowQuery(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	selector, _ := paramString(params, "selector")
	if strings.TrimSpace(selector) == "" {
		return nil, badRequest("missing required parameter 'selector'")
	}
	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	rect, problem, err := b.shadowElementRect(ctx, conn, tabID, selector)
	if err != nil {
		return nil, err
	}
	if problem != "" {
		return map[string]any{"ok": false, "error": "Element not found: " + selector}, nil
	}
	cw, ch := b.viewportSize(ctx, conn, tabID)
	cwf, chf := float64(cw), float64(ch)
	if cwf <= 0 {
		cwf = 1
	}
	if chf <= 0 {
		chf = 1
	}
	return map[string]any{
		"ok":       true,
		"selector": selector,
		"tag":      rect.Tag,
		"rect": map[string]any{
			"x":  round4(rect.Left / cwf),
			"y":  round4(rect.Top / chf),
			"w":  round4(rect.Width / cwf),
			"h":  round4(rect.Height / chf),
			"cx": round4(rect.X / cwf),
			"cy": round4(rect.Y / chf),
		},
		"cssWidth":  cw,
		"cssHeight": ch,
		"note": "rect fields are fractions of the viewport (0..1). " +
			"Pass rect.cx / rect.cy as the coordinate of a browser_interact left_click / hover / key action.",
	}, nil
}

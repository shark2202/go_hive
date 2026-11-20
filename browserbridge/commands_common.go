package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// commands_common.go holds the CDP helpers shared by the L3 tool handlers.
// Everything here maps onto the same extension messages the 1.7.4 extension
// already implements ("cdp", "tab.*", "context.*"); no new wire strings.

// resolveTabTarget applies the upstream `tab_id=None` default: the calling
// profile's tracked active tab. It deliberately does NOT guess among several
// tabs, because acting on the wrong page is worse than a clear error.
//
// `profile` is the logical (agent/session) profile name, which is also the key
// of the context registry. When it is empty and exactly one context is
// registered, that context's active tab is used.
func (b *Bridge) resolveTabTarget(profile string, params map[string]any) (int, error) {
	if id, ok, err := paramTabIDOptional(params); err != nil {
		return 0, err
	} else if ok {
		return id, nil
	}
	if id := b.activeTabForProfile(profile); id > 0 {
		return id, nil
	}
	metas := b.contexts.List()
	if len(metas) == 1 {
		if id := b.activeTabForProfile(metas[0].Profile); id > 0 {
			return id, nil
		}
	}
	return 0, newError(CodeInvalidState,
		"no active tab is known for this profile — pass tab_id (call browser_open or browser_tabs first)", false)
}

func (b *Bridge) activeTabForProfile(profile string) int {
	profile = strings.TrimSpace(profile)
	if profile == "" || profile == "default" {
		return 0
	}
	meta, ok := b.contexts.Get(profile)
	if !ok || meta.GroupID <= 0 {
		return 0
	}
	return b.contextActiveTabFor(profile)
}

// viewportSize returns the live CSS viewport, matching upstream
// `_ensure_viewport_size` including its (1,1) identity fallback.
func (b *Bridge) viewportSize(ctx context.Context, conn *Connection, tabID int) (int, int) {
	v, err := b.evalJS(ctx, conn, tabID, "({w: window.innerWidth, h: window.innerHeight})", 10*time.Second)
	if err != nil {
		return 1, 1
	}
	m, ok := v.(map[string]any)
	if !ok {
		return 1, 1
	}
	w := int(paramFloat(m, "w", 0))
	h := int(paramFloat(m, "h", 0))
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	return w, h
}

// shadowQueryJS is copied from bridge.py `_SHADOW_QUERY_JS`: '>>>' pierces
// shadow roots, one hop per segment.
const shadowQueryJS = `
function _shadowQuery(sel) {
    const parts = sel.split('>>>').map(s => s.trim());
    let node = document;
    for (const part of parts) {
        if (!node) return null;
        node = (node.shadowRoot || node).querySelector(part);
    }
    return node;
}
`

// shadowElementRect scrolls the element into view and reports its CSS-px box.
// Returns ok=false when the selector matches nothing or has zero size.
func (b *Bridge) shadowElementRect(ctx context.Context, conn *Connection, tabID int, selector string) (elementRect, string, error) {
	expr := fmt.Sprintf(`(function(){
%s
const el = _shadowQuery(%s);
if (!el) return {error: "not_found"};
el.scrollIntoView({block: "center"});
const r = el.getBoundingClientRect();
if (r.width === 0 || r.height === 0) return {error: "zero_dimensions"};
return {left: r.left, top: r.top, x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width, h: r.height, tag: el.tagName};
})()`, shadowQueryJS, jsString(selector))

	v, err := b.evalJS(ctx, conn, tabID, expr, 10*time.Second)
	if err != nil {
		return elementRect{}, "", err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return elementRect{}, "not_found", nil
	}
	if msg, ok := m["error"].(string); ok {
		return elementRect{}, msg, nil
	}
	return elementRect{
		Left:   paramFloat(m, "left", 0),
		Top:    paramFloat(m, "top", 0),
		X:      paramFloat(m, "x", 0),
		Y:      paramFloat(m, "y", 0),
		Width:  paramFloat(m, "w", 0),
		Height: paramFloat(m, "h", 0),
		Tag:    stringOf(m["tag"]),
	}, "", nil
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// pollShadowElementRect waits for a shadow-piercing selector to be laid out.
func (b *Bridge) pollShadowElementRect(ctx context.Context, conn *Connection, tabID int, selector string, timeout time.Duration) (elementRect, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	start := time.Now()
	last := ""
	for {
		rect, problem, err := b.shadowElementRect(ctx, conn, tabID, selector)
		if err != nil {
			return elementRect{}, err
		}
		if problem == "" {
			return rect, nil
		}
		last = problem
		if time.Since(start) >= timeout {
			if last == "zero_dimensions" {
				return elementRect{}, newError(CodeInvalidState,
					fmt.Sprintf("element %q has zero dimensions", selector), false)
			}
			return elementRect{}, newError(CodeNotFound, "element not found: "+selector, false)
		}
		select {
		case <-ctx.Done():
			return elementRect{}, newError(CodeTimeout, "element lookup cancelled", false)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// hoverElement moves the pointer to the element's centre.
func (b *Bridge) hoverElement(ctx context.Context, conn *Connection, tabID int, selector string, timeout time.Duration) (map[string]any, error) {
	rect, err := b.pollShadowElementRect(ctx, conn, tabID, selector, timeout)
	if err != nil {
		return nil, err
	}
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": rect.X, "y": rect.Y, "button": "none",
	}, 10*time.Second); err != nil {
		return nil, err
	}
	b.setTabHighlight(tabID, map[string]any{
		"x": rect.Left, "y": rect.Top, "w": rect.Width, "h": rect.Height,
		"label": selector, "kind": "rect",
	})
	return map[string]any{
		"ok": true, "action": "hover", "tabId": tabID, "selector": selector,
		"x": rect.X, "y": rect.Y,
	}, nil
}

// hoverPoint moves the pointer to an absolute CSS-px point.
func (b *Bridge) hoverPoint(ctx context.Context, conn *Connection, tabID int, x, y float64) (map[string]any, error) {
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": x, "y": y, "buttons": 0,
	}, 10*time.Second); err != nil {
		return nil, err
	}
	b.setTabHighlight(tabID, map[string]any{
		"x": x, "y": y, "w": 0.0, "h": 0.0,
		"label": "hover (" + pyFloat(x) + "," + pyFloat(y) + ")", "kind": "point",
	})
	return map[string]any{
		"ok": true, "action": "hover_coordinate", "tabId": tabID, "x": x, "y": y,
	}, nil
}

// pressKeyAt moves the pointer first so the browser's native hit-testing
// routes the key, mirroring bridge.press_key_at.
func (b *Bridge) pressKeyAt(ctx context.Context, conn *Connection, tabID int, x, y float64, key string) (map[string]any, error) {
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": x, "y": y, "buttons": 0,
	}, 10*time.Second); err != nil {
		return nil, err
	}
	def := keyDefinition(key)
	down := map[string]any{
		"type":                  "keyDown",
		"key":                   def.key,
		"code":                  def.code,
		"windowsVirtualKeyCode": def.vk,
		"nativeVirtualKeyCode":  def.vk,
	}
	if def.text == "" {
		down["type"] = "rawKeyDown"
	} else {
		down["text"] = def.text
		down["unmodifiedText"] = def.text
	}
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchKeyEvent", down, 10*time.Second); err != nil {
		return nil, err
	}
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": def.key, "code": def.code,
		"windowsVirtualKeyCode": def.vk, "nativeVirtualKeyCode": def.vk,
	}, 10*time.Second); err != nil {
		return nil, err
	}
	b.setTabHighlight(tabID, map[string]any{
		"x": x, "y": y, "w": 0.0, "h": 0.0,
		"label": def.key + " (" + pyFloat(x) + "," + pyFloat(y) + ")", "kind": "point",
	})
	return map[string]any{
		"ok": true, "action": "press_at", "tabId": tabID, "key": def.key, "x": x, "y": y,
	}, nil
}

// selectOptions ports bridge.select_option: set selected on every option whose
// value is listed, dispatch `change`, and report what actually stuck.
func (b *Bridge) selectOptions(ctx context.Context, conn *Connection, tabID int, selector string, values []string) (map[string]any, error) {
	valuesJSON, err := jsonStringSlice(values)
	if err != nil {
		return nil, err
	}
	expr := fmt.Sprintf(`(function(){
const sel = document.querySelector(%s);
if (!sel) return {found: false};
const options = Array.from(sel.options).map(function(o){return o.value;});
Array.from(sel.options).forEach(function(opt){opt.selected = %s.includes(opt.value);});
sel.dispatchEvent(new Event('change', {bubbles: true}));
return {found: true, options: options, selected: Array.from(sel.selectedOptions).map(function(o){return o.value;})};
})()`, jsString(selector), valuesJSON)

	v, err := b.evalJS(ctx, conn, tabID, expr, 15*time.Second)
	if err != nil {
		return nil, err
	}
	payload, _ := v.(map[string]any)
	if found, _ := payload["found"].(bool); !found {
		return map[string]any{
			"ok": false, "action": "select", "selector": selector,
			"error": "Element not found: " + selector,
		}, nil
	}
	options := stringSliceOf(payload["options"])
	selected := stringSliceOf(payload["selected"])
	var invalid []string
	for _, want := range values {
		if !containsString(options, want) {
			invalid = append(invalid, want)
		}
	}
	if len(invalid) > 0 {
		return map[string]any{
			"ok":       false,
			"action":   "select",
			"selector": selector,
			"error":    fmt.Sprintf("value(s) %v not in this select's options: %v", invalid, options),
			"selected": selected,
			"options":  options,
		}, nil
	}
	return map[string]any{"ok": true, "action": "select", "selector": selector, "selected": selected}, nil
}

// waitForSelector ports bridge.wait_for_selector (5 s default upstream).
func (b *Bridge) waitForSelector(ctx context.Context, conn *Connection, tabID int, selector string, timeout time.Duration) (bool, error) {
	expr := fmt.Sprintf("(function(){ return document.querySelector(%s) !== null; })()", jsString(selector))
	return b.pollBool(ctx, conn, tabID, expr, timeout)
}

// waitForText ports bridge.wait_for_text over document.body.innerText.
func (b *Bridge) waitForText(ctx context.Context, conn *Connection, tabID int, text string, timeout time.Duration) (bool, error) {
	expr := fmt.Sprintf("(function(){ return (document.body ? document.body.innerText : '').includes(%s); })()", jsString(text))
	return b.pollBool(ctx, conn, tabID, expr, timeout)
}

func (b *Bridge) pollBool(ctx context.Context, conn *Connection, tabID int, expr string, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	start := time.Now()
	for {
		v, err := b.evalJS(ctx, conn, tabID, expr, 10*time.Second)
		if err != nil {
			return false, err
		}
		if found, _ := v.(bool); found {
			return true, nil
		}
		if time.Since(start) >= timeout {
			return false, nil
		}
		sleep := 50 * time.Millisecond
		if elapsed := time.Since(start); elapsed > time.Second {
			sleep = 200 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return false, newError(CodeTimeout, "wait cancelled", false)
		case <-time.After(sleep):
		}
	}
}

// captureRegion captures a viewport-fraction rectangle at `scale`. Ported from
// bridge.screenshot_region's clip maths; the Go path returns raw base64 rather
// than resizing (same divergence as browser_screenshot).
func (b *Bridge) captureRegion(ctx context.Context, conn *Connection, tabID int, x0, y0, x1, y1, scale float64) (map[string]any, error) {
	cw, ch := b.viewportSize(ctx, conn, tabID)
	if scale <= 0 {
		scale = 1
	}
	cx0, cx1 := math.Min(math.Max(0, x0), 1), math.Min(math.Max(0, x1), 1)
	cy0, cy1 := math.Min(math.Max(0, y0), 1), math.Min(math.Max(0, y1), 1)
	if cx0 > cx1 {
		cx0, cx1 = cx1, cx0
	}
	if cy0 > cy1 {
		cy0, cy1 = cy1, cy0
	}
	w := math.Max(1, (cx1-cx0)*float64(cw))
	h := math.Max(1, (cy1-cy0)*float64(ch))
	if _, err := b.cdp(ctx, conn, tabID, "Page.enable", nil, 10*time.Second); err != nil {
		return nil, err
	}
	res, err := b.cdp(ctx, conn, tabID, "Page.captureScreenshot", map[string]any{
		"format":                "jpeg",
		"quality":               85,
		"captureBeyondViewport": true,
		"clip": map[string]any{
			"x": cx0 * float64(cw), "y": cy0 * float64(ch),
			"width": w, "height": h, "scale": scale,
		},
	}, 60*time.Second)
	if err != nil {
		return nil, err
	}
	data, _ := res["data"].(string)
	if data == "" {
		return nil, newError(CodeCDPError, "Page.captureScreenshot returned no image data", false)
	}
	url, _ := b.evalJS(ctx, conn, tabID, "window.location.href", 10*time.Second)
	return map[string]any{
		"ok": true, "tabId": tabID, "data": data, "mimeType": "image/jpeg",
		"imageType": "jpeg", "url": url,
		"regionCssWidth": int(w), "regionCssHeight": int(h),
		"captureScale": scale,
		"crop_box":     []any{round4(cx0), round4(cy0), round4(cx1), round4(cy1)},
	}, nil
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func jsonStringSlice(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", badRequest("'values' must be a list of strings")
	}
	return string(data), nil
}

func stringSliceOf(v any) []string {
	rows, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if s, ok := row.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

func cmdNavigate(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	url, _ := paramString(params, "url")
	if strings.TrimSpace(url) == "" {
		return nil, badRequest("missing required parameter 'url'")
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	waitUntil, _ := paramStringAny(params, "wait_until", "waitUntil")
	if waitUntil == "" {
		waitUntil = "load"
	}
	switch waitUntil {
	case "commit", "domcontentloaded", "load", "networkidle":
	default:
		return nil, badRequest("'waitUntil' must be commit, domcontentloaded, load or networkidle")
	}
	timeoutMS := paramIntAny(params, 30000, "timeout_ms", "timeoutMs")
	if timeoutMS < 500 {
		timeoutMS = 500
	}
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)

	// Page.enable is best-effort; some chrome:// pages reject domains.
	_, _ = b.cdp(ctx, conn, tabID, "Page.enable", nil, 10*time.Second)
	nav, err := b.cdp(ctx, conn, tabID, "Page.navigate", map[string]any{"url": url}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	if errText, ok := nav["errorText"].(string); ok && errText != "" {
		return map[string]any{"ok": false, "tabId": tabID, "error": errText, "url": url}, nil
	}
	if loader, ok := nav["loaderId"].(string); !ok || loader == "" {
		// No loader id: still let readyState polling decide, matching upstream.
	}

	ready := ""
	if waitUntil != "commit" {
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, newError(CodeTimeout, "navigation cancelled", false)
			default:
			}
			v, err := b.evalJS(ctx, conn, tabID, "document.readyState", 10*time.Second)
			if err != nil {
				return nil, err
			}
			ready, _ = v.(string)
			done := false
			switch waitUntil {
			case "domcontentloaded":
				done = ready == "interactive" || ready == "complete"
			case "load":
				done = ready == "complete"
			case "networkidle":
				done = ready == "complete"
			}
			if done {
				if waitUntil == "networkidle" {
					select {
					case <-ctx.Done():
						return nil, newError(CodeTimeout, "navigation cancelled", false)
					case <-time.After(500 * time.Millisecond):
					}
				}
				break
			}
			select {
			case <-ctx.Done():
				return nil, newError(CodeTimeout, "navigation cancelled", false)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	resolved, _ := b.evalJS(ctx, conn, tabID, "window.location.href", 10*time.Second)
	title, _ := b.evalJS(ctx, conn, tabID, "document.title", 10*time.Second)
	resolvedURL, _ := resolved.(string)
	if strings.HasPrefix(resolvedURL, "chrome-error://") {
		return map[string]any{
			"ok": false, "tabId": tabID, "error": "navigation_failed", "url": url,
			"resolved_url": resolvedURL,
		}, nil
	}
	return map[string]any{
		"ok":         true,
		"tabId":      tabID,
		"url":        resolvedURL,
		"title":      title,
		"readyState": ready,
		"waitUntil":  waitUntil,
	}, nil
}

func cmdClick(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	button, err := normalizeButton(mustString(params, "button"))
	if err != nil {
		return nil, err
	}
	clickCount := paramIntAny(params, 1, "click_count", "clickCount")
	if clickCount < 1 {
		clickCount = 1
	}
	selector, hasSelector := paramString(params, "selector")
	x, hasX := params["x"]
	y, hasY := params["y"]

	var px, py float64
	if hasSelector {
		timeout := time.Duration(paramIntAny(params, 5000, "timeout_ms", "timeoutMs")) * time.Millisecond
		rect, err := b.pollElementRect(ctx, conn, tabID, selector, timeout)
		if err != nil {
			return nil, err
		}
		px, py = rect.X, rect.Y
	} else if hasX && hasY {
		xf, ok1 := toFloat(x)
		yf, ok2 := toFloat(y)
		if !ok1 || !ok2 {
			return nil, badRequest("'x' and 'y' must be numbers")
		}
		px, py = xf, yf
	} else {
		return nil, badRequest("provide 'selector', or both 'x' and 'y'")
	}
	if err := b.mouseClick(ctx, conn, tabID, px, py, button, clickCount); err != nil {
		return nil, err
	}
	out := map[string]any{
		"ok": true, "action": "click", "tabId": tabID, "x": px, "y": py,
		"button": button, "clickCount": clickCount, "method": "cdp",
	}
	if hasSelector {
		out["selector"] = selector
	}
	return out, nil
}

func cmdType(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	text, ok := paramString(params, "text")
	if !ok {
		return nil, badRequest("missing required string parameter 'text'")
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	selector, hasSelector := paramString(params, "selector")
	clearFirst := paramBoolAny(params, true, "clear_first", "clearFirst")
	delayMS := paramIntAny(params, 1, "delay_ms", "delayMs")
	useInsertText := paramBoolAny(params, true, "use_insert_text", "useInsertText")

	if hasSelector {
		timeout := time.Duration(paramIntAny(params, 5000, "timeout_ms", "timeoutMs")) * time.Millisecond
		rect, err := b.pollElementRectForTyping(ctx, conn, tabID, selector, clearFirst, timeout)
		if err != nil {
			return nil, err
		}
		if err := b.mouseClick(ctx, conn, tabID, rect.X, rect.Y, "left", 1); err != nil {
			return nil, err
		}
	}
	chars := utf8.RuneCountInString(text)
	if useInsertText {
		if text != "" {
			if _, err := b.cdp(ctx, conn, tabID, "Input.insertText", map[string]any{"text": text}, 15*time.Second); err != nil {
				return nil, err
			}
		}
	} else if text != "" {
		for _, r := range text {
			if _, err := b.cdp(ctx, conn, tabID, "Input.insertText", map[string]any{"text": string(r)}, 10*time.Second); err != nil {
				return nil, err
			}
			select {
			case <-ctx.Done():
				return nil, newError(CodeTimeout, "typing cancelled", false)
			case <-time.After(time.Duration(delayMS) * time.Millisecond):
			}
		}
	}
	out := map[string]any{"ok": true, "action": "type", "tabId": tabID, "chars": chars, "length": chars}
	if hasSelector {
		out["selector"] = selector
	}
	return out, nil
}

func (b *Bridge) pollElementRectForTyping(ctx context.Context, conn *Connection, tabID int, selector string, clearFirst bool, timeout time.Duration) (elementRect, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	clear := "false"
	if clearFirst {
		clear = `if (el.value !== undefined) { el.value = ""; el.dispatchEvent(new Event("input", {bubbles: true})); } else if (el.isContentEditable) { el.textContent = ""; el.dispatchEvent(new Event("input", {bubbles: true})); }`
	}
	expr := fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return {error: "not_found"};
  el.scrollIntoView({block: "center"});
  %s
  const r = el.getBoundingClientRect();
  if (r.width === 0 || r.height === 0) return {error: "zero_dimensions"};
  return {left: r.left, top: r.top, x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width, h: r.height};
})()`, jsString(selector), clear)

	start := time.Now()
	last := ""
	for {
		v, err := b.evalJS(ctx, conn, tabID, expr, 10*time.Second)
		if err != nil {
			return elementRect{}, err
		}
		if m, ok := v.(map[string]any); ok {
			if msg, ok := m["error"].(string); ok {
				last = msg
			} else {
				return elementRect{
					Left:   paramFloat(m, "left", 0),
					Top:    paramFloat(m, "top", 0),
					X:      paramFloat(m, "x", 0),
					Y:      paramFloat(m, "y", 0),
					Width:  paramFloat(m, "w", 0),
					Height: paramFloat(m, "h", 0),
				}, nil
			}
		} else {
			last = "not_found"
		}
		if time.Since(start) >= timeout {
			if last == "zero_dimensions" {
				return elementRect{}, newError(CodeInvalidState,
					fmt.Sprintf("element %q has zero dimensions and cannot be focused", selector), false)
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

type keyDef struct {
	text string
	key  string
	code string
	vk   int
}

func cmdKeypress(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	key, _ := paramString(params, "key")
	if key == "" {
		return nil, badRequest("missing required parameter 'key'")
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	mods, err := parseModifiers(params["modifiers"])
	if err != nil {
		return nil, err
	}
	if selector, ok := paramString(params, "selector"); ok {
		expr := fmt.Sprintf(`(() => { const el = document.querySelector(%s); if (!el) return false; el.focus(); return true; })()`, jsString(selector))
		v, err := b.evalJS(ctx, conn, tabID, expr, 10*time.Second)
		if err != nil {
			return nil, err
		}
		if focused, _ := v.(bool); !focused {
			return nil, newError(CodeNotFound, "element not found: "+selector, false)
		}
	}

	def := keyDefinition(key)
	mask, modNames := modifierMask(mods)
	if mask&(1|2|4) != 0 {
		def.text = "" // Ctrl+A must not also type "a"
	}
	down := map[string]any{
		"type":                  "keyDown",
		"key":                   def.key,
		"code":                  def.code,
		"windowsVirtualKeyCode": def.vk,
		"nativeVirtualKeyCode":  def.vk,
		"modifiers":             mask,
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
		"type":                  "keyUp",
		"key":                   def.key,
		"code":                  def.code,
		"windowsVirtualKeyCode": def.vk,
		"nativeVirtualKeyCode":  def.vk,
		"modifiers":             mask,
	}, 10*time.Second); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "action": "press", "tabId": tabID, "key": def.key, "modifiers": modNames,
	}, nil
}

func keyDefinition(key string) keyDef {
	special := map[string]keyDef{
		"Enter":      {"\r", "Enter", "Enter", 13},
		"Tab":        {"\t", "Tab", "Tab", 9},
		"Escape":     {"\x1b", "Escape", "Escape", 27},
		"Backspace":  {"\b", "Backspace", "Backspace", 8},
		"Delete":     {"\x7f", "Delete", "Delete", 46},
		"ArrowUp":    {"", "ArrowUp", "ArrowUp", 38},
		"ArrowDown":  {"", "ArrowDown", "ArrowDown", 40},
		"ArrowLeft":  {"", "ArrowLeft", "ArrowLeft", 37},
		"ArrowRight": {"", "ArrowRight", "ArrowRight", 39},
		"Home":       {"", "Home", "Home", 36},
		"End":        {"", "End", "End", 35},
		"PageUp":     {"", "PageUp", "PageUp", 33},
		"PageDown":   {"", "PageDown", "PageDown", 34},
		"Space":      {" ", " ", "Space", 32},
	}
	if d, ok := special[key]; ok {
		return d
	}
	runes := []rune(key)
	if len(runes) == 1 {
		r := runes[0]
		switch {
		case r >= 'a' && r <= 'z':
			return keyDef{key, key, "Key" + strings.ToUpper(key), int(r - 'a' + 'a')}
		case r >= 'A' && r <= 'Z':
			return keyDef{key, key, "Key" + key, int(r)}
		case r >= '0' && r <= '9':
			return keyDef{key, key, "Digit" + key, int(r)}
		}
		return keyDef{key, key, "", 0}
	}
	return keyDef{key, key, "", 0}
}

func parseModifiers(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	var out []string
	switch m := v.(type) {
	case []any:
		for _, item := range m {
			s, ok := item.(string)
			if !ok {
				return nil, badRequest("'modifiers' must be an array of strings")
			}
			out = append(out, s)
		}
	case []string:
		out = append(out, m...)
	case string:
		for _, part := range strings.Split(m, "+") {
			if strings.TrimSpace(part) != "" {
				out = append(out, strings.TrimSpace(part))
			}
		}
	default:
		return nil, badRequest("'modifiers' must be an array of strings")
	}
	for _, m := range out {
		switch strings.ToLower(m) {
		case "alt", "ctrl", "control", "meta", "cmd", "command", "shift":
		default:
			return nil, badRequest("unsupported modifier %q (use alt, ctrl, meta or shift)", m)
		}
	}
	return out, nil
}

func modifierMask(mods []string) (int, []string) {
	mask := 0
	names := make([]string, 0, len(mods))
	for _, m := range mods {
		switch strings.ToLower(m) {
		case "alt":
			mask |= 1
			names = append(names, "alt")
		case "ctrl", "control":
			mask |= 2
			names = append(names, "ctrl")
		case "meta", "cmd", "command":
			mask |= 4
			names = append(names, "meta")
		case "shift":
			mask |= 8
			names = append(names, "shift")
		}
	}
	return mask, names
}

func cmdScroll(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	direction, _ := paramString(params, "direction")
	if direction == "" {
		direction = "down"
	}
	amount := paramFloat(params, "amount", 500)
	if amount <= 0 {
		amount = 500
	}
	var dx, dy float64
	switch direction {
	case "down":
		dy = amount
	case "up":
		dy = -amount
	case "right":
		dx = amount
	case "left":
		dx = -amount
	default:
		return nil, badRequest("'direction' must be up, down, left or right")
	}

	x, y := 0.0, 0.0
	if selector, ok := paramString(params, "selector"); ok {
		rect, err := b.pollElementRect(ctx, conn, tabID, selector, 5*time.Second)
		if err != nil {
			return nil, err
		}
		x, y = rect.X, rect.Y
	} else {
		v, err := b.evalJS(ctx, conn, tabID,
			"(() => ({x: window.innerWidth / 2, y: window.innerHeight / 2}))()", 10*time.Second)
		if err != nil {
			return nil, err
		}
		if m, ok := v.(map[string]any); ok {
			x = paramFloat(m, "x", 0)
			y = paramFloat(m, "y", 0)
		}
	}
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": x, "y": y, "deltaX": dx, "deltaY": dy, "pointerType": "mouse",
	}, 15*time.Second); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "action": "scroll", "tabId": tabID, "direction": direction, "amount": amount,
		"deltaX": dx, "deltaY": dy, "method": "cdp",
	}, nil
}

type axNode struct {
	NodeID         string   `json:"nodeId"`
	Ignored        bool     `json:"ignored"`
	Role           axVal    `json:"role"`
	Name           axVal    `json:"name"`
	ChildIDs       []string `json:"childIds"`
	Properties     []axProp `json:"properties"`
	IgnoredReasons []struct {
		Name string `json:"name"`
	} `json:"ignoredReasons"`
}

type axVal struct {
	Value any `json:"value"`
}

type axProp struct {
	Name  string `json:"name"`
	Value axVal  `json:"value"`
}

var interactiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "checkbox": true, "radio": true,
	"combobox": true, "listbox": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "option": true, "searchbox": true, "slider": true,
	"spinbutton": true, "switch": true, "tab": true, "treeitem": true,
}

var structuralRoles = map[string]bool{
	"generic": true, "group": true, "list": true, "listitem": true, "none": true,
	"paragraph": true, "section": true, "StaticText": true, "InlineTextBox": true,
}

func cmdSnapshot(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	_, _ = b.cdp(ctx, conn, tabID, "Accessibility.enable", nil, 10*time.Second)
	res, err := b.cdp(ctx, conn, tabID, "Accessibility.getFullAXTree", nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	mode, _ := paramString(params, "mode")
	if mode == "" {
		mode = "default"
	}
	maxNodes := paramInt(params, "maxNodes", 2000)
	if maxNodes <= 0 {
		maxNodes = 2000
	}
	rawNodes, _ := res["nodes"].([]any)
	nodes := make([]axNode, 0, len(rawNodes))
	for _, raw := range rawNodes {
		blob, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var n axNode
		if json.Unmarshal(blob, &n) == nil {
			nodes = append(nodes, n)
		}
	}
	tree, count := formatAXTree(nodes, mode, maxNodes)
	url, _ := b.evalJS(ctx, conn, tabID, "window.location.href", 10*time.Second)
	return map[string]any{
		"ok": true, "tabId": tabID, "url": url, "tree": tree, "nodes": count,
	}, nil
}

func formatAXTree(nodes []axNode, mode string, maxNodes int) (string, int) {
	if len(nodes) == 0 {
		return "(empty tree)", 0
	}
	byID := make(map[string]axNode, len(nodes))
	children := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
		for _, c := range n.ChildIDs {
			children[n.NodeID] = append(children[n.NodeID], c)
		}
	}
	var lines []string
	count := 0
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if count >= maxNodes {
			return
		}
		n, ok := byID[id]
		if !ok {
			return
		}
		role, _ := n.Role.Value.(string)
		name, _ := n.Name.Value.(string)
		skip := false
		if n.Ignored {
			hardHide := false
			for _, r := range n.IgnoredReasons {
				switch r.Name {
				case "notRendered", "displayNone", "inert", "notVisible":
					hardHide = true
				}
			}
			if hardHide || !interactiveRoles[role] || name == "" {
				skip = true
			}
		}
		if role == "none" || role == "Ignored" {
			skip = true
		}
		if mode == "interactive" && !interactiveRoles[role] {
			skip = true
		}
		if mode == "simple" && structuralRoles[role] && name == "" {
			skip = true
		}
		if !skip {
			count++
			label := strings.Repeat("  ", depth) + "- " + role
			if name != "" {
				label += fmt.Sprintf(" %q", name)
			}
			var props []string
			for _, p := range n.Properties {
				v, _ := p.Value.Value.(bool)
				switch p.Name {
				case "focused", "disabled", "checked", "expanded", "selected", "required":
					if v {
						props = append(props, p.Name)
					}
				case "level":
					if p.Value.Value != nil {
						props = append(props, fmt.Sprintf("level=%v", p.Value.Value))
					}
				}
			}
			if len(props) > 0 {
				label += " [" + strings.Join(props, ", ") + "]"
			}
			lines = append(lines, label)
			depth++
		}
		for _, c := range children[id] {
			walk(c, depth)
		}
	}
	walk(nodes[0].NodeID, 0)
	if count >= maxNodes {
		lines = append(lines, "... (tree truncated, too many nodes)")
	}
	if len(lines) == 0 {
		return "(empty tree)", 0
	}
	return strings.Join(lines, "\n"), count
}

func cmdScreenshot(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	format, _ := paramString(params, "format")
	if format == "" {
		format = "png"
	}
	if format != "png" && format != "jpeg" {
		return nil, badRequest("'format' must be png or jpeg")
	}
	_, _ = b.cdp(ctx, conn, tabID, "Page.enable", nil, 10*time.Second)
	capture := map[string]any{"format": format}
	if q := paramInt(params, "quality", 0); q > 0 && format == "jpeg" {
		capture["quality"] = q
	}
	if selector, ok := paramString(params, "selector"); ok {
		rect, err := b.pollElementRect(ctx, conn, tabID, selector, 5*time.Second)
		if err != nil {
			return nil, err
		}
		w, h := math.Min(rect.Width, 16384), math.Min(rect.Height, 16384)
		capture["clip"] = map[string]any{"x": rect.Left, "y": rect.Top, "width": w, "height": h, "scale": 1}
	} else if paramBoolAny(params, false, "full_page", "fullPage") {
		metrics, err := b.cdp(ctx, conn, tabID, "Page.getLayoutMetrics", nil, 15*time.Second)
		if err != nil {
			return nil, err
		}
		cs, _ := metrics["contentSize"].(map[string]any)
		w := paramFloat(cs, "width", 1280)
		h := paramFloat(cs, "height", 720)
		if w <= 0 {
			w = 1280
		}
		if h <= 0 {
			h = 720
		}
		scale := math.Min(1, math.Min(16000/w, 16000/h))
		if w*h*scale*scale > 16_000_000 {
			scale = math.Sqrt(16_000_000 / (w * h))
		}
		if scale < 0.05 {
			scale = 0.05
		}
		capture["captureBeyondViewport"] = true
		capture["clip"] = map[string]any{"x": 0, "y": 0, "width": w, "height": h, "scale": scale}
	}
	res, err := b.cdp(ctx, conn, tabID, "Page.captureScreenshot", capture, 60*time.Second)
	if err != nil {
		return nil, err
	}
	data, _ := res["data"].(string)
	if data == "" {
		return nil, newError(CodeCDPError, "Page.captureScreenshot returned no image data", false)
	}
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
	}
	return map[string]any{
		"ok": true, "tabId": tabID, "data": data, "mimeType": mime, "format": format,
	}, nil
}

func cmdEval(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	script, ok := paramString(params, "script")
	if !ok {
		script, ok = paramString(params, "expression")
	}
	if !ok {
		return nil, badRequest("missing required string parameter 'script' (alias: 'expression')")
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	res, err := b.cdp(ctx, conn, tabID, "Runtime.evaluate", map[string]any{
		"expression":    normalizeScript(script),
		"returnByValue": true,
		"awaitPromise":  true,
	}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if ex, ok := res["exceptionDetails"]; ok && ex != nil {
		return map[string]any{"ok": false, "error": exceptionMessage(ex)}, nil
	}
	inner, _ := res["result"].(map[string]any)
	var value any
	if inner != nil {
		value = inner["value"]
	}
	return map[string]any{"ok": true, "action": "evaluate", "result": value}, nil
}

func normalizeScript(script string) string {
	s := strings.TrimSpace(script)
	if strings.HasPrefix(s, "(function") || strings.HasPrefix(s, "(()") || strings.HasPrefix(s, "async ()") {
		return s
	}
	if strings.HasPrefix(s, "return ") {
		return "(function() { " + s + " })()"
	}
	if strings.Contains(s, "\n") || strings.Contains(s, ";") {
		return "(function() { " + s + " })()"
	}
	return "(function() { return (" + s + ") })()"
}

func mustString(params map[string]any, key string) string {
	s, _ := paramString(params, key)
	return s
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

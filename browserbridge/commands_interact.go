package browserbridge

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// commands_interact.go ports tools/interact.py: the unified `browser_interact`
// action dispatcher plus `browser_select`.
//
// Coordinate semantics follow upstream: fractions of the viewport (0..1), never
// pixels. The active surface keeps upstream's parameter-relevance guard so a
// targeting parameter that the chosen action cannot honour is rejected loudly
// instead of silently acting on the wrong target.

const (
	autoSnapshotSettle = 500 * time.Millisecond
	zoomTargetWidth    = 800.0
	zoomMinScale       = 1.0
	zoomMaxScale       = 4.0
)

var interactClickActions = map[string]bool{
	"left_click": true, "right_click": true, "middle_click": true,
	"double_click": true, "triple_click": true,
}

var interactActions = map[string]bool{
	"left_click": true, "right_click": true, "middle_click": true,
	"double_click": true, "triple_click": true, "hover": true, "type": true,
	"key": true, "scroll": true, "drag": true, "screenshot": true,
	"zoom": true, "wait": true,
}

// interactParamActions mirrors upstream _PARAM_ACTIONS: which actions may
// consume each targeting parameter.
var interactParamActions = map[string]map[string]bool{
	"coordinate":       setOf("left_click", "right_click", "middle_click", "double_click", "triple_click", "hover", "key", "drag"),
	"selector":         setOf("left_click", "right_click", "middle_click", "double_click", "triple_click", "hover", "type", "key", "scroll", "drag", "screenshot"),
	"modifiers":        setOf("key"),
	"region":           setOf("zoom"),
	"start_selector":   setOf("drag"),
	"start_coordinate": setOf("drag"),
}

var interactParamHints = map[string]string{
	"coordinate|type":      " — left_click at the coordinate to focus, then call type with no coordinate",
	"selector|wait":        " — wait matches elements via wait_for_selector, not selector",
	"selector|zoom":        " — zoom captures a rectangle; pass region=[x0,y0,x1,y1]",
	"modifiers|left_click": "; click modifiers are not supported",
}

func setOf(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

func paramMisuseError(param, action string) string {
	allowed := make([]string, 0, len(interactParamActions[param]))
	for name := range interactParamActions[param] {
		allowed = append(allowed, name)
	}
	sortStrings(allowed)
	msg := fmt.Sprintf("`%s` is not used by action='%s' (valid for: %s)", param, action, strings.Join(allowed, ", "))
	if param == "modifiers" {
		msg += "; click modifiers are not supported"
	}
	return msg + interactParamHints[param+"|"+action] + "."
}

func cmdBrowserInteract(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	action := strings.ToLower(strings.TrimSpace(mustString(params, "action")))
	if action == "" {
		return nil, badRequest("missing required parameter 'action'")
	}
	if !interactActions[action] {
		return nil, badRequest("unknown interact action %q (valid: double_click, drag, hover, key, left_click, middle_click, right_click, screenshot, scroll, triple_click, type, wait, zoom)", action)
	}

	selector, hasSelector := paramString(params, "selector")
	if hasSelector && strings.TrimSpace(selector) == "" {
		hasSelector = false
	}
	startSelector, hasStartSelector := paramString(params, "start_selector")
	coordinate, hasCoordinate, err := paramFloatList(params, "coordinate")
	if err != nil {
		return nil, err
	}
	startCoordinate, hasStartCoordinate, err := paramFloatList(params, "start_coordinate")
	if err != nil {
		return nil, err
	}
	region, hasRegion, err := paramFloatList(params, "region")
	if err != nil {
		return nil, err
	}
	modifiers, hasModifiers := paramString(params, "modifiers")

	for _, present := range []struct {
		name  string
		value bool
	}{
		{"coordinate", hasCoordinate},
		{"selector", hasSelector},
		{"modifiers", hasModifiers && modifiers != ""},
		{"region", hasRegion},
		{"start_selector", hasStartSelector && startSelector != ""},
		{"start_coordinate", hasStartCoordinate},
	} {
		if present.value && !interactParamActions[present.name][action] {
			return map[string]any{"ok": false, "error": paramMisuseError(present.name, action)}, nil
		}
	}

	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	label := routeProfile(profile, params)
	conn, err := b.resolve(label, &tabID)
	if err != nil {
		return nil, err
	}
	timeoutMS := paramInt(params, "timeout_ms", 0)
	timeout := func(def int) time.Duration {
		ms := timeoutMS
		if ms <= 0 {
			ms = def
		}
		return time.Duration(ms) * time.Millisecond
	}
	snapshotMode := paramStringDefault(params, "auto_snapshot_mode", "simple")
	waitAfterMS := paramInt(params, "wait_after_ms", 0)

	switch {
	case interactClickActions[action]:
		button := "left"
		switch action {
		case "right_click":
			button = "right"
		case "middle_click":
			button = "middle"
		}
		clickCount := 1
		switch action {
		case "double_click":
			clickCount = 2
		case "triple_click":
			clickCount = 3
		}
		if hasSelector {
			res, err := cmdClick(ctx, b, label, map[string]any{
				"tabId": tabID, "selector": selector, "button": button,
				"clickCount": clickCount, "timeoutMs": int(timeout(5000) / time.Millisecond),
			})
			if err != nil {
				return nil, err
			}
			return attachInteractSnapshot(ctx, b, tabID, res, snapshotMode, waitAfterMS), nil
		}
		if hasCoordinate {
			if bad := badFraction(coordinate); bad != "" {
				return map[string]any{"ok": false, "error": bad}, nil
			}
			px, py, err := b.pointFromFraction(ctx, conn, tabID, coordinate)
			if err != nil {
				return nil, err
			}
			res, err := cmdClick(ctx, b, label, map[string]any{
				"tabId": tabID, "x": px, "y": py, "button": button, "clickCount": clickCount,
			})
			if err != nil {
				return nil, err
			}
			return res, nil
		}
		return map[string]any{"ok": false, "error": action + " requires a selector or coordinate"}, nil

	case action == "hover":
		if hasSelector {
			return b.hoverElement(ctx, conn, tabID, selector, timeout(30000))
		}
		if hasCoordinate {
			if bad := badFraction(coordinate); bad != "" {
				return map[string]any{"ok": false, "error": bad}, nil
			}
			px, py, err := b.pointFromFraction(ctx, conn, tabID, coordinate)
			if err != nil {
				return nil, err
			}
			return b.hoverPoint(ctx, conn, tabID, px, py)
		}
		return map[string]any{"ok": false, "error": "hover requires a selector or coordinate"}, nil

	case action == "type":
		text, ok := paramString(params, "text")
		if !ok {
			return map[string]any{"ok": false, "error": "type requires text"}, nil
		}
		internal := map[string]any{
			"tabId":         tabID,
			"text":          text,
			"clearFirst":    paramBoolAny(params, true, "clear_first", "clearFirst"),
			"useInsertText": paramBoolAny(params, true, "use_insert_text", "useInsertText"),
			"delayMs":       1,
			"timeoutMs":     int(timeout(30000) / time.Millisecond),
		}
		if hasSelector {
			internal["selector"] = selector
		}
		res, err := cmdType(ctx, b, label, internal)
		if err != nil {
			return nil, err
		}
		return attachInteractSnapshot(ctx, b, tabID, res, snapshotMode, waitAfterMS), nil

	case action == "key":
		text, _ := paramString(params, "text")
		if strings.TrimSpace(text) == "" {
			return map[string]any{"ok": false, "error": "key requires text (the key to press)"}, nil
		}
		key, mods := splitKeySpec(text)
		if modifiers != "" {
			for _, part := range strings.Split(modifiers, "+") {
				if part = strings.TrimSpace(part); part != "" {
					mods = append(mods, part)
				}
			}
		}
		if key == "" {
			return map[string]any{"ok": false, "error": fmt.Sprintf("key text '%s' resolved to an empty key — give a key name like 'Enter' or 'ctrl+a'.", text)}, nil
		}
		repeat := paramInt(params, "repeat", 1)
		if repeat < 1 {
			repeat = 1
		}
		if repeat > 100 {
			repeat = 100
		}
		if hasCoordinate {
			if len(mods) > 0 {
				return map[string]any{"ok": false, "error": fmt.Sprintf("Modifier keys (%s) are not supported with a coordinate target. Use a selector instead, or focus the element first and call key with no coordinate.", strings.Join(mods, "+"))}, nil
			}
			if bad := badFraction(coordinate); bad != "" {
				return map[string]any{"ok": false, "error": bad}, nil
			}
			px, py, err := b.pointFromFraction(ctx, conn, tabID, coordinate)
			if err != nil {
				return nil, err
			}
			var res any
			for i := 0; i < repeat; i++ {
				res, err = b.pressKeyAt(ctx, conn, tabID, px, py, key)
				if err != nil {
					return nil, err
				}
			}
			return res, nil
		}
		var res any
		for i := 0; i < repeat; i++ {
			internal := map[string]any{"tabId": tabID, "key": key}
			if hasSelector {
				internal["selector"] = selector
			}
			if len(mods) > 0 {
				internal["modifiers"] = mods
			}
			res, err = cmdKeypress(ctx, b, label, internal)
			if err != nil {
				return nil, err
			}
		}
		return res, nil

	case action == "scroll":
		internal := map[string]any{
			"tabId":     tabID,
			"direction": paramStringDefault(params, "scroll_direction", "down"),
			"amount":    paramInt(params, "scroll_amount", 500),
		}
		if hasSelector {
			internal["selector"] = selector
		}
		res, err := cmdScroll(ctx, b, label, internal)
		if err != nil {
			return nil, err
		}
		return attachInteractSnapshot(ctx, b, tabID, res, snapshotMode, waitAfterMS), nil

	case action == "drag":
		return b.doDrag(ctx, conn, tabID, dragRequest{
			startSelector:      startSelector,
			hasStartSelector:   hasStartSelector && startSelector != "",
			startCoordinate:    startCoordinate,
			hasStartCoordinate: hasStartCoordinate,
			endSelector:        selector,
			hasEndSelector:     hasSelector,
			endCoordinate:      coordinate,
			hasEndCoordinate:   hasCoordinate,
			timeout:            timeout(30000),
		})

	case action == "screenshot":
		intent, _ := paramString(params, "intent")
		if strings.TrimSpace(intent) == "" {
			return map[string]any{"ok": false, "error": "screenshot requires `intent` — one phrase naming the entity, the target element, and the action you plan to take."}, nil
		}
		internal := map[string]any{
			"tabId":    tabID,
			"fullPage": paramBoolAny(params, false, "full_page", "fullPage"),
			"intent":   intent,
		}
		if hasSelector {
			internal["selector"] = selector
		}
		// Same 800px JPEG + annotation contract the standalone verb uses
		// (the CLI spill path sends these alongside action=screenshot).
		if format, ok := paramString(params, "format"); ok {
			internal["format"] = format
		}
		if quality := paramInt(params, "quality", 0); quality > 0 {
			internal["quality"] = quality
		}
		if maxWidth := paramIntAny(params, 0, "max_width", "maxWidth"); maxWidth > 0 {
			internal["max_width"] = maxWidth
		}
		if _, present := params["annotate"]; present {
			internal["annotate"] = paramBool(params, "annotate", false)
		}
		res, err := cmdScreenshot(ctx, b, label, internal)
		if err != nil {
			return nil, err
		}
		if m, ok := res.(map[string]any); ok {
			m["action"] = "screenshot"
			m["imageType"] = m["format"]
		}
		return res, nil

	case action == "zoom":
		if !hasRegion || len(region) != 4 {
			return map[string]any{"ok": false, "error": "zoom requires region [x0, y0, x1, y1]"}, nil
		}
		cw, _ := b.viewportSize(ctx, conn, tabID)
		regionCSSWidth := math.Max(1, math.Abs(region[2]-region[0])*float64(cw))
		scale := math.Max(zoomMinScale, math.Min(zoomMaxScale, zoomTargetWidth/regionCSSWidth))
		res, err := b.captureRegion(ctx, conn, tabID, region[0], region[1], region[2], region[3], scale)
		if err != nil {
			return nil, err
		}
		res["action"] = "zoom"
		res["region"] = region
		res["note"] = "This image is a zoomed crop of the viewport. A position read off it is crop-relative — map it to a " +
			"viewport coordinate via crop_box: vx = crop_box[0] + fx*(crop_box[2]-crop_box[0]), " +
			"vy = crop_box[1] + fy*(crop_box[3]-crop_box[1]). Prefer browser_shadow_query for an exact clickable rect."
		return res, nil

	case action == "wait":
		if selectorWait, _ := paramString(params, "wait_for_selector"); strings.TrimSpace(selectorWait) != "" {
			found, err := b.waitForSelector(ctx, conn, tabID, selectorWait, timeout(5000))
			if err != nil {
				return nil, err
			}
			if !found {
				return map[string]any{"ok": false, "error": "Element not found within timeout: " + selectorWait}, nil
			}
			return map[string]any{"ok": true, "action": "wait", "condition": "selector", "selector": selectorWait}, nil
		}
		if waitText, _ := paramString(params, "wait_for_text"); strings.TrimSpace(waitText) != "" {
			found, err := b.waitForText(ctx, conn, tabID, waitText, timeout(5000))
			if err != nil {
				return nil, err
			}
			if !found {
				return map[string]any{"ok": false, "error": "Text not found within timeout: " + waitText}, nil
			}
			return map[string]any{"ok": true, "action": "wait", "condition": "text", "text": waitText}, nil
		}
		secs := paramFloat(params, "duration", 1.0)
		secs = math.Max(0, math.Min(10, secs))
		select {
		case <-ctx.Done():
			return nil, newError(CodeTimeout, "wait cancelled", false)
		case <-time.After(time.Duration(secs * float64(time.Second))):
		}
		return map[string]any{"ok": true, "action": "wait", "condition": "time", "seconds": secs}, nil
	}
	return map[string]any{"ok": false, "error": "unknown action: " + action}, nil
}

func cmdBrowserSelect(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	selector, _ := paramString(params, "selector")
	if strings.TrimSpace(selector) == "" {
		return nil, badRequest("missing required parameter 'selector'")
	}
	values, err := paramStringList(params, "values")
	if err != nil {
		return nil, err
	}
	if values == nil {
		return nil, badRequest("missing required parameter 'values'")
	}
	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	return b.selectOptions(ctx, conn, tabID, selector, values)
}

// attachInteractSnapshot mirrors interact._attach_snapshot: optional
// wait_after_ms pause, a 500 ms settle, then the accessibility snapshot under
// "snapshot". Snapshot failures never fail the interaction.
func attachInteractSnapshot(ctx context.Context, b *Bridge, tabID int, result any, mode string, waitAfterMS int) any {
	m, ok := result.(map[string]any)
	if !ok {
		return result
	}
	if okFlag, _ := m["ok"].(bool); !okFlag {
		return m
	}
	if waitAfterMS > 0 {
		select {
		case <-ctx.Done():
			m["snapshot_error"] = "cancelled before wait_after_ms"
			return m
		case <-time.After(time.Duration(waitAfterMS) * time.Millisecond):
		}
	}
	if mode == "off" {
		return m
	}
	switch mode {
	case "default", "simple", "interactive":
	default:
		m["snapshot_error"] = "invalid auto_snapshot_mode: " + mode
		return m
	}
	select {
	case <-ctx.Done():
		m["snapshot_error"] = "cancelled before auto-snapshot"
		return m
	case <-time.After(autoSnapshotSettle):
	}
	snap, err := cmdSnapshot(ctx, b, "", map[string]any{"tabId": tabID, "mode": mode})
	if err != nil {
		m["snapshot_error"] = err.Error()
		return m
	}
	m["snapshot"] = snap
	return m
}

type dragRequest struct {
	startSelector      string
	hasStartSelector   bool
	startCoordinate    []float64
	hasStartCoordinate bool
	endSelector        string
	hasEndSelector     bool
	endCoordinate      []float64
	hasEndCoordinate   bool
	timeout            time.Duration
}

// doDrag ports interact._do_drag: CDP mouse events for the physical gesture
// plus synthesised HTML5 drag-and-drop events (pages using the DnD API never
// see the CDP-only drag).
func (b *Bridge) doDrag(ctx context.Context, conn *Connection, tabID int, req dragRequest) (any, error) {
	if !req.hasStartSelector && !req.hasStartCoordinate {
		return map[string]any{"ok": false, "error": "drag requires start_selector or start_coordinate"}, nil
	}
	if !req.hasEndSelector && !req.hasEndCoordinate {
		return map[string]any{"ok": false, "error": "drag requires selector or coordinate for the end point"}, nil
	}
	for _, coord := range [][]float64{req.startCoordinate, req.endCoordinate} {
		if coord != nil {
			if bad := badFraction(coord); bad != "" {
				return map[string]any{"ok": false, "error": bad}, nil
			}
		}
	}
	cw, ch := 0, 0
	if req.hasStartCoordinate || req.hasEndCoordinate {
		cw, ch = b.viewportSize(ctx, conn, tabID)
	}
	deadline := time.Now().Add(req.timeout)
	resolve := func(selector string, hasSelector bool, coord []float64, hasCoord bool, label string) (float64, float64, error) {
		if hasCoord {
			return coord[0] * float64(cw), coord[1] * float64(ch), nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
		rect, err := b.pollElementRect(ctx, conn, tabID, selector, remaining)
		if err != nil {
			return 0, 0, newError(CodeNotFound, label+" element not found: "+selector, false)
		}
		return rect.X, rect.Y, nil
	}
	sx, sy, err := resolve(req.startSelector, req.hasStartSelector, req.startCoordinate, req.hasStartCoordinate, "Start")
	if err != nil {
		return nil, err
	}
	ex, ey, err := resolve(req.endSelector, req.hasEndSelector, req.endCoordinate, req.hasEndCoordinate, "End")
	if err != nil {
		return nil, err
	}

	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mousePressed", "x": sx, "y": sy, "button": "left", "clickCount": 1,
	}, 10*time.Second); err != nil {
		return nil, err
	}
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": ex, "y": ey, "button": "none",
	}, 10*time.Second); err != nil {
		return nil, err
	}
	if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseReleased", "x": ex, "y": ey, "button": "left", "clickCount": 1,
	}, 10*time.Second); err != nil {
		return nil, err
	}

	dragJS := fmt.Sprintf(`(function(sx, sy, ex, ey) {
  var src = document.elementFromPoint(sx, sy);
  var dst = document.elementFromPoint(ex, ey);
  if (!src || !dst) return {ok: false, reason: 'no element at point'};
  var dt = new DataTransfer();
  var ds = new DragEvent('dragstart', {bubbles: true, cancelable: true, dataTransfer: dt});
  src.dispatchEvent(ds);
  if (ds.defaultPrevented) return {ok: false, reason: 'dragstart prevented'};
  dst.dispatchEvent(new DragEvent('dragenter', {bubbles: true, cancelable: true, dataTransfer: dt}));
  dst.dispatchEvent(new DragEvent('dragover', {bubbles: true, cancelable: true, dataTransfer: dt}));
  dst.dispatchEvent(new DragEvent('drop', {bubbles: true, cancelable: true, dataTransfer: dt}));
  src.dispatchEvent(new DragEvent('dragend', {bubbles: true, cancelable: false, dataTransfer: dt}));
  return {ok: true};
})(%f, %f, %f, %f)`, sx, sy, ex, ey)
	if _, err := b.evalJS(ctx, conn, tabID, dragJS, 15*time.Second); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "action": "drag", "tabId": tabID,
		"fromCoords": map[string]any{"x": sx, "y": sy},
		"toCoords":   map[string]any{"x": ex, "y": ey},
	}, nil
}

// pointFromFraction converts a [x,y] viewport fraction into CSS pixels using
// the live viewport size (upstream _ensure_viewport_size).
func (b *Bridge) pointFromFraction(ctx context.Context, conn *Connection, tabID int, coord []float64) (float64, float64, error) {
	if len(coord) != 2 {
		return 0, 0, badRequest("coordinate must be [x, y]")
	}
	cw, ch := b.viewportSize(ctx, conn, tabID)
	return coord[0] * float64(cw), coord[1] * float64(ch), nil
}

func badFraction(coord []float64) string {
	if len(coord) != 2 {
		return "coordinate must be [x, y]"
	}
	x, y := coord[0], coord[1]
	if x > 1.5 || y > 1.5 || x < -0.1 || y < -0.1 {
		return fmt.Sprintf("Coords (%v, %v) look like pixels. coordinate expects fractions 0..1 of the viewport. "+
			"Read the target's proportional position off a screenshot, or pass rect.cx / rect.cy from browser_shadow_query.", x, y)
	}
	return ""
}

// splitKeySpec splits "ctrl+shift+Tab" into ("Tab", ["ctrl","shift"]).
func splitKeySpec(text string) (string, []string) {
	if len(text) > 1 && strings.Contains(text, "+") {
		parts := strings.Split(text, "+")
		key := strings.TrimSpace(parts[len(parts)-1])
		mods := make([]string, 0, len(parts)-1)
		for _, part := range parts[:len(parts)-1] {
			if part = strings.TrimSpace(part); part != "" {
				mods = append(mods, part)
			}
		}
		return key, mods
	}
	return text, nil
}

func paramFloatList(params map[string]any, key string) ([]float64, bool, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return nil, false, nil
	}
	switch rows := v.(type) {
	case []any:
		out := make([]float64, 0, len(rows))
		for _, row := range rows {
			f, ok := toFloat(row)
			if !ok {
				return nil, false, badRequest("'%s' must be an array of numbers", key)
			}
			out = append(out, f)
		}
		return out, true, nil
	case []float64:
		return rows, true, nil
	}
	return nil, false, badRequest("'%s' must be an array of numbers", key)
}

func paramStringList(params map[string]any, key string) ([]string, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return nil, nil
	}
	switch rows := v.(type) {
	case []any:
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			s, ok := row.(string)
			if !ok {
				return nil, badRequest("'%s' must be an array of strings", key)
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		return rows, nil
	case string:
		return []string{rows}, nil
	}
	return nil, badRequest("'%s' must be an array of strings", key)
}

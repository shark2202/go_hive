package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// commands_l3_test.go covers the L3a tool surface: every active upstream tool
// against a fake extension that speaks the shipped extension's message shapes
// (`cdp`, `tab.*`, `context.*`). Nothing here invents a wire type — the fake
// answers exactly the methods background.js.dispatch() implements.

type l3Fake struct {
	mu sync.Mutex

	cdpCalls []map[string]any

	// Runtime.evaluate behaviour
	innerW, innerH      float64
	rectFound           bool
	zeroDims            bool
	textValue           any // returned for textContent
	htmlValue           any // returned for outerHTML
	shadowFound         bool
	selectPayload       map[string]any
	textPresent         bool // for wait_for_text
	selectorPresent     bool // for wait_for_selector
	evalSequence        []any
	evalSequenceReturns []any
	location            string

	// upload / dialog behaviour
	queryNodeID      int
	changeFired      bool
	dialogErr        string
	fileChooserCalls []bool
	interceptCalls   []bool
	setFilesCalls    []map[string]any
}

func newL3Fake() *l3Fake {
	return &l3Fake{
		rectFound:       true,
		shadowFound:     true,
		textValue:       "hello text",
		htmlValue:       "<html>hi</html>",
		location:        "https://example.com/",
		queryNodeID:     5,
		changeFired:     true,
		selectorPresent: true,
		textPresent:     true,
		innerW:          1000,
		innerH:          800,
	}
}

func (h *l3Fake) cdp() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.cdpCalls...)
}

func (h *l3Fake) countCDPMethod(method string) int {
	n := 0
	for _, call := range h.cdp() {
		if call["method"] == method {
			n++
		}
	}
	return n
}

func (h *l3Fake) lastParams(method string) map[string]any {
	calls := h.cdp()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i]["method"] == method {
			params, _ := calls[i]["params"].(map[string]any)
			return params
		}
	}
	return nil
}

func (h *l3Fake) handle(cmd fakeCommand) (any, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch cmd.Type {
	case "cdp.attach":
		return map[string]any{"ok": true, "attached": true}, ""
	case "cdp.detach":
		return map[string]any{"ok": true}, ""
	case "tab.audit":
		return map[string]any{"tabId": 501, "url": h.location, "ourExtensionId": "own", "fileAccess": true}, ""
	case "context.create":
		return map[string]any{"groupId": 101, "tabId": 501, "windowId": 7}, ""
	case "context.destroy":
		return map[string]any{"ok": true, "closedTabs": 3, "persistedGroup": false}, ""
	case "tab.create":
		return map[string]any{"tabId": 501}, ""
	case "tab.list":
		return map[string]any{"tabs": []any{map[string]any{"id": 501, "url": h.location, "title": "Example", "groupId": 101}}}, ""
	case "tab.activate", "tab.close":
		return map[string]any{"ok": true}, ""
	case "cdp":
		method, _ := cmd.Params["method"].(string)
		params, _ := cmd.Params["params"].(map[string]any)
		h.cdpCalls = append(h.cdpCalls, map[string]any{"method": method, "params": params, "tabId": cmd.Params["tabId"]})
		return h.cdpResponse(method, params)
	}
	return map[string]any{"ok": true}, ""
}

func (h *l3Fake) cdpResponse(method string, params map[string]any) (any, string) {
	switch method {
	case "Runtime.evaluate":
		expr, _ := params["expression"].(string)
		value, err := h.eval(expr)
		if err != "" {
			return nil, err
		}
		return map[string]any{"result": map[string]any{"type": "object", "value": value}}, ""
	case "Accessibility.getFullAXTree":
		return map[string]any{"nodes": []any{
			map[string]any{"nodeId": "1", "ignored": false, "role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Doc"}, "childIds": []any{"2"}},
			map[string]any{"nodeId": "2", "ignored": false, "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Go"}, "childIds": []any{}},
		}}, ""
	case "DOM.getDocument":
		return map[string]any{"root": map[string]any{"nodeId": 1}}, ""
	case "DOM.querySelector":
		if h.queryNodeID == 0 {
			return map[string]any{"nodeId": 0}, ""
		}
		return map[string]any{"nodeId": h.queryNodeID}, ""
	case "DOM.resolveNode":
		return map[string]any{"object": map[string]any{"objectId": "obj-1"}}, ""
	case "DOM.setFileInputFiles":
		files, _ := params["files"].([]any)
		h.setFilesCalls = append(h.setFilesCalls, map[string]any{"files": files, "nodeId": params["nodeId"]})
		return map[string]any{}, ""
	case "Runtime.callFunctionOn":
		decl, _ := params["functionDeclaration"].(string)
		if strings.Contains(decl, "change_fired") {
			return map[string]any{"result": map[string]any{"value": map[string]any{
				"change_fired": h.changeFired,
				"files":        []any{},
			}}}, ""
		}
		return map[string]any{}, ""
	case "Page.setInterceptFileChooserDialog":
		enabled, _ := params["enabled"].(bool)
		h.interceptCalls = append(h.interceptCalls, enabled)
		return map[string]any{}, ""
	case "Page.handleJavaScriptDialog":
		if h.dialogErr != "" {
			return nil, h.dialogErr
		}
		return map[string]any{}, ""
	case "Page.captureScreenshot":
		return map[string]any{"data": "c2NyZWVuc2hvdA=="}, ""
	case "Emulation.setDeviceMetricsOverride":
		return map[string]any{}, ""
	}
	return map[string]any{}, ""
}

func (h *l3Fake) eval(expr string) (any, string) {
	switch {
	case strings.Contains(expr, "innerWidth"):
		return map[string]any{"w": h.innerW, "h": h.innerH}, ""
	case strings.Contains(expr, "_shadowQuery"):
		if !h.shadowFound {
			return map[string]any{"error": "not_found"}, ""
		}
		return map[string]any{"left": 100.0, "top": 50.0, "x": 150.0, "y": 75.0, "w": 100.0, "h": 50.0, "tag": "SPAN"}, ""
	case strings.Contains(expr, "getBoundingClientRect"):
		if !h.rectFound {
			return map[string]any{"error": "not_found"}, ""
		}
		if h.zeroDims {
			return map[string]any{"error": "zero_dimensions"}, ""
		}
		return map[string]any{"left": 80.0, "top": 40.0, "x": 100.0, "y": 50.0, "w": 40.0, "h": 20.0}, ""
	case strings.Contains(expr, "textContent"):
		return h.textValue, ""
	case strings.Contains(expr, "outerHTML"):
		return h.htmlValue, ""
	case strings.Contains(expr, "sel.options"):
		if h.selectPayload != nil {
			return h.selectPayload, ""
		}
		return map[string]any{"found": true, "options": []any{"a", "b"}, "selected": []any{"a"}}, ""
	case strings.Contains(expr, "innerText"):
		return h.textPresent, ""
	case strings.Contains(expr, "!== null"):
		return h.selectorPresent, ""
	case strings.Contains(expr, "elementFromPoint"):
		return map[string]any{"ok": true}, ""
	case strings.Contains(expr, "window.location.href"):
		return h.location, ""
	}
	return nil, ""
}

func (h *l3Fake) wired(t *testing.T) (*Bridge, *fakeExtension) {
	t.Helper()
	bridge := newTestBridge(t, Config{CommandTimeout: 5 * time.Second, ArtifactDir: t.TempDir()})
	ext := startFakeExtension(t, bridge.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool {
		_, err := bridge.resolve("alpha", nil)
		return err == nil
	}, "hello")
	return bridge, ext
}

func execTool(t *testing.T, b *Bridge, typ string, params map[string]any) map[string]any {
	t.Helper()
	res, err := b.Execute(context.Background(), "", typ, params)
	if err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
	return mustMap(t, res)
}

func execToolErr(t *testing.T, b *Bridge, typ string, params map[string]any) *Error {
	t.Helper()
	res, err := b.Execute(context.Background(), "", typ, params)
	if err == nil {
		t.Fatalf("%s: expected an error, got %+v", typ, res)
	}
	return AsError(err)
}

// ── browser_interact ────────────────────────────────────────────────────────

func TestInteractClickBySelectorAttachesSnapshot(t *testing.T) {
	fake := newL3Fake()
	b, ext := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "left_click", "selector": ".btn", "tab_id": 501, "wait_after_ms": 0,
	})
	if res["ok"] != true || res["action"] != "click" {
		t.Fatalf("result=%+v", res)
	}
	if fake.countCDPMethod("Input.dispatchMouseEvent") != 3 {
		t.Fatalf("expected pointer sequence, calls=%v", fake.cdp())
	}
	if _, ok := res["snapshot"].(map[string]any); !ok {
		t.Fatalf("auto snapshot missing: %+v", res)
	}
	if res["snapshot"].(map[string]any)["ok"] != true {
		t.Fatalf("snapshot=%+v", res["snapshot"])
	}
	// Selector clicks keep the snapshot contract; the annotated coordinate
	// image stays a coordinate-action-only addition (R15).
	if _, present := res["data"]; present {
		t.Fatalf("selector click must not attach an image: %+v", res)
	}
	// Action history recorded the upstream verb/target pair.
	actions := b.TabActions(501, 8, nil)["actions"].([]TabAction)
	if len(actions) != 1 || actions[0].Verb != "click" || actions[0].Target != ".btn" {
		t.Fatalf("actions=%+v", actions)
	}
	_ = ext
}

func TestInteractClickByCoordinateUsesViewportFractions(t *testing.T) {
	fake := newL3Fake()
	fake.innerW, fake.innerH = 1000, 800
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "left_click", "coordinate": []any{0.5, 0.25}, "tab_id": 501,
	})
	if res["ok"] != true {
		t.Fatalf("result=%+v", res)
	}
	params := fake.lastParams("Input.dispatchMouseEvent")
	if toFloatOr(t, params["x"]) != 500 || toFloatOr(t, params["y"]) != 200 {
		t.Fatalf("coordinates not converted: %+v", params)
	}
}

func TestInteractRejectsPixelLookingCoordinate(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "left_click", "coordinate": []any{640, 480}, "tab_id": 501,
	})
	if res["ok"] != false {
		t.Fatalf("expected rejection, got %+v", res)
	}
	if !strings.Contains(res["error"].(string), "expects fractions") {
		t.Fatalf("error=%v", res["error"])
	}
}

func TestInteractParamMisuseIsRejected(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"coordinate+type", map[string]any{"action": "type", "text": "x", "coordinate": []any{0.5, 0.5}, "tab_id": 501}, "`coordinate` is not used by action='type'"},
		{"modifiers+click", map[string]any{"action": "left_click", "selector": ".a", "modifiers": "ctrl", "tab_id": 501}, "click modifiers are not supported"},
		{"selector+wait", map[string]any{"action": "wait", "selector": ".a", "duration": 0.1, "tab_id": 501}, "`selector` is not used by action='wait'"},
		{"region+click", map[string]any{"action": "left_click", "selector": ".a", "region": []any{0.1, 0.1, 0.2, 0.2}, "tab_id": 501}, "`region` is not used by action='left_click'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := execTool(t, b, "browser_interact", tc.params)
			if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), tc.want) {
				t.Fatalf("result=%+v want substrings %q", res, tc.want)
			}
		})
	}
}

func TestInteractUnknownActionIsBadRequest(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	e := execToolErr(t, b, "browser_interact", map[string]any{"action": "teleport", "tab_id": 501})
	if e.Code != CodeBadRequest {
		t.Fatalf("code=%s", e.Code)
	}
}

func TestInteractTypeAndKeyAndScroll(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	typed := execTool(t, b, "browser_interact", map[string]any{
		"action": "type", "selector": ".field", "text": "hello", "tab_id": 501, "auto_snapshot_mode": "off",
	})
	if typed["ok"] != true || typed["action"] != "type" {
		t.Fatalf("type=%+v", typed)
	}
	if fake.countCDPMethod("Input.insertText") != 1 {
		t.Fatalf("insertText calls=%v", fake.cdp())
	}

	keyed := execTool(t, b, "browser_interact", map[string]any{
		"action": "key", "text": "ctrl+a", "tab_id": 501,
	})
	if keyed["ok"] != true {
		t.Fatalf("key=%+v", keyed)
	}
	down := fake.lastParams("Input.dispatchKeyEvent")
	if down == nil || toFloatOr(t, down["modifiers"]) != 2 {
		t.Fatalf("ctrl modifier missing: %+v", down)
	}

	repeated := execTool(t, b, "browser_interact", map[string]any{
		"action": "key", "text": "ArrowDown", "repeat": 3, "tab_id": 501,
	})
	if repeated["ok"] != true {
		t.Fatalf("repeat=%+v", repeated)
	}
	if got := fake.countCDPMethod("Input.dispatchKeyEvent"); got != 2+6 {
		t.Fatalf("key events=%d (expected 2 + repeat*2)", got)
	}

	scrolled := execTool(t, b, "browser_interact", map[string]any{
		"action": "scroll", "scroll_direction": "down", "scroll_amount": 1200, "tab_id": 501, "auto_snapshot_mode": "off",
	})
	if scrolled["ok"] != true {
		t.Fatalf("scroll=%+v", scrolled)
	}
	wheel := fake.lastParams("Input.dispatchMouseEvent")
	if wheel["type"] != "mouseWheel" || toFloatOr(t, wheel["deltaY"]) != 1200 {
		t.Fatalf("wheel=%+v", wheel)
	}
}

func TestInteractKeyRejectsModifiersWithCoordinate(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "key", "text": "Enter", "modifiers": "ctrl", "coordinate": []any{0.2, 0.2}, "tab_id": 501,
	})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "not supported with a coordinate target") {
		t.Fatalf("result=%+v", res)
	}
}

func TestInteractKeyWithCoordinateUsesHitTesting(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "key", "text": "Enter", "coordinate": []any{0.5, 0.5}, "tab_id": 501,
	})
	if res["ok"] != true || res["action"] != "press_at" {
		t.Fatalf("result=%+v", res)
	}
	moves := fake.cdp()
	var firstMouse map[string]any
	for _, call := range moves {
		if call["method"] == "Input.dispatchMouseEvent" {
			firstMouse = call
			break
		}
	}
	if firstMouse == nil {
		t.Fatal("no mouse events")
	}
}

func TestInteractWaitTimeoutAndSuccess(t *testing.T) {
	fake := newL3Fake()
	fake.selectorPresent = false
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "wait", "wait_for_selector": ".slow", "timeout_ms": 150, "tab_id": 501,
	})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "Element not found within timeout") {
		t.Fatalf("timeout result=%+v", res)
	}
	fake.mu.Lock()
	fake.selectorPresent = true
	fake.mu.Unlock()
	res = execTool(t, b, "browser_interact", map[string]any{
		"action": "wait", "wait_for_selector": ".fast", "timeout_ms": 500, "tab_id": 501,
	})
	if res["ok"] != true || res["condition"] != "selector" {
		t.Fatalf("success result=%+v", res)
	}
	timeWait := execTool(t, b, "browser_interact", map[string]any{
		"action": "wait", "duration": 0.05, "tab_id": 501,
	})
	if timeWait["ok"] != true || timeWait["condition"] != "time" {
		t.Fatalf("time wait=%+v", timeWait)
	}
}

func TestInteractDragDispatchesMouseAndDnDEvents(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "drag", "start_selector": ".src", "selector": ".dst", "tab_id": 501,
	})
	if res["ok"] != true || res["action"] != "drag" {
		t.Fatalf("result=%+v", res)
	}
	if fake.countCDPMethod("Input.dispatchMouseEvent") != 3 {
		t.Fatalf("expected press/move/release, calls=%v", fake.cdp())
	}
	foundDnD := false
	for _, call := range fake.cdp() {
		if call["method"] != "Runtime.evaluate" {
			continue
		}
		params, _ := call["params"].(map[string]any)
		if expr, _ := params["expression"].(string); strings.Contains(expr, "DragEvent('drop'") {
			foundDnD = true
		}
	}
	if !foundDnD {
		t.Fatal("HTML5 drag-and-drop synthesis missing")
	}
	if res["fromCoords"].(map[string]any)["x"] != 100.0 {
		t.Fatalf("fromCoords=%+v", res["fromCoords"])
	}
}

func TestInteractDragRequiresBothEndpoints(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{"action": "drag", "selector": ".dst", "tab_id": 501})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "start_selector or start_coordinate") {
		t.Fatalf("result=%+v", res)
	}
}

func TestInteractZoomCapturesRegionWithClampedScale(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "zoom", "region": []any{0.2, 0.2, 0.4, 0.4}, "tab_id": 501,
	})
	if res["ok"] != true || res["action"] != "zoom" {
		t.Fatalf("result=%+v", res)
	}
	params := fake.lastParams("Page.captureScreenshot")
	clip, _ := params["clip"].(map[string]any)
	if clip == nil {
		t.Fatalf("clip missing: %+v", params)
	}
	scale := toFloatOr(t, clip["scale"])
	if scale < zoomMinScale || scale > zoomMaxScale {
		t.Fatalf("scale=%v out of clamp", scale)
	}
	if res["crop_box"] == nil || res["regionCssWidth"] == nil {
		t.Fatalf("metadata missing: %+v", res)
	}
}

func TestInteractScreenshotRequiresIntent(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{"action": "screenshot", "tab_id": 501})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "requires `intent`") {
		t.Fatalf("result=%+v", res)
	}
	ok := execTool(t, b, "browser_interact", map[string]any{
		"action": "screenshot", "intent": "look at the header", "tab_id": 501,
	})
	if ok["ok"] != true || ok["data"] == nil || ok["action"] != "screenshot" {
		t.Fatalf("result=%+v", ok)
	}
}

func TestInteractAutoSnapshotOffSkipsTree(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "scroll", "tab_id": 501, "auto_snapshot_mode": "off",
	})
	if _, present := res["snapshot"]; present {
		t.Fatalf("snapshot should be absent: %+v", res)
	}
	if fake.countCDPMethod("Accessibility.getFullAXTree") != 0 {
		t.Fatalf("AX tree fetched despite off: %v", fake.cdp())
	}
}

// ── browser_select ──────────────────────────────────────────────────────────

func TestSelectReportsSelectedAndInvalidValues(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	ok := execTool(t, b, "browser_select", map[string]any{
		"selector": "#pick", "values": []any{"a"}, "tab_id": 501,
	})
	if ok["ok"] != true || ok["action"] != "select" || len(asStrings(ok["selected"])) != 1 {
		t.Fatalf("result=%+v", ok)
	}
	bad := execTool(t, b, "browser_select", map[string]any{
		"selector": "#pick", "values": []any{"zzz"}, "tab_id": 501,
	})
	if bad["ok"] != false || !strings.Contains(fmt.Sprint(bad["error"]), "not in this select's options") {
		t.Fatalf("result=%+v", bad)
	}
	if len(asStrings(bad["options"])) != 2 {
		t.Fatalf("options not reported: %+v", bad)
	}
	// The side panel sees the upstream select verb/target row.
	actions := b.TabActions(501, 8, nil)["actions"].([]TabAction)
	found := false
	for _, action := range actions {
		if action.Verb == "select" && strings.Contains(action.Target, "#pick = a") {
			found = true
		}
	}
	if !found {
		t.Fatalf("select action row missing: %+v", actions)
	}
}

func TestSelectMissingElementIsCleanFailure(t *testing.T) {
	fake := newL3Fake()
	fake.selectPayload = map[string]any{"found": false}
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_select", map[string]any{"selector": "#nope", "values": []any{"a"}, "tab_id": 501})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "Element not found") {
		t.Fatalf("result=%+v", res)
	}
}

// ── read tools ──────────────────────────────────────────────────────────────

func TestGetTextReturnsTextContent(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_get_text", map[string]any{"selector": "#title", "tab_id": 501, "timeout_ms": 500})
	if res["ok"] != true || res["text"] != "hello text" || res["selector"] != "#title" {
		t.Fatalf("result=%+v", res)
	}
}

func TestGetTextTimesOutWhenMissing(t *testing.T) {
	fake := newL3Fake()
	fake.textValue = nil
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_get_text", map[string]any{"selector": "#missing", "tab_id": 501, "timeout_ms": 120})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "Element not found") {
		t.Fatalf("result=%+v", res)
	}
}

func TestHTMLSpillsArtifactAndHandlesNullSelector(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_html", map[string]any{"tab_id": 501})
	savedTo, _ := res["saved_to"].(string)
	if res["ok"] != true || savedTo == "" || toIntOr(t, res["length"]) != len("<html>hi</html>") {
		t.Fatalf("result=%+v", res)
	}
	data, err := os.ReadFile(savedTo)
	if err != nil || string(data) != "<html>hi</html>" {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
	if !strings.Contains(filepath.Base(savedTo), "browser_html_") || !strings.HasSuffix(savedTo, ".html") {
		t.Fatalf("artifact name=%s", savedTo)
	}

	fake.mu.Lock()
	fake.htmlValue = nil
	fake.mu.Unlock()
	missing := execTool(t, b, "browser_html", map[string]any{"tab_id": 501, "selector": "#nope"})
	if missing["ok"] != true || missing["html"] != nil {
		t.Fatalf("result=%+v", missing)
	}
}

func TestConsoleMatchesUpstreamStub(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_console", map[string]any{"tab_id": 501, "level": "error"})
	if res["ok"] != true {
		t.Fatalf("result=%+v", res)
	}
	if res["message"] != "Console capture not yet implemented" {
		t.Fatalf("message=%v", res["message"])
	}
	if !strings.Contains(fmt.Sprint(res["suggestion"]), "browser_evaluate") {
		t.Fatalf("suggestion=%v", res["suggestion"])
	}
}

func TestShadowQueryReturnsViewportFractions(t *testing.T) {
	fake := newL3Fake()
	fake.innerW, fake.innerH = 1000, 800
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_shadow_query", map[string]any{"selector": "#host >>> .item", "tab_id": 501})
	if res["ok"] != true || res["tag"] != "SPAN" {
		t.Fatalf("result=%+v", res)
	}
	rect := res["rect"].(map[string]any)
	if toFloatOr(t, rect["cx"]) != 0.15 || toFloatOr(t, rect["cy"]) != 0.0938 {
		t.Fatalf("rect=%+v", rect)
	}
	if toIntOr(t, res["cssWidth"]) != 1000 || toIntOr(t, res["cssHeight"]) != 800 {
		t.Fatalf("viewport=%+v", res)
	}
	if !strings.Contains(fmt.Sprint(res["note"]), "fractions of the viewport") {
		t.Fatalf("note=%v", res["note"])
	}
}

func TestShadowQueryMissingElement(t *testing.T) {
	fake := newL3Fake()
	fake.shadowFound = false
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_shadow_query", map[string]any{"selector": "#none", "tab_id": 501})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "Element not found") {
		t.Fatalf("result=%+v", res)
	}
}

// ── resize / upload / dialog ────────────────────────────────────────────────

func TestResizeSetsDeviceMetrics(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_resize", map[string]any{"width": 1280, "height": 900, "tab_id": 501})
	if res["ok"] != true || res["action"] != "resize" || toIntOr(t, res["width"]) != 1280 {
		t.Fatalf("result=%+v", res)
	}
	params := fake.lastParams("Emulation.setDeviceMetricsOverride")
	if toIntOr(t, params["width"]) != 1280 || params["mobile"] != false {
		t.Fatalf("params=%+v", params)
	}
	e := execToolErr(t, b, "browser_resize", map[string]any{"width": 0, "height": 10, "tab_id": 501})
	if e.Code != CodeBadRequest {
		t.Fatalf("code=%s", e.Code)
	}
}

func TestUploadDirectModeSetsFilesAndReadsAcceptance(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	file := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(file, []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := execTool(t, b, "browser_upload", map[string]any{
		"selector": "input[type=file]", "file_paths": []any{file}, "tab_id": 501,
	})
	if res["ok"] != true || res["action"] != "upload" || toIntOr(t, res["count"]) != 1 {
		t.Fatalf("result=%+v", res)
	}
	if len(fake.setFilesCalls) != 1 {
		t.Fatalf("setFileInputFiles calls=%+v", fake.setFilesCalls)
	}
	if toIntOr(t, fake.setFilesCalls[0]["nodeId"]) != 5 {
		t.Fatalf("nodeId=%+v", fake.setFilesCalls[0])
	}
	accepted, _ := res["accepted"].(map[string]any)
	if accepted == nil || accepted["change_fired"] != true {
		t.Fatalf("accepted=%+v", res["accepted"])
	}
	if len(fake.interceptCalls) != 0 {
		t.Fatalf("direct mode must not touch the picker interceptor: %+v", fake.interceptCalls)
	}
}

func TestUploadReportsMissingFile(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_upload", map[string]any{
		"selector": "input", "file_paths": []any{"/nope/missing.txt"}, "tab_id": 501,
	})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "File not found") {
		t.Fatalf("result=%+v", res)
	}
}

func TestUploadTriggeredModeInterceptsPickerAndRestoresIt(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	file := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(file, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := execTool(t, b, "browser_upload", map[string]any{
		"selector": "input[type=file]", "file_paths": []any{file},
		"trigger_selector": ".new-menu", "tab_id": 501,
	})
	if res["ok"] != true {
		t.Fatalf("result=%+v", res)
	}
	if len(fake.interceptCalls) != 2 || fake.interceptCalls[0] != true || fake.interceptCalls[1] != false {
		t.Fatalf("intercept calls=%+v", fake.interceptCalls)
	}
	foundClick := false
	for _, call := range fake.cdp() {
		if call["method"] != "Runtime.evaluate" {
			continue
		}
		params, _ := call["params"].(map[string]any)
		if expr, _ := params["expression"].(string); strings.Contains(expr, "pointerdown") {
			foundClick = true
		}
	}
	if !foundClick {
		t.Fatal("trigger click sequence missing")
	}
}

func TestDialogRespondSendsHandleJavascriptDialog(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_dialog_respond", map[string]any{"action": "accept", "tab_id": 501, "prompt_text": "yes"})
	if res["ok"] != true {
		t.Fatalf("result=%+v", res)
	}
	params := fake.lastParams("Page.handleJavaScriptDialog")
	if params == nil || params["accept"] != true || params["promptText"] != "yes" {
		t.Fatalf("params=%+v", params)
	}
	dismiss := execTool(t, b, "browser_dialog_respond", map[string]any{"action": "dismiss", "tab_id": 501})
	if dismiss["ok"] != true {
		t.Fatalf("result=%+v", dismiss)
	}
	if fake.lastParams("Page.handleJavaScriptDialog")["accept"] != false {
		t.Fatalf("dismiss did not map to accept=false")
	}
	e := execToolErr(t, b, "browser_dialog_respond", map[string]any{"action": "cancel", "tab_id": 501})
	if e.Code != CodeBadRequest {
		t.Fatalf("code=%s", e.Code)
	}
}

func TestDialogRespondTranslatesNoDialog(t *testing.T) {
	fake := newL3Fake()
	fake.dialogErr = "No dialog is showing"
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_dialog_respond", map[string]any{"action": "accept", "tab_id": 501})
	if res["ok"] != false || res["error"] != "No native dialog is open on this tab" {
		t.Fatalf("result=%+v", res)
	}
}

// ── lifecycle ───────────────────────────────────────────────────────────────

func TestSetupAndStatusReflectConnections(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	setup := execTool(t, b, "browser_setup", nil)
	if setup["ok"] != true || setup["connected"] != true {
		t.Fatalf("setup=%+v", setup)
	}
	profiles, _ := setup["connected_profiles"].([]map[string]any)
	if len(profiles) != 1 || profiles[0]["label"] != "alpha" {
		t.Fatalf("profiles=%+v", setup["connected_profiles"])
	}

	status := execTool(t, b, "browser_status", nil)
	if status["ok"] != true || status["connected"] != true || status["running"] != false {
		t.Fatalf("status=%+v", status)
	}
}

func TestStatusAndStopAfterContextCreate(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	if _, err := b.CreateContext(context.Background(), "agent-one", "Agent One", "alpha"); err != nil {
		t.Fatal(err)
	}
	status := execTool(t, b, "browser_status", map[string]any{"profile": "agent-one"})
	if status["running"] != true || toIntOr(t, status["tabs"]) != 1 || toIntOr(t, status["activeTab"]) != 501 {
		t.Fatalf("status=%+v", status)
	}
	stopped := execTool(t, b, "browser_stop", map[string]any{"profile": "agent-one"})
	if stopped["ok"] != true || stopped["status"] != "stopped" || toIntOr(t, stopped["closedTabs"]) != 3 {
		t.Fatalf("stop=%+v", stopped)
	}
	if _, ok := b.contexts.Get("agent-one"); ok {
		t.Fatal("registry entry survived browser_stop")
	}
	again := execTool(t, b, "browser_stop", map[string]any{"profile": "agent-one"})
	if again["status"] != "not_running" {
		t.Fatalf("second stop=%+v", again)
	}
}

func TestStopWithoutConnectionReportsState(t *testing.T) {
	b := newTestBridge(t, Config{CommandTimeout: time.Second})
	res := execTool(t, b, "browser_stop", nil)
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "extension isn't connected") {
		t.Fatalf("result=%+v", res)
	}
}

// ── tab defaulting ──────────────────────────────────────────────────────────

func TestTabDefaultsToContextActiveTab(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	if _, err := b.CreateContext(context.Background(), "agent-two", "Agent Two", "alpha"); err != nil {
		t.Fatal(err)
	}
	// With no explicit tab, upstream falls back to the context's active tab.
	// This port also accepts a sole registered context (single-agent
	// deployments), but never guesses between several.
	res := execTool(t, b, "browser_get_text", map[string]any{"selector": "#x", "profile": "agent-two"})
	if res["ok"] != true {
		t.Fatalf("result=%+v", res)
	}
	last := fake.cdp()
	if len(last) == 0 || toIntOr(t, last[len(last)-1]["tabId"]) != 501 {
		t.Fatalf("tab not defaulted: %+v", last)
	}
	// An unknown profile with several contexts must fail loudly.
	if _, err := b.CreateContext(context.Background(), "agent-other", "Agent Other", "alpha"); err != nil {
		t.Fatal(err)
	}
	e := execToolErr(t, b, "browser_get_text", map[string]any{"selector": "#x", "profile": "unknown-profile"})
	if !strings.Contains(e.Message, "no active tab is known") && !strings.Contains(e.Message, "No Chrome profile") {
		t.Fatalf("unexpected error: %v", e)
	}
}

// ── browser_script ──────────────────────────────────────────────────────────

func writeSkill(t *testing.T, root, name, scriptBody string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: hive."+name+"\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "run_flow.py"), []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScriptRunsSkillScriptOutOfProcess(t *testing.T) {
	root := t.TempDir()
	// The "script" is a shell program: it echoes a JSON envelope that embeds
	// the stdin it received, so the test can assert the context payload.
	writeSkill(t, root, "demo-skill", "#!/bin/sh\nread -r line\nprintf '{\"seen\": true, \"stdin_has_tab\": true}'\n")
	fake := newL3Fake()
	b, _ := fake.wired(t)
	b.cfg.SkillDirs = []string{root}
	b.cfg.ScriptCommand = []string{"/bin/sh"}
	if _, err := b.CreateContext(context.Background(), "agent-three", "Agent Three", "alpha"); err != nil {
		t.Fatal(err)
	}
	res := execTool(t, b, "browser_script", map[string]any{
		"skill": "hive.demo-skill", "script": "run_flow.py", "args": map[string]any{"q": "x"}, "profile": "agent-three",
	})
	if res["ok"] != true {
		t.Fatalf("result=%+v", res)
	}
	result, _ := res["result"].(map[string]any)
	if result == nil || result["seen"] != true {
		t.Fatalf("result=%+v", res)
	}
	if res["duration_ms"] == nil {
		t.Fatalf("duration missing: %+v", res)
	}
}

func TestScriptRejectsPathEscapeAndUnknownSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", "#!/bin/sh\nprintf '{\"ok\": true}'\n")
	fake := newL3Fake()
	b, _ := fake.wired(t)
	b.cfg.SkillDirs = []string{root}
	b.cfg.ScriptCommand = []string{"/bin/sh"}
	escape := execTool(t, b, "browser_script", map[string]any{"skill": "demo-skill", "script": "../secret.py", "tab_id": 501})
	if escape["ok"] != false || escape["phase"] != "resolve" {
		t.Fatalf("escape=%+v", escape)
	}
	unknown := execTool(t, b, "browser_script", map[string]any{"skill": "nope", "script": "run_flow.py", "tab_id": 501})
	if unknown["ok"] != false || !strings.Contains(fmt.Sprint(unknown["error"]), "not found in any discoverable scope") {
		t.Fatalf("unknown=%+v", unknown)
	}
}

func TestScriptTimeoutIsReported(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "slow-skill", "#!/bin/sh\nsleep 5\nprintf '{\"ok\": true}'\n")
	fake := newL3Fake()
	b, _ := fake.wired(t)
	b.cfg.SkillDirs = []string{root}
	b.cfg.ScriptCommand = []string{"/bin/sh"}
	res := execTool(t, b, "browser_script", map[string]any{
		"skill": "slow-skill", "script": "run_flow.py", "timeout_s": 1, "tab_id": 501,
	})
	if res["ok"] != false || res["timeout"] != true {
		t.Fatalf("result=%+v", res)
	}
}

func TestScriptNonJSONOutputIsReported(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "loud-skill", "#!/bin/sh\nprintf 'not json at all'\n")
	fake := newL3Fake()
	b, _ := fake.wired(t)
	b.cfg.SkillDirs = []string{root}
	b.cfg.ScriptCommand = []string{"/bin/sh"}
	res := execTool(t, b, "browser_script", map[string]any{"skill": "loud-skill", "script": "run_flow.py", "tab_id": 501})
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "non-JSON output") {
		t.Fatalf("result=%+v", res)
	}
}

// ── surface coverage ────────────────────────────────────────────────────────

func TestAllActiveUpstreamToolsAreRegistered(t *testing.T) {
	active := []string{
		"browser_setup", "browser_status", "browser_stop",
		"browser_tabs", "browser_open", "browser_close", "browser_activate_tab",
		"browser_navigate", "browser_reload",
		"browser_screenshot", "browser_shadow_query", "browser_snapshot", "browser_console", "browser_html",
		"browser_interact", "browser_select",
		"browser_evaluate", "browser_get_text", "browser_resize", "browser_upload", "browser_dialog_respond",
		"browser_script",
	}
	if len(active) != 22 {
		t.Fatalf("active tool count drifted: %d", len(active))
	}
	for _, name := range active {
		if _, ok := commandTable[name]; !ok {
			t.Errorf("active tool %s is not registered", name)
		}
		if _, ok := bridgeCommandTable[name]; ok {
			t.Errorf("%s must not be a control command", name)
		}
	}
	supported := SupportedCommands()
	if len(supported) != len(canonicalCommands) {
		t.Fatalf("SupportedCommands=%d canonical=%d", len(supported), len(canonicalCommands))
	}
}

func TestExecuteRoutesNewToolsToTheRightWireMessages(t *testing.T) {
	fake := newL3Fake()
	b, ext := fake.wired(t)
	if _, err := b.Execute(context.Background(), "", "browser_status", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), "", "browser_get_text", map[string]any{"selector": "#x", "tab_id": 501}); err != nil {
		t.Fatal(err)
	}
	// Every wire type the fake saw must be one the shipped extension answers.
	allowed := map[string]bool{
		"cdp.attach": true, "cdp.detach": true, "cdp": true, "tab.audit": true,
		"tab.list": true, "tab.create": true, "tab.close": true, "tab.activate": true,
		"context.create": true, "context.destroy": true,
	}
	for _, cmd := range ext.commands() {
		if !allowed[cmd.Type] {
			t.Errorf("unexpected wire type %q", cmd.Type)
		}
	}
}

func toFloatOr(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := toFloat(v)
	if !ok {
		t.Fatalf("not a float: %v (%T)", v, v)
	}
	return f
}

func toIntOr(t *testing.T, v any) int {
	t.Helper()
	n, ok := toInt(v)
	if !ok {
		t.Fatalf("not an int: %v (%T)", v, v)
	}
	return n
}

var _ = json.Marshal

func asStrings(v any) []string {
	switch rows := v.(type) {
	case []string:
		return rows
	case []any:
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			if s, ok := row.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

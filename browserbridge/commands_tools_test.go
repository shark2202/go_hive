package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func scriptedCDP(cmd fakeCommand) (any, string) {
	switch cmd.Type {
	case "cdp.attach":
		return map[string]any{"ok": true, "attached": true}, ""
	case "cdp.detach":
		return map[string]any{"ok": true}, ""
	case "cdp":
		method, _ := cmd.Params["method"].(string)
		inner, _ := cmd.Params["params"].(map[string]any)
		switch method {
		case "Page.enable", "Accessibility.enable", "Runtime.enable":
			return map[string]any{}, ""
		case "Page.navigate":
			return map[string]any{"frameId": "F1", "loaderId": "L1"}, ""
		case "Page.reload":
			return map[string]any{}, ""
		case "Page.getLayoutMetrics":
			return map[string]any{"contentSize": map[string]any{"width": 800, "height": 600}}, ""
		case "Page.captureScreenshot":
			return map[string]any{"data": "aGVsbG8="}, ""
		case "Runtime.evaluate":
			expr, _ := inner["expression"].(string)
			switch {
			case strings.Contains(expr, "document.readyState"):
				return map[string]any{"result": map[string]any{"type": "string", "value": "complete"}}, ""
			case strings.Contains(expr, "window.location.href"):
				return map[string]any{"result": map[string]any{"type": "string", "value": "https://example.com/page"}}, ""
			case strings.Contains(expr, "document.title"):
				return map[string]any{"result": map[string]any{"type": "string", "value": "Example"}}, ""
			case strings.Contains(expr, "innerWidth"):
				return map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"x": 400.0, "y": 300.0}}}, ""
			case strings.Contains(expr, "getBoundingClientRect"):
				return map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{
					"left": 80.0, "top": 40.0, "x": 100.0, "y": 50.0, "w": 40.0, "h": 20.0,
				}}}, ""
			default:
				return map[string]any{"result": map[string]any{"type": "string", "value": "hi"}}, ""
			}
		case "Accessibility.getFullAXTree":
			return map[string]any{"nodes": []any{
				map[string]any{"nodeId": "1", "ignored": false, "role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Doc"}, "childIds": []any{"2"}},
				map[string]any{"nodeId": "2", "ignored": false, "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Go"}, "childIds": []any{}},
			}}, ""
		case "Input.dispatchMouseEvent", "Input.insertText", "Input.dispatchKeyEvent":
			return map[string]any{}, ""
		}
		return map[string]any{}, ""
	case "tab.create":
		return map[string]any{"tabId": 77}, ""
	case "tab.close", "tab.activate":
		return map[string]any{"ok": true}, ""
	case "tab.list":
		return map[string]any{"tabs": []any{map[string]any{"id": 11, "url": "https://example.com/", "title": "Example", "groupId": -1}}}, ""
	}
	return map[string]any{"ok": true}, ""
}

func newToolBridge(t *testing.T) (*Bridge, *fakeExtension) {
	t.Helper()
	b := newTestBridge(t, Config{CommandTimeout: 5 * time.Second})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), scriptedCDP)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	return b, f
}

func mustMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", v)
	}
	return m
}

func TestBrowserTabsToolShape(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "", "browser_tabs", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["total"].(int) != 1 || m["activeTabId"] != nil {
		t.Fatalf("shape=%+v", m)
	}
	if len(m["tabs"].([]any)) != 1 {
		t.Fatalf("tabs=%+v", m["tabs"])
	}
	if f.count("tab.list") != 1 {
		t.Fatalf("tab.list count=%d", f.count("tab.list"))
	}
}

func TestBrowserOpen(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "", "browser_open", map[string]any{
		"url": "https://example.com/page", "browser_profile": "alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || toIntMust(t, m["tabId"]) != 77 || m["browser_profile"] != "alpha" {
		t.Fatalf("shape=%+v", m)
	}
	create := f.waitForCommand("tab.create", time.Second)
	if create.Params["url"] != "https://example.com/page" {
		t.Fatalf("tab.create params=%+v", create.Params)
	}
	if f.count("tab.activate") != 1 {
		t.Fatal("foreground open did not activate the tab")
	}
	if f.count("cdp") == 0 {
		t.Fatal("open did not navigate/wait via CDP")
	}
}

func TestBrowserOpenBackgroundSkipsActivate(t *testing.T) {
	b, f := newToolBridge(t)
	_, err := b.Execute(context.Background(), "alpha", "browser_open", map[string]any{
		"url": "https://example.com/page", "background": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.count("tab.activate") != 0 {
		t.Fatal("background open activated the tab")
	}
}

func TestBrowserCloseAndActivate(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_close", map[string]any{"tab_id": 11})
	if err != nil {
		t.Fatal(err)
	}
	if mustMap(t, res)["closed"] != 11 {
		t.Fatalf("close result=%+v", res)
	}
	if f.waitForCommand("tab.close", time.Second).Params["tabId"].(float64) != 11 {
		last, _ := f.lastCommand()
		t.Fatalf("tab.close params=%+v", last.Params)
	}
	res, err = b.Execute(context.Background(), "alpha", "browser_activate_tab", map[string]any{"tab_id": 11})
	if err != nil {
		t.Fatal(err)
	}
	if mustMap(t, res)["ok"] != true {
		t.Fatalf("activate=%+v", res)
	}
}

func TestBrowserNavigate(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_navigate", map[string]any{
		"url": "https://example.com/page", "tab_id": 11, "wait_until": "load",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["url"] != "https://example.com/page" || m["title"] != "Example" {
		t.Fatalf("navigate=%+v", m)
	}
	var nav map[string]any
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Page.navigate" {
			nav, _ = cmd.Params["params"].(map[string]any)
		}
	}
	if nav == nil || nav["url"] != "https://example.com/page" {
		t.Fatalf("Page.navigate params=%+v", nav)
	}
}

func TestBrowserClickDoubleClick(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_click", map[string]any{
		"selector": "#go", "tab_id": 11, "double_click": true, "auto_snapshot_mode": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["x"].(float64) != 100 || m["y"].(float64) != 50 {
		t.Fatalf("click=%+v", m)
	}
	var presses []map[string]any
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Input.dispatchMouseEvent" {
			inner, _ := cmd.Params["params"].(map[string]any)
			if inner["type"] == "mousePressed" {
				presses = append(presses, inner)
			}
		}
	}
	if len(presses) != 1 || presses[0]["clickCount"].(float64) != 2 || presses[0]["button"] != "left" {
		t.Fatalf("presses=%+v", presses)
	}
	if f.countCDP("Accessibility.getFullAXTree") != 0 {
		t.Fatal("auto_snapshot_mode=off still captured a snapshot")
	}
}

func TestBrowserClickAutoSnapshot(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_click", map[string]any{
		"selector": "#go", "tab_id": 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	snap, ok := m["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("missing snapshot: %+v", m)
	}
	if snap["ok"] != true || !strings.Contains(snap["tree"].(string), `button "Go"`) {
		t.Fatalf("snapshot=%+v", snap)
	}
	if f.countCDP("Accessibility.getFullAXTree") == 0 {
		t.Fatal("no AX tree call")
	}
}

func TestBrowserTypeInsertText(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_type", map[string]any{
		"selector": "#q", "text": "hello", "tab_id": 11, "auto_snapshot_mode": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["length"].(int) != 5 {
		t.Fatalf("type=%+v", m)
	}
	inserts := 0
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Input.insertText" {
			inner, _ := cmd.Params["params"].(map[string]any)
			if inner["text"] != "hello" {
				t.Fatalf("insertText=%+v", inner)
			}
			inserts++
		}
	}
	if inserts != 1 {
		t.Fatalf("insertText calls=%d, want 1 (insert_text path)", inserts)
	}
}

func TestBrowserTypeFocused(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_type_focused", map[string]any{
		"text": "abc", "tab_id": 11, "auto_snapshot_mode": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mustMap(t, res)["length"].(int) != 3 {
		t.Fatalf("result=%+v", res)
	}
	if f.count("Input.dispatchMouseEvent") != 0 {
		t.Fatal("type_focused dispatched a pointer click")
	}
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Input.insertText" {
			return
		}
	}
	t.Fatal("no insertText call")
}

func TestBrowserPressModifiers(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_press", map[string]any{
		"key": "Enter", "tab_id": 11, "modifiers": []any{"shift"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mustMap(t, res)["action"] != "press" {
		t.Fatalf("press=%+v", res)
	}
	var keyDown map[string]any
	ups := 0
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Input.dispatchKeyEvent" {
			inner, _ := cmd.Params["params"].(map[string]any)
			switch inner["type"] {
			case "keyDown", "rawKeyDown":
				keyDown = inner
			case "keyUp":
				ups++
			}
		}
	}
	if keyDown["key"] != "Enter" || keyDown["modifiers"].(float64) != 8 || keyDown["text"] != "\r" {
		t.Fatalf("keyDown=%+v", keyDown)
	}
	if ups != 1 {
		t.Fatalf("keyUp count=%d", ups)
	}
}

func TestBrowserScrollWheel(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_scroll", map[string]any{
		"direction": "up", "amount": 700, "tab_id": 11, "auto_snapshot_mode": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mustMap(t, res)["action"] != "scroll" {
		t.Fatalf("scroll=%+v", res)
	}
	var wheel map[string]any
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Input.dispatchMouseEvent" {
			inner, _ := cmd.Params["params"].(map[string]any)
			if inner["type"] == "mouseWheel" {
				wheel = inner
			}
		}
	}
	if wheel == nil || wheel["deltaY"].(float64) != -700 {
		t.Fatalf("wheel=%+v", wheel)
	}
}

func TestBrowserSnapshotMode(t *testing.T) {
	b, _ := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_snapshot", map[string]any{
		"tab_id": 11, "mode": "interactive",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || !strings.Contains(m["tree"].(string), `button "Go"`) {
		t.Fatalf("snapshot=%+v", m)
	}
	if m["nodes"].(int) != 1 {
		t.Fatalf("nodes=%v", m["nodes"])
	}
}

func TestBrowserScreenshot(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_screenshot", map[string]any{
		"intent": "see the page", "tab_id": 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["data"] != "aGVsbG8=" || m["mimeType"] != "image/png" {
		t.Fatalf("screenshot=%+v", m)
	}
	if f.countCDP("Page.captureScreenshot") == 0 {
		t.Fatal("captureScreenshot was not called")
	}
}

func TestBrowserScreenshotIntentOptional(t *testing.T) {
	// Upstream's CLI path calls render_screenshot(intent=None), so a missing
	// intent must still capture; the retired MCP tool schema was the only
	// place that required it.
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_screenshot", map[string]any{"tab_id": 11})
	if err != nil {
		t.Fatalf("screenshot without intent: %v", err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["data"] != "aGVsbG8=" {
		t.Fatalf("screenshot=%+v", m)
	}
	if f.countCDP("Page.captureScreenshot") == 0 {
		t.Fatal("captureScreenshot was not called")
	}
}

func TestBrowserEvaluate(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_evaluate", map[string]any{
		"script": "document.title", "tab_id": 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["result"] != "Example" {
		t.Fatalf("evaluate=%+v", m)
	}
	var expr string
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Runtime.evaluate" {
			inner, _ := cmd.Params["params"].(map[string]any)
			expr, _ = inner["expression"].(string)
		}
	}
	if !strings.Contains(expr, "return (document.title)") {
		t.Fatalf("expression=%q", expr)
	}
}

func TestBrowserReload(t *testing.T) {
	b, f := newToolBridge(t)
	res, err := b.Execute(context.Background(), "alpha", "browser_reload", map[string]any{"tab_id": 11})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != true || m["action"] != "reload" || m["url"] != "https://example.com/page" {
		t.Fatalf("reload=%+v", m)
	}
	if f.count("cdp") == 0 {
		t.Fatal("reload never reached CDP")
	}
}

func TestTabIDIsRequiredWhenNoContext(t *testing.T) {
	b, _ := newToolBridge(t)
	_, err := b.Execute(context.Background(), "alpha", "browser_click", map[string]any{"selector": "#go"})
	if err == nil || AsError(err).Code != CodeBadRequest {
		t.Fatalf("want bad_request, got %v", err)
	}
}

func TestExtensionErrorIsStructured(t *testing.T) {
	b := newTestBridge(t, Config{CommandTimeout: 2 * time.Second})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), func(cmd fakeCommand) (any, string) {
		if cmd.Type == "tab.list" {
			return nil, "extension exploded"
		}
		return map[string]any{"ok": true}, ""
	})
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	_, err := b.Execute(context.Background(), "alpha", "browser_tabs", nil)
	if err == nil || AsError(err).Code != CodeExtensionError {
		t.Fatalf("want extension_error, got %v", err)
	}
	if !strings.Contains(AsError(err).Message, "extension exploded") {
		t.Fatalf("message=%q", AsError(err).Message)
	}
	_ = f
}

func TestCdpAttachReusedAcrossCommands(t *testing.T) {
	b, f := newToolBridge(t)
	for i := 0; i < 2; i++ {
		if _, err := b.Execute(context.Background(), "alpha", "browser_snapshot", map[string]any{"tab_id": 11}); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("cdp.attach") != 1 {
		t.Fatalf("cdp.attach count=%d, want 1", f.count("cdp.attach"))
	}
}

func TestEvalExceptionReturnsToolFailure(t *testing.T) {
	b := newTestBridge(t, Config{CommandTimeout: 2 * time.Second})
	handler := func(cmd fakeCommand) (any, string) {
		if cmd.Type == "cdp" {
			method, _ := cmd.Params["method"].(string)
			if method == "Runtime.evaluate" {
				return map[string]any{"exceptionDetails": map[string]any{
					"text": "Uncaught", "exception": map[string]any{"description": "ReferenceError: nope is not defined"},
				}}, ""
			}
		}
		return scriptedCDP(cmd)
	}
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), handler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	res, err := b.Execute(context.Background(), "alpha", "browser_evaluate", map[string]any{"script": "nope", "tab_id": 11})
	if err != nil {
		t.Fatal(err)
	}
	m := mustMap(t, res)
	if m["ok"] != false || !strings.Contains(fmt.Sprint(m["error"]), "ReferenceError") {
		t.Fatalf("eval=%+v", m)
	}
}

func toIntMust(t *testing.T, v any) int {
	t.Helper()
	n, ok := toInt(v)
	if !ok {
		t.Fatalf("not an int: %v (%T)", v, v)
	}
	return n
}

var _ = json.Marshal

package browserbridge

// history_http_test.go covers the surface added with the control plane's
// remaining upstream methods: reveal_tab (HTTP route), history navigation,
// get_attribute, cdp_detach and POST /shutdown.

import (
	"context"
	"testing"
	"time"
)

// cdpAnsweringHandler answers CDP commands with a scripted Runtime.evaluate
// value and lets other extension commands fall through to the default.
func cdpAnsweringHandler(values map[string]any, errorsByType map[string]string) func(fakeCommand) (any, string) {
	return func(cmd fakeCommand) (any, string) {
		if msg, ok := errorsByType[cmd.Type]; ok {
			return nil, msg
		}
		if cmd.Type == "cdp" {
			method, _ := cmd.Params["method"].(string)
			if method == "Runtime.evaluate" {
				params, _ := cmd.Params["params"].(map[string]any)
				expr, _ := params["expression"].(string)
				if value, ok := values[expr]; ok {
					return map[string]any{"result": map[string]any{"type": "string", "value": value}}, ""
				}
			}
			return map[string]any{}, ""
		}
		return map[string]any{"ok": true}, ""
	}
}

func TestHTTPRevealTab(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	ext := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")

	code, body := httpJSON(t, "POST", "http://"+b.HTTPAddr()+"/tabs/11/reveal", "")
	if code != 200 || body["ok"] != true {
		t.Fatalf("reveal=%d %+v", code, body)
	}
	if id, _ := toInt(body["tabId"]); id != 11 {
		t.Fatalf("reveal tabId = %v", body["tabId"])
	}
	if cmd, ok := ext.lastCommand(); !ok || cmd.Type != "tab.reveal" {
		t.Fatalf("extension did not receive tab.reveal: %+v", cmd)
	}

	// A bogus id is a clean 404, not a bridge error.
	code, _ = httpJSON(t, "POST", "http://"+b.HTTPAddr()+"/tabs/0/reveal", "")
	if code != 404 {
		t.Fatalf("reveal of tab 0 = %d, want 404", code)
	}
}

func TestRevealTabFallsBackToActivate(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	ext := startFakeExtension(t, b.WSAddr(), helloFor("alpha"),
		cdpAnsweringHandler(nil, map[string]string{"tab.reveal": "Unknown command: tab.reveal"}))
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")

	result, err := b.RevealTab(context.Background(), 11)
	if err != nil {
		t.Fatalf("RevealTab fallback: %v", err)
	}
	if fallback, _ := result["fallback"].(string); fallback != "activate" {
		t.Fatalf("fallback tag = %v, want activate (%+v)", result["fallback"], result)
	}
	if cmd, ok := ext.lastCommand(); !ok || cmd.Type != "tab.activate" {
		t.Fatalf("fallback did not send tab.activate: %+v", cmd)
	}
}

func TestHistoryNavigationAndAttribute(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	handler := cdpAnsweringHandler(map[string]any{
		"window.location.href": "https://example.com/back",
		"(function(){ const el = document.querySelector(\"#x\"); return el ? el.getAttribute(\"href\") : null; })()": "/next",
	}, nil)
	ext := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), handler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")

	back, err := b.GoBackHistory(context.Background(), 11)
	if err != nil {
		t.Fatalf("GoBackHistory: %v", err)
	}
	if back["action"] != "back" || back["url"] != "https://example.com/back" {
		t.Fatalf("go_back = %+v", back)
	}
	if got := ext.countCDP("Runtime.evaluate"); got < 2 {
		t.Fatalf("go_back did not issue history.back() + URL read (%d Runtime.evaluate)", got)
	}

	forward, err := b.GoForwardHistory(context.Background(), 11)
	if err != nil {
		t.Fatalf("GoForwardHistory: %v", err)
	}
	if forward["action"] != "forward" {
		t.Fatalf("go_forward = %+v", forward)
	}

	attr, err := b.GetAttribute(context.Background(), 11, "#x", "href", 2*time.Second)
	if err != nil {
		t.Fatalf("GetAttribute: %v", err)
	}
	if attr["ok"] != true || attr["value"] != "/next" {
		t.Fatalf("get_attribute = %+v", attr)
	}

	detached, err := b.CDPDetach(context.Background(), 11)
	if err != nil {
		t.Fatalf("CDPDetach: %v", err)
	}
	if detached["ok"] != true {
		t.Fatalf("cdp_detach = %+v", detached)
	}
	if cmd, ok := ext.lastCommand(); !ok || cmd.Type != "cdp.detach" {
		t.Fatalf("extension did not receive cdp.detach: %+v", cmd)
	}
}

func TestHistoryNavigationSurfacesPendingDialog(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	ext := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), cdpAnsweringHandler(nil, nil))
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")

	// A beforeunload dialog lands on the bridge through the cdp_event
	// channel; go_back must report it instead of pretending it navigated.
	b.handleCDPEvent("alpha", 11, "Page.javascriptDialogOpening", map[string]any{
		"type": "beforeunload", "message": "Leave site?", "url": "https://example.com/",
	})
	result, err := b.GoBackHistory(context.Background(), 11)
	if err != nil {
		t.Fatalf("GoBackHistory: %v", err)
	}
	if result["ok"] != false || result["action"] != "back" {
		t.Fatalf("pending-dialog go_back = %+v", result)
	}
	if _, ok := result["pending_dialog"].(map[string]any); !ok {
		t.Fatalf("go_back did not carry pending_dialog: %+v", result)
	}
	_ = ext
}

func TestHTTPShutdownInvokesHook(t *testing.T) {
	fired := make(chan struct{}, 1)
	b := newTestBridge(t, Config{
		SweepDisabled:   true,
		ShutdownRequest: func() { fired <- struct{}{} },
	})
	code, body := httpJSON(t, "POST", "http://"+b.HTTPAddr()+"/shutdown", "")
	if code != 202 || body["ok"] != true {
		t.Fatalf("shutdown = %d %+v", code, body)
	}
	if message, _ := body["message"].(string); message != "Bridge shutdown initiated." {
		t.Fatalf("shutdown message = %v", body["message"])
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("POST /shutdown did not invoke ShutdownRequest")
	}
}

func TestHTTPShutdownWithoutHookIsAccepted(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	code, body := httpJSON(t, "POST", "http://"+b.HTTPAddr()+"/shutdown", "")
	if code != 202 || body["ok"] != true {
		t.Fatalf("shutdown without hook = %d %+v", code, body)
	}
	// The bridge keeps running: only the supervisor wires the exit.
	if !b.ControlListening() {
		t.Fatal("a shutdown request without a hook must not tear the bridge down")
	}
}

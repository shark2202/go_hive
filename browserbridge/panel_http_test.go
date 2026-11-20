package browserbridge

// panel_http_test.go covers the two additive panel endpoints the Go server's
// proxies read: GET /tabs (every tab the extension sees) and POST
// /contexts/close (profile or all).
//
// Parity: framework/server/routes_workers.py::handle_browser_tabs
// Parity: framework/server/routes_workers.py::handle_close_browser_context

import (
	"context"
	"testing"
	"time"
)

func TestPanelTabsHTTPReturnsEveryVisibleTab(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	h.setGroup(501, 101)
	h.setGroup(777, -1)
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	code, payload := httpJSON(t, "GET", "http://"+b.HTTPAddr()+"/tabs", "")
	if code != 200 || payload["ok"] != true {
		t.Fatalf("tabs = %d %+v", code, payload)
	}
	tabs, _ := payload["tabs"].([]any)
	if len(tabs) != 2 {
		t.Fatalf("tabs = %+v, want both grouped and ungrouped rows", tabs)
	}
	seen := map[int]bool{}
	for _, row := range tabs {
		entry, _ := row.(map[string]any)
		seen[paramInt(entry, "id", 0)] = true
	}
	if !seen[501] || !seen[777] {
		t.Fatalf("tabs missing ids: %+v", seen)
	}
}

func TestPanelTabsHTTPWithoutExtensionIsNotConnected(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	code, payload := httpJSON(t, "GET", "http://"+b.HTTPAddr()+"/tabs", "")
	if code != 503 || payload["code"] != CodeNotConnected {
		t.Fatalf("tabs without extension = %d %+v", code, payload)
	}
	tabs, _ := payload["tabs"].([]any)
	if tabs == nil || len(tabs) != 0 {
		t.Fatalf("tabs = %#v, want empty array", payload["tabs"])
	}
}

func TestPanelCloseContextsHTTPClosesProfileAndAll(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-1", "display_name": "Queen One", "browser_profile": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	base := "http://" + b.HTTPAddr()

	code, payload := httpJSON(t, "POST", base+"/contexts/close", `{"profile":"queen-1"}`)
	if code != 200 {
		t.Fatalf("close profile = %d %+v", code, payload)
	}
	closed, _ := payload["closed"].([]any)
	if len(closed) != 1 {
		t.Fatalf("closed = %+v", payload)
	}
	entry, _ := closed[0].(map[string]any)
	if entry["profile"] != "queen-1" || entry["ok"] != true || entry["status"] != "stopped" {
		t.Fatalf("close entry = %+v", entry)
	}
	if destroy, _, _ := h.counts(); destroy != 1 {
		t.Fatalf("extension destroy calls = %d, want 1", destroy)
	}
	if len(b.ListContexts(context.Background())) != 0 {
		t.Fatal("registry entry survived the close")
	}

	// "all" with nothing registered is a clean empty list, not an error.
	code, payload = httpJSON(t, "POST", base+"/contexts/close", `{"all":true}`)
	closed, _ = payload["closed"].([]any)
	if code != 200 || closed == nil || len(closed) != 0 {
		t.Fatalf("close all = %d %+v", code, payload)
	}

	// Neither field is the upstream 400.
	code, payload = httpJSON(t, "POST", base+"/contexts/close", `{}`)
	if code != 400 {
		t.Fatalf("close without profile = %d %+v", code, payload)
	}
}

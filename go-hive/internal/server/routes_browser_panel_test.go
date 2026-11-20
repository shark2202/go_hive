package server

// routes_browser_panel_test.go pins the three panel proxies: real response and
// status passthrough when the bridge answers, the explicit 503 not_connected
// shape when it does not, and verbatim forwarding of the close body.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func browserPanelMux(deps BrowserPanelDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterBrowserPanelRoutes(mux, deps)
	return mux
}

// Parity-exempt: upstream has no test driving GET /api/browser/contexts
// (routes_workers.py's browser routes are tested only through the frontend
// panel). This pins the Go port's proxy contract: the bridge's own body and
// status come through untouched.
func TestBrowserPanelContextsProxyPassesBridgeResponse(t *testing.T) {
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/contexts" {
			t.Errorf("bridge saw %s %s, want GET /contexts", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true,"contexts":[{"profile":"queen-1","groupId":101}]}`))
	}))
	defer bridge.Close()

	mux := browserPanelMux(BrowserPanelDeps{BaseURL: func() string { return bridge.URL }})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser/contexts", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("contexts status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content type = %q", contentType)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %q)", err, recorder.Body.String())
	}
	contexts, _ := body["contexts"].([]any)
	if body["ok"] != true || len(contexts) != 1 {
		t.Fatalf("bridge response not passed through: %#v", body)
	}
	row, _ := contexts[0].(map[string]any)
	if row["profile"] != "queen-1" {
		t.Fatalf("context row = %#v", row)
	}
}

// Parity-exempt: Go port's no-bridge contract (503 + not_connected) is a
// deliberate deviation from upstream's 200/available:false, per the browser
// lane handoff; no upstream test covers it.
func TestBrowserPanelWithoutBridgeAnswers503NotConnected(t *testing.T) {
	// Reserve a port, then close it so the proxy dials a dead address.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := "http://" + listener.Addr().String()
	_ = listener.Close()

	mux := browserPanelMux(BrowserPanelDeps{BaseURL: func() string { return dead }})
	for _, tc := range []struct {
		method     string
		target     string
		collection string
	}{
		{http.MethodGet, "/api/browser/contexts", "contexts"},
		{http.MethodGet, "/api/browser/tabs", "tabs"},
		{http.MethodPost, "/api/browser/contexts/close", "closed"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(tc.method, tc.target, strings.NewReader(`{"all":true}`))
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d, want 503 (body %s)", tc.method, tc.target, recorder.Code, recorder.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v", tc.target, err)
		}
		if body["available"] != false || body["error"] != "not_connected" {
			t.Fatalf("%s body = %#v", tc.target, body)
		}
		collection, _ := body[tc.collection].([]any)
		if collection == nil || len(collection) != 0 {
			t.Fatalf("%s %s = %#v, want empty array", tc.target, tc.collection, body[tc.collection])
		}
	}
}

// Parity-exempt: upstream has no test for POST /api/browser/contexts/close;
// this pins the proxy's error passthrough (bridge 404 stays 404) and that the
// request body reaches the bridge unchanged.
func TestBrowserPanelCloseForwardsBodyAndBridgeStatus(t *testing.T) {
	var seenBody string
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/contexts/close" {
			t.Errorf("bridge saw %s %s, want POST /contexts/close", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		seenBody = string(raw)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":"not found"}`))
	}))
	defer bridge.Close()

	mux := browserPanelMux(BrowserPanelDeps{BaseURL: func() string { return bridge.URL }})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/browser/contexts/close", strings.NewReader(`{"profile":"queen-1"}`)))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("close status = %d, want the bridge's 404 (body %s)", recorder.Code, recorder.Body.String())
	}
	if seenBody != `{"profile":"queen-1"}` {
		t.Fatalf("bridge received body %q", seenBody)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "not found" {
		t.Fatalf("bridge error body not passed through: %#v", body)
	}
}

// Parity-exempt: the "old bridge without /tabs" shape (bridge answers 404) must
// pass through as 404 — the proxy may not upgrade it to 200 or 503.
func TestBrowserPanelTabsPassesThroughBridge404(t *testing.T) {
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":"not found"}`))
	}))
	defer bridge.Close()

	mux := browserPanelMux(BrowserPanelDeps{BaseURL: func() string { return bridge.URL }})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser/tabs", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("tabs status = %d, want the bridge's 404 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "not found") {
		t.Fatalf("bridge body not passed through: %s", recorder.Body.String())
	}
}

// Parity-exempt: Go-only address resolution seam; HIVE_BROWSERBRIDGE_HTTP is
// the supervisor's override and HIVE_BRIDGE_PORT the legacy base.
func TestDefaultBrowserPanelBaseHonoursEnvironment(t *testing.T) {
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", "")
	t.Setenv("HIVE_BRIDGE_PORT", "")
	if got := defaultBrowserPanelBase(); got != "http://127.0.0.1:14830" {
		t.Fatalf("default base = %q", got)
	}
	t.Setenv("HIVE_BRIDGE_PORT", "17829")
	if got := defaultBrowserPanelBase(); got != "http://127.0.0.1:17830" {
		t.Fatalf("HIVE_BRIDGE_PORT base = %q", got)
	}
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", "127.0.0.1:19999")
	if got := defaultBrowserPanelBase(); got != "http://127.0.0.1:19999" {
		t.Fatalf("HIVE_BROWSERBRIDGE_HTTP base = %q", got)
	}
}

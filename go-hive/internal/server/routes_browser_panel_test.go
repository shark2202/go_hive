package server

// routes_browser_panel_test.go pins the three panel routes against the
// upstream routes_workers.py bodies: the {available,count,contexts} registry
// shape, the {available,count,live_count,discarded_count,hive_group_count,tabs}
// diagnostic shape with upstream's isHiveGroup VALUE (always false — see the
// handle_browser_tabs test), the 200/available:false no-bridge envelope
// (upstream has no 503 path here), and the close route's {closed:[…]} / 400
// {"error": …} contract.

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

// panelBridge stands in for the bridge's local agent API, answering each panel
// path with a canned body + status.
type panelBridge struct {
	contexts    string
	tabs        string
	closeBody   string
	closeStatus int
	closeSeen   string
	server      *httptest.Server
}

func newPanelBridge(t *testing.T, pb *panelBridge) *panelBridge {
	t.Helper()
	pb.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/contexts":
			if pb.contexts == "" {
				_, _ = w.Write([]byte(`{"ok":true,"contexts":[]}`))
				return
			}
			_, _ = w.Write([]byte(pb.contexts))
		case r.Method == http.MethodGet && r.URL.Path == "/tabs":
			if pb.tabs == "" {
				_, _ = w.Write([]byte(`{"ok":true,"tabs":[]}`))
				return
			}
			if strings.HasPrefix(pb.tabs, "!503") {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(strings.TrimPrefix(pb.tabs, "!503")))
				return
			}
			_, _ = w.Write([]byte(pb.tabs))
		case r.Method == http.MethodPost && r.URL.Path == "/contexts/close":
			raw, _ := io.ReadAll(r.Body)
			pb.closeSeen = string(raw)
			status := pb.closeStatus
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			if pb.closeBody == "" {
				_, _ = w.Write([]byte(`{"closed":[]}`))
				return
			}
			_, _ = w.Write([]byte(pb.closeBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(pb.server.Close)
	return pb
}

func (pb *panelBridge) deps() BrowserPanelDeps {
	return BrowserPanelDeps{BaseURL: func() string { return pb.server.URL }}
}

func panelJSON(t *testing.T, mux *http.ServeMux, method, target, body string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	mux.ServeHTTP(recorder, httptest.NewRequest(method, target, reader))
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s %s: decode: %v (body %q)", method, target, err, recorder.Body.String())
	}
	return recorder.Code, decoded
}

// Parity: framework/server/routes_workers.py::handle_browser_contexts
//
// Upstream body: {available, count, contexts:[{profile, group_id, name,
// tab_count, active_tab_id}]}.
func TestBrowserPanelContextsMatchesUpstreamShape(t *testing.T) {
	bridge := newPanelBridge(t, &panelBridge{contexts: `{"ok":true,"contexts":[
		{"profile":"queen-1","groupId":101,"activeTab":22,"name":"Queen One","tabs":[{"id":11},{"id":22}]},
		{"profile":"queen-2","groupId":null,"activeTab":null,"name":"","tabs":[]}
	]}`})

	status, body := panelJSON(t, browserPanelMux(bridge.deps()), http.MethodGet, "/api/browser/contexts", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	if body["available"] != true || body["count"] != float64(2) {
		t.Fatalf("envelope = %#v", body)
	}
	contexts, _ := body["contexts"].([]any)
	if len(contexts) != 2 {
		t.Fatalf("contexts = %#v", body["contexts"])
	}
	first, _ := contexts[0].(map[string]any)
	wantKeys := []string{"profile", "group_id", "name", "tab_count", "active_tab_id"}
	for _, key := range wantKeys {
		if _, present := first[key]; !present {
			t.Fatalf("context row missing %q: %#v", key, first)
		}
	}
	if first["profile"] != "queen-1" || first["group_id"] != float64(101) ||
		first["name"] != "Queen One" || first["tab_count"] != float64(2) || first["active_tab_id"] != float64(22) {
		t.Fatalf("context row = %#v", first)
	}
	second, _ := contexts[1].(map[string]any)
	if second["group_id"] != nil || second["active_tab_id"] != nil || second["tab_count"] != float64(0) {
		t.Fatalf("soft-pruned context row = %#v", second)
	}
}

// Parity: framework/server/routes_workers.py::handle_browser_tabs
//
// Upstream body: {available, count, live_count, discarded_count,
// hive_group_count, tabs:[{id, windowId, groupId, isHiveGroup, url, title,
// discarded, active, audible, pinned}]}.
func TestBrowserPanelTabsMatchesUpstreamShape(t *testing.T) {
	bridge := newPanelBridge(t, &panelBridge{
		contexts: `{"ok":true,"contexts":[
			{"profile":"queen-1","groupId":101,"activeTab":11,"name":"One","tabs":[{"id":11}]},
			{"profile":"queen-2","groupId":202,"activeTab":null,"name":"Two","tabs":[]}
		]}`,
		tabs: `{"ok":true,"tabs":[
			{"id":11,"url":"https://a.example","title":"A","groupId":101},
			{"id":12,"url":"https://b.example","title":"B","groupId":-1},
			{"id":13,"url":"https://c.example","title":"C"},
			{"id":14,"url":"https://d.example","title":"D","groupId":303,"windowId":7,
			 "discarded":true,"active":true,"audible":true,"pinned":true}
		]}`})

	status, body := panelJSON(t, browserPanelMux(bridge.deps()), http.MethodGet, "/api/browser/tabs", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	if body["available"] != true || body["count"] != float64(4) || body["live_count"] != float64(3) ||
		body["discarded_count"] != float64(1) || body["hive_group_count"] != float64(0) {
		t.Fatalf("envelope = %#v", body)
	}
	tabs, _ := body["tabs"].([]any)
	if len(tabs) != 4 {
		t.Fatalf("tabs = %#v", body["tabs"])
	}
	rowOf := func(index int) map[string]any {
		row, _ := tabs[index].(map[string]any)
		return row
	}
	// Upstream's hive-group set is always empty: it reads ctx.get("groupId")
	// from list_active_contexts() rows keyed "group_id"
	// (routes_workers.py:341-360), so a tab inside a registered Hive group
	// keeps its groupId but is still isHiveGroup=false, and hive_group_count
	// stays 0. The route mirrors that value, not the code comment's intent
	// (dual-run evidence: docs/fixtures/browser-final-parity/).
	if row := rowOf(0); row["isHiveGroup"] != false || row["groupId"] != float64(101) ||
		row["windowId"] != nil || row["discarded"] != false || row["active"] != false ||
		row["audible"] != false || row["pinned"] != false {
		t.Fatalf("registered-group tab row = %#v", row)
	}
	// Chrome's -1 group id means "no group": null in the body, not hive.
	if row := rowOf(1); row["groupId"] != nil || row["isHiveGroup"] != false {
		t.Fatalf("-1 group row = %#v", row)
	}
	// A missing groupId is null too.
	if row := rowOf(2); row["groupId"] != nil || row["isHiveGroup"] != false {
		t.Fatalf("missing group row = %#v", row)
	}
	// A group that is not in the registry keeps its id but is not hive.
	if row := rowOf(3); row["groupId"] != float64(303) || row["isHiveGroup"] != false ||
		row["windowId"] != float64(7) || row["discarded"] != true || row["active"] != true ||
		row["audible"] != true || row["pinned"] != true {
		t.Fatalf("foreign group row = %#v", row)
	}
	for _, raw := range tabs {
		row, _ := raw.(map[string]any)
		for _, key := range []string{"id", "windowId", "groupId", "isHiveGroup", "url", "title",
			"discarded", "active", "audible", "pinned"} {
			if _, present := row[key]; !present {
				t.Fatalf("tab row missing %q: %#v", key, row)
			}
		}
	}
}

// Parity: framework/server/routes_workers.py::handle_browser_contexts (the
// ImportError branch) — upstream answers 200 with available:false and the
// collection empty when the browser tools are not usable. A bridge that is not
// listening is this port's version of that state; it must not be a 503.
func TestBrowserPanelWithoutBridgeMatchesUpstreamEnvelope(t *testing.T) {
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
		body       string
	}{
		{http.MethodGet, "/api/browser/contexts", "contexts", ""},
		{http.MethodGet, "/api/browser/tabs", "tabs", ""},
		{http.MethodPost, "/api/browser/contexts/close", "", `{"all":true}`},
	} {
		status, body := panelJSON(t, mux, tc.method, tc.target, tc.body)
		if status != http.StatusOK {
			t.Fatalf("%s %s status = %d, want 200 (body %#v)", tc.method, tc.target, status, body)
		}
		if body["available"] != false {
			t.Fatalf("%s body = %#v", tc.target, body)
		}
		if reason, _ := body["reason"].(string); !strings.Contains(reason, "not reachable") {
			t.Fatalf("%s reason = %#v", tc.target, body["reason"])
		}
		if tc.collection == "" {
			continue
		}
		collection, ok := body[tc.collection].([]any)
		if !ok || len(collection) != 0 {
			t.Fatalf("%s %s = %#v, want empty array", tc.target, tc.collection, body[tc.collection])
		}
	}
}

// Parity: framework/server/routes_workers.py::handle_browser_tabs (the
// "bridge not connected" branch) — a reachable bridge with no extension answers
// 503 not_connected; the panel route must render it as the upstream 200
// available:false envelope, not leak the bridge's status code.
func TestBrowserPanelTabsBridgeNotConnectedIsUpstreamEnvelope(t *testing.T) {
	bridge := newPanelBridge(t, &panelBridge{
		tabs: `!503{"ok":false,"code":"not_connected","error":"no browser extension is connected","tabs":[]}`,
	})
	status, body := panelJSON(t, browserPanelMux(bridge.deps()), http.MethodGet, "/api/browser/tabs", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["available"] != false || body["reason"] != "bridge not connected" {
		t.Fatalf("body = %#v", body)
	}
	if tabs, ok := body["tabs"].([]any); !ok || len(tabs) != 0 {
		t.Fatalf("tabs = %#v, want empty array", body["tabs"])
	}
}

// Parity: framework/server/routes_workers.py::handle_close_browser_context
//
// Upstream passes the request body through, returns {closed:[…]} on success and
// 400 {"error": "profile (or all=true) is required"} when neither key is given.
func TestBrowserPanelClosePassesThroughBridgeBodies(t *testing.T) {
	bridge := newPanelBridge(t, &panelBridge{
		closeBody:   `{"error":"profile (or all=true) is required"}`,
		closeStatus: http.StatusBadRequest,
	})
	mux := browserPanelMux(bridge.deps())
	status, body := panelJSON(t, mux, http.MethodPost, "/api/browser/contexts/close", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want the bridge's 400 (body %#v)", status, body)
	}
	if body["error"] != "profile (or all=true) is required" {
		t.Fatalf("bridge error not passed through: %#v", body)
	}
	if bridge.closeSeen != `{}` {
		t.Fatalf("bridge received body %q", bridge.closeSeen)
	}

	okBridge := newPanelBridge(t, &panelBridge{closeBody: `{"closed":[{"profile":"queen-1","ok":true,"status":"stopped"}]}`})
	status, body = panelJSON(t, browserPanelMux(okBridge.deps()), http.MethodPost, "/api/browser/contexts/close", `{"profile":"queen-1"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	closed, _ := body["closed"].([]any)
	if len(closed) != 1 {
		t.Fatalf("closed = %#v", body["closed"])
	}
	entry, _ := closed[0].(map[string]any)
	if entry["profile"] != "queen-1" || entry["ok"] != true {
		t.Fatalf("closed entry = %#v", entry)
	}
	if okBridge.closeSeen != `{"profile":"queen-1"}` {
		t.Fatalf("bridge received body %q", okBridge.closeSeen)
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

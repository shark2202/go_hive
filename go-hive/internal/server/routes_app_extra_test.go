package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/resources"
)

func appExtraMux(deps AppExtraRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterAppExtraRoutes(mux, deps)
	return mux
}

func getAppExtraJSON(t *testing.T, mux *http.ServeMux, target string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v (body %q)", target, err, recorder.Body.String())
	}
	return recorder.Code, body
}

// Parity-exempt: app.py has no Python test for /api/health/resources; this
// pins the pre-sample snapshot shape ResourceMonitor.snapshot returns.
func TestHealthResourcesDefaultSnapshot(t *testing.T) {
	status, body := getAppExtraJSON(t, appExtraMux(AppExtraRouteDeps{}), "/api/health/resources")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if body["available"] != false || body["verdict"] != "unknown" {
		t.Fatalf("body = %#v", body)
	}
	if body["interval_s"] != resources.IntervalSeconds() {
		t.Fatalf("interval_s = %#v", body["interval_s"])
	}
	history, ok := body["history"].([]any)
	if !ok || len(history) != 0 {
		t.Fatalf("history = %#v, want empty list", body["history"])
	}
	thresholds, ok := body["thresholds"].(map[string]any)
	if !ok {
		t.Fatalf("thresholds = %#v", body["thresholds"])
	}
	envThresholds := resources.ThresholdsFromEnv()
	if thresholds["warn_avail_frac"] != envThresholds.WarnAvailFrac || thresholds["crit_avail_frac"] != envThresholds.CritAvailFrac {
		t.Fatalf("threshold fractions = %#v, want %v", thresholds, envThresholds)
	}
	if thresholds["warn_renderers"] != float64(envThresholds.WarnRenderers) || thresholds["crit_renderers"] != float64(envThresholds.CritRenderers) {
		t.Fatalf("threshold renderers = %#v, want %v", thresholds, envThresholds)
	}
}

// Parity-exempt: no upstream test drives the ?history/?full query parsing;
// this pins the default 120, int() fallback and full-buffer signal.
func TestHealthResourcesQueryContract(t *testing.T) {
	var captured []*int
	deps := AppExtraRouteDeps{Resources: func(historyN *int) map[string]any {
		captured = append(captured, historyN)
		return map[string]any{"available": true}
	}}
	mux := appExtraMux(deps)

	cases := []struct {
		target string
		want   *int
	}{
		{"/api/health/resources", intPointer(120)},
		{"/api/health/resources?history=5", intPointer(5)},
		{"/api/health/resources?history=0", intPointer(0)},
		{"/api/health/resources?history=bad", intPointer(120)},
		{"/api/health/resources?history=", intPointer(120)},
		{"/api/health/resources?history=%2B7", intPointer(7)},
		{"/api/health/resources?history=1_0", intPointer(10)},
		{"/api/health/resources?full=1", nil},
		{"/api/health/resources?full=TRUE", nil},
		{"/api/health/resources?full=0", intPointer(120)},
	}
	for _, testCase := range cases {
		if status, _ := getAppExtraJSON(t, mux, testCase.target); status != http.StatusOK {
			t.Fatalf("%s: status = %d", testCase.target, status)
		}
		got := captured[len(captured)-1]
		if testCase.want == nil {
			if got != nil {
				t.Fatalf("%s: history = %d, want whole buffer", testCase.target, *got)
			}
			continue
		}
		if got == nil || *got != *testCase.want {
			t.Fatalf("%s: history = %v, want %d", testCase.target, got, *testCase.want)
		}
	}
}

func intPointer(value int) *int { return &value }

// Parity-exempt: app.py has no Python test for the browser status health
// classification; this pins healthy/stale/disconnected/offline.
func TestBrowserStatusHealthClassification(t *testing.T) {
	cases := []struct {
		name   string
		probe  map[string]any
		health string
	}{
		{"healthy", map[string]any{"bridge": true, "connected": true, "connections": []any{}, "last_pong_age_ms": json.Number("100")}, "healthy"},
		{"healthy without pong", map[string]any{"bridge": true, "connected": true, "connections": []any{}}, "healthy"},
		{"stale", map[string]any{"bridge": true, "connected": true, "connections": []any{}, "last_pong_age_ms": json.Number("15000")}, "stale"},
		{"disconnected", map[string]any{"bridge": true, "connected": false, "connections": []any{}}, "disconnected"},
		{"offline", map[string]any{"bridge": false, "connected": false, "connections": []any{}}, "offline"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			deps := AppExtraRouteDeps{ProbeStatus: func(string) map[string]any { return testCase.probe }}
			status, body := getAppExtraJSON(t, appExtraMux(deps), "/api/browser/status")
			if status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			if body["health"] != testCase.health {
				t.Fatalf("health = %#v, want %s", body["health"], testCase.health)
			}
			if _, present := body["active_tab"]; present {
				t.Fatalf("active_tab must be absent for an unscoped probe: %#v", body)
			}
		})
	}
}

// Parity-exempt: no upstream test drives the session-scoped active_tab; this
// pins the activeTab id match plus the active-tab fallback.
func TestBrowserStatusActiveTab(t *testing.T) {
	contexts := []map[string]any{
		{
			"profile":   "session_a",
			"activeTab": json.Number("22"),
			"tabs": []any{
				map[string]any{"id": json.Number("11"), "title": "Other", "url": "https://other.example"},
				map[string]any{"id": json.Number("22"), "title": "Docs", "url": "https://docs.example"},
			},
		},
		{
			"profile": "session_b",
			"tabs": []any{
				map[string]any{"id": json.Number("33"), "title": "Fallback", "url": "https://fallback.example", "active": true},
			},
		},
		{"profile": "session_c", "tabs": []any{map[string]any{"id": json.Number("44")}}},
	}
	deps := AppExtraRouteDeps{
		ProbeStatus: func(string) map[string]any {
			return map[string]any{"bridge": true, "connected": true, "connections": []any{}}
		},
		ProbeContext: func(string) []map[string]any { return contexts },
	}
	mux := appExtraMux(deps)

	_, body := getAppExtraJSON(t, mux, "/api/browser/status?session_id=session_a")
	tab, ok := body["active_tab"].(map[string]any)
	if !ok || tab["title"] != "Docs" || tab["url"] != "https://docs.example" {
		t.Fatalf("active_tab = %#v", body["active_tab"])
	}
	if tab["id"] != float64(22) {
		t.Fatalf("active_tab id = %#v", tab["id"])
	}

	_, body = getAppExtraJSON(t, mux, "/api/browser/status?session_id=session_b")
	tab, ok = body["active_tab"].(map[string]any)
	if !ok || tab["title"] != "Fallback" {
		t.Fatalf("fallback active_tab = %#v", body["active_tab"])
	}

	_, body = getAppExtraJSON(t, mux, "/api/browser/status?session_id=session_c")
	if body["active_tab"] != nil {
		t.Fatalf("active_tab = %#v, want nil without title/url", body["active_tab"])
	}

	// A disconnected bridge skips the /contexts RPC entirely.
	calls := 0
	deps.ProbeContext = func(string) []map[string]any { calls++; return contexts }
	deps.ProbeStatus = func(string) map[string]any { return map[string]any{"bridge": true, "connected": false} }
	_, body = getAppExtraJSON(t, appExtraMux(deps), "/api/browser/status?session_id=session_a")
	if calls != 0 || body["active_tab"] != nil {
		t.Fatalf("disconnected probe: contexts calls = %d, active_tab = %#v", calls, body["active_tab"])
	}
}

// Parity-exempt: no upstream test drives the browser status SSE; this pins
// the immediate status frame and the 5-quiet-tick keepalive.
func TestBrowserStatusStreamEmitsAndKeepalives(t *testing.T) {
	deps := AppExtraRouteDeps{
		ProbeStatus: func(string) map[string]any {
			return map[string]any{"bridge": true, "connected": true, "connections": []any{}}
		},
		PollInterval:   5 * time.Millisecond,
		KeepaliveTicks: 2,
	}
	server := httptest.NewServer(appExtraMux(deps))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/browser/status/stream", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}

	reader := bufio.NewReader(response.Body)
	name, data := readEventFrame(t, reader)
	if name != "status" {
		t.Fatalf("event = %q, want status", name)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if payload["health"] != "healthy" {
		t.Fatalf("health = %#v", payload["health"])
	}

	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read keepalive: %v", err)
	}
	if strings.TrimRight(line, "\r\n") != ": keepalive" {
		t.Fatalf("line = %q, want keepalive", line)
	}
	cancel()
}

// Parity-exempt: upstream only re-emits on a changed signature; this pins
// that a health transition produces a second status frame.
func TestBrowserStatusStreamEmitsOnChange(t *testing.T) {
	var calls int64
	deps := AppExtraRouteDeps{
		ProbeStatus: func(string) map[string]any {
			if atomic.AddInt64(&calls, 1) == 1 {
				return map[string]any{"bridge": true, "connected": true, "connections": []any{}, "last_pong_age_ms": json.Number("10")}
			}
			return map[string]any{"bridge": true, "connected": true, "connections": []any{}, "last_pong_age_ms": json.Number("20000")}
		},
		PollInterval:   5 * time.Millisecond,
		KeepaliveTicks: 1000,
	}
	server := httptest.NewServer(appExtraMux(deps))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/browser/status/stream", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)

	_, data := readEventFrame(t, reader)
	var first map[string]any
	if err := json.Unmarshal([]byte(data), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first["health"] != "healthy" {
		t.Fatalf("first health = %#v", first["health"])
	}
	_, data = readEventFrame(t, reader)
	var second map[string]any
	if err := json.Unmarshal([]byte(data), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if second["health"] != "stale" {
		t.Fatalf("second health = %#v, want stale", second["health"])
	}
	cancel()
}

// Parity-exempt: no upstream test drives POST /api/browser/tab/reveal; this
// pins the int requirement (bool counts as int, as in Python) and pass-through.
func TestBrowserTabRevealContract(t *testing.T) {
	var revealed []int
	deps := AppExtraRouteDeps{RevealTab: func(_ string, tabID int) map[string]any {
		revealed = append(revealed, tabID)
		return map[string]any{"ok": true, "raised": true}
	}}
	mux := appExtraMux(deps)

	cases := []struct {
		body   string
		status int
	}{
		{``, http.StatusBadRequest},
		{`{}`, http.StatusBadRequest},
		{`{"tab_id":"7"}`, http.StatusBadRequest},
		{`{"tab_id":1.5}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
		{`{"tab_id":7}`, http.StatusOK},
		{`{"tab_id":true}`, http.StatusOK},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/browser/tab/reveal", strings.NewReader(testCase.body)))
		if recorder.Code != testCase.status {
			t.Fatalf("body %q: status = %d, want %d (%s)", testCase.body, recorder.Code, testCase.status, recorder.Body.String())
		}
		if testCase.status == http.StatusBadRequest {
			var body map[string]any
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			if body["error"] != "tab_id (int) is required" {
				t.Fatalf("body %q: %#v", testCase.body, body)
			}
		}
	}
	if len(revealed) != 2 || revealed[0] != 7 || revealed[1] != 1 {
		t.Fatalf("revealed = %#v", revealed)
	}
}

// Parity-exempt: no Python test exercises the TCP probe; this pins the
// HTTP/1.0 status body parse against a real server.
func TestProbeBridgeStatusOverHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connected": true,
			"uptime_ms": 1234,
			"connections": []any{
				map[string]any{"last_pong_age_ms": 42, "version": "1.2.3"},
			},
		})
	}))
	defer server.Close()

	payload := probeBridgeStatus(hostOf(t, server.URL))
	if payload["bridge"] != true || payload["connected"] != true {
		t.Fatalf("payload = %#v", payload)
	}
	pong, ok := toFloat(payload["last_pong_age_ms"])
	if !ok || pong != 42 {
		t.Fatalf("last_pong_age_ms = %#v", payload["last_pong_age_ms"])
	}
	if payload["extension_version"] != "1.2.3" {
		t.Fatalf("extension_version = %#v", payload["extension_version"])
	}
	uptime, ok := toFloat(payload["uptime_ms"])
	if !ok || uptime != 1234 {
		t.Fatalf("uptime_ms = %#v", payload["uptime_ms"])
	}

	unavailable := probeBridgeStatus("127.0.0.1:1")
	if unavailable["bridge"] != false || unavailable["connected"] != false {
		t.Fatalf("unavailable = %#v", unavailable)
	}
	connections, ok := unavailable["connections"].([]any)
	if !ok || len(connections) != 0 {
		t.Fatalf("connections = %#v, want empty list", unavailable["connections"])
	}
}

// Parity-exempt: no Python test exercises the reveal transport; this pins the
// POST /tabs/{id}/reveal call and JSON response.
func TestRevealBrowserTabOverHTTP(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "raised": true})
	}))
	defer server.Close()

	payload := revealBrowserTab(hostOf(t, server.URL), 7)
	if method != http.MethodPost || path != "/tabs/7/reveal" {
		t.Fatalf("request = %s %s", method, path)
	}
	if payload["ok"] != true || payload["raised"] != true {
		t.Fatalf("payload = %#v", payload)
	}
	if failure := revealBrowserTab("127.0.0.1:1", 7); failure["ok"] != false || failure["error"] != "bridge unreachable" {
		t.Fatalf("unreachable = %#v", failure)
	}
}

// Parity-exempt: Python's int() parsing for query params; this pins the
// accepted forms and the fallback.
func TestPythonIntHelpers(t *testing.T) {
	for raw, want := range map[string]int{
		"120": 120, " 12 ": 12, "+7": 7, "-3": -3, "1_0": 10, "": 120, "abc": 120, "1.5": 120,
	} {
		if got := pythonIntOr(raw, 120); got != want {
			t.Fatalf("pythonIntOr(%q) = %d, want %d", raw, got, want)
		}
	}
	if value, ok := pythonIntValue(json.Number("7")); !ok || value != 7 {
		t.Fatalf("pythonIntValue(7) = %d,%v", value, ok)
	}
	if value, ok := pythonIntValue(true); !ok || value != 1 {
		t.Fatalf("pythonIntValue(true) = %d,%v", value, ok)
	}
	if _, ok := pythonIntValue(json.Number("1.5")); ok {
		t.Fatal("pythonIntValue(1.5) must reject a non-integral number")
	}
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return parsed.Host
}

// TestBrowserStatusProbeTargetsBridgePort pins the address fix behind R03: the
// status probe used to dial 9229 + 1 = 9230, which the Go bridge does not bind,
// so a running bridge always reported offline. The probe order is now the
// bridge's own status port (HIVE_BROWSERBRIDGE_HTTP or HIVE_BRIDGE_PORT + 1,
// default 14830) with the legacy 9230 endpoint only as a fallback.
//
// Parity: framework/server/app.py::_probe_browser_bridge
func TestBrowserStatusProbeTargetsBridgePort(t *testing.T) {
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", "")
	t.Setenv("HIVE_BRIDGE_PORT", "")
	if got := defaultBridgeStatusAddrs(); len(got) != 2 || got[0] != "127.0.0.1:14830" || got[1] != "127.0.0.1:9230" {
		t.Fatalf("default addrs = %v, want [127.0.0.1:14830 127.0.0.1:9230]", got)
	}

	t.Setenv("HIVE_BRIDGE_PORT", "17829")
	if got := defaultBridgeStatusAddrs(); got[0] != "127.0.0.1:17830" {
		t.Fatalf("HIVE_BRIDGE_PORT primary = %v", got)
	}

	t.Setenv("HIVE_BRIDGE_PORT", "9229")
	if got := defaultBridgeStatusAddrs(); len(got) != 1 || got[0] != "127.0.0.1:9230" {
		t.Fatalf("legacy base must dedupe to the 9230 fallback: %v", got)
	}

	t.Setenv("HIVE_BRIDGE_PORT", "")
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", "127.0.0.1:19999")
	if got := defaultBridgeStatusAddrs(); got[0] != "127.0.0.1:19999" {
		t.Fatalf("HIVE_BROWSERBRIDGE_HTTP primary = %v", got)
	}
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", "http://127.0.0.1:19999")
	if got := bridgeStatusHostPort("http://127.0.0.1:19999"); got != "127.0.0.1:19999" {
		t.Fatalf("URL override = %q", got)
	}
}

// TestBrowserStatusProbesFallbackWhenPrimarySilent pins the fallback: when the
// primary endpoint has no bridge, the legacy 9230 endpoint is probed and its
// real state is reported — status must never be hardcoded offline just because
// the first address is silent.
//
// Parity: framework/server/app.py::_probe_browser_bridge
func TestBrowserStatusProbesFallbackWhenPrimarySilent(t *testing.T) {
	var probed []string
	var contextsAddr string
	deps := AppExtraRouteDeps{
		StatusAddrs: func() []string { return []string{"127.0.0.1:1", "127.0.0.1:2"} },
		ProbeStatus: func(addr string) map[string]any {
			probed = append(probed, addr)
			if addr == "127.0.0.1:1" {
				return map[string]any{"bridge": false, "connected": false, "connections": []any{}}
			}
			return map[string]any{"bridge": true, "connected": true, "connections": []any{}}
		},
		ProbeContext: func(addr string) []map[string]any {
			contextsAddr = addr
			return nil
		},
	}
	_, body := getAppExtraJSON(t, appExtraMux(deps), "/api/browser/status?session_id=session_a")
	if len(probed) < 2 || probed[0] != "127.0.0.1:1" || probed[1] != "127.0.0.1:2" {
		t.Fatalf("probe order = %v", probed)
	}
	if body["health"] != "healthy" || body["bridge"] != true || body["connected"] != true {
		t.Fatalf("payload = %#v", body)
	}
	if contextsAddr != "127.0.0.1:2" {
		t.Fatalf("contexts probe used %q, want the live endpoint", contextsAddr)
	}
}

// Parity-exempt: no upstream test registers every new module on one mux; this
// guards the Go-specific constraint that duplicate ServeMux patterns panic.
func TestNewRouteModulesCoexistOnOneMux(t *testing.T) {
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, ChatRouteDeps{})
	RegisterEventRoutes(mux, EventRouteDeps{})
	RegisterWorkerRoutes(mux, WorkerRouteDeps{})
	RegisterModelRoutes(mux, ModelRouteDeps{})
	RegisterAppExtraRoutes(mux, AppExtraRouteDeps{})

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/events/global"},
		{http.MethodPost, "/api/crm/changed"},
		{http.MethodGet, "/api/sessions/session_a/workers/stream"},
		{http.MethodPost, "/api/sessions/session_a/workers/stop-all"},
		{http.MethodPost, "/api/sessions/session_a/workers/w1/stop"},
		{http.MethodGet, "/api/sessions/session_a/workers/w1/reap-timeline"},
		{http.MethodGet, "/api/models/overview"},
		{http.MethodPut, "/api/models/slots/worker_llm"},
		{http.MethodDelete, "/api/models/slots/worker_llm"},
		{http.MethodGet, "/api/models/endpoints/magpie/models"},
		{http.MethodPut, "/api/models/endpoints/ollama"},
		{http.MethodDelete, "/api/models/endpoints/ollama"},
		{http.MethodGet, "/api/health/resources"},
		{http.MethodGet, "/api/browser/status"},
		{http.MethodGet, "/api/browser/status/stream"},
		{http.MethodPost, "/api/browser/tab/reveal"},
		{http.MethodGet, "/api/sessions/session_a/events"},
	}
	for _, testCase := range cases {
		_, pattern := mux.Handler(httptest.NewRequest(testCase.method, testCase.path, nil))
		if pattern == "" {
			t.Fatalf("%s %s is not registered", testCase.method, testCase.path)
		}
	}
}

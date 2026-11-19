package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func discardLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func newAppForTest(t *testing.T, deps AppDeps) *App {
	t.Helper()
	disabled := ""
	deps.DesktopToken = &disabled
	if deps.Logger == nil {
		deps.Logger = discardLogger()
	}
	if deps.FrontendDist == "" {
		deps.FrontendDist = t.TempDir()
	}
	return NewApp(deps)
}

func getJSON(t *testing.T, app *App, target string) (int, map[string]any, http.Header) {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s body: %v", target, err)
	}
	return recorder.Code, body, recorder.Header()
}

// Parity: framework/server/tests/test_api.py::TestHealth::test_health
func TestHealthEmptyRuntime(t *testing.T) {
	app := newAppForTest(t, AppDeps{})

	status, body, headers := getJSON(t, app, "/api/health")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", headers.Get("Content-Type"))
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %#v, want ok", body["status"])
	}
	if body["agents_loaded"] != float64(0) {
		t.Fatalf("agents_loaded = %#v, want 0", body["agents_loaded"])
	}
	if body["sessions"] != float64(0) {
		t.Fatalf("sessions = %#v, want 0", body["sessions"])
	}
	resources, ok := body["resources"].(map[string]any)
	if !ok || resources["verdict"] != "unknown" {
		t.Fatalf("resources = %#v, want the no-sample rollup", body["resources"])
	}
	executor, ok := body["request_executor"].(map[string]any)
	if !ok {
		t.Fatalf("request_executor = %#v", body["request_executor"])
	}
	if executor["max_workers"] != float64(defaultRequestExecutorMax) {
		t.Fatalf("max_workers = %#v, want %d", executor["max_workers"], defaultRequestExecutorMax)
	}
	if executor["active"] != float64(0) || executor["queued"] != float64(0) {
		t.Fatalf("executor active/queued = %#v/%#v, want 0/0", executor["active"], executor["queued"])
	}
}

// Parity-exempt: the upstream health test uses an empty SessionManager; this
func TestHealthCountsLiveColonies(t *testing.T) {
	app := newAppForTest(t, AppDeps{
		Sessions: func() []FeatureSession {
			return []FeatureSession{
				{ColonyID: "colony_a"},
				{ColonyID: ""},
				{ColonyID: "colony_b"},
			}
		},
		Resources: func() any { return map[string]any{"verdict": "ok", "avail_mb": 512, "renderers": 1} },
		Executor:  func() ExecutorState { return ExecutorState{MaxWorkers: 8, Active: 2, Queued: 3} },
	})

	status, body, _ := getJSON(t, app, "/api/health")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["sessions"] != float64(3) {
		t.Fatalf("sessions = %#v, want 3", body["sessions"])
	}
	if body["agents_loaded"] != float64(2) {
		t.Fatalf("agents_loaded = %#v, want 2", body["agents_loaded"])
	}
	executor := body["request_executor"].(map[string]any)
	if executor["max_workers"] != float64(8) || executor["active"] != float64(2) || executor["queued"] != float64(3) {
		t.Fatalf("request_executor = %#v", executor)
	}
	resources := body["resources"].(map[string]any)
	if resources["verdict"] != "ok" {
		t.Fatalf("resources = %#v", resources)
	}
}

// Parity-exempt: deliberately diverges from
// framework/server/tests/test_api.py::TestErrorMiddleware::test_unknown_api_route_falls_back_to_frontend,
// which pins the old 200 + index.html fallback that hid unregistered endpoints.
func TestUnknownAPIRouteReturnsJSON404(t *testing.T) {
	dist := t.TempDir()
	index := "<!doctype html><title>Hive</title>"
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(index), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	app := newAppForTest(t, AppDeps{FrontendDist: dist})

	for _, target := range []string{"/api/nonexistent", "/api/colonies/ghost"} {
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", target, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Fatalf("%s content-type = %q, want application/json", target, got)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", target, got)
		}
		var body map[string]any
		if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
			t.Fatalf("%s decode body: %v", target, err)
		}
		if body["error"] != "not found" {
			t.Fatalf("%s body = %#v, want error=not found", target, body)
		}
	}
}

// Parity-exempt: no upstream test drives the static catch-all for existing
func TestStaticServingAndIndexFallback(t *testing.T) {
	dist := t.TempDir()
	index := "<!doctype html><title>Hive</title>"
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(index), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	script := "console.log('hive')"
	if err := os.WriteFile(filepath.Join(dist, "app.js"), []byte(script), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	app := newAppForTest(t, AppDeps{FrontendDist: dist})

	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if recorder.Code != 200 {
		t.Fatalf("asset status = %d, want 200", recorder.Code)
	}
	if recorder.Body.String() != script {
		t.Fatalf("asset body = %q", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("asset content-type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("asset Cache-Control = %q, want empty (only /api/ is no-store)", got)
	}

	for _, target := range []string{"/", "/colonies/colony_a"} {
		recorder = httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != 200 {
			t.Fatalf("%s status = %d, want 200", target, recorder.Code)
		}
		if recorder.Body.String() != index {
			t.Fatalf("%s body = %q, want the index page", target, recorder.Body.String())
		}
	}

	recorder = httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", recorder.Code)
	}
	if recorder.Body.String() == index {
		t.Fatal("missing asset fell back to the index page")
	}
}

// Parity-exempt: no upstream test covers the desktop shared-secret middleware;
func TestDesktopAuthToken(t *testing.T) {
	token := "desktop-secret"
	app := NewApp(AppDeps{
		DesktopToken: &token,
		FrontendDist: t.TempDir(),
		Logger:       discardLogger(),
	})

	request := func(path, provided string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if provided != "" {
			req.Header.Set("X-Hive-Token", provided)
		}
		app.ServeHTTP(recorder, req)
		return recorder
	}

	recorder := request("/api/health", "")
	if recorder.Code != 401 {
		t.Fatalf("missing token status = %d, want 401", recorder.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode 401 body: %v", err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("401 body = %#v", body)
	}

	if recorder = request("/api/health", "wrong"); recorder.Code != 401 {
		t.Fatalf("wrong token status = %d, want 401", recorder.Code)
	}
	if recorder = request("/api/health", token); recorder.Code != 200 {
		t.Fatalf("correct token status = %d, want 200", recorder.Code)
	}

	if recorder = request("/api/queen-mail/inbox", ""); recorder.Code == 401 {
		t.Fatal("queen-mail path must bypass desktop auth")
	}
}

// Parity-exempt: no upstream test names the no-store header on a normal API
func TestAPICacheControl(t *testing.T) {
	app := newAppForTest(t, AppDeps{})

	_, _, headers := getJSON(t, app, "/api/health")
	if got := headers.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

// Parity-exempt: no upstream test forces a handler exception; this pins the
func TestErrorRecoveryReturnsGeneric500(t *testing.T) {
	app := &App{logger: discardLogger()}
	handler := app.errorRecovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/boom", nil))
	if recorder.Code != 500 {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "Internal server error" {
		t.Fatalf("body = %#v", body)
	}
	if strings.Contains(recorder.Body.String(), "secret internal detail") {
		t.Fatal("error response leaked the panic message")
	}
}

// Parity-exempt: no upstream test drives GET /api/startup-status; this pins
func TestStartupStatusEndpoint(t *testing.T) {
	resetBootStatus(t)
	ReportBootStage("Preparing queen session", "queen_a")

	app := newAppForTest(t, AppDeps{})
	status, body, _ := getJSON(t, app, "/api/startup-status")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["ready"] != false {
		t.Fatalf("ready = %#v, want false", body["ready"])
	}
	if body["stage"] != "Preparing queen session" {
		t.Fatalf("stage = %#v", body["stage"])
	}
	if body["detail"] != "queen_a" {
		t.Fatalf("detail = %#v", body["detail"])
	}
	history, ok := body["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("history = %#v, want one entry", body["history"])
	}
	entry := history[0].(map[string]any)
	if entry["stage"] != "Preparing queen session" || entry["detail"] != "queen_a" {
		t.Fatalf("history[0] = %#v", entry)
	}
	if _, ok := body["updated_at"].(float64); !ok {
		t.Fatalf("updated_at = %#v, want a float Unix timestamp", body["updated_at"])
	}

	MarkBootReady("Queen ready")
	_, body, _ = getJSON(t, app, "/api/startup-status")
	if body["ready"] != true {
		t.Fatalf("ready = %#v after mark_ready", body["ready"])
	}
	if body["detail"] != "" {
		t.Fatalf("detail = %#v after mark_ready", body["detail"])
	}
}

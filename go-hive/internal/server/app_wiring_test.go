package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// Parity-exempt: assembly guard, not an upstream behaviour.
//
// A duplicate or ambiguous ServeMux pattern panics inside NewApp, so simply
// constructing the app pins that every wired route table coexists (the wave-1
// tables overlap on /api/sessions, /api/queen and /api/models prefixes).
func TestNewAppRegistersWaveOneTablesWithoutPatternConflict(t *testing.T) {
	app := newAppForTest(t, AppDeps{Home: hivehome.Home{Root: t.TempDir()}})
	if app.Handler() == nil {
		t.Fatal("NewApp returned a nil handler")
	}
}

// muxMiss reports whether the response came from ServeMux's own "no pattern
// matched" branch rather than from a handler. A handler's 404 is the route
// working (unknown session/queen/colony); the mux's 404 means the table was
// never registered.
func muxMiss(recorder *httptest.ResponseRecorder) bool {
	return recorder.Code == http.StatusNotFound &&
		strings.Contains(recorder.Body.String(), "404 page not found")
}

// Parity-exempt: assembly guard. Every table wired into NewApp must answer on
// its own prefix instead of falling through to the mux.
func assertRoutesNotMissing(t *testing.T, app *App, cases []struct{ method, path string }) {
	t.Helper()
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if muxMiss(recorder) {
			t.Errorf("%s %s -> mux 404 (route table not wired)", tc.method, tc.path)
		}
	}
}

func TestWiredRouteTablesAreReachable(t *testing.T) {
	app := newAppForTest(t, AppDeps{Home: hivehome.Home{Root: t.TempDir()}})
	assertRoutesNotMissing(t, app, []struct{ method, path string }{
		{http.MethodGet, "/api/credentials/specs"},
		{http.MethodGet, "/api/credentials"},
		{http.MethodGet, "/api/channels"},
		{http.MethodGet, "/api/prompts"},
		{http.MethodGet, "/api/memories"},
		{http.MethodGet, "/api/mcp/servers"},
		{http.MethodGet, "/api/maintenance/janitor/report"},
		{http.MethodGet, "/api/models/overview"},
		{http.MethodGet, "/api/health/resources"},
		{http.MethodPost, "/api/crm/changed"},
		{http.MethodGet, "/api/sessions/probe-session/tasks"},
		{http.MethodGet, "/api/queen/probe-queen/soul"},
	})
}

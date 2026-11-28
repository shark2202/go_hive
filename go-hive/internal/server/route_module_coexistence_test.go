package server

// route_module_coexistence_test.go keeps the cross-module mux check that used
// to live in routes_app_extra_test.go: it registers every new route family on
// one mux, which is a Go-specific constraint (duplicate ServeMux patterns
// panic) that no upstream Python test exercises.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/server/routesapp"
)

// Parity-exempt: no upstream test registers every new module on one mux; this
// guards the Go-specific constraint that duplicate ServeMux patterns panic.
func TestNewRouteModulesCoexistOnOneMux(t *testing.T) {
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, ChatRouteDeps{})
	RegisterEventRoutes(mux, EventRouteDeps{})
	RegisterWorkerRoutes(mux, WorkerRouteDeps{})
	RegisterModelRoutes(mux, ModelRouteDeps{})
	routesapp.RegisterRoutes(mux, routesapp.Deps{})

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

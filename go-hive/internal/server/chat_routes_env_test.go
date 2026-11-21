package server

// Chat-route envelope tests: the failure shape the value golden pins.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity: framework/server/routes_execution.py::handle_chat
func TestChatRouteUnknownSessionEnvelope(t *testing.T) {
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, ChatRouteDeps{Manager: NewSessionManager()})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/ghost/chat", strings.NewReader(`{"message":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "Session 'ghost' not found" {
		t.Fatalf("error = %#v, want the resolve_session envelope", body["error"])
	}
}

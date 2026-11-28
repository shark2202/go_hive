package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type colonyLoadCall struct {
	SessionID string
	ColonyID  string
	Model     *string
}

type fakeColonyLifecycle struct {
	loadSession *Session
	loadErr     error
	loadCalls   []colonyLoadCall

	unloadOK  bool
	unloadIDs []string
}

func (f *fakeColonyLifecycle) LoadColony(sessionID, colonyID string, model *string) (*Session, error) {
	f.loadCalls = append(f.loadCalls, colonyLoadCall{SessionID: sessionID, ColonyID: colonyID, Model: model})
	return f.loadSession, f.loadErr
}

func (f *fakeColonyLifecycle) UnloadColony(sessionID string) bool {
	f.unloadIDs = append(f.unloadIDs, sessionID)
	return f.unloadOK
}

type pivotQueenNode struct {
	mu       sync.Mutex
	texts    []string
	clients  []bool
	injectTo error
}

func (n *pivotQueenNode) InjectEvent(text string, isClientInput bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.injectTo != nil {
		return n.injectTo
	}
	n.texts = append(n.texts, text)
	n.clients = append(n.clients, isClientInput)
	return nil
}

func (n *pivotQueenNode) calls() ([]string, []bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.texts...), append([]bool(nil), n.clients...)
}

func colonyTestHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return home
}

func writeColonyMetadata(t *testing.T, home hivehome.Home, colonyID string) {
	t.Helper()
	dir := filepath.Join(home.ColoniesDir(), colonyID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"name":"Acme"}`), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

// Parity: framework/server/routes_sessions.py::handle_load_colony
// (bare `body = await request.json()`: malformed JSON and a non-object body
// reach the error middleware as 500; validation failures stay 400).
func TestLoadColonyRejectsBadBodies(t *testing.T) {
	home := colonyTestHome(t)
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: &fakeColonyLifecycle{}})
	target := "/api/sessions/s1/colony"

	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"invalid json", `{`, http.StatusInternalServerError, internalServerError},
		{"not an object", `[]`, http.StatusInternalServerError, internalServerError},
		{"missing colony_id", `{}`, http.StatusBadRequest, "colony_id is required"},
		{"empty colony_id", `{"colony_id": ""}`, http.StatusBadRequest, "colony_id is required"},
		{"non-string colony_id", `{"colony_id": 7}`, http.StatusBadRequest, "colony_id is required"},
		{"traversal colony_id", `{"colony_id": "../etc"}`, http.StatusBadRequest, "Invalid colony_id"},
		{"backslash colony_id", `{"colony_id": "a\\b"}`, http.StatusBadRequest, "Invalid colony_id"},
	}
	for _, tc := range cases {
		recorder, body := doSessionJSON(t, mux, http.MethodPost, target, tc.body)
		if recorder.Code != tc.status || body["error"] != tc.want {
			t.Errorf("%s: status/error = %d/%#v, want %d/%q", tc.name, recorder.Code, body["error"], tc.status, tc.want)
		}
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_load_colony 无独立测试。
func TestLoadColonyMissingMetadataReturns404(t *testing.T) {
	home := colonyTestHome(t)
	lifecycle := &fakeColonyLifecycle{loadSession: NewSession("s1", nil)}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "missing"}`)
	if recorder.Code != http.StatusNotFound || body["error"] != "Colony not found: missing" {
		t.Fatalf("status/error = %d/%#v, want 404/Colony not found: missing", recorder.Code, body["error"])
	}
	if len(lifecycle.loadCalls) != 0 {
		t.Fatalf("load_calls = %#v, want none", lifecycle.loadCalls)
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_load_colony 无独立测试。
func TestLoadColonyConflictReturns409(t *testing.T) {
	home := colonyTestHome(t)
	writeColonyMetadata(t, home, "acme")
	lifecycle := &fakeColonyLifecycle{loadErr: &ColonyLoadConflictError{Message: "Session 's1' not found"}}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme"}`)
	if recorder.Code != http.StatusConflict || body["error"] != "Session 's1' not found" {
		t.Fatalf("status/error = %d/%#v, want 409/Session 's1' not found", recorder.Code, body["error"])
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_load_colony 无独立测试。
func TestLoadColonyCredentialRequiredReturns424(t *testing.T) {
	home := colonyTestHome(t)
	writeColonyMetadata(t, home, "acme")
	structured := &ColonyCredentialRequiredError{
		Message:  "Missing OPENAI_API_KEY",
		Required: []map[string]any{{"credential_id": "openai", "available": false}},
		Err:      credentials.NewCredentialError("Missing OPENAI_API_KEY"),
	}
	lifecycle := &fakeColonyLifecycle{loadErr: structured}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme"}`)
	if recorder.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d, want 424", recorder.Code)
	}
	if body["error"] != "credentials_required" || body["message"] != "Missing OPENAI_API_KEY" {
		t.Fatalf("error/message = %#v/%#v", body["error"], body["message"])
	}
	if body["agent_path"] != "acme" {
		t.Fatalf("agent_path = %#v, want acme", body["agent_path"])
	}
	required, ok := body["required"].([]any)
	if !ok || len(required) != 1 {
		t.Fatalf("required = %#v, want one entry", body["required"])
	}
}

// Parity-exempt: Go 边界覆盖；上游无 validation_result 的 CredentialError 回退。
func TestLoadColonyRawCredentialErrorFallsBackToEmptyRequired(t *testing.T) {
	home := colonyTestHome(t)
	writeColonyMetadata(t, home, "acme")
	lifecycle := &fakeColonyLifecycle{loadErr: credentials.NewCredentialError("no key")}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme"}`)
	if recorder.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d, want 424", recorder.Code)
	}
	if body["error"] != "credentials_required" || body["message"] != "no key" {
		t.Fatalf("error/message = %#v/%#v", body["error"], body["message"])
	}
	required, ok := body["required"].([]any)
	if !ok || len(required) != 0 {
		t.Fatalf("required = %#v, want []", body["required"])
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_load_colony 无独立测试。
func TestLoadColonyInternalErrorReturns500(t *testing.T) {
	home := colonyTestHome(t)
	writeColonyMetadata(t, home, "acme")
	lifecycle := &fakeColonyLifecycle{loadErr: errors.New("boom")}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme"}`)
	if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
		t.Fatalf("status/error = %d/%#v, want 500/%q", recorder.Code, body["error"], internalServerError)
	}

	unconfigured := sessionRoutesMux(SessionRouteDeps{Home: home})
	recorder, body = doSessionJSON(t, unconfigured, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme"}`)
	if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
		t.Fatalf("unconfigured status/error = %d/%#v, want 500", recorder.Code, body["error"])
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_load_colony 无独立测试。
func TestLoadColonySuccessReturnsLiveDict(t *testing.T) {
	home := colonyTestHome(t)
	writeColonyMetadata(t, home, "acme")
	loaded := NewSession("s1", nil)
	loaded.ColonyID = "acme"
	lifecycle := &fakeColonyLifecycle{loadSession: loaded}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme", "model": "openai/gpt-oss-120b"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %#v)", recorder.Code, body)
	}
	if body["session_id"] != "s1" || body["colony_id"] != "acme" || body["has_worker"] != true {
		t.Fatalf("body = %#v", body)
	}
	if len(lifecycle.loadCalls) != 1 {
		t.Fatalf("load_calls = %#v, want one", lifecycle.loadCalls)
	}
	call := lifecycle.loadCalls[0]
	if call.SessionID != "s1" || call.ColonyID != "acme" {
		t.Fatalf("call = %#v", call)
	}
	if call.Model == nil || *call.Model != "openai/gpt-oss-120b" {
		t.Fatalf("model = %#v, want openai/gpt-oss-120b", call.Model)
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status without model = %d, want 200", recorder.Code)
	}
	if lifecycle.loadCalls[1].Model != nil {
		t.Fatalf("model without body field = %#v, want nil", lifecycle.loadCalls[1].Model)
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_load_colony 无独立测试。
func TestLoadColonyNilSessionReturns500(t *testing.T) {
	home := colonyTestHome(t)
	writeColonyMetadata(t, home, "acme")
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: &fakeColonyLifecycle{}})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/colony", `{"colony_id": "acme"}`)
	if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
		t.Fatalf("status/error = %d/%#v, want 500", recorder.Code, body["error"])
	}
}

// Parity: core/framework/server/routes_sessions.py::handle_unload_colony
//
// DELETE is an explicitly registered route upstream (routes_sessions.py:3214),
// not fallout of a generic delete handler. The matched pattern is asserted
// alongside the side effect so a future regression that lets the
// {first}/{second} alias swallow the path fails here.
func TestDeleteSessionColonyUsesUnloadRoute(t *testing.T) {
	lifecycle := &fakeColonyLifecycle{unloadOK: true}
	mux := sessionRoutesMux(SessionRouteDeps{Colony: lifecycle})

	request := httptest.NewRequest(http.MethodDelete, "/api/sessions/s1/colony", nil)
	if _, pattern := mux.Handler(request); pattern != "DELETE /api/sessions/{session_id}/colony" {
		t.Fatalf("matched pattern = %q, want the explicit colony route (generic alias won)", pattern)
	}

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/s1/colony", "")
	if recorder.Code != http.StatusOK || body["colony_unloaded"] != true || body["session_id"] != "s1" {
		t.Fatalf("status/body = %d/%#v, want 200 with colony_unloaded", recorder.Code, body)
	}
	if len(lifecycle.unloadIDs) != 1 || lifecycle.unloadIDs[0] != "s1" {
		t.Fatalf("unload_ids = %#v, want [s1]", lifecycle.unloadIDs)
	}
}

// Parity: core/framework/server/routes_sessions.py::handle_delete_history_session
//
// Upstream registers the history route (routes_sessions.py:3207) before the
// colony route (routes_sessions.py:3214), so aiohttp's first-match-wins sends
// "/api/sessions/history/colony" to history deletion, never to unload. ServeMux
// ranks the colony pattern higher, so the colony handler has to yield for that
// one overlap.
func TestDeleteSessionColonyPatternYieldsHistoryOverlap(t *testing.T) {
	home := colonyTestHome(t)
	lifecycle := &fakeColonyLifecycle{unloadOK: true}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/history/colony", "")
	if recorder.Code != http.StatusOK || body["deleted"] != "colony" {
		t.Fatalf("status/body = %d/%#v, want history deletion of colony", recorder.Code, body)
	}
	if len(lifecycle.unloadIDs) != 0 {
		t.Fatalf("unload_ids = %#v, want none (history wins, as upstream)", lifecycle.unloadIDs)
	}
}

// Parity-exempt: Go ServeMux 特异性；上游没有可对应的路由形状差异
// （aiohttp 按注册顺序匹配，ServeMux 按特异性，两者对同一组路径的选择必须一致）。
func TestDeleteSessionTwoSegmentAliasStaysHistoryOnly(t *testing.T) {
	home := colonyTestHome(t)
	lifecycle := &fakeColonyLifecycle{unloadOK: true}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	// The colony segment is no longer reachable through the alias.
	request := httptest.NewRequest(http.MethodDelete, "/api/sessions/s1/colony", nil)
	if _, pattern := mux.Handler(request); pattern == "DELETE /api/sessions/{first}/{second}" {
		t.Fatalf("matched pattern = %q, the alias must not claim the colony path", pattern)
	}

	recorder, _ := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/s1/bogus", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status for unhandled two-segment delete = %d, want 404", recorder.Code)
	}
	if len(lifecycle.unloadIDs) != 0 {
		t.Fatalf("unload_ids = %#v, want none", lifecycle.unloadIDs)
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_unload_colony 无独立测试。
func TestUnloadColonySuccess(t *testing.T) {
	lifecycle := &fakeColonyLifecycle{unloadOK: true}
	mux := sessionRoutesMux(SessionRouteDeps{Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/s1/colony", "")
	if recorder.Code != http.StatusOK || body["colony_unloaded"] != true || body["session_id"] != "s1" {
		t.Fatalf("status/body = %d/%#v, want 200 with colony_unloaded", recorder.Code, body)
	}
	if len(lifecycle.unloadIDs) != 1 || lifecycle.unloadIDs[0] != "s1" {
		t.Fatalf("unload_ids = %#v, want [s1]", lifecycle.unloadIDs)
	}
}

// Parity-exempt: Go ServeMux 重叠调度；上游按 aiohttp 注册顺序让
// DELETE /api/sessions/history/{id} 先于 DELETE /api/sessions/{id}/colony。
func TestDeleteHistoryColonyPrefersHistoryRoute(t *testing.T) {
	home := colonyTestHome(t)
	lifecycle := &fakeColonyLifecycle{unloadOK: true}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Colony: lifecycle})

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/history/colony", "")
	if recorder.Code != http.StatusOK || body["deleted"] != "colony" {
		t.Fatalf("status/body = %d/%#v, want history deletion of colony", recorder.Code, body)
	}
	if len(lifecycle.unloadIDs) != 0 {
		t.Fatalf("unload_ids = %#v, want none (history route wins)", lifecycle.unloadIDs)
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_unload_colony 无独立测试。
func TestUnloadColonyMissingSessionReturns404(t *testing.T) {
	mux := sessionRoutesMux(SessionRouteDeps{
		Manager: NewSessionManager(),
		Colony:  &fakeColonyLifecycle{unloadOK: false},
	})

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/missing/colony", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "Session 'missing' not found" {
		t.Fatalf("status/error = %d/%#v, want 404", recorder.Code, body["error"])
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_unload_colony 无独立测试。
func TestUnloadColonyWithoutColonyReturns409(t *testing.T) {
	manager := NewSessionManager()
	manager.AddSession(NewSession("s1", nil))
	mux := sessionRoutesMux(SessionRouteDeps{
		Manager: manager,
		Colony:  &fakeColonyLifecycle{unloadOK: false},
	})

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/s1/colony", "")
	if recorder.Code != http.StatusConflict || body["error"] != "No colony loaded in this session" {
		t.Fatalf("status/error = %d/%#v, want 409", recorder.Code, body["error"])
	}
}

// Parity-exempt: Go 边界覆盖；上游无 colony runtime 配置时的防御分支。
func TestUnloadColonyUnconfiguredReturns500(t *testing.T) {
	mux := sessionRoutesMux(SessionRouteDeps{})

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/s1/colony", "")
	if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
		t.Fatalf("status/error = %d/%#v, want 500", recorder.Code, body["error"])
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_dismiss_colony_pivot 无独立测试。
func TestDismissColonyPivotNoPending(t *testing.T) {
	manager := NewSessionManager()
	manager.AddSession(NewSession("s1", nil))
	mux := sessionRoutesMux(SessionRouteDeps{Manager: manager})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/dismiss-colony-pivot", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body["dismissed"] != false || body["reason"] != "no_pending_pivot" {
		t.Fatalf("body = %#v, want dismissed=false/reason=no_pending_pivot", body)
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_dismiss_colony_pivot 无独立测试。
func TestDismissColonyPivotInjectsAndClears(t *testing.T) {
	session := NewSession("s1", nil)
	session.SetPendingColonyPivot(map[string]any{"goal": "ship it"})
	queen := &pivotQueenNode{}
	session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": queen}}

	manager := NewSessionManager()
	manager.AddSession(session)
	mux := sessionRoutesMux(SessionRouteDeps{Manager: manager})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/dismiss-colony-pivot", "")
	if recorder.Code != http.StatusOK || body["dismissed"] != true {
		t.Fatalf("status/body = %d/%#v, want 200 dismissed=true", recorder.Code, body)
	}
	texts, clients := queen.calls()
	if len(texts) != 1 || texts[0] != colonyPivotDismissMessage {
		t.Fatalf("injected texts = %#v, want the dismissal prompt", texts)
	}
	if len(clients) != 1 || !clients[0] {
		t.Fatalf("is_client_input = %#v, want [true]", clients)
	}
	if session.HasPendingColonyPivot() {
		t.Fatal("pending pivot still set after dismissal")
	}

	// A second dismissal is a benign no-op.
	recorder, body = doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/dismiss-colony-pivot", "")
	if recorder.Code != http.StatusOK || body["dismissed"] != false || body["reason"] != "no_pending_pivot" {
		t.Fatalf("second status/body = %d/%#v", recorder.Code, body)
	}
	if texts, _ := queen.calls(); len(texts) != 1 {
		t.Fatalf("injections after second call = %d, want 1", len(texts))
	}
}

// Parity-exempt: Go 边界覆盖；上游 handle_dismiss_colony_pivot 无独立测试。
func TestDismissColonyPivotInjectionFailureStillDismisses(t *testing.T) {
	session := NewSession("s1", nil)
	session.SetPendingColonyPivot(map[string]any{"goal": "ship it"})
	session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": &pivotQueenNode{injectTo: errors.New("boom")}}}

	manager := NewSessionManager()
	manager.AddSession(session)
	mux := sessionRoutesMux(SessionRouteDeps{Manager: manager})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/dismiss-colony-pivot", "")
	if recorder.Code != http.StatusOK || body["dismissed"] != true {
		t.Fatalf("status/body = %d/%#v, want 200 dismissed=true", recorder.Code, body)
	}
	if session.HasPendingColonyPivot() {
		t.Fatal("pending pivot still set after failed injection")
	}
}

// Parity-exempt: Go 边界覆盖；无 queen loop 时仍清除 pending pivot。
func TestDismissColonyPivotWithoutQueenLoop(t *testing.T) {
	session := NewSession("s1", nil)
	session.SetPendingColonyPivot(map[string]any{"goal": "ship it"})
	manager := NewSessionManager()
	manager.AddSession(session)
	mux := sessionRoutesMux(SessionRouteDeps{Manager: manager})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/s1/dismiss-colony-pivot", "")
	if recorder.Code != http.StatusOK || body["dismissed"] != true {
		t.Fatalf("status/body = %d/%#v", recorder.Code, body)
	}
	if session.HasPendingColonyPivot() {
		t.Fatal("pending pivot still set without a queen loop")
	}
}

// Parity-exempt: Go 边界覆盖；上游 resolve_session 的 404 分支。
func TestDismissColonyPivotMissingSession(t *testing.T) {
	mux := sessionRoutesMux(SessionRouteDeps{Manager: NewSessionManager()})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/missing/dismiss-colony-pivot", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "Session 'missing' not found" {
		t.Fatalf("status/error = %d/%#v, want 404", recorder.Code, body["error"])
	}
}

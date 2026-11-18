package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

func sessionRoutesMux(deps SessionRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterSessionRoutes(mux, deps)
	return mux
}

func doSessionJSON(t *testing.T, mux *http.ServeMux, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

func makeSessionDir(t *testing.T, home hivehome.Home, queen, sid string, meta map[string]any) string {
	t.Helper()
	dir := home.QueenSessionDir(queen, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if meta != nil {
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("marshal meta: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	return dir
}

func markSessionWithMessages(t *testing.T, sessionDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(sessionDir, "conversations"), 0o755); err != nil {
		t.Fatalf("mkdir conversations: %v", err)
	}
	summary := `{"message_count": 1, "last_message": "hi", "last_active_at": 5.0}`
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}
}

// Parity: tests/test_session_titles.py::test_title_route_sets_and_clears_a_users_title
func TestTitleRouteSetsAndClearsAUsersTitle(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	dir := makeSessionDir(t, home, "queen_x", "session_20261017_090000_aaaa1111", map[string]any{
		"queen_id": "queen_x",
	})
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	target := "/api/sessions/session_20261017_090000_aaaa1111/title"

	setRecorder, setBody := doSessionJSON(t, mux, http.MethodPatch, target, `{"title": "  Kyoto trip  "}`)
	if setRecorder.Code != http.StatusOK {
		t.Fatalf("set title status = %d, want 200", setRecorder.Code)
	}
	if setBody["title"] != "Kyoto trip" {
		t.Fatalf("set title = %v, want %q", setBody["title"], "Kyoto trip")
	}

	_, cleared := doSessionJSON(t, mux, http.MethodPatch, target, `{"title": ""}`)
	if value, present := cleared["title"]; !present || value != nil {
		t.Fatalf("cleared title = %v (present=%v), want null", value, present)
	}

	if title, ok := ReadTitle(dir); ok || title != "" {
		t.Fatalf("ReadTitle after clear = %q, %v; want empty", title, ok)
	}
}

// Parity-exempt: Go 边界覆盖；上游仅测「DM 会话可改名」，未覆盖非 DM/非字符串分支。
func TestTitleRouteRejectsNonDMAndNonStringTitle(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})

	recorder, body := doSessionJSON(t, mux, http.MethodPatch,
		"/api/sessions/session_20260101_000000_deadbeef/title", `{"title": "x"}`)
	if recorder.Code != http.StatusNotFound || body["error"] != "No such queen conversation" {
		t.Fatalf("unknown session = %d %v, want 404 No such queen conversation", recorder.Code, body)
	}

	sid := "session_20261017_090000_aaaa1111"
	colonyDir := home.ColonyQueenSessionDir("acme", "queen_x", sid)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony session: %v", err)
	}
	recorder, body = doSessionJSON(t, mux, http.MethodPatch,
		"/api/sessions/"+sid+"/title", `{"title": "x"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("colony session title = %d %v, want 404", recorder.Code, body)
	}

	dmDir := makeSessionDir(t, home, "queen_x", "session_20261018_090000_bbbb2222", nil)
	recorder, body = doSessionJSON(t, mux, http.MethodPatch,
		"/api/sessions/session_20261018_090000_bbbb2222/title", `{"title": 7}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "title must be a string" {
		t.Fatalf("non-string title = %d %v, want 400", recorder.Code, body)
	}
	if _, err := os.Stat(filepath.Join(dmDir, "meta.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected title wrote meta.json: %v", err)
	}
}

// Parity-exempt: Go 边界覆盖；上游让 write_title 的 OSError 逃逸到 aiohttp
func TestTitleRouteReturnsGeneric500WhenWriteFails(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	dir := makeSessionDir(t, home, "queen_x", "session_20261017_090000_cccc3333", nil)
	if err := os.MkdirAll(filepath.Join(dir, "meta.json"), 0o755); err != nil {
		t.Fatalf("mkdir meta placeholder: %v", err)
	}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	recorder, body := doSessionJSON(t, mux, http.MethodPatch,
		"/api/sessions/session_20261017_090000_cccc3333/title", `{"title": "x"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("write failure status = %d, want 500", recorder.Code)
	}
	if body["error"] != internalServerError {
		t.Fatalf("write failure body = %v, want the generic envelope", body)
	}
}

// Parity-exempt: 上游经 test_session_fork.py 间接使用 /api/sessions/history；
func TestSessionHistoryListsColdSessionsWithLiveOverlay(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	makeSessionDir(t, home, "queen_a", "session_20260101_000000_aaaa0001", nil)
	makeSessionDir(t, home, "queen_b", "session_20260102_000000_bbbb0002", map[string]any{
		"title": "second",
	})
	makeSessionDir(t, home, "queen_a", "session_20260103_000000_cccc0003", map[string]any{
		"colony_fork": true,
	})

	manager := NewSessionManager()
	live := NewSession("session_20260101_000000_aaaa0001", nil)
	live.ColonyID = "acme"
	live.WorkerPath = "/hive/colonies/acme"
	manager.AddSession(live)

	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/sessions/history", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("history status = %d, want 200", recorder.Code)
	}
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("history sessions = %d, want 2", len(sessions))
	}
	first, _ := sessions[0].(map[string]any)
	second, _ := sessions[1].(map[string]any)
	if first["session_id"] != "session_20260102_000000_bbbb0002" {
		t.Fatalf("first session = %v, want newest first", first["session_id"])
	}
	if first["title"] != "second" {
		t.Fatalf("first title = %v, want second", first["title"])
	}
	if first["cold"] != true || first["live"] != false {
		t.Fatalf("cold session flags = cold:%v live:%v, want true/false", first["cold"], first["live"])
	}
	if second["session_id"] != "session_20260101_000000_aaaa0001" {
		t.Fatalf("second session = %v", second["session_id"])
	}
	if second["cold"] != false || second["live"] != true {
		t.Fatalf("live overlay flags = cold:%v live:%v, want false/true", second["cold"], second["live"])
	}
	if second["agent_name"] != "acme" || second["agent_path"] != "/hive/colonies/acme" {
		t.Fatalf("live overlay agent = %v/%v", second["agent_name"], second["agent_path"])
	}
}

// Parity-exempt: Go 边界覆盖；上游 list_colony_sessions 无独立测试。
func TestColonySessionsRejectInvalidColonyID(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if got := validateColonyIDSegment(bad); got != "" {
			t.Fatalf("validateColonyIDSegment(%q) = %q, want rejection", bad, got)
		}
	}
	for _, good := range []string{"acme", "acme-2", "acme_3", "Acme.4"} {
		if got := validateColonyIDSegment(good); got != good {
			t.Fatalf("validateColonyIDSegment(%q) = %q, want passthrough", good, got)
		}
	}

	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	recorder, _ := doSessionJSON(t, mux, http.MethodGet, "/api/colonies/..%2Fsessions", "")
	if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotFound {
		t.Fatalf("traversal colony id = %d, want a rejection", recorder.Code)
	}
}

// Parity-exempt: Go 边界覆盖；上游 list_colony_sessions 无独立测试。
func TestColonySessionsListIncludesColonyForks(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	dir := home.ColonyQueenSessionDir("acme", "queen_x", "session_20260101_000000_aaaa0001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir colony session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"colony_fork": {"session_id": "s0"}}`), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/colonies/acme/sessions", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("colony sessions status = %d, want 200", recorder.Code)
	}
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("colony sessions = %d, want 1 (colony_fork kept)", len(sessions))
	}

	recorder, body = doSessionJSON(t, mux, http.MethodGet, "/api/colonies/nope/sessions", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("unknown colony status = %d, want 200", recorder.Code)
	}
	if sessions, _ := body["sessions"].([]any); len(sessions) != 0 {
		t.Fatalf("unknown colony sessions = %v, want empty", sessions)
	}
}

// Parity-exempt: Go 边界覆盖；上游 get_colony_active_session 无独立测试。
func TestActiveColonySessionSkipsEmptySessions(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	emptyDir := home.ColonyQueenSessionDir("acme", "queen_x", "session_20260102_000000_bbbb0002")
	withMsgs := home.ColonyQueenSessionDir("acme", "queen_x", "session_20260101_000000_aaaa0001")
	for _, dir := range []string{emptyDir, withMsgs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir colony session: %v", err)
		}
	}
	markSessionWithMessages(t, withMsgs)

	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/colonies/acme/active-session", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("active-session status = %d, want 200", recorder.Code)
	}
	entry, ok := body["session"].(map[string]any)
	if !ok {
		t.Fatalf("active session = %v, want an object", body["session"])
	}
	if entry["session_id"] != "session_20260101_000000_aaaa0001" {
		t.Fatalf("active session = %v, want the one with messages", entry["session_id"])
	}

	removeTarget := sessionRoutesMux(SessionRouteDeps{Home: home})
	if err := os.RemoveAll(withMsgs); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	_, body = doSessionJSON(t, removeTarget, http.MethodGet, "/api/colonies/acme/active-session", "")
	if value, present := body["session"]; !present || value != nil {
		t.Fatalf("active session = %v (present=%v), want null", value, present)
	}
}

// Parity-exempt: Go 边界覆盖；上游删除路由无独立测试。
func TestDeleteHistorySessionRemovesDirAndStopsLiveSession(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sid := "session_20260101_000000_aaaa0001"
	dir := makeSessionDir(t, home, "queen_a", sid, nil)

	manager := NewSessionManager()
	manager.AddSession(NewSession(sid, nil))

	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/history/"+sid, "")
	if recorder.Code != http.StatusOK || body["deleted"] != sid {
		t.Fatalf("delete = %d %v, want 200 deleted", recorder.Code, body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists: %v", err)
	}
	if manager.GetSession(sid) != nil {
		t.Fatalf("live session was not stopped")
	}

	recorder, body = doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/history/session_20260101_000000_ffff9999", "")
	if recorder.Code != http.StatusOK || body["deleted"] != "session_20260101_000000_ffff9999" {
		t.Fatalf("unknown delete = %d %v, want 200", recorder.Code, body)
	}
}

// Parity-exempt: Go-only path guard; reveal must not create/open a folder
// outside HIVE_HOME/queens when the session id contains traversal.
func TestRevealSessionRejectsTraversalID(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	opened := ""
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/escape/reveal", nil)
	request.SetPathValue("session_id", "../escape")
	recorder := httptest.NewRecorder()
	handleRevealSession(recorder, request, SessionRouteDeps{
		Home:   home,
		Reveal: func(path string) error { opened = path; return nil },
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if opened != "" {
		t.Fatalf("reveal opened %q", opened)
	}
	if _, err := os.Stat(filepath.Join(home.QueensDir(), "default", "escape")); err == nil {
		t.Fatal("traversal session directory was created")
	}
}

// Parity-exempt: Go-only path guard; delete must not rmtree a directory
// outside HIVE_HOME/queens when the session id contains traversal.
func TestDeleteHistorySessionRejectsTraversalID(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	victim := filepath.Join(home.QueensDir(), "default", "victim")
	mkdirTestDir(t, victim)
	if err := os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/sessions/history/escape", nil)
	request.SetPathValue("session_id", "../victim")
	recorder := httptest.NewRecorder()
	handleDeleteHistorySession(recorder, request, SessionRouteDeps{Home: home})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
		t.Fatalf("traversal delete removed the victim directory: %v", err)
	}
}

// Parity-exempt: Go 边界覆盖；上游 reveal 路由无独立测试。
func TestRevealSessionCreatesFolderAndOpensIt(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sid := "session_20260101_000000_aaaa0001"

	var opened string
	mux := sessionRoutesMux(SessionRouteDeps{
		Home:   home,
		Reveal: func(path string) error { opened = path; return nil },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/"+sid+"/reveal", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("reveal status = %d, want 200", recorder.Code)
	}
	want := filepath.Join(home.QueensDir(), "default", "sessions", sid)
	if body["path"] != want {
		t.Fatalf("reveal path = %v, want %q", body["path"], want)
	}
	if opened != want {
		t.Fatalf("opened = %q, want %q", opened, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("reveal did not create the folder: %v", err)
	}

	manager := NewSessionManager()
	live := NewSession("session_20260202_000000_bbbb0002", nil)
	live.QueenResumeFrom = sid
	live.QueenName = "queen_z"
	manager.AddSession(live)
	mux = sessionRoutesMux(SessionRouteDeps{
		Home:    home,
		Manager: manager,
		Reveal:  func(path string) error { opened = path; return nil },
	})
	_, body = doSessionJSON(t, mux, http.MethodPost, "/api/sessions/session_20260202_000000_bbbb0002/reveal", "")
	want = home.QueenSessionDir("queen_z", sid)
	if body["path"] != want {
		t.Fatalf("live reveal path = %v, want %q", body["path"], want)
	}
	if opened != want {
		t.Fatalf("live opened = %q, want %q", opened, want)
	}

	failMux := sessionRoutesMux(SessionRouteDeps{
		Home:   home,
		Reveal: func(string) error { return errors.New("no launcher") },
	})
	recorder, body = doSessionJSON(t, failMux, http.MethodPost, "/api/sessions/"+sid+"/reveal", "")
	if recorder.Code != http.StatusInternalServerError || body["error"] != "no launcher" {
		t.Fatalf("reveal failure = %d %v, want 500 no launcher", recorder.Code, body)
	}
}

// Parity-exempt: Go 边界覆盖；上游删除路由无独立测试。shutil.rmtree 先过
func TestDeleteHistorySessionRefusesASymlinkedDir(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sid := "session_20260101_000000_aaaa0001"
	real := filepath.Join(home.Root, "real_session")
	mkdirTestDir(t, real)
	linkDir := filepath.Join(home.QueensDir(), "queen_a", "sessions")
	mkdirTestDir(t, linkDir)
	if err := os.Symlink(real, filepath.Join(linkDir, sid)); err != nil {
		t.Fatalf("symlink session: %v", err)
	}

	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/sessions/history/"+sid, "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("symlinked delete status = %d, want 500", recorder.Code)
	}
	if body["error"] != "Failed to delete session: Cannot call rmtree on a symbolic link" {
		t.Fatalf("symlinked delete body = %v, want the rmtree refusal", body)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("rmtree refusal removed the target: %v", err)
	}
}

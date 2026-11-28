package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/claudecode"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/queenmail"
)

const (
	dashboardQueenID   = "queen_claude"
	dashboardSessionID = "cc-session-1"
)

// dashboardSessionLines mirrors the session file upstream's dashboard test
// writes (SESSION_LINES in test_claude_code_queens.py).
var dashboardSessionLines = []string{
	`{"type": "user", "timestamp": "2026-10-25T10:00:00Z", "message": {"role": "user", "content": "[Ava(queen_ava)]: Where is the north shore?"}}`,
	`{"type": "user", "isMeta": true, "timestamp": "2026-10-25T10:00:00Z", "message": {"role": "user", "content": "caveat"}}`,
	`{"type": "attachment", "timestamp": "2026-10-25T10:00:00Z"}`,
	`{"type": "assistant", "timestamp": "2026-10-25T10:00:01Z", "message": {"role": "assistant", "content": [` +
		`{"type": "thinking", "thinking": "Maybe the atlas."}, {"type": "text", "text": "Let me look."}, ` +
		`{"type": "tool_use", "name": "Bash", "input": {"command": "ls maps"}}]}}`,
	`{"type": "user", "timestamp": "2026-10-25T10:00:02Z", "message": {"role": "user", "content": [{"type": "tool_result", "content": "north.txt", "is_error": false}]}}`,
	`{"type": "assistant", "isSidechain": true, "timestamp": "2026-10-25T10:00:03Z", "message": {"role": "assistant", "content": [{"type": "text", "text": "subagent chatter"}]}}`,
	`{"type": "assistant", "timestamp": "2026-10-25T10:00:04Z", "message": {"role": "assistant", "content": [{"type": "text", "text": "It is at 61°N."}]}}`,
}

// newClaudeQueenHome writes a Hive home with one Claude Code queen configured
// (queen mail on or off) and one conversation of hers with Ava, whose Claude
// Code session file holds the letters and what she did with them.
func newClaudeQueenHome(t *testing.T, enabled bool) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, home.ConfigurationFile(), fmt.Sprintf(
		`{"queen_mail": {"enabled": %t, "claude_code": {"%s": {"name": "Claude", "title": "Engineer", "api_key": "sk-ant-secret"}}}}`,
		enabled, dashboardQueenID))
	// Ava is a Hive queen, so the conversation says who it is with by name.
	writeTestFile(t, filepath.Join(home.Root, "queens", "queen_ava", "profile.yaml"), "name: Ava\ntitle: Cartographer\n")
	writeTestFile(t, filepath.Join(home.Root, "claude_code", dashboardQueenID, "conversations.json"),
		`{"conversations": {"`+dashboardSessionID+`": {"peer": "queen_ava", "reply_to": "session_ava", "started": true}}}`)
	writeTestFile(t, filepath.Join(home.Root, "claude_code", dashboardQueenID, "config", "projects", "some-project", dashboardSessionID+".jsonl"),
		strings.Join(dashboardSessionLines, "\n")+"\n")
	return home
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dashboardMux is the routes over the queen-mail service a Hive home builds,
// the same service NewApp wires.
func dashboardMux(t *testing.T, home hivehome.Home) *http.ServeMux {
	t.Helper()
	mail := queenmail.New(home, queenmail.LoadConfig(home), nil)
	store := claudecode.NewStore(home)
	mail.SetClaudeInboxes(store)
	return claudeCodeTestMux(NewClaudeCodeQueenRouteDeps(mail, store, nil))
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_dashboard_lists_her_conversations_and_reads_one
func TestTheDashboardListsTheConfiguredQueensAndReadsOne(t *testing.T) {
	home := newClaudeQueenHome(t, true)
	mux := dashboardMux(t, home)

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), "sk-ant-secret") {
		t.Fatalf("the list carries her API key: %s", listed.Body.String())
	}
	var listBody map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	queens, _ := listBody["queens"].([]any)
	if len(queens) != 1 {
		t.Fatalf("queens = %#v", listBody["queens"])
	}
	queen := queens[0].(map[string]any)
	if queen["id"] != dashboardQueenID || queen["name"] != "Claude" || queen["title"] != "Engineer" ||
		queen["permission_mode"] != "default" || queen["auth"] != "api_key" {
		t.Fatalf("queen = %#v", queen)
	}
	if model, present := queen["model"]; !present || model != nil {
		t.Fatalf("model = %#v (present %v), want null", model, present)
	}
	if !reflect.DeepEqual(queen["conversations"], []any{
		map[string]any{
			"session_id": dashboardSessionID,
			"peer":       "queen_ava",
			"peer_name":  "Ava",
			"running":    false,
			"letters":    float64(1),
			"last_at":    "2026-10-25T10:00:04Z",
		},
	}) {
		t.Fatalf("conversations = %#v", queen["conversations"])
	}

	transcript := httptest.NewRecorder()
	mux.ServeHTTP(transcript, httptest.NewRequest(http.MethodGet,
		"/api/claude-code-queens/"+dashboardQueenID+"/conversations/"+dashboardSessionID, nil))
	if transcript.Code != http.StatusOK {
		t.Fatalf("transcript status = %d, body = %s", transcript.Code, transcript.Body.String())
	}
	var transcriptBody map[string]any
	if err := json.Unmarshal(transcript.Body.Bytes(), &transcriptBody); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	entries, _ := transcriptBody["entries"].([]any)
	if len(entries) != 5 {
		t.Fatalf("entries = %#v", transcriptBody["entries"])
	}
	wantKinds := []string{"letter", "text", "tool", "result", "text"}
	for index, want := range wantKinds {
		entry := entries[index].(map[string]any)
		if entry["kind"] != want {
			t.Fatalf("entry %d kind = %#v, want %q", index, entry["kind"], want)
		}
	}
	first := entries[0].(map[string]any)
	if first["text"] != "[Ava(queen_ava)]: Where is the north shore?" || first["at"] != "2026-10-25T10:00:00Z" {
		t.Fatalf("first entry = %#v", first)
	}
	tool := entries[2].(map[string]any)
	if tool["tool"] != "Bash" || tool["text"] != `{"command": "ls maps"}` {
		t.Fatalf("tool entry = %#v", tool)
	}
	result := entries[3].(map[string]any)
	if result["error"] != false || result["text"] != "north.txt" {
		t.Fatalf("result entry = %#v", result)
	}

	unknownSession := httptest.NewRecorder()
	mux.ServeHTTP(unknownSession, httptest.NewRequest(http.MethodGet,
		"/api/claude-code-queens/"+dashboardQueenID+"/conversations/no-such-session", nil))
	if unknownSession.Code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d", unknownSession.Code)
	}
	var missing map[string]any
	if err := json.Unmarshal(unknownSession.Body.Bytes(), &missing); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if missing["error"] != "No conversation 'no-such-session' of 'queen_claude'" {
		t.Fatalf("404 body = %#v", missing)
	}

	unknownQueen := httptest.NewRecorder()
	mux.ServeHTTP(unknownQueen, httptest.NewRequest(http.MethodGet,
		"/api/claude-code-queens/queen_zed/conversations/"+dashboardSessionID, nil))
	if unknownQueen.Code != http.StatusNotFound {
		t.Fatalf("unknown queen status = %d", unknownQueen.Code)
	}
}

// Parity-exempt: 上游没有覆盖 queen_mail.enabled=false 的两个 handler；这里钉住
// list 为空、但已配置 queen 的 transcript 仍读得到（上游按 `mail is None` 判断，
// 不按 enabled）。
func TestTheDashboardIsEmptyWithQueenMailOffYetReadsAConfiguredQueensTranscript(t *testing.T) {
	home := newClaudeQueenHome(t, false)
	mux := dashboardMux(t, home)

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d", listed.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if queens, ok := body["queens"].([]any); !ok || len(queens) != 0 {
		t.Fatalf("queens = %#v", body["queens"])
	}

	transcript := httptest.NewRecorder()
	mux.ServeHTTP(transcript, httptest.NewRequest(http.MethodGet,
		"/api/claude-code-queens/"+dashboardQueenID+"/conversations/"+dashboardSessionID, nil))
	if transcript.Code != http.StatusOK {
		t.Fatalf("transcript status = %d, body = %s", transcript.Code, transcript.Body.String())
	}
}

// Parity-exempt: Go 装配分支；没有 queen-mail 服务时等同上游 `mail is None`。
func TestTheDashboardWithoutQueenMailRuntimeIsEmpty(t *testing.T) {
	mux := claudeCodeTestMux(NewClaudeCodeQueenRouteDeps(nil, nil, nil))

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d", listed.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if queens, ok := body["queens"].([]any); !ok || len(queens) != 0 {
		t.Fatalf("queens = %#v", body["queens"])
	}

	transcript := httptest.NewRecorder()
	mux.ServeHTTP(transcript, httptest.NewRequest(http.MethodGet,
		"/api/claude-code-queens/"+dashboardQueenID+"/conversations/"+dashboardSessionID, nil))
	if transcript.Code != http.StatusNotFound {
		t.Fatalf("transcript status = %d", transcript.Code)
	}
}

// Parity-exempt: 装配守卫。NewApp 必须把真实 queen-mail 服务接进只读视图（列表与
// transcript 都读盘），而不是空壳 deps。
func TestNewAppServesTheConfiguredClaudeCodeQueens(t *testing.T) {
	home := newClaudeQueenHome(t, true)
	app := newAppForTest(t, AppDeps{Home: home})

	code, body, _ := getJSON(t, app, "/api/claude-code-queens")
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	queens, _ := body["queens"].([]any)
	if len(queens) != 1 {
		t.Fatalf("queens = %#v", body["queens"])
	}
	if queen := queens[0].(map[string]any); queen["id"] != dashboardQueenID || queen["name"] != "Claude" {
		t.Fatalf("queen = %#v", queen)
	}

	code, body, _ = getJSON(t, app, "/api/claude-code-queens/"+dashboardQueenID+"/conversations/"+dashboardSessionID)
	if code != http.StatusOK {
		t.Fatalf("transcript status = %d, body = %#v", code, body)
	}
	if entries, _ := body["entries"].([]any); len(entries) != 5 {
		t.Fatalf("entries = %#v", body["entries"])
	}

	// A ".." segment in an API path is a 400, not a redirect out of the API.
	traversal := httptest.NewRecorder()
	app.ServeHTTP(traversal, httptest.NewRequest(http.MethodGet, "/api/claude-code-queens/../queens", nil))
	if traversal.Code != http.StatusBadRequest {
		t.Fatalf("dot-segment status = %d, body = %s", traversal.Code, traversal.Body.String())
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py — the dashboard
// lists the configured Claude Code queens in configuration order, matching
// config.claude_code.values().
func TestClaudeQueenListKeepsConfigurationOrder(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, home.ConfigurationFile(), `{"queen_mail": {"enabled": true, "claude_code": {
		"queen_zulu": {"name": "Zulu"},
		"queen_alpha": {"name": "Alpha"},
		"queen_mike": {"name": "Mike"}
	}}}`)
	mail := queenmail.New(home, queenmail.LoadConfig(home), nil)
	deps := NewClaudeCodeQueenRouteDeps(mail, claudecode.NewStore(home), nil)
	if deps.Queens == nil {
		t.Fatal("queen view not built")
	}
	var ids []string
	for _, queen := range deps.Queens() {
		ids = append(ids, queen.ID)
	}
	want := []string{"queen_zulu", "queen_alpha", "queen_mike"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("queen order = %v, want %v", ids, want)
	}
}

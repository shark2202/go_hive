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

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

type executionTestNode struct {
	calls         []string
	stopped       bool
	resumed       bool
	injectCalls   []string
	clientInputs  []bool
	injectErr     error
	userStopCalls int
}

func (n *executionTestNode) InjectEvent(text string, isClientInput bool) error {
	n.injectCalls = append(n.injectCalls, text)
	n.clientInputs = append(n.clientInputs, isClientInput)
	return n.injectErr
}

func (n *executionTestNode) MarkUserStopped() {
	n.calls = append(n.calls, "mark_user_stopped")
	n.stopped = true
	n.userStopCalls++
}

func (n *executionTestNode) CancelCurrentTurn() []<-chan struct{} {
	n.calls = append(n.calls, "cancel_current_turn")
	return nil
}

func (n *executionTestNode) ResumeFromUserStop() {
	n.resumed = true
	n.stopped = false
}

type executionTestColony struct {
	workers map[string]any
	err     error
	calls   []bool
}

func (c *executionTestColony) StreamID() string { return "colony:acme" }

func (c *executionTestColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "", nil
}

func (c *executionTestColony) InjectInput(string, string) error { return nil }

func (c *executionTestColony) StopWorkers(blockDispatch bool) (map[string]any, error) {
	c.calls = append(c.calls, blockDispatch)
	return c.workers, c.err
}

func executionDeps(t *testing.T) (ExecutionRouteDeps, hivehome.Home) {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return ExecutionRouteDeps{Home: home, Manager: NewSessionManager()}, home
}

func executionMux(deps ExecutionRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterExecutionRoutes(mux, deps)
	return mux
}

// Parity: tests/test_cancel_user_stop_flow.py::test_cancel_queen_marks_user_stop_before_cancelling
func TestCancelQueenMarksUserStopBeforeCancelling(t *testing.T) {
	deps, _ := executionDeps(t)
	node := &executionTestNode{}
	deps.Manager.AddSession(&Session{ID: "sid-1", QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}}})
	recorder, body := serveColonyRequest(executionMux(deps), httptest.NewRequest(http.MethodPost, "/api/sessions/sid-1/cancel-queen", nil))
	if recorder.Code != http.StatusOK || body["cancelled"] != true {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
	if len(node.calls) != 2 || node.calls[0] != "mark_user_stopped" || node.calls[1] != "cancel_current_turn" {
		t.Fatalf("calls = %v", node.calls)
	}
	if !node.stopped {
		t.Fatalf("node should be marked stopped")
	}
}

// Parity: tests/test_cancel_user_stop_flow.py::test_cancel_queen_returns_200_with_no_queen
func TestCancelQueenReturns200WithNoQueen(t *testing.T) {
	deps, _ := executionDeps(t)
	deps.Manager.AddSession(&Session{ID: "sid-1"})
	recorder, body := serveColonyRequest(executionMux(deps), httptest.NewRequest(http.MethodPost, "/api/sessions/sid-1/cancel-queen", nil))
	if recorder.Code != http.StatusOK || body["cancelled"] != false || body["error"] != "Queen not active" {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
}

// Parity: tests/test_cancel_user_stop_flow.py::test_presence_does_not_resume_user_stopped_agent
func TestPresenceDoesNotResumeUserStoppedAgent(t *testing.T) {
	deps, _ := executionDeps(t)
	node := &executionTestNode{stopped: true}
	deps.Manager.AddSession(&Session{ID: "sid-1", QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}}})
	recorder, body := serveColonyRequest(executionMux(deps), httptest.NewRequest(http.MethodPost, "/api/sessions/sid-1/presence", nil))
	if recorder.Code != http.StatusOK || body["ok"] != true || body["resumed"] != false {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
	if node.resumed || !node.stopped || node.userStopCalls != 0 {
		t.Fatalf("presence mutated user-stop: resumed=%v stopped=%v calls=%d", node.resumed, node.stopped, node.userStopCalls)
	}
}

// Parity: tests/test_cancel_user_stop_flow.py::test_presence_no_queen_returns_200
func TestPresenceNoQueenReturns200(t *testing.T) {
	deps, _ := executionDeps(t)
	deps.Manager.AddSession(&Session{ID: "sid-1"})
	recorder, _ := serveColonyRequest(executionMux(deps), httptest.NewRequest(http.MethodPost, "/api/sessions/sid-1/presence", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}

// Parity-exempt: 上游无 cancel-queen 的 colony 级联测试；此用例钉住 StopWorkers(true) 与载荷。
func TestCancelQueenCascadesToColonyWorkers(t *testing.T) {
	deps, _ := executionDeps(t)
	node := &executionTestNode{}
	colony := &executionTestColony{workers: map[string]any{"worker-1": "stopped"}}
	deps.Manager.AddSession(&Session{
		ID:            "sid-1",
		QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}},
		Colony:        colony,
	})
	recorder, body := serveColonyRequest(executionMux(deps), httptest.NewRequest(http.MethodPost, "/api/sessions/sid-1/cancel-queen", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	workers, _ := body["workers"].(map[string]any)
	if workers["worker-1"] != "stopped" {
		t.Fatalf("workers = %v", body["workers"])
	}
	if len(colony.calls) != 1 || colony.calls[0] != true {
		t.Fatalf("StopWorkers calls = %v", colony.calls)
	}

	colony.err = errors.New("worker cascade broke")
	_, errorBody := serveColonyRequest(executionMux(deps), httptest.NewRequest(http.MethodPost, "/api/sessions/sid-1/cancel-queen", nil))
	errorWorkers, _ := errorBody["workers"].(map[string]any)
	if errorWorkers["error"] != "worker cascade broke" {
		t.Fatalf("error workers = %v", errorBody["workers"])
	}
}

// Parity-exempt: /queen-context 无 Python 测试；此用例钉住队列注入（非 client input）与复活分支。
func TestQueenContextQueuesAndRevives(t *testing.T) {
	deps, _ := executionDeps(t)
	node := &executionTestNode{}
	deps.Manager.AddSession(&Session{ID: "sid-1", QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}}})
	recorder, body := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-1/queen-context", map[string]any{"message": "context"}))
	if recorder.Code != http.StatusOK || body["status"] != "queued" {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
	if len(node.injectCalls) != 1 || node.injectCalls[0] != "context" || node.clientInputs[0] != false {
		t.Fatalf("inject = %v %v", node.injectCalls, node.clientInputs)
	}

	dead := &Session{ID: "sid-2"}
	deps.Manager.AddSession(dead)
	revivedNode := &executionTestNode{}
	deps.ReviveQueen = func(session *Session) error {
		session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": revivedNode}}
		return nil
	}
	reviveRecorder, reviveBody := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-2/queen-context", map[string]any{"message": "revive me"}))
	if reviveRecorder.Code != http.StatusOK || reviveBody["status"] != "queued_revived" {
		t.Fatalf("revive = %d %v", reviveRecorder.Code, reviveBody)
	}
	if len(revivedNode.injectCalls) != 1 || revivedNode.injectCalls[0] != "revive me" {
		t.Fatalf("revived inject = %v", revivedNode.injectCalls)
	}

	emptyRecorder, emptyBody := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-1/queen-context", map[string]any{"message": ""}))
	if emptyRecorder.Code != http.StatusBadRequest || emptyBody["error"] != "message is required" {
		t.Fatalf("empty = %d %v", emptyRecorder.Code, emptyBody)
	}
}

// Parity-exempt: /queen-context 无 Python 测试；此用例钉住无 executor 且无法复活时 503。
func TestQueenContextUnavailable503(t *testing.T) {
	deps, _ := executionDeps(t)
	deps.Manager.AddSession(&Session{ID: "sid-1"})
	recorder, body := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-1/queen-context", map[string]any{"message": "hello"}))
	if recorder.Code != http.StatusServiceUnavailable || body["error"] != "Queen not available" {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
}

// Parity-exempt: /queen-context 无 Python 测试；此用例钉住注入失败返回 500。
func TestQueenContextInjectionFailure500(t *testing.T) {
	deps, _ := executionDeps(t)
	node := &executionTestNode{injectErr: errors.New("inject boom")}
	deps.Manager.AddSession(&Session{ID: "sid-1", QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}}})
	recorder, body := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-1/queen-context", map[string]any{"message": "hello"}))
	if recorder.Code != http.StatusInternalServerError || body["error"] != "inject boom" {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
}

// Parity-exempt: /record-message 无 Python 测试；此用例钉住 part、cursor 与 events.jsonl 三处落盘。
func TestRecordUserMessagePersistsPartCursorAndEvent(t *testing.T) {
	deps, home := executionDeps(t)
	sessionID := "session_20261118_120000_abcd1234"
	sessionDir := home.QueenSessionDir("default", sessionID)
	conversations := filepath.Join(sessionDir, "conversations")
	if err := os.MkdirAll(conversations, 0o755); err != nil {
		t.Fatal(err)
	}
	store := agentloop.NewFileConversationStore(conversations)
	if err := store.WritePart(0, map[string]any{"seq": 0, "role": "assistant", "content": "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePart(1, map[string]any{"seq": 1, "role": "assistant", "content": "there"}); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	lines := "{\"seq\": 4, \"type\": \"x\"}\nnot json\n{\"seq\": \"9\", \"type\": \"y\"}\n"
	if err := os.WriteFile(eventsPath, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	recorder, body := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/"+sessionID+"/record-message", map[string]any{"message": "picked option"}))
	if recorder.Code != http.StatusOK || body["status"] != "recorded" || body["seq"] != float64(2) {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
	parts, err := store.ReadParts()
	if err != nil || len(parts) != 3 {
		t.Fatalf("parts = %v, err %v", parts, err)
	}
	last := parts[2]
	if last["role"] != agentloop.RoleUser || last["content"] != "picked option" || last["is_client_input"] != true {
		t.Fatalf("last part = %v", last)
	}
	cursor, _ := store.ReadCursor()
	if cursor["next_seq"] != float64(3) && cursor["next_seq"] != 3 {
		t.Fatalf("cursor next_seq = %v", cursor["next_seq"])
	}
	if value, present := cursor["pending_input"]; !present || value != nil {
		t.Fatalf("pending_input = %v (present %v), want null", value, present)
	}

	eventData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	eventLines := strings.Split(strings.TrimSpace(string(eventData)), "\n")
	if len(eventLines) != 4 {
		t.Fatalf("event lines = %d: %q", len(eventLines), eventData)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(eventLines[3]), &event); err != nil {
		t.Fatalf("event json: %v", err)
	}
	if event["type"] != "client_input_received" || event["execution_id"] != sessionID || event["seq"] != float64(5) {
		t.Fatalf("event = %v", event)
	}
	data, _ := event["data"].(map[string]any)
	if data["content"] != "picked option" || data["image_count"] != float64(0) {
		t.Fatalf("event data = %v", data)
	}
}

// Parity-exempt: /record-message 无 Python 测试；此用例钉住未知会话 404 与空消息 400。
func TestRecordUserMessageValidation(t *testing.T) {
	deps, _ := executionDeps(t)
	mux := executionMux(deps)
	missingRecorder, missingBody := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/sessions/session_20260101_000000_deadbeef/record-message", map[string]any{"message": "hi"}))
	if missingRecorder.Code != http.StatusNotFound || missingBody["error"] != "session not found" {
		t.Fatalf("missing = %d %v", missingRecorder.Code, missingBody)
	}
	deps.Manager.AddSession(&Session{ID: "sid-1"})
	emptyRecorder, emptyBody := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-1/record-message", map[string]any{"message": ""}))
	if emptyRecorder.Code != http.StatusBadRequest || emptyBody["error"] != "message is required" {
		t.Fatalf("empty = %d %v", emptyRecorder.Code, emptyBody)
	}
}

// Parity-exempt: /mark-colony-spawned 无 Python 测试；此用例钉住 meta.json 合并与 Session 缓存。
func TestMarkColonySpawnedWritesMeta(t *testing.T) {
	deps, _ := executionDeps(t)
	queenDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(queenDir, "meta.json"), []byte(`{"queen_id":"queen_x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &Session{ID: "sid-1", QueenDir: queenDir}
	deps.Manager.AddSession(session)
	recorder, body := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-1/mark-colony-spawned", map[string]any{"colony_id": "acme"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if body["colony_spawned"] != true || body["spawned_colony_id"] != "acme" {
		t.Fatalf("body = %v", body)
	}
	meta, err := readJSONObject(filepath.Join(queenDir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if meta["queen_id"] != "queen_x" || meta["colony_spawned"] != true || meta["spawned_colony_id"] != "acme" {
		t.Fatalf("meta = %v", meta)
	}
	if timestamp, _ := meta["spawned_colony_at"].(string); !strings.HasSuffix(timestamp, "+00:00") {
		t.Fatalf("spawned_colony_at = %q", meta["spawned_colony_at"])
	}
	if !session.ColonySpawned || session.SpawnedColonyID != "acme" {
		t.Fatalf("session not mirrored: %+v", session)
	}

	missingRecorder, missingBody := serveColonyRequest(executionMux(deps), colonyRequest(t, http.MethodPost, "/api/sessions/sid-1/mark-colony-spawned", map[string]any{}))
	if missingRecorder.Code != http.StatusBadRequest || missingBody["error"] != "colony_id is required" {
		t.Fatalf("missing = %d %v", missingRecorder.Code, missingBody)
	}
}

// Parity-exempt: 所有 execution 路由对未知 session 的统一 404 信封（上游 resolve_session）。
func TestExecutionRoutesRejectUnknownSession(t *testing.T) {
	deps, _ := executionDeps(t)
	mux := executionMux(deps)
	for _, route := range []string{"queen-context", "record-message", "cancel-queen", "presence", "mark-colony-spawned"} {
		recorder, body := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/sessions/ghost/"+route, map[string]any{"message": "x", "colony_id": "x"}))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", route, recorder.Code)
		}
		expected := "Session 'ghost' not found"
		if route == "record-message" {
			expected = "session not found"
		}
		if body["error"] != expected {
			t.Fatalf("%s error = %v", route, body["error"])
		}
	}
}

package server

// Cold-resume parity for the chat runtime.
//
// A runtime reconstructed over a conversation that already ran consults the
// cursor for the wait it must re-enter. Two upstream behaviors are covered:
//
//   * §6b1½ cold-resume mid-turn guard: a cursor with NO pending_input (the
//     crash-mid-turn shape) parks the queen on cold_interrupted until the
//     user's next message instead of silently resuming the in-flight work —
//     unless a prompt is already headed in (upstream's drained-injection
//     condition).
//   * The restored wait is re-announced on the fresh bus (upstream's restored
//     `_await_user_input` re-enters its wait), so /snapshot and the SSE first
//     frame project awaiting_input / park_reason / pending_questions instead
//     of an empty state.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// seedResumedConversation writes the on-disk shape of a session that already
// ran a turn: conversation meta, two message parts, and the cursor passed by
// the test (nil leaves the store without one).
func seedResumedConversation(t *testing.T, root string, cursor map[string]any) {
	t.Helper()
	store := agentloop.NewFileConversationStore(filepath.Join(root, "conversations"))
	if err := store.WriteMeta(map[string]any{"system_prompt": "You are the queen."}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	parts := []map[string]any{
		{"seq": 0, "role": "user", "content": "long task: run the sleep job"},
		{"seq": 1, "role": "assistant", "content": "Started it."},
	}
	for _, part := range parts {
		if err := store.WritePart(part["seq"].(int), part); err != nil {
			t.Fatalf("WritePart: %v", err)
		}
	}
	if cursor != nil {
		if err := store.WriteCursor(cursor); err != nil {
			t.Fatalf("WriteCursor: %v", err)
		}
	}
}

func newResumedChatRuntime(t *testing.T, root string, bus *eventbus.EventBus, initialInputPending bool) *ChatRuntime {
	t.Helper()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:                 &queenChatLLM{},
		Root:                root,
		Bus:                 bus,
		SessionID:           "session_20260101_000000_resume01",
		QueenName:           "queen_growth",
		InitialInputPending: initialInputPending,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// Parity: core/framework/agent_loop/agent_loop.py::execute (§6b1½ cold-resume mid-turn guard)
func TestColdResumeMidTurnWritesColdInterruptedPark(t *testing.T) {
	root := t.TempDir()
	// The crash checkpoint: the turn cleared pending_input when it started,
	// then the runtime died mid-turn (no wait persisted).
	seedResumedConversation(t, root, map[string]any{"next_seq": 2, "park_reason": "turn_done", "pending_input": nil})

	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	runtime := newResumedChatRuntime(t, root, bus, false)

	// The cursor now carries the cold-interrupt park with upstream's exact
	// fields, instead of the plain turn_done resume.
	pending := readParkPendingInput(t, root)
	if pending == nil {
		t.Fatal("cold resume did not write a park")
	}
	if pending["reason"] != "cold_interrupted" {
		t.Fatalf("pending_input reason = %#v, want cold_interrupted", pending["reason"])
	}
	if pending["emit_client_request"] != true {
		t.Fatalf("pending_input emit_client_request = %#v, want true", pending["emit_client_request"])
	}
	if pending["questions"] != nil {
		t.Fatalf("pending_input questions = %#v, want nil", pending["questions"])
	}

	// In memory the session is parked awaiting the user, on cold_interrupted.
	runtime.parkMu.Lock()
	restored := runtime.parkReason
	runtime.parkMu.Unlock()
	if restored != agentloop.ParkReasonColdInterrupted {
		t.Fatalf("restored park reason = %q, want cold_interrupted", restored)
	}
	if !runtime.awaiting.Load() {
		t.Fatal("cold-resumed runtime is not awaiting input")
	}
	if runtime.hasPendingQuestions() {
		t.Fatal("cold-interrupt park must not carry questions")
	}

	// The re-announced wait reaches the snapshot projection.
	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["awaiting_input"] != true || snapshot["park_reason"] != "cold_interrupted" {
		t.Fatalf("snapshot = %#v, want awaiting_input/cold_interrupted", snapshot)
	}
	if snapshot["queen_busy_reason"] != "awaiting_input" {
		t.Fatalf("snapshot queen_busy_reason = %#v, want awaiting_input", snapshot["queen_busy_reason"])
	}

	// ...via the same event pair a live park publishes.
	requests := recorder.ofType(eventbus.EventClientInputRequested)
	if len(requests) != 1 {
		t.Fatalf("client_input_requested count = %d, want 1 (%v)", len(requests), recorder.types())
	}
	if requests[0].Data["park_reason"] != "cold_interrupted" {
		t.Fatalf("client_input_requested park_reason = %#v, want cold_interrupted", requests[0].Data["park_reason"])
	}
	states := recorder.ofType(eventbus.EventLoopStateChanged)
	if len(states) != 1 {
		t.Fatalf("loop_state_changed count = %d, want 1 (%v)", len(states), recorder.types())
	}
	if states[0].Data["activity"] != "awaiting_user" || states[0].Data["park_reason"] != "cold_interrupted" {
		t.Fatalf("loop_state_changed data = %#v, want awaiting_user/cold_interrupted", states[0].Data)
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::execute (§6b1½ — the guard only fires when the first iteration drained no user injection)
func TestColdResumeMidTurnWithPendingPromptKeepsTurnDone(t *testing.T) {
	root := t.TempDir()
	seedResumedConversation(t, root, map[string]any{"next_seq": 2, "pending_input": nil})

	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	runtime := newResumedChatRuntime(t, root, bus, true)

	pending := readParkPendingInput(t, root)
	if pending == nil || pending["reason"] != "turn_done" {
		t.Fatalf("pending_input = %#v, want turn_done (prompt pending suppresses the guard)", pending)
	}
	runtime.parkMu.Lock()
	restored := runtime.parkReason
	runtime.parkMu.Unlock()
	if restored != agentloop.ParkReasonTurnDone {
		t.Fatalf("park reason = %q, want turn_done", restored)
	}
	// No wait was reconstructed, so nothing is re-announced.
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("prompt-pending resume published events: %v", recorder.types())
	}
	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["park_reason"] != nil || snapshot["awaiting_input"] != false {
		t.Fatalf("snapshot = %#v, want the empty fresh-bus projection", snapshot)
	}
}

// Parity: core/framework/agent_loop/internals/cursor_persistence.py::restore (message_count > 0 gate)
func TestColdResumeShapeRequiresConversationHistory(t *testing.T) {
	root := t.TempDir()
	// A cursor without any conversation parts is not upstream's restore
	// target (restore requires messages), so it must stay turn_done.
	store := agentloop.NewFileConversationStore(filepath.Join(root, "conversations"))
	if err := store.WriteCursor(map[string]any{"next_seq": 0, "pending_input": nil}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}

	bus := eventbus.NewEventBus()
	runtime := newResumedChatRuntime(t, root, bus, false)
	pending := readParkPendingInput(t, root)
	if pending == nil || pending["reason"] != "turn_done" {
		t.Fatalf("pending_input = %#v, want turn_done without conversation history", pending)
	}
	runtime.parkMu.Lock()
	restored := runtime.parkReason
	runtime.parkMu.Unlock()
	if restored != agentloop.ParkReasonTurnDone {
		t.Fatalf("park reason = %q, want turn_done", restored)
	}
}

// Parity: core/framework/server/routes_events.py::handle_events (fresh-subscribe session_snapshot)
// Parity: core/framework/agent_loop/agent_loop.py::_await_user_input (restored wait re-announcement)
func TestRestoredAskUserParkShowsInSnapshotAndFirstSSEFrame(t *testing.T) {
	root := t.TempDir()
	seedResumedConversation(t, root, map[string]any{
		"next_seq": 2,
		"pending_input": map[string]any{
			"reason":              "ask_user",
			"emit_client_request": true,
			"questions": []any{
				map[string]any{"id": "widget_color", "prompt": "What color should the widget be?"},
			},
		},
	})

	bus := eventbus.NewEventBus()
	runtime := newResumedChatRuntime(t, root, bus, false)
	if !runtime.hasPendingQuestions() {
		t.Fatal("restarted runtime lost the persisted ask_user question")
	}

	// /snapshot projects the restored wait.
	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["awaiting_input"] != true || snapshot["park_reason"] != "ask_user" {
		t.Fatalf("snapshot = %#v, want awaiting_input/ask_user", snapshot)
	}
	questions, _ := snapshot["pending_questions"].([]map[string]any)
	if len(questions) != 1 || questions[0]["id"] != "widget_color" {
		t.Fatalf("snapshot pending_questions = %#v, want the restored question", snapshot["pending_questions"])
	}

	// A fresh SSE subscriber's FIRST frame carries that same projection.
	session := NewSession("session_20260101_000000_resume01", bus)
	session.QueenDir = root
	session.Chat = runtime
	manager := NewSessionManager()
	manager.AddSession(session)

	app := NewApp(AppDeps{
		Manager:      manager,
		LiveSessions: manager.ListSessions,
		Home:         hivehome.Home{Root: root},
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/sessions/session_20260101_000000_resume01/events")
	if err != nil {
		t.Fatalf("GET events stream: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)

	name, data := readEventFrame(t, reader)
	if name != "" {
		t.Fatalf("first frame carries event name %q; upstream sends data-only frames", name)
	}
	frame := map[string]any{}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		t.Fatalf("decode first frame: %v", err)
	}
	if frame["type"] != "session_snapshot" {
		t.Fatalf("first frame type = %#v, want session_snapshot", frame["type"])
	}
	snapshotData, _ := frame["data"].(map[string]any)
	if snapshotData["awaiting_input"] != true || snapshotData["park_reason"] != "ask_user" {
		t.Fatalf("first-frame snapshot data = %#v, want awaiting_input/ask_user", frame["data"])
	}
	if _, ok := snapshotData["pending_questions"].([]any); !ok {
		t.Fatalf("first-frame snapshot pending_questions = %#v, want the restored question array", snapshotData["pending_questions"])
	}
}

// Parity-exempt: the fresh-session path is Go's own default (no cursor yet);
// upstream has no equivalent (its loop only exists after a session starts).
func TestFreshRuntimeStillStartsTurnDoneWithoutEvents(t *testing.T) {
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	runtime := newResumedChatRuntime(t, root, bus, false)
	runtime.parkMu.Lock()
	reason := runtime.parkReason
	runtime.parkMu.Unlock()
	if reason != agentloop.ParkReasonTurnDone {
		t.Fatalf("fresh runtime park reason = %q, want turn_done", reason)
	}
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("fresh runtime published events: %v", recorder.types())
	}
	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["park_reason"] != nil || snapshot["awaiting_input"] != false {
		t.Fatalf("snapshot = %#v, want the empty fresh-bus projection", snapshot)
	}
}

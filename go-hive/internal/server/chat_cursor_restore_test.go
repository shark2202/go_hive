package server

// Cold-resume persistence for the chat runtime's loop-owned state and the
// explicit user-stop flag.
//
// A processes restart must restore the cumulative tool-call count, the
// turn-level doom fingerprint window and user_stopped from the same cursor the
// park state uses, without dropping the cursor fields other writers own
// (next_seq, recent_responses, pending_input, park records). A real client
// input clears user_stopped both in memory and on disk.
//
// Parity: core/tests/test_event_loop_node.py::TestToolCallLifetimeBudget::test_resume_restores_cursor_and_tool_calls_used
// Parity: core/tests/test_cancel_user_stop_flow.py::test_cursor_persists_user_stopped_flag
// Parity: core/tests/test_cancel_user_stop_flow.py::test_user_stop_park_persists_then_restores

import (
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

func readRuntimeCursor(t *testing.T, root string) map[string]any {
	t.Helper()
	store := agentloop.NewFileConversationStore(filepath.Join(root, "conversations"))
	cursor, err := store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor: %v", err)
	}
	if cursor == nil {
		t.Fatal("cursor is nil, want the persisted checkpoint")
	}
	return cursor
}

// TestColdResumeRestoresUserStoppedAndLoopState constructs a runtime over a
// stopped, mid-turn cursor: the stop flag, cumulative tool calls and doom
// fingerprint window must all be live, the unrelated cursor fields preserved,
// and the next real client message must clear the persisted stop.
//
// Parity: core/tests/test_cancel_user_stop_flow.py::test_presence_does_not_resume_user_stopped_agent
// Parity: core/tests/test_event_loop_node.py::TestToolCallLifetimeBudget::test_resume_restores_cursor_and_tool_calls_used
func TestColdResumeRestoresUserStoppedAndLoopState(t *testing.T) {
	root := t.TempDir()
	seedResumedConversation(t, root, map[string]any{
		"next_seq":         2,
		"recent_responses": []any{"earlier reply"},
		"pending_input": map[string]any{
			"reason":              "user_stopped",
			"emit_client_request": true,
		},
		"park_reason":     "user_stopped",
		"parked_at":       "2026-11-25T00:00:00Z",
		"user_stopped":    true,
		"tool_calls_used": float64(4),
		// Upstream's recent_tool_fingerprints shape: turns of
		// [tool_name, canonical_args_json] pairs.
		"recent_tool_fingerprints": []any{
			[]any{[]any{"read_file", `{"path":"a.txt"}`}},
		},
	})

	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	runtime := newResumedChatRuntime(t, root, bus, false)

	if !runtime.reminderSignals().UserStopped {
		t.Fatal("restarted runtime lost the persisted user_stopped flag")
	}
	if got := runtime.loop.ToolCallsUsed(); got != 4 {
		t.Fatalf("restored tool calls = %d, want 4", got)
	}
	if got := runtime.loop.DoomWindow.Depth(); got != 1 {
		t.Fatalf("restored doom window depth = %d, want 1", got)
	}

	cursor := readRuntimeCursor(t, root)
	if got, ok := cursor["next_seq"].(float64); !ok || int(got) != 2 {
		t.Fatalf("next_seq = %#v, want 2 (preserved)", cursor["next_seq"])
	}
	responses, _ := cursor["recent_responses"].([]any)
	if len(responses) != 1 || responses[0] != "earlier reply" {
		t.Fatalf("recent_responses = %#v, want the preserved window", cursor["recent_responses"])
	}
	pending, _ := cursor[parkPendingInputKey].(map[string]any)
	if pending == nil || pending["reason"] != "user_stopped" {
		t.Fatalf("pending_input = %#v, want the preserved user_stopped park", cursor[parkPendingInputKey])
	}
	if cursor[parkReasonKey] != "user_stopped" {
		t.Fatalf("park_reason = %#v, want user_stopped", cursor[parkReasonKey])
	}
	if _, present := cursor[parkParkedAtKey]; !present {
		t.Fatal("parked_at key was dropped")
	}
	if got := intOrFloat(cursor["tool_calls_used"]); got != 4 {
		t.Fatalf("persisted tool_calls_used = %d, want 4", got)
	}
	if turns, _ := cursor["recent_tool_fingerprints"].([]any); len(turns) != 1 {
		t.Fatalf("recent_tool_fingerprints = %#v, want one preserved turn", cursor["recent_tool_fingerprints"])
	}

	// A real client message is the next real input: it lifts the stop in
	// memory and on disk, then runs a normal turn.
	if err := runtime.Deliver("resume the work"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	if runtime.reminderSignals().UserStopped {
		t.Fatal("user-stop flag survived the next client input")
	}

	cursor = readRuntimeCursor(t, root)
	if stopped, ok := cursor[userStoppedCursorKey].(bool); !ok || stopped {
		t.Fatalf("persisted user_stopped = %#v, want false after client input", cursor[userStoppedCursorKey])
	}
	if got := intOrFloat(cursor["tool_calls_used"]); got != 4 {
		t.Fatalf("tool_calls_used after a text-only turn = %d, want 4", got)
	}
	if got, ok := cursor["next_seq"].(float64); !ok || int(got) <= 2 {
		t.Fatalf("next_seq after the turn = %#v, want it advanced past 2", cursor["next_seq"])
	}
	// The resumed turn was text-only, so the preserved fingerprint window was
	// cleared and the cleared state persisted (upstream's else branch).
	if turns, _ := cursor["recent_tool_fingerprints"].([]any); len(turns) != 0 {
		t.Fatalf("recent_tool_fingerprints after text-only turn = %#v, want empty", cursor["recent_tool_fingerprints"])
	}
}

// TestColdResumePreservesAskUserParkAndCursorFields proves the loop-state
// restore never rewrites the ask_user wait or the unrelated cursor keys.
//
// Parity: core/tests/test_event_loop_node.py::TestCrashRecovery::test_restore_reblocks_pending_user_input_instead_of_continuing
func TestColdResumePreservesAskUserParkAndCursorFields(t *testing.T) {
	root := t.TempDir()
	seedResumedConversation(t, root, map[string]any{
		"next_seq":         2,
		"recent_responses": []any{"earlier reply"},
		"pending_input": map[string]any{
			"reason":              "ask_user",
			"emit_client_request": true,
			"questions": []any{
				map[string]any{"id": "widget_color", "prompt": "What color should the widget be?"},
			},
		},
		"park_reason":     "ask_user",
		"parked_at":       "2026-11-25T00:00:00Z",
		"tool_calls_used": float64(3),
		"recent_tool_fingerprints": []any{
			[]any{[]any{"read_file", `{"path":"a.txt"}`}},
			[]any{[]any{"read_file", `{"path":"a.txt"}`}},
		},
	})

	bus := eventbus.NewEventBus()
	runtime := newResumedChatRuntime(t, root, bus, false)
	if !runtime.hasPendingQuestions() {
		t.Fatal("restarted runtime lost the persisted ask_user question")
	}
	if got := runtime.loop.ToolCallsUsed(); got != 3 {
		t.Fatalf("restored tool calls = %d, want 3", got)
	}
	if got := runtime.loop.DoomWindow.Depth(); got != 2 {
		t.Fatalf("restored doom window depth = %d, want 2", got)
	}

	cursor := readRuntimeCursor(t, root)
	if got, ok := cursor["next_seq"].(float64); !ok || int(got) != 2 {
		t.Fatalf("next_seq = %#v, want 2 (preserved)", cursor["next_seq"])
	}
	responses, _ := cursor["recent_responses"].([]any)
	if len(responses) != 1 || responses[0] != "earlier reply" {
		t.Fatalf("recent_responses = %#v, want the preserved window", cursor["recent_responses"])
	}
	pending, _ := cursor[parkPendingInputKey].(map[string]any)
	if pending == nil || pending["reason"] != "ask_user" {
		t.Fatalf("pending_input = %#v, want the preserved ask_user park", cursor[parkPendingInputKey])
	}
	questions, _ := pending["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("pending_input questions = %#v, want one preserved question", pending["questions"])
	}
	if cursor[parkReasonKey] != "ask_user" {
		t.Fatalf("park_reason = %#v, want ask_user", cursor[parkReasonKey])
	}
	if got := intOrFloat(cursor["tool_calls_used"]); got != 3 {
		t.Fatalf("persisted tool_calls_used = %d, want 3", got)
	}
	if turns, _ := cursor["recent_tool_fingerprints"].([]any); len(turns) != 2 {
		t.Fatalf("recent_tool_fingerprints = %#v, want two preserved turns", cursor["recent_tool_fingerprints"])
	}
}

// TestUserStoppedPersistsAcrossRuntimeRestart takes the real Stop entry point
// (MarkUserStopped, as the cancel-queen route calls it) and proves the flag and
// the loop checkpoint survive a full runtime teardown and reconstruction.
//
// Parity: core/tests/test_cancel_user_stop_flow.py::test_cursor_persists_user_stopped_flag
// Parity: core/tests/test_cancel_user_stop_flow.py::test_user_stop_park_persists_then_restores
func TestUserStoppedPersistsAcrossRuntimeRestart(t *testing.T) {
	root := t.TempDir()
	sessionID := "session_20260101_000000_stopped1"
	bus1 := eventbus.NewEventBus()
	first, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{},
		Root:      root,
		Bus:       bus1,
		SessionID: sessionID,
		QueenName: "queen_growth",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}

	first.MarkUserStopped()
	if !first.reminderSignals().UserStopped {
		t.Fatal("MarkUserStopped did not surface on the idle signal")
	}
	cursor := readRuntimeCursor(t, root)
	if stopped, ok := cursor[userStoppedCursorKey].(bool); !ok || !stopped {
		t.Fatalf("persisted user_stopped = %#v, want true", cursor[userStoppedCursorKey])
	}
	if _, present := cursor["tool_calls_used"]; !present {
		t.Fatal("the pre-stop checkpoint did not persist tool_calls_used")
	}
	if _, present := cursor["recent_tool_fingerprints"]; !present {
		t.Fatal("the pre-stop checkpoint did not persist recent_tool_fingerprints")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reconstruct over the same directory: the idle-nudge gate must still be
	// closed before the user says anything.
	bus2 := eventbus.NewEventBus()
	recorder := recordChatEvents(bus2)
	second := newResumedChatRuntime(t, root, bus2, false)
	if !second.reminderSignals().UserStopped {
		t.Fatal("restarted runtime lost the persisted user_stopped flag")
	}
	if err := second.Deliver("start again"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	if second.reminderSignals().UserStopped {
		t.Fatal("user-stop flag survived the next client input after restart")
	}
	cursor = readRuntimeCursor(t, root)
	if stopped, ok := cursor[userStoppedCursorKey].(bool); !ok || stopped {
		t.Fatalf("persisted user_stopped after restart input = %#v, want false", cursor[userStoppedCursorKey])
	}
}

// intOrFloat normalises the numeric shapes a cursor reader may see.
func intOrFloat(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return -1
	}
}

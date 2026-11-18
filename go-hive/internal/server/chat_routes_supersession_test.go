package server

// Parity: framework/server/tests/test_session_supersession.py::test_chat_rejects_superseded_session
// Parity: framework/server/tests/test_session_supersession.py::test_chat_clears_fork_kickoff_pending

import (
	"net/http"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

// Parity: framework/server/tests/test_session_supersession.py::test_chat_rejects_superseded_session
func TestChatRejectsSupersededSession(t *testing.T) {
	manager := NewSessionManager()
	server := newChatTestServer(t, manager, nil)

	session := NewSession("session_20260101_000000_super001", eventbus.NewEventBus())
	session.SupersededBy = "session_20260101_000000_super002"
	// The superseded guard runs before the fork-kickoff re-arm, so a retired
	// session must keep its flag: upstream returns from the 409 branch first.
	session.ForkKickoffPending = true
	manager.AddSession(session)

	status, body := postChat(t, server.URL, session.ID, map[string]any{"message": "are you still there?"})
	if status != http.StatusConflict {
		t.Fatalf("status = %d body = %#v, want 409", status, body)
	}
	if body["error"] != "session_locked" || body["reason"] != "superseded" {
		t.Fatalf("body = %#v, want session_locked/superseded", body)
	}
	if body["superseded_by"] != session.SupersededBy {
		t.Fatalf("superseded_by = %#v, want %q", body["superseded_by"], session.SupersededBy)
	}
	if message, ok := body["message"].(string); !ok || message == "" {
		t.Fatalf("body = %#v, want a human-readable message", body)
	}
	if !session.ForkKickoffPending {
		t.Fatal("a superseded session must not clear fork_kickoff_pending")
	}
}

// Parity: framework/server/tests/test_session_supersession.py::test_chat_clears_fork_kickoff_pending
func TestChatClearsForkKickoffPending(t *testing.T) {
	manager := NewSessionManager()
	server := newChatTestServer(t, manager, nil)
	runtime, _ := newTestChatRuntime(t, t.TempDir())

	session := NewSession("session_20260101_000000_kick0001", eventbus.NewEventBus())
	session.Chat = runtime
	session.ForkKickoffPending = true
	manager.AddSession(session)

	status, body := postChat(t, server.URL, session.ID, map[string]any{"message": "now do something else"})
	if status != http.StatusOK || body["delivered"] != true {
		t.Fatalf("status = %d body = %#v, want delivered", status, body)
	}
	if session.ForkKickoffPending {
		t.Fatal("fork_kickoff_pending should be cleared by the first genuine user message")
	}

	// Upstream clears the flag before reading the body, so a request that is
	// rejected later still re-arms the session (its guard order puts the clear
	// ahead of the "message is required" check).
	empty := NewSession("session_20260101_000000_kick0002", eventbus.NewEventBus())
	empty.Chat = runtime
	empty.ForkKickoffPending = true
	manager.AddSession(empty)

	status, _ = postChat(t, server.URL, empty.ID, map[string]any{"message": "   "})
	if status != http.StatusBadRequest {
		t.Fatalf("empty message status = %d, want 400", status)
	}
	if empty.ForkKickoffPending {
		t.Fatal("fork_kickoff_pending should clear even when the message is rejected")
	}
}

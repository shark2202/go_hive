package server

// A queen's name/title/core traits are baked into the *static* portion of her
// system prompt. Upstream reads the identity from the phase state on every
// turn (`get_static_prompt`), so a profile save reaches the very next model
// request even when it lands mid-turn. This port bakes the prompt into the
// conversation, where the turn goroutine is the only writer: the route helper
// therefore refuses to write while a turn runs. Without a deferred rebase that
// edit is lost until the session restarts — the blocker this test pins.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
// Parity: framework/server/routes_queens.py::_refresh_live_queen_identity
// Parity: framework/server/queen_orchestrator.py::materialize_queen_identity

import (
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// waitForChatTurn waits for the runtime to finish the turn that issued the
// nth model request. turnActive is cleared by the turn goroutine last, so it
// is the completion signal (the park state alone reports the initial
// turn_done before the first turn even starts).
func waitForChatTurn(t *testing.T, runtime *ChatRuntime, completer *queenChatLLM, wantRequests int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(completer.requestSnapshot()) >= wantRequests && !runtime.turnActive.Load() {
			time.Sleep(5 * time.Millisecond)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("chat turn %d did not finish", wantRequests)
}

// Parity-exempt: next-turn rebase is the Go port's countermeasure to a baked system prompt; upstream re-reads get_static_prompt per turn and has no pytest for a mid-turn profile edit.
func TestMidTurnQueenIdentityEditRebasesAtNextTurn(t *testing.T) {
	root := t.TempDir()
	record := NewSession("session_20260101_000000_ident001", eventbus.NewEventBus())
	record.QueenName = "queen_growth"
	record.QueenDir = root
	phase := AttachSessionPhaseState(record, nil, "")
	if phase == nil {
		t.Fatal("session has no phase state")
	}
	// The profile the session was created with (materialize_queen_identity).
	phase.SetQueenIdentity("queen_growth", map[string]any{
		"name":        "Alexandra",
		"title":       "Head of Technology",
		"core_traits": "measured",
	})
	llmCompleter := &queenChatLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       llmCompleter,
		Root:      root,
		Bus:       record.EventBus,
		SessionID: record.ID,
		Session:   record,
		QueenName: "queen_growth",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	// First turn: the phase state's identity reaches the model request.
	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatTurn(t, runtime, llmCompleter, 1)
	if got := runtime.conv.SystemPrompt(); !strings.Contains(got, "Alexandra") {
		t.Fatalf("system prompt lacks the session identity: %s", got)
	}

	// A save landing while a turn runs cannot write the conversation — the
	// turn goroutine is its only writer — so the route helper declines.
	runtime.turnActive.Store(true)
	phase.SetQueenIdentity("queen_growth", map[string]any{
		"name":        "Beatrix",
		"title":       "Head of Research",
		"core_traits": "curious",
	})
	runtime.RefreshQueenIdentity(map[string]any{
		"name":        "Beatrix",
		"title":       "Head of Research",
		"core_traits": "curious",
	})
	if got := runtime.conv.SystemPrompt(); strings.Contains(got, "Beatrix") {
		t.Fatalf("mid-turn identity refresh wrote the conversation: %s", got)
	}
	runtime.turnActive.Store(false)

	// The next turn boundary rebases the baked prompt from the phase state, so
	// the edit is not dropped.
	if err := runtime.Deliver("continue"); err != nil {
		t.Fatalf("Deliver (after edit): %v", err)
	}
	waitForChatTurn(t, runtime, llmCompleter, 2)
	prompt := runtime.conv.SystemPrompt()
	if !strings.Contains(prompt, "Beatrix") || !strings.Contains(prompt, "Head of Research") {
		t.Fatalf("system prompt did not rebase onto the mid-turn edit: %s", prompt)
	}
	if strings.Contains(prompt, "Alexandra") {
		t.Fatalf("system prompt still carries the superseded identity: %s", prompt)
	}
	// The model saw the same text the conversation now bakes.
	requests := llmCompleter.requestSnapshot()
	if len(requests) == 0 {
		t.Fatal("no model request recorded")
	}
	if last := requests[len(requests)-1]; !strings.Contains(last.System, "Beatrix") {
		t.Fatalf("last model request did not carry the rebased identity: %s", last.System)
	}
}

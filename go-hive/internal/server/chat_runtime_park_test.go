package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func waitForParkReason(t *testing.T, root, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pending := readParkPendingInput(t, root); pending != nil && pending["reason"] == want {
			return pending
		}
		time.Sleep(10 * time.Millisecond)
	}
	pending := readParkPendingInput(t, root)
	t.Fatalf("pending_input reason = %#v, want %q", pending, want)
	return nil
}

func readParkPendingInput(t *testing.T, root string) map[string]any {
	t.Helper()
	store := agentloop.NewFileConversationStore(filepath.Join(root, "conversations"))
	cursor, err := store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor: %v", err)
	}
	if cursor == nil {
		return nil
	}
	pending, _ := cursor[parkPendingInputKey].(map[string]any)
	return pending
}

// Parity: core/tests/test_event_loop_node.py::test_answering_restored_question_clears_pending_input_on_disk
func TestChatRuntimeParkSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	model := &queenChatLLM{responses: []llm.Completion{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "ask-1",
				Name:      "ask_user",
				Arguments: `{"questions":[{"id":"next","prompt":"Which next?","options":["Ship it","Wait"]}]}`,
			}},
		},
		{Content: "Over to you."},
	}}
	bus1 := eventbus.NewEventBus()
	first, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      root,
		Bus:       bus1,
		SessionID: "session_20260101_000000_queen001",
		QueenName: "queen_technology",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	recorder1 := recordChatEvents(bus1)
	if err := first.Deliver("start the work"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder1, eventbus.EventExecutionCompleted, 1)

	pending := readParkPendingInput(t, root)
	if pending == nil {
		t.Fatal("park was not written to the conversation cursor")
	}
	if pending["reason"] != "ask_user" {
		t.Fatalf("pending_input reason = %#v, want ask_user", pending["reason"])
	}
	questions, _ := pending["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("pending_input questions = %#v, want one question", pending["questions"])
	}
	question, _ := questions[0].(map[string]any)
	if question["id"] != "next" || question["prompt"] != "Which next?" {
		t.Fatalf("persisted question = %#v", question)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restart against the same session directory: the question must come back
	// as the runtime's wait instead of being forgotten.
	bus2 := eventbus.NewEventBus()
	second, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{responses: []llm.Completion{{Content: "Answer received."}}},
		Root:      root,
		Bus:       bus2,
		SessionID: "session_20260101_000000_queen001",
		QueenName: "queen_technology",
	})
	if err != nil {
		t.Fatalf("restart NewChatRuntime: %v", err)
	}
	defer second.Close()
	if !second.hasPendingQuestions() {
		t.Fatal("restarted runtime lost the persisted ask_user question")
	}
	second.parkMu.Lock()
	restoredReason := second.parkReason
	second.parkMu.Unlock()
	if restoredReason != agentloop.ParkReasonAskUser {
		t.Fatalf("restored park reason = %q, want ask_user", restoredReason)
	}

	// The answer clears the wait on disk immediately, so a later restart does
	// not re-pop the already-answered question.
	recorder2 := recordChatEvents(bus2)
	if err := second.Deliver("Ship it"); err != nil {
		t.Fatalf("Deliver answer: %v", err)
	}
	waitForChatEvents(t, recorder2, eventbus.EventExecutionCompleted, 1)
	if second.hasPendingQuestions() {
		t.Fatal("pending ask_user question survived the answer")
	}
	pending = waitForParkReason(t, root, "turn_done")
	if pending["questions"] != nil {
		t.Fatalf("pending_input questions after answer = %#v, want nil", pending["questions"])
	}

	if err := second.Close(); err != nil {
		t.Fatalf("Close second runtime: %v", err)
	}
	thirdBus := eventbus.NewEventBus()
	third, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{},
		Root:      root,
		Bus:       thirdBus,
		SessionID: "session_20260101_000000_queen001",
		QueenName: "queen_technology",
	})
	if err != nil {
		t.Fatalf("third NewChatRuntime: %v", err)
	}
	defer third.Close()
	if third.hasPendingQuestions() {
		t.Fatal("answered question was resurrected by a later restart")
	}
}

// Parity-exempt: turn_done is the ordinary auto-park; upstream covers it
// indirectly through the cursor write in the loop, not a dedicated test.
func TestChatRuntimePersistsTurnDonePark(t *testing.T) {
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{responses: []llm.Completion{{Content: "done"}}},
		Root:      root,
		Bus:       bus,
		SessionID: "session_20260101_000000_queen002",
		QueenName: "default",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	recorder := recordChatEvents(bus)
	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pending := waitForParkReason(t, root, "turn_done")
	if pending["questions"] != nil {
		t.Fatalf("pending_input questions = %#v, want nil", pending["questions"])
	}
}

package server

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/server/routes_sessions.py::handle_session_chat
//
// The overseer and the chat session are two conversations over one client
// surface. These tests pin the shared half: an overseer ask_user becomes the
// session's pending_input (the record the chat front end already reads), the
// question reaches the client on the queen stream through the session bridge,
// and a client answer delivered through POST /api/sessions/{id}/chat resolves
// the park for both conversations.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// sawUserText reports whether the overseer's conversation was ever handed a
// user message containing text.
func (f *queenChatLLM) sawUserText(text string) bool {
	for _, request := range f.requestSnapshot() {
		for _, message := range request.Messages {
			if message.Role == "user" && strings.Contains(message.Content, text) {
				return true
			}
		}
	}
	return false
}

func startAskUserColony(t *testing.T, provider llm.Completer) *colony.ColonyRuntime {
	t.Helper()
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         provider,
		EventBus:    eventbus.NewEventBus(),
		StreamID:    "colony_overseer_park",
		StoragePath: t.TempDir(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

func askUserOverseerReplies() []llm.Completion {
	return []llm.Completion{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "ask-1",
				Name:      "ask_user",
				Arguments: `{"questions":[{"id":"next","prompt":"Which next?","options":["Ship it","Wait"]}]}`,
			}},
		},
		{Content: "Over to you."},
		{Content: "Answer received."},
	}
}

// cursorParkedQuestions decodes the questions from the cursor's pending_input
// record (the store round-trips them through JSON).
func cursorParkedQuestions(t *testing.T, cursor map[string]any) []map[string]any {
	t.Helper()
	pending, ok := cursor["pending_input"].(map[string]any)
	if !ok {
		t.Fatalf("cursor pending_input = %#v, want a parked record", cursor["pending_input"])
	}
	raw, _ := pending["questions"].([]any)
	questions := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if question, ok := entry.(map[string]any); ok {
			questions = append(questions, question)
		}
	}
	return questions
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/server/routes_sessions.py::handle_session_chat
func TestOverseerAskUserParksSessionAndAnswerRelays(t *testing.T) {
	root := t.TempDir()
	sessionBus := eventbus.NewEventBus()
	overseerLLM := &queenChatLLM{responses: askUserOverseerReplies()}
	colonyRuntime := startAskUserColony(t, overseerLLM)

	chatLLM := &fakeChatLLM{}
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       chatLLM,
		Bus:       sessionBus,
		Root:      root,
		SessionID: "s-overseer-park",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	var (
		mu        sync.Mutex
		requested []*eventbus.AgentEvent
	)
	subscription := sessionBus.Subscribe([]eventbus.EventType{eventbus.EventClientInputRequested}, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		mu.Lock()
		requested = append(requested, event)
		mu.Unlock()
	})
	t.Cleanup(func() { sessionBus.Unsubscribe(subscription) })

	if _, err := colonyRuntime.StartOverseer(context.Background(), relayQueenSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}

	// The overseer's question reaches the client as the queen stream, mirrored
	// from the colony bus with relayed_from=overseer.
	if !waitForServerCondition(3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requested) > 0
	}) {
		t.Fatal("the overseer's ask_user never reached the session bus")
	}
	mu.Lock()
	event := requested[0]
	mu.Unlock()
	if event.StreamID != chatStreamID {
		t.Fatalf("client_input_requested stream = %q, want %q", event.StreamID, chatStreamID)
	}
	if event.Data["relayed_from"] != overseerStreamID {
		t.Fatalf("client_input_requested relayed_from = %#v, want overseer", event.Data["relayed_from"])
	}
	if event.Data["park_reason"] != string(agentloop.ParkReasonAskUser) {
		t.Fatalf("client_input_requested park_reason = %#v, want ask_user", event.Data["park_reason"])
	}

	// Both halves park on the same record: the chat runtime's in-memory
	// question and the session cursor's pending_input. Wait for the persisted
	// half so the assertions below cannot race the bridge's cursor write.
	if !waitForServerCondition(3*time.Second, func() bool {
		cursor, err := chatRuntime.store.ReadCursor()
		if err != nil || cursor == nil {
			return false
		}
		pending, ok := cursor[parkPendingInputKey].(map[string]any)
		return ok && pending["reason"] == string(agentloop.ParkReasonAskUser)
	}) {
		t.Fatal("the overseer's question never reached the session cursor")
	}
	if !chatRuntime.hasPendingQuestions() {
		t.Fatal("the chat runtime did not record the overseer's question")
	}
	if reason := chatRuntime.pendingParkReason(); reason != agentloop.ParkReasonAskUser {
		t.Fatalf("chat runtime park reason = %q, want ask_user", reason)
	}
	cursor, err := chatRuntime.store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor: %v", err)
	}
	pending, _ := cursor[parkPendingInputKey].(map[string]any)
	if pending["reason"] != string(agentloop.ParkReasonAskUser) {
		t.Fatalf("cursor pending_input = %#v, want reason ask_user", cursor[parkPendingInputKey])
	}
	questions := cursorParkedQuestions(t, cursor)
	if len(questions) != 1 || questions[0]["prompt"] != "Which next?" {
		t.Fatalf("cursor pending questions = %#v, want the overseer's question", questions)
	}

	// A fresh subscriber (dashboard reload) reads the park off the session
	// snapshot rather than replaying events: the mirrored turn boundary must
	// make it awaiting_input with the same question, not a stale "executing".
	if !waitForServerCondition(3*time.Second, func() bool {
		snapshot := eventbus.ComputeSessionSnapshot(sessionBus)
		return snapshot["awaiting_input"] == true && snapshot["park_reason"] == string(agentloop.ParkReasonAskUser)
	}) {
		snapshot := eventbus.ComputeSessionSnapshot(sessionBus)
		t.Fatalf("session snapshot = %#v, want awaiting_input/ask_user", snapshot)
	}
	snapshot := eventbus.ComputeSessionSnapshot(sessionBus)
	pendingQuestions, ok := snapshot["pending_questions"].([]map[string]any)
	if !ok || len(pendingQuestions) != 1 || pendingQuestions[0]["id"] != "next" {
		t.Fatalf("snapshot pending_questions = %#v, want the overseer's question", snapshot["pending_questions"])
	}

	// Answering through the HTTP chat route delivers the answer to the
	// overseer and the chat queen runs no turn of its own.
	manager := NewSessionManager()
	session := NewSession("s-overseer-park", sessionBus)
	session.Chat = chatRuntime
	session.Colony = colonyHostStub{runtime: colonyRuntime}
	manager.AddSession(session)
	server := newChatTestServer(t, manager, nil)

	status, body := postChat(t, server.URL, session.ID, map[string]any{"message": "Ship it"})
	if status != http.StatusOK {
		t.Fatalf("POST /chat status = %d (%#v), want 200", status, body)
	}
	if !waitForServerCondition(3*time.Second, func() bool { return overseerLLM.sawUserText("Ship it") }) {
		t.Fatal("the answer never reached the overseer's conversation")
	}
	if count := chatLLM.requestCount(); count != 0 {
		t.Fatalf("chat queen ran %d turn(s) for the relayed answer, want 0", count)
	}

	// The answer resolves both halves: no pending question, and the cursor's
	// pending_input is rewritten as turn_done (the answer, not a new wait).
	// The rewrite happens after the relay, so wait for the persisted value
	// instead of reading the cursor immediately after the overseer saw the
	// text. The wait is generous: this whole path is asynchronous, and a
	// fully loaded CI machine can take seconds to schedule the writer.
	if chatRuntime.hasPendingQuestions() {
		t.Fatal("the chat runtime kept the answered question")
	}
	if !waitForServerCondition(10*time.Second, func() bool {
		cursor, err := chatRuntime.store.ReadCursor()
		if err != nil || cursor == nil {
			return false
		}
		pending, _ := cursor[parkPendingInputKey].(map[string]any)
		return pending["reason"] == string(agentloop.ParkReasonTurnDone)
	}) {
		cursor, _ := chatRuntime.store.ReadCursor()
		t.Fatalf("cursor pending_input after answer = %#v, want reason turn_done (within 3s)", cursor[parkPendingInputKey])
	}
	cursor, err = chatRuntime.store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor after answer: %v", err)
	}
	pending, _ = cursor[parkPendingInputKey].(map[string]any)
	if pending["reason"] != string(agentloop.ParkReasonTurnDone) {
		t.Fatalf("cursor pending_input after answer = %#v, want reason turn_done", cursor[parkPendingInputKey])
	}
	if questions := cursorParkedQuestions(t, cursor); len(questions) != 0 {
		t.Fatalf("cursor pending questions after answer = %#v, want cleared", questions)
	}
	if parked := colonyRuntime.OverseerPendingQuestions(); len(parked) != 0 {
		t.Fatalf("overseer pending questions = %#v, want cleared by the answer", parked)
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
// (a restarted session re-enters the same wait instead of forgetting it)
func TestOverseerAskUserParkSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	overseerLLM := &queenChatLLM{responses: askUserOverseerReplies()}
	colonyRuntime := startAskUserColony(t, overseerLLM)

	first, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       eventbus.NewEventBus(),
		Root:      root,
		SessionID: "s-overseer-restart",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	first.BindColony(colonyHostStub{runtime: colonyRuntime})
	if _, err := colonyRuntime.StartOverseer(context.Background(), relayQueenSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	// Wait for the durable half, not only the in-memory one: the bridge records
	// the question before it persists the cursor, so hasPendingQuestions can be
	// true a moment before the restart would find anything on disk.
	if !waitForServerCondition(3*time.Second, func() bool {
		cursor, err := first.store.ReadCursor()
		if err != nil || cursor == nil {
			return false
		}
		pending, ok := cursor[parkPendingInputKey].(map[string]any)
		return ok && pending["reason"] == string(agentloop.ParkReasonAskUser)
	}) {
		t.Fatal("the overseer's question never reached the session cursor")
	}
	if !first.hasPendingQuestions() {
		t.Fatal("the chat runtime did not record the overseer's question")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A fresh runtime over the same root restores the overseer's park from the
	// session cursor: the front end still shows the question, so the restarted
	// pair agrees on the wait.
	answerModel := &fakeChatLLM{}
	restarted, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       answerModel,
		Root:      root,
		SessionID: "s-overseer-restart",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime (restart): %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if !restarted.hasPendingQuestions() {
		t.Fatal("a restarted runtime forgot the overseer's pending question")
	}
	if reason := restarted.pendingParkReason(); reason != agentloop.ParkReasonAskUser {
		t.Fatalf("restarted park reason = %q, want ask_user", reason)
	}
	cursor, err := restarted.store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor: %v", err)
	}
	questions := cursorParkedQuestions(t, cursor)
	if len(questions) != 1 || questions[0]["id"] != "next" {
		t.Fatalf("restored pending questions = %#v, want the overseer's question", questions)
	}

	// The user's next message is the answer: it clears the restored park
	// instead of leaving a second copy of the question open.
	if err := restarted.Deliver("Ship it"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !waitForServerCondition(3*time.Second, func() bool { return answerModel.requestCount() >= 1 }) {
		t.Fatal("the answer turn never reached the chat model")
	}
	if restarted.hasPendingQuestions() {
		t.Fatal("the restored question survived the answer")
	}
}

// Parity: framework/server/queen_orchestrator.py::_on_worker_report
// (the overseer's own report is front-end bookkeeping, not a second turn)
func TestOverseerTerminalReportRunsNoChatTurn(t *testing.T) {
	overseerLLM := &queenChatLLM{responses: []llm.Completion{{Content: "idle"}}}
	colonyRuntime := startAskUserColony(t, overseerLLM)

	chatLLM := &fakeChatLLM{}
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       chatLLM,
		Root:      t.TempDir(),
		SessionID: "s-overseer-terminal",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	if _, err := colonyRuntime.StartOverseer(context.Background(), relayQueenSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForServerCondition(2*time.Second, func() bool { return overseerLLM.requestSnapshot() != nil }) {
		t.Fatal("overseer never ran its first turn")
	}
	overseerID := colonyRuntime.OverseerID()
	if overseerID == "" {
		t.Fatal("colony has no overseer id")
	}

	report := eventbus.NewAgentEvent(eventbus.EventSubagentReport, overseerStreamID)
	report.Data = map[string]any{
		"worker_id": overseerID,
		"status":    "stopped",
		"summary":   "Overseer stopped with the colony.",
	}
	colonyRuntime.EventBus().Publish(report)

	// The report is archived as an external turn, never answered by a chat
	// model turn from the second conversation.
	if !waitForServerCondition(3*time.Second, func() bool {
		parts, err := chatRuntime.Transcript()
		if err != nil {
			return false
		}
		for _, part := range parts {
			if content, _ := part["content"].(string); strings.Contains(content, "Overseer stopped with the colony.") {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the overseer's terminal report was not archived in the session transcript")
	}
	if count := chatLLM.requestCount(); count != 0 {
		t.Fatalf("chat queen ran %d turn(s) for the overseer's own report, want 0", count)
	}
}

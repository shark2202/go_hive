package server

// Parity-exempt (Go-only opt-in): every test in this file manually starts the
// dormant live overseer; Hive 1.0.15 production never attaches one. The
// "Parity:" markers below cite the upstream behavior each test mirrors, not a
// production Go caller.
//
// Upstream reference: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (the overseer receives the user's chat: session.colony_runtime.overseer.inject)

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

type relayedDelta struct {
	stream       string
	relayedFrom  string
	snapshot     string
	eventType    eventbus.EventType
	executionSet bool
}

func startRelayColony(t *testing.T, provider *overseerRecordLLM) *colony.ColonyRuntime {
	t.Helper()
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         provider,
		EventBus:    eventbus.NewEventBus(),
		StreamID:    "colony_relay",
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

func relayQueenSpec() agentloop.AgentSpec {
	prompt := "You are the queen of this test colony."
	return agentloop.AgentSpec{ID: "queen", Name: "Queen", AgentType: "event_loop", SystemPrompt: &prompt}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// Parity: framework/server/routes_sessions.py::handle_session_chat (chat delivery)
func TestChatRuntimeRelaysClientMessageToOverseer(t *testing.T) {
	overseerProvider := &overseerRecordLLM{replies: []string{"idle", "the north field is clear"}}
	colonyRuntime := startRelayColony(t, overseerProvider)
	if _, err := colonyRuntime.StartOverseer(context.Background(), relayQueenSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForServerCondition(2*time.Second, func() bool { return overseerProvider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}

	sessionBus := eventbus.NewEventBus()
	chatProvider := &fakeChatLLM{}
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       chatProvider,
		Bus:       sessionBus,
		Root:      t.TempDir(),
		SessionID: "s-relay",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	var (
		mu             sync.Mutex
		deltas         []relayedDelta
		answerDeltaSeq int
		answerTurnDone bool
	)
	subscription := sessionBus.Subscribe([]eventbus.EventType{eventbus.EventClientOutputDelta}, func(event *eventbus.AgentEvent) {
		if event == nil || event.Data == nil {
			return
		}
		snapshot, _ := event.Data["snapshot"].(string)
		relayed, _ := event.Data["relayed_from"].(string)
		mu.Lock()
		deltas = append(deltas, relayedDelta{stream: event.StreamID, relayedFrom: relayed, snapshot: snapshot, eventType: event.Type})
		if event.StreamID == chatStreamID && relayed == overseerStreamID && strings.Contains(snapshot, "north field is clear") {
			answerDeltaSeq = event.Seq
		}
		mu.Unlock()
	})
	t.Cleanup(func() { sessionBus.Unsubscribe(subscription) })
	// The bridge enqueues the record-only assistant turn for the overseer's
	// reply before it publishes the mirrored turn completion, so a completion
	// on the queen stream after the answer delta proves the reply is on the
	// runtime's queue.
	completed := sessionBus.Subscribe([]eventbus.EventType{eventbus.EventExecutionCompleted}, func(event *eventbus.AgentEvent) {
		if event == nil || event.Data == nil {
			return
		}
		if relayed, _ := event.Data["relayed_from"].(string); relayed != overseerStreamID {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if answerDeltaSeq != 0 && event.Seq > answerDeltaSeq {
			answerTurnDone = true
		}
	})
	t.Cleanup(func() { sessionBus.Unsubscribe(completed) })

	question := "what is the state of the north field?"
	if err := chatRuntime.Deliver(question); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if !waitForServerCondition(3*time.Second, func() bool { return overseerProvider.sawUserText(question) }) {
		t.Fatal("the client message never reached the colony overseer")
	}
	// One loop acts on the message: the chat queen must not run a duplicate turn.
	if count := chatProvider.requestCount(); count != 0 {
		t.Fatalf("chat queen ran %d turn(s) for a relayed message, want 0", count)
	}
	// The overseer's reply is mirrored onto the session bus as the queen's
	// stream, which is the only stream the session pane renders.
	if !waitForServerCondition(3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, delta := range deltas {
			if delta.stream == chatStreamID && delta.relayedFrom == overseerStreamID && strings.Contains(delta.snapshot, "north field is clear") {
				return true
			}
		}
		return false
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("session-bus deltas = %#v, want the overseer reply on the queen stream", deltas)
	}

	// The reply mirror rides the runtime's queue: wait (bounded) for the
	// overseer turn to complete after its answer delta so Close() flushes the
	// queued assistant record instead of racing its enqueue.
	if !waitForServerCondition(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return answerTurnDone
	}) {
		t.Fatal("the overseer turn never completed on the session bus")
	}

	// The user turn is recorded in the session transcript, client-input tagged,
	// so a reload still shows what was asked. Close first: the transcript has a
	// single writer (the runtime's queue goroutine).
	if err := chatRuntime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	messages := chatRuntime.conv.Messages()
	// The ask and the mirrored reply are two independent transcript records
	// with no fixed queue order (the relay reaches the overseer before the ask
	// is queued, so the reply can even be recorded first), so check both by
	// content instead of position.
	var recordedAsk, recordedReply bool
	for _, message := range messages {
		if message.Role == agentloop.RoleUser && message.IsClientInput && strings.Contains(message.Content, question) {
			recordedAsk = true
		}
		if message.Role == agentloop.RoleAssistant && strings.Contains(message.Content, "[overseer] the north field is clear") {
			recordedReply = true
		}
	}
	if !recordedAsk {
		t.Fatalf("session transcript = %#v, want the client user turn containing the question", messages)
	}
	if !recordedReply {
		t.Fatalf("session transcript = %#v, want the overseer reply recorded as an assistant turn", messages)
	}
}

// Parity: framework/server/queen_orchestrator.py::_on_worker_report
// Parity: framework/agent_loop/agent_loop.py::inject_event
func TestChatRuntimeRelayOwnsReportsAndTriggers(t *testing.T) {
	overseerProvider := &overseerRecordLLM{replies: []string{"idle", "report noted", "tick handled"}}
	colonyRuntime := startRelayColony(t, overseerProvider)
	if _, err := colonyRuntime.StartOverseer(context.Background(), relayQueenSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForServerCondition(2*time.Second, func() bool { return overseerProvider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}

	chatProvider := &fakeChatLLM{}
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       chatProvider,
		Root:      t.TempDir(),
		SessionID: "s-relay-owns",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	report := eventbus.NewAgentEvent(eventbus.EventSubagentReport, "worker:1")
	report.Data = map[string]any{
		"worker_id":  "worker:1",
		"batch_id":   "batch_1",
		"batch_size": 1,
		"status":     "success",
		"summary":    "wrote two rows",
		"task":       "fill rows",
	}
	colonyRuntime.EventBus().Publish(report)
	if !waitForServerCondition(2*time.Second, func() bool { return overseerProvider.sawUserText("[WORKER_REPORT]") }) {
		t.Fatal("the overseer never received the worker report")
	}
	trigger := agentloop.NewTriggerEvent("timer", "daily")
	if err := chatRuntime.InjectTrigger(trigger); err != nil {
		t.Fatalf("InjectTrigger: %v", err)
	}
	if !waitForServerCondition(2*time.Second, func() bool { return overseerProvider.sawUserText("[TRIGGER: timer/daily]") }) {
		t.Fatal("the overseer never received the framework trigger")
	}
	// The chat queen owns no execution while the overseer is live: no second
	// turn answers the same report/trigger.
	if count := chatProvider.requestCount(); count != 0 {
		t.Fatalf("chat queen ran %d turn(s) for relayed execution inputs, want 0", count)
	}
}

// Parity: framework/server/routes_sessions.py::handle_session_chat (fallback)
// Reference production ownership (Hive 1.0.15): a ChatRuntime bound to a
// colony never has a live overseer on the normal path, so Deliver runs the
// chat runtime's own queen loop. The live-overseer relay is a dormant Go-only
// extension exercised by the opt-in relay tests.
func TestChatRuntimeWithoutOverseerKeepsChatQueen(t *testing.T) {
	overseerProvider := &overseerRecordLLM{replies: []string{"idle"}}
	colonyRuntime := startRelayColony(t, overseerProvider)
	if overseerID := colonyRuntime.OverseerID(); overseerID != "" {
		t.Fatalf("colony has a live overseer id %q; the reference production path has none", overseerID)
	}

	chatProvider := &fakeChatLLM{}
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       chatProvider,
		Root:      t.TempDir(),
		SessionID: "s-no-overseer",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})

	question := "summarise the tracker"
	if err := chatRuntime.Deliver(question); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !waitForServerCondition(3*time.Second, func() bool { return chatProvider.requestCount() >= 1 }) {
		t.Fatal("a colony without an overseer dropped the client message")
	}
	// The chat queen's own loop ran the turn: a model request carries the
	// client message. Nothing was relayed into the dormant overseer slot.
	chatProvider.mu.Lock()
	sawQuestion := false
	for _, request := range chatProvider.requests {
		for _, message := range request.Messages {
			if message.Role == "user" && strings.Contains(message.Content, question) {
				sawQuestion = true
			}
		}
	}
	chatProvider.mu.Unlock()
	if !sawQuestion {
		t.Fatal("the chat queen's model never received the client message")
	}
	if overseerProvider.sawUserText(question) {
		t.Fatal("a message was injected into a colony with no overseer")
	}
	if overseerProvider.callCount() != 0 {
		t.Fatalf("dormant overseer ran %d turn(s), want 0", overseerProvider.callCount())
	}
}

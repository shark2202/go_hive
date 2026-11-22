package colony

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// askUserResponse scripts one ask_user tool call, the shape a queen or the
// persistent overseer emits when it needs a decision from the user.
func askUserResponse(prompt string, options ...string) scriptedResponse {
	question := map[string]any{"id": "next", "prompt": prompt}
	if len(options) > 0 {
		question["options"] = options
	}
	arguments, _ := json.Marshal(map[string]any{"questions": []any{question}})
	return scriptedResponse{
		ToolCalls: []llm.ToolCall{{
			ID:        "ask-1",
			Name:      "ask_user",
			Arguments: string(arguments),
		}},
		StopReason: "tool_calls",
	}
}

func (m *scriptedLLM) sawUserText(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.calls {
		for _, message := range req.Messages {
			if message.Role == "user" && strings.Contains(message.Content, text) {
				return true
			}
		}
	}
	return false
}

func newOverseerParkRuntime(t *testing.T, model llm.Completer, bus *eventbus.EventBus) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         model,
		EventBus:    bus,
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

func overseerParkSpec() agentloop.AgentSpec {
	prompt := "You are the queen of this test colony."
	return agentloop.AgentSpec{ID: "queen", Name: "Queen", AgentType: "event_loop", SystemPrompt: &prompt}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func TestOverseerAskUserParksAndPublishesQuestion(t *testing.T) {
	bus := eventbus.NewEventBus()
	model := newScriptedLLM(
		askUserResponse("Which next?", "Ship it", "Wait"),
		textResponse("Over to you."),
		textResponse("Answer received."),
	)
	runtime := newOverseerParkRuntime(t, model, bus)

	var (
		mu        sync.Mutex
		requested []*eventbus.AgentEvent
		states    []*eventbus.AgentEvent
	)
	subscription := bus.Subscribe([]eventbus.EventType{eventbus.EventClientInputRequested, eventbus.EventLoopStateChanged}, func(event *eventbus.AgentEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch event.Type {
		case eventbus.EventClientInputRequested:
			requested = append(requested, event)
		case eventbus.EventLoopStateChanged:
			states = append(states, event)
		}
	})
	t.Cleanup(func() { bus.Unsubscribe(subscription) })

	if _, err := runtime.StartOverseer(context.Background(), overseerParkSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}

	if !waitForColony(t, 2*time.Second, func() bool { return len(runtime.OverseerPendingQuestions()) > 0 }) {
		t.Fatal("the overseer's ask_user never parked the colony")
	}
	parked := runtime.OverseerPendingQuestions()
	if len(parked) != 1 || parked[0]["id"] != "next" || parked[0]["prompt"] != "Which next?" {
		t.Fatalf("overseer pending questions = %#v, want the normalised question", parked)
	}

	snapshotRequested := func() []*eventbus.AgentEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]*eventbus.AgentEvent(nil), requested...)
	}
	// Handlers run on their own goroutines (EventBus.executeHandlers), so the
	// park can be visible before the subscriber observes the event under load.
	if !waitForColony(t, 2*time.Second, func() bool { return len(snapshotRequested()) > 0 }) {
		t.Fatal("client_input_requested event never reached subscribers")
	}
	events := snapshotRequested()
	if len(events) != 1 {
		t.Fatalf("client_input_requested count = %d, want 1", len(events))
	}
	event := events[0]
	if event.StreamID != overseerStream || event.NodeID == nil || *event.NodeID != overseerStream {
		t.Fatalf("client_input_requested stream/node = %q/%v, want overseer/overseer", event.StreamID, event.NodeID)
	}
	if event.Data["park_reason"] != string(agentloop.ParkReasonAskUser) {
		t.Fatalf("client_input_requested park_reason = %#v, want ask_user", event.Data["park_reason"])
	}
	questions, ok := event.Data["questions"].([]map[string]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("client_input_requested questions = %#v, want one question", event.Data["questions"])
	}
	options, _ := questions[0]["options"].([]string)
	if strings.Join(options, "|") != "Ship it|Wait" {
		t.Fatalf("question options = %#v, want the plain string choices", questions[0]["options"])
	}

	// The user's answer is a client input, not a report/trigger: it releases the
	// park and reaches the overseer as its next turn.
	if err := runtime.AnswerOverseer("Ship it"); err != nil {
		t.Fatalf("AnswerOverseer: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool { return model.sawUserText("Ship it") }) {
		t.Fatal("the answer never reached the overseer's conversation")
	}
	if parked := runtime.OverseerPendingQuestions(); len(parked) != 0 {
		t.Fatalf("overseer pending questions = %#v, want cleared by the answer", parked)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_await_user_input
// (a turn that asked the user parks on ask_user, not turn_done, so the session
// snapshot treats it as an external wait)
func TestOverseerTurnPublishesAskUserParkState(t *testing.T) {
	bus := eventbus.NewEventBus()
	model := newScriptedLLM(
		askUserResponse("Which next?", "Ship it", "Wait"),
		textResponse("Over to you."),
	)
	runtime := newOverseerParkRuntime(t, model, bus)

	var (
		mu     sync.Mutex
		states []*eventbus.AgentEvent
	)
	subscription := bus.Subscribe([]eventbus.EventType{eventbus.EventLoopStateChanged}, func(event *eventbus.AgentEvent) {
		mu.Lock()
		states = append(states, event)
		mu.Unlock()
	})
	t.Cleanup(func() { bus.Unsubscribe(subscription) })

	if _, err := runtime.StartOverseer(context.Background(), overseerParkSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, state := range states {
			if state.Data["activity"] == "awaiting_user" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the overseer turn never published an awaiting_user boundary")
	}
	mu.Lock()
	defer mu.Unlock()
	final := states[len(states)-1].Data
	if final["park_reason"] != string(agentloop.ParkReasonAskUser) {
		t.Fatalf("final loop state = %#v, want park_reason ask_user", final)
	}
	if final["activity"] != "awaiting_user" {
		t.Fatalf("final loop state = %#v, want awaiting_user", final)
	}
}

// A worker report or trigger is new input, not an answer: it must not clear the
// question the user still owes the overseer.
//
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (only a user
// message ends the wait)
func TestInjectOverseerReportKeepsOpenQuestion(t *testing.T) {
	bus := eventbus.NewEventBus()
	model := newScriptedLLM(
		askUserResponse("Which next?", "Ship it", "Wait"),
		textResponse("Over to you."),
		textResponse("Report noted."),
	)
	runtime := newOverseerParkRuntime(t, model, bus)
	if _, err := runtime.StartOverseer(context.Background(), overseerParkSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool { return len(runtime.OverseerPendingQuestions()) > 0 }) {
		t.Fatal("the overseer's ask_user never parked the colony")
	}

	if err := runtime.InjectOverseer("[WORKER_REPORT]\n<status>success</status>"); err != nil {
		t.Fatalf("InjectOverseer: %v", err)
	}
	if !waitForColony(t, 2*time.Second, func() bool { return model.sawUserText("[WORKER_REPORT]") }) {
		t.Fatal("the report never reached the overseer")
	}
	if parked := runtime.OverseerPendingQuestions(); len(parked) == 0 {
		t.Fatal("a worker report cleared the still-open question")
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (the overseer gets the queen's tool catalogue, including ask_user)
func TestOverseerToolSurfaceAdvertisesAskUser(t *testing.T) {
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		StoragePath: t.TempDir(),
		StreamID:    "colony_overseer_tools",
	})
	names := map[string]bool{}
	for _, def := range runtime.overseerToolDefs() {
		names[def.Name] = true
	}
	if !names["ask_user"] {
		t.Fatalf("overseer tool surface = %v, want ask_user advertised", names)
	}
}

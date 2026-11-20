package colony

// Worker escalation channel, colony half: the worker tool surface advertises
// escalate, the event carries the colony id and a request_id, and injected
// guidance resumes the parked worker until it reports.
//
// Parity: core/tests/test_event_loop_node.py::test_escalate_available_for_worker_stream

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

func escalateResponse(reason, contextText string) scriptedResponse {
	arguments, _ := json.Marshal(map[string]any{"reason": reason, "context": contextText})
	return scriptedResponse{
		ToolCalls: []llm.ToolCall{{
			ID:        "escalate_1",
			Name:      "escalate",
			Arguments: string(arguments),
		}},
		StopReason: "tool_calls",
	}
}

// Parity: core/tests/test_event_loop_node.py::test_escalate_available_for_worker_stream
func TestWorkerToolsAdvertiseEscalate(t *testing.T) {
	provider := newScriptedLLM(textResponse("done"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "investigate"})
	names := toolNames(runtime.workerTools(worker))
	if !hasTool(names, "escalate") {
		t.Fatalf("worker tools = %v, want escalate", names)
	}

	overseer := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "overseer:test_colony", Task: ""})
	overseer.Persistent = true
	if hasTool(toolNames(runtime.workerTools(overseer)), "escalate") {
		t.Fatal("the persistent overseer must not be offered escalate")
	}
}

// Parity-exempt: upstream drives the emit through the AgentLoop's event bus; this
// asserts the port's colony half directly (colony stamp + request_id shape).
func TestWorkerEscalationEventCarriesColonyAndRequestID(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, newScriptedLLM(textResponse("done")), bus)

	received := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventEscalationRequested}, func(event *eventbus.AgentEvent) {
		received <- event
	})

	requestID, err := runtime.requestWorkerEscalation(agentEscalationRequest())
	if err != nil {
		t.Fatalf("requestWorkerEscalation: %v", err)
	}
	if requestID == "" {
		t.Fatal("requestWorkerEscalation returned no request_id")
	}

	select {
	case event := <-received:
		if event.StreamID != "worker:w1" {
			t.Fatalf("stream_id = %q, want worker:w1", event.StreamID)
		}
		if event.ColonyID == nil || *event.ColonyID != "test_colony" {
			t.Fatalf("colony_id = %v, want test_colony (colony-scoped subscription depends on it)", event.ColonyID)
		}
		if event.Data["request_id"] != requestID {
			t.Fatalf("event request_id = %v, want %q", event.Data["request_id"], requestID)
		}
		if event.Data["reason"] != "Credential wall" || event.Data["context"] != "HTTP 401" {
			t.Fatalf("event payload = %#v, want the tool's reason/context", event.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ESCALATION_REQUESTED event on the colony bus")
	}
}

// Parity-exempt: end-to-end worker half of the escalation channel (escalate ->
// park -> inject_input -> report). Upstream's equivalent path is covered by the
// session handoff tests plus the event-loop escalate assertions, not one test.
func TestWorkerEscalationParksAndResumesWithInjectedGuidance(t *testing.T) {
	provider := newScriptedLLM(
		escalateResponse("Database credentials missing", "DATABASE_URL is unset"),
		reportResponse("success", "Used the staging database and finished.", nil),
	)
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)

	escalations := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventEscalationRequested}, func(event *eventbus.AgentEvent) {
		select {
		case escalations <- event:
		default:
		}
	})

	ids, err := runtime.Spawn(context.Background(), "query the ledger", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}

	var event *eventbus.AgentEvent
	select {
	case event = <-escalations:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never escalated")
	}

	// The escalation is addressable while the worker is parked: the worker is
	// still active, and the model has not been called a second time.
	if !worker.IsActive() {
		t.Fatal("worker stopped instead of parking on the escalation")
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("worker model calls while parked = %d, want 1", got)
	}
	first, ok := provider.firstCall()
	if !ok || !hasTool(toolNames(first.Tools), "escalate") {
		t.Fatalf("worker's tool surface = %v, want escalate", toolNames(first.Tools))
	}

	requestID, _ := event.Data["request_id"].(string)
	if requestID == "" {
		t.Fatal("escalation event carries no request_id")
	}
	if err := runtime.InjectInput(worker.ID, "[QUEEN_REPLY] request_id="+requestID+"\nUse the staging database."); err != nil {
		t.Fatalf("InjectInput: %v", err)
	}

	if !eventually(t, 5*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatal("worker stayed parked after the queen's reply")
	}
	report := worker.ExplicitReport()
	if report == nil || report["status"] != "success" {
		t.Fatalf("worker report = %#v, want the resumed success report", report)
	}
	if got := provider.callCount(); got != 2 {
		t.Fatalf("worker model calls = %d, want 2 (resume after guidance)", got)
	}
	second := workerCallAt(t, provider, 1)
	if !messagesContain(second.Messages, "[QUEEN_REPLY] request_id="+requestID) {
		t.Fatalf("queen reply missing from the resumed worker call: %#v", second.Messages)
	}
}

func workerCallAt(t *testing.T, provider *scriptedLLM, index int) llm.CompletionRequest {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if index >= len(provider.calls) {
		t.Fatalf("provider has %d calls, want at least %d", len(provider.calls), index+1)
	}
	return provider.calls[index]
}

func agentEscalationRequest() agentloop.EscalationRequest {
	return agentloop.EscalationRequest{
		StreamID:    "worker:w1",
		NodeID:      "worker:w1",
		ExecutionID: "exec_w1",
		Reason:      "Credential wall",
		Context:     "HTTP 401",
	}
}

func messagesContain(messages []llm.ChatMessage, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

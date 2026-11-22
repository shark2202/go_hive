package server

import (
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

type fakeQueen struct {
	mu       sync.Mutex
	calls    []string
	clientIn []bool
	err      error
}

func (q *fakeQueen) InjectEvent(text string, isClientInput bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls = append(q.calls, text)
	q.clientIn = append(q.clientIn, isClientInput)
	return q.err
}

func (q *fakeQueen) callCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.calls)
}

func (q *fakeQueen) lastCall(t *testing.T) (string, bool) {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.calls) == 0 {
		t.Fatal("queen inject_event was never called")
	}
	return q.calls[len(q.calls)-1], q.clientIn[len(q.clientIn)-1]
}

func handoffSession(bus *eventbus.EventBus, id string) *Session {
	return NewSession(id, bus)
}

func attachQueen(session *Session) *fakeQueen {
	queen := &fakeQueen{}
	session.QueenExecutor = &QueenExecutor{
		NodeRegistry: map[string]QueenNode{"queen": queen},
	}
	return queen
}

func strPtr(s string) *string { return &s }

// Parity: core/tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen
func TestWorkerHandoffInjectsAddressedRequestIntoQueen(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := handoffSession(bus, "session_handoff")
	queen := attachQueen(session)

	subID := InstallWorkerEscalationRouting(session)
	if subID == "" {
		t.Fatal("InstallWorkerEscalationRouting returned no subscription id")
	}
	session.WorkerHandoffSub = subID

	bus.EmitEscalationRequested(
		"worker:abc123",
		"research_node",
		"Credential wall",
		"HTTP 401 while calling external API",
		strPtr("exec_123"),
		strPtr("req-xyz"),
	)

	if got := queen.callCount(); got != 1 {
		t.Fatalf("queen inject_event calls = %d, want 1", got)
	}
	injected, isClientInput := queen.lastCall(t)

	for _, want := range []string{
		"[WORKER_ESCALATION]",
		"request_id: req-xyz",
		"worker_id: abc123",
		"node_id: research_node",
		"reason: Credential wall",
		"HTTP 401 while calling external API",
	} {
		if !strings.Contains(injected, want) {
			t.Errorf("injected handoff missing %q:\n%s", want, injected)
		}
	}
	if isClientInput {
		t.Errorf("isClientInput = true, want false")
	}

	entry, ok := session.PendingEscalations["req-xyz"]
	if !ok {
		t.Fatalf("pending_escalations missing req-xyz: %#v", session.PendingEscalations)
	}
	if entry["worker_id"] != "abc123" {
		t.Errorf("entry worker_id = %v, want abc123", entry["worker_id"])
	}
	if entry["reason"] != "Credential wall" {
		t.Errorf("entry reason = %v, want Credential wall", entry["reason"])
	}
}

// Parity: core/tests/test_session_manager_worker_handoff.py::test_worker_handoff_ignores_queen_stream
func TestWorkerHandoffIgnoresQueenStream(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := handoffSession(bus, "session_handoff")
	queen := attachQueen(session)

	InstallWorkerEscalationRouting(session)

	bus.EmitEscalationRequested(
		"queen",
		"queen",
		"should be ignored",
		"",
		nil,
		strPtr("req-ignored"),
	)

	if got := queen.callCount(); got != 0 {
		t.Fatalf("queen inject_event calls = %d, want 0", got)
	}
	if _, ok := session.PendingEscalations["req-ignored"]; ok {
		t.Fatalf("req-ignored must not be recorded")
	}
}

// Parity: core/tests/test_session_manager_worker_handoff.py::test_worker_handoff_queen_dead_falls_back_to_client_input
func TestWorkerHandoffQueenDeadFallsBackToClientInput(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := handoffSession(bus, "session_handoff")

	var capturedOutput []*eventbus.AgentEvent
	var capturedInputReq []*eventbus.AgentEvent
	bus.Subscribe([]eventbus.EventType{eventbus.EventClientOutputDelta}, func(event *eventbus.AgentEvent) {
		capturedOutput = append(capturedOutput, event)
	})
	bus.Subscribe([]eventbus.EventType{eventbus.EventClientInputRequested}, func(event *eventbus.AgentEvent) {
		capturedInputReq = append(capturedInputReq, event)
	})

	InstallWorkerEscalationRouting(session)

	bus.EmitEscalationRequested(
		"worker:w1",
		"node_1",
		"stuck",
		"",
		nil,
		strPtr("req-dead"),
	)

	found := false
	for _, event := range capturedOutput {
		if content, _ := event.Data["content"].(string); strings.Contains(content, "[WORKER_ESCALATION]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no CLIENT_OUTPUT_DELTA carried the handoff: %#v", capturedOutput)
	}
	if len(capturedInputReq) != 1 {
		t.Fatalf("CLIENT_INPUT_REQUESTED events = %d, want 1", len(capturedInputReq))
	}
	if _, ok := session.PendingEscalations["req-dead"]; !ok {
		t.Fatalf("pending_escalations missing req-dead: %#v", session.PendingEscalations)
	}
}

// Parity: core/tests/test_session_manager_worker_handoff.py::test_stop_session_unsubscribes_worker_handoff
func TestStopSessionUnsubscribesWorkerHandoff(t *testing.T) {
	bus := eventbus.NewEventBus()
	manager := NewSessionManager()
	session := handoffSession(bus, "session_stop")
	queen := attachQueen(session)

	session.WorkerHandoffSub = InstallWorkerEscalationRouting(session)
	manager.AddSession(session)

	bus.EmitEscalationRequested("worker:main", "node_1", "before stop", "", nil, strPtr("req-before"))
	if got := queen.callCount(); got != 1 {
		t.Fatalf("before stop: queen inject_event calls = %d, want 1", got)
	}

	stopped := manager.StopSession(session.ID)
	if !stopped {
		t.Fatal("StopSession returned false, want true")
	}
	if session.WorkerHandoffSub != "" {
		t.Fatalf("WorkerHandoffSub = %q, want empty after stop", session.WorkerHandoffSub)
	}

	bus.EmitEscalationRequested("worker:main", "node_1", "after stop", "", nil, strPtr("req-after"))
	if got := queen.callCount(); got != 1 {
		t.Fatalf("after stop: queen inject_event calls = %d, want 1", got)
	}
}

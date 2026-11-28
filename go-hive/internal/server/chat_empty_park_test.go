package server

// A turn that produced nothing at all is a ghost stream, not a finished turn:
// the loop nudges the model and, at the stall-detection threshold, parks the
// session on EMPTY_RESPONSES. Upstream announces that park the way it
// announces every wait — client_input_requested carrying the park reason,
// with the loop state following the reason's activity — and publishes no
// execution_failed, because nothing failed. The chat runtime must not turn
// the designed park into a broken turn.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e''' empty
// response guard -> _await_user_input(reason=ParkReason.EMPTY_RESPONSES))
// Parity: framework/agent_loop/reminders.py::ParkReason.activity

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity-exempt: Go chat-runtime wiring for the EMPTY_RESPONSES park; upstream's _await_user_input/park-activity behavior has no pytest for this event sequence.
func TestEmptyResponseParkAnnouncesClientInputRequest(t *testing.T) {
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	// StallDetectionThreshold defaults to 3: three empty completions park.
	responses := []llm.Completion{{}, {}, {}, {}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{responses: responses},
		Root:      root,
		Bus:       bus,
		SessionID: "session_20260101_000000_empty001",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.startLoopGuards()
	t.Cleanup(runtime.stopLoopGuards)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonEmptyResponses); err != nil {
		t.Fatalf("%v", err)
	}

	// The park boundary announces the wait exactly once, naming the reason.
	requests := recorder.ofType(eventbus.EventClientInputRequested)
	if len(requests) != 1 {
		t.Fatalf("client_input_requested count = %d, want 1: %#v", len(requests), recorder.types())
	}
	if got := requests[0].Data["park_reason"]; got != string(agentloop.ParkReasonEmptyResponses) {
		t.Fatalf("client_input_requested park_reason = %#v", got)
	}
	// Nothing failed: a designed park publishes no execution_failed frame, so
	// the dashboard renders a parked session rather than an error bubble.
	if failed := recorder.ofType(eventbus.EventExecutionFailed); len(failed) != 0 {
		t.Fatalf("empty-response park published execution_failed: %#v", failed[0].Data)
	}
	// The loop state names the same park and its activity (interrupted for a
	// broken park), and it lands after the client request.
	events := recorder.snapshot()
	requestIndex := -1
	stateIndex := -1
	for index, event := range events {
		switch event.Type {
		case eventbus.EventClientInputRequested:
			if requestIndex < 0 {
				requestIndex = index
			}
		case eventbus.EventLoopStateChanged:
			if event.Data["park_reason"] == string(agentloop.ParkReasonEmptyResponses) {
				stateIndex = index
			}
		}
	}
	if stateIndex < 0 {
		t.Fatalf("no loop_state_changed for empty_responses: %#v", recorder.types())
	}
	state := events[stateIndex].Data
	if state["activity"] != "interrupted" {
		t.Fatalf("park activity = %#v, want interrupted", state["activity"])
	}
	if requestIndex > stateIndex {
		t.Fatalf("client_input_requested landed after loop_state_changed: %d > %d", requestIndex, stateIndex)
	}
	// The durable park agrees with the event, and the session carries no
	// error: the next user message answers the park, it does not recover a
	// failed turn.
	pending := waitForParkReason(t, root, string(agentloop.ParkReasonEmptyResponses))
	if pending["reason"] != string(agentloop.ParkReasonEmptyResponses) {
		t.Fatalf("pending_input reason = %#v", pending["reason"])
	}
	if err := runtime.LastError(); err != nil {
		t.Fatalf("LastError = %v, want nil for a designed park", err)
	}
}

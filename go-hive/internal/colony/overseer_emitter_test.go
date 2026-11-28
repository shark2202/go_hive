package colony

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type overseerEvent struct {
	eventType eventbus.EventType
	streamID  string
	snapshot  string
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestStartOverseer::test_start_overseer_creates_persistent_worker
// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
//
// The persistent overseer is the colony's client-facing queen. Its turn must be
// observable on the colony bus under stream "overseer", so a session front end
// can present the reply.
func TestOverseerTurnPublishesClientOutput(t *testing.T) {
	bus := eventbus.NewEventBus()
	provider := newScriptedLLM(textResponse("idle"), textResponse("the north field is clear"))
	runtime := makeTestColony(t, provider, bus)

	var (
		mu     sync.Mutex
		events []overseerEvent
	)
	subscription := bus.Subscribe([]eventbus.EventType{
		eventbus.EventClientOutputDelta,
		eventbus.EventExecutionStarted,
		eventbus.EventExecutionCompleted,
	}, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		snapshot, _ := event.Data["snapshot"].(string)
		mu.Lock()
		events = append(events, overseerEvent{eventType: event.Type, streamID: event.StreamID, snapshot: snapshot})
		mu.Unlock()
	})
	t.Cleanup(func() { bus.Unsubscribe(subscription) })

	worker, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !eventually(t, time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}
	if !worker.Inject("check the north field") {
		t.Fatal("Inject returned false on a live overseer")
	}
	if !eventually(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, event := range events {
			if event.eventType == eventbus.EventClientOutputDelta && strings.Contains(event.snapshot, "north field is clear") {
				return true
			}
		}
		return false
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("colony bus events = %#v, want the overseer reply as client output", events)
	}

	mu.Lock()
	defer mu.Unlock()
	sawStarted, sawCompleted := false, false
	for _, event := range events {
		if event.streamID != "overseer" {
			t.Fatalf("overseer event stream = %q, want overseer", event.streamID)
		}
		switch event.eventType {
		case eventbus.EventExecutionStarted:
			sawStarted = true
		case eventbus.EventExecutionCompleted:
			sawCompleted = true
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("turn boundaries missing: started=%v completed=%v", sawStarted, sawCompleted)
	}
}

// emptyOverseerLLM always answers with a completion that carries no text and
// no tool calls, so the loop's empty-response guard is the only thing that can
// end the turn.
type emptyOverseerLLM struct{}

func (e *emptyOverseerLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, nil
}

// A turn that produced nothing at all is a ghost stream, not a finished turn:
// the loop nudges the model and, at the stall-detection threshold, parks the
// overseer on EMPTY_RESPONSES with a typed error. Upstream announces that park
// the way it announces every wait — client_input_requested carrying the park
// reason, with the loop state following the reason's activity — and publishes
// no execution_failed, because nothing failed. The overseer must not turn the
// designed park into a broken turn.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' empty
// response guard -> _await_user_input(reason=ParkReason.EMPTY_RESPONSES))
// Parity: framework/agent_loop/reminders.py::ParkReason.activity
func TestOverseerEmptyResponseParkAnnouncesWaitWithoutFailure(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := newOverseerParkRuntime(t, &emptyOverseerLLM{}, bus)
	requests := collectEvents(bus, eventbus.EventClientInputRequested)
	states := collectEvents(bus, eventbus.EventLoopStateChanged)
	failures := collectEvents(bus, eventbus.EventExecutionFailed)

	if _, err := runtime.StartOverseer(context.Background(), overseerParkSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForColony(t, 5*time.Second, func() bool { return requests.count() > 0 }) {
		t.Fatalf("overseer never announced its empty-response park: states=%d", states.count())
	}
	// EventBus handlers run on their own goroutines: give a failure frame the
	// same chance to arrive before asserting the park published none.
	time.Sleep(50 * time.Millisecond)
	if failed := failures.snapshot(); len(failed) != 0 {
		t.Fatalf("empty-response park published execution_failed: %#v", failed[0].Data)
	}

	events := requests.snapshot()
	if len(events) != 1 {
		t.Fatalf("client_input_requested count = %d, want 1", len(events))
	}
	event := events[0]
	if event.StreamID != overseerStream || event.NodeID == nil || *event.NodeID != overseerStream {
		t.Fatalf("client_input_requested stream/node = %q/%v, want overseer/overseer", event.StreamID, event.NodeID)
	}
	if got := event.Data["park_reason"]; got != string(agentloop.ParkReasonEmptyResponses) {
		t.Fatalf("client_input_requested park_reason = %#v, want empty_responses", got)
	}

	// The loop state names the same park and its activity: EMPTY_RESPONSES is
	// a broken park, so the snapshot reports interrupted rather than a false
	// "executing" stall.
	var parkState *eventbus.AgentEvent
	for _, state := range states.snapshot() {
		if state.Data["park_reason"] == string(agentloop.ParkReasonEmptyResponses) {
			parkState = state
		}
	}
	if parkState == nil {
		t.Fatalf("no loop_state_changed for empty_responses: %#v", states.snapshot())
	}
	if got := parkState.Data["activity"]; got != string(agentloop.LoopActivityInterrupted) {
		t.Fatalf("park activity = %#v, want interrupted", got)
	}
}

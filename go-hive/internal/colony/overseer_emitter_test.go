package colony

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
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

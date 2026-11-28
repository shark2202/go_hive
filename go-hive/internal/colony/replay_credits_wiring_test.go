package colony

// P0-14 / A12 live-host wiring for colony loops: a worker's replay tier and
// its turn emitter must publish on the colony bus, and the overseer loop must
// carry the replay writer too. The worker check runs a real spawn so the
// wiring is exercised through newWorkerAgentLoop, not a bare struct literal.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// P0-14: a worker repeating a completed call publishes
// tool_call_replay_detected on the colony bus under its own stream, and A12:
// the same turn's llm_turn_complete carries Completion.Credits.
//
// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected
// Parity: framework/agent_loop/internals/event_publishing.py::publish_llm_turn_complete
func TestWorkerLoopPublishesReplayDetectedAndTurnCredits(t *testing.T) {
	credits := 0.5
	provider := newScriptedLLM(
		scriptedResponse{
			ToolCalls:  []llm.ToolCall{{ID: "call-1", Name: "some_other_tool", Arguments: `{"x":1}`}},
			StopReason: "tool_calls",
			Credits:    &credits,
		},
		scriptedResponse{
			ToolCalls:  []llm.ToolCall{{ID: "call-2", Name: "some_other_tool", Arguments: `{"x":1}`}},
			StopReason: "tool_calls",
			Credits:    &credits,
		},
		scriptedResponse{Content: "worker finished", StopReason: "stop", Credits: &credits},
	)
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)
	replays := collectEvents(bus, eventbus.EventToolCallReplayDetected)
	usage := collectEvents(bus, eventbus.EventLLMTurnComplete)

	ids, err := runtime.Spawn(context.Background(), "say hi", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("Spawn returned %d ids, want 1", len(ids))
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}
	if !eventually(t, 5*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatal("worker did not finish within timeout")
	}
	if !eventually(t, 2*time.Second, func() bool { return replays.count() >= 1 }) {
		t.Fatalf("worker published no tool_call_replay_detected event")
	}

	replay := replays.snapshot()[0]
	if replay.StreamID != worker.StreamID {
		t.Fatalf("replay event stream = %q, want %q", replay.StreamID, worker.StreamID)
	}
	if name, _ := replay.Data["tool_name"].(string); name != "some_other_tool" {
		t.Fatalf("replay event tool_name = %#v", replay.Data["tool_name"])
	}
	if prior, _ := replay.Data["prior_seq"].(int); prior <= 0 {
		t.Fatalf("replay event prior_seq = %#v, want the prior call's seq", replay.Data["prior_seq"])
	}

	if !eventually(t, 2*time.Second, func() bool { return usage.count() >= 3 }) {
		t.Fatalf("llm_turn_complete events = %d, want one per LLM call", usage.count())
	}
	withCredits := 0
	for _, event := range usage.snapshot() {
		if event.StreamID != worker.StreamID {
			t.Fatalf("llm_turn_complete stream = %q, want %q", event.StreamID, worker.StreamID)
		}
		amount, present := event.Data["credits"]
		if !present {
			// The scripted LLM's fallback completion ("Done.") carries no
			// credits; only the three scripted responses do.
			continue
		}
		if value, ok := amount.(json.Number); !ok || value != json.Number("0.5") {
			t.Fatalf("llm_turn_complete credits = %#v, want 0.5", amount)
		}
		withCredits++
	}
	if withCredits < 3 {
		t.Fatalf("llm_turn_complete events carrying credits = %d, want 3", withCredits)
	}
}

// P0-14: runOverseer's loop is handed the colony bus so an overseer's
// cross-turn repeat can publish tool_call_replay_detected too.
//
// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected
func TestOverseerLoopWiresReplayEvents(t *testing.T) {
	runtime := newOverseerLoopProfileRuntime(t)
	loop := waitOverseerLoop(t, runtime)
	if loop.ReplayEvents == nil {
		t.Fatal("overseer loop has no ReplayEvents writer; tool_call_replay_detected cannot publish")
	}
	bus, ok := loop.ReplayEvents.(*eventbus.EventBus)
	if !ok || bus != runtime.bus {
		t.Fatalf("overseer ReplayEvents = %#v, want the colony bus", loop.ReplayEvents)
	}
}

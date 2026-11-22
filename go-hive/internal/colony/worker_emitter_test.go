package colony

// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_started
// Parity: framework/agent_loop/internals/event_publishing.py::publish_llm_turn_complete
//
// A spawned worker's turn must be observable on the colony bus under its own
// stream; the session bridge is what later relays those events to the client.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_completed
func TestWorkerTurnPublishesProgressEvents(t *testing.T) {
	provider := newScriptedLLM(
		scriptedResponse{
			ToolCalls: []llm.ToolCall{{
				ID:        "call-1",
				Name:      "some_other_tool",
				Arguments: `{"x":1}`,
			}},
			StopReason: "tool_calls",
		},
		textResponse("worker finished"),
	)
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)

	output := collectEvents(bus, eventbus.EventClientOutputDelta)
	started := collectEvents(bus, eventbus.EventToolCallStarted)
	completed := collectEvents(bus, eventbus.EventToolCallCompleted)
	tokens := collectEvents(bus, eventbus.EventLLMTurnComplete)

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

	wantStream := worker.StreamID
	if !strings.HasPrefix(wantStream, "worker:") {
		t.Fatalf("worker stream = %q, want worker:<id>", wantStream)
	}

	outputs := output.snapshot()
	if len(outputs) == 0 {
		t.Fatal("worker published no client_output_delta on the colony bus")
	}
	sawText := false
	for _, event := range outputs {
		if event.StreamID != wantStream {
			t.Fatalf("client output stream = %q, want %q", event.StreamID, wantStream)
		}
		if _, relayed := event.Data["relayed_from"]; relayed {
			t.Fatalf("colony-level worker event already carries relayed_from: %#v", event.Data)
		}
		if snapshot, _ := event.Data["snapshot"].(string); strings.Contains(snapshot, "worker finished") {
			sawText = true
		}
	}
	if !sawText {
		t.Fatalf("worker reply not published as client output: %#v", outputs)
	}

	starts := started.snapshot()
	if len(starts) != 1 {
		t.Fatalf("tool_call_started events = %d, want 1", len(starts))
	}
	if starts[0].StreamID != wantStream {
		t.Fatalf("tool_call_started stream = %q, want %q", starts[0].StreamID, wantStream)
	}
	if name, _ := starts[0].Data["tool_name"].(string); name != "some_other_tool" {
		t.Fatalf("tool_call_started tool_name = %q, want some_other_tool", name)
	}

	completions := completed.snapshot()
	if len(completions) != 1 {
		t.Fatalf("tool_call_completed events = %d, want 1", len(completions))
	}
	if completions[0].StreamID != wantStream {
		t.Fatalf("tool_call_completed stream = %q, want %q", completions[0].StreamID, wantStream)
	}
	if result, _ := completions[0].Data["result"].(string); result != "ok" {
		t.Fatalf("tool_call_completed result = %q, want ok", result)
	}

	usage := tokens.snapshot()
	if len(usage) < 2 {
		t.Fatalf("llm_turn_complete events = %d, want one per LLM call", len(usage))
	}
	for _, event := range usage {
		if event.StreamID != wantStream {
			t.Fatalf("llm_turn_complete stream = %q, want %q", event.StreamID, wantStream)
		}
		if _, present := event.Data["input_tokens"]; !present {
			t.Fatalf("llm_turn_complete carries no token counters: %#v", event.Data)
		}
	}
}

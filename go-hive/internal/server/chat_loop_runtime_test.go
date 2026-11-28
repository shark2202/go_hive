package server

// Loop-runtime coverage for the queen chat session: the production loop config
// the constructor installs, the per-turn node_loop_started frame's advertised
// cap, the emitter's no-loop fallback, and a turn that runs past the generic
// 50-iteration bound.
//
// Parity: core/framework/agents/queen/agent.py::queen_loop_config
// Parity: core/framework/agent_loop/internals/event_publishing.py::publish_loop_started

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// forceQueenLoopDefaults pins the queen loop profile for a runtime built by the
// test: an exported HIVE_MAX_ITER in the developer shell would otherwise cap
// MaxIterations and turn the profile assertions into environment noise.
func forceQueenLoopDefaults(t *testing.T) {
	t.Helper()
	t.Setenv("HIVE_MAX_ITER", "")
}

// eventInt reads an integer payload field from a bus event. Events carry Go
// ints in memory; a float64 is accepted too so the helper also works on data
// that round-tripped JSON (the persisted session log).
func eventInt(t *testing.T, data map[string]any, key string) int {
	t.Helper()
	switch value := data[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		t.Fatalf("event data[%q] = %#v, want a number", key, data[key])
		return 0
	}
}

// TestChatRuntimeLoopConfigUsesQueenProfile pins the loop profile the session
// constructor installs: the queen's autonomous 999_999 iteration bound with the
// 30/0/5 tool-call envelope (soft checkpoint at 30, hard stop by the multiple
// rule at 150), not the generic worker profile's 50/30/50/5.
//
// Parity: core/framework/agents/queen/agent.py::queen_loop_config
// Parity: core/tests/test_worker_definition.py::TestDefaultLoopConfig::test_reasonable_defaults
func TestChatRuntimeLoopConfigUsesQueenProfile(t *testing.T) {
	forceQueenLoopDefaults(t)
	runtime, _, _ := newQueenChatRuntime(t, &queenChatLLM{}, "", "queen_technology")

	if runtime.loop == nil {
		t.Fatal("NewChatRuntime built no agent loop")
	}
	config := runtime.loop.Config
	if got, want := config.MaxIterations, 999999; got != want {
		t.Fatalf("loop MaxIterations = %d, want %d (queen autonomous bound)", got, want)
	}
	if config.MaxIterations == agentloop.DefaultLoopConfig().MaxIterations {
		t.Fatalf("loop MaxIterations = %d, want the queen profile, not the generic worker default",
			config.MaxIterations)
	}
	if got, want := config.ToolCallBudget, 30; got != want {
		t.Fatalf("loop ToolCallBudget = %d, want %d", got, want)
	}
	if got, want := config.ToolCallHardLimit, 0; got != want {
		t.Fatalf("loop ToolCallHardLimit = %d, want %d (the multiple rule)", got, want)
	}
	if got, want := config.ToolCallHardMultiple, 5; got != want {
		t.Fatalf("loop ToolCallHardMultiple = %d, want %d (hard stop at 150)", got, want)
	}
}

// TestChatRuntimeNodeLoopStartedCarriesQueenMaxIterations proves the per-turn
// frame reports the bound the loop actually enforces. A non-opening turn's
// node_loop_started must carry 999_999, the runtime's configured cap, so the
// dashboard is not told a queen session stops at the generic 50.
//
// Parity: core/tests/test_event_loop_node.py::test_lifecycle_events_published
// Parity: core/framework/agent_loop/internals/event_publishing.py::publish_loop_started
func TestChatRuntimeNodeLoopStartedCarriesQueenMaxIterations(t *testing.T) {
	forceQueenLoopDefaults(t)
	runtime, recorder, _ := newQueenChatRuntime(t, &queenChatLLM{}, "", "queen_technology")

	if err := runtime.Deliver("hello queen"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	started := waitForChatEvents(t, recorder, eventbus.EventNodeLoopStarted, 1)

	// One per-turn frame: the session-start opening block is written by the
	// session loader, not by a runtime built directly.
	if len(started) != 1 {
		t.Fatalf("node_loop_started count = %d, want 1 (%v)", len(started), recorder.types())
	}
	event := started[0]
	if event.StreamID != chatStreamID || event.NodeID == nil || *event.NodeID != chatNodeID {
		t.Fatalf("node_loop_started stream/node = %q/%v, want queen/queen", event.StreamID, event.NodeID)
	}
	if event.ExecutionID == nil || *event.ExecutionID == "" {
		t.Fatalf("node_loop_started execution_id = %v, want the turn's execution", event.ExecutionID)
	}
	if got, want := eventInt(t, event.Data, "max_iterations"), 999999; got != want {
		t.Fatalf("node_loop_started max_iterations = %d, want %d", got, want)
	}
}

// TestChatTurnEmitterFallsBackToQueenMaxIterations guards the emitter's
// no-loop path: a partially built runtime (no loop config to read) must still
// advertise the queen's autonomous bound. Falling back to the generic
// DefaultLoopConfig would report 50 for a queen session.
//
// Parity-exempt: Go-specific guard. Upstream's publish_loop_started always
// receives a LoopConfig, so there is no upstream fallback to mirror; the Go
// emitter is constructed before (or without) a loop in partial runtime builds.
func TestChatTurnEmitterFallsBackToQueenMaxIterations(t *testing.T) {
	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	emitter := &chatTurnEmitter{
		bus:         bus,
		streamID:    chatStreamID,
		nodeID:      chatNodeID,
		executionID: "exec_fallback",
	}

	emitter.executionStarted("hello")

	started := recorder.ofType(eventbus.EventNodeLoopStarted)
	if len(started) != 1 {
		t.Fatalf("node_loop_started count = %d, want 1 (%v)", len(started), recorder.types())
	}
	if got, want := eventInt(t, started[0].Data, "max_iterations"), 999999; got != want {
		t.Fatalf("fallback max_iterations = %d, want %d (queen profile, not the worker 50)", got, want)
	}
}

// TestChatRuntimeCompletesMoreThanFiftyInnerTurns drives one turn past the
// generic loop's 50-iteration cap: 51 distinct tool calls (unique arguments, so
// the doom-loop fingerprint never repeats) followed by the closing text turn.
// It proves the queen profile's 999_999 bound is live, not just configured:
// the turn completes without the "exceeded N inner turns" error an
// unconfigured loop would raise at 50.
//
// Parity-exempt: upstream has no equivalent 51-iteration runtime test (its
// loop tests drive the config, not a full chat turn); this is a Go runtime
// integration proof of the queen bound.
func TestChatRuntimeCompletesMoreThanFiftyInnerTurns(t *testing.T) {
	forceQueenLoopDefaults(t)
	const toolTurns = 51

	responses := make([]llm.Completion, 0, toolTurns)
	for step := 0; step < toolTurns; step++ {
		responses = append(responses, llm.Completion{
			ToolCalls: []llm.ToolCall{{
				ID:        fmt.Sprintf("call-%02d", step),
				Name:      "scribe_turn",
				Arguments: fmt.Sprintf(`{"step":%d}`, step),
			}},
		})
	}
	model := &queenChatLLM{responses: responses}

	var mu sync.Mutex
	var steps []int
	execute := func(_ context.Context, name string, input map[string]any) (string, bool, error) {
		if name != "scribe_turn" {
			return "", true, fmt.Errorf("unexpected tool %q", name)
		}
		step, _ := input["step"].(float64)
		mu.Lock()
		steps = append(steps, int(step))
		mu.Unlock()
		return fmt.Sprintf("stored step %d", int(step)), false, nil
	}
	tools := []llm.ToolDef{{Name: "scribe_turn", Description: "records one distinct step"}}
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     tools,
		Execute:   execute,
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_20260101_000000_longturn",
		QueenName: "queen_technology",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	recorder := recordChatEvents(bus)
	if runtime.loop == nil {
		t.Fatal("NewChatRuntime built no agent loop")
	}

	if err := runtime.Deliver("run the long mission"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	completed := waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	if failed := recorder.ofType(eventbus.EventExecutionFailed); len(failed) != 0 {
		t.Fatalf("execution_failed events = %d, want none: %#v", len(failed), failed[0].Data)
	}

	mu.Lock()
	executed := append([]int(nil), steps...)
	mu.Unlock()
	if len(executed) != toolTurns {
		t.Fatalf("executed tool calls = %d, want %d", len(executed), toolTurns)
	}
	for step, got := range executed {
		if got != step {
			t.Fatalf("executed steps[%d] = %d, want %d (every call must be distinct)", step, got, step)
		}
	}

	// The closing text-only turn is iteration 52: 51 tool iterations plus the
	// answer. Any "exceeded" stop would have failed before reaching it.
	loopCompleted := recorder.ofType(eventbus.EventNodeLoopCompleted)
	if len(loopCompleted) != 1 {
		t.Fatalf("node_loop_completed count = %d, want 1 (%v)", len(loopCompleted), recorder.types())
	}
	if got, want := eventInt(t, loopCompleted[0].Data, "iterations"), toolTurns+1; got != want {
		t.Fatalf("node_loop_completed iterations = %d, want %d", got, want)
	}
	if got, want := len(model.requestSnapshot()), toolTurns+1; got != want {
		t.Fatalf("model requests = %d, want %d", got, want)
	}
	if got, want := len(recorder.ofType(eventbus.EventLLMTurnComplete)), toolTurns+1; got != want {
		t.Fatalf("llm_turn_complete count = %d, want %d", got, want)
	}
	if got, want := len(recorder.ofType(eventbus.EventToolCallCompleted)), toolTurns; got != want {
		t.Fatalf("tool_call_completed count = %d, want %d", got, want)
	}
	if runtime.LastError() != nil {
		t.Fatalf("runtime LastError = %v, want nil after a completed long turn", runtime.LastError())
	}
	output, _ := completed[0].Data["output"].(map[string]any)
	if output["status"] != "completed" {
		t.Fatalf("execution_completed output = %#v, want status completed", completed[0].Data["output"])
	}
}

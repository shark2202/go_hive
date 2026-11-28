package server

// A5: the turn-level duplicate-fingerprint window, end to end through the
// chat runtime's event bus.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_tool_doom_loop with the window description, then
// await_user_input(DOOM_LOOP) for a stream with direct user I/O)

import (
	"context"
	"fmt"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// failingToolRunner makes every call an error result. The replay breaker
// counts only successful completed calls, so an alternating batch of failing
// calls is exactly the shape the turn-level window owns.
func failingToolRunner(context.Context, string, map[string]any) (string, bool, error) {
	return "same failure", true, nil
}

// TestChatDoomLoopWindowPublishesTheBatchDescription drives the queen loop
// with three identical two-call batches. No single call repeats
// consecutively, so the per-call guard never warns; the replay breaker ignores
// errored results, so the turn-level window is the only detector that trips,
// and its park must publish one node_tool_doom_loop event naming the whole
// batch.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_tool_doom_loop with the window description, then
// await_user_input(DOOM_LOOP) for a stream with direct user I/O)
func TestChatDoomLoopWindowPublishesTheBatchDescription(t *testing.T) {
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	const turns = agentloop.DefaultDoomLoopThreshold
	responses := make([]llm.Completion, 0, turns)
	for turn := 0; turn < turns; turn++ {
		responses = append(responses, llm.Completion{
			ToolCalls: []llm.ToolCall{
				{
					ID:        fmt.Sprintf("a%d", turn),
					Name:      "read_file",
					Arguments: `{"path":"a.txt"}`,
				},
				{
					ID:        fmt.Sprintf("b%d", turn),
					Name:      "read_file",
					Arguments: `{"path":"b.txt"}`,
				},
			},
			StopReason: "tool_calls",
		})
	}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{responses: responses},
		Tools:     []llm.ToolDef{{Name: "read_file", Description: "read a file", Parameters: map[string]any{"type": "object"}}},
		Execute:   failingToolRunner,
		Root:      root,
		Bus:       bus,
		SessionID: "session_20260101_000000_doomwin01",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	// AddSession arms the guards in production; without the park sink the
	// node_tool_doom_loop event has no publisher.
	runtime.startLoopGuards()
	t.Cleanup(runtime.stopLoopGuards)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("read both files again"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonDoomLoop); err != nil {
		t.Fatalf("%v", err)
	}

	doom := recorder.ofType(eventbus.EventNodeToolDoomLoop)
	if len(doom) != 1 {
		t.Fatalf("node_tool_doom_loop events = %d, want exactly 1", len(doom))
	}
	want := "Doom loop detected: 3 identical consecutive tool calls (read_file, read_file)"
	if got, _ := doom[0].Data["description"].(string); got != want {
		t.Fatalf("node_tool_doom_loop description = %q, want %q", got, want)
	}
	states := recorder.ofType(eventbus.EventLoopStateChanged)
	if len(states) == 0 {
		t.Fatal("no loop_state_changed event published")
	}
	final := states[len(states)-1].Data
	if final["activity"] != "interrupted" || final["park_reason"] != string(agentloop.ParkReasonDoomLoop) {
		t.Fatalf("final loop state = %#v, want interrupted/doom_loop", final)
	}
}

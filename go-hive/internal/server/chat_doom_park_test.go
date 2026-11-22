package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// identicalToolRunner repeats one tool result so the deterministic doom-loop
// guard sees the same call, arguments and result every iteration.
func identicalToolRunner(context.Context, string, map[string]any) (string, bool, error) {
	return "unchanged result", false, nil
}

// Parity-exempt: upstream has no deterministic doom-loop park (its stall guard
// is judge-based, framework/server/stall_check.py::subscribe_stall_check).
// This pins that a park the loop raises itself reaches the chat runtime's
// event stream and pending_input as doom_loop, not the generic llm_error.
func TestChatDoomLoopParkReasonReachesEventsAndPendingInput(t *testing.T) {
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	responses := make([]llm.Completion, 0, 8)
	for index := 0; index < 8; index++ {
		responses = append(responses, llm.Completion{
			ToolCalls: []llm.ToolCall{{
				ID:        fmt.Sprintf("repeat-%d", index),
				Name:      "read_file",
				Arguments: `{"path":"same-file.txt"}`,
			}},
		})
	}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{responses: responses},
		Tools:     []llm.ToolDef{{Name: "read_file", Description: "read a file", Parameters: map[string]any{"type": "object"}}},
		Execute:   identicalToolRunner,
		Root:      root,
		Bus:       bus,
		SessionID: "session_20260101_000000_doom001",
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

	if err := runtime.Deliver("read the same file again"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonDoomLoop); err != nil {
		t.Fatalf("%v", err)
	}

	// The failure event carries the loop's park, not a generic llm_error.
	states := recorder.ofType(eventbus.EventLoopStateChanged)
	if len(states) == 0 {
		t.Fatal("no loop_state_changed event published")
	}
	final := states[len(states)-1].Data
	if final["activity"] != "interrupted" || final["park_reason"] != string(agentloop.ParkReasonDoomLoop) {
		t.Fatalf("final loop state = %#v, want interrupted/doom_loop", final)
	}
	if doom := recorder.ofType(eventbus.EventNodeToolDoomLoop); len(doom) == 0 {
		t.Fatal("no node_tool_doom_loop event published")
	}
	// The persisted park agrees with both.
	pending := waitForParkReason(t, root, string(agentloop.ParkReasonDoomLoop))
	if pending["reason"] != string(agentloop.ParkReasonDoomLoop) {
		t.Fatalf("pending_input reason = %#v", pending["reason"])
	}
}

package server

// P0-14 / A12 live-host wiring: the chat runtime's loop must publish the
// replay-detection event on the session bus, and its turn emitter must carry
// the LLM completion's credits. Both are pinned through a real turn, not by
// inspecting the fields, so a call site that drops either wiring fails here.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// P0-14: a cross-turn repeat the soft replay tier notices is published as
// tool_call_replay_detected on the session bus. Before this wiring the loop's
// ReplayEvents was nil in every live chat runtime, so the event never reached
// SSE or events.jsonl.
//
// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected
func TestChatRuntimePublishesToolCallReplayDetected(t *testing.T) {
	const toolName = "linear_issue_get"
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: toolName, Arguments: `{"issue_id":"ENG-1"}`}}, StopReason: "tool_calls"},
		{ToolCalls: []llm.ToolCall{{ID: "call-2", Name: toolName, Arguments: `{"issue_id":"ENG-1"}`}}, StopReason: "tool_calls"},
		{Content: "done"},
	}}
	runtime, bus := newBusChatRuntime(t, model,
		[]llm.ToolDef{{Name: toolName, Description: "fetch one issue", Parameters: map[string]any{"type": "object"}}},
		func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
	)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("look it up twice"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	events := waitForChatEvents(t, recorder, eventbus.EventToolCallReplayDetected, 1)
	if len(events) != 1 {
		t.Fatalf("tool_call_replay_detected events = %d, want 1", len(events))
	}
	event := events[0]
	if event.StreamID != "queen" {
		t.Fatalf("replay event stream = %q, want queen", event.StreamID)
	}
	if event.NodeID == nil || *event.NodeID != "queen" {
		t.Fatalf("replay event node = %v, want queen", event.NodeID)
	}
	if name, _ := event.Data["tool_name"].(string); name != toolName {
		t.Fatalf("replay event tool_name = %#v, want %s", event.Data["tool_name"], toolName)
	}
	if prior, _ := event.Data["prior_seq"].(int); prior <= 0 {
		t.Fatalf("replay event prior_seq = %#v, want the prior call's seq", event.Data["prior_seq"])
	}
}

// A12: Completion.Credits reaches llm_turn_complete as the credits field. The
// emitters previously passed nil unconditionally, so a Hive proxy charge never
// reached the session UI.
//
// Parity: core/framework/llm/litellm.py::_credits_from_usage
// Parity: framework/agent_loop/internals/event_publishing.py::publish_llm_turn_complete
func TestChatRuntimePublishesTurnCredits(t *testing.T) {
	credits := 0.25
	model := &queenChatLLM{responses: []llm.Completion{{Content: "done", Credits: &credits}}}
	runtime, bus := newBusChatRuntime(t, model, nil, nil)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	events := waitForChatEvents(t, recorder, eventbus.EventLLMTurnComplete, 1)
	got, present := events[0].Data["credits"]
	if !present {
		t.Fatalf("llm_turn_complete carries no credits: %#v", events[0].Data)
	}
	if amount, ok := got.(json.Number); !ok || amount != json.Number("0.25") {
		t.Fatalf("llm_turn_complete credits = %#v, want 0.25", got)
	}
}

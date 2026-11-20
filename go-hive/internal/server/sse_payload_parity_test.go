package server

// SSE payload parity pins for the sse-payload-parity lane: the session events
// feed's default type filter, its replay set, and the per-turn payload fields
// the dual-run probe (scripts/sse_payload_parity.py) found missing.

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

// upstreamDefaultSessionEventTypes is routes_events.py::DEFAULT_EVENT_TYPES
// verbatim: the curated chat feed a session subscriber receives when it passes
// no ?types= filter. Keep both lists in sync; the upstream set is the contract.
var upstreamDefaultSessionEventTypes = []string{
	"session_snapshot",
	"client_output_delta",
	"client_input_requested",
	"client_input_received",
	"client_input_committed",
	"llm_text_delta",
	"llm_reasoning_delta",
	"client_reasoning",
	"tool_call_started",
	"tool_call_completed",
	"execution_started",
	"execution_completed",
	"execution_failed",
	"execution_paused",
	"payment_required",
	"node_loop_started",
	"node_loop_iteration",
	"node_loop_completed",
	"llm_turn_complete",
	"goal_progress",
	"node_internal_output",
	"node_stalled",
	"node_retry",
	"node_tool_doom_loop",
	"loop_state_changed",
	"context_compaction_started",
	"context_compacted",
	"context_usage_updated",
	"worker_colony_loaded",
	"colony_created",
	"colony_suggestion_requested",
	"credentials_required",
	"client_credential_form_requested",
	"skill_install_requested",
	"skill_install_decided",
	"subagent_report",
	"queen_phase_changed",
	"trigger_available",
	"trigger_activated",
	"trigger_deactivated",
	"trigger_fired",
	"trigger_removed",
	"trigger_updated",
	"task_created",
	"task_updated",
	"task_deleted",
	"task_list_reset",
	"session_forked",
	"session_title_updated",
}

// Parity: framework/server/routes_events.py::DEFAULT_EVENT_TYPES
func TestSessionEventsDefaultFilterMatchesUpstream(t *testing.T) {
	if len(defaultSessionEventTypes) != len(upstreamDefaultSessionEventTypes) {
		t.Fatalf("default filter has %d entries, upstream has %d", len(defaultSessionEventTypes), len(upstreamDefaultSessionEventTypes))
	}
	for _, name := range upstreamDefaultSessionEventTypes {
		if _, present := defaultSessionEventTypes[name]; !present {
			t.Fatalf("default filter missing upstream type %q", name)
		}
		// Every name must resolve to a real EventType, so a typo cannot turn
		// into a silently never-delivered event.
		if _, ok := eventbus.LookupEventType(name); !ok {
			t.Fatalf("default filter names unknown event type %q", name)
		}
	}
	// Unknown names are dropped from an explicit ?types= filter, and a filter
	// that resolves to nothing falls back to the default set at the call site
	// (upstream's `result or DEFAULT_EVENT_TYPES`).
	if got := parseEventTypeFilter("not_a_type,also_not"); len(got) != 0 {
		t.Fatalf("unknown-only filter = %v, want empty so the caller falls back", got)
	}
	if got := parseEventTypeFilter("client_output_delta,not_a_type"); len(got) != 1 {
		t.Fatalf("mixed filter = %v, want only the known name", got)
	}
}

// Parity: framework/server/routes_events.py::_REPLAY_TYPES
func TestSessionEventsReplaySetMatchesUpstream(t *testing.T) {
	upstream := map[eventbus.EventType]struct{}{
		eventbus.EventClientOutputDelta:    {},
		eventbus.EventExecutionStarted:     {},
		eventbus.EventExecutionCompleted:   {},
		eventbus.EventLLMTurnComplete:      {},
		eventbus.EventToolCallStarted:      {},
		eventbus.EventToolCallCompleted:    {},
		eventbus.EventClientInputRequested: {},
		eventbus.EventClientInputReceived:  {},
		eventbus.EventClientInputCommitted: {},
		eventbus.EventTriggerAvailable:     {},
		eventbus.EventTriggerActivated:     {},
		eventbus.EventTriggerDeactivated:   {},
		eventbus.EventTriggerFired:         {},
		eventbus.EventTriggerRemoved:       {},
		eventbus.EventTriggerUpdated:       {},
		eventbus.EventTaskCreated:          {},
		eventbus.EventTaskUpdated:          {},
		eventbus.EventTaskDeleted:          {},
		eventbus.EventTaskListReset:        {},
		eventbus.EventSessionForked:        {},
	}
	if len(sessionEventsReplayTypes) != len(upstream) {
		t.Fatalf("replay set has %d entries, upstream has %d", len(sessionEventsReplayTypes), len(upstream))
	}
	for eventType := range upstream {
		if _, present := sessionEventsReplayTypes[eventType]; !present {
			t.Fatalf("replay set missing upstream type %q", eventType)
		}
	}
	// Upstream never replays execution_failed (only started/completed); a
	// reconnect must not re-surface a past failure as a live state change.
	if _, present := sessionEventsReplayTypes[eventbus.EventExecutionFailed]; present {
		t.Fatal("replay set contains execution_failed, which upstream does not replay")
	}
}

// Parity: framework/server/routes_events.py::handle_events
func TestSessionEventsReplayHonoursTypeFilter(t *testing.T) {
	bus := eventbus.NewEventBus()
	delta := eventbus.NewAgentEvent(eventbus.EventClientOutputDelta, "queen")
	delta.Data = map[string]any{"content": "x", "snapshot": "x", "inner_turn": 0}
	bus.Publish(delta)
	turnComplete := eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, "queen")
	turnComplete.Data = map[string]any{"stop_reason": "end_turn"}
	bus.Publish(turnComplete)

	// A subscriber that asked for one type gets only that type replayed:
	// upstream intersects the replay set with the subscribed filter
	// (_REPLAY_TYPES & event_type_values).
	filtered := make(chan map[string]any, 8)
	replaySessionEvents(bus, filtered, map[string]struct{}{"client_output_delta": {}}, false, nil)
	if len(filtered) != 1 {
		t.Fatalf("filtered replay queued %d frames, want only the requested type", len(filtered))
	}
	if payload := <-filtered; payload["type"] != "client_output_delta" {
		t.Fatalf("filtered replay frame = %#v, want client_output_delta", payload["type"])
	}

	// Without an explicit filter the caller passes the default set; the whole
	// replayable subset follows, oldest first.
	unfiltered := make(chan map[string]any, 8)
	replaySessionEvents(bus, unfiltered, defaultSessionEventTypes, false, nil)
	if len(unfiltered) != 2 {
		t.Fatalf("unfiltered replay queued %d frames, want both replayable events", len(unfiltered))
	}
	if payload := <-unfiltered; payload["type"] != "client_output_delta" {
		t.Fatalf("replay order starts with %#v, want the oldest event first", payload["type"])
	}
	if payload := <-unfiltered; payload["type"] != "llm_turn_complete" {
		t.Fatalf("replay order continues with %#v, want llm_turn_complete", payload["type"])
	}
}

// Parity: framework/agent_loop/agent_loop.py::_publish_iteration
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
// Parity: framework/agent_loop/agent_loop.py::_drain_injection_queue
func TestChatTurnCarriesPhaseCommitAndContextUsage(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &scriptedChatLLM{},
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_20260101_000000_abcdef12",
		Phase:     func() string { return "independent" },
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	recorder := recordChatEvents(bus)
	if err := runtime.DeliverClientInput("hello queen", "corr-sse-1"); err != nil {
		t.Fatalf("DeliverClientInput: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	// node_loop_iteration carries the queen phase on every iteration, as the
	// upstream loop's iteration_metadata_provider reports it.
	iterations := recorder.ofType(eventbus.EventNodeLoopIteration)
	if len(iterations) == 0 {
		t.Fatalf("no node_loop_iteration events; got %v", recorder.types())
	}
	last := iterations[len(iterations)-1]
	if last.Data["phase"] != "independent" {
		t.Fatalf("node_loop_iteration data = %#v, want phase=independent", last.Data)
	}

	// client_input_committed marks the conversation seq the user message
	// landed at and pairs with the received event's correlation id.
	committed := recorder.ofType(eventbus.EventClientInputCommitted)
	if len(committed) != 1 {
		t.Fatalf("client_input_committed count = %d, want 1 (%v)", len(committed), recorder.types())
	}
	if _, ok := committed[0].Data["seq"].(int); !ok {
		t.Fatalf("client_input_committed data = %#v, want an int seq", committed[0].Data)
	}
	if committed[0].CorrelationID == nil || *committed[0].CorrelationID != "corr-sse-1" {
		t.Fatalf("client_input_committed correlation_id = %v, want corr-sse-1", committed[0].CorrelationID)
	}

	// The iteration_start context readout follows the iteration on a normal
	// turn (the opening turn keeps the session-start block's single copy).
	usage := recorder.ofType(eventbus.EventContextUsageUpdated)
	if len(usage) == 0 {
		t.Fatalf("no context_usage_updated events; got %v", recorder.types())
	}
	lastUsage := usage[len(usage)-1]
	for _, key := range []string{
		"usage_ratio", "usage_pct", "message_count", "estimated_tokens",
		"max_context_tokens", "trigger", "breakdown",
	} {
		if _, present := lastUsage.Data[key]; !present {
			t.Fatalf("context_usage_updated missing %q: %#v", key, lastUsage.Data)
		}
	}
	if lastUsage.Data["trigger"] != "iteration_start" {
		t.Fatalf("context_usage_updated trigger = %#v, want iteration_start", lastUsage.Data["trigger"])
	}
	if lastUsage.ExecutionID != nil {
		t.Fatalf("context_usage_updated execution_id = %v, want null", *lastUsage.ExecutionID)
	}
}

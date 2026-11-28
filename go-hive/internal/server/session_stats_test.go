package server

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

// statsProbeEventTypes seeds the event bus so colony merge tests can assert the
// events_by_type counters survive the overlay.
var statsProbeEventTypes = []string{
	"client_input_committed",
	"client_input_received",
	"client_input_requested",
	"client_output_delta",
	"client_reasoning",
	"context_usage_updated",
	"llm_reasoning_delta",
	"llm_turn_complete",
	"loop_state_changed",
	"node_loop_iteration",
	"node_loop_started",
	"session_snapshot",
	"session_title_updated",
	"worker_completed",
	"worker_started",
}

func seedStatsEventBus(session *Session) {
	bus := eventbus.NewEventBus()
	for _, name := range statsProbeEventTypes {
		bus.Publish(eventbus.NewAgentEvent(eventbus.EventType(name), session.ID))
	}
	bus.Subscribe([]eventbus.EventType{eventbus.EventType(statsProbeEventTypes[0])}, func(*eventbus.AgentEvent) {})
	session.EventBus = bus
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_session_stats
func TestSessionStatsQueenOnlyWithoutColonyIsEmpty(t *testing.T) {
	// Upstream: `stats = session.colony.get_stats() if session.colony else {}`.
	// A queen-only session has no colony, so the body is the empty object — not
	// a synthesized baseline with keys Python never sends.
	session := makeTestSession("queen_only")
	session.ColonyID = ""
	seedStatsEventBus(session)
	session.PutTrigger(triggers.TriggerDefinition{ID: "daily", TriggerType: "timer"})
	session.PutTrigger(triggers.TriggerDefinition{ID: "hook", TriggerType: "webhook"})
	app := newAppWithSession(t, session)

	status, body, _ := getJSON(t, app, "/api/sessions/queen_only/stats")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(body) != 0 {
		t.Fatalf("colony-less stats = %#v, want {}", body)
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_session_stats
func TestSessionStatsQueenOnlyRunningWithChat(t *testing.T) {
	session := makeTestSession("chatty")
	session.Chat, _ = newTestChatRuntime(t, t.TempDir())
	app := newAppWithSession(t, session)

	status, body, _ := getJSON(t, app, "/api/sessions/chatty/stats")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	// running is a colony field upstream: without a colony the payload stays
	// empty even while a chat runtime is attached.
	if len(body) != 0 {
		t.Fatalf("colony-less stats = %#v, want {}", body)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_stats
func TestSessionStatsColonyMergeKeepsSchema(t *testing.T) {
	session := makeTestSession("test_agent")
	seedStatsEventBus(session)
	session.Colony = &fakeColony{streamID: "colony_a", stats: map[string]any{
		"running":        true,
		"active_workers": 3,
		"total_workers":  5,
		"triggers":       4,
		"executions":     2,
		// A partial nested block: the merge must not drop the keys the colony
		// omitted (subscriptions / events_by_type).
		"event_bus": map[string]any{"total_events": 99},
	}}
	app := newAppWithSession(t, session)

	status, body, _ := getJSON(t, app, "/api/sessions/test_agent/stats")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}

	if body["running"] != true || body["active_workers"] != float64(3) ||
		body["total_workers"] != float64(5) || body["triggers"] != float64(4) {
		t.Fatalf("colony values did not win: %#v", body)
	}
	if body["executions"] != float64(2) {
		t.Fatalf("extra colony key dropped: executions = %#v", body["executions"])
	}

	eventBus, ok := body["event_bus"].(map[string]any)
	if !ok {
		t.Fatalf("event_bus = %#v, want an object", body["event_bus"])
	}
	if eventBus["total_events"] != float64(99) {
		t.Fatalf("colony total_events did not win: %#v", eventBus["total_events"])
	}
	if eventBus["subscriptions"] != float64(1) {
		t.Fatalf("subscriptions lost in merge: %#v", eventBus["subscriptions"])
	}
	if byType, ok := eventBus["events_by_type"].(map[string]any); !ok || len(byType) != len(statsProbeEventTypes) {
		t.Fatalf("events_by_type lost in merge: %#v", eventBus["events_by_type"])
	}

	budget, ok := body["adaptive_tool_budget"].(map[string]any)
	if !ok {
		t.Fatalf("adaptive_tool_budget = %#v, want an object", body["adaptive_tool_budget"])
	}
	for _, key := range []string{"enabled", "nominal", "samples"} {
		if _, present := budget[key]; !present {
			t.Fatalf("adaptive_tool_budget.%s lost in merge: %#v", key, budget)
		}
	}
	if body["colony_id"] != "test_agent" {
		t.Fatalf("colony_id = %#v, want the session ColonyID", body["colony_id"])
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_session_stats
func TestSessionStatsUnknownSession(t *testing.T) {
	app := newEmptyAppWithHome(t)

	status, body, _ := getJSON(t, app, "/api/sessions/nonexistent/stats")
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["error"] != "Session 'nonexistent' not found" {
		t.Fatalf("error = %#v", body["error"])
	}
}

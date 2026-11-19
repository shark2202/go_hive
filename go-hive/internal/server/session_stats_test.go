package server

import (
	"sort"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/triggers"
)

// A/B reference: the Python probe recorded 28 JSON fields for a live session
// (6 object leaves + 15 events_by_type counters + running/triggers/ids). The
// fixed part of the schema is 13 fields; events_by_type grows one leaf per
// distinct event type, so the fixture below seeds exactly 15 types.
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

// fixedSessionStatsTypes is the recursive key/type map of the stable fields,
// spelled against the Python payload: bool/str/int/int/int + two objects.
var fixedSessionStatsTypes = map[string]string{
	"running":                      "boolean",
	"colony_id":                    "string",
	"active_workers":               "number",
	"total_workers":                "number",
	"triggers":                     "number",
	"event_bus":                    "object",
	"event_bus.total_events":       "number",
	"event_bus.subscriptions":      "number",
	"event_bus.events_by_type":     "object",
	"adaptive_tool_budget":         "object",
	"adaptive_tool_budget.enabled": "boolean",
	"adaptive_tool_budget.nominal": "null",
	"adaptive_tool_budget.samples": "number",
}

// flattenJSONFields records "<path>": "<json type>" for every JSON field below
// value. Containers are recorded as objects/arrays; scalars get their JSON type.
func flattenJSONFields(value any, prefix string, out map[string]string) {
	if prefix != "" {
		switch value.(type) {
		case map[string]any:
			out[prefix] = "object"
		case []any:
			out[prefix] = "array"
		case bool:
			out[prefix] = "boolean"
		case string:
			out[prefix] = "string"
		case float64:
			out[prefix] = "number"
		case nil:
			out[prefix] = "null"
		}
	}
	if object, ok := value.(map[string]any); ok {
		for key, nested := range object {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			flattenJSONFields(nested, path, out)
		}
	}
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
func TestSessionStatsQueenOnlyFullSchema(t *testing.T) {
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

	fields := map[string]string{}
	flattenJSONFields(body, "", fields)

	// The exact shape the Python endpoint served in the A/B run: 13 fixed
	// fields plus one counter per event type (15) = 28.
	if len(fields) != 28 {
		t.Fatalf("field count = %d, want 28: %#v", len(fields), fields)
	}
	for path, wantType := range fixedSessionStatsTypes {
		gotType, present := fields[path]
		if !present {
			t.Fatalf("missing field %q in %#v", path, fields)
		}
		if gotType != wantType {
			t.Fatalf("%s type = %s, want %s", path, gotType, wantType)
		}
	}

	// colony_id falls back to the session id when no colony was spawned.
	if body["colony_id"] != "queen_only" {
		t.Fatalf("colony_id = %#v, want the session id", body["colony_id"])
	}
	// Colony-less sessions have no workers; triggers counts definitions.
	if body["active_workers"] != float64(0) || body["total_workers"] != float64(0) {
		t.Fatalf("worker counters = %#v/%#v, want 0/0", body["active_workers"], body["total_workers"])
	}
	if body["triggers"] != float64(2) {
		t.Fatalf("triggers = %#v, want 2", body["triggers"])
	}
	if body["running"] != false {
		t.Fatalf("running = %#v, want false without a chat runtime", body["running"])
	}

	eventBus, ok := body["event_bus"].(map[string]any)
	if !ok {
		t.Fatalf("event_bus = %#v, want an object", body["event_bus"])
	}
	if eventBus["total_events"] != float64(len(statsProbeEventTypes)) {
		t.Fatalf("total_events = %#v, want %d", eventBus["total_events"], len(statsProbeEventTypes))
	}
	if eventBus["subscriptions"] != float64(1) {
		t.Fatalf("subscriptions = %#v, want 1", eventBus["subscriptions"])
	}
	byType, ok := eventBus["events_by_type"].(map[string]any)
	if !ok || len(byType) != len(statsProbeEventTypes) {
		t.Fatalf("events_by_type = %#v, want %d entries", eventBus["events_by_type"], len(statsProbeEventTypes))
	}
	names := make([]string, 0, len(byType))
	for name := range byType {
		names = append(names, name)
	}
	sort.Strings(names)
	if names[0] != "client_input_committed" || names[len(names)-1] != "worker_started" {
		t.Fatalf("events_by_type keys = %#v", names)
	}
	for name, count := range byType {
		if count != float64(1) {
			t.Fatalf("events_by_type[%s] = %#v, want 1", name, count)
		}
	}

	budget, ok := body["adaptive_tool_budget"].(map[string]any)
	if !ok {
		t.Fatalf("adaptive_tool_budget = %#v, want an object", body["adaptive_tool_budget"])
	}
	if budget["nominal"] != nil {
		t.Fatalf("nominal = %#v, want null before any adaptive sample", budget["nominal"])
	}
	if budget["samples"] != float64(0) {
		t.Fatalf("samples = %#v, want 0", budget["samples"])
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
	if body["running"] != true {
		t.Fatalf("running = %#v, want true while a chat runtime is attached", body["running"])
	}
	if _, ok := body["event_bus"].(map[string]any); !ok {
		t.Fatalf("event_bus = %#v, want an object even without a bus", body["event_bus"])
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

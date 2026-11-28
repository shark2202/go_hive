package server

// Connect-time trigger rehydration parity: routes_events.py rebuilds the full
// trigger set from the live session on every SSE connect, so a newly opened
// colony page renders every trigger card even when the bounded event history
// has aged the trigger lifecycle events out.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

func openSessionEventsStream(t *testing.T, app *App, sessionID string) *bufio.Reader {
	t.Helper()
	server := httptest.NewServer(app.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/sessions/"+sessionID+"/events", nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("build events request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("GET events stream: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		cancel()
		server.Close()
		t.Fatalf("events stream status = %d, want 200", response.StatusCode)
	}
	t.Cleanup(func() {
		response.Body.Close()
		cancel()
		server.Close()
	})
	return bufio.NewReader(response.Body)
}

func readSessionEventFrame(t *testing.T, reader *bufio.Reader) map[string]any {
	t.Helper()
	name, data := readEventFrame(t, reader)
	if name != "" {
		t.Fatalf("SSE frame carries event name %q; upstream sends data-only frames", name)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode SSE frame %q: %v", data, err)
	}
	return payload
}

func expectSnapshotFrame(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	frame := readSessionEventFrame(t, reader)
	if frame["type"] != "session_snapshot" {
		t.Fatalf("first SSE frame type = %#v, want session_snapshot", frame["type"])
	}
}

func triggerEventData(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if frame["stream_id"] != "queen" {
		t.Fatalf("trigger frame stream_id = %#v, want queen", frame["stream_id"])
	}
	data, ok := frame["data"].(map[string]any)
	if !ok {
		t.Fatalf("trigger frame data = %#v, want object", frame["data"])
	}
	return data
}

func expectTriggerFrame(t *testing.T, reader *bufio.Reader, wantType, wantID, wantName string) map[string]any {
	t.Helper()
	frame := readSessionEventFrame(t, reader)
	if frame["type"] != wantType {
		t.Fatalf("trigger frame type = %#v, want %s", frame["type"], wantType)
	}
	data := triggerEventData(t, frame)
	if data["trigger_id"] != wantID {
		t.Fatalf("trigger_id = %#v, want %s", data["trigger_id"], wantID)
	}
	if data["name"] != wantName {
		t.Fatalf("trigger name = %#v, want %q", data["name"], wantName)
	}
	if data["trigger_type"] != "timer" {
		t.Fatalf("trigger_type = %#v, want timer", data["trigger_type"])
	}
	if _, ok := data["trigger_config"].(map[string]any); !ok {
		t.Fatalf("trigger_config = %#v, want object", data["trigger_config"])
	}
	return data
}

// Parity: framework/server/routes_events.py::_authoritative_trigger_events
// Parity: framework/server/routes_events.py::handle_events (connect-time trigger rehydration)
func TestSessionEventsStreamRebuildsFullTriggerState(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := newTriggerTestSession(t, "colony_session", bus)
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "alpha-poll",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"interval_minutes": 15},
		Description:   "Alpha poll",
		Task:          "poll the source",
	})
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "beta-digest",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"cron": "0 9 * * *"},
	})
	session.ActivateTrigger("alpha-poll")
	session.SetTriggerNextFire("alpha-poll", session.NowMonotonic()+120)
	lastFired := int64(1700000000000)
	session.RecordTriggerFire("alpha-poll", triggers.TriggerFireStat{FireCount: 3, LastFiredAt: &lastFired})

	rebuilt := authoritativeTriggerEvents(session)
	if len(rebuilt) != 2 {
		t.Fatalf("rebuilt %d trigger events, want 2", len(rebuilt))
	}

	reader := openSessionEventsStream(t, newAppWithSession(t, session), session.ID)
	expectSnapshotFrame(t, reader)

	// Enabled trigger: ACTIVATED with live countdown and fire badge merged into
	// trigger_config, exactly like SessionManager._emit_trigger_events.
	active := expectTriggerFrame(t, reader, "trigger_activated", "alpha-poll", "Alpha poll")
	activeConfig := active["trigger_config"].(map[string]any)
	if activeConfig["interval_minutes"] != float64(15) {
		t.Fatalf("active trigger_config = %#v, want interval_minutes 15", activeConfig)
	}
	if _, ok := activeConfig["next_fire_in"]; !ok {
		t.Fatalf("active trigger_config missing next_fire_in: %#v", activeConfig)
	}
	if _, ok := activeConfig["next_fire_at"]; !ok {
		t.Fatalf("active trigger_config missing next_fire_at: %#v", activeConfig)
	}
	if activeConfig["fire_count"] != float64(3) {
		t.Fatalf("active trigger fire_count = %#v, want 3", activeConfig["fire_count"])
	}
	if activeConfig["last_fired_at"] != float64(lastFired) {
		t.Fatalf("active trigger last_fired_at = %#v, want %d", activeConfig["last_fired_at"], lastFired)
	}

	// Disabled trigger: AVAILABLE, name falls back to the id when the
	// description is empty, and no runtime state is invented for it.
	inactive := expectTriggerFrame(t, reader, "trigger_available", "beta-digest", "beta-digest")
	inactiveConfig := inactive["trigger_config"].(map[string]any)
	if inactiveConfig["cron"] != "0 9 * * *" {
		t.Fatalf("inactive trigger_config = %#v, want cron", inactiveConfig)
	}
	if _, ok := inactiveConfig["next_fire_in"]; ok {
		t.Fatalf("inactive trigger_config unexpectedly has next_fire_in: %#v", inactiveConfig)
	}

	// The rebuild is ephemeral: it never enters the bus history, so a later
	// connect cannot replay it as a duplicate ring frame.
	if history := bus.GetHistory(eventbus.WithHistoryLimit(-1)); len(history) != 0 {
		t.Fatalf("authoritative trigger frames were published to the bus (%d history entries)", len(history))
	}
}

// Parity: framework/server/routes_events.py::_authoritative_trigger_events
// (the rebuild reads live session state, not stale ring entries)
func TestTriggerReplayTracksRemovedCreatedAndRuntimeState(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := newTriggerTestSession(t, "colony_session", bus)
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "alpha",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"interval_minutes": 5},
		Description:   "Alpha",
		Task:          "alpha task",
	})
	session.ActivateTrigger("alpha")
	app := newAppWithSession(t, session)

	// Connect 1: the initial trigger is active.
	reader := openSessionEventsStream(t, app, session.ID)
	expectSnapshotFrame(t, reader)
	expectTriggerFrame(t, reader, "trigger_activated", "alpha", "Alpha")

	// Removed + created between connections: the old definition must vanish
	// and the new one must appear even though neither emitted an SSE event.
	session.RemoveTrigger("alpha")
	session.DeactivateTrigger("alpha")
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "beta",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"cron": "*/10 * * * *"},
		Description:   "Beta",
		Task:          "beta task",
	})
	reader = openSessionEventsStream(t, app, session.ID)
	expectSnapshotFrame(t, reader)
	expectTriggerFrame(t, reader, "trigger_available", "beta", "Beta")

	// Runtime state changes again: activating the surviving trigger flips the
	// replayed card to ACTIVATED and carries the live schedule.
	session.ActivateTrigger("beta")
	session.SetTriggerNextFire("beta", session.NowMonotonic()+60)
	reader = openSessionEventsStream(t, app, session.ID)
	expectSnapshotFrame(t, reader)
	data := expectTriggerFrame(t, reader, "trigger_activated", "beta", "Beta")
	config := data["trigger_config"].(map[string]any)
	if _, ok := config["next_fire_in"]; !ok {
		t.Fatalf("reactivated trigger_config missing next_fire_in: %#v", config)
	}
}

// Parity: framework/server/routes_events.py::_authoritative_trigger_events
//
// Negative regression: on a chatty colony the bounded ring buffer has already
// aged out the trigger lifecycle events, so a ring-only connect replays ZERO
// trigger frames. The live-session rebuild is what rehydrates the cards; drop
// it and this test's HTTP assertions fail.
func TestTriggerRebuildRequiredWhenRingHistoryAgedOut(t *testing.T) {
	bus := eventbus.NewEventBus(eventbus.WithMaxHistory(2))
	session := newTriggerTestSession(t, "colony_session", bus)
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "alpha",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"interval_minutes": 5},
		Description:   "Alpha",
		Task:          "alpha task",
	})
	session.ActivateTrigger("alpha")

	// Simulate session load publishing the activation, then enough chatty
	// traffic to evict it from the bounded history.
	activation := eventbus.NewAgentEvent(eventbus.EventTriggerActivated, "queen")
	activation.Data = map[string]any{"trigger_id": "alpha"}
	bus.Publish(activation)
	for _, content := range []string{"one", "two"} {
		delta := eventbus.NewAgentEvent(eventbus.EventClientOutputDelta, "queen")
		delta.Data = map[string]any{"content": content}
		bus.Publish(delta)
	}

	// Ring replay alone: the activation is gone, so no trigger frame exists.
	ringOnly := make(chan map[string]any, 8)
	replaySessionEvents(bus, ringOnly, defaultSessionEventTypes, false, nil)
	close(ringOnly)
	ringFrames := 0
	for frame := range ringOnly {
		ringFrames++
		if frame["type"] == "trigger_activated" || frame["type"] == "trigger_available" {
			t.Fatalf("ring replay unexpectedly carried a trigger frame: %#v", frame)
		}
	}
	if ringFrames != 2 {
		t.Fatalf("ring replay queued %d frames, want the 2 surviving deltas", ringFrames)
	}

	// A real connect replays the surviving deltas first (oldest first), then
	// the full trigger state rebuilt from the live session — Python's ordering.
	reader := openSessionEventsStream(t, newAppWithSession(t, session), session.ID)
	expectSnapshotFrame(t, reader)
	for _, content := range []string{"one", "two"} {
		frame := readSessionEventFrame(t, reader)
		if frame["type"] != "client_output_delta" {
			t.Fatalf("replayed frame type = %#v, want client_output_delta before triggers", frame["type"])
		}
		data := frame["data"].(map[string]any)
		if data["content"] != content {
			t.Fatalf("replayed delta content = %#v, want %q", data["content"], content)
		}
	}
	expectTriggerFrame(t, reader, "trigger_activated", "alpha", "Alpha")

	// And it did not grow the ring: the rebuild is not a bus publish.
	if history := bus.GetHistory(eventbus.WithHistoryLimit(-1)); len(history) != 2 {
		t.Fatalf("connect changed bus history length to %d, want 2 (no duplicate ring replay)", len(history))
	}
}

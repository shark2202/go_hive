package server

// The upstream queen loop's first iteration: a freshly created session runs
// one real model turn before any user message, parks on turn_done, and the
// visible frames (snapshot, delta, turn complete, input request) are what a
// reconnect replays. These tests pin that boundary end to end.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: core/framework/server/tests/test_channels.py::test_the_queen_is_heard_turn_by_turn_in_order
func TestSessionOpeningTurnRunsBeforeUserInput(t *testing.T) {
	session, manager, _ := newStartEventsSession(t)
	bus := session.EventBus
	recorder := recordChatEvents(bus)
	manager.AddSession(session)

	if !session.QueenLoopFresh {
		t.Fatal("AddSession did not mark the fresh segment; the opening turn would never start")
	}
	session.Chat.StartOpeningTurn()
	waitForChatEvents(t, recorder, eventbus.EventClientInputRequested, 1)
	waitForChatEvents(t, recorder, eventbus.EventLoopStateChanged, 1)

	want := []string{
		"queen_identity_selected",
		"reminder_injected",
		"node_loop_started",
		"node_loop_iteration",
		"context_usage_updated",
		"client_output_delta",
		"llm_turn_complete",
		"client_input_requested",
		"loop_state_changed",
	}
	if got := recorder.types(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("opening turn events = %v, want %v", got, want)
	}

	// The loop's first iteration is iteration 0 upstream, and the turn carries
	// no execution/node-loop envelope (the session-start block already wrote
	// those).
	delta := recorder.ofType(eventbus.EventClientOutputDelta)[0]
	if delta.Data["iteration"] != 0 {
		t.Fatalf("opening client_output_delta iteration = %#v, want 0", delta.Data["iteration"])
	}
	if _, ok := delta.Data["content"].(string); !ok {
		t.Fatalf("opening client_output_delta data = %#v", delta.Data)
	}

	turnComplete := recorder.ofType(eventbus.EventLLMTurnComplete)[0]
	if turnComplete.Data["iteration"] != 0 {
		t.Fatalf("opening llm_turn_complete iteration = %#v, want 0", turnComplete.Data["iteration"])
	}
	for _, key := range []string{
		"stop_reason", "model", "input_tokens", "output_tokens", "cached_tokens",
		"cache_creation_tokens", "cost_usd", "iteration", "system_prefix_sha",
		"system_suffix_sha", "history_anchor_idx", "message_count",
	} {
		if _, present := turnComplete.Data[key]; !present {
			t.Fatalf("opening llm_turn_complete missing %q: %#v", key, turnComplete.Data)
		}
	}

	request := recorder.ofType(eventbus.EventClientInputRequested)[0]
	if request.Data["park_reason"] != "turn_done" {
		t.Fatalf("opening client_input_requested park_reason = %#v, want turn_done", request.Data["park_reason"])
	}
	state := recorder.ofType(eventbus.EventLoopStateChanged)[0]
	if state.Data["activity"] != "awaiting_user" || state.Data["park_reason"] != "turn_done" {
		t.Fatalf("opening loop state = %#v, want awaiting_user/turn_done", state.Data)
	}

	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["activity"] != "awaiting_user" || snapshot["park_reason"] != "turn_done" {
		t.Fatalf("snapshot = %#v, want the parked opening state", snapshot)
	}
	if snapshot["queen_busy_reason"] != "awaiting_input" {
		t.Fatalf("snapshot queen_busy_reason = %#v, want awaiting_input", snapshot["queen_busy_reason"])
	}
	if _, ok := snapshot["last_event_seq"].(int); !ok {
		t.Fatalf("snapshot last_event_seq = %#v, want the unresolved request seq", snapshot["last_event_seq"])
	}
}

// Parity: core/framework/server/tests/test_api.py::TestSSEFormat::test_events_route_does_not_pass_event_param
// Parity: core/framework/server/tests/test_channels.py::test_the_queen_is_heard_turn_by_turn_in_order
func TestSessionEventsReplayAfterOpeningTurn(t *testing.T) {
	session, manager, root := newStartEventsSession(t)
	manager.AddSession(session)
	session.Chat.StartOpeningTurn()

	recorder := recordChatEvents(session.EventBus)
	waitForChatEvents(t, recorder, eventbus.EventClientInputRequested, 1)

	app := NewApp(AppDeps{
		Manager:      manager,
		LiveSessions: manager.ListSessions,
		Home:         hivehome.Home{Root: root},
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	// A fresh subscriber gets the synthesized snapshot, then the buffered
	// visible-chat history in order. Frames are data-only, as upstream's
	// send_event(data) call leaves off the event: name.
	response, err := http.Get(server.URL + "/api/sessions/" + startEventsSessionID + "/events")
	if err != nil {
		t.Fatalf("GET events stream: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)

	wantTypes := []string{"session_snapshot", "client_output_delta", "llm_turn_complete", "client_input_requested"}
	for index, wantType := range wantTypes {
		name, data := readEventFrame(t, reader)
		if name != "" {
			t.Fatalf("frame %d carries event name %q; upstream sends data-only frames", index, name)
		}
		frame := map[string]any{}
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			t.Fatalf("decode frame %d: %v", index, err)
		}
		if frame["type"] != wantType {
			t.Fatalf("frame %d type = %#v, want %q", index, frame["type"], wantType)
		}
	}
}

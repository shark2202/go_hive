package server

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

var stallJudgeSlot = map[string]any{
	"protocol":  "fake",
	"api_base":  "https://judge.invalid/v1",
	"model":     "judge-1",
	"api_key":   "k",
	"threshold": 0.5,
}

type fakeStallJudge struct {
	mu     sync.Mutex
	p      float64
	states []map[string]any
}

func (f *fakeStallJudge) score(_ map[string]any, _ string, state map[string]any, _ map[string]string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, state)
	return f.p, nil
}

func (f *fakeStallJudge) recorded() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.states...)
}

func writeStallConfig(t *testing.T, home hivehome.Home, enabled bool) {
	t.Helper()
	slot := map[string]any{}
	for key, value := range stallJudgeSlot {
		slot[key] = value
	}
	writeServerTestJSON(t, home.ConfigurationFile(), map[string]any{
		"stall_check": enabled,
		"judge":       slot,
	})
}

func watchStallCheck(t *testing.T, p float64, enabled bool) (*eventbus.EventBus, *fakeQueen, *fakeStallJudge, *StallCheckSubscription) {
	t.Helper()
	t.Setenv("HIVE_STALL_CHECK", "")
	home := serverTestHome(t)
	writeStallConfig(t, home, enabled)

	bus := eventbus.NewEventBus()
	node := &fakeQueen{}
	judge := &fakeStallJudge{p: p}
	handle := SubscribeStallCheck(bus, home, func() QueenNode { return node }, map[string]JudgeProtocol{"fake": judge.score})
	t.Cleanup(handle.Close)
	return bus, node, judge, handle
}

func publishQueen(t *testing.T, bus *eventbus.EventBus, eventType eventbus.EventType, data map[string]any) {
	t.Helper()
	event := eventbus.NewAgentEvent(eventType, "queen")
	event.Data = data
	bus.Publish(event)
}

func stalledTurn(t *testing.T, bus *eventbus.EventBus, handle *StallCheckSubscription) {
	t.Helper()
	publishQueen(t, bus, eventbus.EventClientInputReceived, map[string]any{"content": "[Owner(1)]: draw something nice"})
	publishQueen(t, bus, eventbus.EventClientReasoning, map[string]any{"reasoning": "I'll curl the image API, then attach_file. Let me run it."})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "On it, drawing one now."})
	publishQueen(t, bus, eventbus.EventClientInputRequested, map[string]any{"park_reason": "turn_done"})
	handle.Wait()
}

// Parity: framework/server/tests/test_stall_check.py::test_a_turn_that_announces_work_and_stops_gets_one_reminder
func TestATurnThatAnnouncesWorkAndStopsGetsOneReminder(t *testing.T) {
	bus, node, judge, handle := watchStallCheck(t, 0.95, true)

	stalledTurn(t, bus, handle)
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "Still on it."})
	publishQueen(t, bus, eventbus.EventClientInputRequested, map[string]any{"park_reason": "turn_done"})
	handle.Wait()

	if got := node.callCount(); got != 1 {
		t.Fatalf("queen reminded %d times, want once", got)
	}
	text, isClientInput := node.lastCall(t)
	if text != StallReminder || isClientInput {
		t.Fatalf("reminder = (%q, %v), want (StallReminder, false)", text, isClientInput)
	}
	want := []map[string]any{{
		"user_message":             "[Owner(1)]: draw something nice",
		"assistant_reasoning_tail": "I'll curl the image API, then attach_file. Let me run it.",
		"assistant_reply":          "On it, drawing one now.",
		"tools_called_this_turn":   []string{},
	}}
	if got := judge.recorded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("judge states = %#v, want %#v", got, want)
	}
}

// Parity: framework/server/tests/test_stall_check.py::test_a_turn_the_judge_finds_fine_is_left_alone
func TestATurnTheJudgeFindsFineIsLeftAlone(t *testing.T) {
	bus, node, judge, handle := watchStallCheck(t, 0.05, true)

	stalledTurn(t, bus, handle)

	if got := len(judge.recorded()); got != 1 {
		t.Fatalf("judge asked %d times, want once", got)
	}
	if got := node.callCount(); got != 0 {
		t.Fatalf("queen reminded %d times, want none", got)
	}
}

// Parity: framework/server/tests/test_stall_check.py::test_a_failed_model_call_is_not_judged
func TestAFailedModelCallIsNotJudged(t *testing.T) {
	bus, node, judge, handle := watchStallCheck(t, 0.95, true)

	publishQueen(t, bus, eventbus.EventClientInputReceived, map[string]any{"content": "[Cy(7)]: （戳了戳你）"})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "[Error: LLM call failed: Stream error: Authentication Fails. Please try again.]"})
	publishQueen(t, bus, eventbus.EventClientInputRequested, map[string]any{"park_reason": "turn_done"})
	handle.Wait()

	if got := judge.recorded(); len(got) != 0 {
		t.Fatalf("judge states = %#v, want none", got)
	}
	if got := node.callCount(); got != 0 {
		t.Fatalf("queen reminded %d times, want none", got)
	}
}

// Parity: framework/server/tests/test_stall_check.py::test_nothing_is_judged_while_the_option_is_off
func TestNothingIsJudgedWhileTheOptionIsOff(t *testing.T) {
	bus, node, judge, handle := watchStallCheck(t, 0.95, false)

	stalledTurn(t, bus, handle)

	if got := judge.recorded(); len(got) != 0 {
		t.Fatalf("judge states = %#v, want none", got)
	}
	if got := node.callCount(); got != 0 {
		t.Fatalf("queen reminded %d times, want none", got)
	}
}

// Parity: framework/server/tests/test_stall_check.py::test_the_judge_reads_the_last_model_call
func TestTheJudgeReadsTheLastModelCall(t *testing.T) {
	bus, _, judge, handle := watchStallCheck(t, 0.05, true)

	publishQueen(t, bus, eventbus.EventClientInputReceived, map[string]any{"content": "draw"})
	publishQueen(t, bus, eventbus.EventClientReasoning, map[string]any{"reasoning": "first, read the skill"})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "Reading up."})
	publishQueen(t, bus, eventbus.EventToolCallStarted, map[string]any{"tool_name": "terminal_exec"})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "Here it is."})
	publishQueen(t, bus, eventbus.EventClientInputRequested, map[string]any{"park_reason": "turn_done"})
	handle.Wait()

	states := judge.recorded()
	if len(states) == 0 {
		t.Fatal("judge was never asked")
	}
	if got := states[0]["assistant_reasoning_tail"]; got != "" {
		t.Fatalf("assistant_reasoning_tail = %v, want empty", got)
	}
	if got := states[0]["assistant_reply"]; got != "Here it is." {
		t.Fatalf("assistant_reply = %v, want %q", got, "Here it is.")
	}
	if got := states[0]["tools_called_this_turn"]; !reflect.DeepEqual(got, []string{"terminal_exec"}) {
		t.Fatalf("tools_called_this_turn = %#v, want [terminal_exec]", got)
	}
}

// Parity: framework/server/tests/test_stall_check.py::test_a_correction_is_judged_with_the_turn_it_corrects
func TestACorrectionIsJudgedWithTheTurnItCorrects(t *testing.T) {
	bus, _, judge, handle := watchStallCheck(t, 0.05, true)

	publishQueen(t, bus, eventbus.EventClientInputReceived, map[string]any{"content": "[Ava(1)]: draw a fox"})
	publishQueen(t, bus, eventbus.EventToolCallStarted, map[string]any{"tool_name": "terminal_exec"})
	publishQueen(t, bus, eventbus.EventToolCallStarted, map[string]any{"tool_name": "attach_file"})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "Here's your fox."})
	publishQueen(t, bus, eventbus.EventClientInputRequested, map[string]any{"park_reason": "turn_done"})
	handle.Wait()
	publishQueen(t, bus, eventbus.EventClientInputReceived, map[string]any{"content": "[Ava(1)]: its paw has six fingers"})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "You're right, the paw should have five."})
	publishQueen(t, bus, eventbus.EventClientInputRequested, map[string]any{"park_reason": "turn_done"})
	handle.Wait()

	states := judge.recorded()
	if len(states) != 2 {
		t.Fatalf("judge asked %d times, want twice", len(states))
	}
	if _, present := states[0]["previous_turn"]; present {
		t.Fatalf("first turn has previous_turn = %#v, want none", states[0]["previous_turn"])
	}
	wantPrevious := map[string]any{
		"assistant_reply": "Here's your fox.",
		"tools_called":    []string{"terminal_exec", "attach_file"},
	}
	if got := states[1]["previous_turn"]; !reflect.DeepEqual(got, wantPrevious) {
		t.Fatalf("previous_turn = %#v, want %#v", got, wantPrevious)
	}
	if got := states[1]["tools_called_this_turn"]; !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("tools_called_this_turn = %#v, want []", got)
	}
}

// Parity-exempt: the port publishes the stall on the session bus (upstream only
// reminded the queen); this pins the observable signal the dashboard sees.
func TestAStalledTurnPublishesANodeStalledEvent(t *testing.T) {
	bus, _, _, handle := watchStallCheck(t, 0.95, true)
	events := make(chan *eventbus.AgentEvent, 4)
	bus.Subscribe([]eventbus.EventType{eventbus.EventNodeStalled}, func(event *eventbus.AgentEvent) {
		events <- event
	})

	stalledTurn(t, bus, handle)

	select {
	case event := <-events:
		if event.StreamID != "queen" {
			t.Fatalf("stream = %q, want queen", event.StreamID)
		}
		if got := event.Data["reason"]; got != "stall_check" {
			t.Fatalf("reason = %v, want stall_check", got)
		}
		if got := event.Data["probability"]; got != 0.95 {
			t.Fatalf("probability = %v, want 0.95", got)
		}
		if got := event.Data["threshold"]; got != 0.5 {
			t.Fatalf("threshold = %v, want 0.5", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled turn published no node_stalled event")
	}
}

// Parity-exempt: a turn the judge clears must stay silent on the bus; the stall
// signal is only worth anything if it does not fire on healthy turns.
func TestAHealthyTurnPublishesNoStallEvent(t *testing.T) {
	bus, _, _, handle := watchStallCheck(t, 0.05, true)
	events := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventNodeStalled}, func(event *eventbus.AgentEvent) {
		events <- event
	})

	stalledTurn(t, bus, handle)

	select {
	case event := <-events:
		t.Fatalf("healthy turn published %v", event.Data)
	case <-time.After(100 * time.Millisecond):
	}
}

// Parity-exempt: this port's chat emitter publishes the turn-end park as
// loop_state_changed(awaiting_user, park_reason=turn_done) rather than
// client_input_requested; the subscription must accept that shape or the check
// would stay structurally inert in a chat session.
func TestThePortTurnEndShapeReachesTheStallCheck(t *testing.T) {
	bus, node, judge, handle := watchStallCheck(t, 0.95, true)

	publishQueen(t, bus, eventbus.EventClientInputReceived, map[string]any{"content": "[Owner(1)]: draw something nice"})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "On it, drawing one now."})
	publishQueen(t, bus, eventbus.EventLoopStateChanged, map[string]any{
		"activity":    "awaiting_user",
		"park_reason": "turn_done",
	})
	handle.Wait()

	if got := len(judge.recorded()); got != 1 {
		t.Fatalf("judge asked %d times, want once", got)
	}
	if got := node.callCount(); got != 1 {
		t.Fatalf("queen reminded %d times, want once", got)
	}

	// An ask_user park is an external wait, not a finished turn: no judge.
	publishQueen(t, bus, eventbus.EventClientInputReceived, map[string]any{"content": "which one?"})
	publishQueen(t, bus, eventbus.EventClientOutputDelta, map[string]any{"snapshot": "Pick one and I'll draw it."})
	publishQueen(t, bus, eventbus.EventLoopStateChanged, map[string]any{
		"activity":    "awaiting_user",
		"park_reason": "ask_user",
	})
	handle.Wait()
	if got := len(judge.recorded()); got != 1 {
		t.Fatalf("judge asked %d times after an ask_user park, want 1", got)
	}
}

// Parity: framework/server/tests/test_stall_check.py::test_the_judge_slot_saves_without_the_chat_key_check_and_the_option_toggles
func TestTheJudgeSlotSavesWithoutTheChatKeyCheckAndTheOptionToggles(t *testing.T) {
	home := newFeatureHome(t)

	checked := false
	deps := ConfigRouteDeps{
		Home: home,
		Role: RoleWriteDeps{Validate: func(string, string, string, string) (any, string) {
			checked = true
			return false, "not a chat model"
		}},
	}
	server := newConfigRoutes(t, deps)

	status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/llm-sections", map[string]any{"role": "judge", "section": stallJudgeSlot})
	if status != 200 {
		t.Fatalf("save judge section: status = %d, want 200", status)
	}
	if checked {
		t.Fatal("provider key check ran for the judge slot, want it untouched")
	}

	_, sections := configDo(t, http.MethodGet, server.URL+"/api/config/llm-sections", nil)
	if !reflect.DeepEqual(sections["judge"], stallJudgeSlot) {
		t.Fatalf("sections[judge] = %#v, want the slot verbatim", sections["judge"])
	}

	if status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/features", map[string]any{"features": map[string]any{"stall_check": true}}); status != 200 {
		t.Fatalf("enable stall_check: status = %d, want 200", status)
	}
	_, features := configDo(t, http.MethodGet, server.URL+"/api/config/features", nil)
	if got := featuresBlock(t, features)["stall_check"]; got != true {
		t.Fatalf("features[stall_check] = %#v, want true", got)
	}
	if StallJudgeSlot(home) == nil {
		t.Fatal("judge slot unusable after stall_check was enabled through the route")
	}
}

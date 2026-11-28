package server

// Colony creation dispatch at the chat runtime: a successful suggest_colony /
// task_create(new_colony=true) turn parks on colony_suggestion, publishes the
// variant-correct COLONY_SUGGESTION_REQUESTED event, and keeps the rich
// payload for the accept/dismiss routes and a restart.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func colonyDispatchRuntime(t *testing.T, response llm.Completion, phase string, session *Session, tools []llm.ToolDef) (*ChatRuntime, *eventbus.EventBus) {
	t.Helper()
	bus := eventbus.NewEventBus()
	model := &queenChatLLM{responses: []llm.Completion{response}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:   model,
		Tools: tools,
		Execute: func(context.Context, string, map[string]any) (string, bool, error) {
			return "should not execute", true, nil
		},
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: session.ID,
		QueenName: "default",
		Phase:     func() string { return phase },
		Session:   session,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	return runtime, bus
}

// Parity: framework/agent_loop/agent_loop.py:5150-5175 (suggest_colony success)
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (DM event)
func TestSuggestColonyParksAndEmitsDMVariant(t *testing.T) {
	session := NewSession("session_20260101_000000_sug001", nil)
	runtime, bus := colonyDispatchRuntime(t, llm.Completion{
		ToolCalls: []llm.ToolCall{{
			ID:        "s1",
			Name:      agentloop.SuggestColonyToolName,
			Arguments: `{"colony_id":"morning_hn_digest","reason":"23 profiles to research in parallel"}`,
		}},
	}, "independent", session, nil)
	session.EventBus = bus
	defer runtime.Close()
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("this needs to run every morning"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	events := recorder.ofType(eventbus.EventColonySuggestionRequested)
	if len(events) != 1 {
		t.Fatalf("colony_suggestion_requested events = %d, want 1", len(events))
	}
	data := events[0].Data
	if data["colony_id"] != "morning_hn_digest" {
		t.Fatalf("colony_id = %#v", data["colony_id"])
	}
	if data["reason"] != "23 profiles to research in parallel" {
		t.Fatalf("reason = %#v", data["reason"])
	}
	if data["source_session_id"] != session.ID {
		t.Fatalf("source_session_id = %#v, want %q", data["source_session_id"], session.ID)
	}
	if _, pivotVariant := data["source_phase"]; pivotVariant {
		t.Fatalf("DM suggestion carried source_phase: %#v", data)
	}
	if clientInputs := recorder.ofType(eventbus.EventClientInputRequested); len(clientInputs) != 0 {
		t.Fatalf("client_input_requested = %d, want 0 (the colony event replaces it)", len(clientInputs))
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonColonySuggestion); err != nil {
		t.Fatalf("%v", err)
	}
}

// Parity: framework/agent_loop/agent_loop.py:5270-5330 (new_colony success)
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (colony event)
func TestTaskCreateNewColonyParksStashesPivotAndEmitsColonyVariant(t *testing.T) {
	session := NewSession("session_20260101_000000_piv001", nil)
	runtime, bus := colonyDispatchRuntime(t, llm.Completion{
		ToolCalls: []llm.ToolCall{{
			ID:        "t1",
			Name:      agentloop.TaskCreateToolName,
			Arguments: `{"new_colony":true,"goal":"daily news digest","handoff":"full objective brief","tasks":[{"subject":"one"},{"subject":"two"}]}`,
		}},
	}, "colony", session, nil)
	session.EventBus = bus
	defer runtime.Close()
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("also build a daily news digest"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	events := recorder.ofType(eventbus.EventColonySuggestionRequested)
	if len(events) != 1 {
		t.Fatalf("colony_suggestion_requested events = %d, want 1", len(events))
	}
	data := events[0].Data
	if data["colony_id"] != "" {
		t.Fatalf("colony_id = %#v, want the blank user-filled slug", data["colony_id"])
	}
	if data["source_phase"] != "colony" {
		t.Fatalf("source_phase = %#v, want colony", data["source_phase"])
	}
	if data["goal"] != "daily news digest" || data["handoff"] != "full objective brief" {
		t.Fatalf("goal/handoff = %#v / %#v", data["goal"], data["handoff"])
	}
	if data["task_count"] != 2 {
		t.Fatalf("task_count = %#v, want 2", data["task_count"])
	}
	if data["source_session_id"] != session.ID {
		t.Fatalf("source_session_id = %#v, want %q", data["source_session_id"], session.ID)
	}
	if !session.HasPendingColonyPivot() {
		t.Fatal("session.pending_colony_pivot not set: the accept path has nothing to seed from")
	}
	pivot, _ := session.PendingColonyPivot().(map[string]any)
	if pivot["goal"] != "daily news digest" || pivot["handoff"] != "full objective brief" {
		t.Fatalf("pending pivot = %#v", pivot)
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonColonySuggestion); err != nil {
		t.Fatalf("%v", err)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_await_user_input (park payload persisted)
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
func TestColonyPivotParkPersistsCursorPayloadAndDismissWakes(t *testing.T) {
	rootSession := NewSession("session_20260101_000000_piv002", nil)
	bus := eventbus.NewEventBus()
	model := &queenChatLLM{responses: []llm.Completion{{
		ToolCalls: []llm.ToolCall{{
			ID:        "t1",
			Name:      agentloop.TaskCreateToolName,
			Arguments: `{"new_colony":true,"goal":"goal","handoff":"handoff","tasks":[{"subject":"s"}]}`,
		}},
	}}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: model, Execute: func(context.Context, string, map[string]any) (string, bool, error) { return "", false, nil },
		Root: t.TempDir(), Bus: bus, SessionID: rootSession.ID, QueenName: "default",
		Phase: func() string { return "colony" }, Session: rootSession,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	defer runtime.Close()
	if err := runtime.Deliver("pivot please"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonColonySuggestion); err != nil {
		t.Fatalf("%v", err)
	}

	// The cursor carries the rich pivot, which is what a restarted runtime
	// re-announces and what the accept route seeds from after a restart.
	// waitForParkReason polls: setParkState marks awaiting before it writes
	// the cursor, so an immediate read can race the persist.
	pending := waitForParkReason(t, runtime.deps.Root, string(agentloop.ParkReasonColonySuggestion))
	if persisted, _ := pending["colony_pivot"].(map[string]any); persisted == nil || persisted["goal"] != "goal" {
		t.Fatalf("persisted colony_pivot = %#v", pending["colony_pivot"])
	}

	// Dismiss wakes the source queen whose turn parked on the popup.
	session := NewSession(rootSession.ID, bus)
	session.SetPendingColonyPivot(map[string]any{"goal": "goal"})
	queen := &pivotQueenNode{}
	session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": queen}}
	manager := NewSessionManager()
	manager.AddSession(session)
	mux := sessionRoutesMux(SessionRouteDeps{Manager: manager})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sessions/"+session.ID+"/dismiss-colony-pivot", "")
	if recorder.Code != http.StatusOK || body["dismissed"] != true {
		t.Fatalf("dismiss status/body = %d/%#v", recorder.Code, body)
	}
	texts, clients := queen.calls()
	if len(texts) != 1 || texts[0] != colonyPivotDismissMessage || !clients[0] {
		t.Fatalf("injected = %#v / %#v", texts, clients)
	}
	if session.HasPendingColonyPivot() {
		t.Fatal("pending pivot still set after dismissal")
	}
}

// Parity: framework/agent_loop/agent_loop.py::_await_user_input (restored wait re-emits)
func TestRestoredColonySuggestionReannouncesAfterRestart(t *testing.T) {
	root := t.TempDir()
	sessionID := "session_20260101_000000_sug002"
	firstBus := eventbus.NewEventBus()
	first, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &queenChatLLM{responses: []llm.Completion{{
			ToolCalls: []llm.ToolCall{{
				ID: "s1", Name: agentloop.SuggestColonyToolName,
				Arguments: `{"colony_id":"restart_probe"}`,
			}},
		}}},
		Execute: func(context.Context, string, map[string]any) (string, bool, error) { return "", false, nil },
		Root:    root, Bus: firstBus, SessionID: sessionID, QueenName: "default",
		Phase: func() string { return "independent" },
	})
	if err != nil {
		t.Fatalf("first NewChatRuntime: %v", err)
	}
	if err := first.Deliver("suggest it"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(first, agentloop.ParkReasonColonySuggestion); err != nil {
		t.Fatalf("%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	secondBus := eventbus.NewEventBus()
	recorder := recordChatEvents(secondBus)
	second, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:     &queenChatLLM{responses: []llm.Completion{{Content: "resumed"}}},
		Execute: func(context.Context, string, map[string]any) (string, bool, error) { return "", false, nil },
		Root:    root, Bus: secondBus, SessionID: sessionID, QueenName: "default",
		Phase: func() string { return "independent" },
	})
	if err != nil {
		t.Fatalf("second NewChatRuntime: %v", err)
	}
	defer second.Close()

	events := waitForChatEvents(t, recorder, eventbus.EventColonySuggestionRequested, 1)
	if events[0].Data["colony_id"] != "restart_probe" {
		t.Fatalf("restored event = %#v", events[0].Data)
	}
}

// Parity: framework/server/queen_orchestrator.py::_phase_tools
// Parity: framework/agents/queen/nodes/__init__.py (suggest_colony independent-only)
func TestToolsForTurnAdvertisesSuggestColonyOnlyInIndependentPhase(t *testing.T) {
	session := NewSession("session_20260101_000000_phs001", nil)
	independent, _ := colonyDispatchRuntime(t, llm.Completion{Content: "ok"}, "independent", session, nil)
	defer independent.Close()
	names := llmToolNameSet(independent.toolsForTurn())
	if !names[agentloop.SuggestColonyToolName] {
		t.Fatalf("independent tools = %#v, want suggest_colony", names)
	}

	colonySession := NewSession("session_20260101_000000_phs002", nil)
	colony, _ := colonyDispatchRuntime(t, llm.Completion{Content: "ok"}, "colony", colonySession, nil)
	defer colony.Close()
	names = llmToolNameSet(colony.toolsForTurn())
	if names[agentloop.SuggestColonyToolName] {
		t.Fatalf("colony tools = %#v, want suggest_colony removed", names)
	}
}

func llmToolNameSet(defs []llm.ToolDef) map[string]bool {
	out := make(map[string]bool, len(defs))
	for _, def := range defs {
		out[def.Name] = true
	}
	return out
}

// Parity: core/framework/server/queen_orchestrator.py::_make_pivot_payload_sink
func TestColonyPivotSinkVetoesForkKickoffWithoutStashing(t *testing.T) {
	session := NewSession("session_20260101_000000_veto001", nil)
	session.ForkKickoffPending = true
	runtime, _ := colonyDispatchRuntime(t, llm.Completion{Content: "ok"}, "colony", session, nil)
	defer runtime.Close()

	veto := runtime.sinkColonyPivot(map[string]any{"goal": "chain fork"})
	const want = "This colony was just created by a fork — its task plan " +
		"already exists and this IS the new colony. Do NOT spawn " +
		"another colony or re-create tasks. Call task_list to " +
		"see the plan, then task_update to work it. new_colony " +
		"becomes available again only after the user sends a new " +
		"message in this colony."
	if veto != want {
		t.Fatalf("veto = %q, want the upstream fork-kickoff text", veto)
	}
	if _, pivot := runtime.pendingColonyDispatch(); pivot != nil {
		t.Fatalf("runtime stash = %#v, want nil after a fork-kickoff veto", pivot)
	}
	if session.HasPendingColonyPivot() {
		t.Fatal("session.pending_colony_pivot set after a fork-kickoff veto")
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_make_pivot_payload_sink
func TestColonyPivotSinkVetoesOpenRuntimePopupWithoutOverwriting(t *testing.T) {
	session := NewSession("session_20260101_000000_veto002", nil)
	runtime, _ := colonyDispatchRuntime(t, llm.Completion{Content: "ok"}, "colony", session, nil)
	defer runtime.Close()

	runtime.rememberPendingColonyPivot(map[string]any{"goal": "first pivot"})
	// Isolate the runtime-level stash: the session mirror has its own test.
	session.ClearPendingColonyPivot()

	veto := runtime.sinkColonyPivot(map[string]any{"goal": "second pivot"})
	const want = "A new_colony popup is already open and waiting for the " +
		"user. Do not call task_create(new_colony=true) again " +
		"until the current popup resolves."
	if veto != want {
		t.Fatalf("veto = %q, want the upstream concurrent-popup text", veto)
	}
	_, pivot := runtime.pendingColonyDispatch()
	if pivot == nil || pivot["goal"] != "first pivot" {
		t.Fatalf("runtime stash = %#v, want the first pivot untouched", pivot)
	}
	if session.HasPendingColonyPivot() {
		t.Fatal("veto wrote the payload to the session stash")
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_make_pivot_payload_sink
func TestColonyPivotSinkVetoesOpenSessionPopupWithoutOverwriting(t *testing.T) {
	session := NewSession("session_20260101_000000_veto003", nil)
	session.SetPendingColonyPivot(map[string]any{"goal": "first pivot"})
	runtime, _ := colonyDispatchRuntime(t, llm.Completion{Content: "ok"}, "colony", session, nil)
	defer runtime.Close()

	veto := runtime.sinkColonyPivot(map[string]any{"goal": "second pivot"})
	const want = "A new_colony popup is already open and waiting for the " +
		"user. Do not call task_create(new_colony=true) again " +
		"until the current popup resolves."
	if veto != want {
		t.Fatalf("veto = %q, want the upstream concurrent-popup text", veto)
	}
	if _, pivot := runtime.pendingColonyDispatch(); pivot != nil {
		t.Fatalf("runtime stash = %#v, want nil: the veto must not stash", pivot)
	}
	pending, _ := session.PendingColonyPivot().(map[string]any)
	if pending == nil || pending["goal"] != "first pivot" {
		t.Fatalf("session pending = %#v, want the first pivot untouched", pending)
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_make_pivot_payload_sink
func TestColonyPivotSinkStashesPayloadWhenNoVeto(t *testing.T) {
	session := NewSession("session_20260101_000000_veto004", nil)
	runtime, _ := colonyDispatchRuntime(t, llm.Completion{Content: "ok"}, "colony", session, nil)
	defer runtime.Close()

	payload := map[string]any{
		"goal":    "new colony",
		"handoff": "full objective brief",
		"tasks":   []map[string]any{{"subject": "s"}},
	}
	if veto := runtime.sinkColonyPivot(payload); veto != "" {
		t.Fatalf("veto = %q, want no veto on the clean path", veto)
	}
	_, pivot := runtime.pendingColonyDispatch()
	if pivot == nil || pivot["goal"] != "new colony" || pivot["handoff"] != "full objective brief" {
		t.Fatalf("runtime stash = %#v", pivot)
	}
	pending, _ := session.PendingColonyPivot().(map[string]any)
	if pending == nil || pending["goal"] != "new colony" {
		t.Fatalf("session pending = %#v", pending)
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_make_pivot_payload_sink
// Parity: framework/agent_loop/agent_loop.py:5272-5295 (sink veto skips the popup)
func TestForkKickoffPivotVetoIsAnIsErrorResultAndSkipsThePopup(t *testing.T) {
	session := NewSession("session_20260101_000000_veto005", nil)
	session.ForkKickoffPending = true
	runtime, bus := colonyDispatchRuntime(t, llm.Completion{
		ToolCalls: []llm.ToolCall{{
			ID:        "t1",
			Name:      agentloop.TaskCreateToolName,
			Arguments: `{"new_colony":true,"goal":"chain fork","handoff":"brief","tasks":[{"subject":"s"}]}`,
		}},
	}, "colony", session, nil)
	session.EventBus = bus
	defer runtime.Close()
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("kickoff"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	if events := recorder.ofType(eventbus.EventColonySuggestionRequested); len(events) != 0 {
		t.Fatalf("colony_suggestion_requested = %d, want 0 (the veto must not open a popup)", len(events))
	}
	if session.HasPendingColonyPivot() {
		t.Fatal("session.pending_colony_pivot set after the veto")
	}
	model, ok := runtime.deps.LLM.(*queenChatLLM)
	if !ok {
		t.Fatalf("runtime LLM = %T, want *queenChatLLM", runtime.deps.LLM)
	}
	sawVeto := false
	for _, request := range model.requestSnapshot() {
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "This colony was just created by a fork") &&
				strings.Contains(message.Content, "new_colony becomes available again") {
				sawVeto = true
			}
		}
	}
	if !sawVeto {
		t.Fatal("the model never received the fork-kickoff veto as a tool result")
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_make_pivot_payload_sink
func TestColonyPivotSinkNilSafety(t *testing.T) {
	var nilRuntime *ChatRuntime
	if veto := nilRuntime.sinkColonyPivot(map[string]any{"goal": "g"}); veto != "" {
		t.Fatalf("nil runtime veto = %q, want no veto", veto)
	}
	session := NewSession("session_20260101_000000_veto006", nil)
	runtime, _ := colonyDispatchRuntime(t, llm.Completion{Content: "ok"}, "colony", session, nil)
	defer runtime.Close()
	if veto := runtime.sinkColonyPivot(nil); veto != "" {
		t.Fatalf("nil payload veto = %q, want no veto", veto)
	}
	if _, pivot := runtime.pendingColonyDispatch(); pivot != nil {
		t.Fatalf("runtime stash = %#v, want nil for a nil payload", pivot)
	}
}

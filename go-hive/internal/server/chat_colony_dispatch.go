package server

// Colony creation dispatch at the chat-runtime boundary.
//
// The agent loop intercepts the two colony tool calls (suggest_colony and
// task_create(new_colony=true)) before any executor runs and returns the
// payload on TurnResult. This file is the host half: it parks the turn on the
// user (reason colony_suggestion), stashes the rich payload for the accept and
// dismiss routes, and publishes COLONY_SUGGESTION_REQUESTED — the two event
// variants upstream's _await_user_input emits.

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// sinkColonyPivot stashes the rich task_create(new_colony=true) payload on the
// session while the tool call is still in flight, mirroring upstream's
// pivot_payload_sink (session.pending_colony_pivot). It never vetoes: this
// runtime has no fork-kickoff guard to apply, so it returns "".
//
// Parity: framework/server/queen_orchestrator.py::_make_pivot_payload_sink
func (r *ChatRuntime) sinkColonyPivot(payload map[string]any) string {
	if r == nil || payload == nil {
		return ""
	}
	r.rememberPendingColonyPivot(payload)
	return ""
}

// rememberPendingColonyPivot stores the pivot payload on the runtime and, when
// the session record is wired, on the session (where the accept and dismiss
// routes read it).
func (r *ChatRuntime) rememberPendingColonyPivot(payload map[string]any) {
	cloned := cloneAnyMap(payload)
	r.mu.Lock()
	r.pendingColonyPivot = cloned
	r.mu.Unlock()
	if r.deps.Session != nil {
		r.deps.Session.SetPendingColonyPivot(cloned)
	}
}

// rememberPendingColonySuggestion stores the DM suggestion payload for the
// persisted park state.
func (r *ChatRuntime) rememberPendingColonySuggestion(payload map[string]any) {
	cloned := cloneAnyMap(payload)
	r.mu.Lock()
	r.pendingColonySuggestion = cloned
	r.mu.Unlock()
}

// pendingColonyDispatch returns the park payloads the current turn waits on.
func (r *ChatRuntime) pendingColonyDispatch() (suggestion, pivot map[string]any) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneAnyMap(r.pendingColonySuggestion), cloneAnyMap(r.pendingColonyPivot)
}

// clearPendingColony drops both colony park payloads. The next user message (a
// dismissal, the accept route's confirmation, or an ordinary chat turn) is the
// answer, so the stash does not outlive the wait.
func (r *ChatRuntime) clearPendingColony() {
	if r == nil {
		return
	}
	r.mu.Lock()
	had := r.pendingColonySuggestion != nil || r.pendingColonyPivot != nil
	r.pendingColonySuggestion = nil
	r.pendingColonyPivot = nil
	r.mu.Unlock()
	if had && r.deps.Session != nil {
		r.deps.Session.ClearPendingColonyPivot()
	}
}

// parkColonyTurn applies a TurnResult's colony payloads: it stashes them and
// reports the park reason the turn must settle on. Nil payloads leave the
// reason untouched.
func (r *ChatRuntime) parkColonyTurn(result agentloop.TurnResult) (agentloop.ParkReason, bool) {
	if result.PendingColonyPivot != nil {
		r.rememberPendingColonyPivot(result.PendingColonyPivot)
		return agentloop.ParkReasonColonySuggestion, true
	}
	if result.PendingColonySuggestion != nil {
		r.rememberPendingColonySuggestion(result.PendingColonySuggestion)
		return agentloop.ParkReasonColonySuggestion, true
	}
	return "", false
}

// turnParkReason resolves the park a finished turn ends on: a colony dispatch
// (the suggest_colony / task_create(new_colony=true) popup) outranks an
// ask_user park, whose question is the wait only when no popup is open.
//
// Parity: framework/agent_loop/agent_loop.py::_await_user_input
func (r *ChatRuntime) turnParkReason(result agentloop.TurnResult, emitter *chatTurnEmitter) agentloop.ParkReason {
	if colonyReason, parked := r.parkColonyTurn(result); parked {
		emitter.emitColonySuggestionForPark(result)
		return colonyReason
	}
	if pending := r.pendingParkReason(); pending != "" {
		return pending
	}
	return agentloop.ParkReasonTurnDone
}

// emitColonySuggestion publishes COLONY_SUGGESTION_REQUESTED for the current
// park. The two variants share the event and are discriminated by
// source_phase: the DM suggestion carries the queen-proposed colony_id and
// optional reason; the colony pivot carries a blank colony_id, the
// queen-authored goal/handoff and the plan's task count.
//
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (colony branches)
func (e *chatTurnEmitter) emitColonySuggestion(suggestion, pivot map[string]any) {
	if e == nil || e.bus == nil {
		return
	}
	var executionID *string
	if strings.TrimSpace(e.executionID) != "" {
		executionID = &e.executionID
	}
	if pivot != nil {
		goal, _ := pivot["goal"].(string)
		handoff, _ := pivot["handoff"].(string)
		sourcePhase := agentLoopString(pivot["source_phase"])
		if sourcePhase == "" {
			sourcePhase = "colony"
		}
		taskCount := len(agentloopColonyTasks(pivot["tasks"]))
		sourceSessionID := e.sessionID
		e.bus.EmitColonySuggestionRequested(
			e.streamID,
			e.nodeID,
			executionID,
			"",
			nil,
			optionalColonyString(sourceSessionID),
			optionalColonyString(sourcePhase),
			optionalColonyString(goal),
			optionalColonyString(handoff),
			&taskCount,
		)
		return
	}
	if suggestion == nil {
		return
	}
	colonyID := agentLoopString(suggestion["colony_id"])
	reason := strings.TrimSpace(agentLoopString(suggestion["reason"]))
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	e.bus.EmitColonySuggestionRequested(
		e.streamID,
		e.nodeID,
		executionID,
		colonyID,
		reasonPtr,
		optionalColonyString(e.sessionID),
		nil,
		nil,
		nil,
		nil,
	)
}

// restoreColonySuggestion re-announces a persisted colony park after a
// restart, the way upstream's restored _await_user_input re-enters its wait.
func (r *ChatRuntime) restoreColonySuggestion(reason agentloop.ParkReason) bool {
	if r == nil || r.bus == nil || reason != agentloop.ParkReasonColonySuggestion {
		return false
	}
	suggestion, pivot := r.pendingColonyDispatch()
	if suggestion == nil && pivot == nil {
		return false
	}
	emitter := &chatTurnEmitter{
		bus:         r.bus,
		streamID:    chatStreamID,
		nodeID:      chatNodeID,
		sessionID:   r.sessionID,
		executionID: "",
	}
	emitter.emitColonySuggestion(suggestion, pivot)
	return true
}

// emitColonySuggestionForPark is the live-turn entry point: it publishes the
// event for the payload the loop just parked on, before executionCompleted.
func (e *chatTurnEmitter) emitColonySuggestionForPark(result agentloop.TurnResult) {
	if e == nil {
		return
	}
	e.emitColonySuggestion(result.PendingColonySuggestion, result.PendingColonyPivot)
}

// agentloopColonyTasks normalizes the payload's tasks for the count.
func agentloopColonyTasks(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if object, ok := entry.(map[string]any); ok {
				out = append(out, object)
			}
		}
		return out
	}
	return nil
}

func agentLoopString(value any) string {
	text, _ := value.(string)
	return text
}

func optionalColonyString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// cloneAnyMap copies one level of a payload map (the tasks slice is shared, as
// upstream's stash keeps the same list).
func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

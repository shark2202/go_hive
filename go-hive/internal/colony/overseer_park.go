package colony

// Shared pending question / park state for the persistent overseer.
//
// The chat front end and the colony overseer are two conversations over one
// client surface. When the overseer calls ask_user the question has to become
// the session's parked wait — the same pending_input record a chat queen's
// ask_user writes — instead of living only in the overseer's own transcript.
// This file owns the colony's half: the runtime-side question and the
// client_input_requested event the session bridge mirrors onto the session bus,
// where the chat runtime persists the cursor half. InjectOverseer clears the
// colony half when the answer arrives, so one answer resolves the park for both
// sides and neither asks again.

import (
	"errors"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// overseerStream is the stream id the persistent overseer publishes under
// (StartOverseer creates its worker with StreamID "overseer").
const overseerStream = "overseer"

// askOverseerUser parks the overseer on a user question and announces it on the
// colony bus. The emitted event is the existing client_input_requested family a
// chat queen uses; the session bridge re-stamps it onto the queen stream, so the
// dashboard renders the question in the conversation the user is already
// reading and the chat runtime records the same pending_input.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func (r *ColonyRuntime) askOverseerUser(input map[string]any) agentloop.ToolResult {
	if r == nil {
		return agentloop.ToolResult{Content: "ERROR: colony runtime is unavailable", IsError: true}
	}
	questions, problem := agentloop.NormalizeAskUserQuestions(input)
	if problem != "" {
		return agentloop.ToolResult{Content: problem, IsError: true}
	}
	r.mu.Lock()
	r.overseerQuestions = questions
	r.overseerParkedAt = time.Now().UTC()
	executionID := ""
	if r.overseer != nil && r.overseer.Context != nil {
		executionID = r.overseer.Context.ExecutionID
	}
	bus := r.bus
	r.mu.Unlock()

	if bus != nil {
		parkReason := string(agentloop.ParkReasonAskUser)
		bus.EmitClientInputRequested(overseerStream, overseerStream, &executionID, questions, &parkReason)
	}
	return agentloop.ToolResult{Content: agentloop.AskUserWaitingContent}
}

// OverseerPendingQuestions returns the question the persistent overseer is
// parked on (nil when it is not waiting). The session layer persists the same
// pending_input for the chat front end; this is the colony's runtime half, read
// by the bridge so both halves agree on whether a question is still open.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func (r *ColonyRuntime) OverseerPendingQuestions() []map[string]any {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.overseerQuestions) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(r.overseerQuestions))
	for _, question := range r.overseerQuestions {
		copied := make(map[string]any, len(question))
		for key, value := range question {
			copied[key] = value
		}
		out = append(out, copied)
	}
	return out
}

// loopRaisedParkReason reports the park an error names, when the loop raised
// that park itself instead of failing. Both loop guards that end a turn by
// design (the empty-response ghost stream and the deterministic doom loop)
// return a typed error carrying the reason; upstream's _await_user_input
// publishes the wait off exactly that reason. Ordinary errors (LLM failures,
// persistence errors) carry none and keep the failure path.
//
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (reason)
func loopRaisedParkReason(err error) (agentloop.ParkReason, bool) {
	var carrier interface{ ParkReason() agentloop.ParkReason }
	if !errors.As(err, &carrier) {
		return "", false
	}
	if reason := carrier.ParkReason(); reason != "" {
		return reason, true
	}
	return "", false
}

// overseerTurnParkReason classifies the wait an overseer turn ended on. A
// loop-raised park (the empty-response ghost stream, the deterministic doom
// loop) keeps the reason its typed error names — the same reason the chat
// emitter publishes — so the mirrored turn boundary puts the session snapshot
// in the park's activity instead of leaving the last "executing" state to age
// into a false stall. An ordinary failure falls back to llm_error, the same
// default the chat failure path uses; a question asked during the turn parks
// it on ask_user.
//
// Parity: framework/agent_loop/agent_loop.py::_await_user_input
func (r *ColonyRuntime) overseerTurnParkReason(err error) agentloop.ParkReason {
	if err != nil {
		if reason, parked := loopRaisedParkReason(err); parked {
			return reason
		}
		return agentloop.ParkReasonLLMError
	}
	if len(r.OverseerPendingQuestions()) > 0 {
		return agentloop.ParkReasonAskUser
	}
	return agentloop.ParkReasonTurnDone
}

// clearOverseerQuestions drops the overseer's parked question once a client
// input answers it. The chat runtime clears the session-cursor half of the same
// park on the relay path, so an answered question never re-parks either side.
func (r *ColonyRuntime) clearOverseerQuestions() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.overseerQuestions = nil
	r.overseerParkedAt = time.Time{}
	r.mu.Unlock()
}

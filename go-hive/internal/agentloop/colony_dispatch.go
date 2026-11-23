package agentloop

// Framework-level colony dispatch: the two queen tool calls that open a
// "Create Colony" popup are intercepted before any executor runs, exactly as
// upstream intercepts them in _run_turn_loop.
//
// - suggest_colony (DM/independent phase): a successful call stashes the
//   proposed colony payload, answers the model with a synthetic "popup opened"
//   result, and ends the turn so the host parks on the user (park reason
//   colony_suggestion). The host emits COLONY_SUGGESTION_REQUESTED from the
//   payload returned on TurnResult.
// - task_create(new_colony=true) (colony phase): a successful call requires a
//   goal, a handoff and a non-empty task plan; it stashes the rich pivot
//   payload (goal/handoff/tasks) and parks the same way. The rich payload is
//   what the accept path seeds the new colony from; the event carries the
//   goal/handoff/task_count summary only.
//
// Neither call reaches the registered executor. Going through it would let the
// 60s tool timeout cancel a popup wait that can last minutes (upstream's
// linkedin_4 session found this the hard way), so the intercept sits in the
// loop's call dispatcher, like ask_user.

import (
	"fmt"
	"regexp"
	"strings"
)

// SuggestColonyToolName is the synthetic, framework-dispatched colony proposal
// tool. It is advertised in the independent phase only.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (suggest_colony branch)
const SuggestColonyToolName = "suggest_colony"

// TaskCreateToolName is the task plan tool whose new_colony argument is
// intercepted in the colony phase.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (task_create new_colony branch)
const TaskCreateToolName = "task_create"

// NewColonyPivotField is the colony-phase pivot argument on task_create.
//
// Parity: framework/tasks/tools/session_tools.py::PivotHandler (new_colony)
const NewColonyPivotField = "new_colony"

// colonySlugPattern mirrors upstream's ^[a-z0-9_]+$ argument check.
var colonySlugPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// The error texts below are byte-for-byte the upstream strings: the model
// contract ("逐字") is part of the parity surface, so they are constants and
// never assembled from prose.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (suggest_colony branch)
const (
	ColonySuggestionQueenOnlyError = "ERROR: suggest_colony is queen-only. Workers fan out via run_worker inside an existing colony."

	ColonySuggestionPhaseError = "ERROR: suggest_colony is only available in the independent (DM) phase. " +
		"You are already inside a colony — fan out with run_worker, schedule recurring " +
		"runs with set_trigger, or share protocol with workers via write_skill."

	ColonySuggestionSlugError = "ERROR: colony_id must be lowercase alphanumeric with underscores (e.g. " +
		"'morning_hn_digest'). The popup is pre-filled with this slug, so make it user-friendly."

	ColonyPivotQueenOnlyError = "ERROR: task_create(new_colony=true) is queen-only. Workers don't spawn colonies."

	ColonyPivotPhaseErrorFormat = "ERROR: task_create(new_colony=true) is colony-only (currently '%s'). " +
		"In DM, use new_session for unrelated work, or suggest_colony to spawn the first colony."

	ColonyPivotGoalError = "ERROR: task_create(new_colony=true) requires a `goal` — state the new colony's " +
		"purpose in one sentence, in the user's terms. The new colony anchors on this."

	ColonyPivotHandoffError = "ERROR: task_create(new_colony=true) requires a `handoff`. The new colony inherits " +
		"NOTHING from this conversation; without a handoff its queen starts blind. Write a " +
		"complete, objective brief: user goal in their terms, concrete data (names, URLs, " +
		"IDs, file paths, account to use, exact requirements), decisions made and options " +
		"ruled out, constraints, what 'done' looks like."

	ColonyPivotTasksError = "ERROR: task_create(new_colony=true) requires a non-empty `tasks` array" +
		" — the plan that gets seeded into the new colony."
)

// colonyDispatchOutcome is one intercepted call's verdict. Handled reports that
// the loop consumed the call (the executor must not run); StopsTurn reports a
// successful park (upstream's user_input_requested). Exactly one of Suggestion
// and Pivot is set on a successful park.
type colonyDispatchOutcome struct {
	Result     ToolResult
	Handled    bool
	StopsTurn  bool
	Suggestion map[string]any
	Pivot      map[string]any
}

// interceptColonyDispatch mirrors the two upstream per-call branches. It is a
// pure dispatch decision: no state is mutated here, so the loop owns the park
// bookkeeping and the TurnResult payload.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (suggest_colony branch)
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (task_create new_colony branch)
func (l *AgentLoop) interceptColonyDispatch(agentCtx *AgentContext, call ToolCallRequest) colonyDispatchOutcome {
	switch call.Name {
	case SuggestColonyToolName:
		return l.interceptSuggestColony(agentCtx, call)
	case TaskCreateToolName:
		if value, ok := call.Input[NewColonyPivotField].(bool); ok && value {
			return l.interceptNewColonyPivot(agentCtx, call)
		}
	}
	return colonyDispatchOutcome{}
}

// dispatchColonyCall applies the framework-level colony intercept to one
// dispatched call and folds the verdict into the batch bookkeeping. A consumed
// call gets its synthetic result and start mark; a successful park becomes the
// batch's colony verdict (a pivot outranks a suggestion when a batch somehow
// contains both, matching _await_user_input's branch order). handled reports
// that the executor must not run the call.
func (l *AgentLoop) dispatchColonyCall(agentCtx *AgentContext, call ToolCallRequest, index int, results []ToolResult, started []bool, park colonyDispatchOutcome) (colonyDispatchOutcome, bool) {
	outcome := l.interceptColonyDispatch(agentCtx, call)
	if !outcome.Handled {
		return park, false
	}
	results[index] = outcome.Result
	started[index] = true
	if outcome.StopsTurn && (outcome.Pivot != nil || park.Pivot == nil) {
		return outcome, true
	}
	return park, true
}

// parkTurnOnColonyDispatch folds a successful colony dispatch into the turn
// result and reports that the turn ends here: the popup is open, and the next
// user message (or the accept/dismiss route) drives the following turn, as
// upstream returns on user_input_requested right after the batch.
func parkTurnOnColonyDispatch(result *TurnResult, outcome colonyDispatchOutcome, inner int) bool {
	if !outcome.StopsTurn {
		return false
	}
	result.PendingColonySuggestion = outcome.Suggestion
	result.PendingColonyPivot = outcome.Pivot
	result.InnerTurns = inner + 1
	return true
}

// interceptSuggestColony applies the queen-only, phase and slug gates, then
// stashes the DM suggestion payload.
//
// Parity: framework/agent_loop/agent_loop.py:5076-5175 (suggest_colony branch)
func (l *AgentLoop) interceptSuggestColony(agentCtx *AgentContext, call ToolCallRequest) colonyDispatchOutcome {
	if agentCtx == nil || agentCtx.StreamID != "queen" {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: ColonySuggestionQueenOnlyError, IsError: true},
		}
	}
	// Phase gate (defense in depth): the tool list only advertises
	// suggest_colony in the independent phase, but a mid-session switch must
	// not leave a stale call reachable. A host with no phase provider skips
	// the gate, as upstream does when iteration_metadata_provider is None.
	if phase := agentCtx.currentPhase(); phase != "" && phase != "independent" {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: ColonySuggestionPhaseError, IsError: true},
		}
	}
	colonyID := strings.TrimSpace(anyString(call.Input["colony_id"], ""))
	if !colonySlugPattern.MatchString(colonyID) {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: ColonySuggestionSlugError, IsError: true},
		}
	}
	reason := strings.TrimSpace(anyString(call.Input["reason"], ""))
	payload := map[string]any{"colony_id": colonyID}
	if reason != "" {
		payload["reason"] = reason
	} else {
		payload["reason"] = nil
	}
	return colonyDispatchOutcome{
		Handled:    true,
		StopsTurn:  true,
		Suggestion: payload,
		Result: ToolResult{Content: "Colony creation popup opened with " +
			"colony_id='" + colonyID + "'. Waiting for the user " +
			"to confirm or dismiss. If they confirm, " +
			"this session will lock and you'll be " +
			"compacted into the new colony's queen " +
			"seed. If they dismiss, you'll receive a " +
			"follow-up message and can continue."},
	}
}

// interceptNewColonyPivot applies the queen-only and colony-phase gates,
// validates the goal/handoff/tasks contract, and stashes the rich pivot
// payload the accept path seeds from.
//
// Parity: framework/agent_loop/agent_loop.py:5178-5260 (task_create new_colony branch)
func (l *AgentLoop) interceptNewColonyPivot(agentCtx *AgentContext, call ToolCallRequest) colonyDispatchOutcome {
	if agentCtx == nil || agentCtx.StreamID != "queen" {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: ColonyPivotQueenOnlyError, IsError: true},
		}
	}
	phase := agentCtx.currentPhase()
	if phase != "" && phase != "colony" {
		return colonyDispatchOutcome{
			Handled: true,
			Result: ToolResult{
				Content: fmt.Sprintf(ColonyPivotPhaseErrorFormat, phase),
				IsError: true,
			},
		}
	}
	goal := strings.TrimSpace(anyString(call.Input["goal"], ""))
	handoff := strings.TrimSpace(anyString(call.Input["handoff"], ""))
	if goal == "" {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: ColonyPivotGoalError, IsError: true},
		}
	}
	if handoff == "" {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: ColonyPivotHandoffError, IsError: true},
		}
	}
	tasks := colonyTaskSpecs(call.Input["tasks"])
	if len(tasks) == 0 {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: ColonyPivotTasksError, IsError: true},
		}
	}
	payload := map[string]any{
		"goal":         goal,
		"handoff":      handoff,
		"tasks":        tasks,
		"source_phase": "colony",
	}
	// The sink runs first, as upstream: the orchestrator can veto a pivot the
	// loop cannot know about (a freshly forked colony whose kickoff turn must
	// not re-pivot) and its error text becomes the model-visible result.
	if sinkErr := colonyPivotSinkVeto(agentCtx.PivotPayloadSink, payload); sinkErr != "" {
		return colonyDispatchOutcome{
			Handled: true,
			Result:  ToolResult{Content: "ERROR: " + sinkErr, IsError: true},
		}
	}
	return colonyDispatchOutcome{
		Handled:   true,
		StopsTurn: true,
		Pivot:     payload,
		Result: ToolResult{Content: "Colony pivot popup opened (goal: " +
			truncateRunes(goal, 80) + "). Waiting for the user to " +
			"confirm a slug and create the new colony, " +
			"or dismiss. If they confirm, the new colony " +
			"is spawned with this handoff + task plan " +
			"and they navigate there; you stay here " +
			"idle on this colony's existing plan. If " +
			"they dismiss, you'll receive a follow-up " +
			"message instructing you to call ask_user " +
			"for explicit direction. End your turn now " +
			"and wait for the user."},
	}
}

// currentPhase reads the live phase through the host's provider. An
// unconfigured provider returns "" (the phase gate is skipped), matching
// upstream's nil iteration_metadata_provider.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (phase gate)
func (a *AgentContext) currentPhase() string {
	if a == nil || a.Phase == nil {
		return ""
	}
	return strings.TrimSpace(a.Phase())
}

// colonyTaskSpecs normalizes the model's tasks array to the map shape the task
// store seeds. A non-list or a list without objects is an empty plan, which the
// caller rejects with the upstream tasks error.
func colonyTaskSpecs(raw any) []map[string]any {
	var entries []any
	switch typed := raw.(type) {
	case []any:
		entries = typed
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			out = append(out, entry)
		}
		return out
	default:
		return nil
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if object, ok := entry.(map[string]any); ok {
			out = append(out, object)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// colonyPivotSinkVeto calls the optional pivot sink. It accepts the upstream
// shape (returns an error string to reject) and a Go error-returning variant,
// so both a route-level closure and a test double can veto a pivot.
func colonyPivotSinkVeto(sink any, payload map[string]any) string {
	switch typed := sink.(type) {
	case func(map[string]any) string:
		return strings.TrimSpace(typed(payload))
	case func(map[string]any) error:
		if err := typed(payload); err != nil {
			return strings.TrimSpace(err.Error())
		}
	}
	return ""
}

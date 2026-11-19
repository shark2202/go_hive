package colony

// Worker goal outcome wiring.
//
// Two seams live here:
//
//  1. Prompt projection — the colony goal (criteria + MUST/SHOULD constraints
//     + context) is written into the worker's system prompt, which is the
//     first thing the model sees. The agent-loop side appends
//     AgentContext.GoalContext to the initial message, so the same block also
//     rides the worker's first user turn when the transcript is empty.
//  2. Criteria execution — at worker completion the goal's weighted success
//     criteria run against the final output (agentloop.EvaluateGoal), the
//     resulting verdict rides the worker result/report, and a goal_progress
//     event publishes the weighted score.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
// Parity-exempt: executing SuccessCriterion.metric has no upstream runtime —
// framework/schemas/goal.py::Goal.is_success is schema-only. See
// agentloop.EvaluateGoal.

import (
	"context"
	"strings"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

// workerSystemPrompt builds the worker's system prompt and appends the goal
// block from the worker's AgentContext. An empty goal context returns the base
// prompt unchanged, so colonies without a declared goal behave exactly as
// before.
func (r *ColonyRuntime) workerSystemPrompt(worker *Worker, spec agentloop.AgentSpec) string {
	base := systemPromptFor(spec)
	goalContext := ""
	if worker != nil && worker.Context != nil {
		goalContext = worker.Context.GoalContext
	}
	if strings.TrimSpace(goalContext) == "" && r != nil && r.goal != nil {
		goalContext = r.goal.ToPromptContext()
	}
	goalContext = strings.TrimSpace(goalContext)
	if goalContext == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return goalContext
	}
	return base + "\n\n" + goalContext
}

// goalOutputText is the evidence the criteria execute against: the worker's
// final assistant text plus its explicit report summary (a worker that ends on
// report_to_parent has its result in the report, not in prose).
func (r *ColonyRuntime) goalOutputText(worker *Worker, finalText string) string {
	output := strings.TrimSpace(finalText)
	if worker == nil {
		return output
	}
	if report := worker.ExplicitReport(); report != nil {
		if summary, ok := report["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			if output == "" {
				return strings.TrimSpace(summary)
			}
			return output + "\n\n" + strings.TrimSpace(summary)
		}
	}
	return output
}

// evaluateWorkerGoal executes the colony goal's weighted success criteria for
// one finished worker and publishes the progress event. Nil means the colony
// declares no criteria: nothing to execute, nothing to report.
func (r *ColonyRuntime) evaluateWorkerGoal(ctx context.Context, worker *Worker, loop *agentloop.AgentLoop, turn agentloop.TurnResult) *agentloop.GoalEvaluation {
	if r == nil || r.goal == nil || len(r.goal.SuccessCriteria) == 0 || worker == nil {
		return nil
	}
	output := r.goalOutputText(worker, turn.FinalText)
	var judge agentloop.Judge
	if loop != nil {
		judge = loop.Judge
	}
	request := agentloop.JudgeRequest{
		AssistantText:       output,
		ConversationSummary: output,
	}
	if worker.Context != nil {
		request.AgentSpec = worker.Context.AgentSpec
	}
	evaluation := agentloop.EvaluateGoal(ctx, r.goal, output, judge, request)
	if evaluation == nil {
		return nil
	}
	if r.bus != nil {
		r.bus.EmitGoalProgress(worker.StreamID, evaluation.Score, criterionStatusMap(evaluation))
	}
	return evaluation
}

// judgeEventWriter returns the judge event sink, or nil when the runtime has no
// bus. Returning the untyped nil matters: a nil *eventbus.EventBus stored in a
// non-nil interface would make the loop call methods on a nil receiver.
func (r *ColonyRuntime) judgeEventWriter() agentloop.JudgeEventWriter {
	if r == nil || r.bus == nil {
		return nil
	}
	return r.bus
}

// criterionStatusMap renders the per-criterion verdict for the goal_progress
// event's criteria_status field.
func criterionStatusMap(evaluation *agentloop.GoalEvaluation) map[string]any {
	if evaluation == nil {
		return nil
	}
	out := make(map[string]any, len(evaluation.Criteria))
	for _, criterion := range evaluation.Criteria {
		status := map[string]any{
			"met":       criterion.Met,
			"supported": criterion.Supported,
			"metric":    criterion.Metric,
			"weight":    criterion.Weight,
		}
		if criterion.Detail != "" {
			status["detail"] = criterion.Detail
		}
		out[criterion.ID] = status
	}
	return out
}

// mergeReportData copies the terminal report's existing data map (the explicit
// report map is shared with the worker, so it must not be mutated in place)
// and adds the goal verdict fields the queen and the persisted result.json read.
func mergeReportData(existing any, goalEvaluation, improvementSignals any) map[string]any {
	out := map[string]any{}
	if data, ok := existing.(map[string]any); ok {
		for key, value := range data {
			out[key] = value
		}
	}
	if goalEvaluation != nil {
		out["goal_evaluation"] = goalEvaluation
	}
	if improvementSignals != nil {
		out["improvement_signals"] = improvementSignals
	}
	return out
}

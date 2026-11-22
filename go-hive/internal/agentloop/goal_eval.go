package agentloop

// Goal outcome evaluation.
//
// A structured Goal declares weighted success criteria with a metric
// (output_contains / output_equals / llm_judge / custom). The schema carries
// the weighting rule (schemas.Goal.IsSuccess), but nothing executed the
// per-criterion metrics: the loop judge only read the natural-language
// AgentSpec.SuccessCriteria paragraph. This file is the execution layer: it
// evaluates each criterion against the worker's final output, sets the Met
// flags on a copy of the goal, and calls Goal.IsSuccess() so the weighted
// threshold is the one that decides success.
//
// Metrics with no executable implementation are reported as unsupported
// instead of being silently treated as met: a `custom` criterion is a declared
// hook nobody registered, so the report must say so.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

// Criterion metric names (framework/schemas/goal.py::SuccessCriterion.metric).
const (
	GoalMetricOutputContains = "output_contains"
	GoalMetricOutputEquals   = "output_equals"
	GoalMetricLLMJudge       = "llm_judge"
	GoalMetricCustom         = "custom"
	// GoalMetricSuccessRate is the schema's default Type value; a criterion
	// that names neither a metric nor a recognised runtime type is judged by
	// the LLM when one is configured.
	GoalMetricSuccessRate = "success_rate"
)

// goalOutputExcerptChars bounds the output snapshot carried in the report.
const goalOutputExcerptChars = 240

// GoalCriterionResult is one criterion's executed verdict.
type GoalCriterionResult struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Metric      string  `json:"metric"`
	Weight      float64 `json:"weight"`
	Target      string  `json:"target,omitempty"`
	Met         bool    `json:"met"`
	Supported   bool    `json:"supported"`
	Detail      string  `json:"detail,omitempty"`
}

// GoalEvaluation is the outcome of executing a goal's success criteria.
//
// Success is schemas.Goal.IsSuccess() applied to a goal copy whose Met flags
// this evaluation filled in — the 90% weighted threshold lives in one place.
type GoalEvaluation struct {
	GoalID      string                `json:"goal_id"`
	GoalName    string                `json:"goal_name"`
	Criteria    []GoalCriterionResult `json:"criteria"`
	Score       float64               `json:"score"`
	Success     bool                  `json:"success"`
	Unsupported []string              `json:"unsupported,omitempty"`
	Excerpt     string                `json:"output_excerpt,omitempty"`
	EvaluatedAt string                `json:"evaluated_at"`
}

// Map renders the evaluation as the JSON-shaped payload worker reports and
// HTTP read surfaces carry.
func (e *GoalEvaluation) Map() map[string]any {
	if e == nil {
		return nil
	}
	out := map[string]any{
		"goal_id":        e.GoalID,
		"goal_name":      e.GoalName,
		"score":          e.Score,
		"success":        e.Success,
		"criteria":       criterionMaps(e.Criteria),
		"evaluated_at":   e.EvaluatedAt,
		"output_excerpt": e.Excerpt,
	}
	if len(e.Unsupported) > 0 {
		out["unsupported"] = append([]string(nil), e.Unsupported...)
	}
	return out
}

// UnmetCriterionIDs lists the criteria that did not pass, including the
// unsupported ones. Improvement signals use it to name what fell short.
func (e *GoalEvaluation) UnmetCriterionIDs() []string {
	if e == nil {
		return nil
	}
	ids := make([]string, 0, len(e.Criteria))
	for _, c := range e.Criteria {
		if !c.Met {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func criterionMaps(results []GoalCriterionResult) []any {
	out := make([]any, 0, len(results))
	for _, c := range results {
		row := map[string]any{
			"id":          c.ID,
			"description": c.Description,
			"metric":      c.Metric,
			"weight":      c.Weight,
			"met":         c.Met,
			"supported":   c.Supported,
		}
		if c.Target != "" {
			row["target"] = c.Target
		}
		if c.Detail != "" {
			row["detail"] = c.Detail
		}
		out = append(out, row)
	}
	return out
}

// EvaluateGoal executes a goal's weighted success criteria against output.
//
// metric execution:
//   - output_contains: the output must contain every declared target string.
//   - output_equals:   the trimmed output must equal the declared target.
//   - llm_judge:       the configured judge decides; RETRY/ESCALATE = not met.
//   - success_rate/"": the LLM judge decides when one is configured.
//   - custom:          unsupported — no evaluator is registered, reported as
//     such rather than assumed met.
//
// A nil goal, or a goal with no criteria, returns nil: nothing to execute and
// no report to write. The goal is deep-copied so concurrent workers sharing the
// colony goal never race on the Met flags.
//
// Parity-exempt: framework/schemas/goal.py::Goal.is_success only defines the
// weighting rule; upstream never executes SuccessCriterion.metric. Go-side
// execution at worker completion is the wiring this port adds on purpose.
func EvaluateGoal(ctx context.Context, goal *schemas.Goal, output string, judge Judge, request JudgeRequest) *GoalEvaluation {
	if goal == nil || len(goal.SuccessCriteria) == 0 {
		return nil
	}
	evaluated := *goal
	evaluated.SuccessCriteria = append([]schemas.SuccessCriterion(nil), goal.SuccessCriteria...)

	results := make([]GoalCriterionResult, 0, len(evaluated.SuccessCriteria))
	unsupported := make([]string, 0)
	for i := range evaluated.SuccessCriteria {
		criterion := &evaluated.SuccessCriteria[i]
		result := evaluateGoalCriterion(ctx, criterion, output, judge, request)
		criterion.Met = result.Met
		if !result.Supported {
			unsupported = append(unsupported, result.ID)
		}
		results = append(results, result)
	}

	// IsSuccess is the schema's weighted rule (met weight >= 90% of total).
	evaluation := &GoalEvaluation{
		GoalID:      goal.ID,
		GoalName:    goal.Name,
		Criteria:    results,
		Score:       weightedMetFraction(results),
		Success:     evaluated.IsSuccess(),
		Unsupported: unsupported,
		Excerpt:     truncateRunes(strings.TrimSpace(output), goalOutputExcerptChars),
		EvaluatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return evaluation
}

// weightedMetFraction is the same arithmetic IsSuccess applies, exposed as the
// 0..1 progress number the goal_progress event carries. Weights are summed
// literally (a zero-weight criterion contributes nothing) so the score can
// never disagree with the success flag.
func weightedMetFraction(results []GoalCriterionResult) float64 {
	total := 0.0
	met := 0.0
	for _, c := range results {
		total += c.Weight
		if c.Met {
			met += c.Weight
		}
	}
	if total <= 0 {
		return 0
	}
	return met / total
}

// evaluateGoalCriterion runs one criterion's metric.
func evaluateGoalCriterion(ctx context.Context, criterion *schemas.SuccessCriterion, output string, judge Judge, request JudgeRequest) GoalCriterionResult {
	result := GoalCriterionResult{
		ID:          criterion.ID,
		Description: criterion.Description,
		Metric:      normalizedGoalMetric(criterion.Metric, criterion.Type),
		Weight:      criterion.EffectiveWeight(),
		Supported:   true,
	}
	target, targetOK := goalTargetText(criterion.Target)
	if targetOK {
		result.Target = target
	}
	switch result.Metric {
	case GoalMetricOutputContains:
		if !targetOK || target == "" {
			result.Supported = false
			result.Detail = "output_contains needs a string target"
			return result
		}
		missing := make([]string, 0)
		for _, needle := range goalTargetList(criterion.Target) {
			if !strings.Contains(output, needle) {
				missing = append(missing, needle)
			}
		}
		result.Met = len(missing) == 0
		if !result.Met {
			result.Detail = "output is missing: " + strings.Join(missing, ", ")
		}
		return result
	case GoalMetricOutputEquals:
		if !targetOK {
			result.Supported = false
			result.Detail = "output_equals needs a string target"
			return result
		}
		result.Met = strings.TrimSpace(output) == strings.TrimSpace(target)
		if !result.Met {
			result.Detail = "output does not equal the declared target"
		}
		return result
	case GoalMetricLLMJudge, GoalMetricSuccessRate:
		if judge == nil {
			result.Supported = false
			result.Detail = "no judge is configured for this run"
			return result
		}
		judgeRequest := request
		judgeRequest.Criteria = goalCriterionPrompt(criterion)
		judgeRequest.AssistantText = output
		verdict, err := judge.Evaluate(ctx, judgeRequest)
		if err != nil {
			result.Detail = "judge evaluation failed: " + err.Error()
			return result
		}
		result.Met = verdict.Action == JudgeAccept
		result.Detail = verdict.Feedback
		if result.Detail == "" {
			result.Detail = "judge verdict: " + string(verdict.Action)
		}
		return result
	case GoalMetricCustom:
		result.Supported = false
		result.Detail = "custom metric has no registered evaluator — unsupported, not counted as met"
		return result
	default:
		result.Supported = false
		result.Detail = fmt.Sprintf("unknown metric %q — unsupported, not counted as met", result.Metric)
		return result
	}
}

// normalizedGoalMetric resolves the metric a criterion asks for. Metric is the
// documented field; Type is the schema's runtime-evaluation type and supplies
// the effective value when Metric is blank (the JSON default for both is
// success_rate).
func normalizedGoalMetric(metric, runtimeType string) string {
	for _, candidate := range []string{metric, runtimeType} {
		normalized := strings.ToLower(strings.TrimSpace(candidate))
		if normalized != "" {
			return normalized
		}
	}
	return GoalMetricSuccessRate
}

// goalTargetText renders a scalar target for the report. A list target keeps
// its first element as the label; output_contains consults the full list.
func goalTargetText(target any) (string, bool) {
	switch value := target.(type) {
	case nil:
		return "", false
	case string:
		return value, true
	case []string:
		return strings.Join(value, ", "), true
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return "", false
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, ", "), true
	default:
		return fmt.Sprint(value), true
	}
}

// goalTargetList returns the target strings output_contains must find.
func goalTargetList(target any) []string {
	switch value := target.(type) {
	case string:
		return []string{value}
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// goalCriterionPrompt frames one criterion for the LLM judge: the judge gets
// the criterion itself, not the whole criteria paragraph, so its verdict is
// attributable to that criterion.
func goalCriterionPrompt(criterion *schemas.SuccessCriterion) string {
	parts := []string{strings.TrimSpace(criterion.Description)}
	if target, ok := goalTargetText(criterion.Target); ok && target != "" {
		parts = append(parts, "Target: "+target)
	}
	return strings.Join(parts, "\n")
}

// GoalContextPrompt renders the goal's criteria/constraints/context block for a
// prompt. It is the agentloop-side hook so the loop's prompt builders never
// have to import the goal schema; callers pass schemas.Goal.ToPromptContext().
//
// Parity: framework/agent_loop/agent_loop.py::_build_initial_message (reads
// ctx.goal_context; upstream computes it in the colony runtime).
func GoalContextPrompt(goalContext string) string {
	return strings.TrimSpace(goalContext)
}

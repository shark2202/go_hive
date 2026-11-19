package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/schemas"
)

// goalJudgeStub is a deterministic Judge for goal-criteria tests. It records
// every request so a criterion-level llm_judge call is observable.
type goalJudgeStub struct {
	requests []JudgeRequest
	verdict  JudgeVerdict
	err      error
}

func (j *goalJudgeStub) Evaluate(_ context.Context, request JudgeRequest) (JudgeVerdict, error) {
	j.requests = append(j.requests, request)
	if j.err != nil {
		return JudgeVerdict{}, j.err
	}
	return j.verdict, nil
}

// Parity-exempt: framework/schemas/goal.py::Goal.is_success is schema-only;
// executing SuccessCriterion.metric at worker completion is Go-side wiring.
func TestEvaluateGoalExecutesWeightedMetrics(t *testing.T) {
	judge := &goalJudgeStub{verdict: JudgeVerdict{Action: JudgeAccept, Feedback: "argument holds"}}
	goal := &schemas.Goal{
		ID:   "weighted",
		Name: "Weighted goal",
		SuccessCriteria: []schemas.SuccessCriterion{
			{ID: "contains", Description: "mentions the answer", Metric: GoalMetricOutputContains, Target: "final answer", Weight: 0.3},
			{ID: "equal", Description: "is exactly the answer", Metric: GoalMetricOutputEquals, Target: "final answer", Weight: 0.3},
			{ID: "judged", Description: "argues the conclusion", Metric: GoalMetricLLMJudge, Weight: 0.4},
		},
	}

	evaluation := EvaluateGoal(context.Background(), goal, "final answer", judge, JudgeRequest{AssistantText: "final answer"})
	if evaluation == nil {
		t.Fatal("EvaluateGoal = nil, want an evaluation")
	}
	if !evaluation.Success {
		t.Fatalf("success = false, want true (all three metrics pass): %#v", evaluation.Criteria)
	}
	if evaluation.Score != 1 {
		t.Fatalf("score = %v, want 1", evaluation.Score)
	}
	if len(evaluation.Unsupported) != 0 {
		t.Fatalf("unsupported = %v, want none", evaluation.Unsupported)
	}
	if len(judge.requests) != 1 {
		t.Fatalf("judge calls = %d, want exactly one llm_judge criterion", len(judge.requests))
	}
	// The judge sees the criterion itself, not the whole criteria paragraph.
	if !strings.Contains(judge.requests[0].Criteria, "argues the conclusion") {
		t.Fatalf("judge criteria = %q, want the llm_judge criterion description", judge.requests[0].Criteria)
	}
	// The shared goal must not be mutated: criteria verdicts live on the copy.
	for _, criterion := range goal.SuccessCriteria {
		if criterion.Met {
			t.Fatalf("criterion %q Met = true on the runtime goal, want the evaluation to work on a copy", criterion.ID)
		}
	}
}

// Parity-exempt: framework/schemas/goal.py::Goal.is_success is schema-only;
// this locks the weighted 90% threshold the Go runtime now executes.
func TestEvaluateGoalUsesIsSuccessWeightedThreshold(t *testing.T) {
	goal := &schemas.Goal{
		ID: "threshold",
		SuccessCriteria: []schemas.SuccessCriterion{
			{ID: "heavy", Description: "heavy weight met", Metric: GoalMetricOutputContains, Target: "ok", Weight: 0.5},
			{ID: "missed", Description: "missed", Metric: GoalMetricOutputEquals, Target: "never", Weight: 0.3},
			{ID: "light", Description: "light weight met", Metric: GoalMetricOutputContains, Target: "ok", Weight: 0.2},
		},
	}
	evaluation := EvaluateGoal(context.Background(), goal, "ok", nil, JudgeRequest{})
	if evaluation == nil {
		t.Fatal("EvaluateGoal = nil, want an evaluation")
	}
	// 0.7 of the weight is met, under IsSuccess's 0.9 threshold.
	if evaluation.Success {
		t.Fatalf("success = true, want false at score %v", evaluation.Score)
	}
	if evaluation.Score < 0.69 || evaluation.Score > 0.71 {
		t.Fatalf("score = %v, want 0.7", evaluation.Score)
	}
	if len(evaluation.UnmetCriterionIDs()) != 1 || evaluation.UnmetCriterionIDs()[0] != "missed" {
		t.Fatalf("unmet = %v, want [missed]", evaluation.UnmetCriterionIDs())
	}
}

// Parity-exempt: no upstream evaluator exists for the custom metric; the Go
// runtime must report it as unsupported rather than fake a pass.
func TestEvaluateGoalReportsUnsupportedMetrics(t *testing.T) {
	goal := &schemas.Goal{
		ID: "unsupported",
		SuccessCriteria: []schemas.SuccessCriterion{
			{ID: "hook", Description: "custom check", Metric: GoalMetricCustom, Weight: 0.5},
			{ID: "judge", Description: "judge without a judge", Metric: GoalMetricLLMJudge, Weight: 0.5},
		},
	}
	evaluation := EvaluateGoal(context.Background(), goal, "anything", nil, JudgeRequest{})
	if evaluation == nil {
		t.Fatal("EvaluateGoal = nil, want an evaluation")
	}
	if evaluation.Success {
		t.Fatal("success = true, want false — unsupported criteria cannot count as met")
	}
	if len(evaluation.Unsupported) != 2 {
		t.Fatalf("unsupported = %v, want both criterion ids", evaluation.Unsupported)
	}
	for _, criterion := range evaluation.Criteria {
		if criterion.Met {
			t.Fatalf("criterion %q met = true, want unsupported to stay unmet", criterion.ID)
		}
		if criterion.Supported {
			t.Fatalf("criterion %q supported = true, want false", criterion.ID)
		}
		if criterion.Detail == "" {
			t.Fatalf("criterion %q has no detail; unsupported must be explicit", criterion.ID)
		}
	}
	if !strings.Contains(evaluation.Criteria[0].Detail, "custom") {
		t.Fatalf("custom detail = %q, want it to name the unsupported metric", evaluation.Criteria[0].Detail)
	}
}

// Parity-exempt: metric execution is Go-side; a judge error must not read as a
// silent pass.
func TestEvaluateGoalJudgeErrorKeepsCriterionUnmet(t *testing.T) {
	judge := &goalJudgeStub{err: errors.New("provider timeout")}
	goal := &schemas.Goal{
		ID: "judge-error",
		SuccessCriteria: []schemas.SuccessCriterion{
			{ID: "judged", Description: "judged", Metric: GoalMetricLLMJudge, Weight: 1},
		},
	}
	evaluation := EvaluateGoal(context.Background(), goal, "output", judge, JudgeRequest{})
	if evaluation == nil {
		t.Fatal("EvaluateGoal = nil, want an evaluation")
	}
	if evaluation.Success || evaluation.Criteria[0].Met {
		t.Fatal("judge failure counted as met, want unmet")
	}
	if !strings.Contains(evaluation.Criteria[0].Detail, "provider timeout") {
		t.Fatalf("detail = %q, want the judge error surfaced", evaluation.Criteria[0].Detail)
	}
}

// Parity-exempt: GoalContextPrompt is the Go hook that projects the schema's
// ToPromptContext into the loop's initial-message builder.
func TestBuildInitialMessageIncludesGoalContext(t *testing.T) {
	ctx := &AgentContext{
		InputData:   map[string]any{"task": "write the report"},
		GoalContext: "# Goal: Report\n- [MUST] cite every claim\n- [SHOULD] stay under 500 words",
	}
	message := BuildInitialMessage(ctx)
	if !strings.Contains(message, "[MUST] cite every claim") {
		t.Fatalf("initial message = %q, want the MUST constraint", message)
	}
	if !strings.Contains(message, "[SHOULD] stay under 500 words") {
		t.Fatalf("initial message = %q, want the SHOULD constraint", message)
	}
}

// Parity: framework/agent_loop/prompting.py::build_system_prompt_static (the
// goal block joins the agent's system prompt, so criteria and MUST/SHOULD
// constraints are model-visible on every turn).
func TestBuildSystemPromptCarriesGoalContext(t *testing.T) {
	ctx := &AgentContext{
		AgentSpec:   NewAgentSpec("worker", "Worker", "test worker"),
		GoalContext: "# Goal: Report\n- [MUST] cite every claim",
	}
	prompt := BuildSystemPrompt(BuildPromptSpec(ctx, PromptOverrides{}))
	if !strings.Contains(prompt, "# Goal: Report") {
		t.Fatalf("system prompt = %q, want the goal block", prompt)
	}
	if !strings.Contains(prompt, "[MUST] cite every claim") {
		t.Fatalf("system prompt = %q, want the MUST constraint", prompt)
	}
}

package schemas

import (
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

// Parity: tests/test_run.py::TestRuntimeMetrics::test_success_rate
func TestRunMetricsSuccessRate(t *testing.T) {
	metrics := RunMetrics{TotalDecisions: 10, SuccessfulDecisions: 8, FailedDecisions: 2}
	if got := metrics.SuccessRate(); got != 0.8 {
		t.Fatalf("success_rate = %v, want 0.8", got)
	}
}

// Parity: tests/test_run.py::TestRuntimeMetrics::test_success_rate_zero_decisions
func TestRunMetricsSuccessRateZeroDecisions(t *testing.T) {
	metrics := RunMetrics{TotalDecisions: 0, SuccessfulDecisions: 0, FailedDecisions: 0}
	if got := metrics.SuccessRate(); got != 0.0 {
		t.Fatalf("success_rate = %v, want 0.0", got)
	}
}

// Parity: tests/test_run.py::TestRun::test_duration_ms
func TestRunDurationMS(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
	}
	want := int(completed.Time.Sub(started.Time) / time.Millisecond)
	if got := run.DurationMS(); got != want {
		t.Fatalf("duration_ms = %d, want %d", got, want)
	}
}

// Parity: tests/test_run.py::TestRun::test_add_decision
func TestRunAddDecision(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
	}
	decision := Decision{
		ID:        "test_decision",
		Timestamp: NowTimestamp(),
		NodeID:    "test_node",
		Intent:    "Choose a greeting",
		Options: []Option{
			{ID: "hello", Description: "Say hello", ActionType: "generate"},
			{ID: "hi", Description: "Say hi", ActionType: "generate"},
		},
	}
	run.AddDecision(decision)
	if run.Metrics.TotalDecisions != 1 {
		t.Fatalf("total_decisions = %d, want 1", run.Metrics.TotalDecisions)
	}
	if len(run.Metrics.NodesExecuted) != 1 || run.Metrics.NodesExecuted[0] != "test_node" {
		t.Fatalf("nodes_executed = %v, want [test_node]", run.Metrics.NodesExecuted)
	}
}

// Parity: tests/test_run.py::TestRun::test_record_outcome
func TestRunRecordOutcome(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
		Metrics:     RunMetrics{TotalDecisions: 0, SuccessfulDecisions: 0, FailedDecisions: 0},
	}
	decision := Decision{
		ID:        "test_decision",
		Timestamp: NowTimestamp(),
		NodeID:    "test_node",
		Intent:    "Choose a greeting",
		Options: []Option{
			{ID: "hello", Description: "Say hello", ActionType: "generate"},
			{ID: "hi", Description: "Say hi", ActionType: "generate"},
		},
	}
	outcome := Outcome{Success: true, TokensUsed: 10, LatencyMS: 100}

	run.AddDecision(decision)
	run.RecordOutcome(decision.ID, outcome)

	if run.Decisions[0].Outcome == nil {
		t.Fatal("outcome not attached")
	}
	got := *run.Decisions[0].Outcome
	if got.Success != outcome.Success || got.TokensUsed != outcome.TokensUsed || got.LatencyMS != outcome.LatencyMS {
		t.Fatalf("outcome = %+v, want %+v", got, outcome)
	}
	if run.Metrics.SuccessfulDecisions != 1 {
		t.Fatalf("successful_decisions = %d, want 1", run.Metrics.SuccessfulDecisions)
	}
	if run.Metrics.FailedDecisions != 0 {
		t.Fatalf("failed_decisions = %d, want 0", run.Metrics.FailedDecisions)
	}
	if run.Metrics.TotalTokens != 10 {
		t.Fatalf("total_tokens = %d, want 10", run.Metrics.TotalTokens)
	}
	if run.Metrics.TotalLatencyMS != 100 {
		t.Fatalf("total_latency_ms = %d, want 100", run.Metrics.TotalLatencyMS)
	}
}

// Parity: tests/test_run.py::TestRun::test_add_problem
func TestRunAddProblem(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
	}
	problemID := run.AddProblem(
		"Test problem",
		"Test problem description",
		strPtr("test_decision"),
		strPtr("Test root cause"),
		strPtr("Test suggested fix"),
	)

	if want := "prob_0"; problemID != want {
		t.Fatalf("problem_id = %q, want %q", problemID, want)
	}
	problem := run.Problems[0]
	if problem.ID != "prob_0" {
		t.Fatalf("problem.id = %q, want prob_0", problem.ID)
	}
	if problem.Severity != "Test problem" {
		t.Fatalf("severity = %q", problem.Severity)
	}
	if problem.Description != "Test problem description" {
		t.Fatalf("description = %q", problem.Description)
	}
	if problem.DecisionID == nil || *problem.DecisionID != "test_decision" {
		t.Fatalf("decision_id = %v", problem.DecisionID)
	}
	if problem.RootCause == nil || *problem.RootCause != "Test root cause" {
		t.Fatalf("root_cause = %v", problem.RootCause)
	}
	if problem.SuggestedFix == nil || *problem.SuggestedFix != "Test suggested fix" {
		t.Fatalf("suggested_fix = %v", problem.SuggestedFix)
	}
}

// Parity: tests/test_run.py::TestRun::test_complete
func TestRunComplete(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
	}
	run.Complete(RunCompleted, "Test narrative")
	if run.Status != RunCompleted {
		t.Fatalf("status = %q, want %q", run.Status, RunCompleted)
	}
	if run.Narrative != "Test narrative" {
		t.Fatalf("narrative = %q", run.Narrative)
	}
}

// Parity: tests/test_run.py::TestRunSummary::test_from_run_basic
func TestRunSummaryFromRunBasic(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
	}
	run.Complete(RunCompleted, "Test narrative")

	summary := RunSummaryFromRun(run)

	if summary.RunID != "test_run" {
		t.Fatalf("run_id = %q", summary.RunID)
	}
	if summary.GoalID != "test_goal" {
		t.Fatalf("goal_id = %q", summary.GoalID)
	}
	if summary.Status != RunCompleted {
		t.Fatalf("status = %q", summary.Status)
	}
	if summary.DecisionCount != 0 {
		t.Fatalf("decision_count = %d, want 0", summary.DecisionCount)
	}
	if summary.SuccessRate != 0.0 {
		t.Fatalf("success_rate = %v, want 0.0", summary.SuccessRate)
	}
	if summary.ProblemCount != 0 {
		t.Fatalf("problem_count = %d, want 0", summary.ProblemCount)
	}
	if summary.Narrative != "Test narrative" {
		t.Fatalf("narrative = %q", summary.Narrative)
	}
}

// Parity: tests/test_run.py::TestRunSummary::test_from_run_with_decisions
func TestRunSummaryFromRunWithDecisions(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
	}

	successfulDecision := Decision{
		ID:        "decision_1",
		Timestamp: NowTimestamp(),
		NodeID:    "node_1",
		Intent:    "Choose greeting",
		Options: []Option{
			{ID: "opt_1", Description: "Say hello", ActionType: "generate"},
		},
		ChosenOptionID: "opt_1",
	}
	successfulOutcome := Outcome{
		Success:    true,
		TokensUsed: 10,
		LatencyMS:  100,
		Summary:    "Successfully greeted user",
	}

	failedDecision := Decision{
		ID:        "decision_2",
		Timestamp: NowTimestamp(),
		NodeID:    "node_2",
		Intent:    "Process data",
		Options: []Option{
			{ID: "opt_2", Description: "Parse JSON", ActionType: "tool_call"},
		},
		ChosenOptionID: "opt_2",
	}
	failedOutcome := Outcome{
		Success:    false,
		Error:      strPtr("Invalid JSON format"),
		TokensUsed: 5,
		LatencyMS:  50,
	}

	run.AddDecision(successfulDecision)
	run.RecordOutcome("decision_1", successfulOutcome)
	run.AddDecision(failedDecision)
	run.RecordOutcome("decision_2", failedOutcome)
	run.Complete(RunCompleted, "Test narrative")

	summary := RunSummaryFromRun(run)

	if summary.DecisionCount != 2 {
		t.Fatalf("decision_count = %d, want 2", summary.DecisionCount)
	}
	if summary.SuccessRate != 0.5 {
		t.Fatalf("success_rate = %v, want 0.5", summary.SuccessRate)
	}
	if len(summary.KeyDecisions) != 1 {
		t.Fatalf("key_decisions len = %d, want 1", len(summary.KeyDecisions))
	}
	if len(summary.Successes) != 1 {
		t.Fatalf("successes len = %d, want 1", len(summary.Successes))
	}
	if summary.Successes[0] != "Successfully greeted user" {
		t.Fatalf("successes[0] = %q", summary.Successes[0])
	}
}

// Parity: tests/test_run.py::TestRunSummary::test_from_run_with_problems
func TestRunSummaryFromRunWithProblems(t *testing.T) {
	started := NowTimestamp()
	completed := NowTimestamp()
	run := &Run{
		ID:          "test_run",
		GoalID:      "test_goal",
		StartedAt:   started,
		CompletedAt: &completed,
	}

	run.AddProblem(
		"critical",
		"API timeout",
		strPtr("decision_1"),
		strPtr("Network issue"),
		strPtr("Add retry logic"),
	)
	run.AddProblem(
		"warning",
		"High latency",
		strPtr("decision_2"),
		strPtr("Large payload"),
		strPtr("Optimize data size"),
	)

	run.Complete(RunCompleted, "Test narrative")

	summary := RunSummaryFromRun(run)

	if summary.ProblemCount != 2 {
		t.Fatalf("problem_count = %d, want 2", summary.ProblemCount)
	}
	if len(summary.CriticalProblems) != 1 {
		t.Fatalf("critical_problems len = %d, want 1", len(summary.CriticalProblems))
	}
	if len(summary.Warnings) != 1 {
		t.Fatalf("warnings len = %d, want 1", len(summary.Warnings))
	}
	if summary.CriticalProblems[0] != "API timeout" {
		t.Fatalf("critical_problems[0] = %q", summary.CriticalProblems[0])
	}
	if summary.Warnings[0] != "High latency" {
		t.Fatalf("warnings[0] = %q", summary.Warnings[0])
	}
}

package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func newTestCostGuard(t *testing.T, budget any) *CostGuardStage {
	t.Helper()
	spec := map[string]any{"type": "cost_guard"}
	if budget != nil {
		spec["config"] = map[string]any{"max_cost_per_request": budget}
	}
	stage, err := BuildStage(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return stage.(*CostGuardStage)
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; no
// estimated_cost metadata passes (the unconfigured default never blocks).
func TestCostGuardWithoutEstimatePasses(t *testing.T) {
	stage := newTestCostGuard(t, nil)
	result, err := stage.Process(NewContext("api", map[string]any{}))
	if err != nil || result.Action != ActionContinue {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a null
// estimate passes (upstream's `if estimated is None`).
func TestCostGuardNullEstimatePasses(t *testing.T) {
	stage := newTestCostGuard(t, 1.0)
	ctx := NewContext("api", map[string]any{})
	ctx.Metadata["estimated_cost"] = nil
	result, err := stage.Process(ctx)
	if err != nil || result.Action != ActionContinue {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; an
// estimate over the budget rejects with the upstream message format.
func TestCostGuardOverBudgetRejects(t *testing.T) {
	stage := newTestCostGuard(t, 0.5)
	ctx := NewContext("api", map[string]any{})
	ctx.Metadata["estimated_cost"] = 0.75
	result, err := stage.Process(ctx)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.Action != ActionReject {
		t.Fatalf("action = %s, want reject", result.Action)
	}
	want := "Estimated cost $0.7500 exceeds budget $0.5000"
	if result.RejectionReason != want {
		t.Fatalf("reason = %q, want %q", result.RejectionReason, want)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; an
// estimate at or under budget passes, including JSON integer shapes.
func TestCostGuardUnderBudgetPasses(t *testing.T) {
	stage := newTestCostGuard(t, 1.0)
	for _, estimate := range []any{0.5, json.Number("0"), json.Number("1")} {
		ctx := NewContext("api", map[string]any{})
		ctx.Metadata["estimated_cost"] = estimate
		result, err := stage.Process(ctx)
		if err != nil || result.Action != ActionContinue {
			t.Fatalf("estimate %v: result=%+v err=%v", estimate, result, err)
		}
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a
// non-numeric estimate is a stage error (Python raises TypeError on the
// comparison).
func TestCostGuardNonNumericEstimateIsError(t *testing.T) {
	stage := newTestCostGuard(t, 1.0)
	ctx := NewContext("api", map[string]any{})
	ctx.Metadata["estimated_cost"] = "cheap"
	if _, err := stage.Process(ctx); err == nil || !strings.Contains(err.Error(), "estimated_cost") {
		t.Fatalf("err = %v, want non-numeric estimate error", err)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// config key max_cost_per_request defaults to 1.0 and rejects non-numbers.
func TestCostGuardConfigParsing(t *testing.T) {
	stage := newTestCostGuard(t, nil)
	if stage.MaxCostPerRequest != 1.0 {
		t.Fatalf("default budget = %v, want 1.0", stage.MaxCostPerRequest)
	}
	if _, err := BuildStage(map[string]any{
		"type":   "cost_guard",
		"config": map[string]any{"max_cost_per_request": "cheap"},
	}); err == nil {
		t.Fatal("string budget must fail")
	}
}

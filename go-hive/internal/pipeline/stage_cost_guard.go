package pipeline

import "fmt"

// CostGuardStage rejects requests whose estimated cost exceeds the configured
// per-request budget.
//
// The estimate must be populated in ctx.Metadata["estimated_cost"] by an
// earlier stage or the caller. Without an estimate the stage passes through —
// an unconfigured pipeline therefore never blocks on cost.
//
// Parity: framework/pipeline/stages/cost_guard.py::CostGuardStage
type CostGuardStage struct {
	BaseStage
	// MaxCostPerRequest is the per-request budget (config
	// max_cost_per_request, default 1.0).
	MaxCostPerRequest float64
}

// NewCostGuardStage builds the stage from its declarative config.
func NewCostGuardStage(spec map[string]any) (Stage, error) {
	config := configMap(spec)
	stage := &CostGuardStage{
		BaseStage:         BaseStage{StageName: "cost_guard", StageOrder: 300},
		MaxCostPerRequest: 1.0,
	}
	if raw, present := config["max_cost_per_request"]; present {
		budget, ok := pyNumeric(raw)
		if !ok {
			return nil, fmt.Errorf("cost_guard max_cost_per_request must be a number, got %v", raw)
		}
		stage.MaxCostPerRequest = budget
	}
	return stage, nil
}

// Process compares the estimate against the budget. A non-numeric estimate is
// a stage error (upstream's `estimated > budget` raises TypeError there).
//
// Parity: framework/pipeline/stages/cost_guard.py::CostGuardStage.process
func (s *CostGuardStage) Process(ctx *Context) (Result, error) {
	estimated, present := ctx.Metadata["estimated_cost"]
	if !present || estimated == nil {
		return ContinueResult(), nil
	}
	cost, ok := pyNumeric(estimated)
	if !ok {
		return Result{}, fmt.Errorf("estimated_cost has type %s, not a number", pythonTypeName(estimated))
	}
	if cost > s.MaxCostPerRequest {
		return RejectResult(fmt.Sprintf(
			"Estimated cost $%.4f exceeds budget $%.4f",
			cost, s.MaxCostPerRequest,
		)), nil
	}
	return ContinueResult(), nil
}

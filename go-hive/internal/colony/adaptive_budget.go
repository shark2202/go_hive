package colony

// Parity: framework/host/colony_runtime.py::ColonyRuntime._maybe_adapt_colony_budget
// Parity: framework/host/colony_runtime.py::ColonyRuntime._apply_adaptive_budget

import (
	"math"
	"sort"
)

// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_stats
func (r *ColonyRuntime) AdaptiveToolBudgetStats() map[string]any {
	r.budgetMu.Lock()
	defer r.budgetMu.Unlock()
	var nominal any
	if r.budgetAppliedSet {
		nominal = r.budgetApplied
	}
	return map[string]any{
		"enabled": r.adaptiveEnabled,
		"nominal": nominal,
		"samples": len(r.budgetSamples),
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._maybe_adapt_colony_budget
func (r *ColonyRuntime) maybeAdaptColonyBudget(data map[string]any) {
	if !r.adaptiveEnabled {
		return
	}
	if status, _ := data["status"].(string); status != "success" {
		return
	}
	if payloadTruthy(data["budget_limited"]) || payloadTruthy(data["budget_pinned"]) {
		return
	}
	used, _, ok := pyInt(data["tool_calls_used"])
	if !ok || used <= 0 {
		return
	}

	r.budgetMu.Lock()
	r.budgetSamples = append(r.budgetSamples, used)
	if len(r.budgetSamples) > adaptiveBudgetSampleWindow {
		r.budgetSamples = append([]int(nil), r.budgetSamples[len(r.budgetSamples)-adaptiveBudgetSampleWindow:]...)
	}
	if used > r.budgetMaxSuccess {
		r.budgetMaxSuccess = used
	}
	if len(r.budgetSamples) < AdaptiveBudgetMinSamples {
		r.budgetMu.Unlock()
		return
	}

	samples := append([]int(nil), r.budgetSamples...)
	sort.Ints(samples)
	median := medianInts(samples)
	p90 := samples[minInt(len(samples)-1, int(math.Ceil(0.9*float64(len(samples))))-1)]
	if float64(p90) > adaptiveBudgetDispersionLimit*median {
		r.budgetMu.Unlock()
		return
	}

	base := DefaultWorkerLoopConfig["tool_call_lifetime_budget"]
	if base <= 0 {
		r.budgetMu.Unlock()
		return
	}

	maxSuccess := r.budgetMaxSuccess
	nominal := int(math.Max(
		math.Ceil(adaptiveBudgetMultiplier*median),
		math.Ceil(adaptiveBudgetMaxGuard*float64(maxSuccess)),
	))
	nominal = int(math.Max(float64(AdaptiveBudgetFloor), math.Min(float64(nominal), float64(base))))

	current := base
	if r.budgetAppliedSet {
		current = r.budgetApplied
	}
	if nominal < current {
		r.budgetApplied = nominal
		r.budgetAppliedSet = true
		r.budgetMu.Unlock()
		r.clampInFlightWorkers(nominal)
		return
	}

	evidenceFloor := int(math.Min(
		float64(base),
		math.Max(float64(AdaptiveBudgetFloor), math.Ceil(adaptiveBudgetMaxGuard*float64(maxSuccess))),
	))
	if evidenceFloor <= current {
		r.budgetMu.Unlock()
		return
	}
	r.budgetApplied = evidenceFloor
	r.budgetAppliedSet = true
	r.budgetMu.Unlock()
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._apply_adaptive_budget
func (r *ColonyRuntime) ApplyAdaptiveBudget(worker *Worker) {
	if worker == nil {
		return
	}
	r.budgetMu.Lock()
	nominal := r.budgetApplied
	applied := r.budgetAppliedSet
	r.budgetMu.Unlock()
	if !applied || worker.BudgetPinned || worker.IsPersistent() {
		return
	}
	if !r.adaptiveEnabled {
		return
	}
	if loop := worker.AgentLoop(); loop != nil {
		loop.ApplyLifetimeBudgetCap(nominal)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._maybe_adapt_colony_budget
func (r *ColonyRuntime) clampInFlightWorkers(nominal int) {
	r.mu.Lock()
	targets := make([]*Worker, 0, len(r.workers))
	for _, worker := range r.workers {
		if worker.BudgetPinned || worker.IsPersistent() {
			continue
		}
		status := worker.Status()
		if status != WorkerPending && status != WorkerRunning {
			continue
		}
		targets = append(targets, worker)
	}
	r.mu.Unlock()
	for _, worker := range targets {
		if loop := worker.AgentLoop(); loop != nil {
			loop.ApplyLifetimeBudgetCap(nominal)
		}
	}
}

func payloadTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case int:
		return v != 0
	case int8:
		return v != 0
	case int16:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case uint:
		return v != 0
	case uint8:
		return v != 0
	case uint16:
		return v != 0
	case uint32:
		return v != 0
	case uint64:
		return v != 0
	case float32:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	default:
		return true
	}
}

func medianInts(samples []int) float64 {
	n := len(samples)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return float64(samples[n/2])
	}
	return float64(samples[n/2-1]+samples[n/2]) / 2.0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

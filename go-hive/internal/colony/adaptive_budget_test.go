package colony

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

func withFixedBudgetConstants(t *testing.T) {
	t.Helper()
	oldFloor, oldMin := AdaptiveBudgetFloor, AdaptiveBudgetMinSamples
	AdaptiveBudgetFloor, AdaptiveBudgetMinSamples = 30, 3
	t.Cleanup(func() {
		AdaptiveBudgetFloor, AdaptiveBudgetMinSamples = oldFloor, oldMin
	})
}

func newBudgetTestRuntime(enabled bool) *ColonyRuntime {
	return &ColonyRuntime{
		workers:          map[string]*Worker{},
		adaptiveEnabled:  enabled,
		budgetMaxSuccess: 0,
	}
}

func newBudgetStubWorker(id string, status WorkerStatus, budget int, pinned, persistent bool) *Worker {
	worker := NewWorker(id, "", id)
	worker.SetStatus(status)
	worker.BudgetPinned = pinned
	worker.Persistent = persistent
	loop := agentloop.NewAgentLoop(agentloop.LoopConfig{
		ToolCallLifetimeBudget: budget,
		GraceIterations:        1,
	}, nil)
	worker.SetAgentLoop(loop)
	return worker
}

func effectiveBudget(worker *Worker) int {
	if loop := worker.AgentLoop(); loop != nil {
		return loop.LifetimeBudget()
	}
	return 0
}

func successPayload(used int, extra map[string]any) map[string]any {
	data := map[string]any{"status": "success", "tool_calls_used": used}
	for key, value := range extra {
		data[key] = value
	}
	return data
}

// Parity: tests/test_adaptive_tool_budget.py::test_no_shrink_below_min_samples
func TestNoShrinkBelowMinSamples(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	for i := 0; i < AdaptiveBudgetMinSamples-1; i++ {
		colony.maybeAdaptColonyBudget(successPayload(10, nil))
	}
	if colony.budgetAppliedSet {
		t.Fatalf("budget applied = %d below the sample minimum, want unset", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_shrink_hits_floor_and_clamps_running_workers
func TestShrinkHitsFloorAndClampsRunningWorkers(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	running := newBudgetStubWorker("w_run", WorkerRunning, 150, false, false)
	queued := newBudgetStubWorker("w_queued", WorkerQueued, 150, false, false)
	pinned := newBudgetStubWorker("w_pin", WorkerRunning, 150, true, false)
	persistent := newBudgetStubWorker("w_pers", WorkerRunning, 150, false, true)
	done := newBudgetStubWorker("w_done", WorkerCompleted, 150, false, false)
	for _, worker := range []*Worker{running, queued, pinned, persistent, done} {
		colony.workers[worker.ID] = worker
	}

	for i := 0; i < 3; i++ {
		colony.maybeAdaptColonyBudget(successPayload(10, nil))
	}

	if !colony.budgetAppliedSet || colony.budgetApplied != AdaptiveBudgetFloor {
		t.Fatalf("budget applied = (%d, set=%v), want %d", colony.budgetApplied, colony.budgetAppliedSet, AdaptiveBudgetFloor)
	}
	if got := effectiveBudget(running); got != AdaptiveBudgetFloor {
		t.Errorf("running effective budget = %d, want %d", got, AdaptiveBudgetFloor)
	}
	for name, worker := range map[string]*Worker{
		"queued": queued, "pinned": pinned, "persistent": persistent, "completed": done,
	} {
		if got := effectiveBudget(worker); got != 150 {
			t.Errorf("%s effective budget = %d, want 150", name, got)
		}
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_formula_max_guard_keeps_headroom_over_observed_max
func TestFormulaMaxGuardKeepsHeadroomOverObservedMax(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	for _, used := range []int{40, 40, 100} {
		colony.maybeAdaptColonyBudget(successPayload(used, nil))
	}
	if colony.budgetApplied != 125 {
		t.Fatalf("budget applied = %d, want 125", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_median_driven_raise_is_refused
func TestMedianDrivenRaiseIsRefused(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	for i := 0; i < 3; i++ {
		colony.maybeAdaptColonyBudget(successPayload(30, nil))
	}
	if colony.budgetApplied != 60 {
		t.Fatalf("initial budget applied = %d, want 60", colony.budgetApplied)
	}
	for i := 0; i < 5; i++ {
		colony.maybeAdaptColonyBudget(successPayload(48, nil))
	}
	if colony.budgetApplied != 60 {
		t.Fatalf("budget applied = %d after rising median, want 60", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_evidence_driven_raise_recovers_from_a_stale_low_norm
func TestEvidenceDrivenRaiseRecoversFromStaleLowNorm(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	for i := 0; i < 3; i++ {
		colony.maybeAdaptColonyBudget(successPayload(30, nil))
	}
	if colony.budgetApplied != 60 {
		t.Fatalf("initial budget applied = %d, want 60", colony.budgetApplied)
	}

	colony.maybeAdaptColonyBudget(successPayload(59, nil))
	if colony.budgetApplied != 74 {
		t.Fatalf("budget applied = %d after 59, want 74", colony.budgetApplied)
	}
	colony.maybeAdaptColonyBudget(successPayload(73, nil))
	if colony.budgetApplied != 92 {
		t.Fatalf("budget applied = %d after 73, want 92", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_raise_is_capped_at_the_profile_base
func TestRaiseIsCappedAtProfileBase(t *testing.T) {
	withFixedBudgetConstants(t)
	base := DefaultWorkerLoopConfig["tool_call_lifetime_budget"]
	colony := newBudgetTestRuntime(true)
	colony.budgetApplied = 60
	colony.budgetAppliedSet = true
	for i := 0; i < 3; i++ {
		colony.maybeAdaptColonyBudget(successPayload(180, nil))
	}
	if colony.budgetApplied != base {
		t.Fatalf("budget applied = %d, want base %d", colony.budgetApplied, base)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_raise_does_not_lift_already_clamped_inflight_workers
func TestRaiseDoesNotLiftAlreadyClampedInflightWorkers(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	running := newBudgetStubWorker("w_run", WorkerRunning, 150, false, false)
	colony.workers[running.ID] = running
	for i := 0; i < 3; i++ {
		colony.maybeAdaptColonyBudget(successPayload(10, nil))
	}
	if colony.budgetApplied != AdaptiveBudgetFloor {
		t.Fatalf("budget applied = %d, want %d", colony.budgetApplied, AdaptiveBudgetFloor)
	}
	if got := effectiveBudget(running); got != AdaptiveBudgetFloor {
		t.Fatalf("running effective budget = %d, want %d", got, AdaptiveBudgetFloor)
	}

	colony.maybeAdaptColonyBudget(successPayload(29, nil))
	if colony.budgetApplied != 37 {
		t.Fatalf("budget applied = %d after 29, want 37", colony.budgetApplied)
	}
	if got := effectiveBudget(running); got != AdaptiveBudgetFloor {
		t.Errorf("running effective budget = %d after raise, want %d", got, AdaptiveBudgetFloor)
	}
	fresh := newBudgetStubWorker("w_new", WorkerPending, 150, false, false)
	colony.ApplyAdaptiveBudget(fresh)
	if got := effectiveBudget(fresh); got != 37 {
		t.Errorf("fresh effective budget = %d, want 37", got)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_max_guard_survives_window_eviction
func TestMaxGuardSurvivesWindowEviction(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	for _, used := range []int{40, 40, 100} {
		colony.maybeAdaptColonyBudget(successPayload(used, nil))
	}
	if colony.budgetApplied != 125 {
		t.Fatalf("budget applied = %d, want 125", colony.budgetApplied)
	}
	for i := 0; i < 60; i++ {
		colony.maybeAdaptColonyBudget(successPayload(10, nil))
	}
	for _, sample := range colony.budgetSamples {
		if sample == 100 {
			t.Fatalf("sample window still holds 100: %v", colony.budgetSamples)
		}
	}
	if colony.budgetApplied != 125 {
		t.Fatalf("budget applied = %d after window eviction, want 125", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_dispersion_guard_skips_heterogeneous_samples
func TestDispersionGuardSkipsHeterogeneousSamples(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	for _, used := range []int{10, 10, 100} {
		colony.maybeAdaptColonyBudget(successPayload(used, nil))
	}
	if colony.budgetAppliedSet {
		t.Fatalf("budget applied = %d, want unset", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_nominal_capped_at_base_no_shrink_when_typical_is_expensive
func TestNominalCappedAtBaseWhenTypicalIsExpensive(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	for i := 0; i < 3; i++ {
		colony.maybeAdaptColonyBudget(successPayload(120, nil))
	}
	if colony.budgetAppliedSet {
		t.Fatalf("budget applied = %d, want unset", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_sampler_exclusions
func TestSamplerExclusions(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	colony.maybeAdaptColonyBudget(map[string]any{"status": "failed", "tool_calls_used": 10})
	colony.maybeAdaptColonyBudget(map[string]any{"status": "partial", "tool_calls_used": 10})
	colony.maybeAdaptColonyBudget(successPayload(10, map[string]any{"budget_limited": true}))
	colony.maybeAdaptColonyBudget(successPayload(10, map[string]any{"budget_pinned": true}))
	colony.maybeAdaptColonyBudget(successPayload(0, nil))
	colony.maybeAdaptColonyBudget(map[string]any{"status": "success", "tool_calls_used": "10"})
	if len(colony.budgetSamples) != 0 {
		t.Fatalf("budget samples = %v, want none", colony.budgetSamples)
	}
	if colony.budgetAppliedSet {
		t.Fatalf("budget applied = %d, want unset", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_config_kill_switch
func TestConfigKillSwitch(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(false)
	for i := 0; i < 5; i++ {
		colony.maybeAdaptColonyBudget(successPayload(10, nil))
	}
	if len(colony.budgetSamples) != 0 {
		t.Fatalf("budget samples = %v, want none", colony.budgetSamples)
	}
	if colony.budgetAppliedSet {
		t.Fatalf("budget applied = %d, want unset", colony.budgetApplied)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_admission_clamp_applies_current_nominal
func TestAdmissionClampAppliesCurrentNominal(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	worker := newBudgetStubWorker("w1", WorkerPending, 150, false, false)
	colony.ApplyAdaptiveBudget(worker)
	if got := effectiveBudget(worker); got != 150 {
		t.Fatalf("effective budget = %d with no nominal, want 150", got)
	}
	colony.budgetApplied = 40
	colony.budgetAppliedSet = true
	colony.ApplyAdaptiveBudget(worker)
	if got := effectiveBudget(worker); got != 40 {
		t.Fatalf("effective budget = %d, want 40", got)
	}
	pinned := newBudgetStubWorker("w2", WorkerPending, 150, true, false)
	colony.ApplyAdaptiveBudget(pinned)
	if got := effectiveBudget(pinned); got != 150 {
		t.Fatalf("pinned effective budget = %d, want 150", got)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_get_stats_exposes_nominal
func TestGetStatsExposesNominal(t *testing.T) {
	withFixedBudgetConstants(t)
	colony := newBudgetTestRuntime(true)
	stats := colony.GetStats()["adaptive_tool_budget"].(map[string]any)
	if stats["enabled"] != true || stats["nominal"] != nil || stats["samples"] != 0 {
		t.Fatalf("initial adaptive stats = %#v", stats)
	}
	for i := 0; i < 3; i++ {
		colony.maybeAdaptColonyBudget(successPayload(10, nil))
	}
	stats = colony.GetStats()["adaptive_tool_budget"].(map[string]any)
	if stats["nominal"] != AdaptiveBudgetFloor {
		t.Errorf("nominal = %v, want %d", stats["nominal"], AdaptiveBudgetFloor)
	}
	if stats["samples"] != 3 {
		t.Errorf("samples = %v, want 3", stats["samples"])
	}
}

package agentloop

import "testing"

// Parity: tests/test_adaptive_tool_budget.py::test_setter_shrinks_and_reports_true
func TestApplyLifetimeBudgetCapShrinksAndReportsTrue(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{ToolCallLifetimeBudget: 150, GraceIterations: 1}, nil)
	if !loop.ApplyLifetimeBudgetCap(60) {
		t.Fatal("ApplyLifetimeBudgetCap(60) = false, want true")
	}
	if got := loop.LifetimeBudget(); got != 60 {
		t.Fatalf("lifetime budget = %d, want 60", got)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_setter_refuses_raise_zero_and_unlimited
func TestApplyLifetimeBudgetCapRefusesRaiseZeroAndUnlimited(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{ToolCallLifetimeBudget: 100, GraceIterations: 1}, nil)
	for _, budget := range []int{200, 100, 0, -5} {
		if loop.ApplyLifetimeBudgetCap(budget) {
			t.Errorf("ApplyLifetimeBudgetCap(%d) = true, want false", budget)
		}
	}
	if got := loop.LifetimeBudget(); got != 100 {
		t.Fatalf("lifetime budget = %d, want 100", got)
	}

	unlimited := NewAgentLoop(LoopConfig{ToolCallLifetimeBudget: 0, GraceIterations: 1}, nil)
	if unlimited.ApplyLifetimeBudgetCap(50) {
		t.Error("unlimited loop accepted a cap")
	}
	if got := unlimited.LifetimeBudget(); got != 0 {
		t.Fatalf("unlimited lifetime budget = %d, want 0", got)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_setter_refuses_without_grace_phase
func TestApplyLifetimeBudgetCapRefusesWithoutGracePhase(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{ToolCallLifetimeBudget: 150, GraceIterations: 0}, nil)
	if loop.ApplyLifetimeBudgetCap(60) {
		t.Fatal("ApplyLifetimeBudgetCap(60) = true without grace, want false")
	}
	if got := loop.LifetimeBudget(); got != 150 {
		t.Fatalf("lifetime budget = %d, want 150", got)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_public_tool_calls_used_property
func TestToolCallsUsedProperty(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{}, nil)
	if got := loop.ToolCallsUsed(); got != 0 {
		t.Fatalf("ToolCallsUsed() = %d, want 0", got)
	}
	loop.toolCallsUsed = 7
	if got := loop.ToolCallsUsed(); got != 7 {
		t.Fatalf("ToolCallsUsed() = %d, want 7", got)
	}
}

// Parity: tests/test_adaptive_tool_budget.py::test_worker_cancel_paths_read_live_budget_limited
func TestBudgetLimitedReadsLiveGraceCounter(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{ToolCallLifetimeBudget: 30, GraceIterations: 1}, nil)
	if loop.BudgetLimited() {
		t.Fatal("BudgetLimited() = true before grace")
	}
	loop.counters = map[string]int{"tool_lifetime_budget_grace": 1}
	if !loop.BudgetLimited() {
		t.Fatal("BudgetLimited() = false after entering grace")
	}
	loop.toolCallsUsed = 30
	if got := loop.ToolCallsUsed(); got != 30 {
		t.Fatalf("ToolCallsUsed() = %d, want 30", got)
	}
}

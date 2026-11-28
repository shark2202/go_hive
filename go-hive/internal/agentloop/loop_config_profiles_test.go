package agentloop

import "testing"

// Parity-exempt: upstream has no standalone test for the queen profile
// constants; this pins the values queen_orchestrator builds LoopConfig from
// (core/framework/agents/queen/agent.py::queen_loop_config and
// ::queen_colony_loop_config) and proves DefaultLoopConfig stays untouched.
func TestQueenLoopProfilesMatchUpstreamBudgets(t *testing.T) {
	queen := QueenLoopConfig()
	if queen.MaxIterations != 999_999 {
		t.Errorf("queen MaxIterations = %d, want 999999", queen.MaxIterations)
	}
	if queen.ToolCallBudget != 30 {
		t.Errorf("queen ToolCallBudget = %d, want 30", queen.ToolCallBudget)
	}
	if queen.ToolCallHardLimit != 0 {
		t.Errorf("queen ToolCallHardLimit = %d, want 0 (multiple rule)", queen.ToolCallHardLimit)
	}
	if queen.ToolCallHardMultiple != 5 {
		t.Errorf("queen ToolCallHardMultiple = %d, want 5", queen.ToolCallHardMultiple)
	}
	if queen.GraceIterations != 0 {
		t.Errorf("queen GraceIterations = %d, want 0", queen.GraceIterations)
	}
	if queen.ToolCallLifetimeBudget != 0 {
		t.Errorf("queen ToolCallLifetimeBudget = %d, want 0", queen.ToolCallLifetimeBudget)
	}
	if queen.MaxContextTokens != 180_000 {
		t.Errorf("queen MaxContextTokens = %d, want 180000", queen.MaxContextTokens)
	}
	if soft, hard := NewAgentLoop(queen, nil).toolBudgetLimits(); soft != 30 || hard != 150 {
		t.Errorf("queen tool budget (soft, hard) = (%d, %d), want (30, 150)", soft, hard)
	}

	colony := ColonyQueenLoopConfig()
	if colony.MaxIterations != 999_999 {
		t.Errorf("colony MaxIterations = %d, want 999999", colony.MaxIterations)
	}
	if colony.ToolCallBudget != 50 {
		t.Errorf("colony ToolCallBudget = %d, want 50", colony.ToolCallBudget)
	}
	if colony.ToolCallHardLimit != 0 {
		t.Errorf("colony ToolCallHardLimit = %d, want 0 (multiple rule)", colony.ToolCallHardLimit)
	}
	if colony.ToolCallHardMultiple != 10 {
		t.Errorf("colony ToolCallHardMultiple = %d, want 10", colony.ToolCallHardMultiple)
	}
	if colony.GraceIterations != 0 {
		t.Errorf("colony GraceIterations = %d, want 0", colony.GraceIterations)
	}
	if colony.ToolCallLifetimeBudget != 0 {
		t.Errorf("colony ToolCallLifetimeBudget = %d, want 0", colony.ToolCallLifetimeBudget)
	}
	if colony.MaxContextTokens != 180_000 {
		t.Errorf("colony MaxContextTokens = %d, want 180000", colony.MaxContextTokens)
	}
	if soft, hard := NewAgentLoop(colony, nil).toolBudgetLimits(); soft != 50 || hard != 500 {
		t.Errorf("colony tool budget (soft, hard) = (%d, %d), want (50, 500)", soft, hard)
	}

	// The profiles must not mutate the generic default: workers and the
	// unconfigured loop still depend on its 50-iteration 30/50 base.
	defaults := DefaultLoopConfig()
	if defaults.MaxIterations != 50 || defaults.ToolCallBudget != 30 ||
		defaults.ToolCallHardLimit != 50 || defaults.ToolCallHardMultiple != 5 {
		t.Errorf("DefaultLoopConfig changed: iterations=%d budget=%d hard=%d multiple=%d",
			defaults.MaxIterations, defaults.ToolCallBudget,
			defaults.ToolCallHardLimit, defaults.ToolCallHardMultiple)
	}
}

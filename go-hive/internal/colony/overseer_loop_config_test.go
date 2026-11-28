package colony

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// Parity-exempt: upstream has no standalone test for the colony-queen loop
// profile; this pins the values queen_orchestrator selects for
// effective_phase == "colony" (the profile runOverseer now builds).
func TestOverseerLoopConfigUsesColonyQueenProfile(t *testing.T) {
	t.Setenv("HIVE_MAX_ITER", "")
	storage := t.TempDir()
	config := overseerLoopConfig(storage)

	if config.MaxIterations != 999_999 {
		t.Errorf("MaxIterations = %d, want 999999", config.MaxIterations)
	}
	if config.ToolCallBudget != 50 {
		t.Errorf("ToolCallBudget = %d, want 50", config.ToolCallBudget)
	}
	if config.ToolCallHardLimit != 0 {
		t.Errorf("ToolCallHardLimit = %d, want 0 (multiple rule)", config.ToolCallHardLimit)
	}
	if config.ToolCallHardMultiple != 10 {
		t.Errorf("ToolCallHardMultiple = %d, want 10", config.ToolCallHardMultiple)
	}
	if config.GraceIterations != 0 {
		t.Errorf("GraceIterations = %d, want 0", config.GraceIterations)
	}
	if config.ToolCallLifetimeBudget != 0 {
		t.Errorf("ToolCallLifetimeBudget = %d, want 0", config.ToolCallLifetimeBudget)
	}
	if soft, hard := config.ToolCallBudget, config.ToolCallBudget*config.ToolCallHardMultiple; soft != 50 || hard != 500 {
		t.Errorf("tool budget (soft, hard) = (%d, %d), want (50, 500)", soft, hard)
	}
	if want := filepath.Join(storage, "data"); config.SpilloverDir != want {
		t.Errorf("SpilloverDir = %q, want %q", config.SpilloverDir, want)
	}
	if got := overseerLoopConfig("").SpilloverDir; got != "" {
		t.Errorf("empty storage path SpilloverDir = %q, want empty", got)
	}
}

// Parity-exempt: upstream's HIVE_MAX_ITER read lives inline in
// queen_orchestrator and has no test of its own.
func TestOverseerLoopConfigHonorsHiveMaxIterCap(t *testing.T) {
	t.Setenv("HIVE_MAX_ITER", "7")
	if got := overseerLoopConfig("").MaxIterations; got != 7 {
		t.Fatalf("HIVE_MAX_ITER=7 gave MaxIterations %d, want 7", got)
	}

	if err := os.Unsetenv("HIVE_MAX_ITER"); err != nil {
		t.Fatalf("unset HIVE_MAX_ITER: %v", err)
	}
	if got, want := overseerLoopConfig("").MaxIterations, agentloop.ColonyQueenLoopConfig().MaxIterations; got != want {
		t.Fatalf("unset HIVE_MAX_ITER gave MaxIterations %d, want the colony default %d", got, want)
	}
	for _, value := range []string{"", "  ", "abc", "0", "-3"} {
		t.Setenv("HIVE_MAX_ITER", value)
		if got, want := overseerLoopConfig("").MaxIterations, agentloop.ColonyQueenLoopConfig().MaxIterations; got != want {
			t.Fatalf("HIVE_MAX_ITER=%q gave MaxIterations %d, want the colony default %d", value, got, want)
		}
	}
}

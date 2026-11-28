package colony

// The production overseer path: runOverseer builds its AgentLoop with
// overseerLoopConfig. This test reads the config off the loop the overseer
// actually runs on (not the helper's return value), so a call site that drifts
// back to DefaultLoopConfig or loses the HIVE_MAX_ITER cap fails here.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// newOverseerLoopProfileRuntime starts a real colony runtime whose only stub is
// the completer, so StartOverseer takes the same path a colony host does.
func newOverseerLoopProfileRuntime(t *testing.T) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   queenTestCaseSpec(),
		StoragePath: t.TempDir(),
		LLM:         newScriptedLLM(textResponse("idle")),
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "test_overseer_loop_profile",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// waitOverseerLoop starts the overseer and returns the loop runOverseer built
// once it is attached to the worker.
func waitOverseerLoop(t *testing.T, runtime *ColonyRuntime) *agentloop.AgentLoop {
	t.Helper()
	overseer, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if loop := overseer.AgentLoop(); loop != nil {
			return loop
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("overseer never built its AgentLoop")
	return nil
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop
func TestRunOverseerBuildsColonyQueenLoop(t *testing.T) {
	cases := []struct {
		name           string
		hiveMaxIter    string
		wantIterations int
	}{
		{name: "colony queen profile", wantIterations: 999_999},
		{name: "HIVE_MAX_ITER cap", hiveMaxIter: "7", wantIterations: 7},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("HIVE_MAX_ITER", testCase.hiveMaxIter)
			runtime := newOverseerLoopProfileRuntime(t)
			loop := waitOverseerLoop(t, runtime)
			config := loop.Config

			if config.MaxIterations != testCase.wantIterations {
				t.Errorf("overseer loop MaxIterations = %d, want %d", config.MaxIterations, testCase.wantIterations)
			}
			if config.ToolCallBudget != 50 {
				t.Errorf("overseer loop ToolCallBudget = %d, want 50", config.ToolCallBudget)
			}
			if config.ToolCallHardMultiple != 10 {
				t.Errorf("overseer loop ToolCallHardMultiple = %d, want 10", config.ToolCallHardMultiple)
			}
			if config.ToolCallHardLimit != 0 {
				t.Errorf("overseer loop ToolCallHardLimit = %d, want 0 (multiple rule)", config.ToolCallHardLimit)
			}
			if soft, hard := config.ToolCallBudget, config.ToolCallBudget*config.ToolCallHardMultiple; soft != 50 || hard != 500 {
				t.Errorf("overseer loop tool envelope = (%d, %d), want (50, 500)", soft, hard)
			}
			if config.GraceIterations != 0 {
				t.Errorf("overseer loop GraceIterations = %d, want 0", config.GraceIterations)
			}
			if want := filepath.Join(runtime.storagePath, "data"); config.SpilloverDir != want {
				t.Errorf("overseer loop SpilloverDir = %q, want %q", config.SpilloverDir, want)
			}
		})
	}
}

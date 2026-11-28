package cli

// The fork and pivot write paths both snapshot the source queen's *live* loop
// config into the new colony's worker.json. These tests pin the current queen
// profile end to end (999_999 iterations, 30-call soft budget) instead of only
// comparing worker.json against the snapshot, which is tautological when the
// snapshot itself is stale.

import (
	"path/filepath"
	"testing"

	hivecolony "github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// newLiveLoopForkSource builds a source queen on the production chat runtime
// so her fork surface is the real live loop config. An empty HIVE_MAX_ITER is
// absence to the config reader; pin it so a developer's shell cannot cap the
// profile under test.
func newLiveLoopForkSource(t *testing.T) *hiveserver.Session {
	t.Helper()
	// Empty is absence to envPositiveInt, so this pins the uncapped profile
	// even when the developer's shell exports HIVE_MAX_ITER.
	t.Setenv("HIVE_MAX_ITER", "")
	source := hiveserver.NewSession("src-loop", eventbus.NewEventBus())
	armColonyForkQueen(t, source, []llm.ToolDef{
		{Name: "terminal_exec", Description: "run a command"},
	})
	return source
}

// assertCurrentQueenLoopSurface pins the aligned queen profile the w96 change
// established: unbounded iterations and the 30-call soft budget.
func assertCurrentQueenLoopSurface(t *testing.T, surface hiveserver.QueenForkSurface) {
	t.Helper()
	if !surface.HasLoop {
		t.Fatalf("source queen surface carries no loop config: %#v", surface)
	}
	if surface.MaxIterations != 999_999 {
		t.Fatalf("source queen MaxIterations = %d, want 999999", surface.MaxIterations)
	}
	if surface.ToolCallBudget != 30 {
		t.Fatalf("source queen ToolCallBudget = %d, want 30", surface.ToolCallBudget)
	}
}

// assertWorkerLoopConfig checks the four inherited knobs plus the worker
// defaults the queen profile deliberately does not override (upstream's
// build_loop_config_dict copies the budget but not tool_call_hard_multiple, so
// workers keep their tighter 3x hard stop and 200-call lifetime).
func assertWorkerLoopConfig(t *testing.T, worker map[string]any, surface hiveserver.QueenForkSurface) {
	t.Helper()
	loopConfig, ok := worker["loop_config"].(map[string]any)
	if !ok {
		t.Fatalf("worker loop_config = %#v, want a dict", worker["loop_config"])
	}
	if loopConfig["max_iterations"] != float64(surface.MaxIterations) {
		t.Fatalf("worker max_iterations = %#v, want %d", loopConfig["max_iterations"], surface.MaxIterations)
	}
	if loopConfig["tool_call_budget"] != float64(surface.ToolCallBudget) {
		t.Fatalf("worker tool_call_budget = %#v, want %d", loopConfig["tool_call_budget"], surface.ToolCallBudget)
	}
	if loopConfig["max_context_tokens"] != float64(surface.MaxContextTokens) {
		t.Fatalf("worker max_context_tokens = %#v, want %d", loopConfig["max_context_tokens"], surface.MaxContextTokens)
	}
	if loopConfig["max_tool_result_chars"] != float64(surface.MaxToolResultChars) {
		t.Fatalf("worker max_tool_result_chars = %#v, want %d", loopConfig["max_tool_result_chars"], surface.MaxToolResultChars)
	}
	if want := float64(hivecolony.DefaultWorkerLoopConfig["tool_call_hard_multiple"]); loopConfig["tool_call_hard_multiple"] != want {
		t.Fatalf("worker tool_call_hard_multiple = %#v, want the worker default %v", loopConfig["tool_call_hard_multiple"], want)
	}
	if want := float64(hivecolony.DefaultWorkerLoopConfig["tool_call_lifetime_budget"]); loopConfig["tool_call_lifetime_budget"] != want {
		t.Fatalf("worker tool_call_lifetime_budget = %#v, want the worker default %v", loopConfig["tool_call_lifetime_budget"], want)
	}
}

// Parity: framework/server/routes_execution.py::fork_session_into_colony
// Parity: framework/agents/queen/worker_definition.py::build_loop_config_dict
func TestColonyForkWorkerDefinitionCarriesLiveQueenLoopProfile(t *testing.T) {
	source := newLiveLoopForkSource(t)
	surface := source.Chat.ForkSurfaceSnapshot()
	assertCurrentQueenLoopSurface(t, surface)

	colonyPath := filepath.Join(t.TempDir(), "forkloop")
	if _, err := writeColonyFromSession(
		colonyPath, "forkloop", "queen_alpha", source,
		hiveserver.CreateColonyFromSourceParams{SourceSessionID: source.ID},
	); err != nil {
		t.Fatalf("writeColonyFromSession: %v", err)
	}

	worker := readForkWorker(t, colonyPath)
	loopConfig, _ := worker["loop_config"].(map[string]any)
	if loopConfig["max_iterations"] != float64(999_999) {
		t.Fatalf("forked worker max_iterations = %#v, want 999999", loopConfig["max_iterations"])
	}
	if loopConfig["tool_call_budget"] != float64(30) {
		t.Fatalf("forked worker tool_call_budget = %#v, want 30", loopConfig["tool_call_budget"])
	}
	assertWorkerLoopConfig(t, worker, surface)
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
// Parity: framework/agents/queen/worker_definition.py::build_loop_config_dict
func TestSiblingWorkerDefinitionCarriesLiveQueenLoopProfile(t *testing.T) {
	source := newLiveLoopForkSource(t)
	surface := source.Chat.ForkSurfaceSnapshot()
	assertCurrentQueenLoopSurface(t, surface)

	bindingDir := t.TempDir()
	workerPath := filepath.Join(t.TempDir(), "worker.json")
	binding := &tracker.Binding{
		Name:      "siblingloop",
		Dir:       bindingDir,
		TrackerDB: filepath.Join(bindingDir, "tracker", "tracker.db"),
	}
	if err := writeSiblingWorkerDefinition(workerPath, source, binding); err != nil {
		t.Fatalf("writeSiblingWorkerDefinition: %v", err)
	}

	worker, ok := readJSONObject(workerPath)
	if !ok {
		t.Fatal("read sibling worker.json: unavailable")
	}
	loopConfig, _ := worker["loop_config"].(map[string]any)
	if loopConfig["max_iterations"] != float64(999_999) {
		t.Fatalf("sibling worker max_iterations = %#v, want 999999", loopConfig["max_iterations"])
	}
	if loopConfig["tool_call_budget"] != float64(30) {
		t.Fatalf("sibling worker tool_call_budget = %#v, want 30", loopConfig["tool_call_budget"])
	}
	assertWorkerLoopConfig(t, worker, surface)
}

// Parity-exempt: upstream has no test for a capped source queen loop. The
// behaviour under test is upstream's own: _create_sibling_colony_from_colony
// reads `queen_loop._config` verbatim, so a source loop built with
// HIVE_MAX_ITER=50 (or a runtime still running a pre-alignment build) forks
// 50, not the current 999_999 profile. Normalizing it here would silently
// overrule the operator's cap and diverge from the reference.
//
// Migration residual: a live process started by a pre-alignment binary keeps
// the old loop profile until it restarts; the next process rebuilds the queen
// loop through chatLoopConfig. No worker.json rewrite is wanted, because the
// snapshot must describe the queen that actually forked it.
func TestColonyForkWorkerDefinitionPreservesCappedQueenLoop(t *testing.T) {
	t.Setenv("HIVE_MAX_ITER", "50")
	source := hiveserver.NewSession("src-legacy", eventbus.NewEventBus())
	armColonyForkQueen(t, source, nil)
	surface := source.Chat.ForkSurfaceSnapshot()
	if !surface.HasLoop {
		t.Fatal("source queen surface carries no loop config")
	}
	if surface.MaxIterations != 50 || surface.ToolCallBudget != 30 {
		t.Fatalf("capped source queen surface = %d/%d, want 50/30", surface.MaxIterations, surface.ToolCallBudget)
	}

	colonyPath := filepath.Join(t.TempDir(), "legacyfork")
	if _, err := writeColonyFromSession(
		colonyPath, "legacyfork", "queen_alpha", source,
		hiveserver.CreateColonyFromSourceParams{SourceSessionID: source.ID},
	); err != nil {
		t.Fatalf("writeColonyFromSession: %v", err)
	}
	worker := readForkWorker(t, colonyPath)
	loopConfig, _ := worker["loop_config"].(map[string]any)
	if loopConfig["max_iterations"] != float64(50) {
		t.Fatalf("forked worker max_iterations = %#v, want the source's capped 50", loopConfig["max_iterations"])
	}
	if loopConfig["max_iterations"] == float64(999_999) {
		t.Fatal("fork normalized the source's live loop config to the current queen profile")
	}
	if loopConfig["tool_call_budget"] != float64(30) {
		t.Fatalf("forked worker tool_call_budget = %#v, want 30", loopConfig["tool_call_budget"])
	}
}

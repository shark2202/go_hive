package hive

import (
	"context"
	"os"
	"testing"
	"time"
)

// Parity-exempt: Go-only validation on the colony constructor.
func TestNewColonyValidatesRequiredOptions(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	spec := NewAgentSpec("colony", "Colony", "Test colony")
	if _, err := NewColony(ColonyOptions{Home: home, AgentSpec: spec}); err == nil {
		t.Fatal("NewColony accepted a nil model")
	}
	if _, err := NewColony(ColonyOptions{Home: home, LLM: NewMockProvider()}); err == nil {
		t.Fatal("NewColony accepted an empty agent spec id")
	}
	if _, err := NewColony(ColonyOptions{AgentSpec: spec, LLM: NewMockProvider()}); err == nil {
		t.Fatal("NewColony accepted a missing home")
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime
func TestColonyUsesTheProvidedEventBus(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	bus := NewEventBus()
	colony, err := NewColony(ColonyOptions{
		Home:      home,
		ID:        "bus-colony",
		AgentSpec: NewAgentSpec("colony", "Colony", "Test colony"),
		LLM:       NewMockProvider(),
		EventBus:  bus,
	})
	if err != nil {
		t.Fatalf("NewColony: %v", err)
	}
	defer colony.Stop()
	if colony.Events() != bus {
		t.Fatal("colony did not use the provided event bus")
	}
	if _, ok := colony.Worker("missing"); ok {
		t.Fatal("Worker returned an unknown worker")
	}
}

// Parity: framework/agents/queen/worker_definition.py::build_loop_config_dict
func TestWorkerDefinitionAndLoopConfig(t *testing.T) {
	definition := DefaultWorkerDefinition()
	if definition.ID == "" || definition.Name == "" {
		t.Fatalf("definition = %+v", definition)
	}
	config, err := WorkerLoopConfig(nil)
	if err != nil {
		t.Fatalf("WorkerLoopConfig: %v", err)
	}
	if config.MaxIterations <= 0 {
		t.Fatalf("worker loop has no iteration budget: %+v", config)
	}
	if prompt := WorkerSystemPrompt("Inspect the logs."); prompt == "" {
		t.Fatal("WorkerSystemPrompt returned empty text")
	}
}

// Parity: framework/tools/playbook_tools.py::run_playbook
func TestPlaybookPrimitives(t *testing.T) {
	if name := RunPlaybookTool().Name; name != "run_playbook" {
		t.Fatalf("RunPlaybookTool name = %q", name)
	}
	if name := RunWorkerTool().Name; name != "run_worker" {
		t.Fatalf("RunWorkerTool name = %q", name)
	}
	if tools := PlaybookControlTools(); len(tools) == 0 {
		t.Fatal("PlaybookControlTools returned none")
	}
	if got := ComputeHardTimeout(10 * time.Second); got <= 10*time.Second {
		t.Fatalf("ComputeHardTimeout = %v, want greater than the soft timeout", got)
	}
	if got := SanitizePlaybookName("Nightly Report"); got == "" {
		t.Fatal("SanitizePlaybookName returned empty")
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports
func TestColonyRunsAWorker(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	colony, err := NewColony(ColonyOptions{
		Home:                 home,
		ID:                   "run-colony",
		AgentSpec:            NewAgentSpec("colony", "Colony", "Test colony"),
		LLM:                  NewMockProvider(),
		MaxConcurrentWorkers: 1,
		SkillsProvider:       func() ([]*Skill, bool) { return nil, false },
	})
	if err != nil {
		t.Fatalf("NewColony: %v", err)
	}
	defer colony.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := colony.Run(ctx, []Task{{Task: "Say hello and report."}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.RunID == "" || result.ColonyID != "run-colony" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.WorkerResults) != 1 {
		t.Fatalf("worker results = %+v, want one", result.WorkerResults)
	}
	if result.WorkerResults[0].WorkerID == "" {
		t.Fatalf("worker outcome = %+v", result.WorkerResults[0])
	}
}

// Parity-exempt: Go-only tracker DB surface (hive-desktop-runtime).
func TestTrackerPrimitives(t *testing.T) {
	home, err := HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	if _, err := TrackerFor(home, "missing"); err == nil {
		t.Fatal("TrackerFor accepted a missing colony directory")
	}
	path, err := EnsureTracker(home, "tracked")
	if err != nil {
		t.Fatalf("EnsureTracker: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tracker db not created: %v", err)
	}
	binding, err := TrackerFor(home, "tracked")
	if err != nil {
		t.Fatalf("TrackerFor: %v", err)
	}
	if binding.Name != "tracked" || binding.TrackerDB != path {
		t.Fatalf("binding = %+v", binding)
	}
	if err := ValidateTrackerSQL("SELECT 1"); err != nil {
		t.Fatalf("ValidateTrackerSQL(select): %v", err)
	}
	if err := ValidateTrackerSQL("ATTACH DATABASE 'x' AS y"); err == nil {
		t.Fatal("ValidateTrackerSQL accepted an attach")
	}
	result := QueryTracker(binding, "SELECT 1 AS answer", 10)
	if result == nil {
		t.Fatal("QueryTracker returned nil")
	}
}

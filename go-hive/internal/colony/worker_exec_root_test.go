package colony

// Per-worker tool execution root.
//
// Parity: framework/host/worker.py::Worker.run (storage_path + session_cwd) and
// framework/host/colony_runtime.py::ColonyRuntime.start_overseer (overseer
// storage remains the colony root)

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// A normal worker executes tools through the executor built for its own
// storage directory; the persistent overseer keeps the colony-root executor.
//
// Parity-exempt: Go-only wiring test for the per-worker tool execution root
// seam (SetWorkerToolExecutorFactory) and the overseer's colony-root exception;
// no Python unit test asserts these Go runtime roots.
func TestWorkerToolExecutorUsesWorkerStorageRoot(t *testing.T) {
	colonyRoot := t.TempDir()
	workerRoot := filepath.Join(colonyRoot, "workers", "w_cwd")

	var factoryRoots []string
	var factoryBase agentloop.ToolExecutor
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: colonyRoot,
		LLM:         newScriptedLLM(textResponse("idle")),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "colony-root"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "test_colony",
	})
	runtime.SetWorkerToolExecutorFactory(func(root string, base agentloop.ToolExecutor) agentloop.ToolExecutor {
		factoryRoots = append(factoryRoots, root)
		factoryBase = base
		return func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "worker-root:" + root}, nil
		}
	})

	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w_cwd", Task: "probe the root"})
	if worker.StoragePath != workerRoot {
		t.Fatalf("worker.StoragePath = %q, want %q", worker.StoragePath, workerRoot)
	}
	result, err := runtime.executorFor(worker)(agentloop.ToolCallRequest{
		Name:  "read_file",
		Input: map[string]any{"path": "probe.txt"},
	})
	if err != nil {
		t.Fatalf("worker executor: %v", err)
	}
	if result.Content != "worker-root:"+workerRoot {
		t.Fatalf("worker tool ran through %q, want the executor for %q", result.Content, workerRoot)
	}
	if len(factoryRoots) != 1 || factoryRoots[0] != workerRoot {
		t.Fatalf("executor factory roots = %v, want [%s]", factoryRoots, workerRoot)
	}
	if factoryBase == nil {
		t.Fatal("executor factory did not receive the colony-root executor as its fallback")
	}

	// The overseer is the colony's own client-facing session: it stays rooted
	// at the colony workspace and must never consult the worker factory.
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	overseer, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if overseer.StoragePath != colonyRoot {
		t.Fatalf("overseer.StoragePath = %q, want the colony root %q", overseer.StoragePath, colonyRoot)
	}
	result, err = runtime.executorFor(overseer)(agentloop.ToolCallRequest{
		Name:  "read_file",
		Input: map[string]any{"path": "probe.txt"},
	})
	if err != nil {
		t.Fatalf("overseer executor: %v", err)
	}
	if result.Content != "colony-root" {
		t.Fatalf("overseer tool ran through %q, want the colony-root executor", result.Content)
	}
	if len(factoryRoots) != 1 {
		t.Fatalf("executor factory was consulted for the overseer: %v", factoryRoots)
	}
}

// A worker without its own storage (a hand-built worker, or a resumed one
// whose storage could not be resolved) keeps the colony-root executor instead
// of failing over an empty root.
//
// Parity-exempt: Go-only wiring test - a worker without storage keeps the
// colony-root executor and never consults the factory; no Python unit
// counterpart.
func TestWorkerToolExecutorWithoutStorageKeepsColonyRoot(t *testing.T) {
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "colony-root"}, nil
		},
	})
	called := false
	runtime.SetWorkerToolExecutorFactory(func(root string, base agentloop.ToolExecutor) agentloop.ToolExecutor {
		called = true
		return func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "worker-root"}, nil
		}
	})
	worker := NewWorker("w_no_storage", "probe", "worker:w_no_storage")
	result, err := runtime.executorFor(worker)(agentloop.ToolCallRequest{Name: "read_file"})
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	if called {
		t.Fatal("executor factory was consulted without a worker storage root")
	}
	if result.Content != "colony-root" {
		t.Fatalf("tool ran through %q, want the colony-root executor", result.Content)
	}
}

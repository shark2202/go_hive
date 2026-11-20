package colony

// The worker_llm slot's consumer: a spawned worker runs on the dedicated
// worker transport, while the persistent overseer (the colony's own Queen)
// keeps the colony transport. Without worker_llm workers share the colony
// transport, exactly as upstream's build_worker_llm() -> None fallback.
//
// Parity: framework/server/session_manager.py::_start_queen
// (ColonyRuntime llm=self.build_worker_llm() or session.llm),
// framework/host/colony_runtime.py::ColonyRuntime._build_worker
// (AgentContext(llm=self._llm))

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: core/tests/test_session_unified_colony_runtime.py::test_unified_colony_uses_configured_worker_llm
func TestWorkersRunOnTheWorkerSlotAndTheOverseerKeepsTheColonyModel(t *testing.T) {
	colonyLLM := newGateLLM("slow-none")
	workerLLM := newGateLLM("slow-none")
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		LLM:         colonyLLM,
		WorkerLLM:   workerLLM,
		StreamID:    "colony_alpha",
		EventBus:    eventbus.NewEventBus(),
	})

	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do the task"})
	if worker.Context == nil {
		t.Fatal("worker built without an agent context")
	}
	if worker.Context.LLM != llm.Completer(workerLLM) {
		t.Fatalf("worker context LLM = %#v, want the worker_llm transport", worker.Context.LLM)
	}

	overseer := runtime.buildContext(&Worker{ID: "overseer:colony_alpha", Persistent: true}, testAgentSpec())
	if overseer.LLM != llm.Completer(colonyLLM) {
		t.Fatalf("overseer LLM = %#v, want the colony transport", overseer.LLM)
	}

	// Without worker_llm, workers keep sharing the colony transport.
	bare := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		LLM:         colonyLLM,
		StreamID:    "colony_beta",
		EventBus:    eventbus.NewEventBus(),
	})
	fallback := bare.BuildWorker(WorkerBuildOptions{WorkerID: "w2", Task: "do the task"})
	if fallback.Context == nil || fallback.Context.LLM != llm.Completer(colonyLLM) {
		t.Fatalf("fallback worker LLM = %#v, want the colony transport", fallback.Context.LLM)
	}
}

package server

// The memory-work model slot, proven against upstream's _MemoryLLM tests.
//
// Parity: core/tests/test_queen_memory.py::test_memory_llm_follows_the_setting_while_the_session_runs
// Parity: core/tests/test_queen_memory.py::test_memory_llm_builds_the_worker_model_again_only_when_its_slot_changes
// Parity: core/tests/test_queen_memory.py::test_memory_llm_falls_back_to_her_model_without_a_worker_model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

// namedMemoryLLM is upstream's _NamedLLM: a completer that only reports a
// model name, so a test can tell which slot a call would use.
type namedMemoryLLM struct {
	model string
}

func (n *namedMemoryLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, nil
}

func (n *namedMemoryLLM) ModelName() string { return n.model }

// Parity: core/tests/test_queen_memory.py::test_memory_llm_follows_the_setting_while_the_session_runs
func TestChatMemoryLLMFollowsTheSettingWhileTheSessionRuns(t *testing.T) {
	queen := &namedMemoryLLM{model: "queen-model"}
	worker := &namedMemoryLLM{model: "worker-model"}
	settings := memory.ReflectionConfig{Model: "worker"}
	resolver := &chatMemoryLLM{
		session: queen,
		config:  func() memory.ReflectionConfig { return settings },
	}
	resolver.workerSection = func() string { return `{"model":"worker-model"}` }
	resolver.buildWorker = func() llm.Completer { return worker }

	if got := resolver.ModelName(); got != "worker-model" {
		t.Fatalf("model with memory_reflection.model=worker = %q, want worker-model", got)
	}

	settings.Model = "main"
	if got := resolver.ModelName(); got != "queen-model" {
		t.Fatalf("model after switching to main = %q, want queen-model", got)
	}
}

// Parity: core/tests/test_queen_memory.py::test_memory_llm_builds_the_worker_model_again_only_when_its_slot_changes
func TestChatMemoryLLMBuildsTheWorkerModelAgainOnlyWhenItsSlotChanges(t *testing.T) {
	session := &namedMemoryLLM{model: "queen-model"}
	section := `{"model":"worker-a"}`
	current := "worker-a"
	built := make([]string, 0, 2)
	resolver := &chatMemoryLLM{
		session: session,
		config:  func() memory.ReflectionConfig { return memory.ReflectionConfig{Model: "worker"} },
	}
	resolver.workerSection = func() string { return section }
	resolver.buildWorker = func() llm.Completer {
		built = append(built, current)
		return &namedMemoryLLM{model: current}
	}

	if got := []string{resolver.ModelName(), resolver.ModelName()}; got[0] != "worker-a" || got[1] != "worker-a" {
		t.Fatalf("models = %#v, want worker-a twice", got)
	}
	if len(built) != 1 || built[0] != "worker-a" {
		t.Fatalf("worker builds = %#v, want one worker-a build", built)
	}

	current = "worker-b"
	section = `{"model":"worker-b"}`
	if got := resolver.ModelName(); got != "worker-b" {
		t.Fatalf("model after the slot changed = %q, want worker-b", got)
	}
	if len(built) != 2 || built[1] != "worker-b" {
		t.Fatalf("worker builds = %#v, want a second worker-b build", built)
	}
}

// Parity: core/tests/test_queen_memory.py::test_memory_llm_falls_back_to_her_model_without_a_worker_model
func TestChatMemoryLLMFallsBackToHerModelWithoutAWorkerModel(t *testing.T) {
	queen := &namedMemoryLLM{model: "queen-model"}
	resolver := &chatMemoryLLM{
		session: queen,
		config:  func() memory.ReflectionConfig { return memory.ReflectionConfig{Model: "worker"} },
	}
	resolver.workerSection = func() string { return "null" }
	resolver.buildWorker = func() llm.Completer { return nil }

	if got := resolver.ModelName(); got != "queen-model" {
		t.Fatalf("model without a worker slot = %q, want the session's queen-model", got)
	}
}

// TestChatRuntimeReflectionFollowsTheWorkerSlot proves the runtime wiring, not
// just the resolver: the reflection scheduler is built on the resolver, so a
// configured worker_llm section runs memory work there and memory_reflection
// .model=main moves it back to the session's own provider.
//
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop
// (_memory_llm = session_manager.memory_llm(session))
func TestChatRuntimeReflectionFollowsTheWorkerSlot(t *testing.T) {
	homeRoot := t.TempDir()
	writeMemorySlotConfig(t, homeRoot, `{
	  "worker_llm": {"provider": "ollama", "model": "qwen-local"},
	  "memory_reflection": {"every_turns": 6, "long_every": 4}
	}`)

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &namedMemoryLLM{model: "queen-model"},
		Root:      t.TempDir(),
		SessionID: "session_memory_slot",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.reflection == nil {
		t.Fatal("reflection scheduler not built")
	}
	resolver, ok := runtime.reflection.LLM.(*chatMemoryLLM)
	if !ok {
		t.Fatalf("reflection LLM = %T, want *chatMemoryLLM", runtime.reflection.LLM)
	}
	if got := resolver.ModelName(); got != "ollama_chat/qwen-local" {
		t.Fatalf("reflection model = %q, want the worker slot's ollama_chat/qwen-local", got)
	}

	writeMemorySlotConfig(t, homeRoot, `{
	  "worker_llm": {"provider": "ollama", "model": "qwen-local"},
	  "memory_reflection": {"every_turns": 6, "long_every": 4, "model": "main"}
	}`)
	if got := resolver.ModelName(); got != "queen-model" {
		t.Fatalf("reflection model after main = %q, want the session's queen-model", got)
	}
}

func writeMemorySlotConfig(t *testing.T, homeRoot, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(homeRoot, "configuration.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
}

// The timeline shares the resolver (upstream passes the same _memory_llm to
// subscribe_timeline_triggers). Pin that the runtime's timeline writers read
// it rather than the session provider directly.
//
// Parity: core/framework/server/queen_orchestrator.py (subscribe_timeline_triggers
// receives _memory_llm)
func TestChatRuntimeTimelineUsesTheMemorySlotResolver(t *testing.T) {
	homeRoot := t.TempDir()
	writeMemorySlotConfig(t, homeRoot, `{"worker_llm": {"provider": "ollama", "model": "qwen-local"}}`)
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &namedMemoryLLM{model: "queen-model"},
		Root:      t.TempDir(),
		SessionID: "session_memory_timeline",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.memoryWorkLLM() != runtime.reflection.LLM {
		t.Fatalf("timeline resolver = %T, reflection LLM = %T; want the same memory-work completer",
			runtime.memoryWorkLLM(), runtime.reflection.LLM)
	}
	if !strings.Contains(runtime.memoryWorkLLM().(llm.ModelNamer).ModelName(), "qwen-local") {
		t.Fatalf("memory-work model = %q, want the worker slot", runtime.memoryWorkLLM().(llm.ModelNamer).ModelName())
	}
}

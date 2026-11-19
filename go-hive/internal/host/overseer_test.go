package host

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
)

type overseerTestLLM struct {
	calls atomic.Int32
}

func (f *overseerTestLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.calls.Add(1)
	return llm.Completion{Content: "ready", Model: "scripted"}, nil
}

// Parity: tests/test_colony_runtime_overseer.py::test_start_overseer_creates_persistent_worker
func TestHostStartOverseerStartsRuntimeFirst(t *testing.T) {
	built, err := New(Config{
		ColonyID:    "colony_a",
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "worker", AgentType: "event_loop"},
		StoragePath: t.TempDir(),
		LLM:         &overseerTestLLM{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if built.Started() {
		t.Fatal("host reports started before Start")
	}

	queenSpec := agentloop.AgentSpec{ID: "queen", Name: "Queen", AgentType: "event_loop"}
	worker, err := built.StartOverseer(context.Background(), queenSpec, nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if worker == nil {
		t.Fatal("StartOverseer returned no worker")
	}
	if !built.Started() {
		t.Fatal("StartOverseer did not start the host")
	}
	if !built.OverseerRunning() {
		t.Fatal("OverseerRunning = false after StartOverseer")
	}
	if built.Runtime().Overseer() != worker {
		t.Fatal("runtime overseer is not the returned worker")
	}

	built.Stop()
	if built.Started() {
		t.Fatal("host reports started after Stop")
	}
}

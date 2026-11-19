package colony

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

// Parity-exempt: Go-side worker reminder wiring. Upstream workers inherit the
// agent loop's hub; the Go port constructed a bare loop, so this test pins the
// task + idle sources on the worker's own hub.
func TestWorkerAgentLoopRegistersTaskAndIdleReminderSources(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_worker_reminders",
	})
	worker := NewWorker("w1", "do the thing", "worker:w1")
	loop, stop := colony.newWorkerAgentLoop(worker, agentloop.DefaultLoopConfig(), nil)
	defer stop()

	if loop.Hub == nil {
		t.Fatal("worker loop has no reminder hub")
	}
	registered := map[string]bool{}
	for _, name := range loop.Hub.RegisteredSourceNames() {
		registered[name] = true
	}
	if !registered["tasks"] {
		t.Fatalf("registered sources = %v, want the task reminder", loop.Hub.RegisteredSourceNames())
	}
	if !registered["idle_nudge"] {
		t.Fatalf("registered sources = %v, want the idle nudge", loop.Hub.RegisteredSourceNames())
	}
	if !loop.Hub.HasTicker() {
		t.Fatal("worker hub did not start its idle ticker")
	}
}

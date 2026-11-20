package colony

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// Parity-exempt: the worker hub's source set is a Go wiring decision (upstream
// workers inherit the shared AgentLoop hub); pins the worker role's registered
// matrix: task + worker-tracker cadence + idle + stream-stall, with no
// queen-only source.
func TestWorkerAgentLoopRegistersWorkerRoleSources(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_worker_matrix",
	})
	worker := NewWorker("w1", "do the thing", "worker:w1")
	tools := workerTaskToolDefs()
	loop, stop := colony.newWorkerAgentLoop(worker, agentloop.DefaultLoopConfig(), tools)
	defer stop()

	registered := loop.Hub.RegisteredSourceNames()
	for _, want := range []string{"tasks", "worker_tracker_snapshot", "idle_nudge", "stream_stall"} {
		if !containsName(registered, want) {
			t.Fatalf("worker hub sources = %v, missing %q", registered, want)
		}
	}
	for _, unwanted := range []string{"colony_parallel", "active_workers", "tracker_snapshot", "colony_worker_snapshot", "skills_catalog"} {
		if containsName(registered, unwanted) {
			t.Fatalf("worker hub carries the queen-only source %q: %v", unwanted, registered)
		}
	}

	// Bound matrix: the task source applies because the worker's tool list
	// carries task tools; the worker tracker source applies on binding-provider
	// presence.
	active := loop.Hub.ActiveSourceNames()
	for _, want := range []string{"tasks", "worker_tracker_snapshot", "idle_nudge", "stream_stall"} {
		if !containsName(active, want) {
			t.Fatalf("active worker sources = %v, missing %q", active, want)
		}
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer (the
// overseer wraps a full AgentLoop, so its hub carries the shipped sources and
// applies_to decides) — the Go port built the loop bare; pins the queen-role
// sources its hub now registers.
func TestOverseerReminderWiringRegistersQueenSources(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_overseer_matrix",
	})
	worker := NewWorker("overseer:colony_overseer_matrix", "", "overseer")
	worker.Persistent = true
	loop := agentloop.NewAgentLoop(agentloop.DefaultLoopConfig(), colony.executorFor(worker))
	stop := colony.startOverseerReminders(loop, worker, loop.Config, colony.workerTools(worker))
	defer stop()

	registered := loop.Hub.RegisteredSourceNames()
	for _, want := range []string{"colony_parallel", "active_workers", "tracker_snapshot", "colony_worker_snapshot", "stream_stall"} {
		if !containsName(registered, want) {
			t.Fatalf("overseer hub sources = %v, missing %q", registered, want)
		}
	}
	if containsName(registered, "worker_tracker_snapshot") {
		t.Fatalf("overseer hub must not carry the worker-only tracker source: %v", registered)
	}
	if loop.Hub.HasTicker() {
		t.Fatal("overseer hub must not start a session-level idle ticker (the chat runtime owns it)")
	}

	// Bound matrix: every registered queen source passes its gate with a
	// queen-shaped context and a real colony binding provider.
	active := loop.Hub.ActiveSourceNames()
	for _, want := range []string{"colony_parallel", "active_workers", "tracker_snapshot", "colony_worker_snapshot", "stream_stall"} {
		if !containsName(active, want) {
			t.Fatalf("active overseer sources = %v, missing %q", active, want)
		}
	}

	// The colony-phase gate sees run_worker, so the parallel nudge is live.
	body, ok := agentloop.NewColonyParallelNudgeSource().Render(agentloop.ReminderContext{
		Point: agentloop.POST_TOOL_USE,
		AgentCtx: &agentloop.ReminderAgentContext{
			IsQueenStream:  true,
			AvailableTools: reminderToolsFromDefs(colony.workerTools(worker)),
		},
	})
	if ok {
		t.Fatalf("parallel nudge fired before pilot work: %q", body)
	}
	for _, def := range colony.workerTools(worker) {
		if def.Name == "run_worker" {
			return
		}
	}
	t.Fatal("overseer tool list is missing run_worker; the colony-phase gate would stay closed")
}

// Parity-exempt: teardown leak check for the overseer hub (Go-only wiring).
func TestOverseerReminderTeardownClearsPending(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_overseer_leak",
	})
	worker := NewWorker("overseer:colony_overseer_leak", "", "overseer")
	worker.Persistent = true
	loop := agentloop.NewAgentLoop(agentloop.DefaultLoopConfig(), colony.executorFor(worker))
	stop := colony.startOverseerReminders(loop, worker, loop.Config, colony.workerTools(worker))

	loop.Hub.Post(agentloop.Reminder{Body: "parked", Source: "test"})
	if loop.Hub.PendingLen() == 0 {
		t.Fatal("Post did not park the reminder")
	}
	stop()
	if got := loop.Hub.PendingLen(); got != 0 {
		t.Fatalf("pending reminders after teardown = %d, want 0", got)
	}
	if loop.Hub.HasTicker() {
		t.Fatal("ticker still running after teardown")
	}
}

// Parity-exempt: the worker-side binding adapter is Go plumbing; pins that it
// resolves the colony's tracker DB and self-skips without one.
func TestReminderColonyBindingSkipsWithoutTracker(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_binding_matrix",
	})
	if binding := colony.reminderColonyBinding(); binding != nil {
		t.Fatalf("binding without a provisioned tracker = %+v, want nil", binding)
	}
	if stats := colony.reminderColonyStats(); stats.Active != 0 || stats.Total != 0 {
		t.Fatalf("stats for an empty runtime = %+v, want zeroes", stats)
	}
	if workers := colony.activeWorkerReminders(); len(workers) != 0 {
		t.Fatalf("active workers for an empty runtime = %v, want none", workers)
	}
}

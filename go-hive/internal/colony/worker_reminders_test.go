package colony

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// Parity-exempt: Go-side real-session evidence for the worker tracker nudge.
// Upstream ships no worker-tracker test module; this drives a worker through
// the production spawn path, with a real tracker.db row and a scripted model
// that uses a tool, and reads the reminder out of the persisted worker
// conversation.
func TestWorkerTrackerSnapshotReachesARealWorkerTurn(t *testing.T) {
	// The cadence is a package-level knob read when the worker's source is
	// built; pin it for this test (and restore it after).
	previous := agentloop.TrackerWorkerCadenceToolCalls
	agentloop.TrackerWorkerCadenceToolCalls = 1
	t.Cleanup(func() { agentloop.TrackerWorkerCadenceToolCalls = previous })
	provider := &trackerScriptLLM{calls: []llm.ToolCall{
		trackerToolCall("task_create", map[string]any{"tasks": []any{map[string]any{"subject": "collect data"}}}),
		trackerReportCall("collected"),
	}}
	runtime, colonyDir := trackerTestColony(t, provider, "reminderWorkerColony")
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	dbPath := filepath.Join(colonyDir, "tracker", "tracker.db")
	if _, err := tracker.ExecuteSQL(dbPath, "CREATE TABLE jobs (id INTEGER PRIMARY KEY, done INTEGER)", 1); err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	if _, err := tracker.ExecuteSQL(dbPath, "INSERT INTO jobs (done) VALUES (0)", 1); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "fill the tracker"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	trackerWaitReports(t, runtime, ids)

	parts, err := runtime.workerConversationStore(ids[0]).ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	joined := ""
	for _, part := range parts {
		if content, _ := part["content"].(string); content != "" {
			joined += content + "\n"
		}
	}
	for _, want := range []string{"Tracker state right now:", "jobs: 1 row", "tracker_upsert"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("worker conversation missing %q:\n%s", want, joined)
		}
	}
}

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

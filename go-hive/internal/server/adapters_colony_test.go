package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
)

var (
	_ ColonyWorkerReadSurface = colonyWorkerSurface{}
	_ LiveWorkerRuntime       = colonyLiveWorkers{}
	_ SentinelStore           = sentinelStoreAdapter{}
	_ SentinelNotifier        = sentinelNotifierAdapter{}
)

// Parity-exempt: adapter mapping guard. The colony route tables read
// WorkerInfoView/LiveWorkerSnapshot rows, so the projection from a live worker
// must carry status, result and batch data rather than zero values.
func TestColonyWorkerViewProjectsLiveWorker(t *testing.T) {
	worker := colony.NewWorker("w-1", "summarize the tickets", "colony:c1")
	worker.BatchID = "batch-1"
	worker.BatchIndex = 2
	worker.BatchSize = 4
	started := time.Now().Add(-90 * time.Second)
	worker.MarkStarted(started)
	worker.Finish(colony.WorkerCompleted, colony.WorkerResult{
		WorkerID:      "w-1",
		Status:        colony.WorkerCompleted,
		Output:        map[string]any{"summary": "done", "tokens_used": float64(1234)},
		ToolCallsUsed: 7,
	})

	view := colonyWorkerInfoView(worker)
	if view.ID != "w-1" || view.Task != "summarize the tickets" {
		t.Fatalf("view identity = %#v", view)
	}
	if view.Status != colony.WorkerCompleted {
		t.Fatalf("status = %q, want %q", view.Status, colony.WorkerCompleted)
	}
	if view.Batch.BatchID != "batch-1" || view.Batch.BatchIndex != 2 || view.Batch.BatchSize != 4 {
		t.Fatalf("batch = %#v", view.Batch)
	}
	if view.Result == nil {
		t.Fatal("completed worker has no result view")
	}
	if view.Result.Summary != "done" || view.Result.TokensUsed != 1234 || view.Result.ToolCallsUsed != 7 {
		t.Fatalf("result = %#v", view.Result)
	}
	if view.Result.DurationSeconds <= 0 {
		t.Fatalf("duration = %v, want > 0", view.Result.DurationSeconds)
	}

	snapshot := colonyLiveWorkerSnapshot(worker)
	if snapshot.Status != string(colony.WorkerCompleted) || snapshot.IsActive {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.ResultStatus == nil || *snapshot.ResultStatus != string(colony.WorkerCompleted) {
		t.Fatalf("result status = %#v", snapshot.ResultStatus)
	}
	if snapshot.StartedAt <= 0 {
		t.Fatalf("started_at = %v, want epoch seconds", snapshot.StartedAt)
	}
}

// Parity-exempt: adapter mapping guard. A runtime with no workers must report
// empty reads and a zeroed stop summary, never an error or a fabricated row.
func TestColonyAdaptersReportEmptyRuntime(t *testing.T) {
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "test-agent", Name: "tester"},
		StoragePath: t.TempDir(),
		StreamID:    "colony-c1",
	})
	surface := colonyWorkerSurface{runtime: runtime}
	if workers := surface.ListWorkers(); len(workers) != 0 {
		t.Fatalf("workers = %#v, want none", workers)
	}
	if _, _, ok := surface.Worker("missing"); ok {
		t.Fatal("Worker(missing) reported found")
	}
	if surface.ColonyID() != "colony-c1" {
		t.Fatalf("colony id = %q", surface.ColonyID())
	}

	live := colonyLiveWorkers{runtime: runtime}
	if workers := live.LiveWorkers(); len(workers) != 0 {
		t.Fatalf("live workers = %#v, want none", workers)
	}
	if _, ok := live.LiveWorker("missing"); ok {
		t.Fatal("LiveWorker(missing) reported found")
	}
	summary := live.StopAllLiveWorkers()
	if summary["stopped_count"] != 0 {
		t.Fatalf("stop-all summary = %#v", summary)
	}
	if stopped, ok := summary["stopped"].([]string); !ok || len(stopped) != 0 {
		t.Fatalf("stopped = %#v, want an empty list", summary["stopped"])
	}

	// Nil runtime surfaces must stay nil so the routes take their explicit
	// fallbacks instead of a zero-valued live view.
	if colonyWorkerReadSurfaceFor(nil) != nil || liveWorkerRuntimeFor(nil) != nil {
		t.Fatal("nil runtime produced a surface")
	}
}

// Parity-exempt: adapter contract. A missing colony directory must surface as
// os.ErrNotExist, which is what the sentinel route compares against.
func TestSentinelStoreAdapterMapsMissingColony(t *testing.T) {
	root := t.TempDir()
	store := newSentinelStoreAdapter(root)

	config := store.LoadNotificationsConfig("absent")
	if config.Channel != "hive" || !config.SentinelEnabled {
		t.Fatalf("defaults = %#v, want channel hive + enabled", config)
	}

	err := store.UpdateNotificationsConfig("absent", SentinelNotificationsUpdate{SentinelEnabled: true})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}

	colonyDir := filepath.Join(root, "colonies", "c1")
	if mkErr := os.MkdirAll(colonyDir, 0o755); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}
	channel := "telegram"
	if err := store.UpdateNotificationsConfig("c1", SentinelNotificationsUpdate{
		SentinelEnabled: true,
		Channel:         &channel,
		Target:          map[string]any{"chat_id": "42"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	loaded := store.LoadNotificationsConfig("c1")
	if loaded.Channel != "telegram" {
		t.Fatalf("channel = %q, want telegram", loaded.Channel)
	}
}

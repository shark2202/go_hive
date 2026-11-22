package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
)

// The real assembly layer puts a *host.Host on Session.Colony; the adapter
// resolves it through Runtime() rather than a direct type assertion.
var _ interface {
	Runtime() *colony.ColonyRuntime
} = (*colonyhost.Host)(nil)

// hostedColonyRuntime mirrors *host.Host's shape: it is the session's
// ColonyRuntime and exposes the live colony through Runtime().
type hostedColonyRuntime struct {
	runtime *colony.ColonyRuntime
}

func (h hostedColonyRuntime) StreamID() string { return h.runtime.StreamID() }

func (h hostedColonyRuntime) SubscribeToEvents(eventTypes []eventbus.EventType, handler eventbus.EventHandler, opts ...eventbus.SubscribeOption) (string, error) {
	return h.runtime.SubscribeToEvents(eventTypes, handler, opts...)
}

func (h hostedColonyRuntime) InjectInput(workerID, content string) error {
	return h.runtime.InjectInput(workerID, content)
}

func (h hostedColonyRuntime) Runtime() *colony.ColonyRuntime { return h.runtime }

var (
	_ ColonyWorkerReadSurface = colonyWorkerSurface{}
	_ LiveWorkerRuntime       = colonyLiveWorkers{}
	_ SentinelStore           = sentinelStoreAdapter{}
	_ SentinelNotifier        = sentinelNotifierAdapter{}
)

// Parity-exempt: adapter mapping guard. The colony route tables read
// WorkerInfoView rows, so the projection from a live runtime row must carry
// status, result and batch data rather than zero values.
func TestColonyWorkerViewProjectsWorkerInfo(t *testing.T) {
	errorText := ""
	info := colony.WorkerInfo{
		WorkerID:    "w-1",
		Task:        "summarize the tickets",
		Status:      colony.WorkerCompleted,
		BatchID:     "batch-1",
		Index:       2,
		Size:        4,
		WorkerSeq:   7,
		StartedAt:   1700000000.5,
		ProfileName: "researcher",
		Result: &colony.WorkerResult{
			WorkerID:        "w-1",
			Status:          colony.WorkerCompleted,
			ReportStatus:    "success",
			Summary:         "done",
			Data:            map[string]any{"tokens_used": float64(1234)},
			Error:           &errorText,
			ToolCallsUsed:   7,
			DurationSeconds: 90,
		},
	}

	view := colonyWorkerInfoView(info)
	if view.ID != "w-1" || view.Task != "summarize the tickets" {
		t.Fatalf("view identity = %#v", view)
	}
	if view.Status != colony.WorkerCompleted || view.ProfileName != "researcher" {
		t.Fatalf("status/profile = %q/%q", view.Status, view.ProfileName)
	}
	if view.Batch.BatchID != "batch-1" || view.Batch.BatchIndex != 2 ||
		view.Batch.BatchSize != 4 || view.Batch.WorkerSeq != 7 {
		t.Fatalf("batch = %#v", view.Batch)
	}
	if view.Result == nil {
		t.Fatal("completed worker has no result view")
	}
	if view.Result.Status != "success" || view.Result.Summary != "done" ||
		view.Result.TokensUsed != 1234 || view.Result.ToolCallsUsed != 7 ||
		view.Result.DurationSeconds != 90 {
		t.Fatalf("result = %#v", view.Result)
	}
}

// Parity-exempt: adapter contract. Session.Colony holds a host wrapper in the
// real process, so the live runtime lookup must unwrap Runtime() instead of
// returning nil and falling back to disk for a running colony.
func TestSessionColonyRuntimeUnwrapsHost(t *testing.T) {
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "test-agent", Name: "tester"},
		StoragePath: t.TempDir(),
		StreamID:    "colony-hosted",
	})
	session := NewSession("session-hosted", eventbus.NewEventBus())
	session.Colony = hostedColonyRuntime{runtime: runtime}

	got := sessionColonyRuntime(session)
	if got != runtime {
		t.Fatalf("sessionColonyRuntime = %p, want the hosted runtime %p", got, runtime)
	}
	if runtime == nil {
		t.Fatal("test runtime is nil")
	}

	// A session holding the runtime directly keeps working.
	direct := NewSession("session-direct", eventbus.NewEventBus())
	direct.Colony = runtime
	if got := sessionColonyRuntime(direct); got != runtime {
		t.Fatalf("direct runtime = %p, want %p", got, runtime)
	}

	// No colony at all stays nil so the routes take their disk fallback.
	if got := sessionColonyRuntime(NewSession("session-empty", eventbus.NewEventBus())); got != nil {
		t.Fatalf("empty session runtime = %p, want nil", got)
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
	if surface.StoragePath() == "" {
		t.Fatal("storage path = \"\", want the runtime storage root")
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

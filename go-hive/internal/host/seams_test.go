package host

// Host is the object the server assembly holds, so it must carry the optional
// runtime seams the routes duck-type on Session.Colony. These assertions pin
// the forwarders; behavioural coverage follows.

import (
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/skills"
)

// colonySessionSeam mirrors server.ColonyRuntime (session.go).
type colonySessionSeam interface {
	StreamID() string
	SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error)
	InjectInput(workerID, content string) error
}

// liveWorkerSeam mirrors the runtime half of server.LiveWorkerRuntime and
// server.ColonyWorkerReadSurface, in colony's own value types.
type liveWorkerSeam interface {
	ListWorkers() []colony.WorkerInfo
	Worker(string) (colony.WorkerInfo, bool)
	LiveWorkers() []colony.LiveWorkerState
	LiveWorker(string) (colony.LiveWorkerState, bool)
	StopLiveWorker(string) (map[string]any, error)
	StopAllLiveWorkers() map[string]any
	StopWorkers(bool) (map[string]any, error)
	ColonyID() string
	StoragePath() string
	Skills() ([]*skills.ParsedSkill, bool)
	Tools() ([]colony.ColonyTool, map[string]string)
	WorkerLLM() any
	BoundWorkersForProfile(string) []string
	ActiveWorkerCount() int
	GetWorker(string) *colony.Worker
	WorkerToolTiering() []colony.WorkerToolTiering
}

var _ colonySessionSeam = (*Host)(nil)
var _ liveWorkerSeam = (*Host)(nil)

// Parity-exempt: Host is a Go assembly facade; the Python server reads
// session.colony directly, so this forwarder has no upstream counterpart.
func TestHostForwardsRuntimeSeams(t *testing.T) {
	h := newTestHost(t, &blockingLLM{}, 2)

	if got := h.StreamID(); got != "host_test_colony" {
		t.Fatalf("StreamID = %q, want host_test_colony", got)
	}
	if got := h.ColonyID(); got != h.StreamID() {
		t.Fatalf("ColonyID = %q, want the stream id", got)
	}
	if got := h.StoragePath(); got == "" {
		t.Fatal("StoragePath is empty")
	}

	run, err := h.Spawn(t.Context(), []colony.BatchTask{{Task: "blocked work", ProfileName: "slack-work"}}, colony.RunSpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	workerIDs := run.WorkerIDs()
	if len(workerIDs) != 1 {
		t.Fatalf("worker ids = %v, want 1", workerIDs)
	}

	if !eventuallyHost(t, 2*time.Second, func() bool {
		worker := h.GetWorker(workerIDs[0])
		return worker != nil && worker.AgentLoop() != nil
	}) {
		t.Fatal("worker loop never started through the host seam")
	}
	infos := h.ListWorkers()
	if len(infos) != 1 || infos[0].WorkerID != workerIDs[0] || infos[0].ProfileName != "slack-work" {
		t.Fatalf("ListWorkers = %+v, want the spawned worker", infos)
	}
	rows := h.LiveWorkers()
	if len(rows) != 1 || !rows[0].IsActive {
		t.Fatalf("LiveWorkers = %+v, want one active row", rows)
	}
	if bound := h.BoundWorkersForProfile("slack-work"); len(bound) != 1 || bound[0] != workerIDs[0] {
		t.Fatalf("BoundWorkersForProfile = %v, want [%s]", bound, workerIDs[0])
	}
	if tiers := h.WorkerToolTiering(); len(tiers) != 1 || tiers[0].WorkerID != workerIDs[0] {
		t.Fatalf("WorkerToolTiering = %+v, want the worker", tiers)
	}
	if worker := h.GetWorker(workerIDs[0]); worker == nil {
		t.Fatal("GetWorker returned nil")
	}
	if stats := h.GetStats(); stats["colony_id"] != "host_test_colony" {
		t.Fatalf("GetStats colony_id = %v, want host_test_colony", stats["colony_id"])
	}
	if err := h.InjectInput(workerIDs[0], "wrap up"); err != nil {
		t.Fatalf("InjectInput: %v", err)
	}
	if err := h.InjectInput("worker_missing", "hi"); err == nil {
		t.Fatal("InjectInput accepted an unknown worker")
	}

	subscriptionID, err := h.SubscribeToEvents([]eventbus.EventType{eventbus.EventSubagentReport}, func(*eventbus.AgentEvent) {})
	if err != nil || subscriptionID == "" {
		t.Fatalf("SubscribeToEvents = %q/%v, want a subscription", subscriptionID, err)
	}

	summary := h.StopAllLiveWorkers()
	if got := summary["stopped_count"]; got != 1 {
		t.Fatalf("StopAllLiveWorkers stopped_count = %v, want 1", got)
	}
	if !eventuallyHost(t, 5*time.Second, func() bool { return h.ActiveWorkerCount() == 0 }) {
		t.Fatal("worker still active after stop-all")
	}
	if h.DispatchBlocked() {
		t.Fatal("stop-all left dispatch blocked")
	}
}

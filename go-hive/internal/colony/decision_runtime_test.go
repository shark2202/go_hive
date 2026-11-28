package colony

// Production wiring tests for the per-worker decision ledger: the worker and
// overseer AgentContexts must carry a stream-scoped tracker over the colony's
// storage root, and a runtime with no storage root must stay unwired.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/storage"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

func decisionWiringRuntime(t *testing.T, storagePath string) *ColonyRuntime {
	t.Helper()
	return NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: storagePath,
		StreamID:    "test_colony",
	})
}

// Parity-exempt: Go wiring regression. Upstream has no unit test for
// _make_runtime_adapter, but the worker context must carry the stream runtime
// (removing the buildContext assignment fails here) and a storage-less runtime
// must stay unwired (wiring it unconditionally also fails here).
func TestColonyWorkerDecisionRuntimeWiringRequired(t *testing.T) {
	runtime := decisionWiringRuntime(t, t.TempDir())
	worker := NewWorker("worker_1", "task", "worker:worker_1")
	ctx := runtime.buildContext(worker, testAgentSpec())
	decisionRuntime, ok := ctx.Runtime.(*tracker.StreamDecisionTracker)
	if !ok || decisionRuntime == nil {
		t.Fatalf("worker context Runtime = %#v, want *tracker.StreamDecisionTracker", ctx.Runtime)
	}
	if got := decisionRuntime.StreamID(); got != "worker:worker_1" {
		t.Fatalf("worker decision stream = %q, want worker:worker_1", got)
	}

	unwired := decisionWiringRuntime(t, "")
	if ctx := unwired.buildContext(worker, testAgentSpec()); ctx.Runtime != nil {
		t.Fatalf("storage-less runtime wired a decision runtime: %#v", ctx.Runtime)
	}
	if got := unwired.decisionRuntimeFor("worker_1"); got != nil {
		t.Fatalf("storage-less decisionRuntimeFor = %v, want nil", got)
	}
}

// Parity-exempt: Go wiring regression. A run recorded through the wired worker
// context must land in the colony ledger (runs/<run_id>.json) with its
// decision/outcome intact — the ledger is only useful if the context carries
// the same storage the colony owns.
func TestColonyWorkerDecisionRuntimeLedgerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	runtime := decisionWiringRuntime(t, dir)
	worker := NewWorker("worker_1", "task", "worker:worker_1")
	ctx := runtime.buildContext(worker, testAgentSpec())
	decisionRuntime, ok := ctx.Runtime.(*tracker.StreamDecisionTracker)
	if !ok || decisionRuntime == nil {
		t.Fatalf("worker context Runtime = %#v, want *tracker.StreamDecisionTracker", ctx.Runtime)
	}

	runID := decisionRuntime.StartRun("worker_1", "goal-1", "wire a ledger", map[string]any{"task": "task"})
	decisionID := decisionRuntime.Decide(
		"worker_1",
		"pick a tool",
		[]tracker.DecisionOption{{ID: "write", Description: "Write a file"}},
		"write",
		"the task needs a file",
		tracker.DecisionParams{NodeID: "worker-node"},
	)
	if decisionID != "dec_0" {
		t.Fatalf("decision id = %q, want dec_0", decisionID)
	}
	decisionRuntime.RecordOutcome("worker_1", tracker.OutcomeRecord{
		DecisionID: decisionID,
		Success:    true,
		Result:     map[string]any{"path": "out.txt"},
		Summary:    "wrote the file",
		TokensUsed: 12,
	})
	if err := decisionRuntime.EndRun("worker_1", true, "worker finished", nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "runs", runID+".json")); err != nil {
		t.Fatalf("colony ledger missing %s: %v", runID, err)
	}
	loaded, err := storage.NewDefaultConcurrentStorage(dir).LoadRunSync(runID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRunSync(%s) = %v, %v", runID, loaded, err)
	}
	if loaded.Status != "completed" || loaded.Narrative != "worker finished" {
		t.Fatalf("loaded status/narrative = %q/%q", loaded.Status, loaded.Narrative)
	}
	if len(loaded.Decisions) != 1 || !loaded.Decisions[0].WasSuccessful() {
		t.Fatalf("loaded decisions = %+v, want one successful decision", loaded.Decisions)
	}
	if got := loaded.Decisions[0].NodeID; got != "worker-node" {
		t.Fatalf("decision node = %q, want worker-node", got)
	}
	if loaded.Metrics.TotalTokens != 12 {
		t.Fatalf("metrics.tokens = %d, want 12", loaded.Metrics.TotalTokens)
	}
}

// Parity-exempt: Go wiring pin for upstream's overseer_id convention
// (f"overseer:{stream_id}"): the persistent overseer records on
// worker:overseer:<colony>, and every worker/overseer stream shares one ledger
// backend.
func TestColonyOverseerDecisionRuntimeStreamIDAndSharedStorage(t *testing.T) {
	runtime := decisionWiringRuntime(t, t.TempDir())

	worker := NewWorker("worker_1", "task", "worker:worker_1")
	workerCtx := runtime.buildContext(worker, testAgentSpec())
	workerRuntime, ok := workerCtx.Runtime.(*tracker.StreamDecisionTracker)
	if !ok {
		t.Fatalf("worker Runtime = %#v", workerCtx.Runtime)
	}

	overseer := NewWorker("overseer", "", "overseer")
	overseer.Persistent = true
	overseerCtx := runtime.buildContext(overseer, testAgentSpec())
	overseerRuntime, ok := overseerCtx.Runtime.(*tracker.StreamDecisionTracker)
	if !ok {
		t.Fatalf("overseer Runtime = %#v", overseerCtx.Runtime)
	}
	if got := overseerRuntime.StreamID(); got != "worker:overseer:test_colony" {
		t.Fatalf("overseer decision stream = %q, want worker:overseer:test_colony", got)
	}
	if workerRuntime.Storage() != overseerRuntime.Storage() {
		t.Fatal("worker and overseer decision streams do not share the colony ledger backend")
	}
}

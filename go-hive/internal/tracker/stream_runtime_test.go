package tracker

// Stream runtime tests: per-execution lifecycle, terminal persistence, node
// context, cancellation and the decide/end_run race. Upstream has no dedicated
// stream_runtime test module (the only pinned behaviour is the adapter's
// execution_id), so the rest carry Parity-exempt notes with the upstream method
// they pin.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/storage"
)

func newTestStreamRuntime(t *testing.T, streamID string) (*StreamDecisionTracker, *storage.ConcurrentStorage, string) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewDefaultConcurrentStorage(dir)
	stream, err := NewStreamDecisionTracker(streamID, store)
	if err != nil {
		t.Fatalf("NewStreamDecisionTracker: %v", err)
	}
	return stream, store, dir
}

func testStreamDecision(stream *StreamDecisionTracker, executionID, intent string) string {
	return stream.Decide(executionID, intent, []DecisionOption{{ID: "a", Description: "Action A"}}, "a", "because", DecisionParams{})
}

// Parity-exempt: upstream stream_runtime.py has no test module; pins
// StreamDecisionTracker.start_run/end_run and the runs/<run_id>.json ledger.
func TestStreamRuntimeStartAndStop(t *testing.T) {
	stream, store, dir := newTestStreamRuntime(t, "webhook")

	runID := stream.StartRun("exec_1", "support-goal", "Handle support tickets", map[string]any{"ticket": 7})
	if len(runID) < len("run_webhook_") || runID[:len("run_webhook_")] != "run_webhook_" {
		t.Fatalf("run id = %q, want run_webhook_ prefix", runID)
	}
	run := stream.GetRun("exec_1")
	if run == nil {
		t.Fatal("GetRun(exec_1) = nil after StartRun")
	}
	if run.GoalID != "support-goal" || run.GoalDescription != "Handle support tickets" || run.Status != schemas.RunRunning {
		t.Fatalf("run = %+v", run)
	}

	if id := testStreamDecision(stream, "exec_1", "Classify ticket"); id != "dec_0" {
		t.Fatalf("decision id = %q, want dec_0", id)
	}
	stream.RecordOutcome("exec_1", OutcomeRecord{DecisionID: "dec_0", Success: true, Summary: "classified"})
	if active := stream.GetActiveExecutions(); len(active) != 1 || active[0] != "exec_1" {
		t.Fatalf("active executions = %v, want [exec_1]", active)
	}

	if err := stream.EndRun("exec_1", true, "Ticket resolved", nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	if stream.GetRun("exec_1") != nil {
		t.Fatal("run still tracked after EndRun")
	}
	if active := stream.GetActiveExecutions(); len(active) != 0 {
		t.Fatalf("active executions = %v, want empty", active)
	}

	path := filepath.Join(dir, "runs", runID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persisted run %s: %v", path, err)
	}
	loaded, err := store.LoadRunSync(runID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRunSync(%s) = %v, %v", runID, loaded, err)
	}
	if loaded.Status != schemas.RunCompleted || loaded.Narrative != "Ticket resolved" {
		t.Fatalf("loaded status/narrative = %q/%q", loaded.Status, loaded.Narrative)
	}
	if len(loaded.Decisions) != 1 || !loaded.Decisions[0].WasSuccessful() {
		t.Fatalf("loaded decisions = %+v, want one successful decision", loaded.Decisions)
	}
}

// Parity-exempt: pins upstream's execution_id-keyed run map (concurrent
// executions in one stream never share a current run or decision numbering).
func TestStreamRuntimeTracksExecutionsIndependently(t *testing.T) {
	stream, _, _ := newTestStreamRuntime(t, "webhook")
	stream.StartRun("exec_1", "goal-1", "first", nil)
	stream.StartRun("exec_2", "goal-2", "second", nil)

	if id := testStreamDecision(stream, "exec_1", "first action"); id != "dec_0" {
		t.Fatalf("exec_1 first id = %q, want dec_0", id)
	}
	if id := testStreamDecision(stream, "exec_1", "second action"); id != "dec_1" {
		t.Fatalf("exec_1 second id = %q, want dec_1", id)
	}
	if id := testStreamDecision(stream, "exec_2", "other action"); id != "dec_0" {
		t.Fatalf("exec_2 first id = %q, want dec_0", id)
	}

	if got := len(stream.GetRun("exec_1").Decisions); got != 2 {
		t.Fatalf("exec_1 decisions = %d, want 2", got)
	}
	if got := len(stream.GetRun("exec_2").Decisions); got != 1 {
		t.Fatalf("exec_2 decisions = %d, want 1", got)
	}

	if err := stream.EndRun("exec_1", true, "first done", nil); err != nil {
		t.Fatalf("EndRun exec_1: %v", err)
	}
	if stream.GetRun("exec_2") == nil {
		t.Fatal("ending exec_1 cleared exec_2's run")
	}
	if id := testStreamDecision(stream, "exec_2", "after other end"); id != "dec_1" {
		t.Fatalf("exec_2 next id = %q, want dec_1", id)
	}
}

// Parity-exempt: pins upstream's async save + finally cleanup as deterministic
// synchronous semantics: the run file exists when EndRun returns, the execution
// is detached, and a late decide returns "".
func TestStreamRuntimeEndRunPersistsAndCleansUp(t *testing.T) {
	stream, _, dir := newTestStreamRuntime(t, "webhook")
	runID := stream.StartRun("exec_1", "goal", "desc", nil)
	testStreamDecision(stream, "exec_1", "action")

	if err := stream.EndRun("exec_1", false, "failed", nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", runID+".json")); err != nil {
		t.Fatalf("run file: %v", err)
	}
	if id := testStreamDecision(stream, "exec_1", "late"); id != "" {
		t.Fatalf("late decision id = %q, want empty", id)
	}
	if err := stream.EndRun("exec_1", true, "again", nil); err != nil {
		t.Fatalf("second EndRun = %v, want nil", err)
	}
}

// Parity-exempt: cancellation semantics — stopping one execution must not
// disturb the other, and the stopped execution rejects every late write.
func TestStreamRuntimeStopCancellationLeavesOtherExecution(t *testing.T) {
	stream, store, _ := newTestStreamRuntime(t, "webhook")
	cancelledID := stream.StartRun("exec_cancelled", "goal-a", "first", nil)
	stream.StartRun("exec_live", "goal-b", "second", nil)
	testStreamDecision(stream, "exec_cancelled", "cancel me")
	testStreamDecision(stream, "exec_live", "keep me")

	if err := stream.EndRun("exec_cancelled", false, "cancelled", nil); err != nil {
		t.Fatalf("EndRun cancelled: %v", err)
	}
	// Every late write is a graceful no-op.
	if id := testStreamDecision(stream, "exec_cancelled", "late decide"); id != "" {
		t.Fatalf("cancelled decide id = %q, want empty", id)
	}
	stream.RecordOutcome("exec_cancelled", OutcomeRecord{DecisionID: "dec_0", Success: true})
	if id := stream.ReportProblem("exec_cancelled", ProblemReport{Severity: "critical", Description: "late"}); id != "" {
		t.Fatalf("cancelled problem id = %q, want empty", id)
	}
	if stream.GetRun("exec_cancelled") != nil {
		t.Fatal("cancelled execution still has a run")
	}
	// The live execution keeps recording.
	if id := testStreamDecision(stream, "exec_live", "still running"); id != "dec_1" {
		t.Fatalf("live execution next id = %q, want dec_1", id)
	}
	if stream.GetRun("exec_live") == nil {
		t.Fatal("live execution lost its run")
	}

	loaded, err := store.LoadRunSync(cancelledID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRunSync(cancelled) = %v, %v", loaded, err)
	}
	if loaded.Status != schemas.RunFailed || loaded.Narrative != "cancelled" {
		t.Fatalf("cancelled run status/narrative = %q/%q", loaded.Status, loaded.Narrative)
	}
	if len(loaded.Decisions) != 1 {
		t.Fatalf("cancelled run decisions = %d, want the one recorded before the end", len(loaded.Decisions))
	}
}

// Parity-exempt: Go concurrency pin for the decide/end_run race. Every decide
// either lands in the run that EndRun persists or returns "" — a decision can
// never be accepted into a run nobody will save.
func TestStreamRuntimeRaceEndRunVsDecisions(t *testing.T) {
	stream, store, _ := newTestStreamRuntime(t, "webhook")
	runID := stream.StartRun("exec_race", "goal", "race", nil)

	const before = 5
	for i := 0; i < before; i++ {
		if id := testStreamDecision(stream, "exec_race", "pre-race"); id == "" {
			t.Fatalf("pre-race decision %d was rejected", i)
		}
	}

	const racers = 96
	results := make([]string, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = testStreamDecision(stream, "exec_race", "race")
		}(i)
	}
	var endErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		endErr = stream.EndRun("exec_race", true, "race done", nil)
	}()
	close(start)
	wg.Wait()

	if endErr != nil {
		t.Fatalf("concurrent EndRun: %v", endErr)
	}
	loaded, err := store.LoadRunSync(runID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRunSync(%s) = %v, %v", runID, loaded, err)
	}
	accepted := 0
	seen := make(map[string]struct{}, racers)
	for _, id := range results {
		if id == "" {
			continue
		}
		accepted++
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate accepted decision id %q", id)
		}
		seen[id] = struct{}{}
	}
	if got := len(loaded.Decisions); got != accepted+before {
		t.Fatalf("persisted decisions = %d, accepted-after-race = %d, before = %d", got, accepted, before)
	}
	if late := testStreamDecision(stream, "exec_race", "very late"); late != "" {
		t.Fatalf("post-end decision id = %q, want empty", late)
	}
}

// Parity-exempt: pins upstream's dict-slot overwrite (a second start_run on one
// execution replaces the run; the replaced run is never persisted).
func TestStreamRuntimeStartReplacesExecution(t *testing.T) {
	stream, _, dir := newTestStreamRuntime(t, "webhook")
	oldID := stream.StartRun("exec_1", "old-goal", "old", nil)
	newID := stream.StartRun("exec_1", "new-goal", "new", nil)
	if oldID == newID {
		t.Fatalf("replacement run reused id %q", oldID)
	}
	run := stream.GetRun("exec_1")
	if run == nil || run.GoalID != "new-goal" {
		t.Fatalf("current run = %+v, want new-goal", run)
	}
	if active := stream.GetActiveExecutions(); len(active) != 1 || active[0] != "exec_1" {
		t.Fatalf("active executions = %v, want one exec_1 slot", active)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", oldID+".json")); !os.IsNotExist(err) {
		t.Fatalf("replaced run was persisted (stat err = %v)", err)
	}
}

// Parity-exempt: Go wiring guard. A stream with no storage backend must fail at
// construction; accepting decisions it cannot persist would lose the ledger.
func TestStreamRuntimeRequiresStorage(t *testing.T) {
	if _, err := NewStreamDecisionTracker("webhook", nil); err != ErrStreamStorageRequired {
		t.Fatalf("NewStreamDecisionTracker(nil) err = %v, want ErrStreamStorageRequired", err)
	}
	if _, err := NewStreamLedger(""); err != ErrDecisionStorageRequired {
		t.Fatalf("NewStreamLedger(\"\") err = %v, want ErrDecisionStorageRequired", err)
	}
	var unopened StreamLedger
	if _, err := unopened.NewStream("webhook"); err != ErrStreamStorageRequired {
		t.Fatalf("unopened ledger NewStream err = %v, want ErrStreamStorageRequired", err)
	}
}

// Parity-exempt: pins get_stats()'s stream_id/active_executions/execution_ids
// shape and Python's insertion order for active execution ids.
func TestStreamRuntimeGetStats(t *testing.T) {
	stream, _, _ := newTestStreamRuntime(t, "webhook")
	stream.StartRun("exec_b", "goal", "first", nil)
	stream.StartRun("exec_a", "goal", "second", nil)

	stats := stream.GetStats()
	if stats["stream_id"] != "webhook" {
		t.Fatalf("stream_id = %v, want webhook", stats["stream_id"])
	}
	if stats["active_executions"] != 2 {
		t.Fatalf("active_executions = %v, want 2", stats["active_executions"])
	}
	ids, ok := stats["execution_ids"].([]string)
	if !ok || len(ids) != 2 || ids[0] != "exec_b" || ids[1] != "exec_a" {
		t.Fatalf("execution_ids = %v, want insertion order [exec_b exec_a]", stats["execution_ids"])
	}
	ids[0] = "mutated"
	if got := stream.GetActiveExecutions()[0]; got != "exec_b" {
		t.Fatalf("GetActiveExecutions leaked its backing slice: %v", stream.GetActiveExecutions())
	}
	if err := stream.EndRun("exec_b", true, "done", nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	if active := stream.GetActiveExecutions(); len(active) != 1 || active[0] != "exec_a" {
		t.Fatalf("active after end = %v, want [exec_a]", active)
	}
}

// Parity: core/tests/test_event_loop_node.py::TestExecutionId.test_stream_runtime_adapter_exposes_execution_id
func TestStreamRuntimeAdapterExposesExecutionID(t *testing.T) {
	stream, _, _ := newTestStreamRuntime(t, "webhook")
	adapter := NewStreamRuntimeAdapter(stream, "exec_456")
	if adapter.ExecutionID() != "exec_456" {
		t.Fatalf("ExecutionID = %q, want exec_456", adapter.ExecutionID())
	}
	if adapter.CurrentRun() != nil {
		t.Fatal("CurrentRun non-nil before StartRun")
	}
	if adapter.Storage() != stream.Storage() {
		t.Fatal("adapter does not expose the stream's storage")
	}
	runID := adapter.StartRun("goal", "desc", nil)
	if runID == "" || adapter.CurrentRun() == nil {
		t.Fatalf("adapter StartRun = %q current = %v", runID, adapter.CurrentRun())
	}
}

// Parity-exempt: pins StreamRuntimeAdapter's node fallback (current_node starts
// at "unknown"; set_node feeds decide/quick_decision when no node is passed).
func TestStreamRuntimeAdapterNodeFallback(t *testing.T) {
	stream, _, _ := newTestStreamRuntime(t, "webhook")
	adapter := NewStreamRuntimeAdapter(stream, "exec_1")
	adapter.StartRun("goal", "desc", nil)

	adapter.Decide("first", []DecisionOption{{ID: "a", Description: "A"}}, "a", "test", DecisionParams{})
	if got := stream.GetRun("exec_1").Decisions[0].NodeID; got != "unknown" {
		t.Fatalf("default node = %q, want unknown", got)
	}

	adapter.SetNode("node-x")
	adapter.Decide("second", []DecisionOption{{ID: "a", Description: "A"}}, "a", "test", DecisionParams{})
	if got := stream.GetRun("exec_1").Decisions[1].NodeID; got != "node-x" {
		t.Fatalf("node after SetNode = %q, want node-x", got)
	}
	adapter.Decide("third", []DecisionOption{{ID: "a", Description: "A"}}, "a", "test", DecisionParams{NodeID: "explicit"})
	if got := stream.GetRun("exec_1").Decisions[2].NodeID; got != "explicit" {
		t.Fatalf("explicit node = %q, want explicit", got)
	}

	adapter.QuickDecision("quick", "act", "test", "")
	if got := stream.GetRun("exec_1").Decisions[3].NodeID; got != "node-x" {
		t.Fatalf("quick decision node = %q, want node-x", got)
	}
}

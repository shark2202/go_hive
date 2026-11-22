package agentloop

import (
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

func workerTrackerContext(dbPath string) *ReminderAgentContext {
	return &ReminderAgentContext{
		ColonyID: "colony_1",
		ColonyBindingProvider: func() *ColonyBinding {
			return &ColonyBinding{TrackerDB: dbPath}
		},
	}
}

func trackerFixture(t *testing.T) string {
	t.Helper()
	dbPath, err := tracker.EnsureTrackerDB(t.TempDir())
	if err != nil {
		t.Fatalf("EnsureTrackerDB: %v", err)
	}
	return dbPath
}

func seedTrackerRows(t *testing.T, dbPath string, rows int) {
	t.Helper()
	if _, err := tracker.ExecuteSQL(dbPath, "CREATE TABLE jobs (id INTEGER PRIMARY KEY, done INTEGER)", 1); err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	for i := 0; i < rows; i++ {
		if _, err := tracker.ExecuteSQL(dbPath, "INSERT INTO jobs (done) VALUES (0)", 1); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}
}

// Parity-exempt: framework/agent_loop/tracker_snapshot_reminder.py ships no
// test module, so the worker cadence and gates are re-derived from its
// documented behaviour.
func TestWorkerTrackerSnapshotCadenceAndGates(t *testing.T) {
	dbPath := trackerFixture(t)
	seedTrackerRows(t, dbPath, 2)
	source := NewWorkerTrackerSnapshotReminderSourceWithCadence(3)

	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: true, ColonyBindingProvider: func() *ColonyBinding { return &ColonyBinding{TrackerDB: dbPath} }}) {
		t.Fatal("worker tracker snapshot must not apply to the queen")
	}
	if source.AppliesTo(&ReminderAgentContext{}) {
		t.Fatal("worker tracker snapshot must not apply without a colony binding provider")
	}

	ctx := workerTrackerContext(dbPath)
	source.ObserveTurn([]string{"read_file"})
	source.ObserveTurn([]string{"web_scrape"})
	if body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatalf("fired before the cadence: %q", body)
	}
	source.ObserveTurn([]string{"read_file"})
	body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx})
	if !ok {
		t.Fatal("worker tracker snapshot did not fire at the cadence")
	}
	for _, want := range []string{"Tracker state right now:", "jobs: 2 rows", "tracker_upsert"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body = %q, missing %q", body, want)
		}
	}
	if _, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatal("the cadence was not consumed by the fire")
	}

	// A tracker-tool turn resets the cadence: the worker just persisted.
	source.ObserveTurn([]string{"read_file"})
	source.ObserveTurn([]string{"tracker_upsert"})
	source.ObserveTurn([]string{"read_file"})
	if _, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatal("a tracker-tool turn did not reset the worker cadence")
	}
}

// Parity-exempt: the worker source's empty-tracker rule has no upstream test
// module; the assertion pins it (workers fill the queen's tables, they do not
// create them).
func TestWorkerTrackerSnapshotEmptyTrackerKeepsCadence(t *testing.T) {
	dbPath := trackerFixture(t)
	source := NewWorkerTrackerSnapshotReminderSourceWithCadence(1)
	ctx := workerTrackerContext(dbPath)

	source.ObserveTurn([]string{"read_file"})
	if body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatalf("empty tracker produced a reminder: %q", body)
	}
	// The cadence was not consumed while there was nothing to show.
	seedTrackerRows(t, dbPath, 1)
	body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx})
	if !ok {
		t.Fatal("worker tracker snapshot stayed silent after the tracker gained a table")
	}
	if !strings.Contains(body, "jobs: 1 row") {
		t.Fatalf("body = %q, want the single-row table count", body)
	}
}

// Parity-exempt: framework/agent_loop/colony_worker_snapshot_reminder.py ships
// no test module; the gate matrix and render body are asserted directly.
func TestColonyWorkerSnapshotGatesAndBody(t *testing.T) {
	source := NewColonyWorkerSnapshotReminderSource()
	binding := func() *ColonyBinding { return &ColonyBinding{TrackerDB: "/tmp/unused.db"} }

	if source.AppliesTo(&ReminderAgentContext{ColonyBindingProvider: binding}) {
		t.Fatal("colony worker snapshot must not apply to a non-queen stream")
	}
	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: true}) {
		t.Fatal("colony worker snapshot must not apply without a binding provider")
	}

	ctx := &ReminderAgentContext{
		IsQueenStream:         true,
		ColonyBindingProvider: binding,
		ColonyStatsProvider:   func() ColonyStats { return ColonyStats{Active: 2, Total: 3} },
		ActiveWorkersProvider: func() []ActiveWorker {
			return []ActiveWorker{
				{ID: "w1", Status: "running", Task: "scrape rows"},
				{ID: "w2", Status: "queued", Task: "summarise"},
			}
		},
	}
	body, ok := source.Render(ReminderContext{Point: TOOL_BUDGET_CHECKPOINT, AgentCtx: ctx})
	if !ok {
		t.Fatal("colony worker snapshot did not render at the budget checkpoint")
	}
	for _, want := range []string{
		"Colony fleet: 2 active worker(s), 3 total this session.",
		"w1 [running]: scrape rows",
		"w2 [queued]: summarise",
		"Don't re-dispatch in-flight tasks",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body = %q, missing %q", body, want)
		}
	}

	if _, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatal("colony worker snapshot fired outside its tool-budget checkpoint")
	}

	empty := &ReminderAgentContext{
		IsQueenStream:         true,
		ColonyBindingProvider: binding,
		ColonyStatsProvider:   func() ColonyStats { return ColonyStats{} },
	}
	if body, ok := source.Render(ReminderContext{Point: TOOL_BUDGET_CHECKPOINT, AgentCtx: empty}); ok {
		t.Fatalf("an empty colony produced a snapshot: %q", body)
	}
}

// Parity-exempt: the TurnResetter seam is Go-only plumbing for upstream's
// direct `_stream_stall_source.reset_turn()` call.
func TestStreamStallSourceSatisfiesTurnResetter(t *testing.T) {
	var _ TurnResetter = NewStreamStallSource(1, true)
}

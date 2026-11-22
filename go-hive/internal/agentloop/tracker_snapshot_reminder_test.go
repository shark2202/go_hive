package agentloop

import (
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

func trackerSnapshotContext(dbPath string) *ReminderAgentContext {
	return &ReminderAgentContext{
		IsQueenStream: true,
		ColonyBindingProvider: func() *ColonyBinding {
			return &ColonyBinding{TrackerDB: dbPath}
		},
	}
}

// Parity-exempt: framework/agent_loop/tracker_snapshot_reminder.py has no test
// module; this pins the ported first-fire cadence and the empty-tracker body.
func TestTrackerSnapshotReminderFiresAfterFirstToolTurns(t *testing.T) {
	dir := t.TempDir()
	dbPath, err := tracker.EnsureTrackerDB(dir)
	if err != nil {
		t.Fatalf("EnsureTrackerDB: %v", err)
	}
	source := NewTrackerSnapshotReminderSourceWithConfig(TrackerSnapshotConfig{
		FirstFireTurns: 3, CooldownNoTable: 10, CooldownWithTable: 20,
	})
	ctx := trackerSnapshotContext(dbPath)
	for i := 0; i < 2; i++ {
		source.ObserveTurn([]string{"read_file"})
		if body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
			t.Fatalf("fired after %d tool turn(s): %q", i+1, body)
		}
	}
	source.ObserveTurn([]string{"read_file"})
	body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx})
	if !ok {
		t.Fatal("tracker snapshot did not fire after the first-fire threshold")
	}
	if !strings.Contains(body, "no tracker table yet") {
		t.Fatalf("body = %q, want the empty-tracker nudge", body)
	}
}

// Parity-exempt: framework/agent_loop/tracker_snapshot_reminder.py has no test
// module; this pins the table snapshot body and the with-table cooldown.
func TestTrackerSnapshotReminderReadsTablesAndCoolsDown(t *testing.T) {
	dir := t.TempDir()
	dbPath, err := tracker.EnsureTrackerDB(dir)
	if err != nil {
		t.Fatalf("EnsureTrackerDB: %v", err)
	}
	if _, err := tracker.ExecuteSQL(dbPath, "CREATE TABLE jobs (id INTEGER PRIMARY KEY, done INTEGER)", 1); err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	if _, err := tracker.ExecuteSQL(dbPath, "INSERT INTO jobs (done) VALUES (1)", 1); err != nil {
		t.Fatalf("insert first job: %v", err)
	}
	if _, err := tracker.ExecuteSQL(dbPath, "INSERT INTO jobs (done) VALUES (0)", 1); err != nil {
		t.Fatalf("insert second job: %v", err)
	}
	source := NewTrackerSnapshotReminderSourceWithConfig(TrackerSnapshotConfig{
		FirstFireTurns: 1, CooldownNoTable: 1, CooldownWithTable: 3,
	})
	ctx := trackerSnapshotContext(dbPath)
	source.ObserveTurn([]string{"read_file"})
	body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx})
	if !ok {
		t.Fatal("tracker snapshot did not fire")
	}
	for _, want := range []string{"Tracker state right now:", "jobs: 2 rows"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body = %q, missing %q", body, want)
		}
	}

	source.ObserveTurn([]string{"read_file"})
	if body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatalf("fired inside the with-table cooldown: %q", body)
	}
	source.ObserveTurn([]string{"read_file"})
	source.ObserveTurn([]string{"read_file"})
	if _, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); !ok {
		t.Fatal("tracker snapshot did not fire after the cooldown elapsed")
	}
}

// Parity: framework/agent_loop/tracker_snapshot_reminder.py::TrackerSnapshotReminderSource.applies_to
func TestTrackerSnapshotReminderIsQueenColonyOnly(t *testing.T) {
	source := NewTrackerSnapshotReminderSourceWithConfig(TrackerSnapshotConfig{FirstFireTurns: 1, CooldownNoTable: 1, CooldownWithTable: 1})
	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: false, ColonyBindingProvider: func() *ColonyBinding { return &ColonyBinding{} }}) {
		t.Fatal("tracker snapshot applies to a worker")
	}
	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: true}) {
		t.Fatal("tracker snapshot applies without a colony binding provider")
	}
	if !source.AppliesTo(trackerSnapshotContext("tracker.db")) {
		t.Fatal("tracker snapshot skipped a bound queen")
	}
	source.ObserveTurn([]string{"tracker_upsert"})
	if body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: trackerSnapshotContext("")}); ok {
		t.Fatalf("tracker-tool-only turn fired without a DB: %q", body)
	}
}

// Parity-exempt: framework/agent_loop/tracker_snapshot_reminder.py has no test
// module; pins the tracker-tool cooldown reset.
func TestTrackerSnapshotReminderTrackerUseResetsCadence(t *testing.T) {
	dir := t.TempDir()
	dbPath, err := tracker.EnsureTrackerDB(dir)
	if err != nil {
		t.Fatalf("EnsureTrackerDB: %v", err)
	}
	source := NewTrackerSnapshotReminderSourceWithConfig(TrackerSnapshotConfig{
		FirstFireTurns: 1, CooldownNoTable: 5, CooldownWithTable: 5,
	})
	ctx := trackerSnapshotContext(dbPath)
	source.ObserveTurn([]string{"read_file"})
	if _, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); !ok {
		t.Fatal("first fire did not happen")
	}
	source.ObserveTurn([]string{"read_file"})
	source.ObserveTurn([]string{"read_file"})
	source.ObserveTurn([]string{"tracker_query"})
	if body, ok := source.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatalf("tracker-tool turn did not reset the cooldown: %q", body)
	}
}

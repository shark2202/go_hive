package colony

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func strPtr(s string) *string { return &s }

// Parity: core/tests/test_colony_db.py::test_record_and_list_roundtrip
func TestRecordAndListRoundtrip(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	db := DBPath(home, "c1")
	if got := filepath.Base(db); got != "colony.db" {
		t.Fatalf("DBPath base = %q, want colony.db", got)
	}
	name := "enrich"
	if err := RecordPlaybookRun(db, PlaybookRunInput{
		RunID:        "pb_1",
		Name:         &name,
		Status:       "done",
		Dispatched:   12,
		DeadLettered: 2,
		Result:       map[string]any{"remaining": 0},
		Logs:         []string{"round 1: 12 dispatched", "converged"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	runs, err := ListPlaybookRuns(db, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	row := runs[0]
	if row.RunID != "pb_1" || row.Status != "done" || row.Dispatched != 12 || row.DeadLettered != 2 {
		t.Fatalf("row = %+v", row)
	}
	if row.Name == nil || *row.Name != "enrich" {
		t.Fatalf("name = %v, want enrich", row.Name)
	}
	if row.ResultJSON == nil || !strings.Contains(*row.ResultJSON, `"remaining": 0`) {
		t.Fatalf("result_json = %v, want python spacing", row.ResultJSON)
	}
	if row.LogsJSON == nil || !strings.Contains(*row.LogsJSON, "converged") {
		t.Fatalf("logs_json = %v, want converged", row.LogsJSON)
	}
	if row.CreatedAt == "" {
		t.Fatal("created_at is empty")
	}
}

// Parity: core/tests/test_colony_db.py::test_error_run_records_message
func TestErrorRunRecordsMessage(t *testing.T) {
	db := filepath.Join(t.TempDir(), "colony.db")
	if err := RecordPlaybookRun(db, PlaybookRunInput{
		RunID: "pb_err", Status: "error", Error: strPtr("boom"),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	runs, err := ListPlaybookRuns(db, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].Status != "error" || runs[0].Error == nil || *runs[0].Error != "boom" {
		t.Fatalf("row = %+v", runs[0])
	}
	if runs[0].Dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0", runs[0].Dispatched)
	}
}

// Parity: core/tests/test_colony_db.py::test_upsert_replaces_same_run_id
func TestUpsertReplacesSameRunID(t *testing.T) {
	db := filepath.Join(t.TempDir(), "colony.db")
	name := "a"
	if err := RecordPlaybookRun(db, PlaybookRunInput{
		RunID: "pb_x", Name: &name, Status: "error", Error: strPtr("first"),
	}); err != nil {
		t.Fatalf("record 1: %v", err)
	}
	if err := RecordPlaybookRun(db, PlaybookRunInput{
		RunID: "pb_x", Name: &name, Status: "done", Dispatched: 5,
	}); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	runs, err := ListPlaybookRuns(db, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1 (replaced, not duplicated)", len(runs))
	}
	if runs[0].Status != "done" || runs[0].Dispatched != 5 {
		t.Fatalf("row = %+v", runs[0])
	}
}

// Parity: core/tests/test_colony_db.py::test_list_empty_when_no_db
func TestListEmptyWhenNoDB(t *testing.T) {
	runs, err := ListPlaybookRuns(filepath.Join(t.TempDir(), "nope.db"), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %d, want 0", len(runs))
	}
}

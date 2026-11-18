package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Parity: core/tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_parent_directory_traversal
func TestSessionIDAndTraversal(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	id, err := s.GenerateID(time.Date(2026, 2, 6, 14, 30, 22, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(id) {
		t.Fatalf("generated id is not canonical: %s", id)
	}
	if len(id) != len("session_20260206_143022_")+8 {
		t.Fatalf("unexpected id length: %s", id)
	}
	if _, err := s.Dir("../escape"); err == nil {
		t.Fatal("traversal must be rejected")
	}
	if _, err := s.Dir(".."); err == nil {
		t.Fatal("parent must be rejected")
	}
	if _, err := s.Dir("a/b"); err == nil {
		t.Fatal("slash must be rejected")
	}
}

// Parity: core/tests/test_path_traversal_fix.py::TestPathTraversalWithActualFiles::test_save_and_load_roundtrip
func TestStateRoundTrip(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	state, dir, err := s.Create("goal-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "conversations", "parts")); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadState(state.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GoalID != "goal-1" || got.Status != StatusActive || got.SchemaVersion != "1.1" {
		t.Fatalf("unexpected state: %+v", got)
	}

	updated, err := s.UpdateState(state.SessionID, now.Add(time.Minute), func(st *State) error {
		st.Status = StatusPaused
		st.Progress.Percent = 42
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusPaused || updated.Progress.Percent != 42 {
		t.Fatalf("update lost: %+v", updated)
	}
	if !updated.Timestamps.UpdatedAt.After(updated.Timestamps.CreatedAt) {
		t.Fatal("updated_at not advanced")
	}
}

// Parity-exempt: exercises the goal-session Store's status filter and
func TestListFiltersAndSorts(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	a, _, err := s.Create("g1", base)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.Create("g2", base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(a.SessionID, base, func(st *State) error {
		st.Status = StatusCompleted
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(b.SessionID, base, func(st *State) error {
		st.Status = StatusPaused
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.List("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].SessionID != b.SessionID {
		t.Fatalf("expected b then a, got %+v", all)
	}
	paused, _ := s.List(StatusPaused, 0)
	if len(paused) != 1 || paused[0].SessionID != b.SessionID {
		t.Fatalf("status filter failed: %+v", paused)
	}
}

// Parity-exempt: Store state validation is a Go-only guard; Python validates
func TestStateValidation(t *testing.T) {
	s := NewStore(t.TempDir())
	_, _, err := s.Create("", time.Now())
	if err == nil {
		t.Fatal("empty goal must fail")
	}
	bad := &State{SessionID: "x", GoalID: "g", Status: "bogus"}
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid status must fail")
	}
	bad.Status = StatusActive
	if err := bad.Validate(); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_write_and_read_parts_in_order
// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_corrupt_part_skipped_on_read
func TestConversationParts(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	state, _, err := s.Create("g", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conv := s.Conversations(state.SessionID)
	for i := 0; i < 3; i++ {
		if err := conv.AddPart(i, map[string]any{"role": "user", "text": "hi", "seq": i}); err != nil {
			t.Fatal(err)
		}
	}
	parts, err := conv.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	if parts[0]["seq"].(float64) != 0 || parts[2]["seq"].(float64) != 2 {
		t.Fatalf("order broken: %+v", parts)
	}
	next, _ := conv.NextSeq()
	if next != 3 {
		t.Fatalf("next seq = %d", next)
	}
	partsDir := filepath.Join(s.Conversations(state.SessionID).base, "parts")
	if err := os.WriteFile(filepath.Join(partsDir, "0000000001.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	parts, err = conv.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("corrupt part not skipped: %d", len(parts))
	}
}

// Parity-exempt: cursor and partial-result persistence has no Python pytest.
func TestCursorAndPartials(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "sessions"))
	state, _, err := s.Create("g", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conv := s.Conversations(state.SessionID)
	cur := Cursor{Iteration: 4, Turn: 2, AccumulatedOutput: "abc", ToolCalls: 7, StallRepeat: 1}
	if err := conv.WriteCursor(cur); err != nil {
		t.Fatal(err)
	}
	got, err := conv.ReadCursor()
	if err != nil {
		t.Fatal(err)
	}
	if got.Iteration != 4 || got.AccumulatedOutput != "abc" || got.ToolCalls != 7 {
		t.Fatalf("cursor lost: %+v", got)
	}
	if err := conv.WritePartial("call/1", map[string]any{"text": "partial"}); err != nil {
		t.Fatal(err)
	}
	p, err := conv.ReadPartial("call/1")
	if err != nil {
		t.Fatal(err)
	}
	if p["text"] != "partial" {
		t.Fatalf("partial lost: %+v", p)
	}
	if err := conv.DeletePartial("call/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.ReadPartial("call/1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial not deleted: %v", err)
	}
	conv2 := s.Conversations(state.SessionID + "x")
	_ = conv2
}

// Parity-exempt: the Go checkpoint store has no Python pytest counterpart.
func TestCheckpointStore(t *testing.T) {
	dir := t.TempDir()
	cs := NewCheckpointStore(dir)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if err := cs.Save(Checkpoint{ID: "cp1", Type: "node", Node: "node_a", CreatedAt: now, Data: map[string]any{"x": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := cs.Save(Checkpoint{ID: "cp2", Type: "node", Node: "node_b", CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	idx, err := cs.Index()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 {
		t.Fatalf("index = %v", idx)
	}
	if idx[0] > idx[1] {
		t.Fatalf("index not sorted: %v", idx)
	}
}

// Parity-exempt: part filename parsing is a Go-only helper with no Python test.
func TestParsePartSeq(t *testing.T) {
	if n, ok := ParsePartSeq("0000000042.json"); !ok || n != 42 {
		t.Fatalf("got %d %v", n, ok)
	}
	if _, ok := ParsePartSeq("bad.json"); ok {
		t.Fatal("expected failure")
	}
}

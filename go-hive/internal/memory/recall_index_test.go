package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readRecallIndexJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("Unmarshal %s: %v", path, err)
	}
	return document
}

// Parity: tools/src/memory_tools/turn_index.py::connect (one index per
// scope/owner) and tools/src/memory_tools/paths.py::turn_index_path.
func TestRecallIndexIsPerOwner(t *testing.T) {
	home := t.TempDir()
	writePastRecallSession(t, home, "session_20230522_211800_aaaa0001", [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time."},
	})
	writePastRecallSession(t, home, "session_20230524_143200_aaaa0002", [][2]string{
		{"What can individuals do to help coral reef conservation?", "Reduce runoff..."},
	})

	ref := pastRecallQueenRef()
	path := RecallIndexPath(home, ref)
	if !strings.HasSuffix(path, filepath.Join(".message_index", "recall", "queens", "queen_test.json")) {
		t.Fatalf("index path = %s", path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the index must not exist before the first query")
	}

	turns := RecallPastConversations(home, ref, "How long is my daily commute to work?", PastRecallOptions{})
	if len(turns) != 1 || turns[0].Session != "session_20230522_211800_aaaa0001" {
		t.Fatalf("turns = %+v, want the commute session", turns)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the first query must build the index: %v", err)
	}

	other := ScopeRef{Scope: SearchScopeQueens, Owner: "queen_other"}
	if otherPath := RecallIndexPath(home, other); otherPath == path {
		t.Fatalf("two owners share one index path: %s", path)
	}
	document := readRecallIndexJSON(t, path)
	if document["version"] != float64(RecallIndexVersion) {
		t.Fatalf("index version = %v, want %d", document["version"], RecallIndexVersion)
	}
	sessions, _ := document["sessions"].(map[string]any)
	if len(sessions) != 2 {
		t.Fatalf("indexed sessions = %d, want 2", len(sessions))
	}
	for name, raw := range sessions {
		entry, _ := raw.(map[string]any)
		size, _ := entry["events_size"].(float64)
		info, err := os.Stat(filepath.Join(home, "queens", "queen_test", "sessions", name, "events.jsonl"))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if int64(size) != info.Size() {
			t.Fatalf("indexed events_size for %s = %v, want %d", name, size, info.Size())
		}
	}
}

// Parity: tools/src/memory_tools/turn_index.py::refresh (a session whose
// events.jsonl changed is re-read; unchanged sessions stay indexed).
func TestRecallIndexRefreshPicksUpChangedSessions(t *testing.T) {
	home := t.TempDir()
	first := "session_20230601_090000_aaaa0003"
	writePastRecallSession(t, home, first, [][2]string{
		{"My kayak is stored in the garage.", "Noted."},
	})
	ref := pastRecallQueenRef()
	before := RecallPastConversations(home, ref, "Where is my kayak?", PastRecallOptions{})
	if len(before) != 1 {
		t.Fatalf("initial turns = %+v", before)
	}

	// A new session joins the store: a warm index must pick it up.
	second := "session_20230602_090000_aaaa0004"
	writePastRecallSession(t, home, second, [][2]string{
		{"My climbing harness is in the blue bin.", "Noted."},
	})
	turns := RecallPastConversations(home, ref, "Where is my climbing harness?", PastRecallOptions{})
	if len(turns) != 1 || turns[0].Session != second {
		t.Fatalf("turns = %+v, want the new session", turns)
	}

	// An existing session grows: the changed size forces a re-read.
	writePastRecallSession(t, home, first, [][2]string{
		{"My kayak is stored in the garage.", "Noted."},
		{"My kayak paddle is a red Werner.", "Noted."},
	})
	turns = RecallPastConversations(home, ref, "What colour is my kayak paddle?", PastRecallOptions{})
	found := false
	for _, turn := range turns {
		if turn.Session == first && strings.Contains(turn.User, "Werner") {
			found = true
		}
	}
	if !found {
		t.Fatalf("turns = %+v, want the grown session's paddle turn", turns)
	}
}

// Parity: tools/src/memory_tools/recall.py::recall (a deleted index is
// rebuilt with the same results; it is derived, not a record).
func TestRecallIndexDeletionRebuildsWithSameResults(t *testing.T) {
	home := t.TempDir()
	writePastRecallSession(t, home, "session_20230522_211800_aaaa0001", [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time."},
	})
	ref := pastRecallQueenRef()
	first := RecallPastConversations(home, ref, "How long is my daily commute to work?", PastRecallOptions{})
	if len(first) != 1 {
		t.Fatalf("first turns = %+v", first)
	}

	path := RecallIndexPath(home, ref)
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove index: %v", err)
	}
	rebuilt := RecallPastConversations(home, ref, "How long is my daily commute to work?", PastRecallOptions{})
	if len(rebuilt) != 1 || rebuilt[0].Session != first[0].Session {
		t.Fatalf("rebuilt turns = %+v, want %+v", rebuilt, first)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the deleted index must be rebuilt: %v", err)
	}
}

// Parity: core/framework/agent_loop/past_conversations_reminder.py::RECALL_TIMEOUT_S
// and agent_loop.py (the turn proceeds without excerpts while the build keeps
// going in its thread).
func TestRecallIndexColdBuildRespectsDeadline(t *testing.T) {
	home := t.TempDir()
	writePastRecallSession(t, home, "session_20230522_211800_aaaa0001", [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time."},
	})
	ref := pastRecallQueenRef()

	started := time.Now()
	turns := RecallPastConversations(home, ref, "How long is my daily commute to work?", PastRecallOptions{Timeout: time.Nanosecond})
	if len(turns) != 0 {
		t.Fatalf("a cold build past the deadline must miss: %+v", turns)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the query waited %s past a 1ns deadline", elapsed)
	}

	// The build survives the query: the next turn reads it.
	path := RecallIndexPath(home, ref)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background build never produced the index")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ready := RecallPastConversations(home, ref, "How long is my daily commute to work?", PastRecallOptions{})
	if len(ready) != 1 {
		t.Fatalf("turns after the build = %+v", ready)
	}
}

// Parity: tools/src/memory_tools/turn_index.py::refresh (the index covers
// every session, not a recent-session window).
func TestRecallIndexCoversEverySession(t *testing.T) {
	home := t.TempDir()
	oldest := "session_20200101_090000_zzzz0000"
	writePastRecallSession(t, home, oldest, [][2]string{
		{"My first kayak was a red Tempest 170.", "Noted."},
	})
	for i := 0; i < 205; i++ {
		session := "session_2024" + twoDigits(i%12+1) + twoDigits(i%28+1) + "_090000_yyyy" + fourDigits(i)
		writePastRecallSession(t, home, session, [][2]string{
			{"Routine planning note " + itoa(i) + ": errands and groceries.", "Sounds fine."},
		})
	}

	turns := RecallPastConversations(home, pastRecallQueenRef(), "What was my first kayak?", PastRecallOptions{})
	if len(turns) != 1 || turns[0].Session != oldest {
		t.Fatalf("turns = %+v, want the oldest session", turns)
	}
}

package session

import (
	"os"
	"path/filepath"
	"testing"
)

func makeColdSessionDir(t *testing.T, root, queenID, sessionID string) {
	t.Helper()
	dir := filepath.Join(root, queenID, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func markColdSessionWithMessages(t *testing.T, sessionDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(sessionDir, "conversations"), 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{"message_count": 1, "last_message": "hi", "last_active_at": 5.0}`
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Parity-exempt: 上游 SessionManager.list_colony_sessions 无独立测试。与
func TestListColonySessionsKeepsColonyForks(t *testing.T) {
	queensRoot := filepath.Join(t.TempDir(), "colonies", "acme", "queens")
	dir := filepath.Join(queensRoot, "queen_x", "sessions", "session_20260514_120000_aaaaaaaa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"),
		[]byte(`{"colony_fork": {"session_id": "s0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	results := ListColonySessions(queensRoot)
	if len(results) != 1 {
		t.Fatalf("ListColonySessions = %v, want the colony_fork entry kept", results)
	}
	if results[0]["session_id"] != "session_20260514_120000_aaaaaaaa" {
		t.Fatalf("session_id = %v", results[0]["session_id"])
	}

	if got := ListColonySessions(filepath.Join(t.TempDir(), "missing")); len(got) != 0 {
		t.Fatalf("unknown colony = %v, want empty", got)
	}
}

// Parity-exempt: 上游 SessionManager.get_colony_active_session 无独立测试。
func TestColonyActiveSessionSkipsEmptySessions(t *testing.T) {
	queensRoot := filepath.Join(t.TempDir(), "queens")
	empty := filepath.Join(queensRoot, "queen_x", "sessions", "session_20260516_120000_bbbbbbbb")
	withMsgs := filepath.Join(queensRoot, "queen_x", "sessions", "session_20260515_120000_cccccccc")
	for _, dir := range []string{empty, withMsgs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	markColdSessionWithMessages(t, withMsgs)

	active := ColonyActiveSession(queensRoot)
	if active == nil {
		t.Fatal("ColonyActiveSession = nil, want the session with messages")
	}
	if active["session_id"] != "session_20260515_120000_cccccccc" {
		t.Fatalf("active session = %v, want the one with messages", active["session_id"])
	}
	if active["has_messages"] != true {
		t.Fatalf("has_messages = %v, want true", active["has_messages"])
	}

	if got := ColonyActiveSession(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("empty tree active = %v, want nil", got)
	}
}

// Parity: core/tests/test_session_listing_order.py::test_list_cold_sessions_is_strict_newest_created_first
func TestListColdSessionsIsStrictNewestCreatedFirst(t *testing.T) {
	queensDir := t.TempDir()
	ids := []string{
		"session_20260514_120000_aaaaaaaa",
		"session_20260516_090000_cccccccc",
		"session_20260515_080000_bbbbbbbb",
		"session_20260514_120000_dddddddd",
	}
	for _, sessionID := range ids {
		makeColdSessionDir(t, queensDir, "queen_test", sessionID)
	}

	results := ListColdSessions(queensDir)
	got := make([]string, 0, len(results))
	for _, row := range results {
		got = append(got, coldString(row["session_id"]))
	}

	want := []string{
		"session_20260516_090000_cccccccc",
		"session_20260515_080000_bbbbbbbb",
		"session_20260514_120000_dddddddd",
		"session_20260514_120000_aaaaaaaa",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Parity: core/tests/test_session_listing_order.py::test_list_cold_sessions_spans_multiple_queens_in_order
func TestListColdSessionsSpansMultipleQueensInOrder(t *testing.T) {
	queensDir := t.TempDir()
	makeColdSessionDir(t, queensDir, "queen_a", "session_20260510_100000_11111111")
	makeColdSessionDir(t, queensDir, "queen_b", "session_20260517_100000_22222222")
	makeColdSessionDir(t, queensDir, "queen_a", "session_20260512_100000_33333333")

	results := ListColdSessions(queensDir)
	got := make([]string, 0, len(results))
	for _, row := range results {
		got = append(got, coldString(row["session_id"]))
	}

	want := []string{
		"session_20260517_100000_22222222",
		"session_20260512_100000_33333333",
		"session_20260510_100000_11111111",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

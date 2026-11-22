package longmemeval_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/longmemeval"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

// Parity-exempt: proves the seed writes the events.jsonl shape the Go
// runtime's own search_messages reads (upstream test parity lives in
// tests/e2e/test_longmemeval.py::test_longmemeval, which needs a live model).
func TestSeedHaystackWritesSearchableGoSessions(t *testing.T) {
	items, err := longmemeval.ReadItems(filepath.Join("testdata", "fixture_cleaned.json"))
	if err != nil {
		t.Fatalf("ReadItems: %v", err)
	}
	home := t.TempDir()
	seeded, err := longmemeval.SeedHaystack(home, "queen_technology", items[0])
	if err != nil {
		t.Fatalf("SeedHaystack: %v", err)
	}
	if seeded.Sessions != 2 || len(seeded.Mapping) != 2 {
		t.Fatalf("seeded = %+v", seeded)
	}
	hiveID := seeded.Mapping["fixture_s1"]
	if !strings.HasPrefix(hiveID, "session_20230518_081200_") {
		t.Fatalf("hive session id = %q, want the session's own date", hiveID)
	}
	sessionDir := filepath.Join(home, "queens", "queen_technology", "sessions", hiveID)
	data, err := os.ReadFile(filepath.Join(sessionDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("events = %d lines, want 2", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["type"] != "client_input_received" || first["stream_id"] != "queen" || first["node_id"] != "queen" {
		t.Fatalf("first event = %v", first)
	}
	if first["execution_id"] != hiveID || first["seq"] != float64(1) {
		t.Fatalf("first event identity = %v", first)
	}
	meta, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), `"phase":"independent"`) || !strings.Contains(string(meta), `"queen_id":"queen_technology"`) {
		t.Fatalf("meta = %s", meta)
	}

	// The storage-format proof: the ported memory search finds the needle.
	ref := memory.ScopeRef{Scope: memory.SearchScopeQueens, Owner: "queen_technology"}
	result, toolErr := memory.SearchMessages(home, ref, memory.SearchMessagesOptions{Pattern: "45 minutes"})
	if toolErr != nil {
		t.Fatalf("SearchMessages: %v", toolErr)
	}
	if result.TotalMatches != 1 || len(result.Matches) != 1 {
		t.Fatalf("matches = %+v", result)
	}
	match := result.Matches[0]
	if match.Session != hiveID {
		t.Fatalf("match session = %q, want %q", match.Session, hiveID)
	}
	if match.SessionStartedAt == nil || !strings.HasPrefix(*match.SessionStartedAt, "2023-05-18") {
		t.Fatalf("session_started_at = %v", match.SessionStartedAt)
	}
}

// Parity-exempt: content-less haystack sessions are skipped, as upstream's
// seed_haystack does.
func TestSeedHaystackSkipsEmptySessions(t *testing.T) {
	item := longmemeval.Item{
		QuestionID:         "fixture_empty",
		QuestionType:       "single-session-user",
		Question:           "q",
		Answer:             longmemeval.TextAnswer("a"),
		QuestionDate:       "2023/05/20 (Sat) 10:00",
		HaystackSessionIDs: []string{"empty", "full"},
		HaystackDates:      []string{"2023/05/18 (Thu) 08:12", "2023/05/19 (Fri) 09:00"},
		HaystackSessions: [][]longmemeval.Turn{
			{{Role: "user", Content: "  "}},
			{{Role: "user", Content: "hello"}},
		},
		AnswerSessionIDs: []string{"full"},
	}
	seeded, err := longmemeval.SeedHaystack(t.TempDir(), "queen_technology", item)
	if err != nil {
		t.Fatalf("SeedHaystack: %v", err)
	}
	if seeded.Sessions != 1 || seeded.Skipped != 1 {
		t.Fatalf("seeded = %+v", seeded)
	}
	if _, ok := seeded.Mapping["empty"]; ok {
		t.Fatal("empty session was mapped")
	}
}

// Parity-exempt: a traversing queen id must be refused before any path join.
func TestSeedHaystackRejectsUnsafeQueen(t *testing.T) {
	item := longmemeval.Item{
		QuestionID:         "fixture_x",
		QuestionType:       "single-session-user",
		Question:           "q",
		QuestionDate:       "2023/05/20 (Sat) 10:00",
		HaystackSessionIDs: []string{"s1"},
		HaystackDates:      []string{"2023/05/18 (Thu) 08:12"},
		HaystackSessions:   [][]longmemeval.Turn{{{Role: "user", Content: "hi"}}},
	}
	if _, err := longmemeval.SeedHaystack(t.TempDir(), "../escape", item); err == nil {
		t.Fatal("traversing queen id was accepted")
	}
}

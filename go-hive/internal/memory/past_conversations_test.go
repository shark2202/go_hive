package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const pastRecallTestQueen = "queen_test"

func writePastRecallSession(t *testing.T, homeRoot, session string, turns [][2]string) string {
	t.Helper()
	dir := filepath.Join(homeRoot, "queens", pastRecallTestQueen, "sessions", session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	var builder strings.Builder
	for i, turn := range turns {
		user, _ := json.Marshal(map[string]any{
			"type": "client_input_received",
			"data": map[string]any{"content": turn[0]},
		})
		builder.Write(user)
		builder.WriteByte('\n')
		if turn[1] != "" {
			assistant, _ := json.Marshal(map[string]any{
				"type": "client_output_delta",
				"data": map[string]any{"snapshot": turn[1], "iteration": i, "inner_turn": 0},
			})
			builder.Write(assistant)
			builder.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("WriteFile events.jsonl: %v", err)
	}
	return dir
}

func pastRecallQueenRef() ScopeRef {
	return ScopeRef{Scope: SearchScopeQueens, Owner: pastRecallTestQueen}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_query_terms_drop_scaffolding_and_stem
func TestPastRecallQueryTermsDropScaffoldingAndStem(t *testing.T) {
	got := PastRecallQueryTerms("How many weddings have I attended in the past few months?")
	want := []string{"wedding", "attend"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query terms = %v, want %v", got, want)
	}
	if terms := PastRecallQueryTerms("thanks!"); len(terms) != 0 {
		t.Fatalf("query terms = %v, want empty", terms)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_query_terms_skip_the_envelope_and_cut_chinese_into_pairs
func TestPastRecallQueryTermsSkipEnvelopeAndCutChinesePairs(t *testing.T) {
	stamped := "[2026-10-17 22:47 Pacific Daylight Time] [Ann(10001)]: 睡了吗"
	if got, want := PastRecallQueryTerms(stamped), []string{"睡了"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stamped terms = %v, want %v", got, want)
	}
	group := "[群里最近的对话]\nCy(10002): 你好\n\n[Ann(10001)]: RTX 4090用着顺手吗"
	want := []string{"rtx", "4090", "用着", "着顺", "顺手", "手吗"}
	if got := PastRecallQueryTerms(group); !reflect.DeepEqual(got, want) {
		t.Fatalf("group terms = %v, want %v", got, want)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_recall_finds_the_turn_that_answers
func TestPastRecallFindsTheTurnThatAnswers(t *testing.T) {
	home := t.TempDir()
	writePastRecallSession(t, home, "session_20230522_211800_aaaa0001", [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time. Here are some fiction picks..."},
	})
	writePastRecallSession(t, home, "session_20230524_143200_aaaa0002", [][2]string{
		{"What can individuals do to help coral reef conservation?", "Reduce runoff..."},
	})

	turns := RecallPastConversations(home, pastRecallQueenRef(), "How long is my daily commute to work?", PastRecallOptions{})
	if len(turns) != 1 || turns[0].Session != "session_20230522_211800_aaaa0001" {
		t.Fatalf("turns = %+v, want the commute session", turns)
	}
	block := RenderPastConversationExcerpts(turns, "How long is my daily commute to work?")
	if !strings.Contains(block, "2023-05-22 (Mon)") {
		t.Fatalf("block missing the dated header: %q", block)
	}
	if !strings.Contains(block, "45 minutes each way") {
		t.Fatalf("block missing the answer snippet: %q", block)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_recall_excludes_the_live_session_and_stays_quiet_when_nothing_relates
func TestPastRecallExcludesLiveSessionAndStaysQuiet(t *testing.T) {
	home := t.TempDir()
	live := "session_20230601_090000_aaaa0003"
	writePastRecallSession(t, home, live, [][2]string{
		{"My commute is brutal today.", "Sorry to hear that."},
	})
	if turns := RecallPastConversations(home, pastRecallQueenRef(), "Remind me how long my commute is", PastRecallOptions{ExcludeSession: live}); len(turns) != 0 {
		t.Fatalf("turns = %+v, want the live session excluded", turns)
	}
	if block := PastConversationRecallBlock(home, pastRecallQueenRef(), "Write a haiku about autumn leaves", ""); block != "" {
		t.Fatalf("block = %q, want empty when nothing relates", block)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_one_shared_common_word_is_not_enough
func TestPastRecallOneSharedCommonWordIsNotEnough(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 60; i++ {
		session := "session_202305" + twoDigits(i%28+1) + "_1000" + twoDigits(i) + "_bbbb" + fourDigits(i)
		writePastRecallSession(t, home, session, [][2]string{
			{"Planning day " + itoa(i) + ": errands, groceries, laundry.", "Sounds good."},
		})
	}
	if turns := RecallPastConversations(home, pastRecallQueenRef(), "Planning to buy a telescope?", PastRecallOptions{}); len(turns) != 0 {
		t.Fatalf("turns = %+v, want no match from one shared common word", turns)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_recall_finds_the_turn_that_answers
func TestPastRecallCapsExcerptsAtSix(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 9; i++ {
		session := "session_2023060" + itoa(i+1) + "_0900" + twoDigits(i) + "_cccc" + fourDigits(i)
		writePastRecallSession(t, home, session, [][2]string{
			{"My neighbour's beehive inspection happened on day " + itoa(i) + ".", "Noted."},
		})
	}
	turns := RecallPastConversations(home, pastRecallQueenRef(), "When was the beehive inspection?", PastRecallOptions{})
	if len(turns) == 0 {
		t.Fatal("no recall for an exact multi-term query")
	}
	if len(turns) > PastRecallTopK {
		t.Fatalf("excerpts = %d, want at most %d", len(turns), PastRecallTopK)
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func fourDigits(n int) string {
	text := itoa(n)
	for len(text) < 4 {
		text = "0" + text
	}
	return text
}

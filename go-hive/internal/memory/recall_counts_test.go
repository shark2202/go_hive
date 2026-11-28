package memory

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// referenceRecall builds the scoring docs the uncached way — the exact term
// counts recall used before the memo (pastRecallTurnCounts) in the same sorted
// form — and hands them to the shared tail. It is the oracle the cached path
// must match turn-for-turn, score-for-score.
//
// Parity: tools/src/memory_tools/recall.py::recall (uncached reference form;
// the memo only skips repeated work).
func referenceRecall(homeRoot string, ref ScopeRef, text, exclude string, topK int) []PastTurn {
	terms := PastRecallQueryTerms(text)
	if len(terms) == 0 {
		return nil
	}
	index, ok := recallIndexForQuery(homeRoot, ref, exclude, time.Now().Add(PastRecallTimeout))
	if !ok || index == nil || len(index.Turns) == 0 {
		return nil
	}
	docs := make([]pastRecallDoc, 0, len(index.Turns))
	for _, turn := range index.Turns {
		if turn.Session == exclude {
			continue
		}
		docs = append(docs, pastRecallDoc{
			turn:  PastTurn{Session: turn.Session, Ordinal: turn.Ordinal, User: turn.User, Reply: turn.Reply},
			freqs: recallTermFreqsFromTurn(turn.User, turn.Reply),
		})
	}
	return pastRecallChoose(docs, len(docs), terms, exclude, topK)
}

func recallCacheSessionsForTest(homeRoot string, ref ScopeRef) map[string]bool {
	value, ok := recallCountsCaches.Load(recallCountsHome{home: homeRoot, scope: ref.Scope, owner: ref.Owner})
	if !ok {
		return nil
	}
	cache := value.(*recallCountsOwnerCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	out := map[string]bool{}
	for session := range cache.sessions {
		out[session] = true
	}
	return out
}

// pastRecallFrequencyMap is the frequency lookup recall used before the memo:
// an exact key match for CJK terms, a prefix sum over the whole count map
// otherwise. The memo's sorted-slice lookup must agree for every term.
func pastRecallFrequencyMap(counts map[string]int, term string) int {
	if pastRecallCJKTerm.MatchString(term) {
		return counts[term]
	}
	total := 0
	for word, count := range counts {
		if strings.HasPrefix(word, term) {
			total += count
		}
	}
	return total
}

// Parity: tools/src/memory_tools/recall.py::recall (a per-turn count used for
// scoring is the same whether it comes from the map or the sorted memo form).
func TestRecallTermFreqsMatchMapFrequency(t *testing.T) {
	turns := [][2]string{
		{"I've been listening to audiobooks on my commute, 45 minutes each way.", "That's a great use of the time."},
		{"running runner runs run, refund refunded refunding refunds.", "database databases data 1234 56789"},
		{"数据库迁移的验收标准是什么？", "迁移计划分两步，第一步是数据校验。"},
		{"don't didn't it's", ""},
	}
	terms := []string{
		"refund", "refunding", "refunds", "run", "runn", "runner", "ru",
		"data", "database", "1234", "don", "don't",
		"数据库", "库迁", "迁移", "数据", "验收", "不存在的词",
	}
	for _, turn := range turns {
		counts := pastRecallTurnCounts(turn[0], turn[1])
		freqs := recallTermFreqsFromTurn(turn[0], turn[1])
		for _, term := range terms {
			want := pastRecallFrequencyMap(counts, term)
			if got := pastRecallFrequency(freqs, term); got != want {
				t.Fatalf("turn %.30q term %q: sorted freq = %d, map freq = %d", turn[0], term, got, want)
			}
		}
	}
}

// Parity: tools/src/memory_tools/turn_index.py::_put_turn (BM25's document
// length is len(user)*2 + len(reply) characters — Python str length, so
// Unicode code points — not the turn's token total. The scorer's denominator
// must use the same unit or a long CJK turn weighs differently on each side.)
func TestRecallDocumentLengthMatchesUpstreamCharacters(t *testing.T) {
	cases := []struct {
		user  string
		reply string
		want  int
	}{
		{"refund refund policy", "refund", 46},
		{"数据库迁移", "两步", 12},
		{"don't didn't it's", "", 34},
		{"I've been listening to audiobooks on my commute, 45 minutes each way.", "That's a great use of the time.", 169},
	}
	for _, tc := range cases {
		if got := recallDocumentLength(tc.user, tc.reply); got != tc.want {
			t.Fatalf("recallDocumentLength(%q, %q) = %d, want %d", tc.user, tc.reply, got, tc.want)
		}
		freqs := recallTermFreqsFromTurn(tc.user, tc.reply)
		if freqs.length != tc.want {
			t.Fatalf("turn %q: BM25 length = %d, want %d characters", tc.user, freqs.length, tc.want)
		}
	}

	// The same turn's token total is deliberately different: 7 tokens but 46
	// characters. Pinning the character count catches a fallback to sum(tf).
	user, reply := "refund refund policy", "refund"
	freqs := recallTermFreqsFromTurn(user, reply)
	if freqs.length == 7 {
		t.Fatalf("BM25 length = %d, want the character count", freqs.length)
	}
}

// Parity: tools/src/memory_tools/recall.py::recall (per-turn term counts are
// identical whether kept in the memo or recomputed; the memo must keep the
// reference's user-twice/reply-once weighting exactly).
func TestRecallTurnFreqsCacheMatchesRecompute(t *testing.T) {
	home := t.TempDir()
	ref := pastRecallQueenRef()
	turns := []recallIndexTurn{
		{Session: "s1", Ordinal: 3, User: "I've been listening to audiobooks on my commute, 45 minutes each way.", Reply: "That's a great use of the time."},
		{Session: "s1", Ordinal: 5, User: "数据库迁移的验收标准是什么？", Reply: "迁移计划分两步，第一步是数据校验。"},
		{Session: "s2", Ordinal: 1, User: "12 34 56789 don't didn't", Reply: ""},
	}
	hits := recallCountsHits.Load()
	misses := recallCountsMisses.Load()
	for _, turn := range turns {
		want := recallTermFreqsFromTurn(turn.User, turn.Reply)
		freqs := recallTurnFreqsFor(home, ref, 42, turn.Session, turn.Ordinal, turn.User, turn.Reply)
		if !reflect.DeepEqual(freqs, want) {
			t.Fatalf("turn %s/%d freqs = %+v, want %+v", turn.Session, turn.Ordinal, freqs, want)
		}
		// Second call: same turn served from the memo, still identical.
		if again := recallTurnFreqsFor(home, ref, 42, turn.Session, turn.Ordinal, turn.User, turn.Reply); !reflect.DeepEqual(again, want) {
			t.Fatalf("cached turn %s/%d freqs = %+v, want %+v", turn.Session, turn.Ordinal, again, want)
		}
	}
	if got := recallCountsMisses.Load() - misses; got != uint64(len(turns)) {
		t.Fatalf("memo misses = %d, want %d", got, len(turns))
	}
	if got := recallCountsHits.Load() - hits; got != uint64(len(turns)) {
		t.Fatalf("memo hits = %d, want %d", got, len(turns))
	}

	// The reference counts the user text twice and the reply once
	// (pastRecallTurnCounts); the memo must reproduce that exact shape.
	// Its BM25 length is the reference's character count, not the token
	// total (7 tokens here): len(user)*2 + len(reply) = 20*2 + 6.
	freqs := recallTurnFreqsFor(home, ref, 1, "s-quirk", 0, "refund refund policy", "refund")
	if got := pastRecallFrequency(freqs, "refund"); got != 5 || freqs.length != 46 {
		t.Fatalf("quirk refund freq = %d len %d, want refund:5 len 46", got, freqs.length)
	}
	if got := pastRecallFrequency(freqs, "policy"); got != 2 {
		t.Fatalf("quirk policy freq = %d, want 2", got)
	}
}

// Parity: tools/src/memory_tools/recall.py::recall (cached and uncached recall
// return identical turns and scores; the exclusion path agrees too).
func TestRecallCachedResultsEqualUncachedReference(t *testing.T) {
	home := t.TempDir()
	first := "session_20260501_090000_pppp0001"
	second := "session_20260502_090000_pppp0002"
	writePastRecallSession(t, home, first, [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time."},
		{"My kayak paddle is a red Werner, in the garage next to the bikes.", "Noted, garage shelf two."},
		{"把发票流程相关的记录都找出来，重点是上线节奏。", "发票流程分三步，第一步是数据校验。"},
	})
	writePastRecallSession(t, home, second, [][2]string{
		{"What can individuals do to help coral reef conservation?", "Reduce runoff and support marine protected areas."},
		{"Refund policy reminder: the quarterly billing invoice needs approval over 1000 USD.", "Refund policy stands."},
	})
	ref := pastRecallQueenRef()
	queries := []string{
		"How long is my daily commute to work?",
		"What colour is my kayak paddle?",
		"quarterly billing invoice refund",
		"coral reef conservation",
		"发票流程 上线节奏",
		"退款政策",
	}
	for _, query := range queries {
		cold := RecallPastConversations(home, ref, query, PastRecallOptions{})
		warm := RecallPastConversations(home, ref, query, PastRecallOptions{})
		want := referenceRecall(home, ref, query, "", PastRecallTopK)
		if !reflect.DeepEqual(cold, want) {
			t.Fatalf("cold %q = %+v, want %+v", query, cold, want)
		}
		if !reflect.DeepEqual(warm, want) {
			t.Fatalf("warm %q = %+v, want %+v", query, warm, want)
		}
		excluded := RecallPastConversations(home, ref, query, PastRecallOptions{ExcludeSession: first})
		wantExcluded := referenceRecall(home, ref, query, first, PastRecallTopK)
		if !reflect.DeepEqual(excluded, wantExcluded) {
			t.Fatalf("excluded %q = %+v, want %+v", query, excluded, wantExcluded)
		}
	}
}

// Parity: tools/src/memory_tools/turn_index.py::refresh (the memo follows the
// index across session growth and deletion, not its own stale copy).
func TestRecallCacheTracksIndexChanges(t *testing.T) {
	home := t.TempDir()
	first := "session_20260503_090000_qqqq0001"
	second := "session_20260504_090000_qqqq0002"
	writePastRecallSession(t, home, first, [][2]string{
		{"My kayak is stored in the garage.", "Noted."},
	})
	writePastRecallSession(t, home, second, [][2]string{
		{"The quarterly budget review slides are in Drive.", "Noted."},
	})
	ref := pastRecallQueenRef()

	// Warm the memo on both sessions.
	if got := RecallPastConversations(home, ref, "Where is my kayak stored?", PastRecallOptions{}); len(got) != 1 {
		t.Fatalf("initial turns = %+v", got)
	}

	// The session grows: the same session must be recomputed from the re-read
	// index, and the new turns must be findable.
	writePastRecallSession(t, home, first, [][2]string{
		{"My kayak is stored in the garage.", "Noted."},
		{"My kayak paddle is a red Werner.", "Noted."},
		{"The spare kayak paddle lives in the shed.", "Noted."},
	})
	query := "What colour is my kayak paddle?"
	got := RecallPastConversations(home, ref, query, PastRecallOptions{})
	want := referenceRecall(home, ref, query, "", PastRecallTopK)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grown %q = %+v, want %+v", query, got, want)
	}
	found := false
	for _, turn := range got {
		if strings.Contains(turn.User, "Werner") {
			found = true
		}
	}
	if !found {
		t.Fatalf("grown session turn not found: %+v", got)
	}

	// The session disappears: its memo entries are pruned with the refresh.
	if err := os.RemoveAll(filepath.Join(home, "queens", pastRecallTestQueen, "sessions", second)); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	query = "budget review slides"
	got = RecallPastConversations(home, ref, query, PastRecallOptions{})
	want = referenceRecall(home, ref, query, "", PastRecallTopK)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after deletion %q = %+v, want %+v", query, got, want)
	}
	cached := recallCacheSessionsForTest(home, ref)
	if cached[second] {
		t.Fatalf("deleted session %s still cached: %v", second, cached)
	}
	if !cached[first] {
		t.Fatalf("live session %s was pruned from the memo: %v", first, cached)
	}
}

// Parity: tools/src/memory_tools/recall.py::recall (the memo is per home: two
// homes with the same owner/session/size must never share counts).
func TestRecallCountsCacheIsolatedPerHome(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()
	session := "session_20260505_090000_rrrr0001"
	// Equal events.jsonl byte lengths, same owner/session/ordinal, different
	// texts: only the home distinguishes them.
	writePastRecallSession(t, homeA, session, [][2]string{{"The zzzz migration window opens on Friday.", ""}})
	writePastRecallSession(t, homeB, session, [][2]string{{"The wwww migration window opens on Friday.", ""}})
	if sizeA, sizeB := recallSessionEventsSize(SearchSessionDir(homeA, SearchScopeQueens, pastRecallTestQueen, session)), recallSessionEventsSize(SearchSessionDir(homeB, SearchScopeQueens, pastRecallTestQueen, session)); sizeA != sizeB {
		t.Fatalf("fixture sizes differ: %d vs %d", sizeA, sizeB)
	}
	ref := pastRecallQueenRef()

	// Warm A, then ask B for the token only A has (and vice versa). A single
	// term keeps the check unambiguous: with one term the scorer does not
	// admit rare one-of-two matches.
	if got := RecallPastConversations(homeA, ref, "zzzz", PastRecallOptions{}); len(got) != 1 {
		t.Fatalf("home A turns = %+v", got)
	}
	if got := RecallPastConversations(homeB, ref, "zzzz", PastRecallOptions{}); len(got) != 0 {
		t.Fatalf("home B served home A's counts: %+v", got)
	}
	if got := RecallPastConversations(homeB, ref, "wwww", PastRecallOptions{}); len(got) != 1 {
		t.Fatalf("home B turns = %+v", got)
	}
	if got := RecallPastConversations(homeA, ref, "wwww", PastRecallOptions{}); len(got) != 0 {
		t.Fatalf("home A served home B's counts: %+v", got)
	}
}

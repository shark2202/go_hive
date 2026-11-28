package memory

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// Process-wide memo of the per-turn term frequencies the recall path scores
// from.
//
// Recall runs on every user message before the model call, and it reloads the
// index file for each query. A turn's term counts are a pure function of its
// (user, reply) text, so they are kept across queries: at 15,000 turns a warm
// query spent ~90% of its time rebuilding them (pprof attribution in
// go_hive/docs/perf-recall-15k.md §4 — all of it repeated work).
//
// Keying and invalidation: entries are keyed by
// (home, scope, owner, session, ordinal) and carry the events.jsonl size the
// session had when they were built. events.jsonl is append-only, and the
// recall index itself uses nothing but that size as its change signal
// (recall_index.go: an unchanged size skips the session) — so a size mismatch
// is exactly the condition under which the index re-reads a session. A
// mismatch drops that session's entries and rebuilds them on demand; sessions
// that leave the index are pruned when a refresh or rebuild lands. A cached
// entry is therefore only ever served for the exact turn text the current
// index holds, and scoring stays identical to recomputing (pinned by
// recall_counts_test.go against the uncached path).
//
// Storage: counts are kept in sorted, pointer-free form (a NUL separated
// term blob plus offsets and counts) rather than a map — that is all
// pastRecallFrequency needs, it is several times smaller per turn, the
// garbage collector scans it cheaply, and a prefix lookup becomes a binary
// search plus the handful of matching keys instead of a scan over every key.
//
// Parity: tools/src/memory_tools/recall.py::recall (the cache is not part of
// the reference algorithm; it only skips repeated work).

type recallCountsHome struct {
	home  string
	scope SearchScope
	owner string
}

// recallTermFreqs is one turn's term counts in scoring form: terms sorted
// ascending, each with its count. It is kept pointer-free on purpose (a NUL
// separated blob plus scalar slices), so many cached turns scan cheaply in
// the garbage collector. length is BM25's document length in the reference's
// unit — characters, with the user text counted twice — not a token total.
type recallTermFreqs struct {
	blob    string   // sorted terms, each followed by '\x00'
	offsets []uint32 // offsets[i] = start of term i in blob
	counts  []int32  // counts[i] = multiplicity of term i
	length  int
}

// termAt returns the i-th stored term.
func (f recallTermFreqs) termAt(i int) string {
	start := f.offsets[i]
	end := len(f.blob) - 1
	if i+1 < len(f.offsets) {
		end = int(f.offsets[i+1]) - 1
	}
	return f.blob[start:end]
}

// search returns the first index whose term is >= term (terms are sorted).
func (f recallTermFreqs) search(term string) int {
	return sort.Search(len(f.offsets), func(i int) bool { return f.termAt(i) >= term })
}

// recallDocumentLength is BM25's document length exactly as the reference
// stores it: the user text twice plus the reply once, in characters — Python
// str length, i.e. Unicode code points — not a token total. Scoring with the
// token count would weigh long CJK turns differently from upstream
// (turn_index.py:200, recall.py::_score).
//
// Parity: tools/src/memory_tools/turn_index.py::_put_turn
func recallDocumentLength(user, reply string) int {
	return 2*utf8.RuneCountInString(user) + utf8.RuneCountInString(reply)
}

// recallTermFreqsFromTurn converts one turn's (user, reply) text to the sorted
// scoring form. It computes both the term counts and BM25's document length
// from the same text, so a caller cannot pair one turn's counts with another's
// length. Counts are per-turn token multiplicities, far below the int32 range;
// terms never contain NUL (they are [a-z0-9]+ runs or Han/Kana pairs), so the
// blob separator is unambiguous.
func recallTermFreqsFromTurn(user, reply string) recallTermFreqs {
	counts := pastRecallTurnCounts(user, reply)
	type pair struct {
		term  string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for term, count := range counts {
		pairs = append(pairs, pair{term: term, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].term < pairs[j].term })
	freqs := recallTermFreqs{
		offsets: make([]uint32, len(pairs)),
		counts:  make([]int32, len(pairs)),
		length:  recallDocumentLength(user, reply),
	}
	var blob strings.Builder
	for i, item := range pairs {
		freqs.offsets[i] = uint32(blob.Len())
		blob.WriteString(item.term)
		blob.WriteByte(0)
		freqs.counts[i] = int32(item.count)
	}
	freqs.blob = blob.String()
	return freqs
}

// recallCountsSession holds one session's cached turns, all built from the
// events.jsonl at eventsSize.
type recallCountsSession struct {
	eventsSize int64
	turns      map[int]recallTermFreqs
}

type recallCountsOwnerCache struct {
	mu       sync.Mutex
	sessions map[string]*recallCountsSession
	turns    int
}

// recallCountsMaxTurnsPerOwner bounds one owner's memo. Past this many cached
// turns the owner's cache is dropped and refills lazily; counts are always
// recomputable, so this only trades memory for one recompute.
const recallCountsMaxTurnsPerOwner = 50000

var recallCountsCaches sync.Map // recallCountsHome -> *recallCountsOwnerCache

// recallCountsHits/recallCountsMisses make the memo observable in tests (and
// are the cheapest honest telemetry for "is the warm path actually warm").
var (
	recallCountsHits   atomic.Uint64
	recallCountsMisses atomic.Uint64
)

func recallCountsOwnerCacheFor(key recallCountsHome) *recallCountsOwnerCache {
	value, _ := recallCountsCaches.LoadOrStore(key, &recallCountsOwnerCache{sessions: map[string]*recallCountsSession{}})
	return value.(*recallCountsOwnerCache)
}

// recallTurnFreqsFor returns the term frequencies of one indexed turn, from
// the cache when it holds this exact turn.
func recallTurnFreqsFor(homeRoot string, ref ScopeRef, eventsSize int64, session string, ordinal int, user, reply string) recallTermFreqs {
	cache := recallCountsOwnerCacheFor(recallCountsHome{home: homeRoot, scope: ref.Scope, owner: ref.Owner})
	cache.mu.Lock()
	defer cache.mu.Unlock()
	sessionCache := cache.sessions[session]
	if sessionCache == nil || sessionCache.eventsSize != eventsSize {
		sessionCache = &recallCountsSession{eventsSize: eventsSize, turns: map[int]recallTermFreqs{}}
		cache.sessions[session] = sessionCache
	}
	if freqs, ok := sessionCache.turns[ordinal]; ok {
		recallCountsHits.Add(1)
		return freqs
	}
	recallCountsMisses.Add(1)
	if cache.turns >= recallCountsMaxTurnsPerOwner {
		cache.sessions = map[string]*recallCountsSession{}
		cache.turns = 0
		sessionCache = &recallCountsSession{eventsSize: eventsSize, turns: map[int]recallTermFreqs{}}
		cache.sessions[session] = sessionCache
	}
	freqs := recallTermFreqsFromTurn(user, reply)
	sessionCache.turns[ordinal] = freqs
	cache.turns++
	return freqs
}

// recallCountsPrune drops cached turns for sessions the index no longer
// covers. The recall path calls it whenever a refresh or rebuild has written
// the index.
func recallCountsPrune(homeRoot string, ref ScopeRef, index *recallIndex) {
	value, ok := recallCountsCaches.Load(recallCountsHome{home: homeRoot, scope: ref.Scope, owner: ref.Owner})
	if !ok {
		return
	}
	cache := value.(*recallCountsOwnerCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for session, sessionCache := range cache.sessions {
		if _, present := index.Sessions[session]; !present {
			cache.turns -= len(sessionCache.turns)
			delete(cache.sessions, session)
		}
	}
}

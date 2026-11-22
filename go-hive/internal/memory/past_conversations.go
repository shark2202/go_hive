package memory

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PastRecallTopK            = 6
	PastRecallMinMessageChars = 8
	PastRecallMaxTerms        = 12
	PastRecallUserChars       = 500
	PastRecallReplyChars      = 500
)

var PastRecallTimeout = time.Duration(envFloat("HIVE_PAST_RECALL_TIMEOUT", 3) * float64(time.Second))

var (
	pastRecallStampRe  = regexp.MustCompile(`\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}[^\]\n]*\]\s*`)
	pastRecallSenderRe = regexp.MustCompile(`\[[^\[\]\n]{1,80}\]:\s*`)
	pastRecallTokenRe  = regexp.MustCompile(`[a-z0-9]+(?:'[a-z]+)?`)
	pastRecallWordRe   = regexp.MustCompile(`[a-z0-9]+`)
	pastRecallCJKRe    = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]{2,}`)
	pastRecallCJKTerm  = regexp.MustCompile(`^[\p{Han}\p{Hiragana}\p{Katakana}]+$`)
)

var pastRecallStopwords = pastRecallWordSet(`
a about above after again against all also am an and any are aren around as at be because been before being
below between both but by can cannot could couldn did didn do does doesn doing don down during each else
ever every few for from further get gets getting give go going got had has have having he her here hers
herself him himself his how however i if in into is isn it its itself just know let like ll made make many
may me might mine more most much must my myself need no nor not now of off on once one only or other ought
our ours ourselves out over own please re really remind remember said same say see she should so some such
tell than thank thanks that the their theirs them themselves then there these they thing things think this
those through to too under until up us use used very want was wasn way we well were weren what whats when
where which while who whom why will with would yes yet you your yours yourself yourselves
tip tips recommend recommendation recommendations suggest suggestion suggestions advice idea ideas help
mention mentioned told talk talked earlier previously before total amount number times time
today yesterday tomorrow day days week weeks month months year years ago recently recent last past next
current currently lately now since
again become becoming bit lot little good great new try trying keep keeping kind sort
`)

var pastRecallCJKStopwords = pastRecallWordSet(`
什么 怎么 为什么 我们 你们 他们 她们 这个 那个 这样 那样 这些 那些 就是 还是 但是 因为 所以 如果 可以 没有 不是 一下
一个 已经 现在 今天 时候 知道 觉得 真的 还有 然后 自己 的话 一样 有点 一点 东西 好的 是的 吧吧 了吗 的吗 了吧
`)

func pastRecallWordSet(words string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, word := range strings.Fields(words) {
		out[word] = struct{}{}
	}
	return out
}

// PastTurn is one earlier conversation turn: a user message plus the assistant
// replies that followed it.
type PastTurn struct {
	Session string
	Ordinal int
	User    string
	Reply   string
	Score   float64
}

type PastRecallOptions struct {
	ExcludeSession string
	TopK           int
	Timeout        time.Duration
}

func pastRecallStem(word string) string {
	for _, rule := range []struct {
		suffix string
		minLen int
	}{{"ies", 5}, {"ing", 6}, {"ed", 5}, {"es", 6}, {"s", 4}} {
		if strings.HasSuffix(word, rule.suffix) && len(word) >= rule.minLen && !strings.HasSuffix(word, "ss") {
			return word[:len(word)-len(rule.suffix)]
		}
	}
	return word
}

// PastRecallQueryTerms extracts the content-word stems and CJK two-character
// terms a user message is about.
//
// Parity: tools/src/memory_tools/recall.py::query_terms
func PastRecallQueryTerms(text string) []string {
	text = pastRecallStampRe.ReplaceAllString(text, "")
	if senders := pastRecallSenderRe.FindAllStringIndex(text, -1); len(senders) > 0 {
		text = text[senders[len(senders)-1][1]:]
	}
	terms := []string{}
	seen := map[string]struct{}{}
	for _, token := range pastRecallTokenRe.FindAllString(strings.ToLower(text), -1) {
		if index := strings.IndexByte(token, '\''); index >= 0 {
			token = token[:index]
		}
		digits := true
		for _, r := range token {
			if r < '0' || r > '9' {
				digits = false
				break
			}
		}
		if _, stop := pastRecallStopwords[token]; stop {
			continue
		}
		if len(token) < 3 && !digits {
			continue
		}
		if digits && len(token) >= 5 {
			continue
		}
		stem := pastRecallStem(token)
		if _, dup := seen[stem]; dup {
			continue
		}
		seen[stem] = struct{}{}
		terms = append(terms, stem)
	}
	for _, run := range pastRecallCJKRe.FindAllString(text, -1) {
		runes := []rune(run)
		for i := 0; i+1 < len(runes); i++ {
			pair := string(runes[i : i+2])
			if _, stop := pastRecallCJKStopwords[pair]; stop {
				continue
			}
			if _, dup := seen[pair]; dup {
				continue
			}
			seen[pair] = struct{}{}
			terms = append(terms, pair)
		}
	}
	if len(terms) > PastRecallMaxTerms {
		terms = terms[:PastRecallMaxTerms]
	}
	return terms
}

func pastRecallTermCounts(text string) map[string]int {
	out := map[string]int{}
	for _, word := range pastRecallWordRe.FindAllString(strings.ToLower(text), -1) {
		if len(word) < 3 && (word == "" || word[0] < '0' || word[0] > '9') {
			continue
		}
		out[word]++
	}
	for _, run := range pastRecallCJKRe.FindAllString(text, -1) {
		runes := []rune(run)
		for i := 0; i+1 < len(runes); i++ {
			out[string(runes[i:i+2])]++
		}
	}
	return out
}

func pastRecallTurnCounts(user, reply string) map[string]int {
	out := pastRecallTermCounts(user)
	for word, count := range pastRecallTermCounts(user) {
		out[word] += count
	}
	for word, count := range pastRecallTermCounts(reply) {
		out[word] += count
	}
	return out
}

// pastRecallFrequency is the term frequency a doc contributes for a query
// term: for CJK terms an exact key match; for everything else every stored
// term with that prefix (the stems in PastRecallQueryTerms match inflections).
func pastRecallFrequency(freqs recallTermFreqs, term string) int {
	index := freqs.search(term)
	if pastRecallCJKTerm.MatchString(term) {
		if index < len(freqs.offsets) && freqs.termAt(index) == term {
			return int(freqs.counts[index])
		}
		return 0
	}
	total := 0
	for ; index < len(freqs.offsets) && strings.HasPrefix(freqs.termAt(index), term); index++ {
		total += int(freqs.counts[index])
	}
	return total
}

type pastRecallDoc struct {
	turn  PastTurn
	freqs recallTermFreqs
}

func pastRecallTurns(sessionDir, sessionID string) []PastTurn {
	messages := SearchSessionMessages(sessionDir)
	turns := []PastTurn{}
	current := -1
	for _, message := range messages {
		switch message.Role {
		case "user":
			turns = append(turns, PastTurn{Session: sessionID, Ordinal: message.Ordinal, User: message.Content})
			current = len(turns) - 1
		case "assistant":
			if current < 0 {
				continue
			}
			if turns[current].Reply != "" {
				turns[current].Reply += "\n"
			}
			turns[current].Reply += message.Content
		}
	}
	return turns
}

// RecallPastConversations scores earlier turns against the raw user message
// with BM25 over the same term space the Python recall uses. It reads the
// per-owner index (recall_index.go), not every session's events.jsonl.
//
// Parity: tools/src/memory_tools/recall.py::recall
// Parity: tools/src/memory_tools/turn_index.py::refresh
func RecallPastConversations(homeRoot string, ref ScopeRef, text string, opts PastRecallOptions) []PastTurn {
	terms := PastRecallQueryTerms(text)
	if len(terms) == 0 {
		return nil
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = PastRecallTopK
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = PastRecallTimeout
	}
	deadline := time.Now().Add(timeout)

	index, ok := recallIndexForQuery(homeRoot, ref, opts.ExcludeSession, deadline)
	if !ok || index == nil || len(index.Turns) == 0 {
		return nil
	}

	docs := make([]pastRecallDoc, 0, len(index.Turns))
	for _, turn := range index.Turns {
		if turn.Session == opts.ExcludeSession {
			// The live session is already in context, and leaving it out
			// before the term statistics keeps BM25's n the same as the
			// scan-based reference.
			continue
		}
		// Term frequencies come from the process-wide memo
		// (recall_counts.go); they are a pure function of this turn's
		// text, so a warm query never rebuilds them. The reference path
		// is pastRecallTurnCounts + the same length sum, and results are
		// identical (recall_counts_test.go).
		freqs := recallTurnFreqsFor(homeRoot, ref, index.Sessions[turn.Session].EventsSize, turn.Session, turn.Ordinal, turn.User, turn.Reply)
		docs = append(docs, pastRecallDoc{
			turn:  PastTurn{Session: turn.Session, Ordinal: turn.Ordinal, User: turn.User, Reply: turn.Reply},
			freqs: freqs,
		})
	}
	return pastRecallChoose(docs, len(docs), terms, opts.ExcludeSession, topK)
}

// pastRecallChoose scores the candidate turns and picks the up-to-topK that
// clear the relevance bar, expanding within a session that already placed a
// top hit. It is the shared tail of the indexed query and the reference
// algorithm: only the source of docs differs.
//
// Parity: tools/src/memory_tools/recall.py::_score
// Parity: tools/src/memory_tools/recall.py::_expand_within_sessions
func pastRecallChoose(docs []pastRecallDoc, allTurns int, terms []string, excludeSession string, topK int) []PastTurn {
	if allTurns == 0 || len(docs) == 0 {
		return nil
	}

	df := make([]int, len(terms))
	matching := make([]pastRecallDoc, 0, len(docs))
	for _, doc := range docs {
		if doc.turn.Session == excludeSession {
			continue
		}
		hit := false
		for i, term := range terms {
			if pastRecallFrequency(doc.freqs, term) > 0 {
				df[i]++
				hit = true
			}
		}
		if hit {
			matching = append(matching, doc)
		}
	}
	if len(matching) == 0 {
		return nil
	}
	rare := int(0.02 * float64(allTurns))
	if rare < 2 {
		rare = 2
	}
	avgLength := 0.0
	for _, doc := range matching {
		avgLength += float64(doc.freqs.length)
	}
	avgLength /= float64(len(matching))
	const k1, b = 1.2, 0.75

	scored := make([]PastTurn, 0, len(matching))
	for _, doc := range matching {
		matched := []int{}
		for i, term := range terms {
			if pastRecallFrequency(doc.freqs, term) > 0 {
				matched = append(matched, i)
			}
		}
		if len(matched) < 2 && len(terms) > 1 {
			hasRare := false
			for _, i := range matched {
				if df[i] <= rare {
					hasRare = true
					break
				}
			}
			if !hasRare {
				continue
			}
		}
		norm := k1 * (1 - b + b*float64(doc.freqs.length)/avgLength)
		score := 0.0
		for _, i := range matched {
			tf := float64(pastRecallFrequency(doc.freqs, terms[i]))
			idf := math.Log(1 + (float64(allTurns-df[i])+0.5)/(float64(df[i])+0.5))
			score += idf * tf * (k1 + 1) / (tf + norm)
		}
		turn := doc.turn
		turn.Score = score
		scored = append(scored, turn)
	}
	if len(scored) == 0 {
		return nil
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	best := scored[0].Score
	limit := topK
	if limit > len(scored) {
		limit = len(scored)
	}
	primary := []PastTurn{}
	for _, turn := range scored[:limit] {
		if turn.Score >= 0.35*best {
			primary = append(primary, turn)
		}
	}
	if len(primary) == 0 {
		return nil
	}
	chosen := append([]PastTurn(nil), primary...)
	hot := map[string]struct{}{}
	seen := map[string]struct{}{}
	for _, turn := range primary {
		hot[turn.Session] = struct{}{}
		seen[turn.Session+"\x00"+itoa(turn.Ordinal)] = struct{}{}
	}
	perSession := map[string]int{}
	extra := []PastTurn{}
	floor := 0.15 * best
	for _, turn := range scored {
		if len(extra) >= 4 {
			break
		}
		if turn.Score < floor {
			break
		}
		if _, ok := hot[turn.Session]; !ok {
			continue
		}
		key := turn.Session + "\x00" + itoa(turn.Ordinal)
		if _, dup := seen[key]; dup {
			continue
		}
		if perSession[turn.Session] >= 2 {
			continue
		}
		perSession[turn.Session]++
		seen[key] = struct{}{}
		extra = append(extra, turn)
	}
	chosen = append(chosen, extra...)
	return chosen
}

func pastRecallSnippet(text string, terms []string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	start := pastRecallWindowStart(text, runes, terms, limit)
	end := start + limit
	if end > len(runes) {
		end = len(runes)
	}
	out := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func pastRecallWindowStart(text string, runes []rune, terms []string, limit int) int {
	type mark struct {
		pos  int
		term int
	}
	marks := []mark{}
	for i, term := range terms {
		pattern := regexp.QuoteMeta(term)
		if !pastRecallCJKTerm.MatchString(term) {
			pattern = `\b` + pattern
		}
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			continue
		}
		for _, location := range re.FindAllStringIndex(text, -1) {
			marks = append(marks, mark{pos: utf8.RuneCountInString(text[:location[0]]), term: i})
			if len(marks) >= 200 {
				break
			}
		}
		if len(marks) >= 200 {
			break
		}
	}
	if len(marks) == 0 {
		return 0
	}
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].pos < marks[j].pos })
	starts := map[int]struct{}{}
	for _, item := range marks {
		start := item.pos - limit/3
		if start < 0 {
			start = 0
		}
		if maxStart := len(runes) - limit; start > maxStart {
			start = maxStart
		}
		starts[start] = struct{}{}
	}
	ordered := make([]int, 0, len(starts))
	for start := range starts {
		ordered = append(ordered, start)
	}
	sort.Ints(ordered)
	bestStart := ordered[0]
	bestDistinct, bestCount := -1, -1
	for _, start := range ordered {
		distinct := map[int]struct{}{}
		count := 0
		for _, item := range marks {
			if item.pos >= start && item.pos < start+limit {
				distinct[item.term] = struct{}{}
				count++
			}
		}
		if count == 0 {
			continue
		}
		if len(distinct) > bestDistinct || (len(distinct) == bestDistinct && count > bestCount) {
			bestDistinct, bestCount, bestStart = len(distinct), count, start
		}
	}
	return bestStart
}

// RenderPastConversationExcerpts renders the best turns oldest first so dates
// read as a timeline.
//
// Parity: tools/src/memory_tools/recall.py::render
func RenderPastConversationExcerpts(turns []PastTurn, text string) string {
	if len(turns) == 0 {
		return ""
	}
	terms := PastRecallQueryTerms(text)
	lines := []string{
		"Excerpts from earlier conversations that may relate to the user's latest message " +
			"(matched by keyword, so possibly incomplete; search_messages and search_timeline go deeper):",
	}
	ordered := append([]PastTurn(nil), turns...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Session != ordered[j].Session {
			return ordered[i].Session < ordered[j].Session
		}
		return ordered[i].Ordinal < ordered[j].Ordinal
	})
	for _, turn := range ordered {
		when := "undated"
		if started, ok := SessionStartedAt(turn.Session); ok {
			when = started.Format("2006-01-02 (Mon)")
		}
		lines = append(lines, "- "+when+", "+turn.Session)
		lines = append(lines, "  user: "+pastRecallSnippet(turn.User, terms, PastRecallUserChars))
		if strings.TrimSpace(turn.Reply) != "" {
			lines = append(lines, "  you: "+pastRecallSnippet(turn.Reply, terms, PastRecallReplyChars))
		}
	}
	return strings.Join(lines, "\n")
}

// PastConversationRecallBlock is recall + render; empty when nothing clears
// the relevance bar.
//
// Parity: tools/src/memory_tools/recall.py::recall_block
func PastConversationRecallBlock(homeRoot string, ref ScopeRef, text, excludeSession string) string {
	turns := RecallPastConversations(homeRoot, ref, text, PastRecallOptions{ExcludeSession: excludeSession})
	return RenderPastConversationExcerpts(turns, text)
}

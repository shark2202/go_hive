package memory

// search_messages: cross-session regex search over what was actually said.
//
// The Python tool (tools/src/memory_tools) searches a mirror cache that is
// built from events.jsonl. This port reads the same source directly and
// rebuilds, per call, exactly the in-scope text the cache would hold:
//
//	client_input_received  -> data.content             (user)
//	client_output_delta    -> last data.snapshot       (assistant)
//	tool_call_completed    -> data.result, or the      (tool)
//	                          spilled body the result points at
//
// Nothing else from the event stream is read. Tool names, tool arguments,
// reasoning_* fields, token counts and timestamps never enter the searchable
// text, so the matcher physically cannot match them (parity with
// index.py's "why a mirror cache" invariant and test_sync_excludes_out_of_scope_fields).
//
// Ordinals are assigned in write order, the same order the Python indexer
// hands them out, so turn windows line up with the reference implementation.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const (
	SearchDefaultMaxMatches      = 20
	SearchDefaultTurnCharCap     = 4000
	SearchDefaultPerMessageChars = 800
	SearchMaxSpillCopyRunes      = 1 << 20
	SearchOmittedMarker          = "…[%d chars omitted]…"
)

// SearchScope is the owner namespace a memory search runs in.
type SearchScope string

const (
	SearchScopeQueens   SearchScope = "queens"
	SearchScopeColonies SearchScope = "colonies"
)

// ScopeRef is one resolved (scope, owner) pair.
type ScopeRef struct {
	Scope SearchScope
	Owner string
}

// ToolError is the dict-shaped error the Python tools return instead of
// raising: {"error": code, ...}. It implements error for Go callers.
type ToolError struct {
	Code      string
	Message   string
	Feature   string
	Position  *int
	Available []string
}

func (e *ToolError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// ToMap renders the error exactly as the MCP tool would.
func (e *ToolError) ToMap() map[string]any {
	out := map[string]any{"error": e.Code, "message": e.Message}
	if e.Feature != "" {
		out["feature"] = e.Feature
	}
	if e.Position != nil {
		out["position"] = *e.Position
	} else {
		out["position"] = nil
	}
	if e.Available != nil {
		out["available"] = e.Available
	}
	return out
}

// ── Scope resolution ──────────────────────────────────────────────────

// SearchHomeRoot resolves $HIVE_HOME the way memory_tools.paths.hive_home does.
func SearchHomeRoot() string {
	if home, err := hivehome.Resolve(); err == nil {
		return home.Root
	}
	usr, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(usr, ".hive")
}

// SearchScopeRoot is the source root for a scope: <home>/queens or <home>/colonies.
func SearchScopeRoot(homeRoot string, scope SearchScope) string {
	return filepath.Join(homeRoot, string(scope))
}

// SearchOwnerDir is <home>/<scope>/<owner>.
func SearchOwnerDir(homeRoot string, scope SearchScope, owner string) string {
	return filepath.Join(SearchScopeRoot(homeRoot, scope), owner)
}

// SearchSessionRoots lists the directories holding an owner's session_* dirs.
// A queen has one; a colony has one per queen that has overseen it.
func SearchSessionRoots(homeRoot string, scope SearchScope, owner string) []string {
	if scope == SearchScopeQueens {
		return []string{filepath.Join(SearchOwnerDir(homeRoot, scope, owner), "sessions")}
	}
	queens := filepath.Join(SearchOwnerDir(homeRoot, scope, owner), "queens")
	entries, err := os.ReadDir(queens)
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(queens, entry.Name(), "sessions")
		if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return roots
}

// SearchListOwners lists queen ids or colony names with at least one session.
func SearchListOwners(homeRoot string, scope SearchScope) []string {
	root := SearchScopeRoot(homeRoot, scope)
	entries, err := os.ReadDir(root)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "session_") {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out
}

// SearchListSessions returns the session ids under an owner, optionally
// filtered by id and by a [since, until) start window.
func SearchListSessions(homeRoot string, scope SearchScope, owner, sessionFilter string, since, until *time.Time) []string {
	out := []string{}
	for _, root := range SearchSessionRoots(homeRoot, scope, owner) {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "session_") {
				continue
			}
			if sessionFilter != "" && entry.Name() != sessionFilter {
				continue
			}
			if since != nil || until != nil {
				started, ok := SessionStartedAt(entry.Name())
				if !ok {
					continue
				}
				if since != nil && started.Before(*since) {
					continue
				}
				if until != nil && !started.Before(*until) {
					continue
				}
			}
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out
}

// SearchSessionDir resolves one session id to its directory (which may not
// exist when the id is unknown).
func SearchSessionDir(homeRoot string, scope SearchScope, owner, session string) string {
	roots := SearchSessionRoots(homeRoot, scope, owner)
	for _, root := range roots {
		dir := filepath.Join(root, session)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	if len(roots) > 0 {
		return filepath.Join(roots[0], session)
	}
	return filepath.Join(SearchOwnerDir(homeRoot, scope, owner), "sessions", session)
}

// ResolveInjectedScope reads the host-injected scope binding. Exactly one of
// HIVE_QUEEN_ID / HIVE_COLONY_NAME must be set; empty is treated as unset.
func ResolveInjectedScope(env map[string]string, homeRoot string) (ScopeRef, *ToolError) {
	queen := strings.TrimSpace(env["HIVE_QUEEN_ID"])
	colony := strings.TrimSpace(env["HIVE_COLONY_NAME"])

	if queen != "" && colony != "" {
		return ScopeRef{}, &ToolError{
			Code:    "scope_unbound",
			Message: "both HIVE_QUEEN_ID and HIVE_COLONY_NAME are set; the framework must inject exactly one",
		}
	}
	if queen == "" && colony == "" {
		return ScopeRef{}, &ToolError{
			Code:    "scope_unbound",
			Message: "no scope bound — framework must set HIVE_QUEEN_ID or HIVE_COLONY_NAME in the memory-tools subprocess env",
		}
	}

	ref := ScopeRef{Scope: SearchScopeQueens, Owner: queen}
	if queen == "" {
		ref = ScopeRef{Scope: SearchScopeColonies, Owner: colony}
	}
	if info, err := os.Stat(SearchOwnerDir(homeRoot, ref.Scope, ref.Owner)); err != nil || !info.IsDir() {
		// Upstream builds this noun with scope[:-1], which yields "colonie"
		// for colonies. Kept verbatim for observable-text parity.
		noun := strings.TrimSuffix(string(ref.Scope), "s")
		return ScopeRef{}, &ToolError{
			Code:      "scope_not_found",
			Message:   fmt.Sprintf("%s %s not found on disk", noun, searchQuote(ref.Owner)),
			Available: SearchListOwners(homeRoot, ref.Scope),
		}
	}
	return ref, nil
}

func searchQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

// ── Regex validation (Rust/RE2 subset) ───────────────────────────────

var searchForbiddenRe = regexp.MustCompile(`\(\?[=!]|\(\?<[=!]|\\[1-9]`)

// ValidateSearchPattern rejects the constructs the public contract excludes,
// with the same error codes and features the Python tool reports.
func ValidateSearchPattern(pattern string) *ToolError {
	if pattern == "" {
		return &ToolError{Code: "regex_invalid", Message: "pattern is empty"}
	}
	if m := searchForbiddenRe.FindStringSubmatchIndex(pattern); m != nil {
		feature := pattern[m[0]:m[1]]
		kind := "backreference"
		switch {
		case strings.HasPrefix(feature, "(?="):
			kind = "lookahead"
		case strings.HasPrefix(feature, "(?!"):
			kind = "negative_lookahead"
		case strings.HasPrefix(feature, "(?<="):
			kind = "lookbehind"
		case strings.HasPrefix(feature, "(?<!"):
			kind = "negative_lookbehind"
		}
		pos := m[0]
		return &ToolError{
			Code:     "regex_unsupported",
			Message:  kind + " not supported (Rust-style regex / RE2 subset)",
			Feature:  kind,
			Position: &pos,
		}
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return &ToolError{Code: "regex_invalid", Message: err.Error()}
	}
	return nil
}

// ParseSearchISOTime accepts a date or datetime, mirroring _parse_iso.
func ParseSearchISOTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "T ") {
		trimmed := strings.ReplaceAll(value, "Z", "")
		layouts := []string{
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05",
			"2006-01-02T15:04",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
			"2006-01-02T15:04:05-07:00",
			"2006-01-02T15:04:05.999999999-07:00",
		}
		for _, layout := range layouts {
			if t, err := time.ParseInLocation(layout, trimmed, time.Local); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func searchFilterError(field, value string) *ToolError {
	return &ToolError{
		Code:    "filter_invalid",
		Message: field + "=" + searchQuote(value) + " is not a valid ISO date or datetime",
	}
}

// ── Reading in-scope messages ────────────────────────────────────────

// MemoryMessage is one in-scope message of a session, in cache order.
type MemoryMessage struct {
	Ordinal int
	Role    string
	Content string
}

var (
	searchSpillHeaderRe = regexp.MustCompile("^Tool `[^`]+` returned [\\d,]+ characters")
	searchSpillPathRe   = regexp.MustCompile(`Full result saved at:\s*(\S+)`)
)

type searchPendingAssistant struct {
	iteration any
	innerTurn any
	snapshot  string
	dirty     bool
}

// SearchSessionMessages rebuilds a session's in-scope messages from
// events.jsonl, with the same ordinal assignment the Python indexer uses.
func SearchSessionMessages(sessionDir string) []MemoryMessage {
	f, err := os.Open(filepath.Join(sessionDir, "events.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()

	messages := []MemoryMessage{}
	reader := bufio.NewReader(f)
	var pending *searchPendingAssistant

	nextOrdinal := 0
	appendMessage := func(role, content string) {
		messages = append(messages, MemoryMessage{Ordinal: nextOrdinal, Role: role, Content: content})
		nextOrdinal++
	}
	finalize := func() {
		if pending == nil {
			return
		}
		if pending.dirty && pending.snapshot != "" {
			appendMessage("assistant", pending.snapshot)
		}
		pending = nil
	}

	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) == nil {
				data, _ := event["data"].(map[string]any)
				switch event["type"] {
				case "client_input_received":
					finalize()
					if content, ok := data["content"].(string); ok && content != "" {
						appendMessage("user", content)
					}
				case "client_output_delta":
					snapshot, _ := data["snapshot"].(string)
					if pending != nil && searchScalarEqual(pending.iteration, data["iteration"]) &&
						searchScalarEqual(pending.innerTurn, data["inner_turn"]) {
						// Snapshots are cumulative; last write wins.
						pending.snapshot = snapshot
						pending.dirty = true
					} else {
						finalize()
						pending = &searchPendingAssistant{
							iteration: data["iteration"],
							innerTurn: data["inner_turn"],
							snapshot:  snapshot,
							dirty:     true,
						}
					}
				case "tool_call_completed":
					finalize()
					appendMessage("tool", searchToolResultBody(data["result"]))
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	// The session's last event is usually its final assistant reply; nothing
	// later will flush it.
	finalize()
	return messages
}

func searchScalarEqual(a, b any) bool {
	af, aok := a.(float64)
	bf, bok := b.(float64)
	if aok && bok {
		return af == bf
	}
	return a == b
}

func searchToolResultBody(result any) string {
	text := ""
	switch v := result.(type) {
	case nil:
		return ""
	case string:
		text = v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		text = string(data)
	}
	return searchResolveSpill(text, SearchMaxSpillCopyRunes)
}

// searchResolveSpill replaces a large-result placeholder with the body it
// points at, capped at maxRunes characters (the Python cap is in characters).
func searchResolveSpill(resultText string, maxRunes int) string {
	if resultText == "" || !searchSpillHeaderRe.MatchString(resultText) {
		return resultText
	}
	m := searchSpillPathRe.FindStringSubmatch(resultText)
	if m == nil {
		return resultText
	}
	path := m[1]
	f, err := os.Open(path)
	if err != nil {
		return resultText
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	var b strings.Builder
	count := 0
	for count < maxRunes {
		r, _, err := reader.ReadRune()
		if err != nil {
			if err == io.EOF {
				break
			}
			return resultText
		}
		b.WriteRune(r)
		count++
	}
	if count < maxRunes {
		return b.String()
	}
	_, _, err = reader.ReadRune()
	if err == io.EOF {
		return b.String()
	}
	return b.String() + fmt.Sprintf("\n\n…[truncated to %d bytes — full body at %s]…\n", maxRunes, path)
}

// ── Search_messages ──────────────────────────────────────────────────

// SearchMessagesOptions mirrors the tool's parameters.
type SearchMessagesOptions struct {
	Pattern           string
	Session           string
	Role              string
	MatchSource       string
	Since             string
	Until             string
	MaxMatches        int
	Context           string
	TurnCharCap       int
	PerMessageCharCap int
}

// SearchTurnMessage is one element of a returned turn window.
type SearchTurnMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	IsHit   bool   `json:"is_hit,omitempty"`
}

// SearchMessageMatch is one matching message plus its turn window.
type SearchMessageMatch struct {
	Session          string               `json:"session"`
	SessionStartedAt *string              `json:"session_started_at"`
	Turn             *[]SearchTurnMessage `json:"turn,omitempty"`
}

// SearchMessagesResult is the tool's success envelope.
type SearchMessagesResult struct {
	Matches      []SearchMessageMatch `json:"matches"`
	TotalMatches int                  `json:"total_matches"`
}

type searchHit struct {
	session  string
	ordinal  int
	role     string
	span     [2]int // rune offsets into the hit message
	messages []MemoryMessage
}

// SearchMessages searches every session in scope. Returns a ToolError for the
// contract violations (bad scope, bad regex, bad filters); per-session read
// errors are tolerated, as in the reference implementation.
func SearchMessages(homeRoot string, ref ScopeRef, opts SearchMessagesOptions) (*SearchMessagesResult, *ToolError) {
	if err := ValidateSearchPattern(opts.Pattern); err != nil {
		return nil, err
	}
	role := opts.Role
	if role == "" {
		role = "all"
	}
	if role != "all" && role != "user" && role != "assistant" && role != "tool" {
		return nil, &ToolError{Code: "filter_invalid", Message: "role=" + searchQuote(role) + " must be user, assistant, tool or all"}
	}
	matchSource := opts.MatchSource
	if matchSource == "" {
		matchSource = "all"
	}
	if matchSource != "all" && matchSource != "events" && matchSource != "data" {
		return nil, &ToolError{Code: "filter_invalid", Message: "match_source=" + searchQuote(matchSource) + " must be events, data or all"}
	}

	var since, until *time.Time
	if opts.Since != "" {
		parsed, ok := ParseSearchISOTime(opts.Since)
		if !ok {
			return nil, searchFilterError("since", opts.Since)
		}
		since = &parsed
	}
	if opts.Until != "" {
		parsed, ok := ParseSearchISOTime(opts.Until)
		if !ok {
			return nil, searchFilterError("until", opts.Until)
		}
		until = &parsed
	}

	regex, err := regexp.Compile(opts.Pattern)
	if err != nil {
		return nil, &ToolError{Code: "regex_invalid", Message: err.Error()}
	}

	maxMatches := opts.MaxMatches
	if maxMatches == 0 {
		maxMatches = SearchDefaultMaxMatches
	}
	contextMode := opts.Context
	if contextMode == "" {
		contextMode = "turn"
	}
	if contextMode != "none" && contextMode != "narrow" && contextMode != "turn" {
		return nil, &ToolError{Code: "filter_invalid", Message: "context=" + searchQuote(contextMode) + " must be none, narrow or turn"}
	}
	turnCap := opts.TurnCharCap
	if turnCap == 0 {
		turnCap = SearchDefaultTurnCharCap
	}
	perMessageCap := opts.PerMessageCharCap
	if perMessageCap == 0 {
		perMessageCap = SearchDefaultPerMessageChars
	}

	sessions := SearchListSessions(homeRoot, ref.Scope, ref.Owner, opts.Session, since, until)
	hits := []searchHit{}
	startedAt := make(map[string]time.Time, len(sessions))

	for _, session := range sessions {
		if started, ok := SessionStartedAt(session); ok {
			startedAt[session] = started
		}
		messages := SearchSessionMessages(SearchSessionDir(homeRoot, ref.Scope, ref.Owner, session))
		if len(messages) == 0 {
			continue
		}
		for _, message := range messages {
			if role != "all" && message.Role != role {
				continue
			}
			loc := regex.FindStringIndex(message.Content)
			if loc == nil {
				continue
			}
			hits = append(hits, searchHit{
				session:  session,
				ordinal:  message.Ordinal,
				role:     message.Role,
				span:     searchRuneSpan(message.Content, loc[0], loc[1]),
				messages: messages,
			})
		}
	}

	// Most-recent session first, then ordinal ascending.
	sort.SliceStable(hits, func(i, j int) bool {
		ti, iok := startedAt[hits[i].session]
		tj, jok := startedAt[hits[j].session]
		var si, sj float64
		if iok {
			si = float64(ti.UnixNano()) / 1e9
		}
		if jok {
			sj = float64(tj.UnixNano()) / 1e9
		}
		if si != sj {
			return si > sj
		}
		return hits[i].ordinal < hits[j].ordinal
	})

	total := len(hits)
	kept := searchCapHits(hits, maxMatches)

	result := &SearchMessagesResult{Matches: []SearchMessageMatch{}, TotalMatches: total}
	for _, hit := range kept {
		match := SearchMessageMatch{Session: hit.session}
		if started, ok := startedAt[hit.session]; ok {
			formatted := started.Format("2006-01-02T15:04:05")
			match.SessionStartedAt = &formatted
		}
		if contextMode != "none" {
			turn := searchEnrichHit(hit, contextMode, turnCap, perMessageCap)
			match.Turn = &turn
		}
		result.Matches = append(result.Matches, match)
	}
	return result, nil
}

// searchCapHits applies Python's hits[:max] slicing, including negative bounds.
func searchCapHits(hits []searchHit, max int) []searchHit {
	n := len(hits)
	end := max
	if end < 0 {
		end = n + end
	}
	if end < 0 {
		end = 0
	}
	if end > n {
		end = n
	}
	return hits[:end]
}

// SearchMessagesEnvelope is the tool-facing wrapper: it resolves the scope
// from env and always returns a dict, never an error.
func SearchMessagesEnvelope(env map[string]string, homeRoot string, opts SearchMessagesOptions) map[string]any {
	ref, scopeErr := ResolveInjectedScope(env, homeRoot)
	if scopeErr != nil {
		return scopeErr.ToMap()
	}
	result, err := SearchMessages(homeRoot, ref, opts)
	if err != nil {
		return err.ToMap()
	}
	out := map[string]any{
		"matches":       result.Matches,
		"total_matches": result.TotalMatches,
	}
	return out
}

// ── Turn enrichment ──────────────────────────────────────────────────

func searchEnrichHit(hit searchHit, mode string, turnCap, perMessageCap int) []SearchTurnMessage {
	hitText := ""
	for _, message := range hit.messages {
		if message.Ordinal == hit.ordinal {
			hitText = message.Content
			break
		}
	}
	trimmed, span := searchTrimAroundMatch(hitText, hit.span, perMessageCap)

	if mode == "narrow" {
		return []SearchTurnMessage{{Role: hit.role, Content: trimmed, IsHit: true}}
	}

	start, end := searchTurnBounds(hit.messages, hit.ordinal)
	window := make([]SearchTurnMessage, 0, end-start)
	for i := start; i < end; i++ {
		message := hit.messages[i]
		if message.Ordinal == hit.ordinal {
			window = append(window, SearchTurnMessage{Role: message.Role, Content: trimmed, IsHit: true})
			continue
		}
		body := message.Content
		if utf8.RuneCountInString(body) > perMessageCap {
			body = searchMiddleTrim(body, perMessageCap)
		}
		window = append(window, SearchTurnMessage{Role: message.Role, Content: body})
	}

	total := 0
	for _, message := range window {
		total += utf8.RuneCountInString(message.Content)
	}
	if total > turnCap {
		// First pass: shrink non-hit messages by their proportional share of
		// the overflow, preserving at least 160 chars apiece.
		overflow := total - turnCap
		nonHitTotal := 0
		for _, message := range window {
			if !message.IsHit {
				nonHitTotal += utf8.RuneCountInString(message.Content)
			}
		}
		if nonHitTotal == 0 {
			nonHitTotal = 1
		}
		for i := range window {
			if window[i].IsHit {
				continue
			}
			length := utf8.RuneCountInString(window[i].Content)
			share := int(math.RoundToEven(float64(overflow) * (float64(length) / float64(nonHitTotal))))
			newCap := length - share
			if newCap < 160 {
				newCap = 160
			}
			if length > newCap {
				window[i].Content = searchMiddleTrim(window[i].Content, newCap)
			}
		}
		total = 0
		for _, message := range window {
			total += utf8.RuneCountInString(message.Content)
		}
	}

	if total > turnCap {
		// Final pass: tighten the hit message, never across the match.
		for i := range window {
			if !window[i].IsHit {
				continue
			}
			length := utf8.RuneCountInString(window[i].Content)
			newCap := length - (total - turnCap)
			if minCap := span[1] - span[0] + 64; newCap < minCap {
				newCap = minCap
			}
			tighter, newSpan := searchTrimAroundMatch(trimmed, span, newCap)
			window[i].Content = tighter
			span = newSpan
		}
	}
	return window
}

func searchTurnBounds(messages []MemoryMessage, hitOrdinal int) (int, int) {
	hitIndex := -1
	for i, message := range messages {
		if message.Ordinal == hitOrdinal {
			hitIndex = i
			break
		}
	}
	if hitIndex < 0 {
		return 0, 0
	}
	start := hitIndex
	for start > 0 && messages[start].Role != "user" {
		start--
	}
	end := hitIndex + 1
	for end < len(messages) && messages[end].Role != "user" {
		end++
	}
	return start, end
}

// searchMiddleTrim middle-truncates text to cap characters.
func searchMiddleTrim(text string, cap int) string {
	runes := []rune(text)
	if len(runes) <= cap {
		return text
	}
	omitted := len(runes) - cap
	marker := fmt.Sprintf(SearchOmittedMarker, omitted)
	keep := cap - utf8.RuneCountInString(marker)
	if keep < 0 {
		keep = 0
	}
	head := keep / 2
	tail := keep - head
	if tail == 0 {
		return string(runes[:head]) + marker
	}
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

// searchTrimAroundMatch trims text to cap characters while preserving span.
func searchTrimAroundMatch(text string, span [2]int, cap int) (string, [2]int) {
	runes := []rune(text)
	start, end := span[0], span[1]
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	if end < start {
		end = start
	}
	matchLen := end - start

	if len(runes) <= cap || matchLen >= cap {
		if matchLen >= cap {
			return string(runes[start:end]), [2]int{0, matchLen}
		}
		return text, [2]int{start, end}
	}

	budget := cap - matchLen
	wantBefore := budget / 2
	wantAfter := budget - wantBefore
	availBefore := start
	availAfter := len(runes) - end

	takeBefore := wantBefore
	if takeBefore > availBefore {
		takeBefore = availBefore
	}
	takeAfter := wantAfter
	if takeAfter > availAfter {
		takeAfter = availAfter
	}
	leftover := budget - takeBefore - takeAfter
	if leftover > 0 && availBefore > takeBefore {
		extra := leftover
		if extra > availBefore-takeBefore {
			extra = availBefore - takeBefore
		}
		takeBefore += extra
		leftover -= extra
	}
	if leftover > 0 && availAfter > takeAfter {
		extra := leftover
		if extra > availAfter-takeAfter {
			extra = availAfter - takeAfter
		}
		takeAfter += extra
	}

	headOmitted := start - takeBefore
	tailOmitted := availAfter - takeAfter

	var b strings.Builder
	newStart := 0
	if headOmitted > 0 {
		marker := fmt.Sprintf(SearchOmittedMarker, headOmitted)
		b.WriteString(marker)
		newStart += utf8.RuneCountInString(marker)
	}
	b.WriteString(string(runes[start-takeBefore : start]))
	newStart += takeBefore
	newEnd := newStart + matchLen
	b.WriteString(string(runes[start:end]))
	b.WriteString(string(runes[end : end+takeAfter]))
	if tailOmitted > 0 {
		b.WriteString(fmt.Sprintf(SearchOmittedMarker, tailOmitted))
	}
	return b.String(), [2]int{newStart, newEnd}
}

// searchRuneSpan converts byte offsets from regexp into rune offsets.
func searchRuneSpan(text string, startByte, endByte int) [2]int {
	start := utf8.RuneCountInString(text[:startByte])
	end := start + utf8.RuneCountInString(text[startByte:endByte])
	return [2]int{start, end}
}

package memory

// search_timeline: the dated events, facts and plans a user has mentioned.
//
// Reads the per-session timeline.jsonl files the host extracts
// (framework/agents/queen/timeline.py, ported in timeline.go) and answers the
// questions raw transcript search is bad at: when something happened, how many
// times, what changed and what is current.
//
// Each item carries the date range it happened or held, already resolved from
// the session date, plus said_on for when the user mentioned it.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SearchTimelineDefaultMaxResults = 60
	SearchTimelineMaxResults        = 300
)

// SearchTimelineOptions mirrors the tool's parameters.
type SearchTimelineOptions struct {
	Pattern    string
	Since      string
	Until      string
	Kind       string
	MaxResults int
}

// SearchTimelineItem is one dated occurrence.
type SearchTimelineItem struct {
	When    string  `json:"when"`
	Kind    string  `json:"kind"`
	Text    string  `json:"text"`
	SaidOn  *string `json:"said_on"`
	Session string  `json:"session"`
	Turn    any     `json:"turn"`
}

// SearchTimelineResult is the tool's success envelope.
type SearchTimelineResult struct {
	Items                []SearchTimelineItem `json:"items"`
	Total                int                  `json:"total"`
	Truncated            bool                 `json:"truncated"`
	SessionsWithTimeline int                  `json:"sessions_with_timeline"`
}

type searchTimelineRecord struct {
	item  SearchTimelineItem
	order []string
}

// SearchTimeline finds matching timeline items across the owner's sessions,
// oldest first. maxResults <= 0 returns every item (the tool layer clamps).
func SearchTimeline(homeRoot string, ref ScopeRef, opts SearchTimelineOptions) (*SearchTimelineResult, *ToolError) {
	kind := opts.Kind
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "event" && kind != "fact" && kind != "plan" {
		return nil, &ToolError{
			Code:    "filter_invalid",
			Message: "kind=" + searchQuote(kind) + " must be event, fact, plan or all",
		}
	}

	var since, until *time.Time
	if opts.Since != "" {
		parsed, ok := ParseSearchISOTime(opts.Since)
		if !ok {
			return nil, searchFilterError("since", opts.Since)
		}
		day := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC)
		since = &day
	}
	if opts.Until != "" {
		parsed, ok := ParseSearchISOTime(opts.Until)
		if !ok {
			return nil, searchFilterError("until", opts.Until)
		}
		day := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC)
		until = &day
	}

	regex, err := regexp.Compile("(?i)" + opts.Pattern)
	if err != nil {
		return nil, &ToolError{Code: "pattern_invalid", Message: err.Error()}
	}

	items := []searchTimelineRecord{}
	sessionsWithTimeline := 0

	for _, root := range SearchSessionRoots(homeRoot, ref.Scope, ref.Owner) {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name(), TimelineFile)
			info, statErr := os.Stat(path)
			if statErr != nil || info.IsDir() {
				continue
			}
			sessionsWithTimeline++

			started, hasStarted := SessionStartedAt(entry.Name())
			saidOn := ""
			if hasStarted {
				saidOn = started.Format("2006-01-02")
			}

			f, openErr := os.Open(path)
			if openErr != nil {
				continue
			}
			reader := bufio.NewReader(f)
			for {
				line, lineErr := reader.ReadString('\n')
				if text := strings.TrimSpace(line); text != "" {
					if record, ok := searchTimelineRecordFor(text, entry.Name(), saidOn, regex, kind, since, until); ok {
						items = append(items, record)
					}
				}
				if lineErr != nil {
					break
				}
			}
			_ = f.Close()
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].order, items[j].order
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})

	out := make([]SearchTimelineItem, 0, len(items))
	for _, record := range items {
		out = append(out, record.item)
	}

	total := len(out)
	truncated := false
	if opts.MaxResults > 0 && len(out) > opts.MaxResults {
		out = out[:opts.MaxResults]
		truncated = true
	}
	return &SearchTimelineResult{
		Items:                out,
		Total:                total,
		Truncated:            truncated,
		SessionsWithTimeline: sessionsWithTimeline,
	}, nil
}

func searchTimelineRecordFor(
	line, session, saidOn string,
	regex *regexp.Regexp,
	kind string,
	since, until *time.Time,
) (searchTimelineRecord, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return searchTimelineRecord{}, false
	}
	if kind != "all" {
		if value, _ := raw["kind"].(string); value != kind {
			return searchTimelineRecord{}, false
		}
	}

	text, _ := raw["text"].(string)
	aliases := []string{}
	switch value := raw["aliases"].(type) {
	case []any:
		for _, entry := range value {
			if alias, ok := entry.(string); ok {
				aliases = append(aliases, alias)
			}
		}
	case []string:
		aliases = append(aliases, value...)
	}
	haystack := strings.Join(append([]string{text}, aliases...), " | ")
	if !regex.MatchString(haystack) {
		return searchTimelineRecord{}, false
	}

	startText, _ := raw["start"].(string)
	endText, _ := raw["end"].(string)
	start, err := time.Parse("2006-01-02", startText)
	if err != nil {
		return searchTimelineRecord{}, false
	}
	end, err := time.Parse("2006-01-02", endText)
	if err != nil {
		end = start
	}
	if end.Before(start) {
		start, end = end, start
	}
	if since != nil && end.Before(*since) {
		return searchTimelineRecord{}, false
	}
	if until != nil && !start.Before(*until) {
		return searchTimelineRecord{}, false
	}

	when := startText
	if startText != endText {
		when = startText + ".." + endText
	}
	item := SearchTimelineItem{
		When:    when,
		Kind:    searchTimelineKind(raw["kind"]),
		Text:    text,
		Session: session,
		Turn:    raw["turn"],
	}
	if saidOn != "" {
		value := saidOn
		item.SaidOn = &value
	}
	order := []string{
		when,
		saidOn,
		session,
		searchTimelineTurnDigits(raw["turn"]),
	}
	return searchTimelineRecord{item: item, order: order}, true
}

func searchTimelineKind(value any) string {
	if kind, ok := value.(string); ok && kind != "" {
		return kind
	}
	return "event"
}

// searchTimelineTurnDigits renders the turn for the sort key the way Python
// compares it: numerically for ints, as zero when absent.
func searchTimelineTurnDigits(value any) string {
	switch v := value.(type) {
	case float64:
		digits := make([]byte, 0, 8)
		if v < 0 {
			return "0"
		}
		for n := int64(v); n > 0; n /= 10 {
			digits = append(digits, byte('0'+n%10))
		}
		if len(digits) == 0 {
			return "0"
		}
		for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
			digits[i], digits[j] = digits[j], digits[i]
		}
		// Zero-pad so lexicographic order matches numeric order.
		for len(digits) < 10 {
			digits = append([]byte{'0'}, digits...)
		}
		return string(digits)
	case string:
		return v
	}
	return "0"
}

// SearchTimelineEnvelope is the tool-facing wrapper: resolves the scope,
// validates the pattern, clamps max_results and always returns a dict.
func SearchTimelineEnvelope(env map[string]string, homeRoot string, opts SearchTimelineOptions) map[string]any {
	ref, scopeErr := ResolveInjectedScope(env, homeRoot)
	if scopeErr != nil {
		return scopeErr.ToMap()
	}
	if _, err := regexp.Compile(opts.Pattern); err != nil {
		return map[string]any{"error": "pattern_invalid", "message": err.Error()}
	}
	maxResults := opts.MaxResults
	if maxResults == 0 {
		maxResults = SearchTimelineDefaultMaxResults
	}
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > SearchTimelineMaxResults {
		maxResults = SearchTimelineMaxResults
	}
	opts.MaxResults = maxResults

	result, err := SearchTimeline(homeRoot, ref, opts)
	if err != nil {
		return err.ToMap()
	}
	return map[string]any{
		"items":                  result.Items,
		"total":                  result.Total,
		"truncated":              result.Truncated,
		"sessions_with_timeline": result.SessionsWithTimeline,
	}
}

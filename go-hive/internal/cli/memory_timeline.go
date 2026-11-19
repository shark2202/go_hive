package cli

// memory_timeline: a reading view over the dated memory timeline.
//
// search_timeline answers "find this pattern in what the user told me". It
// requires a pattern, so the queen has no way to simply look at what happened
// lately. This tool fills that hole: it groups the same timeline.jsonl records
// the host extracts (internal/memory/timeline.go) by the day they happened,
// newest day first, and returns a short summary per day.
//
// The engine stays memory.SearchTimeline — no second reader of the timeline
// format. The grouping, day/range capping and secret redaction live here
// because they are presentation concerns, and the tool never exposes raw
// session files, credentials or timeline metadata.

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	// MemoryTimelineToolName is the session tool the queen sees.
	MemoryTimelineToolName = "memory_timeline"

	MemoryTimelineDefaultMaxDays = 14
	MemoryTimelineMaxDays        = 90
	MemoryTimelineDefaultPerDay  = 5
	MemoryTimelineMaxPerDay      = 25
	// MemoryTimelineTextLimit bounds one summary so a day stays scannable.
	MemoryTimelineTextLimit = 200
)

// memory_timeline is a Go-only browse surface: upstream's timeline.py is
// search-only, so there is no tool to mirror. It reuses the ported engine.
func memoryTimelineToolDefs(home hivehome.Home) []llm.ToolDef {
	if !memoryTimelineAvailable(home) {
		return nil
	}
	return []llm.ToolDef{memoryTimelineToolDef()}
}

// Parity-exempt: upstream has no browse-by-day tool; Timeline search lives in
// tools/src/memory_tools/timeline.py and this reuses memory.SearchTimeline.
func memoryTimelineToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: MemoryTimelineToolName,
		Description: "Browse the dated memory timeline by day — the events, facts and plans the user mentioned " +
			"across past conversations, newest day first. Unlike search_timeline this needs no pattern: use it " +
			"when the user asks what happened lately or wants a recap of a date range, or to orient yourself " +
			"before drilling into one day. Each day carries the true item count and a few short summaries; " +
			"secret-looking values are redacted. Follow up with search_timeline for a specific pattern or " +
			"search_messages for the exact wording.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"since": map[string]any{
					"type":        "string",
					"description": "ISO date or datetime. Only items that happened on or after this day are shown.",
				},
				"until": map[string]any{
					"type":        "string",
					"description": "ISO date or datetime. Only items that happened before this day are shown (exclusive).",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"event", "fact", "plan", "all"},
					"description": "Kind of occurrence: 'event', 'fact', 'plan', or 'all' (default).",
				},
				"max_days": map[string]any{
					"type":        "integer",
					"description": "Newest days to return (default 14, capped at 90).",
				},
				"per_day": map[string]any{
					"type":        "integer",
					"description": "Summaries per day (default 5, capped at 25). The day's count still reports every item.",
				},
			},
		},
	}
}

type memoryTimelineEntry struct {
	Kind    string `json:"kind"`
	When    string `json:"when"`
	Text    string `json:"text"`
	Session string `json:"session,omitempty"`
}

type memoryTimelineDay struct {
	Day   string                `json:"day"`
	Count int                   `json:"count"`
	Items []memoryTimelineEntry `json:"items"`
}

// executeMemoryTimelineTool runs one browse call. handled is false for any
// other tool name so the caller keeps its dispatch order.
func (c *sessionToolCatalogue) executeMemoryTimelineTool(name string, input map[string]any) (content string, isError bool, handled bool) {
	if name != MemoryTimelineToolName {
		return "", false, false
	}
	ref, toolErr := c.searchScope()
	if toolErr != nil {
		return tools.PythonJSONDumps(toolErr.ToMap()), true, true
	}
	if !memoryTimelineAvailable(c.home) {
		return tools.PythonJSONDumps(map[string]any{
			"error":   "feature_disabled",
			"message": "the memory_timeline feature is off; turn it on in Developer settings to browse the dated timeline",
		}), true, true
	}

	maxDays := clampTimelineRange(searchIntParam(input, "max_days"), MemoryTimelineDefaultMaxDays, MemoryTimelineMaxDays)
	perDay := clampTimelineRange(searchIntParam(input, "per_day"), MemoryTimelineDefaultPerDay, MemoryTimelineMaxPerDay)

	result, toolErr := memory.SearchTimeline(c.home.Root, ref, memory.SearchTimelineOptions{
		// A browse has no pattern; ".*" compiles to an always-match regex. The
		// engine still applies the range and kind filters.
		Pattern:    ".*",
		Since:      searchStringParam(input, "since"),
		Until:      searchStringParam(input, "until"),
		Kind:       searchStringParam(input, "kind"),
		MaxResults: 0,
	})
	if toolErr != nil {
		return tools.PythonJSONDumps(toolErr.ToMap()), true, true
	}

	days, totalItems, daysWithItems, truncated := groupTimelineByDay(result.Items, maxDays, perDay)
	return tools.PythonJSONDumps(map[string]any{
		"days":                   days,
		"total_items":            totalItems,
		"days_with_items":        daysWithItems,
		"truncated":              truncated,
		"sessions_with_timeline": result.SessionsWithTimeline,
	}), false, true
}

// groupTimelineByDay folds the engine's oldest-first items into day buckets,
// newest day first. count is every item for that day; items is the capped
// summary list. truncated reports any cap that actually dropped something.
func groupTimelineByDay(items []memory.SearchTimelineItem, maxDays, perDay int) (
	days []memoryTimelineDay, totalItems, daysWithItems int, truncated bool,
) {
	order := make([]string, 0, 8)
	byDay := make(map[string][]memoryTimelineEntry, 8)
	for _, item := range items {
		day := timelineDayOf(item.When)
		if day == "" {
			continue
		}
		if _, seen := byDay[day]; !seen {
			order = append(order, day)
		}
		byDay[day] = append(byDay[day], memoryTimelineEntry{
			Kind:    item.Kind,
			When:    item.When,
			Text:    truncateTimelineText(redactTimelineText(item.Text), MemoryTimelineTextLimit),
			Session: item.Session,
		})
	}
	daysWithItems = len(order)
	for _, entries := range byDay {
		totalItems += len(entries)
	}

	// The engine sorts oldest first; browsing wants the recent days first.
	sort.Sort(sort.Reverse(sort.StringSlice(order)))
	if len(order) > maxDays {
		order = order[:maxDays]
		truncated = true
	}

	days = make([]memoryTimelineDay, 0, len(order))
	for _, day := range order {
		entries := byDay[day]
		count := len(entries)
		if len(entries) > perDay {
			entries = entries[:perDay]
			truncated = true
		}
		days = append(days, memoryTimelineDay{Day: day, Count: count, Items: entries})
	}
	return days, totalItems, daysWithItems, truncated
}

// timelineDayOf returns the day an item is filed under. A range item
// ("2026-01-01..2026-01-05") is filed under its start day; a malformed date is
// dropped rather than becoming a junk bucket.
func timelineDayOf(when string) string {
	if index := strings.Index(when, ".."); index >= 0 {
		when = when[:index]
	}
	when = strings.TrimSpace(when)
	if _, err := time.Parse("2006-01-02", when); err != nil {
		return ""
	}
	return when
}

func clampTimelineRange(value, fallback, max int) int {
	if value <= 0 {
		value = fallback
	}
	if value > max {
		value = max
	}
	return value
}

// timelineSecretPatterns redacts the common secret shapes a user might have
// dictated into a conversation ("my key is sk-…", "password=…"). The label is
// kept so the summary still reads as prose.
var timelineSecretPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?key|secret|token|password|passwd|pwd)\s*[:=]\s*)\S+`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._\-]{12,}`), "${1}[redacted]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{12,}`), "[redacted]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[redacted]"},
}

func redactTimelineText(text string) string {
	for _, secret := range timelineSecretPatterns {
		text = secret.pattern.ReplaceAllString(text, secret.replacement)
	}
	return text
}

func truncateTimelineText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

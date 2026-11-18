package memory

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func searchTimelineItem(text, start, end, kind string, aliases []string, turn int) map[string]any {
	item := map[string]any{
		"turn":  turn,
		"kind":  kind,
		"text":  text,
		"start": start,
		"end":   end,
	}
	if aliases != nil {
		item["aliases"] = aliases
	}
	return item
}

func writeSearchTimelineSession(t *testing.T, home, session string, items []map[string]any) string {
	t.Helper()
	dir := filepath.Join(home, "queens", "queen_timeline", "sessions", session)
	mkdirAll(t, dir)
	writeFile(t, filepath.Join(dir, "events.jsonl"),
		`{"type":"client_input_received","data":{"content":"..."}}`+"\n")
	var lines []string
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	writeFile(t, filepath.Join(dir, TimelineFile), strings.Join(lines, "\n")+"\n")
	return dir
}

func seedWeddings(t *testing.T, home string) {
	t.Helper()
	writeSearchTimelineSession(t, home, "session_20230901_100000_cccc0001", []map[string]any{
		searchTimelineItem("The user attended cousin Rachel's vineyard wedding.", "2023-08-01", "2023-08-31", "event", []string{"went to a wedding"}, 1),
		searchTimelineItem("The user is planning their own beach wedding.", "2023-09-01", "2023-09-01", "plan", nil, 1),
	})
	writeSearchTimelineSession(t, home, "session_20231015_100000_cccc0002", []map[string]any{
		searchTimelineItem("The user attended Jen and Tom's barn wedding.", "2023-10-07", "2023-10-08", "event", nil, 1),
		searchTimelineItem("The user was a bridesmaid in 2021.", "2021-06-12", "2021-06-12", "event", []string{"wedding party"}, 1),
	})
}

func timelineEnv() map[string]string {
	return map[string]string{"HIVE_QUEEN_ID": "queen_timeline"}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_search_filters_by_date_overlap_and_sorts_oldest_first
func TestSearchTimelineFiltersByDateOverlapAndSortsOldestFirst(t *testing.T) {
	home := searchHome(t)
	seedWeddings(t, home)

	got, err := SearchTimeline(home, ScopeRef{Scope: SearchScopeQueens, Owner: "queen_timeline"}, SearchTimelineOptions{
		Pattern: "wedding", Since: "2023-01-01", Kind: "event",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].Text != "The user attended cousin Rachel's vineyard wedding." ||
		got.Items[1].Text != "The user attended Jen and Tom's barn wedding." {
		t.Fatalf("order = %+v", got.Items)
	}
	if got.Items[0].When != "2023-08-01..2023-08-31" {
		t.Fatalf("when = %q", got.Items[0].When)
	}
	if got.Items[0].SaidOn == nil || *got.Items[0].SaidOn != "2023-09-01" {
		t.Fatalf("said_on = %v", got.Items[0].SaidOn)
	}
	if got.SessionsWithTimeline != 2 {
		t.Fatalf("sessions_with_timeline = %d", got.SessionsWithTimeline)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_search_matches_aliases_and_kinds
func TestSearchTimelineMatchesAliasesAndKinds(t *testing.T) {
	home := searchHome(t)
	seedWeddings(t, home)
	ref := ScopeRef{Scope: SearchScopeQueens, Owner: "queen_timeline"}

	aliases, err := SearchTimeline(home, ref, SearchTimelineOptions{Pattern: "wedding party"})
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases.Items) != 1 || aliases.Items[0].Text != "The user was a bridesmaid in 2021." {
		t.Fatalf("alias items = %+v", aliases.Items)
	}

	plans, err := SearchTimeline(home, ref, SearchTimelineOptions{Pattern: ".", Kind: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans.Items) != 1 || plans.Items[0].Kind != "plan" {
		t.Fatalf("plan items = %+v", plans.Items)
	}

	august, err := SearchTimeline(home, ref, SearchTimelineOptions{
		Pattern: "rachel", Since: "2023-08-20", Until: "2023-08-21",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(august.Items) != 1 {
		t.Fatalf("overlap items = %+v", august.Items)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_search_truncates_and_reports_total
func TestSearchTimelineTruncatesAndReportsTotal(t *testing.T) {
	home := searchHome(t)
	seedWeddings(t, home)

	got, err := SearchTimeline(home, ScopeRef{Scope: SearchScopeQueens, Owner: "queen_timeline"}, SearchTimelineOptions{
		Pattern: ".", MaxResults: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Total != 4 || !got.Truncated {
		t.Fatalf("result = %+v", got)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_search_is_empty_without_timelines
func TestSearchTimelineEmptyWithoutTimelines(t *testing.T) {
	home := searchHome(t)
	mkdirAll(t, filepath.Join(home, "queens", "queen_timeline", "sessions", "session_20230901_100000_dddd0001"))

	result := SearchTimelineEnvelope(timelineEnv(), home, SearchTimelineOptions{Pattern: "."})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	items, ok := result["items"].([]SearchTimelineItem)
	if !ok || len(items) != 0 {
		t.Fatalf("items = %v (%T)", result["items"], result["items"])
	}
	if result["total"] != 0 || result["truncated"] != false || result["sessions_with_timeline"] != 0 {
		t.Fatalf("envelope = %v", result)
	}
}

// Parity: tools/tests/test_memory_recall_timeline.py::test_colony_scope_covers_every_overseeing_queens_sessions
// — the Python test exercises recall over colony roots; the timeline roots are
// the same paths.session_roots walk, so this pins the ported path resolution.
func TestSearchTimelineColonyScopeCoversOverseerQueens(t *testing.T) {
	home := searchHome(t)
	for _, entry := range []struct{ queen, session, text string }{
		{"queen_a", "session_20230301_100000_eeee0001", "We shipped the billing migration to Postgres."},
		{"queen_b", "session_20230302_100000_eeee0002", "The billing migration rollback plan is ready."},
	} {
		dir := filepath.Join(home, "colonies", "acme", "queens", entry.queen, "sessions", entry.session)
		mkdirAll(t, dir)
		writeFile(t, filepath.Join(dir, TimelineFile), `{"turn":1,"kind":"event","text":"`+entry.text+`","start":"2023-03-01","end":"2023-03-01","aliases":[]}`+"\n")
	}

	got, err := SearchTimeline(home, ScopeRef{Scope: SearchScopeColonies, Owner: "acme"}, SearchTimelineOptions{Pattern: "billing migration"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	sessions := map[string]bool{}
	for _, item := range got.Items {
		sessions[item.Session] = true
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %v", sessions)
	}
}

// Parity-exempt: no Python test covers search_timeline's pattern_invalid
// branch; this mirrors the re.compile guard in memory_tools/timeline.py.
func TestSearchTimelineRejectsInvalidPattern(t *testing.T) {
	home := searchHome(t)
	seedWeddings(t, home)

	result := SearchTimelineEnvelope(timelineEnv(), home, SearchTimelineOptions{Pattern: "("})
	if result["error"] != "pattern_invalid" {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity-exempt: no Python test covers search_timeline's filter_invalid
// branch; this mirrors tool.py::_parse_iso before timeline.search runs.
func TestSearchTimelineRejectsInvalidFilter(t *testing.T) {
	home := searchHome(t)
	seedWeddings(t, home)

	result := SearchTimelineEnvelope(timelineEnv(), home, SearchTimelineOptions{Pattern: ".", Since: "nope"})
	if result["error"] != "filter_invalid" {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity-exempt: the [1, 300] clamp lives in the tool wrapper (timeline.py::
// register_search_timeline) and has no dedicated Python test.
func TestSearchTimelineClampsMaxResults(t *testing.T) {
	home := searchHome(t)
	seedWeddings(t, home)

	result := SearchTimelineEnvelope(timelineEnv(), home, SearchTimelineOptions{Pattern: ".", MaxResults: 5000})
	if result["total"] != 4 {
		t.Fatalf("total = %v", result["total"])
	}
	if items := result["items"].([]SearchTimelineItem); len(items) != 4 {
		t.Fatalf("kept = %d", len(items))
	}
}

// Parity-exempt: no Python test feeds malformed timeline.jsonl lines; this
// mirrors timeline.search's per-line ValueError skip.
func TestSearchTimelineSkipsMalformedLines(t *testing.T) {
	home := searchHome(t)
	dir := filepath.Join(home, "queens", "queen_timeline", "sessions", "session_20230901_100000_ffff0001")
	mkdirAll(t, dir)
	writeFile(t, filepath.Join(dir, TimelineFile),
		"not json\n"+`{"kind":"event","text":"kept birthday","start":"2023-01-01","end":"2023-01-01"}`+"\n"+
			`{"kind":"event","text":"missing dates"}`+"\n")

	got, err := SearchTimeline(home, ScopeRef{Scope: SearchScopeQueens, Owner: "queen_timeline"}, SearchTimelineOptions{Pattern: "birthday"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Text != "kept birthday" {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].Kind != "event" {
		t.Fatalf("kind = %q", got.Items[0].Kind)
	}
}

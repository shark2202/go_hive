package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func writeMemoryTimelineConfig(t *testing.T, home string, enabled bool) {
	t.Helper()
	config := "{}"
	if enabled {
		config = `{"memory_timeline": true}`
	}
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

// timelineTestHome builds one queen with a past session whose timeline spans
// four days, including a range item and a secret-shaped fact.
func timelineTestHome(t *testing.T) (home, queen, pastSession, liveSession string) {
	t.Helper()
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	home = t.TempDir()
	queen = "queen_timeline"
	pastSession = "session_20260101_090000_aaaa1111"
	liveSession = "session_20260102_100000_bbbb2222"

	past := filepath.Join(home, "queens", queen, "sessions", pastSession)
	if err := os.MkdirAll(past, 0o755); err != nil {
		t.Fatal(err)
	}
	records := []string{
		`{"kind":"event","text":"renewed the HIVE-731 domain","start":"2026-01-01","end":"2026-01-01","turn":2}`,
		`{"kind":"fact","text":"api_key=sk-ABCDEFGHIJKLMNOP is in the vault","start":"2026-01-02","end":"2026-01-02","turn":3}`,
		`{"kind":"event","text":"shipped the rate limiter","start":"2026-01-02","end":"2026-01-02","turn":4}`,
		`{"kind":"event","text":"office move","start":"2026-02-10","end":"2026-02-12","turn":5}`,
		`{"kind":"plan","text":"renew the certificate in March","start":"2026-03-01","end":"2026-03-01","turn":6}`,
	}
	if err := os.WriteFile(filepath.Join(past, "timeline.jsonl"),
		[]byte(strings.Join(records, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	live := filepath.Join(home, "queens", queen, "sessions", liveSession)
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMemoryTimelineConfig(t, home, true)
	return home, queen, pastSession, liveSession
}

func timelineTestCatalogue(home, queen, liveSession string) *sessionToolCatalogue {
	return searchTestCatalogue(filepath.Join(home, "queens", queen, "sessions", liveSession), home)
}

// Parity-exempt: upstream has no browse-by-day tool; the timeline engine it
// reuses is covered by tools/src/memory_tools/timeline.py::search_timeline.
func TestMemoryTimelineBrowseGroupsByDayNewestFirst(t *testing.T) {
	home, queen, pastSession, liveSession := timelineTestHome(t)
	catalogue := timelineTestCatalogue(home, queen, liveSession)

	content, isError, err := catalogue.Execute(context.Background(), MemoryTimelineToolName, map[string]any{})
	if err != nil || isError {
		t.Fatalf("memory_timeline: err=%v isError=%v content=%s", err, isError, content)
	}
	payload := decodeToolPayload(t, content)

	rawDays, _ := payload["days"].([]any)
	if len(rawDays) != 4 {
		t.Fatalf("days = %d, want 4 (%v)", len(rawDays), payload["days"])
	}
	wantOrder := []string{"2026-03-01", "2026-02-10", "2026-01-02", "2026-01-01"}
	for i, want := range wantOrder {
		day, _ := rawDays[i].(map[string]any)
		if day["day"] != want {
			t.Fatalf("day[%d] = %v, want %s (newest first)", i, day["day"], want)
		}
	}
	janTwo, _ := rawDays[2].(map[string]any)
	if count, _ := janTwo["count"].(float64); count != 2 {
		t.Fatalf("2026-01-02 count = %v, want 2", janTwo["count"])
	}
	if total, _ := payload["total_items"].(float64); total != 5 {
		t.Fatalf("total_items = %v, want 5", payload["total_items"])
	}
	if days, _ := payload["days_with_items"].(float64); days != 4 {
		t.Fatalf("days_with_items = %v, want 4", payload["days_with_items"])
	}
	if truncated, _ := payload["truncated"].(bool); truncated {
		t.Fatal("uncapped browse reported truncation")
	}
	if sessions, _ := payload["sessions_with_timeline"].(float64); sessions != 1 {
		t.Fatalf("sessions_with_timeline = %v, want 1", payload["sessions_with_timeline"])
	}

	// The secret is redacted; the surrounding prose and the source session
	// survive so the queen can drill in with search_messages.
	joined := content
	if strings.Contains(joined, "sk-ABCDEFGHIJKLMNOP") {
		t.Fatalf("browse leaked the API key: %s", joined)
	}
	if !strings.Contains(joined, "api_key=[redacted]") {
		t.Fatalf("browse did not redact the labeled secret: %s", joined)
	}
	if !strings.Contains(joined, pastSession) {
		t.Fatalf("browse dropped the source session: %s", joined)
	}
}

// Parity-exempt: range/kind filtering reuses the upstream search_timeline
// semantics; this locks the browse surface onto them.
func TestMemoryTimelineBrowseHonoursRangeAndKind(t *testing.T) {
	home, queen, _, liveSession := timelineTestHome(t)
	catalogue := timelineTestCatalogue(home, queen, liveSession)

	content, isError, err := catalogue.Execute(context.Background(), MemoryTimelineToolName, map[string]any{
		"since": "2026-01-02",
		"kind":  "fact",
	})
	if err != nil || isError {
		t.Fatalf("memory_timeline: err=%v isError=%v content=%s", err, isError, content)
	}
	payload := decodeToolPayload(t, content)
	rawDays, _ := payload["days"].([]any)
	if len(rawDays) != 1 {
		t.Fatalf("days = %v, want only 2026-01-02", payload["days"])
	}
	day, _ := rawDays[0].(map[string]any)
	if day["day"] != "2026-01-02" {
		t.Fatalf("day = %v", day["day"])
	}
	items, _ := day["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want the one fact", items)
	}
	item, _ := items[0].(map[string]any)
	if item["kind"] != "fact" {
		t.Fatalf("kind = %v, want fact", item["kind"])
	}
}

// Parity-exempt: browse caps are Go-only presentation limits.
func TestMemoryTimelineBrowseCapsDaysAndPerDay(t *testing.T) {
	home, queen, _, liveSession := timelineTestHome(t)
	catalogue := timelineTestCatalogue(home, queen, liveSession)

	content, isError, err := catalogue.Execute(context.Background(), MemoryTimelineToolName, map[string]any{
		"max_days": 2,
		"per_day":  1,
	})
	if err != nil || isError {
		t.Fatalf("memory_timeline: err=%v isError=%v content=%s", err, isError, content)
	}
	payload := decodeToolPayload(t, content)
	rawDays, _ := payload["days"].([]any)
	if len(rawDays) != 2 {
		t.Fatalf("days = %d, want the 2 newest", len(rawDays))
	}
	newest, _ := rawDays[0].(map[string]any)
	if newest["day"] != "2026-03-01" {
		t.Fatalf("newest day = %v", newest["day"])
	}
	second, _ := rawDays[1].(map[string]any)
	if second["day"] != "2026-02-10" {
		t.Fatalf("second day = %v", second["day"])
	}
	if items, _ := second["items"].([]any); len(items) != 1 {
		t.Fatalf("per_day=1 returned %d items", len(items))
	}
	if truncated, _ := payload["truncated"].(bool); !truncated {
		t.Fatal("capped browse did not report truncation")
	}
	if total, _ := payload["total_items"].(float64); total != 5 {
		t.Fatalf("total_items = %v, want the uncapped 5", payload["total_items"])
	}
}

// Parity-exempt: the feature flag is shared with search_timeline; this locks
// that the browse tool is invisible and refuses to run while it is off.
func TestMemoryTimelineFeatureFlagGatesAdvertiseAndExecution(t *testing.T) {
	home, queen, _, liveSession := timelineTestHome(t)
	writeMemoryTimelineConfig(t, home, false)
	catalogue := timelineTestCatalogue(home, queen, liveSession)

	if defs := memoryTimelineToolDefs(catalogue.home); len(defs) != 0 {
		t.Fatalf("memory_timeline advertised while the feature is off: %v", defs)
	}
	content, isError, _ := catalogue.Execute(context.Background(), MemoryTimelineToolName, map[string]any{})
	if !isError {
		t.Fatalf("memory_timeline ran with the feature off: %s", content)
	}
	if payload := decodeToolPayload(t, content); payload["error"] != "feature_disabled" {
		t.Fatalf("error = %v, want feature_disabled", payload["error"])
	}

	writeMemoryTimelineConfig(t, home, true)
	defs := memoryTimelineToolDefs(catalogue.home)
	if len(defs) != 1 || defs[0].Name != MemoryTimelineToolName {
		t.Fatalf("defs = %v, want one memory_timeline", defs)
	}
	content, isError, err := catalogue.Execute(context.Background(), MemoryTimelineToolName, map[string]any{})
	if err != nil || isError {
		t.Fatalf("memory_timeline with the feature on: err=%v isError=%v content=%s", err, isError, content)
	}
}

// Parity-exempt: assembly guard — the browse tool must reach the real session
// catalogue the chat runtime builds, not just a hand-made struct.
func TestMemoryTimelineAdvertisedInSessionCatalogue(t *testing.T) {
	home, queen, _, liveSession := timelineTestHome(t)
	catalogue := newSessionToolCatalogue(
		filepath.Join(home, "queens", queen, "sessions", liveSession),
		nil, hivehome.Home{Root: home})
	names := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		names[def.Name] = struct{}{}
	}
	if _, ok := names[MemoryTimelineToolName]; !ok {
		t.Fatal("memory_timeline missing from the session catalogue")
	}
	if _, ok := names[SearchTimelineToolName]; !ok {
		t.Fatal("search_timeline missing from the session catalogue")
	}
}

// Parity-exempt: redaction and summary capping are Go-only presentation rules.
func TestMemoryTimelineRedactsAndTruncates(t *testing.T) {
	long := strings.Repeat("进展", 180) // 360 runes
	redacted := redactTimelineText("token: abc123 secret and Bearer abcdefghijklmnop")
	if strings.Contains(redacted, "abc123") || strings.Contains(redacted, "abcdefghijklmnop") {
		t.Fatalf("redaction leaked a secret: %s", redacted)
	}
	if !strings.Contains(redacted, "token: [redacted]") {
		t.Fatalf("token label lost: %s", redacted)
	}
	truncated := truncateTimelineText(long, MemoryTimelineTextLimit)
	if got := len([]rune(truncated)); got != MemoryTimelineTextLimit+1 {
		t.Fatalf("truncated length = %d, want %d + ellipsis", got, MemoryTimelineTextLimit)
	}
	if !strings.HasSuffix(truncated, "…") {
		t.Fatalf("truncated text lost its ellipsis: %q", truncated)
	}
}

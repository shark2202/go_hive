package maintenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const queenTestSessionID = "session_20250101_090000_cafecafe"

func ageTree(t *testing.T, root string, days float64) {
	t.Helper()
	old := time.Now().Add(-time.Duration(days * testDay * float64(time.Second)))
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	})
	if err != nil {
		t.Fatalf("age %s: %v", root, err)
	}
}

func eventLine(t *testing.T, evtType string, data map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"type":      evtType,
		"stream_id": "queen",
		"data":      data,
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(encoded)
}

func buildQueenSession(t *testing.T, home hivehome.Home, queen, sid string, ageDays float64) string {
	t.Helper()
	sdir := filepath.Join(home.QueensDir(), queen, "sessions", sid)
	parts := filepath.Join(sdir, "conversations", "parts")
	if err := os.MkdirAll(parts, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(sdir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestJSON(t, filepath.Join(parts, "0000000001.json"), map[string]any{
		"seq": 1, "role": "tool",
		"content": "Full result at: " + dataDir + "/terminal_exec_15.txt (grep it)",
	})
	writeTestJSON(t, filepath.Join(parts, "0000000002.json"), map[string]any{
		"seq": 2, "role": "tool",
		"content": "Old result [Saved to 'web_scrape_3.txt']",
	})
	writeTestJSON(t, filepath.Join(parts, "0000000003.json"), map[string]any{
		"seq": 3, "role": "user",
		"content": "Previous conversation saved at conversation_1.md",
	})
	writeTestJSON(t, filepath.Join(sdir, "conversations", "cursor.json"), map[string]any{
		"next_seq": 4,
		"outputs":  map[string]any{"report": "Output saved at: " + dataDir + "/output_report.json"},
	})

	writeTestFile(t, filepath.Join(dataDir, "terminal_exec_15.txt"), "kept-abs-path")
	writeTestFile(t, filepath.Join(dataDir, "web_scrape_3.txt"), "kept-legacy-format")
	writeTestFile(t, filepath.Join(dataDir, "output_report.json"), "kept-cursor-ref")
	writeTestFile(t, filepath.Join(dataDir, "browser_snapshot_7.txt"), strings.Repeat("o", 400))
	writeTestFile(t, filepath.Join(dataDir, "conversation_1.md"), "handoff transcript")
	writeTestFile(t, filepath.Join(dataDir, "attachments", "upload.pdf"), "user upload")

	lines := []string{
		eventLine(t, "tool_call_started", map[string]any{"tool_name": "web_scrape"}),
		eventLine(t, "context_usage_updated", map[string]any{
			"usage_pct":    10,
			"full_request": map[string]any{"messages": []any{strings.Repeat("m", 2000)}},
		}),
		eventLine(t, "tool_call_completed", map[string]any{"tool_name": "web_scrape", "result": strings.Repeat("r", 50)}),
		"THIS IS NOT JSON {",
		eventLine(t, "context_usage_updated", map[string]any{
			"usage_pct":    55,
			"full_request": map[string]any{"messages": []any{strings.Repeat("m", 2000)}},
		}),
		eventLine(t, "client_output_delta", map[string]any{"text": "hello"}),
	}
	writeTestFile(t, filepath.Join(sdir, "events.jsonl"), strings.Join(lines, "\n")+"\n")

	for _, tree := range []string{"events", "data"} {
		writeTestFile(t, filepath.Join(home.Root, ".message_index", tree, "queens", queen, sid, "000001.tool.txt"), "indexed copy")
	}
	meta := filepath.Join(home.Root, ".message_index", "meta", "queens", queen, sid)
	writeTestFile(t, filepath.Join(meta, "cursor.json"), `{"events_byte_offset": 10, "next_ordinal": 2}`)
	writeTestFile(t, filepath.Join(meta, "data_map.json"), "{}")

	ageTree(t, sdir, ageDays)
	return sdir
}

func splitLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func snapshotTree(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = -1
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		out[p] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_orphan_detection_protects_all_reference_formats
func TestOrphanDetectionProtectsAllReferenceFormats(t *testing.T) {
	home := testHome(t)
	sdir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)

	orphans := FindOrphanSpillovers(sdir, 30, nowSeconds())
	var names []string
	for _, p := range orphans {
		names = append(names, filepath.Base(p))
	}
	if !reflect.DeepEqual(names, []string{"browser_snapshot_7.txt"}) {
		t.Fatalf("orphans = %v, want [browser_snapshot_7.txt]", names)
	}
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_queen_hygiene_end_to_end
func TestQueenHygieneEndToEnd(t *testing.T) {
	home := testHome(t)
	sdir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)
	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10

	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{3: true},
		Execute: true,
	})

	dataDir := filepath.Join(sdir, "data")
	if pathExists(filepath.Join(dataDir, "browser_snapshot_7.txt")) {
		t.Fatal("orphan spillover should be removed")
	}
	for _, keep := range []string{"terminal_exec_15.txt", "web_scrape_3.txt", "output_report.json", "conversation_1.md"} {
		if !pathExists(filepath.Join(dataDir, keep)) {
			t.Fatalf("%s is referenced by a live message and must survive", keep)
		}
	}
	if !pathExists(filepath.Join(dataDir, "attachments", "upload.pdf")) {
		t.Fatal("user uploads must survive")
	}

	var types []string
	for _, line := range splitLines(t, filepath.Join(sdir, "events.jsonl")) {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			types = append(types, "<corrupt>")
			continue
		}
		evtType, ok := obj["type"].(string)
		if !ok {
			types = append(types, "<corrupt>")
			continue
		}
		types = append(types, evtType)
	}
	want := []string{"tool_call_started", "tool_call_completed", "<corrupt>", "client_output_delta"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	rewritten, err := os.ReadFile(filepath.Join(sdir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewritten), "THIS IS NOT JSON {") {
		t.Fatal("corrupt lines preserved verbatim")
	}
	if tmp, _ := filepath.Glob(filepath.Join(sdir, "*.janitor-tmp")); len(tmp) != 0 {
		t.Fatalf("leftover rewrite temp: %v", tmp)
	}
	if pathExists(filepath.Join(sdir, ".janitor.lock")) {
		t.Fatal("session lock must be released")
	}
	if !pathExists(filepath.Join(sdir, ".pruned.json")) {
		t.Fatal("expected a .pruned.json marker")
	}

	idx := filepath.Join(home.Root, ".message_index")
	if pathExists(filepath.Join(idx, "events", "queens", "q1", queenTestSessionID)) {
		t.Fatal("events index subtree should be reset")
	}
	if pathExists(filepath.Join(idx, "data", "queens", "q1", queenTestSessionID)) {
		t.Fatal("data index subtree should be reset")
	}
	if pathExists(filepath.Join(idx, "meta", "queens", "q1", queenTestSessionID, "cursor.json")) {
		t.Fatal("index cursor should be removed")
	}

	tier3 := findTarget(report, "queen_hygiene")
	if tier3 == nil {
		t.Fatal("missing queen_hygiene target report")
	}
	if tier3.BytesFreed <= 0 {
		t.Fatalf("bytes_freed = %d, want > 0", tier3.BytesFreed)
	}
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_rewrite_strips_legacy_full_request_from_kept_lines
func TestRewriteStripsLegacyFullRequestFromKeptLines(t *testing.T) {
	home := testHome(t)
	sdir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)
	events := filepath.Join(sdir, "events.jsonl")

	lines := splitLines(t, events)
	lines = append(lines, eventLine(t, "llm_turn_complete", map[string]any{
		"iteration":    1,
		"full_request": map[string]any{"messages": []any{strings.Repeat("x", 3000)}},
	}))
	writeTestFile(t, events, strings.Join(lines, "\n")+"\n")
	ageTree(t, sdir, 60)

	RewriteEventsJSONL(events, 10, DeleteDisposer{Home: home}, &Manifest{})

	kept, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(kept), "full_request") {
		t.Fatal("legacy full_request payload should be stripped from kept lines")
	}
	keptTypes := map[string]bool{}
	for _, line := range splitLines(t, events) {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if evtType, ok := obj["type"].(string); ok {
			keptTypes[evtType] = true
		}
	}
	if !keptTypes["llm_turn_complete"] {
		t.Fatal("llm_turn_complete is a real event and must be kept")
	}
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_rewrite_skips_small_files_and_low_savings
func TestRewriteSkipsSmallFilesAndLowSavings(t *testing.T) {
	home := testHome(t)
	sdir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)
	events := filepath.Join(sdir, "events.jsonl")
	original, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}

	if got := RewriteEventsJSONL(events, 1_000_000_000, DeleteDisposer{Home: home}, &Manifest{}); got != 0 {
		t.Fatalf("rewrite returned %d bytes, want 0 below min_bytes", got)
	}
	after, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("file below min_bytes must be untouched")
	}

	big := make([]string, 0, 11)
	for i := 0; i < 10; i++ {
		big = append(big, eventLine(t, "tool_call_completed", map[string]any{"result": strings.Repeat("k", 5000)}))
	}
	big = append(big, eventLine(t, "context_usage_updated", map[string]any{"usage_pct": 1}))
	writeTestFile(t, events, strings.Join(big, "\n")+"\n")
	before, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if got := RewriteEventsJSONL(events, 10, DeleteDisposer{Home: home}, &Manifest{}); got != 0 {
		t.Fatalf("rewrite returned %d bytes, want 0 under the 10%% savings gate", got)
	}
	after, err = os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("low-savings rewrite must not touch the file")
	}
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_active_session_untouched
func TestActiveSessionUntouched(t *testing.T) {
	home := testHome(t)
	sdir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)
	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10

	safety := &SafetyContext{
		ProtectedSessionIDs: map[string]bool{queenTestSessionID: true},
		LiveSessionDirs:     map[string]bool{},
		GraceSeconds:        0,
		Now:                 nowSeconds(),
	}
	report := RunOnce(home, safety, &cfg, RunOptions{
		Tiers:   map[int]bool{3: true},
		Execute: true,
	})

	if !pathExists(filepath.Join(sdir, "data", "browser_snapshot_7.txt")) {
		t.Fatal("a live session's spillovers are never touched")
	}
	if pathExists(filepath.Join(sdir, ".pruned.json")) {
		t.Fatal("a live session is never marked pruned")
	}
	tier3 := findTarget(report, "queen_hygiene")
	if tier3 == nil {
		t.Fatal("missing queen_hygiene target report")
	}
	if tier3.Skipped < 1 {
		t.Fatalf("skipped = %d, want >= 1", tier3.Skipped)
	}
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_recently_active_session_untouched
func TestRecentlyActiveSessionUntouched(t *testing.T) {
	home := testHome(t)
	sdir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)
	now := time.Now()
	cursor := filepath.Join(sdir, "conversations", "cursor.json")
	if err := os.Chtimes(cursor, now, now); err != nil {
		t.Fatal(err)
	}

	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10
	RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{3: true},
		Execute: true,
	})

	if !pathExists(filepath.Join(sdir, "data", "browser_snapshot_7.txt")) {
		t.Fatal("a session active inside the gate must be untouched")
	}
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_dry_run_changes_nothing
func TestDryRunChangesNothing(t *testing.T) {
	home := testHome(t)
	sdir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)
	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10

	before := snapshotTree(t, sdir)
	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{3: true},
		Execute: false,
	})
	after := snapshotTree(t, sdir)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("dry run must not change anything on disk")
	}
	tier3 := findTarget(report, "queen_hygiene")
	if tier3 == nil {
		t.Fatal("missing queen_hygiene target report")
	}
	if tier3.BytesFreed <= 0 {
		t.Fatalf("dry-run bytes_freed = %d, want > 0", tier3.BytesFreed)
	}
}

// Parity: core/tests/test_janitor_queen_hygiene.py::test_colony_overseer_sessions_are_covered
func TestColonyOverseerSessionsAreCovered(t *testing.T) {
	home := testHome(t)
	sid := "session_20250101_080000_feedfeed"
	sdir := filepath.Join(home.ColoniesDir(), "c9", "queens", "qz", "sessions", sid)
	if err := os.MkdirAll(filepath.Join(sdir, "conversations", "parts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sdir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sdir, "data", "orphan_1.txt"), strings.Repeat("x", 200))
	lines := make([]string, 0, 4)
	for i := 0; i < 3; i++ {
		lines = append(lines, eventLine(t, "context_usage_updated", map[string]any{
			"full_request": map[string]any{"m": strings.Repeat("y", 500)},
		}))
	}
	lines = append(lines, eventLine(t, "tool_call_completed", map[string]any{"result": "keep"}))
	writeTestFile(t, filepath.Join(sdir, "events.jsonl"), strings.Join(lines, "\n")+"\n")
	ageTree(t, sdir, 90)

	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10
	RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{3: true},
		Execute: true,
	})

	if pathExists(filepath.Join(sdir, "data", "orphan_1.txt")) {
		t.Fatal("orphan spillover under a colony overseer should be removed")
	}
	kept, err := os.ReadFile(filepath.Join(sdir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(kept), "context_usage_updated") {
		t.Fatal("telemetry events should be dropped")
	}
	if !strings.Contains(string(kept), "tool_call_completed") {
		t.Fatal("real events should be kept")
	}
}

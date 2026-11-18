package maintenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testCfg() RetentionConfig {
	cfg := DefaultRetentionConfig()
	cfg.ActiveGraceHours = 0
	cfg.IOSleepMS = 0
	cfg.Mode = "delete"
	return cfg
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeTestJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeTestFile(t, path, string(data))
}

func findTarget(report *JanitorReport, name string) *TargetReport {
	for i := range report.Targets {
		if report.Targets[i].Name == name {
			return &report.Targets[i]
		}
	}
	return nil
}

// Parity: core/tests/test_janitor_safety.py::test_parse_session_dir_ts
func TestParseSessionDirTS(t *testing.T) {
	ts, ok := ParseSessionDirTS("session_20260101_120000_deadbeef")
	if !ok {
		t.Fatal("expected timestamp for session_20260101_120000_deadbeef")
	}
	if year := time.Unix(int64(ts), 0).Local().Year(); year != 2026 {
		t.Fatalf("year = %d, want 2026", year)
	}
	if _, ok := ParseSessionDirTS("not_a_session"); ok {
		t.Fatal("expected no timestamp for not_a_session")
	}
}

// Parity: core/tests/test_janitor_safety.py::test_assert_safe_target_refuses_protected_and_escaping_paths
func TestAssertSafeTargetRefusesProtectedAndEscapingPaths(t *testing.T) {
	home := testHome(t)
	for _, name := range []string{"memories", "credentials", "secrets", "charts"} {
		if err := AssertSafeTarget(home, filepath.Join(home.Root, name, "x.md")); err == nil {
			t.Fatalf("expected %s to be refused", name)
		}
	}
	if err := AssertSafeTarget(home, "/etc/passwd"); err == nil {
		t.Fatal("expected /etc/passwd to be refused")
	}
	if err := AssertSafeTarget(home, filepath.Join(home.Root, "..", "elsewhere")); err == nil {
		t.Fatal("expected escaping path to be refused")
	}
	if err := AssertSafeTarget(home, filepath.Join(home.Root, "event_logs", "x.jsonl")); err != nil {
		t.Fatalf("expected event_logs target to pass: %v", err)
	}
}

// Parity: core/tests/test_janitor_safety.py::test_kill_switch_blocks_run
func TestKillSwitchBlocksRun(t *testing.T) {
	home := testHome(t)
	cfg := testCfg()
	cfg.Enabled = false
	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{1: true},
		Execute: true,
	})
	if !strings.Contains(report.Error, "disabled") {
		t.Fatalf("error = %q, want it to mention disabled", report.Error)
	}
	if len(report.Targets) != 0 {
		t.Fatalf("targets = %v, want none", report.Targets)
	}
}

// Parity: core/tests/test_janitor_safety.py::test_retention_config_file_and_env_precedence
func TestRetentionConfigFileAndEnvPrecedence(t *testing.T) {
	home := testHome(t)
	writeTestJSON(t, home.ConfigurationFile(), map[string]any{
		"retention": map[string]any{"event_logs_days": 3, "mode": "delete", "enabled": true},
	})
	cfg := LoadRetentionConfig(home)
	if cfg.EventLogsDays != 3 {
		t.Fatalf("event_logs_days = %d, want 3", cfg.EventLogsDays)
	}
	if cfg.Mode != "delete" {
		t.Fatalf("mode = %q, want delete", cfg.Mode)
	}

	t.Setenv("HIVE_RETENTION_EVENT_LOGS_DAYS", "9")
	t.Setenv("HIVE_RETENTION_ENABLED", "0")
	cfg = LoadRetentionConfig(home)
	if cfg.EventLogsDays != 9 {
		t.Fatalf("env should beat configuration.json, got %d", cfg.EventLogsDays)
	}
	if cfg.Enabled {
		t.Fatal("enabled should be false from env")
	}

	writeTestJSON(t, home.ConfigurationFile(), map[string]any{
		"retention": map[string]any{"event_logs_days": "soon", "enabled": 1},
	})
	_ = os.Unsetenv("HIVE_RETENTION_EVENT_LOGS_DAYS")
	_ = os.Unsetenv("HIVE_RETENTION_ENABLED")
	cfg = LoadRetentionConfig(home)
	if cfg.EventLogsDays != 7 {
		t.Fatalf("wrong-typed value should be ignored, got %d", cfg.EventLogsDays)
	}
	if !cfg.Enabled {
		t.Fatal("wrong-typed enabled should be ignored, want default true")
	}
}

// Parity: core/tests/test_janitor_safety.py::test_global_lock_single_flight_and_stale_takeover
func TestGlobalLockSingleFlightAndStaleTakeover(t *testing.T) {
	home := testHome(t)
	cfg := testCfg()
	lock := GlobalLockPath(home)
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, lock, `{"pid": 1}`)

	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{1: true},
		Execute: true,
	})
	if !strings.Contains(report.Error, "in progress") {
		t.Fatalf("error = %q, want in progress", report.Error)
	}

	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	report = RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{1: true},
		Execute: true,
	})
	if report.Error != "" {
		t.Fatalf("error = %q, want empty after stale takeover", report.Error)
	}
	if pathExists(lock) {
		t.Fatal("lock released after the run")
	}

	writeTestFile(t, lock, `{"pid": 1}`)
	report = RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{1: true},
		Execute: false,
	})
	if report.Error != "" {
		t.Fatalf("dry run should not contend for the lock, got %q", report.Error)
	}
}

// Parity: core/tests/test_janitor_safety.py::test_leftover_sweep_removes_stale_residue_only
func TestLeftoverSweepRemovesStaleResidueOnly(t *testing.T) {
	home := testHome(t)
	staleTmp := filepath.Join(home.Root, "queens", "q", "sessions", "s", "events.jsonl.janitor-tmp")
	writeTestFile(t, staleTmp, "partial")
	staleTrash := filepath.Join(home.Root, "colonies", "c", "workers", "w.jtrash-123")
	writeTestFile(t, filepath.Join(staleTrash, "f"), "x")
	old := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{staleTmp, staleTrash, filepath.Join(staleTrash, "f")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	freshTmp := filepath.Join(home.Root, "queens", "q", "sessions", "s", "other.janitor-tmp")
	writeTestFile(t, freshTmp, "in flight")

	report := SweepJanitorLeftovers(home, &Manifest{})
	if pathExists(staleTmp) {
		t.Fatal("stale tmp should be removed")
	}
	if pathExists(staleTrash) {
		t.Fatal("stale trash should be removed")
	}
	if !pathExists(freshTmp) {
		t.Fatal("fresh residue may belong to a live run")
	}
	if report.Files != 2 {
		t.Fatalf("files = %d, want 2", report.Files)
	}
}

// Parity: core/tests/test_janitor_safety.py::test_legacy_pass_never_deletes_the_agents_tree
func TestLegacyPassNeverDeletesTheAgentsTree(t *testing.T) {
	home := testHome(t)
	agents := filepath.Join(home.Root, "agents")
	session := filepath.Join(agents, "queens", "q1", "sessions", "session_20250101_000000_deadbeef")
	if err := os.MkdirAll(filepath.Join(session, "conversations", "parts"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentDef := filepath.Join(agents, "x_rapid_replier")
	writeTestFile(t, filepath.Join(agentDef, "profile.yaml"), "name: x")

	old := time.Now().Add(-90 * 24 * time.Hour)
	_ = filepath.WalkDir(session, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			_ = os.Chtimes(p, old, old)
		}
		return nil
	})

	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:         map[int]bool{},
		Execute:       true,
		IncludeLegacy: true,
	})

	if !pathExists(agents) {
		t.Fatal("agents tree must survive")
	}
	if !pathExists(agentDef) {
		t.Fatal("agent definitions under agents/ are live, never touched")
	}
	if !pathExists(session) {
		t.Fatal("legacy pass is hygiene-only; transcripts survive")
	}
	if got := IterLegacyQueenSessions(home); !reflect.DeepEqual(got, []string{session}) {
		t.Fatalf("iter_legacy_queen_sessions = %v, want [%s]", got, session)
	}
}

// Parity: core/tests/test_janitor_safety.py::test_legacy_sessions_get_tier3_hygiene
func TestLegacySessionsGetTier3Hygiene(t *testing.T) {
	home := testHome(t)
	session := filepath.Join(home.Root, "agents", "queens", "q1", "sessions", "session_20250101_000000_deadbeef")
	if err := os.MkdirAll(filepath.Join(session, "conversations", "parts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(session, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(session, "data", "orphan_9.txt"), strings.Repeat("x", 300))
	events := []string{}
	for i := 0; i < 3; i++ {
		events = append(events, `{"type": "context_usage_updated", "data": {"full_request": {"m": "`+strings.Repeat("y", 400)+`"}}}`)
	}
	events = append(events, `{"type": "tool_call_completed", "data": {"result": "keep"}}`)
	writeTestFile(t, filepath.Join(session, "events.jsonl"), strings.Join(events, "\n")+"\n")
	old := time.Now().Add(-90 * 24 * time.Hour)
	_ = filepath.WalkDir(session, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			_ = os.Chtimes(p, old, old)
		}
		return nil
	})

	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10
	RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:         map[int]bool{},
		Execute:       true,
		IncludeLegacy: true,
	})

	if pathExists(filepath.Join(session, "data", "orphan_9.txt")) {
		t.Fatal("orphan spillover should be removed")
	}
	kept, err := os.ReadFile(filepath.Join(session, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(kept), "context_usage_updated") {
		t.Fatal("telemetry events should be dropped")
	}
	if !strings.Contains(string(kept), "tool_call_completed") {
		t.Fatal("real events should be kept")
	}
	if !pathExists(filepath.Join(session, "conversations")) {
		t.Fatal("conversations dir should survive")
	}
}

// Parity: core/tests/test_janitor_safety.py::test_orphan_message_index_sweep
func TestOrphanMessageIndexSweep(t *testing.T) {
	home := testHome(t)
	idx := filepath.Join(home.Root, ".message_index")
	liveSrc := filepath.Join(home.Root, "agents", "queens", "qa", "sessions", "s1")
	writeTestFile(t, filepath.Join(liveSrc, "events.jsonl"), "{}")
	for _, item := range [][3]string{{"queens", "qa", "s1"}, {"queens", "qa", "s2"}, {"colonies", "cb", "s3"}} {
		for _, tree := range []string{"events", "data", "meta"} {
			writeTestFile(t, filepath.Join(idx, tree, item[0], item[1], item[2], "stub"), "x")
		}
	}

	report := SweepOrphanMessageIndex(home, DeleteDisposer{Home: home}, &Manifest{})
	if !pathExists(filepath.Join(idx, "meta", "queens", "qa", "s1")) {
		t.Fatal("live session kept")
	}
	if pathExists(filepath.Join(idx, "meta", "queens", "qa", "s2")) {
		t.Fatal("orphan meta tree should be removed")
	}
	if pathExists(filepath.Join(idx, "events", "queens", "qa", "s2")) {
		t.Fatal("orphan events tree should be removed")
	}
	if pathExists(filepath.Join(idx, "meta", "colonies", "cb", "s3")) {
		t.Fatal("orphan colony meta tree should be removed")
	}
	if report.Files != 6 {
		t.Fatalf("files = %d, want 6", report.Files)
	}

	for _, tree := range []string{"events", "data", "meta"} {
		writeTestFile(t, filepath.Join(idx, tree, "queens", "qa", "s4", "stub"), "x")
	}
	report = SweepOrphanMessageIndex(home, DryRunDisposer{}, &Manifest{})
	if !pathExists(filepath.Join(idx, "meta", "queens", "qa", "s4")) {
		t.Fatal("dry run must not delete")
	}
	if report.Files != 3 {
		t.Fatalf("dry-run files = %d, want 3", report.Files)
	}
}

// Parity: core/tests/test_janitor_safety.py::test_junk_detection_reports_but_never_deletes_without_flag
func TestJunkDetectionReportsButNeverDeletesWithoutFlag(t *testing.T) {
	home := testHome(t)
	junk := filepath.Join(home.Root, "os")
	writeTestFile(t, junk, "%!PS"+strings.Repeat("x", 100))
	cfg := testCfg()
	cfg.JunkMinBytes = 10
	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{},
		Execute: true,
	})
	if !pathExists(junk) {
		t.Fatal("junk needs the explicit include_junk flag")
	}
	junkReport := findTarget(report, "junk")
	if junkReport == nil {
		t.Fatal("missing junk target report")
	}
	if junkReport.Skipped != 1 {
		t.Fatalf("junk skipped = %d, want 1", junkReport.Skipped)
	}

	report = RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:       map[int]bool{},
		Execute:     true,
		IncludeJunk: true,
	})
	if pathExists(junk) {
		t.Fatal("junk should be deleted with include_junk")
	}
}

// Parity: core/tests/test_janitor_safety.py::test_protected_toplevel_entries_never_appear_as_junk
func TestProtectedToplevelEntriesNeverAppearAsJunk(t *testing.T) {
	home := testHome(t)
	writeTestFile(t, filepath.Join(home.Root, "memories", "fact.md"), strings.Repeat("x", 200))
	writeTestFile(t, filepath.Join(home.Root, "charts", "c.png"), strings.Repeat("x", 200))
	cfg := testCfg()
	cfg.JunkMinBytes = 1
	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:       map[int]bool{},
		Execute:     true,
		IncludeJunk: true,
	})
	if !pathExists(filepath.Join(home.Root, "memories", "fact.md")) {
		t.Fatal("memories must survive")
	}
	if !pathExists(filepath.Join(home.Root, "charts", "c.png")) {
		t.Fatal("charts must survive")
	}
	junkReport := findTarget(report, "junk")
	if junkReport == nil {
		t.Fatal("missing junk target report")
	}
	if junkReport.Files != 0 {
		t.Fatalf("junk files = %d, want 0", junkReport.Files)
	}
}

// Parity: core/tests/test_janitor_safety.py::test_report_and_manifest_written_every_run
func TestReportAndManifestWrittenEveryRun(t *testing.T) {
	home := testHome(t)
	cfg := testCfg()
	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{1: true},
		Execute: false,
	})
	if report.ManifestPath != "" && !pathExists(report.ManifestPath) {
		t.Fatalf("manifest path %q does not exist", report.ManifestPath)
	}
	saved, ok := LoadLastReport(home)
	if !ok {
		t.Fatal("expected a saved report")
	}
	obj, ok := saved.(map[string]any)
	if !ok {
		t.Fatalf("saved report is %T, want object", saved)
	}
	if obj["dry_run"] != true {
		t.Fatalf("dry_run = %v, want true", obj["dry_run"])
	}
	if !reflect.DeepEqual(obj["tiers"], []any{float64(1)}) {
		t.Fatalf("tiers = %#v, want [1]", obj["tiers"])
	}
}

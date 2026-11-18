package maintenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const reviewSessionID = "session_20250101_070000_beadbead"

func buildReviewSession(t *testing.T, home hivehome.Home, sid string) string {
	t.Helper()
	sdir := filepath.Join(home.QueensDir(), "qx", "sessions", sid)
	if err := os.MkdirAll(filepath.Join(sdir, "conversations", "parts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sdir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, 4)
	for i := 0; i < 3; i++ {
		lines = append(lines, eventLine(t, "context_usage_updated", map[string]any{
			"full_request": map[string]any{"m": strings.Repeat("z", 500)},
		}))
	}
	lines = append(lines, eventLine(t, "tool_call_completed", map[string]any{"result": "keep"}))
	writeTestFile(t, filepath.Join(sdir, "events.jsonl"), strings.Join(lines, "\n")+"\n")
	ageTree(t, sdir, 60)
	return sdir
}

// Parity: core/tests/test_janitor_review_fixes.py::test_orphan_scan_protects_current_header_after_prune_placeholder
func TestOrphanScanProtectsCurrentHeaderAfterPrunePlaceholder(t *testing.T) {
	home := testHome(t)
	sdir := buildReviewSession(t, home, reviewSessionID)
	dataDir := filepath.Join(sdir, "data")
	writeTestFile(t, filepath.Join(dataDir, "terminal_exec_15.txt"), "big")
	writeTestFile(t, filepath.Join(dataDir, "greppable_7.txt"), "big")

	parts := filepath.Join(sdir, "conversations", "parts")
	writeTestJSON(t, filepath.Join(parts, "0000000001.json"), map[string]any{
		"seq":     1,
		"role":    "tool",
		"content": "... Full result at: " + filepath.Join(dataDir, "terminal_exec_15.txt"),
	})
	writeTestJSON(t, filepath.Join(parts, "0000000002.json"), map[string]any{
		"seq":     2,
		"role":    "assistant",
		"content": "",
		"tool_calls": []any{map[string]any{
			"id": "call_1",
			"function": map[string]any{
				"name":      "terminal_exec",
				"arguments": `{"command": "grep -n foo ` + filepath.Join(dataDir, "greppable_7.txt") + `"}`,
			},
		}},
	})
	ageTree(t, sdir, 60)

	orphans := FindOrphanSpillovers(sdir, 30, nowSeconds())
	if len(orphans) != 0 {
		t.Fatalf("orphans = %v, want none", orphans)
	}
}

// Parity: core/tests/test_janitor_review_fixes.py::test_orphan_scan_fails_closed_on_read_error
func TestOrphanScanFailsClosedOnReadError(t *testing.T) {
	home := testHome(t)
	sdir := buildReviewSession(t, home, reviewSessionID)
	writeTestFile(t, filepath.Join(sdir, "data", "would_be_orphan_1.txt"), strings.Repeat("x", 100))
	writeTestJSON(t, filepath.Join(sdir, "conversations", "parts", "0000000001.json"), map[string]any{
		"seq": 1, "role": "tool", "content": "hi",
	})
	ageTree(t, sdir, 60)

	previous := readTextFile
	readTextFile = func(path string) (string, error) {
		if filepath.Base(path) == "0000000001.json" {
			return "", errors.New("transient I/O error")
		}
		return previous(path)
	}
	t.Cleanup(func() { readTextFile = previous })

	if orphans := FindOrphanSpillovers(sdir, 30, nowSeconds()); len(orphans) != 0 {
		t.Fatalf("orphans = %v, want none (fail closed on read error)", orphans)
	}
}

// Parity: core/tests/test_janitor_review_fixes.py::test_own_lock_does_not_mask_liveness_recheck
func TestOwnLockDoesNotMaskLivenessRecheck(t *testing.T) {
	home := testHome(t)
	sdir := buildReviewSession(t, home, reviewSessionID)
	writeTestFile(t, filepath.Join(sdir, ".janitor.lock"), `{"pid": 1}`)

	safety := &SafetyContext{
		ProtectedSessionIDs: map[string]bool{reviewSessionID: true},
		LiveSessionDirs:     map[string]bool{},
		GraceSeconds:        0,
	}
	ok, reason := safety.IsSafeToPrune(sdir, 30, true)
	if ok || !strings.Contains(reason, "live") {
		t.Fatalf("ok=%v reason=%q, liveness must be evaluated even under our own lock", ok, reason)
	}
}

// Parity: core/tests/test_janitor_review_fixes.py::test_refresher_blocks_mid_run_resume
func TestRefresherBlocksMidRunResume(t *testing.T) {
	home := testHome(t)
	sdir := buildReviewSession(t, home, reviewSessionID)
	eventsPath := filepath.Join(sdir, "events.jsonl")
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10
	safety := &SafetyContext{
		ProtectedSessionIDs: map[string]bool{},
		LiveSessionDirs:     map[string]bool{},
		GraceSeconds:        0,
		Refresher: func() (map[string]bool, map[string]bool) {
			return map[string]bool{reviewSessionID: true}, map[string]bool{}
		},
	}
	report := PruneQueenSession(home, sdir, cfg, safety, DeleteDisposer{Home: home}, &Manifest{})

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("a session that resumed mid-run must not be rewritten")
	}
	if report.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", report.Skipped)
	}
	if pathExists(filepath.Join(sdir, ".janitor.lock")) {
		t.Fatal("session lock must be released")
	}
}

// Parity: core/tests/test_janitor_review_fixes.py::test_offline_execute_refused_when_server_marker_live
func TestOfflineExecuteRefusedWhenServerMarkerLive(t *testing.T) {
	home := testHome(t)
	port := 1234
	WriteServerMarker(home, &port)
	writeTestJSON(t, ServerMarkerPath(home), map[string]any{
		"pid": os.Getppid(), "started_at": nowSeconds(),
	})

	cfg := testCfg()
	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true, 3: true},
		Execute: true,
	})
	if !strings.Contains(report.Error, "live runtime owns this HIVE_HOME") {
		t.Fatalf("error = %q, want the live-runtime refusal", report.Error)
	}

	report = RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{1: true},
		Execute: true,
	})
	if report.Error != "" {
		t.Fatalf("tier 1 error = %q, want empty", report.Error)
	}

	report = RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true, 3: true},
		Execute: false,
	})
	if report.Error != "" {
		t.Fatalf("dry-run error = %q, want empty", report.Error)
	}

	writeTestJSON(t, ServerMarkerPath(home), map[string]any{
		"pid": 2<<21 + 12345, "started_at": nowSeconds(),
	})
	report = RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true, 3: true},
		Execute: true,
	})
	if report.Error != "" {
		t.Fatalf("error = %q, want empty after a dead pid", report.Error)
	}
}

// Parity: core/tests/test_janitor_review_fixes.py::test_server_marker_lifecycle
func TestServerMarkerLifecycle(t *testing.T) {
	home := testHome(t)
	port := 8787
	WriteServerMarker(home, &port)

	data, err := os.ReadFile(ServerMarkerPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var marker map[string]any
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	if pid, ok := marker["pid"].(float64); !ok || int(pid) != os.Getpid() {
		t.Fatalf("pid = %v, want %d", marker["pid"], os.Getpid())
	}
	if LiveServerOwnsHiveHome(home) {
		t.Fatal("own pid must not look like a foreign live server")
	}
	ClearServerMarker(home)
	if pathExists(ServerMarkerPath(home)) {
		t.Fatal("cleared marker should be gone")
	}
}

// Parity: core/tests/test_janitor_review_fixes.py::test_global_lock_not_leaked_on_disposer_setup_failure
func TestGlobalLockNotLeakedOnDisposerSetupFailure(t *testing.T) {
	home := testHome(t)
	cfg := testCfg()
	cfg.Mode = "archive"

	previous := BuildDisposer
	BuildDisposer = func(home hivehome.Home, cfg RetentionConfig, execute bool) (Disposer, error) {
		return nil, errors.New("archive dir on read-only fs")
	}
	t.Cleanup(func() { BuildDisposer = previous })

	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})
	if !strings.Contains(report.Error, "disposer setup failed") {
		t.Fatalf("error = %q, want disposer setup failed", report.Error)
	}
	if pathExists(GlobalLockPath(home)) {
		t.Fatal("no 2h lockout may be left behind")
	}
	if _, ok := LoadLastReport(home); !ok {
		t.Fatal("a failure still produces a report")
	}

	BuildDisposer = previous
	report = RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})
	if report.Error != "" {
		t.Fatalf("follow-up error = %q, want empty", report.Error)
	}
}

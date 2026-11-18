package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const testDay = 86400.0

func testHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return home
}

func writeAged(t *testing.T, path, body string, ageDays float64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	old := time.Now().Add(-time.Duration(ageDays * testDay * float64(time.Second)))
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

func logName(ageDays float64) string {
	return time.Now().Add(-time.Duration(ageDays*testDay*float64(time.Second))).Format("20060102_150405") + ".jsonl"
}

func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// Parity: core/tests/test_janitor_tier1.py::test_tier1_filename_ts_parsers
func TestTier1FilenameTSParsers(t *testing.T) {
	if _, ok := tier1FileTS("20260515_131452.jsonl"); !ok {
		t.Fatal("expected timestamp for 20260515_131452.jsonl")
	}
	if _, ok := tier1FileTS("20260429T002854_346307_queen.md"); !ok {
		t.Fatal("expected timestamp for compaction-log name")
	}
	ts, ok := tier1FileTS("browser_snapshot_1783031564665_tab2.txt")
	if !ok {
		t.Fatal("expected epoch-ms timestamp for browser snapshot")
	}
	if diff := ts - 1783031564.665; diff > 1 || diff < -1 {
		t.Fatalf("browser snapshot ts = %v, want ~1783031564.665", ts)
	}
	if _, ok := tier1FileTS("random-name.log"); ok {
		t.Fatal("expected no timestamp for random-name.log")
	}
}

// Parity: core/tests/test_janitor_tier1.py::test_prune_aged_dir_deletes_old_keeps_new_and_newest
func TestPruneAgedDirDeletesOldKeepsNewAndNewest(t *testing.T) {
	home := testHome(t)
	d := filepath.Join(home.Root, "event_logs")
	oldA := writeAged(t, filepath.Join(d, logName(30)), strings.Repeat("x", 100), 30)
	oldB := writeAged(t, filepath.Join(d, logName(20)), strings.Repeat("y", 50), 20)
	fresh := writeAged(t, filepath.Join(d, logName(1)), "z", 1)

	report := PruneAgedDir(PruneAgedDirOptions{
		Dir:        d,
		Target:     "event_logs",
		MaxAgeDays: 7,
		Disposer:   DeleteDisposer{Home: home},
		Manifest:   &Manifest{},
		KeepNewest: 1,
		Now:        nowSeconds(),
	})
	if pathExists(oldA) {
		t.Fatal("old_a should be deleted")
	}
	if pathExists(oldB) {
		t.Fatal("old_b should be deleted")
	}
	if !pathExists(fresh) {
		t.Fatal("fresh should survive")
	}
	if report.Files != 2 {
		t.Fatalf("files = %d, want 2", report.Files)
	}
	if report.BytesFreed != 150 {
		t.Fatalf("bytes_freed = %d, want 150", report.BytesFreed)
	}
}

// Parity: core/tests/test_janitor_tier1.py::test_prune_aged_dir_keep_newest_survives_even_if_old
func TestPruneAgedDirKeepNewestSurvivesEvenIfOld(t *testing.T) {
	home := testHome(t)
	d := filepath.Join(home.Root, "event_logs")
	only := writeAged(t, filepath.Join(d, logName(90)), "x", 90)

	PruneAgedDir(PruneAgedDirOptions{
		Dir:        d,
		Target:     "event_logs",
		MaxAgeDays: 7,
		Disposer:   DeleteDisposer{Home: home},
		Manifest:   &Manifest{},
		KeepNewest: 1,
		Now:        nowSeconds(),
	})
	if !pathExists(only) {
		t.Fatal("the newest file per dir is never deleted (may be held open)")
	}
}

// Parity: core/tests/test_janitor_tier1.py::test_prune_aged_dir_skips_files_written_since_process_start
func TestPruneAgedDirSkipsFilesWrittenSinceProcessStart(t *testing.T) {
	home := testHome(t)
	d := filepath.Join(home.Root, "event_logs")
	heldOpen := filepath.Join(d, logName(60))
	if err := os.MkdirAll(filepath.Dir(heldOpen), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(heldOpen, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	decoy := writeAged(t, filepath.Join(d, logName(59)), "old", 59)

	processStart := nowSeconds() - 3600
	PruneAgedDir(PruneAgedDirOptions{
		Dir:            d,
		Target:         "event_logs",
		MaxAgeDays:     7,
		Disposer:       DeleteDisposer{Home: home},
		Manifest:       &Manifest{},
		KeepNewest:     0,
		ProcessStartTS: &processStart,
		Now:            nowSeconds(),
	})
	if !pathExists(heldOpen) {
		t.Fatal("held-open file should survive")
	}
	if pathExists(decoy) {
		t.Fatal("decoy should be deleted")
	}
}

// Parity: core/tests/test_janitor_tier1.py::test_prune_aged_dir_mtime_fallback_for_unparseable_names
func TestPruneAgedDirMtimeFallbackForUnparseableNames(t *testing.T) {
	home := testHome(t)
	d := filepath.Join(home.Root, "logs")
	rotatedOld := writeAged(t, filepath.Join(d, "sentinel.log.3"), "aaa", 45)
	rotatedNew := writeAged(t, filepath.Join(d, "sentinel.log.1"), "bbb", 2)
	live := writeAged(t, filepath.Join(d, "sentinel.log"), "ccc", 0)

	PruneAgedDir(PruneAgedDirOptions{
		Dir:        d,
		Target:     "logs",
		MaxAgeDays: 30,
		Disposer:   DeleteDisposer{Home: home},
		Manifest:   &Manifest{},
		KeepNewest: 2,
		Now:        nowSeconds(),
	})
	if pathExists(rotatedOld) {
		t.Fatal("rotated_old should be deleted")
	}
	if !pathExists(rotatedNew) {
		t.Fatal("rotated_new should survive")
	}
	if !pathExists(live) {
		t.Fatal("live should survive")
	}
}

// Parity: core/tests/test_janitor_tier1.py::test_prune_aged_dir_dry_run_reports_without_deleting
func TestPruneAgedDirDryRunReportsWithoutDeleting(t *testing.T) {
	home := testHome(t)
	d := filepath.Join(home.Root, "compaction_log")
	old := writeAged(t, filepath.Join(d, "20250101T000000_000001_queen.md"), strings.Repeat("m", 64), 200)
	manifest := &Manifest{}
	report := PruneAgedDir(PruneAgedDirOptions{
		Dir:        d,
		Target:     "compaction_log",
		MaxAgeDays: 7,
		Disposer:   DryRunDisposer{},
		Manifest:   manifest,
		KeepNewest: 0,
		Now:        nowSeconds(),
	})
	if !pathExists(old) {
		t.Fatal("dry run must not delete")
	}
	if report.BytesFreed != 64 {
		t.Fatalf("bytes_freed = %d, want 64", report.BytesFreed)
	}
	if len(manifest.Items) != 1 {
		t.Fatalf("manifest items = %d, want 1", len(manifest.Items))
	}
	if manifest.Items[0].Outcome != "candidate" {
		t.Fatalf("outcome = %q, want candidate", manifest.Items[0].Outcome)
	}
	if manifest.Items[0].Bytes != 64 {
		t.Fatalf("item bytes = %d, want 64", manifest.Items[0].Bytes)
	}
}

// Parity: core/tests/test_janitor_tier1.py::test_prune_aged_dir_missing_dir_is_noop
func TestPruneAgedDirMissingDirIsNoop(t *testing.T) {
	home := testHome(t)
	report := PruneAgedDir(PruneAgedDirOptions{
		Dir:        filepath.Join(home.Root, "does_not_exist"),
		Target:     "event_logs",
		MaxAgeDays: 7,
		Disposer:   DeleteDisposer{Home: home},
		Manifest:   &Manifest{},
	})
	if report.Files != 0 || report.BytesFreed != 0 {
		t.Fatalf("missing dir should be a noop, got files=%d bytes=%d", report.Files, report.BytesFreed)
	}
}

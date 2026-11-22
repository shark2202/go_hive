package maintenance

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const workerTestID = "session_20250101_120000_deadbeef"

func buildWorker(t *testing.T, home hivehome.Home, colony, wid string, ageDays float64) string {
	t.Helper()
	wdir := filepath.Join(home.ColoniesDir(), colony, "workers", wid)
	if err := os.MkdirAll(filepath.Join(wdir, "conversations", "parts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wdir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestJSON(t, filepath.Join(wdir, "meta.json"), map[string]any{
		"worker_id":        wid,
		"queen_session_id": "session_20250101_110000_beefbeef",
		"colony_id":        colony,
		"task":             "do the thing",
		"profile_name":     "researcher",
	})
	writeTestFile(t, filepath.Join(wdir, "tasks.json"), `{"tasks": []}`)
	writeTestFile(t, filepath.Join(wdir, "reminder_state.json"), `{"turns_total": 3}`)
	writeTestFile(t, filepath.Join(wdir, "os"), "%!PS-Adobe-3.0 stray junk")

	parts := filepath.Join(wdir, "conversations", "parts")
	writeTestJSON(t, filepath.Join(parts, "0000000001.json"), map[string]any{"seq": 1, "role": "user", "content": "task"})
	writeTestJSON(t, filepath.Join(parts, "0000000002.json"), map[string]any{"seq": 2, "role": "assistant", "content": "working on it"})
	writeTestJSON(t, filepath.Join(parts, "0000000003.json"), map[string]any{"seq": 3, "role": "tool", "content": "result", "tool_use_id": "call_1"})
	writeTestJSON(t, filepath.Join(parts, "0000000004.json"), map[string]any{"seq": 4, "role": "assistant", "content": "final summary of the work"})
	writeTestFile(t, filepath.Join(wdir, "conversations", "cursor.json"), `{"next_seq": 5}`)
	writeTestFile(t, filepath.Join(wdir, "data", "web_scrape_1.txt"), strings.Repeat("x", 500))

	for _, tree := range []string{"events", "data", "meta"} {
		writeTestFile(t, filepath.Join(home.Root, ".message_index", tree, "colonies", colony, wid, "stub.txt"), "copy")
	}

	ageTree(t, wdir, ageDays)
	return wdir
}

func sortedTreePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)
	return paths
}

func readTarGz(t *testing.T, path, wantSuffix string) ([]string, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	var body string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar %s: %v", path, err)
		}
		names = append(names, hdr.Name)
		if strings.HasSuffix(hdr.Name, wantSuffix) {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read member %s: %v", hdr.Name, err)
			}
			body = string(data)
		}
	}
	return names, body
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_deep_clean_end_to_end
func TestDeepCleanEndToEnd(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)
	cfg := testCfg()
	cfg.WorkerDeepCleanDays = 14

	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})

	for _, gone := range []string{"conversations", "data", "reminder_state.json", "os"} {
		if pathExists(filepath.Join(wdir, gone)) {
			t.Fatalf("%s should be removed", gone)
		}
	}
	for _, keep := range []string{"meta.json", "tasks.json"} {
		if !pathExists(filepath.Join(wdir, keep)) {
			t.Fatalf("%s is in the worker keep-set", keep)
		}
	}

	data, err := os.ReadFile(filepath.Join(wdir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tombstone map[string]any
	if err := json.Unmarshal(data, &tombstone); err != nil {
		t.Fatal(err)
	}
	if tombstone["status"] != "completed" {
		t.Fatalf("status = %v, want completed", tombstone["status"])
	}
	if tombstone["summary"] != "final summary of the work" {
		t.Fatalf("summary = %v, want the last assistant message", tombstone["summary"])
	}
	if _, ok := tombstone["_janitor"]; !ok {
		t.Fatal("tombstone must record _janitor metadata")
	}

	for _, tree := range []string{"events", "data", "meta"} {
		if pathExists(filepath.Join(home.Root, ".message_index", tree, "colonies", "c1", workerTestID)) {
			t.Fatalf("%s message-index subtree should be co-deleted", tree)
		}
	}

	tier2 := findTarget(report, "worker_deep_clean")
	if tier2 == nil {
		t.Fatal("missing worker_deep_clean target report")
	}
	if tier2.BytesFreed <= 0 {
		t.Fatalf("bytes_freed = %d, want > 0", tier2.BytesFreed)
	}
	if report.ManifestPath == "" || !pathExists(report.ManifestPath) {
		t.Fatalf("manifest %q should exist", report.ManifestPath)
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_deep_clean_is_idempotent
func TestDeepCleanIsIdempotent(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)
	cfg := testCfg()
	cfg.WorkerDeepCleanDays = 14

	RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})
	firstData, err := os.ReadFile(filepath.Join(wdir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}

	report2 := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})
	secondData, err := os.ReadFile(filepath.Join(wdir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstData) != string(secondData) {
		t.Fatal("existing tombstone is never rewritten")
	}
	tier2 := findTarget(report2, "worker_deep_clean")
	if tier2 == nil {
		t.Fatal("missing worker_deep_clean target report")
	}
	if tier2.BytesFreed != 0 {
		t.Fatalf("bytes_freed = %d, want 0 on a second pass", tier2.BytesFreed)
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_crash_resume_converges
func TestCrashResumeConverges(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)
	tombstone := map[string]any{
		"status":   "completed",
		"summary":  "s",
		"_janitor": map[string]any{"pruned_at": "x"},
	}
	writeTestJSON(t, filepath.Join(wdir, "result.json"), tombstone)
	ageTree(t, wdir, 30)

	report := DeepCleanWorker(home, "c1", workerTestID, wdir, DeleteDisposer{Home: home}, &Manifest{})

	if pathExists(filepath.Join(wdir, "conversations")) {
		t.Fatal("conversations should be gone")
	}
	if pathExists(filepath.Join(wdir, "data")) {
		t.Fatal("data should be gone")
	}
	data, err := os.ReadFile(filepath.Join(wdir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var kept map[string]any
	if err := json.Unmarshal(data, &kept); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kept, tombstone) {
		t.Fatalf("tombstone changed: %#v", kept)
	}
	if report.BytesFreed <= 0 {
		t.Fatalf("bytes_freed = %d, want > 0", report.BytesFreed)
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_recent_worker_is_skipped
func TestRecentWorkerIsSkipped(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 1.0)
	cfg := testCfg()
	cfg.WorkerDeepCleanDays = 14

	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})

	if !pathExists(filepath.Join(wdir, "conversations")) {
		t.Fatal("a worker inside the window must be untouched")
	}
	tier2 := findTarget(report, "worker_deep_clean")
	if tier2 == nil {
		t.Fatal("missing worker_deep_clean target report")
	}
	if tier2.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", tier2.Skipped)
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_worker_of_live_queen_is_skipped
func TestWorkerOfLiveQueenIsSkipped(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)
	cfg := testCfg()
	cfg.WorkerDeepCleanDays = 14

	safety := &SafetyContext{
		ProtectedSessionIDs: map[string]bool{"session_20250101_110000_beefbeef": true},
		LiveSessionDirs:     map[string]bool{},
		GraceSeconds:        0,
		Now:                 nowSeconds(),
	}
	RunOnce(home, safety, &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})

	if !pathExists(filepath.Join(wdir, "conversations")) {
		t.Fatal("spawning queen live => worker untouched")
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_dry_run_changes_nothing_and_lists_candidates
func TestDryRunChangesNothingAndListsCandidates(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)
	cfg := testCfg()
	cfg.WorkerDeepCleanDays = 14

	before := sortedTreePaths(t, wdir)
	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: false,
	})
	after := sortedTreePaths(t, wdir)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("dry run must not change anything on disk")
	}
	if pathExists(filepath.Join(wdir, "result.json")) {
		t.Fatal("dry run must not write a tombstone")
	}
	tier2 := findTarget(report, "worker_deep_clean")
	if tier2 == nil {
		t.Fatal("missing worker_deep_clean target report")
	}
	if tier2.BytesFreed <= 0 {
		t.Fatalf("dry-run bytes_freed = %d, want > 0", tier2.BytesFreed)
	}
	manifest, err := os.ReadFile(report.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(manifest), "\n"), "\n") {
		if strings.Contains(line, `"candidate"`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dry-run manifest should list candidates")
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_archive_mode_round_trip
func TestArchiveModeRoundTrip(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)
	originalPart, err := os.ReadFile(filepath.Join(wdir, "conversations", "parts", "0000000004.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCfg()
	cfg.WorkerDeepCleanDays = 14
	cfg.Mode = "archive"

	report := RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})

	if pathExists(filepath.Join(wdir, "conversations")) {
		t.Fatal("archived worker's conversations should be removed")
	}
	if report.ArchivePath == "" {
		t.Fatal("archive mode records the run's archive dir")
	}
	info, err := os.Stat(report.ArchivePath)
	if err != nil || !info.IsDir() {
		t.Fatalf("archive dir %q is not a directory", report.ArchivePath)
	}
	tarballs, err := filepath.Glob(filepath.Join(report.ArchivePath, "*.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(tarballs)
	if len(tarballs) == 0 {
		t.Fatal("one durable tarball per disposal target")
	}
	if tmps, _ := filepath.Glob(filepath.Join(report.ArchivePath, "*.tmp")); len(tmps) != 0 {
		t.Fatalf("torn in-flight archives left behind: %v", tmps)
	}

	relPrefix := "colonies/c1/workers/" + workerTestID + "/conversations"
	memberSuffix := relPrefix + "/parts/0000000004.json"
	var names []string
	var restored string
	for _, tb := range tarballs {
		tbNames, body := readTarGz(t, tb, memberSuffix)
		names = append(names, tbNames...)
		if body != "" {
			restored = body
		}
	}
	foundPrefix := false
	for _, name := range names {
		if strings.HasPrefix(name, relPrefix) {
			foundPrefix = true
			break
		}
	}
	if !foundPrefix {
		t.Fatalf("archive members %v should include %s", names, relPrefix)
	}
	if restored != string(originalPart) {
		t.Fatalf("restored part = %q, want %q", restored, string(originalPart))
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_archive_failure_keeps_source
func TestArchiveFailureKeepsSource(t *testing.T) {
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)

	disposer, err := NewArchiveDisposer(home, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := archiveOneFunc
	archiveOneFunc = func(d *ArchiveDisposer, path string) error {
		return errors.New("disk full")
	}
	t.Cleanup(func() { archiveOneFunc = previous })

	if _, _, err := disposer.DisposeDir(filepath.Join(wdir, "conversations")); err == nil {
		t.Fatal("expected the archive failure to surface")
	}
	if !pathExists(filepath.Join(wdir, "conversations", "parts", "0000000004.json")) {
		t.Fatal("a failed archive must never let the source be deleted")
	}
}

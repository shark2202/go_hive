package maintenance

// Parity: core/framework/maintenance/janitor.py::write_server_marker
// Parity: core/framework/maintenance/janitor.py::_save_report
// Parity: core/framework/maintenance/retention.py::prune_queen_session
//
// Every one of these writes goes through framework/utils/io.py::atomic_write,
// whose tempfile.mkstemp descriptor starts at 0600 and whose os.replace keeps
// that mode. The Go port must not widen the result to 0644.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func requireOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("%s permission = %#o, want 0600", filepath.Base(path), perm)
	}
}

// Parity-exempt: 0600 artifact modes mirror framework/utils/io.py::atomic_write; no upstream pytest asserts file modes.
func TestMaintenanceArtifactsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not model POSIX file modes")
	}
	home := testHome(t)

	WriteServerMarker(home, nil)
	requireOwnerOnly(t, ServerMarkerPath(home))

	if err := SaveReport(home, &JanitorReport{StartedAt: nowSeconds(), Mode: "delete"}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	requireOwnerOnly(t, filepath.Join(MaintenanceDir(home), ReportFilename))

	// Tier-3 pruning writes the session's .pruned.json marker only after a
	// successful rewrite; reuse the queen fixture the hygiene tests already
	// exercise.
	sessionDir := buildQueenSession(t, home, "q1", queenTestSessionID, 60)
	cfg := testCfg()
	cfg.QueenHygieneDays = 30
	cfg.EventsRewriteMin = 10
	RunOnce(home, ForOffline(cfg, nowSeconds()), &cfg, RunOptions{
		Tiers:   map[int]bool{3: true},
		Execute: true,
	})
	requireOwnerOnly(t, filepath.Join(sessionDir, ".pruned.json"))
}

// Parity: core/framework/maintenance/retention.py::deep_clean_worker (the
// result.json tombstone is written through atomic_write, whose mkstemp temp
// starts at 0600).
func TestWorkerTombstoneIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not model POSIX file modes")
	}
	home := testHome(t)
	wdir := buildWorker(t, home, "c1", workerTestID, 30)
	DeepCleanWorker(home, "c1", workerTestID, wdir, DeleteDisposer{Home: home}, &Manifest{})
	requireOwnerOnly(t, filepath.Join(wdir, "result.json"))
}

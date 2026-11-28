package storage

// Parity: core/framework/utils/io.py::atomic_write — tempfile.mkstemp creates
// the temp file 0600 and os.replace keeps that mode. Python writes run
// documents (framework/storage/concurrent.py), conversation parts/meta/cursor/
// partials (framework/storage/conversation_store.py) and the session summary
// (framework/storage/session_summary.py) through it, so the Go writers must
// keep the same owner-only mode instead of widening it to 0644.
//
// Parity-exempt: no upstream pytest asserts file modes; these pin the Go
// artifacts against the mode atomic_write guarantees.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

// skipWithoutPOSIXModes keeps the assertions meaningful on Windows, where
// os.Stat reports synthesized modes and os.Chmod only toggles read-only.
func skipWithoutPOSIXModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not model POSIX file modes")
	}
}

// requireOwnerOnly fails on the exact 0600 mode and, as the security
// property behind it, on any group/world access bit.
func requireOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Fatalf("%s permission = %#o, want 0600", path, perm)
	}
	if perm&0o077 != 0 {
		t.Fatalf("%s grants group/world access: %#o", path, perm)
	}
}

// Parity-exempt: pins the 0600 mode framework/utils/io.py::atomic_write
// guarantees for run documents; no upstream pytest asserts file modes.
func TestRunFilesAreOwnerOnly(t *testing.T) {
	skipWithoutPOSIXModes(t)
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	run := createTestRun("run_perms", "goal_perms", schemas.RunCompleted, nil)
	if err := store.SaveRunSync(run); err != nil {
		t.Fatalf("SaveRunSync: %v", err)
	}
	requireOwnerOnly(t, filepath.Join(store.BasePath, "runs", "run_perms.json"))
}

// Parity-exempt: pins the 0600 mode framework/storage/conversation_store.py
// gets from atomic_write; no upstream pytest asserts file modes.
func TestConversationFilesAreOwnerOnly(t *testing.T) {
	skipWithoutPOSIXModes(t)
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	if err := store.WriteMeta(map[string]any{"system_prompt": "hi"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 1}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "first"}); err != nil {
		t.Fatalf("WritePart: %v", err)
	}
	if err := store.WritePartial(1, map[string]any{"seq": 1, "content": "inflight"}); err != nil {
		t.Fatalf("WritePartial: %v", err)
	}
	for _, path := range []string{
		filepath.Join(store.BasePath(), "meta.json"),
		filepath.Join(store.BasePath(), "cursor.json"),
		filepath.Join(store.BasePath(), "parts", "0000000000.json"),
		filepath.Join(store.BasePath(), "partials", "0000000001.json"),
	} {
		requireOwnerOnly(t, path)
	}
}

// Parity-exempt: pins the 0600 mode framework/storage/session_summary.py gets
// from atomic_write; no upstream pytest asserts file modes.
func TestSessionSummaryFileIsOwnerOnly(t *testing.T) {
	skipWithoutPOSIXModes(t)
	sessionDir := makeSummarySessionDir(t)
	writeSummaryPart(t, sessionDir, 0, map[string]any{"role": "user", "content": "hi"})
	if summary := RebuildSummary(sessionDir); summary == nil {
		t.Fatal("RebuildSummary returned nil")
	}
	requireOwnerOnly(t, summaryPath(sessionDir))
}

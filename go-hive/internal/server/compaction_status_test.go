package server

// Parity: tests/test_compaction_sweep.py.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedCompactionStatus(t *testing.T, queensRoot, queen, session, status string) string {
	t.Helper()
	dir := filepath.Join(queensRoot, queen, "sessions", session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(CompactionStatusPath(dir), body, 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	return dir
}

func statusOf(t *testing.T, dir string) string {
	t.Helper()
	status := GetStatus(dir)
	if status == nil {
		t.Fatalf("no compaction status under %s", dir)
	}
	value, _ := status["status"].(string)
	return value
}

// Parity: tests/test_compaction_sweep.py::test_sweep_missing_root_is_noop
func TestSweepMissingRootIsNoop(t *testing.T) {
	if cleaned := SweepStaleInProgress(filepath.Join(t.TempDir(), "nope")); cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0", cleaned)
	}
}

// Parity: tests/test_compaction_sweep.py::test_sweep_clears_in_progress_markers
func TestSweepClearsInProgressMarkers(t *testing.T) {
	root := t.TempDir()
	dir := seedCompactionStatus(t, root, "alpha", "session_1", "in_progress")
	if cleaned := SweepStaleInProgress(root); cleaned != 1 {
		t.Fatalf("cleaned = %d, want 1", cleaned)
	}
	status := GetStatus(dir)
	if status == nil {
		t.Fatal("status disappeared after sweep")
	}
	if status["status"] != "failed" {
		t.Errorf("status = %v, want failed", status["status"])
	}
	if errText, _ := status["error"].(string); !strings.Contains(errText, "server restarted") {
		t.Errorf("error = %q, want it to mention a server restart", errText)
	}
}

// Parity: tests/test_compaction_sweep.py::test_sweep_leaves_done_and_failed_alone
func TestSweepLeavesDoneAndFailedAlone(t *testing.T) {
	root := t.TempDir()
	doneDir := seedCompactionStatus(t, root, "alpha", "s_done", "done")
	failedDir := seedCompactionStatus(t, root, "alpha", "s_failed", "failed")
	if cleaned := SweepStaleInProgress(root); cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0", cleaned)
	}
	if got := statusOf(t, doneDir); got != "done" {
		t.Errorf("done marker = %q, want done", got)
	}
	if got := statusOf(t, failedDir); got != "failed" {
		t.Errorf("failed marker = %q, want failed", got)
	}
}

// Parity: tests/test_compaction_sweep.py::test_sweep_handles_multiple_queens
func TestSweepHandlesMultipleQueens(t *testing.T) {
	root := t.TempDir()
	seedCompactionStatus(t, root, "alpha", "s_a", "in_progress")
	seedCompactionStatus(t, root, "beta", "s_b", "in_progress")
	seedCompactionStatus(t, root, "gamma", "s_c", "done")
	if cleaned := SweepStaleInProgress(root); cleaned != 2 {
		t.Fatalf("cleaned = %d, want 2", cleaned)
	}
}

// Parity: tests/test_compaction_sweep.py::test_sweep_skips_sessions_without_marker
func TestSweepSkipsSessionsWithoutMarker(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "alpha", "sessions", "s_clean")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if cleaned := SweepStaleInProgress(root); cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0", cleaned)
	}
	if _, err := os.Stat(CompactionStatusPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("sweep created a marker (stat err = %v)", err)
	}
}

// Parity-exempt: exercises mark_in_progress / mark_done / mark_failed /
func TestCompactionStatusRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := GetStatus(dir); got != nil {
		t.Fatalf("GetStatus on an empty dir = %v, want nil", got)
	}

	MarkInProgress(dir)
	status := GetStatus(dir)
	if status["status"] != "in_progress" {
		t.Errorf("status = %v, want in_progress", status["status"])
	}
	if started, _ := status["started_at"].(string); started == "" {
		t.Error("in_progress marker has no started_at")
	}

	MarkDone(dir, 7, 42)
	status = GetStatus(dir)
	if status["status"] != "done" {
		t.Errorf("status = %v, want done", status["status"])
	}
	if status["messages_compacted"] != float64(7) {
		t.Errorf("messages_compacted = %v, want 7", status["messages_compacted"])
	}
	if status["summary_chars"] != float64(42) {
		t.Errorf("summary_chars = %v, want 42", status["summary_chars"])
	}
	if completed, _ := status["completed_at"].(string); completed == "" {
		t.Error("done marker has no completed_at")
	}

	MarkFailed(dir, strings.Repeat("错", 600))
	status = GetStatus(dir)
	if status["status"] != "failed" {
		t.Errorf("status = %v, want failed", status["status"])
	}
	errText, _ := status["error"].(string)
	if runes := []rune(errText); len(runes) != 500 {
		t.Errorf("error runes = %d, want 500", len(runes))
	}

	if err := os.WriteFile(CompactionStatusPath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt marker: %v", err)
	}
	if got := GetStatus(dir); got != nil {
		t.Fatalf("GetStatus on a corrupt marker = %v, want nil", got)
	}
}

// Parity-exempt: exercises await_completion's poll/timeout/cancel behaviour;
func TestAwaitCompletion(t *testing.T) {
	dir := t.TempDir()

	if got := AwaitCompletion(context.Background(), dir, time.Second, time.Millisecond); got != nil {
		t.Fatalf("await with no marker = %v, want nil", got)
	}

	MarkDone(dir, 1, 2)
	start := time.Now()
	got := AwaitCompletion(context.Background(), dir, 5*time.Second, 10*time.Millisecond)
	if got == nil || got["status"] != "done" {
		t.Fatalf("await on done = %v, want done", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("await on a settled marker took %s", elapsed)
	}

	MarkInProgress(dir)
	got = AwaitCompletion(context.Background(), dir, 30*time.Millisecond, 5*time.Millisecond)
	if got == nil || got["status"] != "in_progress" {
		t.Fatalf("await timeout = %v, want the in_progress observation", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	got = AwaitCompletion(ctx, dir, time.Hour, 10*time.Millisecond)
	if got == nil || got["status"] != "in_progress" {
		t.Fatalf("await with cancelled ctx = %v, want the in_progress observation", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled await took %s", elapsed)
	}
}

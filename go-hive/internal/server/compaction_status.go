package server

// Parity: framework/server/compaction_status.py

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const (
	compactionStatusFilename = "compaction_status.json"
	compactionErrorCap       = 500
)

func CompactionStatusPath(queenDir string) string {
	return filepath.Join(queenDir, compactionStatusFilename)
}

// Parity: compaction_status.mark_in_progress.
func MarkInProgress(queenDir string) {
	writeCompactionStatus(queenDir, map[string]any{
		"status":     "in_progress",
		"started_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// Parity: compaction_status.mark_done.
func MarkDone(queenDir string, messagesCompacted, summaryChars int) {
	writeCompactionStatus(queenDir, map[string]any{
		"status":             "done",
		"completed_at":       time.Now().UTC().Format(time.RFC3339Nano),
		"messages_compacted": messagesCompacted,
		"summary_chars":      summaryChars,
	})
}

// Parity: compaction_status.mark_failed.
func MarkFailed(queenDir, errMsg string) {
	writeCompactionStatus(queenDir, map[string]any{
		"status":       "failed",
		"completed_at": time.Now().UTC().Format(time.RFC3339Nano),
		"error":        truncateRunes(errMsg, compactionErrorCap),
	})
}

func writeCompactionStatus(queenDir string, status map[string]any) {
	path := CompactionStatusPath(queenDir)
	if err := atomicfs.WriteJSON(path, status, 0o644); err != nil {
		log.Printf("compaction_status: failed to write %q at %s: %v",
			status["status"], path, err)
	}
}

// Parity: compaction_status.get_status.
func GetStatus(queenDir string) map[string]any {
	data, err := os.ReadFile(CompactionStatusPath(queenDir))
	if err != nil {
		return nil
	}
	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		return nil
	}
	return status
}

// Parity: compaction_status.await_completion.
func AwaitCompletion(ctx context.Context, queenDir string, timeout, poll time.Duration) map[string]any {
	if timeout < 0 {
		timeout = 0
	}
	deadline := time.Now().Add(timeout)
	for {
		last := GetStatus(queenDir)
		if last == nil {
			return nil
		}
		if status, _ := last["status"].(string); status != "in_progress" {
			return last
		}
		if !time.Now().Before(deadline) {
			log.Printf(
				"compaction_status: timed out after %.0fs waiting for %s (proceeding with raw transcript)",
				timeout.Seconds(), queenDir)
			return last
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last
		case <-timer.C:
		}
	}
}

// Parity: compaction_status.sweep_stale_in_progress.
func SweepStaleInProgress(queensRoot string) int {
	queens, err := os.ReadDir(queensRoot)
	if err != nil {
		return 0
	}
	cleaned := 0
	for _, queen := range queens {
		if !queen.IsDir() {
			continue
		}
		sessionsDir := filepath.Join(queensRoot, queen.Name(), "sessions")
		sessions, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}
		for _, session := range sessions {
			if !session.IsDir() {
				continue
			}
			dir := filepath.Join(sessionsDir, session.Name())
			status := GetStatus(dir)
			if status == nil {
				continue
			}
			if s, _ := status["status"].(string); s != "in_progress" {
				continue
			}
			MarkFailed(dir, "server restarted while compaction was in progress")
			cleaned++
		}
	}
	if cleaned > 0 {
		log.Printf("compaction_status: cleared %d stale 'in_progress' marker(s) at startup", cleaned)
	}
	return cleaned
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

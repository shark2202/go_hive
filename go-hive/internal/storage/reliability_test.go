package storage

// Reliability probes for on-disk session artifacts: corrupted state and index
// files must degrade to skip/rebuild, and unwritable directories must surface
// as errors instead of silent no-ops.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/schemas"
)

func testCheckpoint(id string) *schemas.Checkpoint {
	return &schemas.Checkpoint{
		CheckpointID:   id,
		CheckpointType: "node",
		SessionID:      "session-test",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		IsClean:        true,
	}
}

// Parity-exempt: fault-injection probe for a truncated checkpoint index; the
// index is derived from cp_*.json and must be rebuilt, not read as empty.
func TestCorruptCheckpointIndexRebuilds(t *testing.T) {
	store := NewCheckpointStore(t.TempDir())
	if err := store.SaveCheckpoint(testCheckpoint("cp-a")); err != nil {
		t.Fatalf("save a: %v", err)
	}
	if err := store.SaveCheckpoint(testCheckpoint("cp-b")); err != nil {
		t.Fatalf("save b: %v", err)
	}
	if err := os.WriteFile(store.IndexPath, []byte(`{"checkpoints":`), 0o644); err != nil {
		t.Fatalf("corrupt index: %v", err)
	}

	index, err := store.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex after corruption: %v", err)
	}
	if index == nil || len(index.Checkpoints) != 2 {
		t.Fatalf("rebuilt index = %+v, want both checkpoints", index)
	}

	// A save after the corruption must not persist an index that forgets the
	// checkpoints recovered from disk.
	if err := store.SaveCheckpoint(testCheckpoint("cp-c")); err != nil {
		t.Fatalf("save c: %v", err)
	}
	summaries, err := store.ListCheckpoints("", nil)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("checkpoints = %d after save, want 3: %+v", len(summaries), summaries)
	}
}

// Parity-exempt: a corrupt state.json must not remove the session from
// listings or panic a caller; a direct read reports the decode error.
func TestCorruptSessionStateSkippedAndReported(t *testing.T) {
	base := t.TempDir()
	store := NewSessionStore(base)
	now := time.Now().UTC()
	good := schemas.NewSessionState("session_good", "g", now)
	bad := schemas.NewSessionState("session_bad", "g", now)
	for _, state := range []*schemas.SessionState{good, bad} {
		if err := store.WriteState(state.SessionID, state); err != nil {
			t.Fatalf("write state %s: %v", state.SessionID, err)
		}
	}
	badPath := filepath.Join(store.SessionsDir, bad.SessionID, "state.json")
	if err := os.WriteFile(badPath, []byte(`{"session_id":`), 0o644); err != nil {
		t.Fatalf("corrupt state: %v", err)
	}

	sessions, err := store.ListSessions("", "", -1)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != good.SessionID {
		t.Fatalf("sessions = %+v, want only the healthy one", sessions)
	}
	if _, err := store.ReadState(bad.SessionID); err == nil {
		t.Fatal("ReadState hid a corrupt state.json (nil error)")
	}
}

// Parity-exempt: writes into a read-only parts directory must return an error
// (the write path cannot start a distinguishable "disk full" on every OS, but
// both failures surface through the same temp-create/rename path).
func TestWritePartReadOnlyDirectoryReportsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store := NewFileConversationStore(t.TempDir())
	if err := store.WritePart(0, map[string]any{"seq": 0, "role": "user", "content": "ok"}); err != nil {
		t.Fatalf("seed part: %v", err)
	}
	partsDir := store.PartsDir()
	if err := os.Chmod(partsDir, 0o555); err != nil {
		t.Fatalf("chmod parts: %v", err)
	}
	defer os.Chmod(partsDir, 0o755)

	if err := store.WritePart(1, map[string]any{"seq": 1, "role": "user", "content": "denied"}); err == nil {
		t.Fatal("WritePart returned nil for a read-only directory; the failure was silent")
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want the single seeded part", len(parts))
	}
}

// Parity-exempt: the index file itself being replaced by a directory is a
// write-path failure that must not be reported as success.
func TestCheckpointIndexWriteFailureIsExplicit(t *testing.T) {
	store := NewCheckpointStore(t.TempDir())
	if err := os.MkdirAll(store.CheckpointsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(store.IndexPath, 0o755); err != nil {
		t.Fatalf("mkdir index path: %v", err)
	}
	if err := store.SaveCheckpoint(testCheckpoint("cp-a")); err == nil {
		t.Fatal("SaveCheckpoint reported success while index.json was a directory")
	}
	// The checkpoint file itself is valid JSON even when the index write fails.
	raw, err := os.ReadFile(filepath.Join(store.CheckpointsDir, "cp-a.json"))
	if err != nil {
		t.Fatalf("checkpoint file missing: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("checkpoint file corrupt: %v", err)
	}
}

// Parity-exempt: rebuilding a session summary must skip corrupt/foreign part
// files rather than failing the whole transcript.
func TestRebuildSummarySkipsCorruptParts(t *testing.T) {
	sessionDir := t.TempDir()
	partsDir := filepath.Join(sessionDir, "conversations", "parts")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	valid := []string{
		`{"seq":0,"role":"user","content":"hello","created_at":1700000000}`,
		`{"seq":1,"role":"assistant","content":"hi there","created_at":1700000001}`,
	}
	for i, payload := range valid {
		if err := os.WriteFile(filepath.Join(partsDir, fmt.Sprintf("%010d.json", i)), []byte(payload), 0o644); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(partsDir, "0000000002.json"), []byte(`{"seq":2,"role":`), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write foreign: %v", err)
	}

	summary := RebuildSummary(sessionDir)
	if summary == nil {
		t.Fatal("RebuildSummary returned nil")
	}
	if summary.MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2", summary.MessageCount)
	}
	if summary.LastMessage == nil || *summary.LastMessage != "hi there" {
		t.Fatalf("last_message = %v, want hi there", summary.LastMessage)
	}
}

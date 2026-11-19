package cli

// Session layout: a chat session's transcript tree and its memory scope must
// resolve from the same queen identifier, so queens never read each other's
// history and a pre-w5 session under queens/default stays readable.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

const (
	layoutQueen   = "queen_growth"
	layoutSession = "session_20260101_000000_aa11bb22"
)

// writeSessionLayoutHome points a temp HIVE_HOME at a keyless local model so
// createChatSession builds a real ChatRuntime without network credentials.
func writeSessionLayoutHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(filepath.Join(t.TempDir(), "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), []byte(`{"llm":{"model":"ollama/llama3"}}`), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	return home
}

func createLayoutSession(t *testing.T, home hivehome.Home, sessionID, queenName string) *hiveserver.Session {
	t.Helper()
	var output bytes.Buffer
	runner := &Runner{Stdout: &output, Stderr: &output}
	manager := hiveserver.NewSessionManager()
	live := make([]*hiveserver.Session, 0, 1)
	liveMu := &sync.Mutex{}
	params := hiveserver.CreateSessionParams{SessionID: &sessionID}
	if queenName != "" {
		params.QueenName = &queenName
	}
	record, err := runner.createChatSession(params, manager, liveMu, &live, home, "")
	if err != nil {
		t.Fatalf("createChatSession: %v (output: %s)", err, output.String())
	}
	if record.Chat == nil {
		t.Fatalf("chat runtime is nil; output: %s", output.String())
	}
	t.Cleanup(func() { _ = record.Chat.Close() })
	return record
}

// seedSessionTranscript writes one conversation part into a session tree, the
// same place FileConversationStore reads transcripts from.
func seedSessionTranscript(t *testing.T, sessionDir, content string) {
	t.Helper()
	partsDir := filepath.Join(sessionDir, "conversations", "parts")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatalf("mkdir parts: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"seq": 0, "role": "user", "content": content})
	if err != nil {
		t.Fatalf("marshal part: %v", err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "0000000000.json"), payload, 0o644); err != nil {
		t.Fatalf("write part: %v", err)
	}
}

func transcriptContains(t *testing.T, record *hiveserver.Session, want string) bool {
	t.Helper()
	parts, err := record.Chat.Transcript()
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	for _, part := range parts {
		if text, ok := part["content"].(string); ok && text == want {
			return true
		}
	}
	return false
}

// Parity: framework/server/tests/test_api.py::TestQueenSessionSelection::test_select_queen_session_restores_specific_history_session
func TestCreateChatSessionUsesQueenSessionsLayout(t *testing.T) {
	home := writeSessionLayoutHome(t)
	record := createLayoutSession(t, home, layoutSession, layoutQueen)

	if record.QueenName != layoutQueen {
		t.Fatalf("queen name = %q, want %q", record.QueenName, layoutQueen)
	}
	queenDir := home.QueenSessionDir(layoutQueen, layoutSession)
	if _, err := os.Stat(filepath.Join(queenDir, "events.jsonl")); err != nil {
		t.Fatalf("queen transcript tree missing: %v", err)
	}
	// The memory scope the runtime recalls from is the same queen id.
	queenMemory := filepath.Join(home.Root, "memories", "agents", "queens", layoutQueen)
	if info, err := os.Stat(queenMemory); err != nil || !info.IsDir() {
		t.Fatalf("queen memory scope missing: %v", err)
	}
	if _, err := os.Stat(home.QueenSessionDir("default", layoutSession)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default session tree created for a queen-scoped session: %v", err)
	}
}

// Parity: framework/server/tests/test_session_dir_resolution.py::test_first_match_lookup_still_prefers_the_dm_tree
func TestCreateChatSessionResumesLegacyDefaultSession(t *testing.T) {
	home := writeSessionLayoutHome(t)
	legacyDir := home.QueenSessionDir("default", layoutSession)
	seedSessionTranscript(t, legacyDir, "legacy transcript marker")

	record := createLayoutSession(t, home, layoutSession, layoutQueen)

	if !transcriptContains(t, record, "legacy transcript marker") {
		t.Fatal("legacy default transcript was not restored")
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "events.jsonl")); err != nil {
		t.Fatalf("legacy session tree not reused: %v", err)
	}
	if _, err := os.Stat(home.QueenSessionDir(layoutQueen, layoutSession)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queen tree created instead of reusing the legacy tree: %v", err)
	}
}

// Parity: framework/server/tests/test_api.py::TestQueenSessionSelection::test_select_queen_session_rejects_foreign_session
func TestCreateChatSessionKeepsQueensSeparate(t *testing.T) {
	home := writeSessionLayoutHome(t)
	growthDir := home.QueenSessionDir(layoutQueen, layoutSession)
	seedSessionTranscript(t, growthDir, "growth transcript marker")

	record := createLayoutSession(t, home, layoutSession, "queen_technology")

	if transcriptContains(t, record, "growth transcript marker") {
		t.Fatal("queen_technology session read queen_growth's transcript")
	}
	techDir := home.QueenSessionDir("queen_technology", layoutSession)
	if _, err := os.Stat(filepath.Join(techDir, "events.jsonl")); err != nil {
		t.Fatalf("queen_technology transcript tree missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(growthDir, "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queen_technology session wrote into queen_growth's tree: %v", err)
	}
}

// Parity: core/tests/test_session_unified_colony_runtime.py::test_start_unified_colony_runtime_creates_real_colony
func TestCreateChatSessionWithoutQueenUsesDefaultTree(t *testing.T) {
	home := writeSessionLayoutHome(t)
	record := createLayoutSession(t, home, layoutSession, "")

	if record.QueenName != "" {
		t.Fatalf("queen name = %q, want empty for an unbound session", record.QueenName)
	}
	if _, err := os.Stat(filepath.Join(home.QueenSessionDir("default", layoutSession), "events.jsonl")); err != nil {
		t.Fatalf("default transcript tree missing: %v", err)
	}
	if _, err := os.Stat(home.QueenSessionDir(layoutQueen, layoutSession)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queen tree created for an unbound session: %v", err)
	}
}

// Parity-exempt: queen_name arrives in the create-session body, so joining it
// into a path needs the same segment guard session_id already has; no upstream
// test covers the Go create hook's own defense.
func TestCreateChatSessionRejectsTraversalQueenName(t *testing.T) {
	home := writeSessionLayoutHome(t)
	var output bytes.Buffer
	runner := &Runner{Stdout: &output, Stderr: &output}
	manager := hiveserver.NewSessionManager()
	live := make([]*hiveserver.Session, 0)
	liveMu := &sync.Mutex{}
	sessionID := layoutSession
	badQueen := "../escape"
	_, err := runner.createChatSession(
		hiveserver.CreateSessionParams{SessionID: &sessionID, QueenName: &badQueen},
		manager, liveMu, &live, home, "",
	)
	var valueErr *hiveserver.SessionCreateValueError
	if !errors.As(err, &valueErr) {
		t.Fatalf("error = %v, want SessionCreateValueError", err)
	}
	if len(live) != 0 {
		t.Fatalf("rejected session was registered: %d", len(live))
	}
	if _, statErr := os.Stat(filepath.Join(home.Root, "escape")); statErr == nil {
		t.Fatal("invalid queen_name created a directory outside the queen tree")
	}
}

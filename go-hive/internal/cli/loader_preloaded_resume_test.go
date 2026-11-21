package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

func readPreloadedColonyMetadata(t *testing.T, home hivehome.Home, colonyID string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home.ColoniesDir(), colonyID, "metadata.json"))
	if err != nil {
		t.Fatalf("read colony metadata: %v", err)
	}
	meta := map[string]any{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("decode colony metadata: %v", err)
	}
	return meta
}

// Parity: framework/server/session_manager.py::_create_session_for_existing_colony
func TestPreloadedColonySessionReusesRecordedSessionID(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	writeColonySpecFixture(t, home, "honeycomb", `{"name":"worker","description":"bee counter"}`)

	firstID, firstDir, resumed := resolvePreloadedColonySession(home, "honeycomb", "default")
	if firstID == "" || resumed {
		t.Fatalf("first boot = (%q, %q), want a fresh id", firstID, firstDir)
	}
	wantDir := chatSessionDir(home, "default", firstID)
	if firstDir != wantDir {
		t.Fatalf("session dir = %q, want %q", firstDir, wantDir)
	}
	if info, err := os.Stat(firstDir); err != nil || !info.IsDir() {
		t.Fatalf("session dir was not materialised: %v", err)
	}
	if meta := readPreloadedColonyMetadata(t, home, "honeycomb"); meta["queen_session_id"] != firstID {
		t.Fatalf("metadata queen_session_id = %#v, want %q", meta["queen_session_id"], firstID)
	}

	// A restart resolves the recorded id and reports the session as resumed.
	secondID, secondDir, resumed := resolvePreloadedColonySession(home, "honeycomb", "default")
	if !resumed || secondID != firstID || secondDir != firstDir {
		t.Fatalf("second boot = (%q, %q, resumed=%v), want (%q, %q, resumed=true)",
			secondID, secondDir, resumed, firstID, firstDir)
	}
}

// Parity: framework/server/session_manager.py::_create_session_for_existing_colony
func TestPreloadedColonySessionMintsNewIDWhenRecordedSessionIsGone(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	writeColonySpecFixture(t, home, "honeycomb", "")
	// Record an id whose directory no longer exists (colony tree pruned).
	metaPath := filepath.Join(home.ColoniesDir(), "honeycomb", "metadata.json")
	meta := map[string]any{"queen_name": "queen_alpha", "queen_session_id": "session_20260101_000000_gone"}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	sessionID, sessionDir, resumed := resolvePreloadedColonySession(home, "honeycomb", "default")
	if resumed || sessionID == "" || sessionID == "session_20260101_000000_gone" {
		t.Fatalf("boot = (%q, resumed=%v), want a fresh id", sessionID, resumed)
	}
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("fresh session dir was not created: %v", err)
	}
	updated := readPreloadedColonyMetadata(t, home, "honeycomb")
	if updated["queen_session_id"] != sessionID {
		t.Fatalf("metadata queen_session_id = %#v, want %q", updated["queen_session_id"], sessionID)
	}
	if updated["queen_name"] != "queen_alpha" {
		t.Fatalf("metadata queen_name = %#v, must be preserved", updated["queen_name"])
	}
}

// staticChatLLM answers every completion with plain text, so a preloaded
// session completes a turn without dispatching workers.
type staticChatLLM struct{}

func (staticChatLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{Content: "noted", StopReason: "stop"}, nil
}

// waitForTranscriptContaining waits until the runtime's persisted transcript
// contains the given text (the turn is delivered asynchronously).
func waitForTranscriptContaining(t *testing.T, runtime *hiveserver.ChatRuntime, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		parts, err := runtime.Transcript()
		if err == nil {
			for _, part := range parts {
				if text, _ := part["content"].(string); strings.Contains(text, want) {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transcript never contained %q", want)
}

// Parity-exempt: `serve --colony` preloading is Go-only (upstream builds the
// session through SessionManager); pins that a restarted preload adopts the
// persisted session id via queen_resume_from instead of minting a new one.
func TestPreloadedColonySessionRestartReusesID(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := writeColonySpecFixture(t, home, "honeycomb",
		`{"name":"worker","description":"bee counter","system_prompt":"count bees carefully"}`)

	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return staticChatLLM{}, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	var stdout bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stdout}
	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex

	live := make([]*hiveserver.Session, 0)
	first := runner.loadPreloadedColonySession(colonyPath, manager, &liveMu, &live, home, "")
	if first == nil {
		t.Fatalf("first preload is nil; output: %s", stdout.String())
	}
	firstID := first.ID
	queenName := colonyQueenName(home, "honeycomb")
	if queenName == "" {
		t.Fatal("colony fixture has no queen_name; the preload must scope to the colony's queen")
	}
	if first.QueenName != queenName {
		t.Fatalf("preloaded session queen = %q, want the colony's queen %q", first.QueenName, queenName)
	}
	if _, err := os.Stat(chatSessionDir(home, queenName, firstID)); err != nil {
		t.Fatalf("first preload did not materialise its session dir under queen %q: %v", queenName, err)
	}
	// One real turn so the transcript the restart must continue exists.
	if err := first.Chat.Deliver("remember the bees"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForTranscriptContaining(t, first.Chat, "remember the bees")
	closePreloadedSession(t, first)

	live = make([]*hiveserver.Session, 0)
	second := runner.loadPreloadedColonySession(colonyPath, manager, &liveMu, &live, home, "")
	if second == nil {
		t.Fatalf("second preload is nil; output: %s", stdout.String())
	}
	t.Cleanup(func() { closePreloadedSession(t, second) })
	if second.ID != firstID {
		t.Fatalf("restarted preload id = %q, want the persisted %q", second.ID, firstID)
	}
	if second.QueenResumeFrom != firstID {
		t.Fatalf("QueenResumeFrom = %q, want %q (the queen_resume_from path)", second.QueenResumeFrom, firstID)
	}
	parts, err := second.Chat.Transcript()
	if err != nil {
		t.Fatalf("restarted transcript: %v", err)
	}
	found := false
	for _, part := range parts {
		if text, _ := part["content"].(string); strings.Contains(text, "remember the bees") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("restarted session did not continue the persisted transcript (%d parts)", len(parts))
	}
}

func closePreloadedSession(t *testing.T, session *hiveserver.Session) {
	t.Helper()
	if session == nil {
		return
	}
	if session.Chat != nil {
		_ = session.Chat.Close()
	}
	if host, ok := session.Colony.(*colonyhost.Host); ok && host != nil {
		host.Stop()
	}
}

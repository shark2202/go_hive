package cli

// Queen-resume create path: a POST /api/sessions that carries
// queen_resume_from must re-register the persisted session under its original
// id. The dashboard's session-gone auto-resume depends on it — after a serve
// restart /events only stops 404ing once the create has put that id back in
// the live manager.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	hivesession "github.com/aden-hive/hive-go/internal/session"
)

const resumeMissingID = "session_20260101_000000_deadbeef"

// resumeLayoutSession drives createChatSession the way the dashboard's
// onSessionGone does: queen_resume_from plus the page's queen, no session_id.
func resumeLayoutSession(t *testing.T, home hivehome.Home, manager *hiveserver.SessionManager, resumeFrom, queenName string) (*hiveserver.Session, error) {
	t.Helper()
	var output bytes.Buffer
	runner := &Runner{Stdout: &output, Stderr: &output}
	live := make([]*hiveserver.Session, 0, 1)
	liveMu := &sync.Mutex{}
	params := hiveserver.CreateSessionParams{QueenResumeFrom: &resumeFrom}
	if queenName != "" {
		params.QueenName = &queenName
	}
	record, err := runner.createChatSession(params, manager, liveMu, &live, home, "")
	if err != nil {
		return nil, fmt.Errorf("%w (output: %s)", err, output.String())
	}
	t.Cleanup(func() {
		if record.Chat != nil {
			_ = record.Chat.Close()
		}
	})
	return record, nil
}

// seedConversationMeta writes the store meta RestoreConversation needs; without
// it a resumed runtime would start a fresh conversation and overwrite seq 0.
func seedConversationMeta(t *testing.T, sessionDir string) {
	t.Helper()
	metaDir := filepath.Join(sessionDir, "conversations")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir conversations: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"system_prompt": "restored persona"})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), payload, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

// writeResumeTurnHome points HIVE_HOME at a local OpenAI-compatible stub so a
// resumed session can run a real turn and append parts offline. The returned
// function snapshots the request bodies the runtime sent, which lets a test
// assert the persisted transcript reached the model.
func writeResumeTurnHome(t *testing.T) (hivehome.Home, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	requests := make([]map[string]any, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		requests = append(requests, payload)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"mock","choices":[{"message":{"role":"assistant","content":"resumed reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	t.Cleanup(server.Close)

	home, err := hivehome.ResolveAt(filepath.Join(testTempDir(t), "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	config := fmt.Sprintf(`{"llm":{"model":"ollama/llama3","api_base":%q}}`, server.URL)
	if err := os.WriteFile(home.ConfigurationFile(), []byte(config), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	return home, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]any, len(requests))
		copy(out, requests)
		return out
	}
}

func waitForResumeAssistantPart(t *testing.T, runtime *hiveserver.ChatRuntime) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var parts []map[string]any
	for time.Now().Before(deadline) {
		read, err := runtime.Transcript()
		if err != nil {
			t.Fatalf("transcript: %v", err)
		}
		parts = read
		for _, part := range parts {
			if part["role"] == "assistant" && part["content"] == "resumed reply" {
				return parts
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the resumed turn; parts = %#v", parts)
	return nil
}

func completionRequestMentions(payload map[string]any, want string) bool {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return strings.Contains(string(encoded), want)
}

// Parity: framework/server/tests/test_channels.py::test_a_stopped_session_is_resumed_and_still_heard
func TestCreateChatSessionResumesPersistedSessionID(t *testing.T) {
	home := writeSessionLayoutHome(t)
	manager := hiveserver.NewSessionManager()
	sessionDir := home.QueenSessionDir(layoutQueen, layoutSession)
	seedSessionTranscript(t, sessionDir, "before restart")
	seedConversationMeta(t, sessionDir)

	record, err := resumeLayoutSession(t, home, manager, layoutSession, layoutQueen)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if record.ID != layoutSession {
		t.Fatalf("session id = %q, want the resumed id %q", record.ID, layoutSession)
	}
	if got := manager.GetSession(layoutSession); got != record {
		t.Fatalf("manager[%s] = %p, want the resumed record %p", layoutSession, got, record)
	}
	if !transcriptContains(t, record, "before restart") {
		t.Fatal("resumed runtime did not restore the persisted transcript")
	}
	if record.QueenResumeFrom != layoutSession {
		t.Fatalf("QueenResumeFrom = %q, want %q", record.QueenResumeFrom, layoutSession)
	}

	// A retry of the same resume must not build a second runtime on the same
	// transcript: the live record is the resume result.
	retried, err := resumeLayoutSession(t, home, manager, layoutSession, layoutQueen)
	if err != nil {
		t.Fatalf("retry resume: %v", err)
	}
	if retried != record {
		t.Fatal("a repeated resume replaced the live session instead of reusing it")
	}
	if count := manager.SessionCount(); count != 1 {
		t.Fatalf("live sessions = %d, want 1", count)
	}

	// /events must answer 200 for the resumed id — the request the dashboard
	// retries until the session is routable again.
	mux := http.NewServeMux()
	hiveserver.RegisterChatRoutes(mux, hiveserver.ChatRouteDeps{Manager: manager})
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/sessions/"+layoutSession+"/events", nil)
	if err != nil {
		t.Fatalf("build events request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("subscribe /events: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("/events status = %d, want 200", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("/events content type = %q", contentType)
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_after_a_restart_what_she_says_still_goes_back
func TestCreateChatSessionResumeContinuesTranscript(t *testing.T) {
	home, requests := writeResumeTurnHome(t)
	manager := hiveserver.NewSessionManager()
	sessionDir := home.QueenSessionDir(layoutQueen, layoutSession)
	seedSessionTranscript(t, sessionDir, "before restart")
	seedConversationMeta(t, sessionDir)

	record, err := resumeLayoutSession(t, home, manager, layoutSession, layoutQueen)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if record.ID != layoutSession {
		t.Fatalf("session id = %q, want %q", record.ID, layoutSession)
	}
	if err := record.Chat.Deliver("after restart"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	parts := waitForResumeAssistantPart(t, record.Chat)

	seenUser, seenAssistant := false, false
	for _, part := range parts {
		switch part["content"] {
		case "before restart":
			seenUser = true
		case "resumed reply":
			seenAssistant = true
		}
	}
	if !seenUser || !seenAssistant {
		t.Fatalf("transcript did not continue after resume: %#v", parts)
	}
	// The restored conversation, not a blank one, is what the model saw.
	sent := requests()
	if len(sent) == 0 {
		t.Fatal("the resumed turn sent no completion request")
	}
	if !completionRequestMentions(sent[0], "before restart") {
		t.Fatalf("resumed turn did not carry the persisted transcript: %#v", sent[0])
	}
	// The new turn appends after the restored seq instead of overwriting it.
	if _, err := os.Stat(filepath.Join(sessionDir, "conversations", "parts", "0000000001.json")); err != nil {
		t.Fatalf("resumed turn did not append a new part: %v", err)
	}
}

// Parity-exempt: upstream create_session reuses queen_resume_from blindly
// (`resolved_session_id = queen_resume_from or session_id`); the Go hook only
// adopts an id that has a session tree, so a resume of a deleted session
// creates a fresh id instead of a ghost with no transcript.
func TestCreateChatSessionResumeMissingTargetCreatesNewSession(t *testing.T) {
	home := writeSessionLayoutHome(t)
	manager := hiveserver.NewSessionManager()

	record, err := resumeLayoutSession(t, home, manager, resumeMissingID, layoutQueen)
	if err != nil {
		t.Fatalf("resume missing target: %v", err)
	}
	if record.ID == resumeMissingID {
		t.Fatalf("session id = %q, want a fresh id", record.ID)
	}
	if !hivesession.ValidSegment(record.ID) {
		t.Fatalf("fresh session id %q is not a valid segment", record.ID)
	}
	if manager.GetSession(resumeMissingID) != nil {
		t.Fatal("the missing resume id was registered as a live session")
	}
	if record.QueenResumeFrom != "" {
		t.Fatalf("QueenResumeFrom = %q, want empty after falling back", record.QueenResumeFrom)
	}
	if _, err := os.Stat(home.QueenSessionDir(layoutQueen, record.ID)); err != nil {
		t.Fatalf("fresh session tree missing: %v", err)
	}
}

// Parity: framework/server/queen_orchestrator.py::create_queen
// (a resumed channel session keeps session.channel from meta.json, so the
// chat-app gate reads the same answer it did before the restart)
func TestCreateChatSessionResumeRestoresTheSessionsChannel(t *testing.T) {
	home := writeSessionLayoutHome(t)
	manager := hiveserver.NewSessionManager()
	sessionDir := home.QueenSessionDir(layoutQueen, layoutSession)
	seedSessionTranscript(t, sessionDir, "a queens thread")
	seedConversationMeta(t, sessionDir)
	meta, err := json.Marshal(map[string]any{"channel": "queens", "peer": "queen_ivy"})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), meta, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	record, err := resumeLayoutSession(t, home, manager, layoutSession, layoutQueen)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := record.Channel(); got != "queens" {
		t.Fatalf("resumed channel = %q, want queens", got)
	}

	// A dashboard session (no channel in meta) stays channel-less.
	dashboardDir := home.QueenSessionDir(layoutQueen, "session_20260101_000000_dash0001")
	seedSessionTranscript(t, dashboardDir, "a dashboard chat")
	dashboard, err := resumeLayoutSession(t, home, manager, "session_20260101_000000_dash0001", layoutQueen)
	if err != nil {
		t.Fatalf("resume dashboard: %v", err)
	}
	if got := dashboard.Channel(); got != "" {
		t.Fatalf("dashboard channel = %q, want empty", got)
	}
}

// Parity: framework/server/tests/test_api.py::test_select_queen_session_rejects_foreign_session
func TestCreateChatSessionResumeDoesNotCrossQueens(t *testing.T) {
	home := writeSessionLayoutHome(t)
	manager := hiveserver.NewSessionManager()
	growthDir := home.QueenSessionDir(layoutQueen, layoutSession)
	seedSessionTranscript(t, growthDir, "growth transcript marker")

	record, err := resumeLayoutSession(t, home, manager, layoutSession, "queen_technology")
	if err != nil {
		t.Fatalf("resume across queens: %v", err)
	}
	if record.ID == layoutSession {
		t.Fatalf("queen_technology adopted queen_growth's session id %q", layoutSession)
	}
	if transcriptContains(t, record, "growth transcript marker") {
		t.Fatal("queen_technology read queen_growth's transcript through a resume")
	}
	if _, err := os.Stat(filepath.Join(growthDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("queen_technology wrote into queen_growth's tree: %v", err)
	}

	// The legacy default tree is shared, but a queen_id stamp makes it another
	// queen's session and must block the resume as well.
	legacyID := "session_20260101_000000_aa99bb88"
	legacyDir := home.QueenSessionDir("default", legacyID)
	seedSessionTranscript(t, legacyDir, "legacy growth marker")
	meta, err := json.Marshal(map[string]any{"queen_id": layoutQueen})
	if err != nil {
		t.Fatalf("marshal legacy meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "meta.json"), meta, 0o644); err != nil {
		t.Fatalf("write legacy meta: %v", err)
	}
	record, err = resumeLayoutSession(t, home, manager, legacyID, "queen_technology")
	if err != nil {
		t.Fatalf("resume legacy across queens: %v", err)
	}
	if record.ID == legacyID {
		t.Fatalf("queen_technology adopted queen_growth's legacy session id %q", legacyID)
	}
	if transcriptContains(t, record, "legacy growth marker") {
		t.Fatal("queen_technology read queen_growth's legacy transcript through a resume")
	}

	// A live session under another queen with the same id is a collision, not
	// a resume: refuse it instead of replacing that queen's runtime.
	collidingID := "session_20260101_000000_bb77cc66"
	seedSessionTranscript(t, home.QueenSessionDir("queen_technology", collidingID), "technology marker")
	foreign := hiveserver.NewSession(collidingID, eventbus.NewEventBus())
	foreign.QueenName = layoutQueen
	manager.AddSession(foreign)
	if _, err := resumeLayoutSession(t, home, manager, collidingID, "queen_technology"); err == nil {
		t.Fatal("resume replaced a live session owned by another queen")
	} else {
		var valueErr *hiveserver.SessionCreateValueError
		if !errors.As(err, &valueErr) || !strings.Contains(valueErr.Message, "already exists") {
			t.Fatalf("collision error = %v, want 'already exists' ValueError", err)
		}
	}
}

package cli

// Cold-start queen resolution: the create path binds a session to the queen
// upstream would pick, before the response is written.
//
//  * no queen_name, no resume, empty message → static default queen;
//  * no queen_name, no resume, message-bearing → the queen classifier's pick;
//  * no queen_name, resume → the queen the persisted session carries.
//
// The classifier runs on the session's own base provider; any failure falls
// back to the static default, as select_queen_with_reason does.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// selectorStubLLM answers every completion with the same classifier JSON, so a
// create that carries an initial_prompt resolves to the stub's queen.
type selectorStubLLM struct {
	content string
	calls   int
	mu      sync.Mutex
}

func (s *selectorStubLLM) AComplete(_ context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return llm.Completion{Content: s.content, Model: "selector-stub"}, nil
}

func injectSelectorLLM(t *testing.T, content string) *selectorStubLLM {
	t.Helper()
	stub := &selectorStubLLM{content: content}
	previous := chatProviderFactory
	t.Cleanup(func() { chatProviderFactory = previous })
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return stub, nil }
	return stub
}

// Parity: framework/agents/queen/queen_profiles.py::select_queen_with_reason
func TestCreateChatSessionClassifierBindsQueenForInitialPrompt(t *testing.T) {
	home := writeSessionLayoutHome(t)
	injectSelectorLLM(t, `{"reason": "architectural work", "queen_id": "queen_technology"}`)
	var output strings.Builder
	runner := &Runner{Stdout: &output, Stderr: &output}
	manager := hiveserver.NewSessionManager()
	live := make([]*hiveserver.Session, 0, 1)
	liveMu := &sync.Mutex{}
	sessionID := layoutSession
	prompt := "Design the storage migration"
	record, err := runner.createChatSession(
		hiveserver.CreateSessionParams{SessionID: &sessionID, InitialPrompt: &prompt},
		manager, liveMu, &live, home, "",
	)
	if err != nil {
		t.Fatalf("createChatSession: %v (output: %s)", err, output.String())
	}
	t.Cleanup(func() {
		if record.Chat != nil {
			_ = record.Chat.Close()
		}
	})
	if record.QueenName != "queen_technology" {
		t.Fatalf("queen name = %q, want the classifier's pick", record.QueenName)
	}
	if _, err := os.Stat(filepath.Join(home.QueenSessionDir("queen_technology", layoutSession), "meta.json")); err != nil {
		t.Fatalf("classifier queen tree missing: %v", err)
	}
}

// Parity: framework/server/session_manager.py::_ensure_session_queen_identity
func TestSelectColdStartQueenFallsBackToDefaultOnProviderError(t *testing.T) {
	home := writeSessionLayoutHome(t)
	previous := chatProviderFactory
	t.Cleanup(func() { chatProviderFactory = previous })
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) {
		return nil, errors.New("no provider")
	}
	runner := &Runner{Stdout: &strings.Builder{}, Stderr: &strings.Builder{}}
	prompt := "Design the storage migration"
	if got := runner.selectColdStartQueen(home, &prompt, ""); got != hiveserver.DefaultQueenID() {
		t.Fatalf("queen = %q, want the default queen on provider failure", got)
	}
	if got := runner.selectColdStartQueen(home, nil, ""); got != hiveserver.DefaultQueenID() {
		t.Fatalf("queen = %q, want the default queen for an empty prompt", got)
	}
}

// Parity: framework/server/session_manager.py::_resume_queen_name
//
// The dashboard's session-gone auto-resume posts queen_resume_from without a
// queen name. The resume adopts the queen the session was persisted under
// (meta.json["queen_id"], else the queen directory holding it) so the original
// id comes back routable under the tree it actually lives in.
func TestCreateChatSessionResumeWithoutQueenAdoptsPersistedQueen(t *testing.T) {
	home := writeSessionLayoutHome(t)
	manager := hiveserver.NewSessionManager()
	persistedDir := home.QueenSessionDir("queen_technology", layoutSession)
	seedSessionTranscript(t, persistedDir, "persisted marker")
	seedConversationMeta(t, persistedDir)
	meta, err := json.Marshal(map[string]any{"queen_id": "queen_technology", "phase": "independent"})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(persistedDir, "meta.json"), meta, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	record, err := resumeLayoutSession(t, home, manager, layoutSession, "")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if record.ID != layoutSession {
		t.Fatalf("session id = %q, want the resumed id", record.ID)
	}
	if record.QueenName != "queen_technology" {
		t.Fatalf("queen name = %q, want the persisted queen", record.QueenName)
	}
	if record.QueenDir != persistedDir {
		t.Fatalf("queen dir = %q, want the persisted tree %q", record.QueenDir, persistedDir)
	}
	if !transcriptContains(t, record, "persisted marker") {
		t.Fatal("resumed runtime did not restore the persisted transcript")
	}
	if _, err := os.Stat(home.QueenSessionDir("default", layoutSession)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default tree written for a queen-scoped resume: %v", err)
	}
}

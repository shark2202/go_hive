//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/server"
)

// scriptedCompleter is a deterministic llm.Completer: no network, one reply
// per call, echoing the last user turn so the test can prove a real agent-loop
// turn ran and not just that the HTTP handler returned 200.
type scriptedCompleter struct {
	mu    sync.Mutex
	calls []string
}

func (f *scriptedCompleter) ModelName() string { return "e2e-fake-model" }

func (f *scriptedCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	if err := ctx.Err(); err != nil {
		return llm.Completion{}, err
	}
	lastUser := ""
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			lastUser = msg.Content
		}
	}
	if lastUser == "" {
		return llm.Completion{}, fmt.Errorf("e2e fake completer: request had no user message")
	}

	f.mu.Lock()
	f.calls = append(f.calls, lastUser)
	call := len(f.calls)
	f.mu.Unlock()

	return llm.Completion{
		Content:    fmt.Sprintf("assistant reply %d for: %s", call, lastUser),
		Model:      "e2e-fake-model",
		StopReason: "end_turn",
	}, nil
}

func (f *scriptedCompleter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// chatHarness boots the production HTTP app (server.NewApp) with a temp hive
// home, a real CreateSession callback that builds a real server.ChatRuntime
// around the fake completer, and a temp on-disk conversation store.
type chatHarness struct {
	t         *testing.T
	server    *httptest.Server
	manager   *server.SessionManager
	root      string
	completer *scriptedCompleter

	mu       sync.Mutex
	nextID   int
	runtimes []*server.ChatRuntime
}

func newChatHarness(t *testing.T) *chatHarness {
	t.Helper()

	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive-home"))
	if err != nil {
		t.Fatalf("resolve temp hive home: %v", err)
	}

	h := &chatHarness{
		t:         t,
		manager:   server.NewSessionManager(),
		root:      root,
		completer: &scriptedCompleter{},
	}

	createSession := func(params server.CreateSessionParams) (*server.Session, error) {
		h.mu.Lock()
		h.nextID++
		id := fmt.Sprintf("session_e2e_%03d", h.nextID)
		h.mu.Unlock()

		if params.SessionID != nil && *params.SessionID != "" {
			id = *params.SessionID
		}

		storeDir := h.transcriptDir(id)
		runtime, err := server.NewChatRuntime(server.ChatRuntimeDeps{
			LLM:   h.completer,
			Store: agentloop.NewFileConversationStore(storeDir),
			Tools: nil,
		})
		if err != nil {
			return nil, fmt.Errorf("build chat runtime: %w", err)
		}

		session := server.NewSession(id, eventbus.NewEventBus())
		session.LLM = h.completer
		session.Chat = runtime

		h.mu.Lock()
		h.runtimes = append(h.runtimes, runtime)
		h.mu.Unlock()

		h.manager.AddSession(session)
		return session, nil
	}

	emptyToken := ""
	app := server.NewApp(server.AppDeps{
		Manager:       h.manager,
		Home:          home,
		CreateSession: createSession,
		FrontendDist:  filepath.Join(root, "no-frontend-dist"),
		DesktopToken:  &emptyToken,
	})
	h.server = httptest.NewServer(app)

	t.Cleanup(func() {
		h.server.Close()
		h.mu.Lock()
		runtimes := append([]*server.ChatRuntime(nil), h.runtimes...)
		h.mu.Unlock()
		for _, runtime := range runtimes {
			_ = runtime.Close()
		}
	})
	return h
}

// transcriptDir is where the harness wires each session's conversation store:
// <root>/queens/default/sessions/<id>/conversations.
func (h *chatHarness) transcriptDir(sessionID string) string {
	return filepath.Join(h.root, "queens", "default", "sessions", sessionID, "conversations")
}

type httpResult struct {
	status int
	body   map[string]any
	raw    string
}

func (h *chatHarness) doJSON(t *testing.T, method, path string, payload any) httpResult {
	t.Helper()

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode %s %s request: %v", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s request: %v", method, path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	result := httpResult{status: resp.StatusCode, raw: string(raw)}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &result.body)
	}
	return result
}

func readTranscript(t *testing.T, dir string) []map[string]any {
	t.Helper()
	parts, err := agentloop.NewFileConversationStore(dir).ReadParts()
	if err != nil {
		t.Fatalf("read transcript %s: %v", dir, err)
	}
	return parts
}

// waitForTranscript polls the on-disk store until want sees a stable result.
// The chat route acknowledges delivery before the worker turn finishes.
func waitForTranscript(t *testing.T, dir string, want func([]map[string]any) bool) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var parts []map[string]any
	for {
		parts = readTranscript(t, dir)
		if want(parts) {
			return parts
		}
		if time.Now().After(deadline) {
			t.Fatalf("transcript %s did not reach expected state within 15s; parts: %s", dir, describeParts(parts))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func hasMessage(parts []map[string]any, role, substring string) bool {
	for _, part := range parts {
		if partRole, _ := part["role"].(string); partRole != role {
			continue
		}
		if content, _ := part["content"].(string); strings.Contains(content, substring) {
			return true
		}
	}
	return false
}

func countRole(parts []map[string]any, role string) int {
	count := 0
	for _, part := range parts {
		if partRole, _ := part["role"].(string); partRole == role {
			count++
		}
	}
	return count
}

func describeParts(parts []map[string]any) string {
	described := make([]string, 0, len(parts))
	for _, part := range parts {
		role, _ := part["role"].(string)
		content, _ := part["content"].(string)
		if len(content) > 120 {
			content = content[:120] + "…"
		}
		described = append(described, fmt.Sprintf("%s=%q", role, content))
	}
	return "[" + strings.Join(described, ", ") + "]"
}

// TestChatDeliveryEndToEnd drives the production HTTP app end to end:
// create -> POST /chat -> persisted transcript -> second POST /chat.
//
// Parity: core/tests/test_chat_attachments.py::test_pdf_page_text_reaches_the_queen
func TestChatDeliveryEndToEnd(t *testing.T) {
	h := newChatHarness(t)

	// 1. Create a session through the real route; the route maps the callback's
	// session to 201 Created.
	created := h.doJSON(t, http.MethodPost, "/api/sessions", map[string]any{})
	if created.status != http.StatusOK && created.status != http.StatusCreated {
		t.Fatalf("POST /api/sessions = %d, want 200/201; body: %s", created.status, created.raw)
	}
	sessionID, _ := created.body["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("POST /api/sessions response has no session_id; body: %s", created.raw)
	}

	// 2. First chat message is accepted and delivered.
	first := h.doJSON(t, http.MethodPost, "/api/sessions/"+sessionID+"/chat", map[string]any{"message": "hello"})
	if first.status != http.StatusOK {
		t.Fatalf("first POST /api/sessions/%s/chat = %d, want 200; body: %s", sessionID, first.status, first.raw)
	}
	if delivered, _ := first.body["delivered"].(bool); !delivered {
		t.Fatalf("first chat response delivered != true; body: %s", first.raw)
	}

	// 3. The turn must persist a real user message and the model's reply.
	transcriptDir := h.transcriptDir(sessionID)
	parts := waitForTranscript(t, transcriptDir, func(parts []map[string]any) bool {
		return hasMessage(parts, "user", "hello") && hasMessage(parts, "assistant", "assistant reply")
	})
	if !hasMessage(parts, "assistant", "hello") {
		t.Fatalf("assistant reply does not reflect the user turn; parts: %s", describeParts(parts))
	}
	if h.completer.callCount() == 0 {
		t.Fatal("fake completer was never called; the chat turn did not run the agent loop")
	}

	// 4. The session is visible through the live-session route.
	detail := h.doJSON(t, http.MethodGet, "/api/sessions/"+sessionID, nil)
	if detail.status != http.StatusOK {
		t.Fatalf("GET /api/sessions/%s = %d, want 200; body: %s", sessionID, detail.status, detail.raw)
	}
	if got, _ := detail.body["session_id"].(string); got != sessionID {
		t.Fatalf("GET /api/sessions/%s session_id = %q; body: %s", sessionID, got, detail.raw)
	}

	// 5. A second chat message also succeeds and appends to the transcript.
	second := h.doJSON(t, http.MethodPost, "/api/sessions/"+sessionID+"/chat", map[string]any{"message": "again"})
	if second.status != http.StatusOK {
		t.Fatalf("second POST /api/sessions/%s/chat = %d, want 200; body: %s", sessionID, second.status, second.raw)
	}
	if delivered, _ := second.body["delivered"].(bool); !delivered {
		t.Fatalf("second chat response delivered != true; body: %s", second.raw)
	}

	parts = waitForTranscript(t, transcriptDir, func(parts []map[string]any) bool {
		return hasMessage(parts, "user", "again") && hasMessage(parts, "assistant", "again")
	})
	if got := countRole(parts, "user"); got != 2 {
		t.Fatalf("persisted user turns = %d, want 2; parts: %s", got, describeParts(parts))
	}
	if got := countRole(parts, "assistant"); got < 2 {
		t.Fatalf("persisted assistant turns = %d, want >= 2; parts: %s", got, describeParts(parts))
	}
	if got := h.completer.callCount(); got < 2 {
		t.Fatalf("fake completer calls = %d, want >= 2 (one per chat turn)", got)
	}
}

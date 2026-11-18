package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// fakeChatLLM is a Completer that never touches the network: it records each
// request and answers with the last user message it saw.
type fakeChatLLM struct {
	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (f *fakeChatLLM) ModelName() string { return "fake-chat-model" }

func (f *fakeChatLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	count := len(f.requests)
	f.mu.Unlock()

	last := ""
	for _, message := range req.Messages {
		if message.Role == "user" {
			last = message.Content
		}
	}
	return llm.Completion{
		Content:     "echo " + strings.TrimSpace(last),
		Model:       "fake-chat-model",
		StopReason:  "end_turn",
		InputTokens: count,
	}, nil
}

func (f *fakeChatLLM) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func newTestChatRuntime(t *testing.T, root string) (*ChatRuntime, *fakeChatLLM) {
	t.Helper()
	model := &fakeChatLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{LLM: model, Root: root})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, model
}

func newChatTestServer(t *testing.T, manager *SessionManager, create func(CreateSessionParams) (*Session, error)) *httptest.Server {
	t.Helper()
	app := NewApp(AppDeps{
		Manager:       manager,
		LiveSessions:  manager.ListSessions,
		CreateSession: create,
	})
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return server
}

func postCreateSession(t *testing.T, url string, body any) int {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	response, err := http.Post(url+"/api/sessions", "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("POST /api/sessions: %v", err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func postChat(t *testing.T, url, sessionID string, body any) (int, map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	response, err := http.Post(url+"/api/sessions/"+sessionID+"/chat", "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("POST chat: %v", err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return response.StatusCode, decoded
}

func waitForAssistantParts(t *testing.T, runtime *ChatRuntime, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var parts []map[string]any
	for time.Now().Before(deadline) {
		read, err := runtime.Transcript()
		if err != nil {
			t.Fatalf("Transcript: %v", err)
		}
		parts = read
		assistants := 0
		for _, part := range parts {
			if part["role"] == "assistant" {
				assistants++
			}
		}
		if assistants >= want {
			return parts
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d assistant messages; parts = %#v", want, parts)
	return nil
}

// Parity: framework/server/routes_execution.py::handle_chat (deliver → turn → transcript)
func TestChatRuntimeCreateSessionAndChat(t *testing.T) {
	root := t.TempDir()
	manager := NewSessionManager()
	var runtime *ChatRuntime
	var model *fakeChatLLM

	server := newChatTestServer(t, manager, func(params CreateSessionParams) (*Session, error) {
		id := "session_20260101_000000_abcdef01"
		if params.SessionID != nil && *params.SessionID != "" {
			id = *params.SessionID
		}
		session := NewSession(id, eventbus.NewEventBus())
		session.LoadedAt = float64(time.Now().UnixNano()) / 1e9
		created, built := newTestChatRuntime(t, root)
		runtime = created
		model = built
		session.Chat = created
		session.LLM = built
		manager.AddSession(session)
		return session, nil
	})

	status := postCreateSession(t, server.URL, map[string]any{})
	if status != http.StatusCreated {
		t.Fatalf("POST /api/sessions status = %d, want %d", status, http.StatusCreated)
	}
	if runtime == nil || model == nil {
		t.Fatal("CreateSession callback did not build a chat runtime")
	}

	sessionID := manager.ListSessions()[0].ID
	status, body := postChat(t, server.URL, sessionID, map[string]any{"message": "hello queen"})
	if status != http.StatusOK {
		t.Fatalf("chat status = %d body = %#v, want 200", status, body)
	}
	if body["delivered"] != true || body["status"] != "queen" {
		t.Fatalf("chat body = %#v, want {status: queen, delivered: true}", body)
	}

	parts := waitForAssistantParts(t, runtime, 1)
	if len(parts) < 2 {
		t.Fatalf("parts = %#v, want a user and an assistant message", parts)
	}
	if parts[0]["role"] != "user" || !strings.Contains(parts[0]["content"].(string), "hello queen") {
		t.Fatalf("first part = %#v, want the user message", parts[0])
	}
	if parts[1]["role"] != "assistant" || !strings.Contains(parts[1]["content"].(string), "echo") {
		t.Fatalf("second part = %#v, want the assistant reply", parts[1])
	}

	// The transcript must survive a restart: the runtime writes parts under
	// Root/conversations via the file store, so a fresh store sees them.
	partsOnDisk, err := agentloopFileParts(filepath.Join(root, "conversations"))
	if err != nil {
		t.Fatalf("read persisted parts: %v", err)
	}
	if len(partsOnDisk) < 2 {
		t.Fatalf("persisted parts = %d, want at least 2", len(partsOnDisk))
	}

	// A second message runs a second turn on the same conversation.
	status, body = postChat(t, server.URL, sessionID, map[string]any{"message": "and again"})
	if status != http.StatusOK || body["delivered"] != true {
		t.Fatalf("second chat status = %d body = %#v, want delivered", status, body)
	}
	parts = waitForAssistantParts(t, runtime, 2)
	if len(parts) < 4 {
		t.Fatalf("parts after second chat = %d, want at least 4", len(parts))
	}
	if model.requestCount() < 2 {
		t.Fatalf("model requests = %d, want at least 2", model.requestCount())
	}
}

// Parity: framework/server/routes_execution.py::handle_chat (error statuses)
func TestChatRouteRejectsMissingSessionEmptyMessageAndNoLLM(t *testing.T) {
	manager := NewSessionManager()
	server := newChatTestServer(t, manager, nil)

	status, body := postChat(t, server.URL, "session_missing", map[string]any{"message": "hi"})
	if status != http.StatusNotFound {
		t.Fatalf("missing session status = %d body = %#v, want 404", status, body)
	}

	modeless := NewSession("session_20260101_000000_abcdef02", eventbus.NewEventBus())
	manager.AddSession(modeless)

	status, body = postChat(t, server.URL, modeless.ID, map[string]any{"message": "hi"})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("no-LLM status = %d body = %#v, want 503", status, body)
	}
	if body["error"] == nil {
		t.Fatalf("no-LLM body = %#v, want an error message", body)
	}

	runtime, _ := newTestChatRuntime(t, t.TempDir())
	modeless.Chat = runtime
	status, body = postChat(t, server.URL, modeless.ID, map[string]any{"message": "   "})
	if status != http.StatusBadRequest || body["error"] != "message is required" {
		t.Fatalf("empty message status = %d body = %#v, want 400 message is required", status, body)
	}

	locked := NewSession("session_20260101_000000_abcdef03", eventbus.NewEventBus())
	locked.Chat = runtime
	locked.ColonySpawned = true
	locked.SpawnedColonyID = "colony_x"
	manager.AddSession(locked)
	status, body = postChat(t, server.URL, locked.ID, map[string]any{"message": "hi"})
	if status != http.StatusConflict || body["reason"] != "colony_spawned" {
		t.Fatalf("locked session status = %d body = %#v, want 409 colony_spawned", status, body)
	}
}

// agentloopFileParts reads the on-disk transcript through the same file store
// the runtime uses, proving the durable layout.
func agentloopFileParts(base string) ([]map[string]any, error) {
	store := agentloop.NewFileConversationStore(base)
	defer store.Close()
	if _, err := os.Stat(base); err != nil {
		return nil, err
	}
	return store.ReadParts()
}

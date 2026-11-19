package server

// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
// Parity: framework/server/routes_credentials.py::handle_credential_form
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
//
// The chat loop produces a credential_form park from inside its own turn. When
// a live overseer executes the colony, the chat loop never sees the failing
// tool; the overseer's completed tool event is the only signal. It must produce
// the same pending_input + client_credential_form_requested pair so the secure
// submission route can consume the wait and resume the overseer.

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

type overseerCredentialLLM struct {
	mu      sync.Mutex
	replies []llm.Completion
	calls   []llm.CompletionRequest
}

func (m *overseerCredentialLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	index := len(m.calls) - 1
	if index >= len(m.replies) {
		return llm.Completion{Content: "done", StopReason: "stop"}, nil
	}
	return m.replies[index], nil
}

func (m *overseerCredentialLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *overseerCredentialLLM) sawUserText(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.calls {
		for _, message := range req.Messages {
			if message.Role == "user" && strings.Contains(message.Content, text) {
				return true
			}
		}
	}
	return false
}

// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
// Parity: framework/server/routes_credentials.py::handle_credential_form
func TestOverseerCredentialMissParksAndResumesThroughRoute(t *testing.T) {
	homeRoot := t.TempDir()
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	t.Setenv("LINEAR_API_KEY", "")

	colonyBus := eventbus.NewEventBus()
	overseerProvider := &overseerCredentialLLM{replies: []llm.Completion{
		{Content: "idle", StopReason: "stop"},
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "linear-overseer-1",
				Name:      "linear_issue_get",
				Arguments: `{"issue_id":"ENG-42"}`,
			}},
			StopReason: "tool_calls",
		},
		{Content: "I need the Linear API key first.", StopReason: "stop"},
		{Content: "Thanks, retrying with the saved key.", StopReason: "stop"},
	}}
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         overseerProvider,
		EventBus:    colonyBus,
		StreamID:    "colony_overseer_credential",
		StoragePath: t.TempDir(),
		Tools: []llm.ToolDef{{
			Name:        "linear_issue_get",
			Description: "fetch one Linear issue",
			Parameters:  map[string]any{"type": "object"},
		}},
		ToolExecutor: func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			if req.Name == "linear_issue_get" {
				return agentloop.ToolResult{
					Content: `{"error": "Linear credentials not configured", "help": "Set LINEAR_API_KEY or configure the credential store."}`,
				}, nil
			}
			return agentloop.ToolResult{Content: "ok"}, nil
		},
	})
	if err := colonyRuntime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(colonyRuntime.Stop)
	if _, err := colonyRuntime.StartOverseer(context.Background(), relayQueenSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForServerCondition(3*time.Second, func() bool { return overseerProvider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}

	sessionBus := eventbus.NewEventBus()
	chatProvider := &fakeChatLLM{}
	root := t.TempDir()
	sessionID := "session_20260101_000000_overseercred"
	chatRuntime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       chatProvider,
		Bus:       sessionBus,
		Root:      root,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chatRuntime.Close() })
	chatRuntime.BindColony(colonyHostStub{runtime: colonyRuntime})
	recorder := recordChatEvents(sessionBus)

	if err := chatRuntime.Deliver("open ENG-42"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !waitForServerCondition(3*time.Second, func() bool {
		return len(recorder.ofType(eventbus.EventClientCredentialFormRequested)) == 1
	}) {
		t.Fatalf("session bus events = %v, want the overseer's credential form request",
			recorder.types())
	}
	formEvents := recorder.ofType(eventbus.EventClientCredentialFormRequested)
	if formEvents[0].StreamID != chatStreamID {
		t.Fatalf("credential form stream = %q, want %q", formEvents[0].StreamID, chatStreamID)
	}
	if id, _ := formEvents[0].Data["credential_id"].(string); id != "linear" {
		t.Fatalf("credential form id = %q, want linear (data %#v)", id, formEvents[0].Data)
	}
	if fields, _ := formEvents[0].Data["fields"].([]any); len(fields) != 1 {
		t.Fatalf("credential form fields = %#v, want one masked field", formEvents[0].Data["fields"])
	}

	pending := waitForParkReason(t, root, "credential_form")
	form, _ := pending["credential_form"].(map[string]any)
	if form["credential_id"] != "linear" || form["account"] != "default" {
		t.Fatalf("persisted credential_form = %#v", pending["credential_form"])
	}
	if chatRuntime.PendingCredentialForm() == nil {
		t.Fatal("runtime lost the overseer-produced pending credential form")
	}

	manager := NewSessionManager()
	session := NewSession(sessionID, sessionBus)
	session.Chat = chatRuntime
	// GetSession re-binds the session's colony on lookup; the record must carry
	// the same live colony the chat runtime was bound to, exactly as serve does.
	session.Colony = colonyHostStub{runtime: colonyRuntime}
	manager.AddSession(session)
	mux := credentialRouteTestMux(CredentialRouteDeps{
		Home:    hivehome.Home{Root: homeRoot},
		Manager: manager,
	})
	recorderHTTP, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/"+sessionID+"/credential-form",
		`{"status":"saved","credential_id":"linear","account":"default","keys":{"api_key":"sk-overseer-test"}}`)
	if recorderHTTP.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorderHTTP.Code, recorderHTTP.Body.String())
	}
	if strings.Contains(recorderHTTP.Body.String(), "sk-overseer-test") {
		t.Fatalf("response leaked the submitted secret: %s", recorderHTTP.Body.String())
	}
	if body["saved"] != "linear/default" || body["resumed"] != true {
		t.Fatalf("body = %#v, want saved linear/default and resumed true", body)
	}
	if chatRuntime.PendingCredentialForm() != nil {
		t.Fatal("pending credential form survived the secure submission")
	}
	pendingAfter := waitForParkReason(t, root, "turn_done")
	if pendingAfter["credential_form"] != nil {
		t.Fatalf("pending_input credential_form after submission = %#v, want nil", pendingAfter["credential_form"])
	}

	encrypted, err := credentials.NewEncryptedFileStorage(filepath.Join(homeRoot, "credentials"), key)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	stored, err := encrypted.Load("linear")
	if err != nil || stored == nil {
		t.Fatalf("stored credential = %v, err = %v", stored, err)
	}
	if value := stored.GetKey("api_key"); value == nil || *value != "sk-overseer-test" {
		t.Fatalf("stored api_key = %v", value)
	}

	// The resume confirmation is relayed to the overseer (the turn owner),
	// not answered by the idle chat queen.
	if !waitForServerCondition(5*time.Second, func() bool {
		return overseerProvider.sawUserText("was saved securely")
	}) {
		t.Fatal("the overseer never received the credential-form resume turn")
	}
	if calls := chatProvider.requestCount(); calls != 0 {
		t.Fatalf("chat queen ran %d turn(s); the overseer owns the session", calls)
	}
}

package server

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: core/framework/server/routes_credentials.py::handle_credential_form
func TestCredentialFormSubmissionStoresSecretAndResumesQueen(t *testing.T) {
	homeRoot := t.TempDir()
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	t.Setenv("LINEAR_API_KEY", "")

	root := t.TempDir()
	bus := eventbus.NewEventBus()
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "linear-form-1", Name: "linear_issue_get", Arguments: `{"issue_id":"ENG-9"}`}}},
		{Content: "I need the Linear API key first."},
		{Content: "Thanks, retrying with the saved key."},
	}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     []llm.ToolDef{{Name: "linear_issue_get", Description: "fetch one Linear issue", Parameters: map[string]any{"type": "object"}}},
		Execute:   missingCredentialToolExecutor,
		Root:      root,
		Bus:       bus,
		SessionID: "session_20260101_000000_credform01",
		QueenName: "default",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	manager := NewSessionManager()
	session := NewSession("session_20260101_000000_credform01", bus)
	session.Chat = runtime
	session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": runtime}}
	manager.AddSession(session)

	if err := runtime.Deliver("open ENG-9"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForParkReason(t, root, "credential_form")

	mux := credentialRouteTestMux(CredentialRouteDeps{
		Home:    hivehome.Home{Root: homeRoot},
		Manager: manager,
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/session_20260101_000000_credform01/credential-form",
		`{"status":"saved","credential_id":"linear","account":"default","keys":{"api_key":"sk-linear-secret"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "sk-linear-secret") {
		t.Fatalf("response leaked the submitted secret: %s", recorder.Body.String())
	}
	if body["saved"] != "linear/default" || body["resumed"] != true {
		t.Fatalf("body = %#v, want saved linear/default and resumed true", body)
	}

	// The value landed in the same encrypted store the settings page uses.
	encrypted, err := credentials.NewEncryptedFileStorage(filepath.Join(homeRoot, "credentials"), key)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	stored, err := encrypted.Load("linear")
	if err != nil || stored == nil {
		t.Fatalf("stored credential = %v, err = %v", stored, err)
	}
	if value := stored.GetKey("api_key"); value == nil || *value != "sk-linear-secret" {
		t.Fatalf("stored api_key = %v", value)
	}

	// The park was consumed and the queen resumed with a non-secret message.
	if runtime.pendingCredentialFormSet() {
		t.Fatal("pending credential form survived a successful submission")
	}
	pending := waitForParkReason(t, root, "turn_done")
	if pending["credential_form"] != nil {
		t.Fatalf("pending_input credential_form after submit = %#v, want nil", pending["credential_form"])
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(model.requestSnapshot()) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(model.requestSnapshot()); got < 3 {
		t.Fatalf("model calls = %d, want the resumed turn (a third call)", got)
	}
	for _, request := range model.requestSnapshot() {
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "sk-linear-secret") {
				t.Fatalf("resume turn carried the secret into the model conversation")
			}
		}
	}
}

// Parity: core/framework/server/routes_credentials.py::handle_credential_form
func TestCredentialFormSubmissionRequiresPendingForm(t *testing.T) {
	root := t.TempDir()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{},
		Root:      root,
		SessionID: "session_20260101_000000_credform02",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	manager := NewSessionManager()
	session := NewSession("session_20260101_000000_credform02", eventbus.NewEventBus())
	session.Chat = runtime
	manager.AddSession(session)

	mux := credentialRouteTestMux(CredentialRouteDeps{
		Home:    hivehome.Home{Root: t.TempDir()},
		Manager: manager,
	})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/session_20260101_000000_credform02/credential-form",
		`{"credential_id":"linear","keys":{"api_key":"sk-other"}}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "sk-other") {
		t.Fatal("rejection response leaked the submitted secret")
	}
}

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// credentialToolRecorder stands in for the pre-change catalogue: it answers
// every credentials call with the old not_implemented envelope.
type credentialToolRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *credentialToolRecorder) execute(_ context.Context, name string, _ map[string]any) (string, bool, []map[string]any, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name)
	r.mu.Unlock()
	return `{"error":"not_implemented","tool":"` + name + `"}`, true, nil, nil
}

func (r *credentialToolRecorder) called() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func newCredentialToolRuntime(t *testing.T, sessionDir string, recorder *credentialToolRecorder, deps ChatRuntimeDeps) *ChatRuntime {
	t.Helper()
	deps.LLM = &fakeChatLLM{}
	deps.Tools = []llm.ToolDef{{Name: tools.CredentialsToolName}}
	deps.ExecuteBlocks = recorder.execute
	deps.Root = sessionDir
	deps.SessionID = "sess_credentials"
	if deps.Bus == nil {
		deps.Bus = eventbus.NewEventBus()
	}
	runtime, err := NewChatRuntime(deps)
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (credentials branch)
//
// Negative regression: the runtime must answer the credentials tool itself.
// If the new intercept were removed, the call would fall through to the tool
// executor (the old not_implemented envelope) and no pin would be written.
func TestChatCredentialsToolInterceptsBeforeCatalogue(t *testing.T) {
	sessionDir := t.TempDir()
	recorder := &credentialToolRecorder{}
	runtime := newCredentialToolRuntime(t, sessionDir, recorder, ChatRuntimeDeps{})

	result, err := runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "attach", "credential_id": "stripe", "account": "work"},
	})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if result.IsError || result.Content != "Attached 'stripe' (account 'work') to this session. It will be reminded in your context each turn." {
		t.Fatalf("attach result = (%q, %v)", result.Content, result.IsError)
	}
	if calls := recorder.called(); len(calls) != 0 {
		t.Fatalf("credentials leaked to the tool executor: %#v", calls)
	}
	refs := tools.ReadCredentialAttachments(sessionDir)
	if len(refs) != 1 || refs[0].CredentialID != "stripe" || refs[0].Account != "work" {
		t.Fatalf("pinned refs = %#v", refs)
	}

	// help and detach stay on the same path.
	result, _ = runtime.executeTool(agentloop.ToolCallRequest{Name: tools.CredentialsToolName, Input: map[string]any{}})
	if result.IsError || result.Content != tools.CredentialsHelp() {
		t.Fatalf("help result = (%q, %v)", result.Content, result.IsError)
	}
	result, _ = runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "detach", "credential_id": "stripe", "account": "work"},
	})
	if result.IsError || result.Content != "Detached 'stripe' (account 'work') from this session." {
		t.Fatalf("detach result = (%q, %v)", result.Content, result.IsError)
	}
	if calls := recorder.called(); len(calls) != 0 {
		t.Fatalf("credentials leaked to the tool executor: %#v", calls)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (credentials collect branch)
func TestChatCredentialsCollectParksSecureForm(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := newCredentialToolRuntime(t, t.TempDir(), &credentialToolRecorder{}, ChatRuntimeDeps{Bus: bus})

	forms := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventClientCredentialFormRequested}, func(event *eventbus.AgentEvent) {
		select {
		case forms <- event:
		default:
		}
	})

	result, err := runtime.executeTool(agentloop.ToolCallRequest{
		Name: tools.CredentialsToolName,
		Input: map[string]any{
			"action":        "collect",
			"credential_id": "stripe",
			"account":       "work",
			"title":         "Connect Stripe",
			"fields": []any{
				map[string]any{"name": "api_key", "label": "Secret key", "secret": true},
			},
		},
	})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("collect reported an error: %s", result.Content)
	}
	want := "Showing the user a secure form for 'stripe' (account 'work', fields: api_key). " +
		"Waiting for them to submit — the values are stored securely and are not shown to you."
	if result.Content != want {
		t.Fatalf("collect result = %q, want %q", result.Content, want)
	}
	form := runtime.PendingCredentialForm()
	if form == nil {
		t.Fatal("collect did not park on a credential form")
	}
	if form["credential_id"] != "stripe" || form["account"] != "work" || form["title"] != "Connect Stripe" {
		t.Fatalf("form = %#v", form)
	}
	if correlation, _ := form["correlation_id"].(string); correlation == "" {
		t.Fatalf("form lost correlation_id: %#v", form)
	}
	if fields, ok := form["fields"].([]any); !ok || len(fields) != 1 || fields[0].(map[string]any)["name"] != "api_key" || fields[0].(map[string]any)["secret"] != true {
		t.Fatalf("form fields = %#v", form["fields"])
	}
	select {
	case event := <-forms:
		if event.StreamID != chatStreamID || event.Data["credential_id"] != "stripe" || event.Data["account"] != "work" {
			t.Fatalf("form event = %#v", event)
		}
	default:
		t.Fatal("collect did not emit client_credential_form_requested")
	}

	// An invalid collect is a tool error and leaves no park behind.
	runtime.ClearCredentialFormPark()
	result, _ = runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "collect", "credential_id": "bad id"},
	})
	if !result.IsError || !strings.HasPrefix(result.Content, "ERROR: collect requires a valid credential_id") {
		t.Fatalf("invalid collect = (%q, %v)", result.Content, result.IsError)
	}
	if runtime.PendingCredentialForm() != nil {
		t.Fatal("invalid collect parked a form")
	}
}

// Parity-exempt: upstream's credential_tool.attachments_path() has no unit
// test; this pins the cross-runtime file shape and location.
func TestChatCredentialsAttachmentsUsePythonGlobalPath(t *testing.T) {
	home := t.TempDir()
	sessionDir := filepath.Join(home, "queens", "default", "sessions", "session_20260101_000000_abcd1234")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := newCredentialToolRuntime(t, sessionDir, &credentialToolRecorder{}, ChatRuntimeDeps{HomeRoot: home})
	if result, _ := runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "attach", "credential_id": "stripe", "account": "work"},
	}); result.IsError {
		t.Fatalf("attach failed: %s", result.Content)
	}

	path := filepath.Join(home, "sessions", "sess_credentials", "attached_credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Python's json.dumps(refs, indent=2) writes exactly this shape.
	want := "[\n  {\n    \"credential_id\": \"stripe\",\n    \"account\": \"work\"\n  }\n]"
	if string(data) != want {
		t.Fatalf("attachment file = %q, want %q", data, want)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "attached_credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy session-local file was written: %v", err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "token") {
		t.Fatalf("attachment file carries more than refs: %s", data)
	}
}

// Parity-exempt: upstream reads a list of dicts and ignores malformed entries;
// this pins that tolerance plus the one-time migration of the older Go
// session-local file.
func TestChatCredentialsReadsPythonFileAndMigratesLegacyFile(t *testing.T) {
	home := t.TempDir()
	sessionDir := filepath.Join(home, "colonies", "probe", "queens", "default", "sessions", "sess_credentials")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalDir := filepath.Join(home, "sessions", "sess_credentials")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A Python-written file with one valid ref, an object missing
	// credential_id, and a non-object element. Only the valid ref survives.
	pythonFile := "[\n  {\"credential_id\": \"google\", \"account\": \"email3\"},\n  {\"account\": \"orphan\"},\n  \"not-a-ref\"\n]"
	if err := os.WriteFile(filepath.Join(globalDir, "attached_credentials.json"), []byte(pythonFile), 0o644); err != nil {
		t.Fatal(err)
	}
	// The pre-interop Go build wrote beside state.json; it must be merged and
	// then removed once the global file is rewritten.
	legacyFile := "[\n  {\n    \"credential_id\": \"stripe\",\n    \"account\": \"work\"\n  }\n]"
	if err := os.WriteFile(filepath.Join(sessionDir, "attached_credentials.json"), []byte(legacyFile), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime := newCredentialToolRuntime(t, sessionDir, &credentialToolRecorder{}, ChatRuntimeDeps{HomeRoot: home})
	// A duplicate attach is also the upgrade trigger: the legacy pin moves to
	// the global file even though no new ref is added.
	if result, _ := runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "attach", "credential_id": "stripe", "account": "work"},
	}); result.IsError || result.Content != "Credential 'stripe' (account 'work') is already attached to this session." {
		t.Fatalf("duplicate attach = (%q, %v)", result.Content, result.IsError)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "attached_credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("duplicate attach did not migrate the legacy file: %v", err)
	}
	if refs := tools.ReadCredentialAttachmentsForSession(home, "sess_credentials", sessionDir); len(refs) != 2 {
		t.Fatalf("refs after migration = %#v", refs)
	}
	if result, _ := runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "attach", "credential_id": "slack", "account": "team"},
	}); result.IsError {
		t.Fatalf("attach failed: %s", result.Content)
	}
	refs := tools.ReadCredentialAttachmentsForSession(home, "sess_credentials", sessionDir)
	if len(refs) != 3 || refs[0].CredentialID != "google" || refs[1].CredentialID != "stripe" || refs[2].CredentialID != "slack" {
		t.Fatalf("merged refs = %#v", refs)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "attached_credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy file was not migrated: %v", err)
	}

	if result, _ := runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "detach", "credential_id": "stripe", "account": "work"},
	}); result.IsError {
		t.Fatalf("detach failed: %s", result.Content)
	}
	refs = tools.ReadCredentialAttachmentsForSession(home, "sess_credentials", sessionDir)
	if len(refs) != 2 || refs[0].CredentialID != "google" || refs[1].CredentialID != "slack" {
		t.Fatalf("refs after detach = %#v", refs)
	}
}

// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
//
// The pinned detail is re-read when the tool runs, so the next model request
// sees the pin instead of waiting for a restart.
func TestChatCredentialsAttachDetachRefreshesPinnedBlock(t *testing.T) {
	sessionDir := t.TempDir()
	pinned := func() string {
		if len(tools.ReadCredentialAttachments(sessionDir)) == 0 {
			return ""
		}
		return "# Connected integrations\n\ngoogle\n  \"email3\""
	}
	runtime := newCredentialToolRuntime(t, sessionDir, &credentialToolRecorder{}, ChatRuntimeDeps{
		PinnedCredentials: pinned,
	})
	if prompt := runtime.conv.SystemPrompt(); strings.Contains(prompt, "Pinned to this session:") {
		t.Fatalf("fresh session already has a pinned block: %s", prompt)
	}

	if result, _ := runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "attach", "credential_id": "google", "account": "email3"},
	}); result.IsError {
		t.Fatalf("attach failed: %s", result.Content)
	}
	prompt := runtime.conv.SystemPrompt()
	if !strings.Contains(prompt, "Pinned to this session:\n# Connected integrations") || !strings.Contains(prompt, "google") {
		t.Fatalf("attach did not rebase the pinned block: %s", prompt)
	}

	if result, _ := runtime.executeTool(agentloop.ToolCallRequest{
		Name:  tools.CredentialsToolName,
		Input: map[string]any{"action": "detach", "credential_id": "google", "account": "email3"},
	}); result.IsError {
		t.Fatalf("detach failed: %s", result.Content)
	}
	if prompt := runtime.conv.SystemPrompt(); strings.Contains(prompt, "Pinned to this session:") {
		t.Fatalf("detach left the pinned block behind: %s", prompt)
	}
}

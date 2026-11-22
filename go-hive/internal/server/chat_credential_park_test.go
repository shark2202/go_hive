package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// pendingCredentialFormSet reports whether the runtime is waiting on a
// credential form. Test-only helper: the field is guarded by mu.
func (r *ChatRuntime) pendingCredentialFormSet() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingCredentialForm != nil
}

// waitForRuntimePark waits until the runtime reports the given park reason as
// its settled wait.
func waitForRuntimePark(r *ChatRuntime, want agentloop.ParkReason) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.parkMu.Lock()
		parked := r.parkReason
		r.parkMu.Unlock()
		if r.awaiting.Load() && parked == want {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.parkMu.Lock()
	parked := r.parkReason
	r.parkMu.Unlock()
	return fmt.Errorf("park reason = %q, want %q", parked, want)
}

// missingCredentialToolExecutor is the integration-tool result shape: a JSON
// error dict naming the missing credential, not a Go error.
func missingCredentialToolExecutor(ctx context.Context, name string, input map[string]any) (string, bool, error) {
	return `{"error": "Linear credentials not configured", "help": "Set LINEAR_API_KEY or configure the credential store."}`, false, nil
}

// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
func TestMissingCredentialToolResultParksCredentialForm(t *testing.T) {
	root := t.TempDir()
	model := &queenChatLLM{responses: []llm.Completion{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "linear-1",
				Name:      "linear_issue_get",
				Arguments: `{"issue_id":"ENG-1"}`,
			}},
		},
		{Content: "I need the Linear API key first."},
	}}
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     []llm.ToolDef{{Name: "linear_issue_get", Description: "fetch one Linear issue", Parameters: map[string]any{"type": "object"}}},
		Execute:   missingCredentialToolExecutor,
		Root:      root,
		Bus:       bus,
		SessionID: "session_20260101_000000_cred001",
		QueenName: "default",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	defer runtime.Close()
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("open ENG-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	formEvents := recorder.ofType(eventbus.EventClientCredentialFormRequested)
	if len(formEvents) != 1 {
		t.Fatalf("client_credential_form_requested events = %d, want 1", len(formEvents))
	}
	form, _ := formEvents[0].Data["credential_id"].(string)
	if form != "linear" {
		t.Fatalf("form credential_id = %q, want linear (data %#v)", form, formEvents[0].Data)
	}
	if fields, _ := formEvents[0].Data["fields"].([]any); len(fields) != 1 {
		t.Fatalf("form fields = %#v, want one masked field", formEvents[0].Data["fields"])
	}

	// The parked turn ends on credential_form, not turn_done/ask_user.
	pending := waitForParkReason(t, root, "credential_form")
	persisted, _ := pending["credential_form"].(map[string]any)
	if persisted["credential_id"] != "linear" || persisted["account"] != "default" {
		t.Fatalf("persisted credential_form = %#v", pending["credential_form"])
	}
	if !runtime.pendingCredentialFormSet() {
		t.Fatal("runtime lost the pending credential form")
	}
}

// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
func TestCredentialFormParkSurvivesRestartAndClearsOnAnswer(t *testing.T) {
	root := t.TempDir()
	sessionID := "session_20260101_000000_cred002"
	first, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &queenChatLLM{responses: []llm.Completion{
			{ToolCalls: []llm.ToolCall{{ID: "linear-2", Name: "linear_issue_get", Arguments: `{}`}}},
			{Content: "Waiting on the key."},
		}},
		Tools:     []llm.ToolDef{{Name: "linear_issue_get", Description: "fetch one Linear issue", Parameters: map[string]any{"type": "object"}}},
		Execute:   missingCredentialToolExecutor,
		Root:      root,
		Bus:       eventbus.NewEventBus(),
		SessionID: sessionID,
		QueenName: "default",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	if err := first.Deliver("open ENG-2"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(first, agentloop.ParkReasonCredentialForm); err != nil {
		t.Fatalf("%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A restarted runtime re-enters the credential wait from the cursor.
	secondBus := eventbus.NewEventBus()
	second, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{responses: []llm.Completion{{Content: "Thanks, retrying."}}},
		Root:      root,
		Bus:       secondBus,
		SessionID: sessionID,
		QueenName: "default",
	})
	if err != nil {
		t.Fatalf("restart NewChatRuntime: %v", err)
	}
	defer second.Close()
	second.parkMu.Lock()
	restored := second.parkReason
	second.parkMu.Unlock()
	if restored != agentloop.ParkReasonCredentialForm {
		t.Fatalf("restored park reason = %q, want credential_form", restored)
	}
	if !second.pendingCredentialFormSet() {
		t.Fatal("restarted runtime lost the persisted credential form")
	}

	// A real user message answers the wait: the form is dropped on disk and in
	// memory before the next turn's park is written.
	secondRecorder := recordChatEvents(secondBus)
	if err := second.Deliver("key submitted"); err != nil {
		t.Fatalf("Deliver answer: %v", err)
	}
	waitForChatEvents(t, secondRecorder, eventbus.EventExecutionCompleted, 1)
	if second.pendingCredentialFormSet() {
		t.Fatal("pending credential form survived the user's answer")
	}
	pending := waitForParkReason(t, root, "turn_done")
	if pending["credential_form"] != nil {
		t.Fatalf("pending_input credential_form after answer = %#v, want nil", pending["credential_form"])
	}
}

// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
func TestCredentialFormNotProducedForUnrelatedToolErrors(t *testing.T) {
	root := t.TempDir()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &queenChatLLM{responses: []llm.Completion{
			{ToolCalls: []llm.ToolCall{{ID: "mattermost-1", Name: "mattermost_post", Arguments: `{}`}}},
			{Content: "The server URL is missing."},
		}},
		Tools: []llm.ToolDef{{Name: "mattermost_post", Description: "post a message", Parameters: map[string]any{"type": "object"}}},
		Execute: func(ctx context.Context, name string, input map[string]any) (string, bool, error) {
			return `{"error": "Mattermost server URL not configured"}`, false, nil
		},
		Root:      root,
		Bus:       eventbus.NewEventBus(),
		SessionID: "session_20260101_000000_cred003",
		QueenName: "default",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	defer runtime.Close()
	if err := runtime.Deliver("post the update"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := waitForRuntimePark(runtime, agentloop.ParkReasonTurnDone); err != nil {
		t.Fatalf("%v", err)
	}
	if runtime.pendingCredentialFormSet() {
		t.Fatal("an unrelated 'not configured' error opened a credential form")
	}
}

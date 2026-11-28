package server

// The chat runtime's `credentials` tool dispatch: help / collect / attach /
// detach. Upstream parks the loop on ParkReason.CREDENTIAL_FORM when the queen
// calls credentials(collect) — the form values go straight to the encrypted
// store — and pins credentials to the session with attach/detach so the
// per-turn ambient block re-injects their detail.
//
// The Go port already had the park producer for a *missing* credential
// (chat_credential_park.go). This file is the direct tool surface: the model
// calls credentials(action=...) itself, and the non-blocking actions run in
// the shared tool helpers.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (credentials branch)
// Parity: framework/agent_loop/internals/credential_tool.py

import (
	"fmt"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// executeCredentialsTool dispatches one `credentials` call. It runs before the
// tool executor so `collect` can park the loop and `attach`/`detach` can
// rebase the session's credentials block on the next model request.
func (r *ChatRuntime) executeCredentialsTool(input map[string]any) agentloop.ToolResult {
	action := tools.CredentialsAction(input)
	if action == "collect" {
		return r.parkCredentialCollect(input)
	}
	content, isError := tools.ExecuteCredentialsToolForSession(
		r.credentialsHiveHome(), r.credentialsSessionID(), r.credentialsSessionDir(), input)
	if !isError && (action == "attach" || action == "detach") {
		r.refreshPinnedCredentialsPrompt()
	}
	return agentloop.ToolResult{Content: content, IsError: isError}
}

// parkCredentialCollect validates the collect call, stores the no-secret form
// spec as the turn's park, persists it (so a restart re-enters the wait) and
// announces it on the session bus. The model gets a confirmation that names
// the fields but never a value.
func (r *ChatRuntime) parkCredentialCollect(input map[string]any) agentloop.ToolResult {
	payload, problem := tools.ValidateCredentialsCollect(input)
	if problem != "" {
		return agentloop.ToolResult{Content: "ERROR: " + problem, IsError: true}
	}
	payload["correlation_id"] = newChatExecutionID()
	r.mu.Lock()
	r.pendingCredentialForm = payload
	r.mu.Unlock()
	// Persist immediately: if the process dies before the turn's end
	// checkpoint, the form is still the wait a restarted session re-enters.
	r.persistParkState()
	if r.bus != nil && r.agentCtx != nil {
		executionID := r.agentCtx.ExecutionID
		r.bus.EmitCredentialFormRequested(chatStreamID, chatNodeID, &executionID, payload)
	}
	fieldNames := make([]string, 0)
	if fields, ok := payload["fields"].([]map[string]any); ok {
		for _, field := range fields {
			fieldNames = append(fieldNames, fmt.Sprint(field["name"]))
		}
	}
	return agentloop.ToolResult{Content: fmt.Sprintf(
		"Showing the user a secure form for '%s' (account '%s', fields: %s). "+
			"Waiting for them to submit — the values are stored securely and are not shown to you.",
		payload["credential_id"], payload["account"], strings.Join(fieldNames, ", "),
	)}
}

// credentialsSessionDir is the session's own directory. Pins live in
// upstream's global `$HIVE_HOME/sessions/<sid>` (see credentialsHiveHome and
// credentialsSessionID); this directory is only the read fallback for anchors
// written by an older Go build.
func (r *ChatRuntime) credentialsSessionDir() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.deps.Root)
}

// credentialsHiveHome is the HIVE_HOME pins are rooted under.
func (r *ChatRuntime) credentialsHiveHome() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.deps.HomeRoot)
}

// credentialsSessionID is the id upstream's attachments_path() keys the file
// by.
func (r *ChatRuntime) credentialsSessionID() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.deps.SessionID)
}

// refreshPinnedCredentialsPrompt rebuilds the cache-stable system prompt so a
// just-pinned (or just-unpinned) credential's detail lands in the next model
// request. The turn goroutine is the conversation's only writer, so this is
// the same safe seam as the identity rebase.
func (r *ChatRuntime) refreshPinnedCredentialsPrompt() {
	if r == nil || r.conv == nil || strings.TrimSpace(r.appliedQueenIdentity) == "" {
		return
	}
	r.conv.UpdateSystemPrompt(chatQueenStaticPromptWithIdentity(r.deps, r.appliedQueenIdentity), nil)
}

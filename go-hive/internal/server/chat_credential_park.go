package server

// Credential-form park producer.
//
// Upstream parks the agent loop on ParkReason.CREDENTIAL_FORM when the queen
// calls credentials(collect): the loop writes the pending form into the cursor
// and emits CLIENT_CREDENTIAL_FORM_REQUESTED, and the user's submission goes
// straight to the encrypted store. The Go port had the park reason enum and the
// event type but nothing ever produced either.
//
// This file is that producer for the path the Go session actually takes: an
// integration tool that fails because its credential is missing. Instead of
// returning only an error, the runtime resolves the credential spec behind the
// tool, parks the turn on a no-secret form spec, persists it (so a restart
// re-enters the wait), and emits the same client event the dashboard's
// AgentCredentialForm renders. The tool result tells the model a form is open
// and that it must wait for the user.
//
// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (credential_form branch)
// Parity: framework/host/event_bus.py::emit_credential_form_requested

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

// credentialMissingMarker is the fragment every integration tool uses when its
// credential is absent ("<Name> credentials not configured" / "... API key not
// configured"). Kept as two narrow markers so an unrelated "not configured"
// error (a missing server URL, say) never pops a credential form.
const (
	credentialMissingMarker    = "credentials not configured"
	credentialAPIKeyMarker     = "api key not configured"
	credentialFormWaitingReply = "A secure credential form is now open for the user. Values go straight to the encrypted store and are never shown to you. Wait for the user to submit, then retry the tool."
)

// parkForMissingCredential inspects one tool result and, when it reports a
// missing credential for a tool the generated spec table knows, records the
// pending form, persists the park, and announces it on the session bus. It
// returns the annotated tool content and whether it parked.
func (r *ChatRuntime) parkForMissingCredential(toolName, content string) (string, bool) {
	if r == nil || !missingCredentialResult(content) {
		return content, false
	}
	spec, ok := credentialSpecForTool(toolName)
	if !ok {
		return content, false
	}
	form := credentialFormPayload(spec)
	r.mu.Lock()
	if existing := r.pendingCredentialForm; existing != nil {
		if currentID, _ := existing["credential_id"].(string); currentID == form["credential_id"] {
			// The same form is already open (the model retried the tool before
			// the user answered). Do not re-emit a duplicate client request.
			r.mu.Unlock()
			return content + "\n\n" + credentialFormWaitingReply, true
		}
	}
	r.pendingCredentialForm = form
	r.mu.Unlock()
	// Persist immediately: if the process dies before the turn's end
	// checkpoint, the form is still the wait a restarted session re-enters.
	r.persistParkState()
	if r.bus != nil {
		executionID := r.agentCtx.ExecutionID
		r.bus.EmitCredentialFormRequested(chatStreamID, chatNodeID, &executionID, form)
	}
	return content + "\n\n" + credentialFormWaitingReply, true
}

// PendingCredentialForm returns a copy of the secure form this session is
// parked on, or nil when no credential_form wait is active. It is the read
// side of the submission entry: the route only accepts a submission that
// answers the wait the runtime actually persisted.
func (r *ChatRuntime) PendingCredentialForm() map[string]any {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneParkForm(r.pendingCredentialForm)
}

// ClearCredentialFormPark ends the credential wait once the submitted values
// have reached the encrypted store. It drops the in-memory form and the
// persisted pending_input marker before the resume turn runs, so a crash
// between the save and the resume cannot re-enter the same wait.
func (r *ChatRuntime) ClearCredentialFormPark() {
	if r == nil {
		return
	}
	r.clearPendingCredentialForm()
}

// missingCredentialResult reports whether a tool result is an error payload
// carrying the missing-credential marker the integration modules emit. The
// error-shape guard keeps unrelated prose (a file the agent read) from opening
// a credential form.
func missingCredentialResult(content string) bool {
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "\"error\"") && !strings.HasPrefix(strings.TrimSpace(lower), "error") {
		return false
	}
	if strings.Contains(lower, credentialMissingMarker) || strings.Contains(lower, credentialAPIKeyMarker) {
		return true
	}
	return strings.Contains(lower, "not configured") && strings.Contains(lower, "credential")
}

// credentialSpecForTool resolves the generated credential spec behind a tool
// name: the spec's declared tool list first (authoritative), then the tool's
// provider prefix (e.g. "linear_issue_get" → "linear").
func credentialSpecForTool(toolName string) (credentials.GeneratedCredentialSpec, bool) {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return credentials.GeneratedCredentialSpec{}, false
	}
	for _, spec := range credentials.GeneratedCredentialSpecs {
		for _, declared := range spec.Tools {
			if declared == name {
				return spec, true
			}
		}
	}
	prefix := name
	if index := strings.Index(name, "_"); index > 0 {
		prefix = name[:index]
	}
	for _, spec := range credentials.GeneratedCredentialSpecs {
		if spec.CredentialID == prefix || spec.Name == prefix {
			return spec, true
		}
	}
	return credentials.GeneratedCredentialSpec{}, false
}

// credentialFormPayload builds the no-secret form spec the client renders:
// credential id, account alias, title, instructions and one masked field. Its
// shape mirrors the upstream collect payload, including correlation_id.
//
// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
// Parity: framework/agent_loop/internals/credential_tool.py::_sanitize_fields
func credentialFormPayload(spec credentials.GeneratedCredentialSpec) map[string]any {
	credentialID := strings.TrimSpace(spec.CredentialID)
	if credentialID == "" {
		credentialID = strings.TrimSpace(spec.Name)
	}
	displayName := strings.TrimSpace(spec.Name)
	if displayName == "" {
		displayName = credentialID
	}
	fieldName := strings.TrimSpace(spec.CredentialKey)
	if fieldName == "" {
		fieldName = "api_key"
	}
	return map[string]any{
		"credential_id": credentialID,
		"account":       "default",
		"title":         "Connect " + displayName,
		"instructions":  spec.APIKeyInstructions,
		"fields": []any{
			map[string]any{
				"name":        fieldName,
				"label":       credentialFieldLabel(fieldName),
				"secret":      true,
				"required":    true,
				"placeholder": "",
			},
		},
		"correlation_id": newChatExecutionID(),
	}
}

// credentialFieldLabel turns "api_key" into "Api Key" for the form label.
func credentialFieldLabel(fieldName string) string {
	words := strings.Split(strings.ReplaceAll(fieldName, "_", " "), " ")
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}

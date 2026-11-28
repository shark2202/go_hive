package server

// Secure credential-form submission for a parked chat session.
//
// Upstream renders the form the agent popped (credentials(collect)) and posts
// it back to POST /api/sessions/{id}/credential-form: the secret values go
// straight into the encrypted store, never back to the model, and the parked
// queen loop is resumed with a confirmation that names only the fields. The Go
// port already produced the form (chat_credential_park.go), emitted
// client_credential_form_requested and persisted pending_input; this file is
// the missing consumer.
//
// Parity: framework/server/routes_credentials.py::handle_credential_form

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

// credentialFormPrimaryKeyHints is the field order tried when the submitted
// form did not include "api_key": the store keeps every field under its real
// name, but api_key is the default key tools and the env sync resolve.
//
// Parity: framework/server/routes_credentials.py::_PRIMARY_KEY_HINTS
var credentialFormPrimaryKeyHints = []string{"api_key", "token", "access_token", "password", "secret", "value", "key"}

// handleCredentialForm answers the wait a credential_form park recorded. Body:
// {correlation_id?, status, credential_id, account, keys}. On "saved" the keys
// are written to the encrypted store (HIVE_CREDENTIAL_KEY) and the queen is
// resumed with a non-secret confirmation; on "cancelled" the wait ends with a
// cancel note. Secret values never appear in the response or in a log line.
func (rt *CredentialRoutes) handleCredentialForm(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	var session *Session
	if rt.deps.Manager != nil {
		session = rt.deps.Manager.GetSession(sessionID)
	}
	if session == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Session '" + sessionID + "' not found"})
		return
	}
	runtime := session.Chat
	if runtime == nil {
		// Without a live queen there is no pending_input to consume and no
		// loop to resume; reporting success would strand the credential.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Queen not available"})
		return
	}

	value, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, ok := value.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	status := strings.ToLower(strings.TrimSpace(stringValue(body["status"])))
	if status == "" {
		status = "saved"
	}
	credentialID := strings.TrimSpace(stringValue(body["credential_id"]))
	account := strings.TrimSpace(stringValue(body["account"]))
	if account == "" {
		account = "default"
	}

	if status == "cancelled" {
		// End the wait before the resume turn so a crash in between cannot
		// re-enter the cancelled form.
		runtime.ClearCredentialFormPark()
		label := credentialID
		if label == "" {
			label = "the requested credential"
		}
		resumed := runtime.InjectEvent(
			"The user cancelled the credential form for '"+label+"'.",
			true,
		) == nil
		writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "resumed": resumed})
		return
	}

	pending := runtime.PendingCredentialForm()
	if pending == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "No credential form is pending for this session"})
		return
	}
	pendingID := strings.TrimSpace(stringValue(pending["credential_id"]))
	if credentialID == "" {
		credentialID = pendingID
	}
	if pendingID != "" && credentialID != pendingID {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "credential_id does not match the pending credential form",
		})
		return
	}
	keys := credentialFormKeys(body["keys"])
	if credentialID == "" || len(keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "credential_id and non-empty keys are required"})
		return
	}

	store, err := rt.credentialStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	credential := credentials.NewCredentialObject(credentialID)
	for _, name := range sortedCredentialKeyNames(keys) {
		credential.Keys.Set(credentials.NewCredentialKey(name, keys[name]))
	}
	if _, present := keys["api_key"]; !present {
		// Upstream stores the primary value under api_key as well, so the
		// credential has a resolvable default even when the form named a
		// different field (token, password, ...).
		credential.Keys.Set(credentials.NewCredentialKey("api_key", credentialPrimaryValue(keys)))
	}
	if err := store.SaveCredential(credential); err != nil {
		if errorsIsReadOnlyCredentialStore(err) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error":  "credentials_store_locked",
				"detail": "Credential store is read-only (HIVE_CREDENTIAL_KEY missing). Restart hive serve to trigger the self-heal recovery.",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	rt.publishCredential(true, credentialID)
	// Integration tools read os.Getenv; export the saved key before the resumed
	// turn retries the tool that reported it missing.
	rt.syncEnv()
	// Consume the park: drop the in-memory form and the persisted pending_input
	// marker, then queue the non-secret confirmation as the resume turn.
	runtime.ClearCredentialFormPark()
	confirmation := "✅ Credential '" + credentialID + "' (account '" + account + "') was saved securely. " +
		"Fields provided: " + strings.Join(sortedCredentialKeyNames(keys), ", ") + ". " +
		"It is now available — use it by passing account=\"" + account + "\" to the relevant tool. " +
		"(The secret values are stored encrypted and are not shown here.)"
	resumed := runtime.InjectEvent(confirmation, true) == nil
	writeJSON(w, http.StatusCreated, map[string]any{
		"saved":   credentialID + "/" + account,
		"resumed": resumed,
	})
}

// credentialFormKeys keeps the non-empty string fields a submitted form
// carries. Values are never logged; this map exists only to be handed to the
// encrypted store.
func credentialFormKeys(raw any) map[string]string {
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	keys := make(map[string]string, len(fields))
	for name, value := range fields {
		name = strings.TrimSpace(name)
		text := strings.TrimSpace(stringValue(value))
		if name == "" || text == "" {
			continue
		}
		keys[name] = text
	}
	return keys
}

// credentialPrimaryValue is the value the credential's default key receives
// when the form did not name api_key: the first hinted field present, else the
// first field in sorted order (deterministic across map iteration).
func credentialPrimaryValue(keys map[string]string) string {
	for _, hint := range credentialFormPrimaryKeyHints {
		if value := keys[hint]; value != "" {
			return value
		}
	}
	names := sortedCredentialKeyNames(keys)
	if len(names) == 0 {
		return ""
	}
	return keys[names[0]]
}

func sortedCredentialKeyNames(keys map[string]string) []string {
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// errorsIsReadOnlyCredentialStore centralises the sentinel check so the route
// does not need to import errors at every call site.
func errorsIsReadOnlyCredentialStore(err error) bool {
	return errors.Is(err, credentials.ErrEnvStorageReadOnlySave)
}

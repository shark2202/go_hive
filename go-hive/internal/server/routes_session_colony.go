package server

// Parity: framework/server/routes_sessions.py::handle_load_colony
// Parity: framework/server/routes_sessions.py::handle_unload_colony
// Parity: framework/server/routes_sessions.py::handle_dismiss_colony_pivot

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

const colonyPivotDismissMessage = "[User dismissed the 'Create Colony' popup. The off-goal " +
	"work was NOT added to this colony's task list — adding it " +
	"silently would defeat the pivot prompt. Call ask_user " +
	"right now with a question like 'You dismissed the new " +
	"colony — should I do this work here in the current " +
	"colony, or drop it?' so the user decides explicitly. Do " +
	"NOT just resume work or add tasks without their explicit " +
	"answer.]"

// ColonyLifecycle is the seam between the HTTP contract and the colony
// orchestration runtime. LoadColony mirrors SessionManager.load_colony
// (including its ValueError-equivalent conflict failure); UnloadColony mirrors
// SessionManager.unload_colony and reports whether a colony was removed.
type ColonyLifecycle interface {
	LoadColony(sessionID, colonyID string, model *string) (*Session, error)
	UnloadColony(sessionID string) bool
}

// ColonyLoadConflictError maps a load precondition failure (Python ValueError,
// e.g. an unknown session) to 409 {"error": Message}.
type ColonyLoadConflictError struct{ Message string }

func (e *ColonyLoadConflictError) Error() string { return e.Message }

// ColonyCredentialRequiredError maps a CredentialError raised while loading a
// colony to the 424 credentials_required payload. Err, when set, is the
// underlying credential error (kept for errors.Is/As inspection).
type ColonyCredentialRequiredError struct {
	Message   string
	AgentPath string
	Required  []map[string]any
	Err       error
}

func (e *ColonyCredentialRequiredError) Error() string { return e.Message }

func (e *ColonyCredentialRequiredError) Unwrap() error { return e.Err }

func handleLoadColony(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")

	decoded, err := decodeJSONValue(r)
	if err != nil {
		// Upstream's bare `await request.json()` lets a malformed body reach
		// the error middleware as 500; only an over-limit body is 413.
		writeJSONBodyFailure(w, err, http.StatusInternalServerError, internalServerError)
		return
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	colonyID, ok := body["colony_id"].(string)
	if !ok || colonyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "colony_id is required"})
		return
	}
	if validateColonyIDSegment(colonyID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid colony_id"})
		return
	}

	metadataPath := filepath.Join(deps.Home.ColoniesDir(), colonyID, "metadata.json")
	if _, err := os.Stat(metadataPath); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Colony not found: " + colonyID})
		return
	}

	var model *string
	if value, present := body["model"]; present {
		if text, ok := value.(string); ok {
			model = &text
		}
	}

	if deps.Colony == nil {
		log.Printf("[load_colony] no colony runtime configured")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	session, err := deps.Colony.LoadColony(sessionID, colonyID, model)
	if err != nil {
		var conflict *ColonyLoadConflictError
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": conflict.Message})
			return
		}
		if message, required, isCredential := credentialLoadFailure(err); isCredential {
			writeJSON(w, http.StatusFailedDependency, map[string]any{
				"error":      "credentials_required",
				"message":    message,
				"agent_path": colonyID,
				"required":   required,
			})
			return
		}
		log.Printf("[load_colony] failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if session == nil {
		log.Printf("[load_colony] runtime returned no session for %q", colonyID)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	writeJSON(w, http.StatusOK, sessionToLiveDict(session))
}

func credentialLoadFailure(err error) (string, []map[string]any, bool) {
	var structured *ColonyCredentialRequiredError
	if errors.As(err, &structured) {
		required := structured.Required
		if required == nil {
			required = []map[string]any{}
		}
		return structured.Message, required, true
	}
	if isCredentialError(err) {
		return err.Error(), []map[string]any{}, true
	}
	return "", nil, false
}

func isCredentialError(err error) bool {
	var base *credentials.CredentialError
	if errors.As(err, &base) {
		return true
	}
	var notFound *credentials.CredentialNotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var keyNotFound *credentials.CredentialKeyNotFoundError
	if errors.As(err, &keyNotFound) {
		return true
	}
	var refresh *credentials.CredentialRefreshError
	if errors.As(err, &refresh) {
		return true
	}
	var validation *credentials.CredentialValidationError
	if errors.As(err, &validation) {
		return true
	}
	var decryption *credentials.CredentialDecryptionError
	if errors.As(err, &decryption) {
		return true
	}
	var expired *credentials.CredentialExpiredError
	if errors.As(err, &expired) {
		return true
	}
	return false
}

// handleDeleteSessionColony serves the explicit DELETE /api/sessions/{session_id}/colony
// pattern. Python wins the /api/sessions/history/colony overlap for
// handle_delete_history_session because it registers that route first
// (core/framework/server/routes_sessions.py:3207) and aiohttp matches in
// registration order; ServeMux instead ranks the more specific colony pattern
// above the generic alias. Delegating here restores the Python winner without
// re-registering the conflicting history pattern.
func handleDeleteSessionColony(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	if r.PathValue("session_id") == "history" {
		// The history pattern's own session_id is the trailing segment.
		r.SetPathValue("session_id", "colony")
		handleDeleteHistorySession(w, r, deps)
		return
	}
	handleUnloadColony(w, r, deps)
}

func handleUnloadColony(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")

	if deps.Colony == nil {
		log.Printf("[unload_colony] no colony runtime configured")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	if deps.Colony.UnloadColony(sessionID) {
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id":      sessionID,
			"colony_unloaded": true,
		})
		return
	}
	if lookupSession(deps, sessionID) == nil {
		sessionNotFound(w, sessionID)
		return
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": "No colony loaded in this session"})
}

func handleDismissColonyPivot(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	if !session.HasPendingColonyPivot() {
		writeJSON(w, http.StatusOK, map[string]any{
			"dismissed": false,
			"reason":    "no_pending_pivot",
		})
		return
	}

	queen := session.QueenExecutor.QueenNode()
	if queen != nil {
		if err := queen.InjectEvent(colonyPivotDismissMessage, true); err != nil {
			log.Printf("[dismiss_colony_pivot] inject_event failed: %v", err)
		}
	}

	session.ClearPendingColonyPivot()
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": true})
}

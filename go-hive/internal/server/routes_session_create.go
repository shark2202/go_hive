package server

// Parity: framework/server/routes_sessions.py::handle_create_session

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
	"github.com/shark2202/go_hive/go-hive/internal/session"
)

var validInitialPhases = []string{"colony", "independent"}

var legacyPhaseAliases = map[string]string{
	"working":    "colony",
	"reviewing":  "colony",
	"incubating": "independent",
}

var colonySlugPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// invalidForkSlug mirrors the slug check in _create_colony_from_source:
// (colony_id or "").strip() must match ^[a-z0-9_]+$.
func invalidForkSlug(colonyID string) bool {
	return !colonySlugPattern.MatchString(strings.TrimSpace(colonyID))
}

// writeForkSlugError emits the _ColonyForkError body upstream raises (400).
func writeForkSlugError(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": "colony_id must be lowercase alphanumeric with underscores",
	})
}

// CreateSessionParams mirrors the keyword arguments handle_create_session
// forwards to SessionManager.create_session. Nil means Python's None.
type CreateSessionParams struct {
	ColonyID        *string
	SessionID       *string
	Model           *string
	InitialPrompt   *string
	QueenResumeFrom *string
	QueenName       *string
	InitialPhase    *string
}

// CreateColonyFromSourceParams mirrors the subset of create-session fields the
// fork-into-new-colony path threads into _create_colony_from_source.
type CreateColonyFromSourceParams struct {
	SourceSessionID string
	ColonyID        string
	QueenName       *string
	Model           *string
	InitialPhase    *string
	InitialPrompt   *string
}

// SessionCreateValueError carries a ValueError raised by create_session; the
// route maps "currently loading" to a 409 with the loading marker and every
// other message to a plain 409.
type SessionCreateValueError struct{ Message string }

func (e *SessionCreateValueError) Error() string { return e.Message }

// SessionCreateColonyNotFoundError carries the FileNotFoundError raised when
// colony_id names a colony that is not on disk; the route maps it to 404.
type SessionCreateColonyNotFoundError struct{ Err error }

func (e *SessionCreateColonyNotFoundError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "colony not found"
}

func (e *SessionCreateColonyNotFoundError) Unwrap() error { return e.Err }

// ColonyForkError mirrors _ColonyForkError: a validation/resolution failure in
// the fork path that carries its own HTTP status (default 400).
type ColonyForkError struct {
	Message string
	Status  int
}

func (e *ColonyForkError) Error() string { return e.Message }

func normalizeLegacyPhase(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	if alias, ok := legacyPhaseAliases[text]; ok {
		return alias
	}
	return value
}

func bodyString(body map[string]any, key string) *string {
	text, ok := body[key].(string)
	if !ok {
		return nil
	}
	return &text
}

func handleCreateSession(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	body := map[string]any{}
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		decoded, err := decodeJSONValue(r)
		if err != nil {
			writeJSONBodyFailure(w, err, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		object, ok := decoded.(map[string]any)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Request body must be a JSON object"})
			return
		}
		body = object
	}

	colonyID := bodyString(body, "colony_id")
	sessionID := bodyString(body, "session_id")
	model := bodyString(body, "model")
	initialPrompt := bodyString(body, "initial_prompt")
	queenResumeFrom := bodyString(body, "queen_resume_from")
	queenName := bodyString(body, "queen_name")
	sourceSessionID := bodyString(body, "source_session_id")

	// Custom session ids become one path segment; separators and dot segments
	// could root a runtime (and its transcript) outside the session tree.
	if sessionID != nil && !session.ValidSegment(strings.TrimSpace(*sessionID)) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid session_id"})
		return
	}
	if queenResumeFrom != nil && !session.ValidSegment(strings.TrimSpace(*queenResumeFrom)) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid queen_resume_from"})
		return
	}
	if sourceSessionID != nil && !session.ValidSegment(strings.TrimSpace(*sourceSessionID)) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid source_session_id"})
		return
	}

	// Python's normalize_legacy_phase does a dict.get(value, value); a list or
	// dict is unhashable and raises TypeError, which the error middleware turns
	// into a plain 500.
	switch body["initial_phase"].(type) {
	case []any, map[string]any:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	initialPhaseAny := normalizeLegacyPhase(body["initial_phase"])
	var initialPhase *string
	if initialPhaseAny != nil {
		text, ok := initialPhaseAny.(string)
		if !ok || (text != "colony" && text != "independent") {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": fmt.Sprintf("Invalid initial_phase '%s'", pyStr(initialPhaseAny)),
				"valid": validInitialPhases,
			})
			return
		}
		initialPhase = &text
	}

	// A truthy non-string queen_name reaches Path / value → TypeError → 500;
	// falsy non-strings are skipped by upstream's `if queen_name:`.
	if queenName == nil && pyTruthy(body["queen_name"]) {
		queen.EnsureDefaultQueens()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	// A create bound to an existing colony inherits that colony's queen.
	resolvedQueen, err := resolveColonyQueenForCreate(deps.Home, colonyID, sourceSessionID, queenName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to resolve colony queen: " + err.Error()})
		return
	}
	queenName = resolvedQueen

	if queenName != nil && *queenName != "" {
		queen.EnsureDefaultQueens()
		if _, err := queen.LoadQueenProfile(deps.Home.QueensDir(), *queenName); err != nil {
			var notFound *queen.ProfileNotFoundError
			if errors.As(err, &notFound) {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": "Queen '" + *queenName + "' not found",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
	}

	colonyValue := ""
	if colonyID != nil {
		colonyValue = *colonyID
	}
	sourceValue := ""
	if sourceSessionID != nil {
		sourceValue = *sourceSessionID
	}

	// Frontend "Create Colony" fork: a soft-deleted colony squatting on the
	// name is parked aside; a live one forces a deduplicated slug.
	if colonyValue != "" && sourceValue != "" {
		// Upstream validates this slug after vacate/dedupe; reject a
		// path-unsafe slug before it reaches the filesystem.
		if invalidForkSlug(colonyValue) {
			writeForkSlugError(w)
			return
		}
		if deps.VacateSoftDeleted != nil {
			if err := deps.VacateSoftDeleted(colonyValue); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": fmt.Sprintf("failed to clear soft-deleted colony '%s': %v", colonyValue, err),
				})
				return
			}
		}
		if colonyMetadataExists(deps.Home, colonyValue) {
			colonyValue = deduplicateColonyID(deps.Home, colonyValue)
		}
		// Upstream re-checks the deduplicated value (cn = colony_id.strip()).
		if invalidForkSlug(colonyValue) {
			writeForkSlugError(w)
			return
		}

		if deps.CreateColonyFromSource == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		// Python creates with the trimmed slug (cn = colony_id.strip()); the
		// hook sees the name the colony actually lands under.
		session, err := deps.CreateColonyFromSource(CreateColonyFromSourceParams{
			SourceSessionID: sourceValue,
			ColonyID:        strings.TrimSpace(colonyValue),
			QueenName:       queenName,
			Model:           model,
			InitialPhase:    initialPhase,
			InitialPrompt:   initialPrompt,
		})
		if err != nil {
			var forkErr *ColonyForkError
			if errors.As(err, &forkErr) {
				status := forkErr.Status
				if status == 0 {
					status = http.StatusBadRequest
				}
				writeJSON(w, status, map[string]any{"error": forkErr.Message})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		if session == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		writeJSON(w, http.StatusCreated, sessionToLiveDict(session))
		return
	}

	// Go-only guard: upstream forwards colony_id untouched and it becomes a
	// directory segment; refuse separators/dot segments, keep upstream naming.
	if colonyID != nil && *colonyID != "" && !session.ValidSegment(*colonyID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid colony_id"})
		return
	}

	if deps.CreateSession == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	session, err := deps.CreateSession(CreateSessionParams{
		ColonyID:        colonyID,
		SessionID:       sessionID,
		Model:           model,
		InitialPrompt:   initialPrompt,
		QueenResumeFrom: queenResumeFrom,
		QueenName:       queenName,
		InitialPhase:    initialPhase,
	})
	if err != nil {
		var valueErr *SessionCreateValueError
		if errors.As(err, &valueErr) {
			if strings.Contains(valueErr.Message, "currently loading") {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":     valueErr.Message,
					"colony_id": colonyValue,
					"loading":   true,
				})
				return
			}
			writeJSON(w, http.StatusConflict, map[string]any{"error": valueErr.Message})
			return
		}
		var notFound *SessionCreateColonyNotFoundError
		if errors.As(err, &notFound) {
			label := colonyValue
			if label == "" {
				label = "no colony_id"
			}
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error": "Colony not found: " + label,
			})
			return
		}
		if message, required, isCredential := credentialLoadFailure(err); isCredential {
			writeJSON(w, http.StatusFailedDependency, map[string]any{
				"error":      "credentials_required",
				"message":    message,
				"agent_path": colonyValue,
				"required":   required,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if session == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusCreated, sessionToLiveDict(session))
}

// Parity: framework/server/session_manager.py::_deduplicate_colony_id
func deduplicateColonyID(home interface{ ColoniesDir() string }, colonyID string) string {
	if !colonyMetadataExists(home, colonyID) {
		return colonyID
	}
	base := colonyID
	if match := regexp.MustCompile(`^(.+?)_(\d+)$`).FindStringSubmatch(colonyID); match != nil {
		base = match[1]
	}
	n := 2
	for colonyMetadataExists(home, fmt.Sprintf("%s_%d", base, n)) {
		n++
	}
	return fmt.Sprintf("%s_%d", base, n)
}

func colonyMetadataExists(home interface{ ColoniesDir() string }, colonyID string) bool {
	if home == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(home.ColoniesDir(), colonyID, "metadata.json"))
	return err == nil
}

// colonyMetadataQueenName returns the queen a colony records in its
// metadata.json, or "" when the colony does not exist yet (the fresh-colony
// bootstrap path owns that case) or records no queen. A malformed metadata
// document is an error: silently binding the default queen would detach the
// session from the colony's history.
func colonyMetadataQueenName(home hivehome.Home, colonyID string) (string, error) {
	colonyID = strings.TrimSpace(colonyID)
	if colonyID == "" || !session.ValidSegment(colonyID) {
		return "", nil
	}
	metadataPath := filepath.Join(home.ColoniesDir(), colonyID, "metadata.json")
	metadata, err := readJSONObject(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", metadataPath, err)
	}
	return strings.TrimSpace(pythonString(metadata["queen_name"])), nil
}

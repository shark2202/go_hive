package server

// Parity: framework/server/routes_sessions.py::handle_session_history
// Parity: framework/server/routes_sessions.py::handle_list_colony_sessions
// Parity: framework/server/routes_sessions.py::handle_get_active_colony_session
// Parity: framework/server/routes_sessions.py::handle_delete_history_session
// Parity: framework/server/routes_sessions.py::handle_set_session_title
// Parity: framework/server/routes_sessions.py::handle_reveal_session_folder

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/platform"
	"github.com/shark2202/go_hive/go-hive/internal/session"
)

// Parity: framework/server/routes_sessions.py::_validate_colony_id_segment
func validateColonyIDSegment(raw string) string {
	if raw == "" {
		return ""
	}
	if raw == "." || raw == ".." {
		return ""
	}
	if strings.ContainsAny(raw, "/\\\x00") {
		return ""
	}
	return raw
}

func liveSessionIndex(deps SessionRouteDeps) map[string]*Session {
	index := map[string]*Session{}
	for _, live := range liveSessions(deps) {
		index[live.ID] = live
	}
	return index
}

func applyLiveOverlay(entries []map[string]any, index map[string]*Session) {
	for _, entry := range entries {
		sid, _ := entry["session_id"].(string)
		live, ok := index[sid]
		if !ok {
			continue
		}
		entry["cold"] = false
		entry["live"] = true
		if !pyTruthy(entry["agent_name"]) && live.ColonyID != "" {
			entry["agent_name"] = live.ColonyID
		}
		if !pyTruthy(entry["agent_path"]) && live.WorkerPath != "" {
			entry["agent_path"] = live.WorkerPath
		}
	}
}

func handleSessionHistory(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	var entries []map[string]any
	if err := withHistoryReadSlot(func() {
		entries = session.ListColdSessions(deps.Home.QueensDir())
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	applyLiveOverlay(entries, liveSessionIndex(deps))
	writeJSON(w, http.StatusOK, map[string]any{"sessions": entries})
}

func handleListColonySessions(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	colonyID := validateColonyIDSegment(r.PathValue("colony_id"))
	if colonyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid colony_id"})
		return
	}
	var entries []map[string]any
	if err := withHistoryReadSlot(func() {
		entries = session.ListColonySessions(deps.Home.ColonyQueensDir(colonyID))
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	applyLiveOverlay(entries, liveSessionIndex(deps))
	writeJSON(w, http.StatusOK, map[string]any{"sessions": entries})
}

func handleActiveColonySession(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	colonyID := validateColonyIDSegment(r.PathValue("colony_id"))
	if colonyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid colony_id"})
		return
	}
	var entry any
	if active := session.ColonyActiveSession(deps.Home.ColonyQueensDir(colonyID)); active != nil {
		entry = active
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": entry})
}

func handleDeleteHistorySession(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	if !validSessionDirID(sessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid session id"})
		return
	}

	if deps.Manager != nil && deps.Manager.GetSession(sessionID) != nil {
		deps.Manager.StopSession(sessionID)
	}

	queenSessionDir, err := FindQueenSessionDir(deps.Home, sessionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid session id"})
		return
	}
	if queenSessionDir == "" {
		// No queen folder: target the default location, but only after the
		// segment check above made the join safe.
		queenSessionDir = filepath.Join(deps.Home.QueensDir(), "default", "sessions", sessionID)
	}
	if info, statErr := os.Stat(queenSessionDir); statErr == nil && info.IsDir() {
		if linkInfo, linkErr := os.Lstat(queenSessionDir); linkErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to delete session: Cannot call rmtree on a symbolic link",
			})
			return
		}
		if removeErr := os.RemoveAll(queenSessionDir); removeErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to delete session: " + removeErr.Error(),
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": sessionID})
}

func handleSetSessionTitle(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	sessionDir, ok := QueenDMSessionDir(deps.Home, sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such queen conversation"})
		return
	}

	body, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	object, isObject := body.(map[string]any)
	if !isObject {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title must be a string"})
		return
	}
	title, isString := object["title"].(string)
	if !isString {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title must be a string"})
		return
	}

	stored, err := WriteTitle(sessionDir, title, "user")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	var payload any
	if stored != "" {
		payload = stored
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "title": payload})
}

func handleRevealSession(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	if !validSessionDirID(sessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid session id"})
		return
	}

	var folder string
	var live *Session
	if deps.Manager != nil {
		live = deps.Manager.GetSession(sessionID)
	}
	if live != nil {
		storageID := live.ID
		if live.QueenResumeFrom != "" {
			storageID = live.QueenResumeFrom
		}
		if !validSessionDirID(storageID) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid session id"})
			return
		}
		queenName := live.QueenName
		if queenName == "" {
			queenName = "default"
		}
		folder = deps.Home.QueenSessionDir(queenName, storageID)
	} else {
		found, err := FindQueenSessionDir(deps.Home, sessionID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid session id"})
			return
		}
		if found == "" {
			// FindQueenSessionDir found no queen folder; create the default one.
			// sessionID is validated above, so this join cannot escape the tree.
			found = filepath.Join(deps.Home.QueensDir(), "default", "sessions", sessionID)
		}
		folder = found
	}

	if err := os.MkdirAll(folder, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	reveal := deps.Reveal
	if reveal == nil {
		reveal = platform.OpenPath
	}
	if err := reveal(folder); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": folder})
}

package server

// Parity: framework/server/routes_sessions.py::handle_fork_session

import (
	"net/http"
	"path/filepath"
	"time"

	"github.com/aden-hive/hive-go/internal/session"
)

func handleForkSession(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	src, ok := QueenDMSessionDir(deps.Home, sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such queen conversation"})
		return
	}

	var body any = map[string]any{}
	if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		decoded, err := decodeJSONValue(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
			return
		}
		body = decoded
	}
	var before any
	if object, isObject := body.(map[string]any); isObject {
		before = object["before_input_at"]
	}
	var beforeInputAt *string
	if before != nil {
		text, isString := before.(string)
		if !isString {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "before_input_at must be a timestamp string",
			})
			return
		}
		beforeInputAt = &text
	}

	newSessionID, err := session.NewStore("").GenerateID(time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	dst, err := session.ForkSessionDir(src, newSessionID, beforeInputAt)
	if err != nil {
		if forkErr, isFork := session.AsForkError(err); isFork {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": forkErr.Message})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id": filepath.Base(dst),
		"queen_id":   filepath.Base(filepath.Dir(filepath.Dir(src))),
	})
}

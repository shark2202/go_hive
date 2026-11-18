package server

// Parity: framework/server/routes_claude_code_queens.py::register_routes

import (
	"net/http"
	"strings"
)

// ClaudeCodeQueenView is one configured Claude Code queen, as the dashboard
// lists her. Auth is how she pays ("api_key" or "subscription"), never the key.
type ClaudeCodeQueenView struct {
	ID             string
	Name           string
	Title          string
	PermissionMode string
	Auth           string
	Model          string
}

// ClaudeCodeQueenRouteDeps wires the read-only dashboard views. When Enabled
// is false (or Queens is nil — no queen mail runtime) the list is empty, the
// same as `not config.enabled or mail is None`.
type ClaudeCodeQueenRouteDeps struct {
	Enabled       bool
	Queens        func() []ClaudeCodeQueenView
	Conversations func(queenID string) ([]map[string]any, error)
	// Transcript returns (entries, known); unknown conversations are the 404.
	Transcript func(queenID, sessionID string) ([]map[string]any, bool)
}

func (deps ClaudeCodeQueenRouteDeps) mail() bool {
	return deps.Enabled && deps.Queens != nil
}

// Parity: framework/server/routes_claude_code_queens.py::handle_list
func handleClaudeCodeQueenList(w http.ResponseWriter, r *http.Request, deps ClaudeCodeQueenRouteDeps) {
	if !deps.mail() {
		writeJSON(w, http.StatusOK, map[string]any{"queens": []any{}})
		return
	}
	queens := []map[string]any{}
	for _, entry := range deps.Queens() {
		var conversations []map[string]any
		if deps.Conversations != nil {
			loaded, err := deps.Conversations(entry.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
				return
			}
			conversations = loaded
		}
		if conversations == nil {
			conversations = []map[string]any{}
		}
		queens = append(queens, map[string]any{
			"id":              entry.ID,
			"name":            entry.Name,
			"title":           entry.Title,
			"permission_mode": entry.PermissionMode,
			"auth":            entry.Auth,
			"model":           entry.Model,
			"conversations":   conversations,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"queens": queens})
}

// Parity: framework/server/routes_claude_code_queens.py::handle_transcript
func handleClaudeCodeQueenTranscript(w http.ResponseWriter, r *http.Request, deps ClaudeCodeQueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	sessionID := r.PathValue("session_id")
	var entries []map[string]any
	known := false
	if deps.mail() && deps.Transcript != nil {
		entries, known = deps.Transcript(queenID, sessionID)
	}
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "No conversation " + pyRepr(sessionID) + " of " + pyRepr(queenID),
		})
		return
	}
	if entries == nil {
		entries = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// pyRepr renders a Python repr() for a string: single quotes unless the text
// holds a single quote and no double quote, with the usual escapes.
func pyRepr(value string) string {
	quote := byte('\'')
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var builder strings.Builder
	builder.WriteByte(quote)
	for _, char := range value {
		switch char {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case rune(quote):
			builder.WriteByte('\\')
			builder.WriteRune(char)
		default:
			builder.WriteRune(char)
		}
	}
	builder.WriteByte(quote)
	return builder.String()
}

// RegisterClaudeCodeQueenRoutes mounts the dashboard's read-only views of the
// queens run by Claude Code.
//
// Parity: framework/server/routes_claude_code_queens.py::register_routes
func RegisterClaudeCodeQueenRoutes(mux *http.ServeMux, deps ClaudeCodeQueenRouteDeps) {
	mux.HandleFunc("GET /api/claude-code-queens", func(w http.ResponseWriter, r *http.Request) {
		handleClaudeCodeQueenList(w, r, deps)
	})
	mux.HandleFunc("GET /api/claude-code-queens/{queen_id}/conversations/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		handleClaudeCodeQueenTranscript(w, r, deps)
	})
}

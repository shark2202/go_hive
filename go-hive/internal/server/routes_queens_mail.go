package server

// Parity: framework/server/queen_mail.py::register_routes

import (
	"net/http"
)

// QueenMailTokenHeader is the header a Claude Code queen's run authenticates
// with; the token is good for one conversation of hers.
const QueenMailTokenHeader = "X-Queen-Mail-Token"

// QueenMailError is a letter that can't be sent; the message is shown to the
// queen who tried.
type QueenMailError struct{ Message string }

func (e *QueenMailError) Error() string { return e.Message }

// QueenMailBackend is the server's queen mail, as the routes need it. The
// runtime (channel host, deliveries, depth accounting) lives behind this
// interface; the routes own the 401/404/400 contract.
type QueenMailBackend interface {
	// Authorizes reports whether token is good for conversation ref.
	Authorizes(ref, token string) bool
	// QueenOf names the queen behind a conversation ref.
	QueenOf(ref string) (string, bool)
	// Correspondents lists the queens that queen may write to.
	Correspondents(queenID string) []map[string]any
	// SendFromConversation hands a letter from a conversation to a queen.
	SendFromConversation(ref, to, message string) (map[string]any, error)
}

type QueenMailRouteDeps struct {
	QueenMail QueenMailBackend
}

func (deps QueenMailRouteDeps) mailFor(w http.ResponseWriter, r *http.Request, ref string) QueenMailBackend {
	if deps.QueenMail == nil || !deps.QueenMail.Authorizes(ref, r.Header.Get(QueenMailTokenHeader)) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return nil
	}
	return deps.QueenMail
}

// Parity: framework/server/queen_mail.py::handle_queens
func handleQueenMailQueens(w http.ResponseWriter, r *http.Request, deps QueenMailRouteDeps) {
	ref := r.URL.Query().Get("from")
	mail := deps.mailFor(w, r, ref)
	if mail == nil {
		return
	}
	queenID, ok := mail.QueenOf(ref)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "This conversation isn't known to Hive"})
		return
	}
	queens := mail.Correspondents(queenID)
	if queens == nil {
		queens = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queens": queens})
}

// Parity: framework/server/queen_mail.py::handle_letter
func handleQueenMailLetter(w http.ResponseWriter, r *http.Request, deps QueenMailRouteDeps) {
	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSONBodyFailure(w, err, http.StatusBadRequest, "The body isn't JSON")
		return
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "The body must be a JSON object"})
		return
	}
	ref := queenStr(body["from"])
	mail := deps.mailFor(w, r, ref)
	if mail == nil {
		return
	}
	sent, err := mail.SendFromConversation(ref, queenStr(body["to"]), queenStr(body["message"]))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if sent == nil {
		sent = map[string]any{}
	}
	writeJSON(w, http.StatusOK, sent)
}

// RegisterQueenMailRoutes mounts the routes a Claude Code queen's runs write
// through.
//
// Parity: framework/server/queen_mail.py::register_routes
func RegisterQueenMailRoutes(mux *http.ServeMux, deps QueenMailRouteDeps) {
	mux.HandleFunc("GET /api/queen-mail/queens", func(w http.ResponseWriter, r *http.Request) {
		handleQueenMailQueens(w, r, deps)
	})
	mux.HandleFunc("POST /api/queen-mail/letters", func(w http.ResponseWriter, r *http.Request) {
		handleQueenMailLetter(w, r, deps)
	})
}

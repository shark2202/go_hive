package httpapi_test

import (
	"net/http"
	"testing"
)

// TestEndpointGoldenShapes pins the remaining response shapes and the two error
// bodies the clients can surface (401 signed-out text, 400 gate text).
func TestEndpointGoldenShapes(t *testing.T) {
	e := newEnv(t)

	// 401 body — byte-for-byte contract copy.
	resp, raw := e.do(http.MethodPost, "/v1/global-db/query", map[string]any{"sql": "SELECT 1 AS one"}, "", "")
	goldenJSON(t, "globaldb-unauthorized", e.decodeJSON(resp, raw, http.StatusUnauthorized))

	// 400 gate error body.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{"sql": "PRAGMA table_info(leads)"})
	goldenJSON(t, "globaldb-gate-error", e.decodeJSON(resp, raw, http.StatusBadRequest))

	// 404 unknown table.
	resp, raw = e.authed(http.MethodGet, "/v1/global-db/tables/nope/rows", nil)
	goldenJSON(t, "globaldb-unknown-table", e.decodeJSON(resp, raw, http.StatusNotFound))

	// upsert (create-on-demand, merge shape).
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/upsert", map[string]any{
		"table": "leads",
		"row":   map[string]any{"id": "lead-1", "name": "Alice", "score": 5},
	})
	goldenJSON(t, "globaldb-upsert", e.decodeJSON(resp, raw, http.StatusOK))

	// import.
	resp, raw = e.authed(http.MethodPost, "/v1/global-db/import", map[string]any{
		"table": "leads",
		"rows": []map[string]any{
			{"id": "lead-2", "name": "Bob", "score": 7},
			{"id": "lead-3", "name": "Carol", "score": 9},
		},
	})
	goldenJSON(t, "globaldb-import", e.decodeJSON(resp, raw, http.StatusOK))

	// PATCH row.
	resp, raw = e.authed(http.MethodPatch, "/v1/global-db/tables/leads/rows", map[string]any{
		"pk":      map[string]any{"id": "lead-1"},
		"updates": map[string]any{"score": 6},
	})
	goldenJSON(t, "globaldb-patch", e.decodeJSON(resp, raw, http.StatusOK))

	// Inbox message id shape.
	resp, raw = e.do(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-golden",
		"session_id":        "s-golden",
		"colony_id":         "c-golden",
		"kind":              "blocker",
		"title":             "t",
		"body":              "b",
		"correlation_token": "ct-golden",
		"deep_link":         "hive://colony/c-golden",
	}, "Bearer", e.jwt)
	goldenJSON(t, "inbox-message", e.decodeJSON(resp, raw, http.StatusOK))

	// Inbox resolve shape.
	resp, raw = e.do(http.MethodPost, "/v1/inbox/resolve", map[string]any{
		"session_id":  "s-golden",
		"resolved_by": "u_test",
	}, "Bearer", e.jwt)
	goldenJSON(t, "inbox-resolve", e.decodeJSON(resp, raw, http.StatusOK))

	// Stream-token refresh shape.
	streamToken := e.mintStream(3600 * 1e9)
	resp, raw = e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": streamToken}, "", "")
	goldenJSON(t, "stream-token-refresh", e.decodeJSON(resp, raw, http.StatusOK))

	// Invalid stream token error body.
	resp, raw = e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": "garbage"}, "", "")
	goldenJSON(t, "stream-token-invalid", e.decodeJSON(resp, raw, http.StatusUnauthorized))
}

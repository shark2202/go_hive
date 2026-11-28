package httpapi_test

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/httpapi"
)

// This file is the M2 contract suite for the observed Inbox surface. It pins
// the parts observed-contract.json actually records:
//
//	POST /v1/inbox/messages   request fields, kind default/validation, {id}
//	POST /v1/inbox/resolve    {session_id, resolved_by} -> 2xx
//	GET  /v1/inbox/runtime/stream  runtime_id query, SSE event: reply
//
// Behaviour that only appears in prose (the desktop Inbox UI endpoints and
// GET /v1/inbox/stream) is deliberately not implemented; the surface test
// asserts those routes stay 404.

// sseStream is a line/frame reader for one live SSE response. A single
// bufio.Reader is kept per stream because it buffers ahead.
type sseStream struct {
	reader *bufio.Reader
}

func newSSEStream(body io.Reader) *sseStream {
	return &sseStream{reader: bufio.NewReaderSize(body, 64*1024)}
}

// nextLineWithin reads one line (without the trailing newline). ok is false on
// timeout or EOF.
func (s *sseStream) nextLineWithin(d time.Duration) (string, bool) {
	type result struct {
		line string
		ok   bool
	}
	ch := make(chan result, 1)
	go func() {
		line, err := s.reader.ReadString('\n')
		if err != nil && line == "" {
			ch <- result{ok: false}
			return
		}
		ch <- result{line: strings.TrimRight(line, "\r\n"), ok: true}
	}()
	select {
	case item := <-ch:
		return item.line, item.ok
	case <-time.After(d):
		return "", false
	}
}

// nextFrameWithin reads the next data-bearing frame, skipping comment
// (keepalive) and empty frames.
func (s *sseStream) nextFrameWithin(d time.Duration) (event, data string, ok bool) {
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", "", false
		}
		line, lineOK := s.nextLineWithin(remaining)
		if !lineOK {
			return "", "", false
		}
		switch {
		case line == "":
			if data != "" {
				return event, data, true
			}
			event = ""
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
}

// teamDB opens the test team's SQLite file read-only. It lets the contract
// tests assert what the handlers actually stored, not just the response body.
func (e *env) teamDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(e.dir, "teams", testTeam+".db")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("team db not created: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open team db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (e *env) auditActions(t *testing.T) map[string]int {
	t.Helper()
	db := e.teamDB(t)
	rows, err := db.Query(`SELECT action FROM _cloud_audit`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	actions := map[string]int{}
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		actions[action]++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit rows: %v", err)
	}
	return actions
}

// TestInboxObservedAuthSemantics pins the auth class of every observed inbox
// endpoint: operator JWT only; a streamToken is not an operator session; the
// 401 body is the client's verbatim signed-out sentence.
func TestInboxObservedAuthSemantics(t *testing.T) {
	e := newEnv(t)
	streamToken := e.mintStream(time.Hour)

	messageBody := map[string]any{"runtime_id": "rt-auth", "session_id": "s-auth", "kind": "blocker"}
	resolveBody := map[string]any{"session_id": "s-auth", "resolved_by": "u_test"}

	type probe struct {
		name   string
		method string
		target string
		body   any
	}
	probes := []probe{
		{"messages", http.MethodPost, "/v1/inbox/messages", messageBody},
		{"resolve", http.MethodPost, "/v1/inbox/resolve", resolveBody},
		{"runtime stream", http.MethodGet, "/v1/inbox/runtime/stream?runtime_id=rt-auth", nil},
	}
	for _, tc := range probes {
		t.Run(tc.name+"/missing", func(t *testing.T) {
			resp, raw := e.do(tc.method, tc.target, tc.body, "", "")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status %d want 401 (body %s)", resp.StatusCode, raw)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode 401: %v", err)
			}
			if body["error"] != httpapi.SignedOutMessage {
				t.Fatalf("401 error = %q, want %q", body["error"], httpapi.SignedOutMessage)
			}
		})
		t.Run(tc.name+"/stream-token-rejected", func(t *testing.T) {
			resp, raw := e.do(tc.method, tc.target, tc.body, "Bearer", streamToken)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("streamToken status %d want 401 (body %s)", resp.StatusCode, raw)
			}
			if !strings.Contains(string(raw), httpapi.SignedOutMessage) {
				t.Fatalf("401 body missing signed-out text: %s", raw)
			}
		})
	}

	// The operator JWT works on all three.
	resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", messageBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed message: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/resolve", resolveBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed resolve: %d %s", resp.StatusCode, raw)
	}
	stream := e.openStream(t, "rt-auth", "smoke")
	stream.Body.Close()
}

// TestInboxMessageStoredContract checks the request field set against what the
// handler persists. deep_link is nullable, kind defaults to blocker and
// user_id/team_id come from the JWT (clients never send them).
func TestInboxMessageStoredContract(t *testing.T) {
	e := newEnv(t)
	deepLink := "hive://colony/c-1/session/s-1"
	resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-stored",
		"session_id":        "s-stored",
		"colony_id":         "c-1",
		"kind":              "progress",
		"title":             "Halfway",
		"body":              "3 of 6 checks",
		"correlation_token": "corr-stored",
		"deep_link":         deepLink,
	})
	body := e.decodeJSON(resp, raw, http.StatusOK)
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("message id missing: %v", body)
	}

	// A second message omits kind: notifier.go defaults it to blocker.
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id": "rt-stored",
		"session_id": "s-stored",
	})
	defaulted := e.decodeJSON(resp, raw, http.StatusOK)
	defaultedID, _ := defaulted["id"].(string)

	db := e.teamDB(t)
	var (
		kind, title, text, token, status, userID, teamID, colony, runtime, session string
		deep                                                                       sql.NullString
	)
	err := db.QueryRow(`
SELECT kind, title, body, correlation_token, deep_link, status, user_id, team_id, colony_id, runtime_id, session_id
FROM inbox_messages WHERE id = ?`, id).Scan(
		&kind, &title, &text, &token, &deep, &status, &userID, &teamID, &colony, &runtime, &session)
	if err != nil {
		t.Fatalf("select message: %v", err)
	}
	if kind != "progress" || title != "Halfway" || text != "3 of 6 checks" ||
		token != "corr-stored" || status != "open" || userID != testUser || teamID != testTeam ||
		colony != "c-1" || runtime != "rt-stored" || session != "s-stored" {
		t.Fatalf("stored row = kind=%q title=%q body=%q token=%q status=%q user=%q team=%q colony=%q runtime=%q session=%q",
			kind, title, text, token, status, userID, teamID, colony, runtime, session)
	}
	if !deep.Valid || deep.String != deepLink {
		t.Fatalf("deep_link = %v, want %q", deep, deepLink)
	}

	var defaultKind string
	if err := db.QueryRow(`SELECT kind FROM inbox_messages WHERE id = ?`, defaultedID).Scan(&defaultKind); err != nil {
		t.Fatalf("select defaulted kind: %v", err)
	}
	if defaultKind != "blocker" {
		t.Fatalf("default kind = %q, want blocker", defaultKind)
	}

	// Nil deep_link stays NULL.
	var nullDeep sql.NullString
	if err := db.QueryRow(`SELECT deep_link FROM inbox_messages WHERE id = ?`, defaultedID).Scan(&nullDeep); err != nil {
		t.Fatalf("select nil deep_link: %v", err)
	}
	if nullDeep.Valid {
		t.Fatalf("deep_link = %q, want NULL", nullDeep.String)
	}

	// Error paths from the observed contract.
	cases := []struct {
		name   string
		body   any
		status int
		error  string
	}{
		{"invalid json", "{", http.StatusBadRequest, "invalid JSON body"},
		{"trailing json", `{"session_id":"s"}{"x":1}`, http.StatusBadRequest, "invalid JSON body"},
		{"invalid kind", map[string]any{"session_id": "s", "kind": "nope"}, http.StatusBadRequest, "invalid kind (want blocker|heartbeat|progress|done)"},
		{"missing session", map[string]any{"kind": "blocker"}, http.StatusBadRequest, "session_id is required"},
		{"blank session", map[string]any{"session_id": "   "}, http.StatusBadRequest, "session_id is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", tc.body)
			body := e.decodeJSON(resp, raw, tc.status)
			if body["error"] != tc.error {
				t.Fatalf("error = %v, want %q", body["error"], tc.error)
			}
		})
	}
}

// TestInboxResolveContract pins the resolve transition: every open message of
// the session closes once, later resolves report zero, and resolved_by /
// resolved_at are persisted.
func TestInboxResolveContract(t *testing.T) {
	e := newEnv(t)
	for _, token := range []string{"corr-r1", "corr-r2"} {
		resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
			"runtime_id":        "rt-resolve",
			"session_id":        "s-resolve",
			"kind":              "blocker",
			"correlation_token": token,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post message: %d %s", resp.StatusCode, raw)
		}
	}

	resp, raw := e.authed(http.MethodPost, "/v1/inbox/resolve", map[string]any{
		"session_id":  "s-resolve",
		"resolved_by": "u_test",
	})
	first := e.decodeJSON(resp, raw, http.StatusOK)
	if first["resolved"].(float64) != 2 {
		t.Fatalf("first resolve = %v, want 2", first["resolved"])
	}
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/resolve", map[string]any{
		"session_id":  "s-resolve",
		"resolved_by": "u_test",
	})
	second := e.decodeJSON(resp, raw, http.StatusOK)
	if second["resolved"].(float64) != 0 {
		t.Fatalf("second resolve = %v, want 0", second["resolved"])
	}

	db := e.teamDB(t)
	var status, resolvedBy string
	var resolvedAt sql.NullString
	if err := db.QueryRow(`SELECT status, resolved_by, resolved_at FROM inbox_messages WHERE correlation_token = ?`, "corr-r1").
		Scan(&status, &resolvedBy, &resolvedAt); err != nil {
		t.Fatalf("select resolved row: %v", err)
	}
	if status != "resolved" || resolvedBy != "u_test" || !resolvedAt.Valid || resolvedAt.String == "" {
		t.Fatalf("resolved row = status=%q by=%q at=%v", status, resolvedBy, resolvedAt)
	}

	// Error paths from the observed contract.
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/resolve", map[string]any{"session_id": " "})
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "session_id is required" {
		t.Fatalf("missing session error = %v", body["error"])
	}
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/resolve", "{")
	body = e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "invalid JSON body" {
		t.Fatalf("invalid json error = %v", body["error"])
	}
}

// TestInboxStreamWireContract pins the SSE wire shape: headers, the connected
// comment, keepalive comments and one reply frame with the observed fields.
func TestInboxStreamWireContract(t *testing.T) {
	e := newEnv(t)
	stream := e.openStream(t, "rt-wire", "smoke")
	defer stream.Body.Close()

	if ct := stream.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := stream.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("cache-control = %q", cc)
	}
	if xab := stream.Header.Get("X-Accel-Buffering"); xab != "no" {
		t.Fatalf("x-accel-buffering = %q", xab)
	}

	reader := newSSEStream(stream.Body)
	line, ok := reader.nextLineWithin(2 * time.Second)
	if !ok || line != ": connected" {
		t.Fatalf("first line = %q ok=%v, want ': connected'", line, ok)
	}
	// The connected comment is its own frame, so a blank separator follows.
	if line, ok = reader.nextLineWithin(2 * time.Second); !ok || line != "" {
		t.Fatalf("frame separator = %q ok=%v, want empty", line, ok)
	}
	line, ok = reader.nextLineWithin(2 * time.Second)
	if !ok || line != ": keepalive" {
		t.Fatalf("second line = %q ok=%v, want ': keepalive'", line, ok)
	}

	resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-wire",
		"session_id":        "s-wire",
		"colony_id":         "c-wire",
		"kind":              "done",
		"correlation_token": "corr-wire",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "corr-wire",
		"text":              "wire reply",
	})
	reply := e.decodeJSON(resp, raw, http.StatusOK)
	if reply["delivered"].(float64) != 1 {
		t.Fatalf("delivered = %v, want 1", reply["delivered"])
	}

	event, data, ok := reader.nextFrameWithin(2 * time.Second)
	if !ok {
		t.Fatal("no reply frame")
	}
	if event != "reply" {
		t.Fatalf("event = %q, want reply", event)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode data %q: %v", data, err)
	}
	if payload["correlation_token"] != "corr-wire" || payload["text"] != "wire reply" ||
		payload["colony_id"] != "c-wire" || payload["message_id"] == "" {
		t.Fatalf("reply payload = %v", payload)
	}
	// The wire line is exactly `event: reply\ndata: {...}` (no other fields).
	if count := strings.Count(data, "\n"); count != 0 {
		t.Fatalf("reply data spans %d lines: %q", count+1, data)
	}
}

// TestInboxStreamFanoutAndIsolation proves replies reach every subscriber of
// the target runtime_id and nobody else.
func TestInboxStreamFanoutAndIsolation(t *testing.T) {
	e := newEnv(t)
	first := e.openStream(t, "rt-fan", "a")
	defer first.Body.Close()
	second := e.openStream(t, "rt-fan", "b")
	defer second.Body.Close()
	other := e.openStream(t, "rt-other", "c")
	defer other.Body.Close()

	firstReader := newSSEStream(first.Body)
	secondReader := newSSEStream(second.Body)
	otherReader := newSSEStream(other.Body)
	for name, stream := range map[string]*sseStream{"first": firstReader, "second": secondReader, "other": otherReader} {
		if line, ok := stream.nextLineWithin(2 * time.Second); !ok || line != ": connected" {
			t.Fatalf("%s: first line = %q ok=%v", name, line, ok)
		}
	}

	resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-fan",
		"session_id":        "s-fan",
		"correlation_token": "corr-fan",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "corr-fan",
		"text":              "fanout",
	})
	reply := e.decodeJSON(resp, raw, http.StatusOK)
	if reply["delivered"].(float64) != 2 {
		t.Fatalf("delivered = %v, want 2", reply["delivered"])
	}

	for name, stream := range map[string]*sseStream{"first": firstReader, "second": secondReader} {
		event, data, ok := stream.nextFrameWithin(2 * time.Second)
		if !ok || event != "reply" || !strings.Contains(data, `"corr-fan"`) {
			t.Fatalf("%s: event=%q data=%q ok=%v", name, event, data, ok)
		}
	}
	if event, data, ok := otherReader.nextFrameWithin(300 * time.Millisecond); ok {
		t.Fatalf("other runtime received %q %q", event, data)
	}
}

// TestInboxStreamQueuedReplay proves a reply produced while the runtime was
// disconnected is replayed once on connect, and not again after delivery.
func TestInboxStreamQueuedReplay(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-replay",
		"session_id":        "s-replay",
		"colony_id":         "colony-replay",
		"correlation_token": "corr-replay",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "corr-replay",
		"text":              "replayed",
	})
	queued := e.decodeJSON(resp, raw, http.StatusOK)
	if queued["delivered"].(float64) != 0 {
		t.Fatalf("queued reply delivered = %v, want 0", queued["delivered"])
	}

	stream := e.openStream(t, "rt-replay", "smoke")
	reader := newSSEStream(stream.Body)
	if line, ok := reader.nextLineWithin(2 * time.Second); !ok || line != ": connected" {
		t.Fatalf("first line = %q ok=%v", line, ok)
	}
	event, data, ok := reader.nextFrameWithin(2 * time.Second)
	if !ok || event != "reply" || !strings.Contains(data, `"replayed"`) ||
		!strings.Contains(data, `"colony_id":"colony-replay"`) {
		t.Fatalf("replayed frame = %q %q ok=%v", event, data, ok)
	}
	stream.Body.Close()

	// Reconnect: the delivered reply must not replay.
	again := e.openStream(t, "rt-replay", "smoke")
	defer again.Body.Close()
	againReader := newSSEStream(again.Body)
	if line, ok := againReader.nextLineWithin(2 * time.Second); !ok || line != ": connected" {
		t.Fatalf("reconnect first line = %q ok=%v", line, ok)
	}
	if event, data, ok := againReader.nextFrameWithin(300 * time.Millisecond); ok {
		t.Fatalf("replayed twice: %q %q", event, data)
	}
}

// TestInboxStreamPresence documents that the kind query parameter is recorded
// as connection bookkeeping, not used as a reply filter (cloud filter
// semantics are UNKNOWN).
func TestInboxStreamPresence(t *testing.T) {
	e := newEnv(t)
	stream := e.openStream(t, "rt-presence", "desktop")
	reader := newSSEStream(stream.Body)
	if line, ok := reader.nextLineWithin(2 * time.Second); !ok || line != ": connected" {
		t.Fatalf("first line = %q ok=%v", line, ok)
	}
	defer stream.Body.Close()

	db := e.teamDB(t)
	var kind string
	if err := db.QueryRow(`SELECT kind FROM runtime_connections WHERE runtime_id = ?`, "rt-presence").Scan(&kind); err != nil {
		t.Fatalf("select runtime connection: %v", err)
	}
	if kind != "desktop" {
		t.Fatalf("kind = %q, want desktop", kind)
	}

	// A reply tagged with a different kind still reaches this connection.
	resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-presence",
		"session_id":        "s-presence",
		"kind":              "heartbeat",
		"correlation_token": "corr-presence",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "corr-presence",
		"text":              "no filter",
	})
	reply := e.decodeJSON(resp, raw, http.StatusOK)
	if reply["delivered"].(float64) != 1 {
		t.Fatalf("delivered = %v, want 1 (kind must not filter)", reply["delivered"])
	}
	if event, data, ok := reader.nextFrameWithin(2 * time.Second); !ok || event != "reply" || !strings.Contains(data, "no filter") {
		t.Fatalf("frame = %q %q ok=%v", event, data, ok)
	}
}

// TestInboxStreamValidation pins the only documented stream error body.
func TestInboxStreamValidationContract(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.authed(http.MethodGet, "/v1/inbox/runtime/stream", nil)
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "runtime_id is required" {
		t.Fatalf("error = %v", body["error"])
	}
}

// TestInboxDesktopStreamStaysUnknown pins INV-05 for the Inbox paths that
// appear only in prose: docs/hive-inbox.html §02 names the desktop UI
// endpoints, but no request/response shape is recoverable and none of them is
// in observed-contract.json. They must not be served with an empty 200.
func TestInboxDesktopStreamStaysUnknown(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.authed(http.MethodGet, "/v1/inbox/stream", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /v1/inbox/stream = %d %s, want 404", resp.StatusCode, raw)
	}

	// GET /v1/inbox/messages collides with the implemented POST route, so the
	// mux answers 405. That is an explicit method error, not a list endpoint.
	resp, raw = e.authed(http.MethodGet, "/v1/inbox/messages", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/inbox/messages = %d %s, want 405", resp.StatusCode, raw)
	}

	cases := []struct{ method, target string }{
		{http.MethodPost, "/v1/inbox/messages/m1/reply"},
		{http.MethodPost, "/v1/inbox/messages/m1/read"},
		{http.MethodPost, "/v1/inbox/messages/mark-all-read"},
		{http.MethodGet, "/v1/inbox/messages/unread-count"},
		{http.MethodPost, "/v1/inbox/runtime/heartbeat"},
	}
	for _, tc := range cases {
		resp, raw := e.authed(tc.method, tc.target, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s = %d %s, want 404", tc.method, tc.target, resp.StatusCode, raw)
		}
	}
}

// TestInboxAuditTrail proves the M2 audit requirement: every inbox write is
// recorded in _cloud_audit with the JWT identity.
func TestInboxAuditTrail(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-audit",
		"session_id":        "s-audit",
		"correlation_token": "corr-audit",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/resolve", map[string]any{"session_id": "s-audit", "resolved_by": "u_test"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: %d %s", resp.StatusCode, raw)
	}
	// A second message is needed because the first is resolved before reply.
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-audit",
		"session_id":        "s-audit-2",
		"correlation_token": "corr-audit-2",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message 2: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "corr-audit-2",
		"text":              "ok",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reply: %d %s", resp.StatusCode, raw)
	}

	actions := e.auditActions(t)
	for action, want := range map[string]int{"inbox_message": 2, "inbox_resolve": 1, "inbox_reply": 1} {
		if actions[action] != want {
			t.Fatalf("audit[%s] = %d, want %d (all: %v)", action, actions[action], want, actions)
		}
	}

	db := e.teamDB(t)
	var userID, teamID string
	if err := db.QueryRow(`SELECT user_id, team_id FROM _cloud_audit WHERE action = 'inbox_message' ORDER BY id LIMIT 1`).
		Scan(&userID, &teamID); err != nil {
		t.Fatalf("select audit identity: %v", err)
	}
	if userID != testUser || teamID != testTeam {
		t.Fatalf("audit identity = %s/%s, want %s/%s", userID, teamID, testUser, testTeam)
	}
}

// TestInboxRateLimitExtension covers the opt-in M2 limiter. It is a
// private-deploy control: disabled by default, per-team, fixed window, 429 with
// Retry-After when enabled.
func TestInboxRateLimitExtension(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, func(cfg *httpapi.Config) {
		cfg.InboxRateLimit = 2
		cfg.InboxRateWindow = time.Minute
		cfg.Now = func() time.Time { return now }
	})
	post := func(token string) (*http.Response, []byte) {
		return e.do(http.MethodPost, "/v1/inbox/messages", map[string]any{
			"runtime_id": "rt-limit",
			"session_id": "s-limit",
			"kind":       "heartbeat",
		}, "jwt", token)
	}
	for i := 0; i < 2; i++ {
		resp, raw := post(e.jwt)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("write %d: %d %s", i+1, resp.StatusCode, raw)
		}
	}
	resp, raw := post(e.jwt)
	limited := e.decodeJSON(resp, raw, http.StatusTooManyRequests)
	if limited["error"] != "inbox rate limit exceeded" {
		t.Fatalf("429 body = %v", limited)
	}
	if retry := resp.Header.Get("Retry-After"); retry != "60" {
		t.Fatalf("Retry-After = %q, want 60", retry)
	}

	// Resolve shares the same per-team bucket.
	resp, raw = e.authed(http.MethodPost, "/v1/inbox/resolve", map[string]any{"session_id": "s-limit"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resolve while limited = %d %s", resp.StatusCode, raw)
	}

	// Another team has its own bucket.
	otherJWT, err := e.signer.MintUserToken("u_other", "team_other", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw = post(otherJWT)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("other team: %d %s", resp.StatusCode, raw)
	}

	// The next window admits writes again.
	now = now.Add(61 * time.Second)
	resp, raw = post(e.jwt)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after window: %d %s", resp.StatusCode, raw)
	}

	// Default server: limiter disabled (no 429 for a burst).
	open := newEnv(t)
	for i := 0; i < 5; i++ {
		resp, raw := open.authed(http.MethodPost, "/v1/inbox/messages", map[string]any{
			"runtime_id": "rt-open",
			"session_id": "s-open",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("default limiter write %d: %d %s", i+1, resp.StatusCode, raw)
		}
	}
}

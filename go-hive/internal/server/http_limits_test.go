package server

// Parity-exempt: Go-only HTTP hardening; the body cap and malformed-JSON
// routing mirror aiohttp's client_max_size and each route's Python except
// clause, not an upstream test.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity-exempt: Go-only HTTP hardening; no upstream test pins the body cap.
func TestReadLimitedBodyRejectsOversizedJSON(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), int(maxJSONBodyBytes)+1)
	request := httptest.NewRequest("POST", "/api/anything", bytes.NewReader(payload))
	if _, err := readLimitedBody(request, maxJSONBodyBytes); err != errBodyTooLarge {
		t.Fatalf("oversized body error = %v, want errBodyTooLarge", err)
	}

	request = httptest.NewRequest("POST", "/api/anything", strings.NewReader(`{"ok":true}`))
	data, err := readLimitedBody(request, maxJSONBodyBytes)
	if err != nil {
		t.Fatalf("small body: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("small body = %q", data)
	}
}

// Parity-exempt: boundary guard for the 20 MiB cap copied from
// framework/server/app.py::create_app (client_max_size=20*1024*1024).
func TestReadLimitedBodyAcceptsExactlyTheCap(t *testing.T) {
	payload := bytes.Repeat([]byte(" "), int(maxJSONBodyBytes))
	request := httptest.NewRequest("POST", "/api/anything", bytes.NewReader(payload))
	data, err := readLimitedBody(request, maxJSONBodyBytes)
	if err != nil {
		t.Fatalf("body at the cap: %v", err)
	}
	if int64(len(data)) != maxJSONBodyBytes {
		t.Fatalf("body length = %d, want %d", len(data), maxJSONBodyBytes)
	}
}

func postRawJSON(t *testing.T, app *App, target string, body []byte) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded
}

// Parity: framework/server/routes_sessions.py::handle_create_session
// (except json.JSONDecodeError: malformed is 400; client_max_size is 413)
func TestCreateSessionOversizedJSONIs413(t *testing.T) {
	home, _ := securityTestHome(t)
	created := false
	mux := sessionRoutesMux(SessionRouteDeps{
		Home: home,
		CreateSession: func(CreateSessionParams) (*Session, error) {
			created = true
			return nil, nil
		},
	})
	payload := bytes.Repeat([]byte(" "), int(maxJSONBodyBytes)+1)
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(payload))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", recorder.Code, recorder.Body.String())
	}
	body := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["error"] != "Request body too large" {
		t.Fatalf("error = %#v, want Request body too large", body["error"])
	}
	if created {
		t.Fatal("CreateSession ran for an oversized body")
	}
}

// Parity: framework/server/routes_execution.py (bare request.json()),
// framework/server/routes_credentials.py::handle_save_credential
// (bare request.json()) and routes_sessions.py::handle_load_colony: malformed
// JSON is an uncaught JSONDecodeError -> error middleware -> 500 envelope.
func TestMalformedJSONIs500WhereUpstreamLeavesItUncaught(t *testing.T) {
	session := makeTestSession("session_20261125_100000_json0001")
	app := newAppWithSession(t, session)
	cases := []struct {
		name   string
		target string
	}{
		{"queen-context", "/api/sessions/" + session.ID + "/queen-context"},
		{"record-message", "/api/sessions/" + session.ID + "/record-message"},
		{"load-colony", "/api/sessions/" + session.ID + "/colony"},
		{"chat", "/api/sessions/" + session.ID + "/chat"},
		{"mark-colony-spawned", "/api/sessions/" + session.ID + "/mark-colony-spawned"},
		{"save-credential", "/api/credentials"},
		{"check-agent", "/api/credentials/check-agent"},
	}
	for _, testCase := range cases {
		status, body := postRawJSON(t, app, testCase.target, []byte(`{"message":`))
		if status != http.StatusInternalServerError {
			t.Fatalf("%s status = %d, want 500 (body %#v)", testCase.name, status, body)
		}
		if body["error"] != internalServerError {
			t.Fatalf("%s error = %#v, want %q", testCase.name, body["error"], internalServerError)
		}
	}
}

// Parity: framework/server/app.py::create_app (client_max_size=20 MiB) on the
// routes whose Python handlers leave request.json() uncaught.
func TestOversizedJSONIs413OnUncaughtRoutes(t *testing.T) {
	session := makeTestSession("session_20261125_100000_json0002")
	app := newAppWithSession(t, session)
	targets := []string{
		"/api/sessions/" + session.ID + "/queen-context",
		"/api/sessions/" + session.ID + "/chat",
		"/api/credentials",
	}
	for _, target := range targets {
		payload := bytes.Repeat([]byte(" "), int(maxJSONBodyBytes)+1)
		status, body := postRawJSON(t, app, target, payload)
		if status != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s status = %d, want 413 (body %#v)", target, status, body)
		}
		if body["error"] != "Request body too large" {
			t.Fatalf("%s error = %#v, want Request body too large", target, body["error"])
		}
	}
}

// Parity: framework/server/routes_mcp.py::handle_add_server
// (except Exception: both malformed JSON and an over-limit body answer the
// handler's own 400, not the generic 500/413 pair).
func TestCaughtJSONErrorsStay400(t *testing.T) {
	session := makeTestSession("session_20261125_100000_json0003")
	app := newAppWithSession(t, session)

	status, body := postRawJSON(t, app, "/api/mcp/servers", []byte(`{"name":`))
	if status != http.StatusBadRequest || body["error"] != "Invalid JSON body" {
		t.Fatalf("malformed caught body = %d %#v, want 400 Invalid JSON body", status, body)
	}

	payload := bytes.Repeat([]byte(" "), int(maxJSONBodyBytes)+1)
	status, body = postRawJSON(t, app, "/api/mcp/servers", payload)
	if status != http.StatusBadRequest || body["error"] != "Invalid JSON body" {
		t.Fatalf("oversized caught body = %d %#v, want 400 Invalid JSON body", status, body)
	}
}

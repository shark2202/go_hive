package server

// Parity: framework/server/tests/test_api.py::TestSessionCRUD
// File-level anchor: upstream exercises POST /api/sessions from TestSessionCRUD.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

func createSessionRequest(t *testing.T, deps SessionRouteDeps, body string) (int, map[string]any) {
	t.Helper()
	mux := sessionRoutesMux(deps)
	recorder, decoded := doSessionJSON(t, mux, http.MethodPost, "/api/sessions", body)
	return recorder.Code, decoded
}

func createSessionHome(t *testing.T) hivehome.Home {
	t.Helper()
	return hivehome.Home{Root: t.TempDir()}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_create_session_with_colony_forwards_session_id
func TestCreateSessionWithColonyForwardsSessionID(t *testing.T) {
	var captured CreateSessionParams
	deps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			captured = params
			return &Session{ID: "my-custom-session", ColonyID: "example_agent", LoadedAt: 1000000.0}, nil
		},
	}
	status, data := createSessionRequest(t, deps, `{"session_id":"my-custom-session","colony_id":"example_agent"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if data["session_id"] != "my-custom-session" {
		t.Fatalf("session_id = %#v", data["session_id"])
	}
	if captured.ColonyID == nil || *captured.ColonyID != "example_agent" {
		t.Fatalf("colony_id = %#v, want example_agent", captured.ColonyID)
	}
	if captured.SessionID == nil || *captured.SessionID != "my-custom-session" {
		t.Fatalf("session_id = %#v, want my-custom-session", captured.SessionID)
	}
	if captured.Model != nil || captured.InitialPrompt != nil || captured.QueenResumeFrom != nil || captured.QueenName != nil || captured.InitialPhase != nil {
		t.Fatalf("unexpected forwarded params: %+v", captured)
	}
}

// Parity-exempt: Go glue covering the branch where request.can_read_body is
// false (Python feeds body={} and forwards every field as None).
func TestCreateSessionWithoutBodyForwardsNils(t *testing.T) {
	called := false
	deps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			called = true
			if params.ColonyID != nil || params.SessionID != nil || params.Model != nil || params.InitialPrompt != nil || params.QueenResumeFrom != nil || params.QueenName != nil || params.InitialPhase != nil {
				t.Fatalf("params = %+v, want all nil", params)
			}
			return &Session{ID: "s1", LoadedAt: 1000000.0}, nil
		},
	}
	status, _ := createSessionRequest(t, deps, "")
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if !called {
		t.Fatalf("CreateSession was not called")
	}
}

// Parity-exempt: Go glue for the invalid-body branches of handle_create_session
// (Python raises json.JSONDecodeError / rejects non-dicts identically).
func TestCreateSessionRejectsInvalidBodies(t *testing.T) {
	deps := SessionRouteDeps{Home: createSessionHome(t)}
	cases := []struct {
		body    string
		message string
	}{
		{`{not json`, "Invalid JSON body"},
		{`[1,2]`, "Request body must be a JSON object"},
		{`"text"`, "Request body must be a JSON object"},
		{`null`, "Request body must be a JSON object"},
	}
	for _, tc := range cases {
		status, data := createSessionRequest(t, deps, tc.body)
		if status != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", tc.body, status)
		}
		if data["error"] != tc.message {
			t.Fatalf("body %q: error = %#v, want %q", tc.body, data["error"], tc.message)
		}
	}
}

// Parity-exempt: Go glue for the initial_phase validation branch.
func TestCreateSessionRejectsInvalidInitialPhase(t *testing.T) {
	deps := SessionRouteDeps{Home: createSessionHome(t)}
	status, data := createSessionRequest(t, deps, `{"initial_phase":"bogus"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if data["error"] != "Invalid initial_phase 'bogus'" {
		t.Fatalf("error = %#v", data["error"])
	}
	valid, _ := data["valid"].([]any)
	if len(valid) != 2 || valid[0] != "colony" || valid[1] != "independent" {
		t.Fatalf("valid = %#v", data["valid"])
	}
}

// Parity-exempt: Go glue for the f-string rendering of a non-string phase
// (Python renders a bool as "True", not Go's "true").
func TestCreateSessionInvalidPhaseUsesPythonStr(t *testing.T) {
	deps := SessionRouteDeps{Home: createSessionHome(t)}
	status, data := createSessionRequest(t, deps, `{"initial_phase":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if data["error"] != "Invalid initial_phase 'True'" {
		t.Fatalf("error = %#v", data["error"])
	}
}

// Parity-exempt: Go glue for Python's unhashable-phase TypeError, which the
// error middleware turns into a plain 500.
func TestCreateSessionUnhashablePhaseIsInternalError(t *testing.T) {
	deps := SessionRouteDeps{Home: createSessionHome(t)}
	for _, body := range []string{`{"initial_phase":[]}`, `{"initial_phase":{}}`} {
		status, data := createSessionRequest(t, deps, body)
		if status != http.StatusInternalServerError {
			t.Fatalf("%s: status = %d, want 500", body, status)
		}
		if data["error"] != internalServerError {
			t.Fatalf("%s: error = %#v", body, data["error"])
		}
	}
}

// Parity-exempt: Go glue for a truthy non-string queen_name, where Python's
// Path / value raises TypeError → 500 (a falsy one is skipped by `if
// queen_name:` and still creates the session).
func TestCreateSessionQueenNameTypeHandling(t *testing.T) {
	for _, body := range []string{`{"queen_name":5}`, `{"queen_name":true}`, `{"queen_name":["a"]}`} {
		status, data := createSessionRequest(t, SessionRouteDeps{Home: createSessionHome(t)}, body)
		if status != http.StatusInternalServerError {
			t.Fatalf("%s: status = %d, want 500 (%#v)", body, status, data)
		}
	}

	for _, body := range []string{`{"queen_name":0}`, `{"queen_name":false}`, `{"queen_name":[]}`} {
		called := false
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateSession: func(params CreateSessionParams) (*Session, error) {
				called = true
				if params.QueenName != nil {
					t.Fatalf("%s: queen_name = %#v, want nil", body, params.QueenName)
				}
				return &Session{ID: "s1", LoadedAt: 1000000.0}, nil
			},
		}
		status, data := createSessionRequest(t, deps, body)
		if status != http.StatusCreated {
			t.Fatalf("%s: status = %d, want 201 (%#v)", body, status, data)
		}
		if !called {
			t.Fatalf("%s: CreateSession was not called", body)
		}
	}
}

// Parity-exempt: Go glue for normalize_legacy_phase on the request body.
func TestCreateSessionNormalizesLegacyPhase(t *testing.T) {
	cases := map[string]string{
		"working":     "colony",
		"reviewing":   "colony",
		"incubating":  "independent",
		"colony":      "colony",
		"independent": "independent",
	}
	for input, want := range cases {
		var captured CreateSessionParams
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateSession: func(params CreateSessionParams) (*Session, error) {
				captured = params
				return &Session{ID: "s1", LoadedAt: 1000000.0}, nil
			},
		}
		status, _ := createSessionRequest(t, deps, `{"initial_phase":"`+input+`"}`)
		if status != http.StatusCreated {
			t.Fatalf("phase %q: status = %d, want 201", input, status)
		}
		if captured.InitialPhase == nil || *captured.InitialPhase != want {
			t.Fatalf("phase %q: forwarded %#v, want %q", input, captured.InitialPhase, want)
		}
	}
}

// Parity-exempt: Go glue for the queen-profile validation branch (unknown
// queen -> 404; a built-in default queen materializes and passes).
func TestCreateSessionQueenValidation(t *testing.T) {
	notFoundDeps := SessionRouteDeps{Home: createSessionHome(t)}
	status, data := createSessionRequest(t, notFoundDeps, `{"queen_name":"no_such_queen_xyz"}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if data["error"] != "Queen 'no_such_queen_xyz' not found" {
		t.Fatalf("error = %#v", data["error"])
	}

	home := createSessionHome(t)
	called := false
	foundDeps := SessionRouteDeps{
		Home: home,
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			called = true
			return &Session{ID: "s1", LoadedAt: 1000000.0}, nil
		},
	}
	status, _ = createSessionRequest(t, foundDeps, `{"queen_name":"queen_operations"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if !called {
		t.Fatalf("CreateSession was not called for a valid queen")
	}
	profile := filepath.Join(home.QueensDir(), "queen_operations", "profile.yaml")
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("default queen was not materialized at %s: %v", profile, err)
	}
}

// Parity-exempt: Go glue for the ValueError -> 409 mapping, including the
// "currently loading" marker payload.
func TestCreateSessionValueErrorMapsToConflict(t *testing.T) {
	loadingDeps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(CreateSessionParams) (*Session, error) {
			return nil, &SessionCreateValueError{Message: "Colony 'abc' is currently loading"}
		},
	}
	status, data := createSessionRequest(t, loadingDeps, `{"colony_id":"abc"}`)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if data["error"] != "Colony 'abc' is currently loading" {
		t.Fatalf("error = %#v", data["error"])
	}
	if data["colony_id"] != "abc" {
		t.Fatalf("colony_id = %#v", data["colony_id"])
	}
	if loading, _ := data["loading"].(bool); !loading {
		t.Fatalf("loading = %#v, want true", data["loading"])
	}

	plainDeps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(CreateSessionParams) (*Session, error) {
			return nil, &SessionCreateValueError{Message: "boom"}
		},
	}
	status, data = createSessionRequest(t, plainDeps, "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if data["error"] != "boom" {
		t.Fatalf("error = %#v", data["error"])
	}
	if _, present := data["loading"]; present {
		t.Fatalf("plain conflict must not carry loading: %#v", data)
	}
}

// Parity-exempt: Go glue for the FileNotFoundError -> 404 mapping and its
// "no colony_id" label fallback.
func TestCreateSessionColonyNotFoundMapsTo404(t *testing.T) {
	deps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(CreateSessionParams) (*Session, error) {
			return nil, &SessionCreateColonyNotFoundError{}
		},
	}
	status, data := createSessionRequest(t, deps, `{"colony_id":"abc"}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if data["error"] != "Colony not found: abc" {
		t.Fatalf("error = %#v", data["error"])
	}
	status, data = createSessionRequest(t, deps, "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if data["error"] != "Colony not found: no colony_id" {
		t.Fatalf("error = %#v", data["error"])
	}
}

// Parity-exempt: Go glue for the credential failure -> 424 mapping.
func TestCreateSessionCredentialFailureMapsTo424(t *testing.T) {
	deps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(CreateSessionParams) (*Session, error) {
			return nil, credentials.NewCredentialError("missing $%s", "OPENAI_KEY")
		},
	}
	status, data := createSessionRequest(t, deps, `{"colony_id":"abc"}`)
	if status != http.StatusFailedDependency {
		t.Fatalf("status = %d, want 424", status)
	}
	if data["error"] != "credentials_required" {
		t.Fatalf("error = %#v", data["error"])
	}
	if data["message"] != "missing $OPENAI_KEY" {
		t.Fatalf("message = %#v", data["message"])
	}
	if data["agent_path"] != "abc" {
		t.Fatalf("agent_path = %#v", data["agent_path"])
	}
	if required, ok := data["required"].([]any); !ok || len(required) != 0 {
		t.Fatalf("required = %#v, want []", data["required"])
	}
}

// Parity-exempt: Go glue for the generic-error, nil-hook and nil-session
// fallbacks on the standard create path.
func TestCreateSessionInternalFailures(t *testing.T) {
	genericDeps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(CreateSessionParams) (*Session, error) {
			return nil, errors.New("kaboom")
		},
	}
	status, data := createSessionRequest(t, genericDeps, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
	if data["error"] != internalServerError {
		t.Fatalf("error = %#v", data["error"])
	}

	nilHookStatus, _ := createSessionRequest(t, SessionRouteDeps{Home: createSessionHome(t)}, "")
	if nilHookStatus != http.StatusInternalServerError {
		t.Fatalf("nil hook status = %d, want 500", nilHookStatus)
	}

	nilSessionDeps := SessionRouteDeps{
		Home: createSessionHome(t),
		CreateSession: func(CreateSessionParams) (*Session, error) {
			return nil, nil
		},
	}
	nilSessionStatus, _ := createSessionRequest(t, nilSessionDeps, "")
	if nilSessionStatus != http.StatusInternalServerError {
		t.Fatalf("nil session status = %d, want 500", nilSessionStatus)
	}
}

// Parity-exempt: Go glue for the fork-into-new-colony branch
// (colony_id + source_session_id): vacate, dedupe, slug validation and the
// _ColonyForkError status plumbing.
func TestCreateSessionForkBranch(t *testing.T) {
	t.Run("vacate failure maps to 500", func(t *testing.T) {
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			VacateSoftDeleted: func(string) error {
				return errors.New("boom")
			},
			CreateColonyFromSource: func(CreateColonyFromSourceParams) (*Session, error) {
				t.Fatalf("fork hook must not run after a vacate failure")
				return nil, nil
			},
		}
		status, data := createSessionRequest(t, deps, `{"colony_id":"abc","source_session_id":"src"}`)
		if status != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", status)
		}
		if data["error"] != "failed to clear soft-deleted colony 'abc': boom" {
			t.Fatalf("error = %#v", data["error"])
		}
	})

	t.Run("existing colony dedupes", func(t *testing.T) {
		home := createSessionHome(t)
		dir := filepath.Join(home.ColoniesDir(), "abc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		var captured CreateColonyFromSourceParams
		deps := SessionRouteDeps{
			Home: home,
			CreateColonyFromSource: func(params CreateColonyFromSourceParams) (*Session, error) {
				captured = params
				return &Session{ID: "forked", ColonyID: params.ColonyID, LoadedAt: 1000000.0}, nil
			},
		}
		status, data := createSessionRequest(t, deps, `{"colony_id":"abc","source_session_id":"src"}`)
		if status != http.StatusCreated {
			t.Fatalf("status = %d, want 201", status)
		}
		if captured.ColonyID != "abc_2" {
			t.Fatalf("forwarded colony_id = %q, want abc_2", captured.ColonyID)
		}
		if captured.SourceSessionID != "src" {
			t.Fatalf("forwarded source_session_id = %q", captured.SourceSessionID)
		}
		if data["colony_id"] != "abc_2" {
			t.Fatalf("response colony_id = %#v", data["colony_id"])
		}
	})

	t.Run("slug is trimmed before forwarding", func(t *testing.T) {
		var captured CreateColonyFromSourceParams
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateColonyFromSource: func(params CreateColonyFromSourceParams) (*Session, error) {
				captured = params
				return &Session{ID: "forked", ColonyID: params.ColonyID, LoadedAt: 1000000.0}, nil
			},
		}
		status, data := createSessionRequest(t, deps, `{"colony_id":"  abc  ","source_session_id":"src"}`)
		if status != http.StatusCreated {
			t.Fatalf("status = %d, want 201", status)
		}
		if captured.ColonyID != "abc" {
			t.Fatalf("forwarded colony_id = %q, want abc", captured.ColonyID)
		}
		if data["colony_id"] != "abc" {
			t.Fatalf("response colony_id = %#v", data["colony_id"])
		}
	})

	t.Run("bad slug maps to 400", func(t *testing.T) {
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateColonyFromSource: func(CreateColonyFromSourceParams) (*Session, error) {
				t.Fatalf("fork hook must not run for an invalid slug")
				return nil, nil
			},
		}
		status, data := createSessionRequest(t, deps, `{"colony_id":"Bad-Slug","source_session_id":"src"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if data["error"] != "colony_id must be lowercase alphanumeric with underscores" {
			t.Fatalf("error = %#v", data["error"])
		}
	})

	t.Run("ColonyForkError carries its status", func(t *testing.T) {
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateColonyFromSource: func(CreateColonyFromSourceParams) (*Session, error) {
				return nil, &ColonyForkError{Message: "source is not live", Status: http.StatusNotFound}
			},
		}
		status, data := createSessionRequest(t, deps, `{"colony_id":"abc","source_session_id":"src"}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
		if data["error"] != "source is not live" {
			t.Fatalf("error = %#v", data["error"])
		}
	})

	t.Run("ColonyForkError defaults to 400", func(t *testing.T) {
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateColonyFromSource: func(CreateColonyFromSourceParams) (*Session, error) {
				return nil, &ColonyForkError{Message: "bad request"}
			},
		}
		status, _ := createSessionRequest(t, deps, `{"colony_id":"abc","source_session_id":"src"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
	})

	t.Run("generic error maps to 500", func(t *testing.T) {
		deps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateColonyFromSource: func(CreateColonyFromSourceParams) (*Session, error) {
				return nil, errors.New("kaboom")
			},
		}
		status, data := createSessionRequest(t, deps, `{"colony_id":"abc","source_session_id":"src"}`)
		if status != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", status)
		}
		if data["error"] != internalServerError {
			t.Fatalf("error = %#v", data["error"])
		}
	})

	t.Run("nil hook and nil session map to 500", func(t *testing.T) {
		nilHookStatus, _ := createSessionRequest(t, SessionRouteDeps{Home: createSessionHome(t)}, `{"colony_id":"abc","source_session_id":"src"}`)
		if nilHookStatus != http.StatusInternalServerError {
			t.Fatalf("nil hook status = %d, want 500", nilHookStatus)
		}
		nilSessionDeps := SessionRouteDeps{
			Home: createSessionHome(t),
			CreateColonyFromSource: func(CreateColonyFromSourceParams) (*Session, error) {
				return nil, nil
			},
		}
		nilSessionStatus, _ := createSessionRequest(t, nilSessionDeps, `{"colony_id":"abc","source_session_id":"src"}`)
		if nilSessionStatus != http.StatusInternalServerError {
			t.Fatalf("nil session status = %d, want 500", nilSessionStatus)
		}
	})
}

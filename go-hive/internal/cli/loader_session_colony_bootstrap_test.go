package cli

// Parity: framework/server/session_manager.py::SessionManager.create_session
// Parity: framework/server/session_manager.py::_ensure_minimal_colony
//
// The dashboard's "Create Colony" popup posts a fresh colony_id to
// POST /api/sessions. Upstream SessionManager.create_session (the branch
// matrix in routes_sessions.py::handle_create_session's docstring) bootstraps
// the minimal colony when colony_id names a colony that is not on disk yet
// and no source_session_id forks an existing one; the route answers 201 with
// the live colony session. These tests drive the real create-session hook
// through the real HTTP app — no fake SessionRouteDeps — so a regression to
// "Colony not found" fails here as it would for the user.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

type colonyCreateFixture struct {
	home    hivehome.Home
	manager *hiveserver.SessionManager
	app     *hiveserver.App
}

// newColonyCreateFixture wires the production create-session hook
// (createChatSession) into a real server app over a temp HIVE_HOME.
func newColonyCreateFixture(t *testing.T) *colonyCreateFixture {
	t.Helper()
	home := writeSessionLayoutHome(t)
	// The session credential adapter resolves the persisted key through
	// LoadCredentialKey, which reads HIVE_HOME from the process env and
	// exports HIVE_CREDENTIAL_KEY when it finds the file. Pin the env to this
	// fixture's fresh home so the lookup cannot read (and leak) the
	// developer's or another test's key.
	t.Setenv("HIVE_HOME", home.Root)
	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0, 2)
	runner := &Runner{Stdout: io.Discard, Stderr: io.Discard}
	create := func(params hiveserver.CreateSessionParams) (*hiveserver.Session, error) {
		return runner.createChatSession(params, manager, &liveMu, &live, home, "")
	}
	app := hiveserver.NewApp(hiveserver.AppDeps{
		Manager:       manager,
		Home:          home,
		LiveSessions:  manager.ListSessions,
		CreateSession: create,
	})
	fixture := &colonyCreateFixture{home: home, manager: manager, app: app}
	t.Cleanup(func() {
		for _, session := range manager.ListSessions() {
			if session.Chat != nil {
				_ = session.Chat.Close()
			}
			stopColonyRuntime(session.Colony)
			manager.StopSession(session.ID)
		}
	})
	return fixture
}

func (f *colonyCreateFixture) serve(t *testing.T, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(recorder, request)
	decoded := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s %s: response is not a JSON object: %v (body=%s)", method, target, err, recorder.Body.String())
	}
	return recorder, decoded
}

func readJSONMapFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return decoded
}

// Parity: framework/server/session_manager.py::SessionManager.create_session
func TestCreateChatSessionBootstrapsMissingColony(t *testing.T) {
	fixture := newColonyCreateFixture(t)
	injectSelectorLLM(t, "ok")

	recorder, body := fixture.serve(t, http.MethodPost, "/api/sessions",
		`{"colony_id":"night_shift","queen_name":"queen_operations","initial_phase":"colony"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", recorder.Code, recorder.Body.String())
	}
	colonyPath := fixture.home.ColonyDir("night_shift")
	if body["colony_id"] != "night_shift" || body["has_worker"] != true {
		t.Fatalf("colony_id/has_worker = %#v/%#v, want night_shift/true", body["colony_id"], body["has_worker"])
	}
	if body["agent_path"] != colonyPath {
		t.Fatalf("agent_path = %#v, want %q", body["agent_path"], colonyPath)
	}
	// The phase follows the colony binding; initial_phase is never the thing
	// that makes the session colony-bound.
	if body["queen_phase"] != hiveserver.QueenPhaseColony {
		t.Fatalf("queen_phase = %#v, want colony", body["queen_phase"])
	}
	sessionID, _ := body["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("response carries no session_id: %#v", body)
	}
	live := fixture.manager.GetSession(sessionID)
	if live == nil {
		t.Fatalf("session %q is not live", sessionID)
	}
	if live.ColonyID != "night_shift" || live.WorkerPath != colonyPath {
		t.Fatalf("live binding = %q/%q, want night_shift/%q", live.ColonyID, live.WorkerPath, colonyPath)
	}
	built, ok := live.Colony.(*colonyhost.Host)
	if !ok || built == nil || !built.Started() {
		t.Fatalf("session colony = %T, want a started *host.Host", live.Colony)
	}

	// metadata.json: upstream _ensure_minimal_colony writes exactly
	// {name, created_at (seconds float), queen_name?}.
	meta := readJSONMapFile(t, filepath.Join(colonyPath, "metadata.json"))
	if len(meta) != 3 || meta["name"] != "night_shift" || meta["queen_name"] != "queen_operations" {
		t.Fatalf("metadata.json = %#v, want {name, created_at, queen_name}", meta)
	}
	if createdAt, ok := meta["created_at"].(float64); !ok || createdAt <= 0 {
		t.Fatalf("created_at = %#v, want a positive seconds float", meta["created_at"])
	}

	// worker.json: field-for-field upstream's minimal worker config.
	worker := readJSONMapFile(t, filepath.Join(colonyPath, "worker.json"))
	if len(worker) != 5 || worker["name"] != "Night Shift" ||
		worker["description"] != "Colony night_shift" || worker["queen_name"] != "queen_operations" {
		t.Fatalf("worker.json = %#v", worker)
	}
	if tools, ok := worker["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("worker.tools = %#v, want []", worker["tools"])
	}
	goal, _ := worker["goal"].(map[string]any)
	if goal["description"] != "Work on tasks for colony night_shift" {
		t.Fatalf("worker.goal = %#v", worker["goal"])
	}

	// GET /api/sessions lists the live colony session.
	recorder, listed := fixture.serve(t, http.MethodGet, "/api/sessions", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions status = %d", recorder.Code)
	}
	visible := false
	for _, entry := range listed["sessions"].([]any) {
		session, _ := entry.(map[string]any)
		if session["session_id"] == sessionID {
			visible = session["colony_id"] == "night_shift" && session["agent_path"] == colonyPath
		}
	}
	if !visible {
		t.Fatalf("GET /api/sessions does not show %q bound to night_shift: %#v", sessionID, listed)
	}

	// GET /api/colonies/tools-index lists the bootstrapped colony.
	recorder, index := fixture.serve(t, http.MethodGet, "/api/colonies/tools-index", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/colonies/tools-index status = %d", recorder.Code)
	}
	colonies, _ := index["colonies"].([]any)
	visible = false
	for _, entry := range colonies {
		colony, _ := entry.(map[string]any)
		if colony["name"] == "night_shift" {
			visible = colony["queen_name"] == "queen_operations"
		}
	}
	if !visible {
		t.Fatalf("tools-index does not show night_shift: %#v", index)
	}
}

// Parity: framework/server/session_manager.py::_ensure_minimal_colony
//
// queen_name is optional: without it the bootstrap files carry no queen_name
// key at all (Python's `if queen_name:`).
func TestCreateChatSessionBootstrapWithoutQueenName(t *testing.T) {
	fixture := newColonyCreateFixture(t)
	injectSelectorLLM(t, "ok")

	recorder, body := fixture.serve(t, http.MethodPost, "/api/sessions", `{"colony_id":"quiet_desk"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", recorder.Code, recorder.Body.String())
	}
	colonyPath := fixture.home.ColonyDir("quiet_desk")
	if body["agent_path"] != colonyPath || body["colony_id"] != "quiet_desk" {
		t.Fatalf("agent_path/colony_id = %#v/%#v, want %q/quiet_desk", body["agent_path"], body["colony_id"], colonyPath)
	}
	// No initial_phase was sent: colony is the phase the binding derives.
	if body["queen_phase"] != hiveserver.QueenPhaseColony {
		t.Fatalf("queen_phase = %#v, want colony from the binding", body["queen_phase"])
	}

	meta := readJSONMapFile(t, filepath.Join(colonyPath, "metadata.json"))
	if len(meta) != 2 || meta["name"] != "quiet_desk" {
		t.Fatalf("metadata.json = %#v, want {name, created_at}", meta)
	}
	if _, present := meta["queen_name"]; present {
		t.Fatalf("metadata.json must not carry queen_name without one: %#v", meta)
	}

	worker := readJSONMapFile(t, filepath.Join(colonyPath, "worker.json"))
	if len(worker) != 4 || worker["name"] != "Quiet Desk" || worker["description"] != "Colony quiet_desk" {
		t.Fatalf("worker.json = %#v", worker)
	}
	if _, present := worker["queen_name"]; present {
		t.Fatalf("worker.json must not carry queen_name without one: %#v", worker)
	}
	if tools, ok := worker["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("worker.tools = %#v, want []", worker["tools"])
	}
	goal, _ := worker["goal"].(map[string]any)
	if goal["description"] != "Work on tasks for colony quiet_desk" {
		t.Fatalf("worker.goal = %#v", worker["goal"])
	}
}

// Parity: framework/server/session_manager.py::_ensure_minimal_colony
//
// A partially built colony (directory present, metadata.json missing) takes
// the bootstrap branch too, and bootstrap only fills in the files that are
// absent: an existing worker.json is never overwritten.
func TestCreateChatSessionBootstrapKeepsExistingWorkerFile(t *testing.T) {
	fixture := newColonyCreateFixture(t)
	injectSelectorLLM(t, "ok")

	colonyPath := filepath.Join(fixture.home.ColoniesDir(), "half_built")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	workerPath := filepath.Join(colonyPath, "worker.json")
	original := []byte(lifecycleWorkerSpec)
	if err := os.WriteFile(workerPath, original, 0o644); err != nil {
		t.Fatalf("write worker.json: %v", err)
	}

	recorder, body := fixture.serve(t, http.MethodPost, "/api/sessions",
		`{"colony_id":"half_built","queen_name":"queen_operations"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if body["agent_path"] != colonyPath {
		t.Fatalf("agent_path = %#v, want %q", body["agent_path"], colonyPath)
	}
	meta := readJSONMapFile(t, filepath.Join(colonyPath, "metadata.json"))
	if len(meta) != 3 || meta["name"] != "half_built" || meta["queen_name"] != "queen_operations" {
		t.Fatalf("metadata.json = %#v", meta)
	}
	// The pre-existing worker definition survives the bootstrap: the minimal
	// worker.json is only written when the colony has none. (The tracker
	// stamps input_data.binding into a config with a system_prompt when its
	// runtime starts, so the file is compared field-wise, not byte-wise.)
	worker := readJSONMapFile(t, workerPath)
	if worker["name"] != "worker" || worker["description"] != "trader" ||
		worker["system_prompt"] != "be a careful trader" {
		t.Fatalf("bootstrap overwrote an existing worker.json: %#v", worker)
	}
	if tools, ok := worker["tools"].([]any); !ok || len(tools) != 1 || tools[0] != "read_file" {
		t.Fatalf("existing worker tools were replaced: %#v", worker["tools"])
	}

	sessionID, _ := body["session_id"].(string)
	live := fixture.manager.GetSession(sessionID)
	if live == nil {
		t.Fatalf("session %q is not live", sessionID)
	}
	built, ok := live.Colony.(*colonyhost.Host)
	if !ok || built == nil || !built.Started() {
		t.Fatalf("session colony = %T, want a started *host.Host", live.Colony)
	}
}

// Parity: framework/server/session_manager.py::SessionManager.create_session
//
// An existing colony — a scaffold-only colony included — is loaded as-is:
// upstream's scaffolded exception (:738-760) keeps the directory instead of
// deduplicating a fresh "<name>_2", and no bootstrap write touches its files.
func TestCreateChatSessionExistingScaffoldedColonyIsReused(t *testing.T) {
	fixture := newColonyCreateFixture(t)
	injectSelectorLLM(t, "ok")

	colonyPath := filepath.Join(fixture.home.ColoniesDir(), "launch_pad")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	metadata := []byte(`{"name":"launch_pad","created_at":1730000000.0,"queen_name":"queen_operations","scaffolded":true}`)
	worker := []byte(lifecycleWorkerSpec)
	if err := os.WriteFile(filepath.Join(colonyPath, "metadata.json"), metadata, 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyPath, "worker.json"), worker, 0o644); err != nil {
		t.Fatalf("write worker: %v", err)
	}

	// The initial prompt is what makes the slug dedup path reachable up
	// upstream; the scaffolded flag keeps the existing directory instead.
	recorder, body := fixture.serve(t, http.MethodPost, "/api/sessions",
		`{"colony_id":"launch_pad","initial_prompt":"Take over the launch work"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if body["agent_path"] != colonyPath || body["queen_phase"] != hiveserver.QueenPhaseColony {
		t.Fatalf("agent_path/queen_phase = %#v/%#v", body["agent_path"], body["queen_phase"])
	}
	if _, err := os.Stat(fixture.home.ColonyDir("launch_pad_2")); !os.IsNotExist(err) {
		t.Fatalf("existing colony was deduplicated into launch_pad_2 (stat err = %v)", err)
	}
	// The bootstrap writes (skills/, runs/, triggers.json) are the marker
	// that the fresh-colony branch ran; an existing colony must not get them.
	if _, err := os.Stat(filepath.Join(colonyPath, "skills")); !os.IsNotExist(err) {
		t.Fatalf("bootstrap ran over an existing colony (skills/ appeared, stat err = %v)", err)
	}
	metaData, err := os.ReadFile(filepath.Join(colonyPath, "metadata.json"))
	if err != nil || !bytes.Equal(metaData, metadata) {
		t.Fatalf("existing metadata.json was rewritten: %s (err = %v)", metaData, err)
	}
	// The existing worker definition is still the one the session loaded;
	// only the runtime's tracker stamp may have touched it.
	workerData := readJSONMapFile(t, filepath.Join(colonyPath, "worker.json"))
	if workerData["name"] != "worker" || workerData["system_prompt"] != "be a careful trader" {
		t.Fatalf("existing worker.json was rewritten: %#v", workerData)
	}

	sessionID, _ := body["session_id"].(string)
	live := fixture.manager.GetSession(sessionID)
	if live == nil || live.WorkerPath != colonyPath {
		t.Fatalf("live session = %#v, want worker_path %q", live, colonyPath)
	}
}

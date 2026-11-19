package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

const lifecycleWorkerSpec = `{"name":"worker","description":"trader","system_prompt":"be a careful trader",` +
	`"tools":["read_file"],"goal":{"description":"trade honeycomb"}}`

// lifecycleTestLLM answers every turn with plain text, so a started overseer
// finishes its turn immediately and parks until the host is stopped.
type lifecycleTestLLM struct {
	calls atomic.Int32
}

func (f *lifecycleTestLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.calls.Add(1)
	return llm.Completion{Content: "ok", Model: "scripted"}, nil
}

type lifecycleFixture struct {
	lifecycle *colonyLifecycle
	manager   *hiveserver.SessionManager
	home      hivehome.Home
	liveMu    *sync.Mutex
	live      *[]*hiveserver.Session
}

// newLifecycleFixture wires the loader's colony lifecycle against a temp
// HIVE_HOME and a scripted host factory; the scripted runtime is a real
// colony host, only its completer is stubbed, so Start/StartOverseer exercise
// the production paths.
func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)
	runner := &Runner{Stdout: io.Discard, Stderr: io.Discard}
	lifecycle := runner.newColonyLifecycle(manager, &liveMu, &live, home, "")
	lifecycle.build = func(colonyPath, colonyID, model string) (*colonyhost.Host, error) {
		return colonyhost.New(colonyhost.Config{
			ColonyID:    colonyID,
			AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "worker", AgentType: "event_loop"},
			StoragePath: colonyPath,
			LLM:         &lifecycleTestLLM{},
		})
	}
	return &lifecycleFixture{lifecycle: lifecycle, manager: manager, home: home, liveMu: &liveMu, live: &live}
}

func (f *lifecycleFixture) newApp() *hiveserver.App {
	return hiveserver.NewApp(hiveserver.AppDeps{
		Manager:                f.manager,
		Home:                   f.home,
		LiveSessions:           f.manager.ListSessions,
		Colony:                 f.lifecycle,
		CreateColonyFromSource: f.lifecycle.CreateColonyFromSource,
	})
}

func (f *lifecycleFixture) cleanup(t *testing.T) {
	t.Helper()
	for _, session := range f.manager.ListSessions() {
		stopColonyRuntime(session.Colony)
		f.manager.StopSession(session.ID)
	}
}

// Parity: tests/test_session_unified_colony_runtime.py::test_start_unified_colony_runtime_creates_real_colony
func TestLoadColonyStartsRuntimeAndOverseer(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_ = writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)

	session := hiveserver.NewSession("s1", eventbus.NewEventBus())
	fixture.manager.AddSession(session)

	loaded, err := fixture.lifecycle.LoadColony("s1", "honeycomb", nil)
	if err != nil {
		t.Fatalf("LoadColony: %v", err)
	}
	built, ok := loaded.Colony.(*colonyhost.Host)
	if !ok {
		t.Fatalf("session colony = %T, want *host.Host", loaded.Colony)
	}
	if !built.Started() {
		t.Fatal("colony runtime was not started")
	}
	if !built.OverseerRunning() {
		t.Fatal("colony overseer was not started")
	}
	if loaded.ColonyID != "honeycomb" {
		t.Fatalf("session colony id = %q", loaded.ColonyID)
	}
	if loaded.WorkerPath != fixture.home.ColonyDir("honeycomb") {
		t.Fatalf("session worker path = %q", loaded.WorkerPath)
	}

	if !fixture.lifecycle.UnloadColony("s1") {
		t.Fatal("UnloadColony = false, want true")
	}
	if loaded.Colony != nil || loaded.ColonyID != "" || loaded.WorkerPath != "" {
		t.Fatalf("unload left session colony=%v id=%q path=%q", loaded.Colony, loaded.ColonyID, loaded.WorkerPath)
	}
	if fixture.lifecycle.UnloadColony("s1") {
		t.Fatal("second UnloadColony = true, want false")
	}
}

// Parity: tests/test_colony_fork_live.py::test_live_queen_fork_to_colony
func TestCreateColonyFromSourceMarksSourceAndStartsOverseer(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_ = writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)

	source := hiveserver.NewSession("src", eventbus.NewEventBus())
	source.QueenName = "queen_alpha"
	fixture.manager.AddSession(source)

	request := httptest.NewRequest(http.MethodPost, "/api/sessions",
		strings.NewReader(`{"colony_id":"forked","source_session_id":"src"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("create colony status = %d body = %s", response.Code, response.Body.String())
	}
	if !source.ColonySpawned || source.SpawnedColonyID != "forked" {
		t.Fatalf("source spawn lock = %v/%q", source.ColonySpawned, source.SpawnedColonyID)
	}

	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	sessionID, _ := payload["session_id"].(string)
	created := fixture.manager.GetSession(sessionID)
	if created == nil {
		t.Fatalf("created session %q is not registered", sessionID)
	}
	if created.ColonyID != "forked" || created.WorkerPath != fixture.home.ColonyDir("forked") {
		t.Fatalf("created session colony = %q/%q", created.ColonyID, created.WorkerPath)
	}
	built, ok := created.Colony.(*colonyhost.Host)
	if !ok {
		t.Fatalf("created session colony = %T, want *host.Host", created.Colony)
	}
	if !built.Started() || !built.OverseerRunning() {
		t.Fatalf("created colony started=%v overseer=%v", built.Started(), built.OverseerRunning())
	}

	workerData, err := os.ReadFile(filepath.Join(fixture.home.ColonyDir("forked"), "worker.json"))
	if err != nil {
		t.Fatalf("read forked worker.json: %v", err)
	}
	if !strings.Contains(string(workerData), `"description": "Colony forked"`) {
		t.Fatalf("forked worker.json = %s", workerData)
	}
}

// Parity: tests/test_colony_runtime_overseer.py::test_start_overseer_creates_persistent_worker
func TestCreateColonyFromSourceReportsRuntimeFailure(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	fixture.lifecycle.build = func(colonyPath, colonyID, model string) (*colonyhost.Host, error) {
		return nil, errors.New("no provider configured")
	}
	source := hiveserver.NewSession("src", eventbus.NewEventBus())
	fixture.manager.AddSession(source)

	request := httptest.NewRequest(http.MethodPost, "/api/sessions",
		strings.NewReader(`{"colony_id":"broken","source_session_id":"src"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed fork status = %d body = %s, want 503", response.Code, response.Body.String())
	}
	if source.ColonySpawned {
		t.Fatal("failed fork still marked the source session as spawned")
	}
	if _, statErr := os.Stat(fixture.home.ColonyDir("broken")); !os.IsNotExist(statErr) {
		t.Fatalf("failed fork left %q on disk (stat err = %v)", fixture.home.ColonyDir("broken"), statErr)
	}
}

// Parity: tests/test_session_unified_colony_runtime.py::test_start_unified_colony_runtime_creates_real_colony
func TestLoadColonyReportsUnavailableRuntime(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_ = writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)
	session := hiveserver.NewSession("s1", eventbus.NewEventBus())
	fixture.manager.AddSession(session)

	if _, err := fixture.lifecycle.LoadColony("missing", "missing", nil); err == nil {
		t.Fatal("LoadColony on an unknown session returned no error")
	}

	fixture.lifecycle.build = func(colonyPath, colonyID, model string) (*colonyhost.Host, error) {
		return nil, errors.New("no provider configured")
	}
	if _, err := fixture.lifecycle.LoadColony("s1", "honeycomb", nil); err == nil {
		t.Fatal("LoadColony with an unavailable runtime returned no error")
	}
	if session.Colony != nil || session.ColonyID != "" {
		t.Fatalf("failed load mutated the session: colony=%v id=%q", session.Colony, session.ColonyID)
	}
}

// Parity: framework/server/routes_workers.py::handle_stop_live_worker
func TestWorkerRoutesStayUnavailableWithoutAColony(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	session := hiveserver.NewSession("s1", eventbus.NewEventBus())
	fixture.manager.AddSession(session)

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/workers/w1/stop", nil)
	response := httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-colony stop status = %d body = %s, want 503", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "No active colony") {
		t.Fatalf("no-colony stop body = %s", response.Body.String())
	}
	if session.Colony != nil {
		t.Fatalf("routing invented a colony on the session: %T", session.Colony)
	}
}

// Parity-exempt: Go-only guard for the port's create_session branching — a
// session bound to a colony must not come back without its runtime.
func TestCreateSessionWithColonyFailsWhenRuntimeCannotBeBuilt(t *testing.T) {
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), []byte(`{"llm":{"model":"openai/gpt-4o"}}`), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	colonyPath := filepath.Join(home.ColoniesDir(), "honeycomb")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyPath, "worker.json"), []byte(lifecycleWorkerSpec), 0o644); err != nil {
		t.Fatalf("write worker config: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("HIVE_API_KEY", "")

	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)
	runner := &Runner{Stdout: io.Discard, Stderr: io.Discard}
	colonyID := "honeycomb"
	session, err := runner.createChatSession(hiveserver.CreateSessionParams{ColonyID: &colonyID}, manager, &liveMu, &live, home, "")
	if err == nil {
		stopColonyRuntime(session.Colony)
		manager.StopSession(session.ID)
		t.Fatal("createChatSession with a colony and no provider returned no error")
	}
	if manager.SessionCount() != 0 {
		t.Fatalf("failed create registered %d sessions", manager.SessionCount())
	}
}

// Parity: tests/test_session_unified_colony_runtime.py::test_unified_colony_workers_land_under_queen_dir
func TestCreateSessionWithoutColonyKeepsFallingBackToDisk(t *testing.T) {
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), []byte(`{"llm":{"model":"ollama/llama3"}}`), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)
	runner := &Runner{Stdout: io.Discard, Stderr: io.Discard}
	session, err := runner.createChatSession(hiveserver.CreateSessionParams{}, manager, &liveMu, &live, home, "")
	if err != nil {
		t.Fatalf("createChatSession: %v", err)
	}
	t.Cleanup(func() { manager.StopSession(session.ID) })
	if session.Colony != nil {
		t.Fatalf("unbound session got a colony runtime: %T", session.Colony)
	}
	if session.ColonyID != "" {
		t.Fatalf("unbound session colony id = %q", session.ColonyID)
	}
}

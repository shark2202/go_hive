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
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
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
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	// The session credential adapter resolves the persisted encryption key
	// through LoadCredentialKey, which reads HIVE_HOME from the process env and
	// exports HIVE_CREDENTIAL_KEY when it finds the file. Pin the env to this
	// fixture's fresh home so the lookup cannot read (and leak) the developer's
	// or another test's key.
	t.Setenv("HIVE_HOME", home.Root)
	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)
	runner := &Runner{Stdout: io.Discard, Stderr: io.Discard}
	lifecycle := runner.newColonyLifecycle(manager, &liveMu, &live, home, "")
	// The fork's compaction await uses the production 180s cap but a fast
	// poll so the tests that fork a copied session do not each sleep 500ms.
	lifecycle.forkAwaitPoll = 5 * time.Millisecond
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

// Parity: framework/server/session_manager.py::SessionManager._load_worker_core
// (a marked session refuses a second colony load, and a completed load clears
// the marker)
func TestLoadColonyHonoursAndClearsLoadingMarker(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_ = writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)

	session := hiveserver.NewSession("s-loading", eventbus.NewEventBus())
	fixture.manager.AddSession(session)

	if !fixture.manager.MarkLoading("s-loading") {
		t.Fatal("MarkLoading on a fresh session = false")
	}
	_, err := fixture.lifecycle.LoadColony("s-loading", "honeycomb", nil)
	if err == nil || !strings.Contains(err.Error(), "currently loading a colony") {
		t.Fatalf("LoadColony while marked = %v, want the loading conflict", err)
	}
	fixture.manager.ClearLoading("s-loading")

	loaded, err := fixture.lifecycle.LoadColony("s-loading", "honeycomb", nil)
	if err != nil {
		t.Fatalf("LoadColony: %v", err)
	}
	if fixture.manager.IsLoading("s-loading") {
		t.Fatal("LoadColony left the session marked as loading")
	}
	if loaded == nil || loaded.Colony == nil {
		t.Fatal("LoadColony did not bind a runtime")
	}
}

// Parity: framework/server/routes_config.py::_apply_adaptive_budget_flag
// (the loader's live-session snapshot must carry the colony runtime, not just
// its id, or colonies_applied is always 0).
func TestFeatureSessionsSnapshotCarriesColonyRuntime(t *testing.T) {
	fixture := newLifecycleFixture(t)
	built, err := fixture.lifecycle.build(t.TempDir(), "probe", "")
	if err != nil {
		t.Fatalf("build colony host: %v", err)
	}
	session := hiveserver.NewSession("s-feature", eventbus.NewEventBus())
	session.ColonyID = "colony_live"
	session.Colony = built
	live := []*hiveserver.Session{session}

	snapshot := featureSessionsSnapshot(nil, live)
	if len(snapshot) != 1 {
		t.Fatalf("snapshot length = %d, want 1", len(snapshot))
	}
	if snapshot[0].Colony == nil {
		t.Fatal("snapshot dropped the live colony runtime")
	}
	if snapshot[0].ColonyID != "colony_live" {
		t.Fatalf("colony id = %q, want colony_live", snapshot[0].ColonyID)
	}
	snapshot[0].Colony.SetAdaptiveToolBudget(false)
	if enabled, _ := built.AdaptiveToolBudget(); enabled {
		t.Fatal("hot-apply did not reach the live runtime")
	}
}

// TestLoadColonyStartsRuntimeWithoutOverseer pins Hive 1.0.15's production
// ownership: loading a colony starts the runtime, but no live overseer; the
// colony-bound queen chat executes the turn.
//
// Parity: tests/test_session_unified_colony_runtime.py::test_start_unified_colony_runtime_creates_real_colony
func TestLoadColonyStartsRuntimeWithoutOverseer(t *testing.T) {
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
	if built.OverseerRunning() {
		t.Fatal("colony overseer started by default; the colony-bound queen chat must own the turn")
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
func TestCreateColonyFromSourceMarksSourceWithoutOverseer(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_ = writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)

	source := hiveserver.NewSession("src", eventbus.NewEventBus())
	source.QueenName = "queen_alpha"
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": &recordingQueenNode{}}}
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
	if !built.Started() || built.OverseerRunning() {
		t.Fatalf("created colony started=%v overseer=%v, want true/false", built.Started(), built.OverseerRunning())
	}

	// The fork records the session it resumes under (queen_session_id), exactly
	// like upstream's fork metadata: a `serve --colony` restart resolves that id
	// and continues the same transcript instead of minting a fresh session.
	// Parity: framework/server/routes_execution.py::fork_session_into_colony
	metaRaw, err := os.ReadFile(filepath.Join(fixture.home.ColonyDir("forked"), "metadata.json"))
	if err != nil {
		t.Fatalf("read forked metadata.json: %v", err)
	}
	var forkMeta map[string]any
	if err := json.Unmarshal(metaRaw, &forkMeta); err != nil {
		t.Fatalf("decode forked metadata.json: %v", err)
	}
	if recorded, _ := forkMeta["queen_session_id"].(string); recorded != sessionID {
		t.Fatalf("forked metadata queen_session_id = %q, want %q", recorded, sessionID)
	}

	workerData, err := os.ReadFile(filepath.Join(fixture.home.ColonyDir("forked"), "worker.json"))
	if err != nil {
		t.Fatalf("read forked worker.json: %v", err)
	}
	if !strings.Contains(string(workerData), `"description": "Worker clone from queen session src"`) {
		t.Fatalf("forked worker.json = %s", workerData)
	}
	var forked map[string]any
	if err := json.Unmarshal(workerData, &forked); err != nil {
		t.Fatalf("decode forked worker.json: %v", err)
	}
	forkedPrompt, _ := forked["system_prompt"].(string)
	if !strings.Contains(forkedPrompt, "focused worker agent") {
		t.Fatalf("forked worker.json system_prompt is not the worker identity: %q", forkedPrompt)
	}
	if strings.Contains(forkedPrompt, "queen_alpha") || strings.Contains(forkedPrompt, "You are the queen") {
		t.Fatalf("forked worker.json still carries the queen persona: %q", forkedPrompt)
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
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": &recordingQueenNode{}}}
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
	root := testTempDir(t)
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
	root := testTempDir(t)
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

// colonyTriggerCapture collects trigger_removed events off a session bus.
type colonyTriggerCapture struct {
	mu     sync.Mutex
	events []*eventbus.AgentEvent
}

func newColonyTriggerCapture(bus *eventbus.EventBus) *colonyTriggerCapture {
	capture := &colonyTriggerCapture{}
	bus.Subscribe([]eventbus.EventType{eventbus.EventTriggerRemoved}, func(event *eventbus.AgentEvent) {
		capture.mu.Lock()
		capture.events = append(capture.events, event.Clone())
		capture.mu.Unlock()
	})
	return capture
}

func (c *colonyTriggerCapture) removedByID() map[string]*eventbus.AgentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]*eventbus.AgentEvent, len(c.events))
	for _, event := range c.events {
		triggerID, _ := event.Data["trigger_id"].(string)
		out[triggerID] = event
	}
	return out
}

func writeColonyTriggersJSON(t *testing.T, colonyPath string, entries []map[string]any) {
	t.Helper()
	if err := triggers.WriteAgentTriggersJSON(colonyPath, entries); err != nil {
		t.Fatalf("write triggers.json: %v", err)
	}
}

// Parity-exempt: upstream unload_colony cancels trigger timers/subscriptions,
// publishes TRIGGER_REMOVED per available trigger and clears available_triggers
// (framework/server/session_manager.py:1284-1302 via _emit_trigger_events), but
// no Python test covers it. This drives the loader's real UnloadColony through
// the HTTP route and asserts the session state and the session bus events.
func TestUnloadColonyRemovesTriggersAndPublishesRemoved(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	colonyPath := writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)
	writeColonyTriggersJSON(t, colonyPath, []map[string]any{
		{
			"id":             "hourly-sweep",
			"name":           "Hourly sweep",
			"trigger_type":   "timer",
			"trigger_config": map[string]any{"interval_minutes": 60},
			"task":           "sweep the inbox",
			"enabled":        true,
		},
		{
			"id":             "deploy-hook",
			"name":           "deploy hook",
			"trigger_type":   "webhook",
			"trigger_config": map[string]any{"path": "/hooks/deploy", "methods": []any{"POST"}},
			"task":           "ship it",
			"enabled":        false,
		},
	})

	session := hiveserver.NewSession("s1", eventbus.NewEventBus())
	fixture.manager.AddSession(session)
	capture := newColonyTriggerCapture(session.EventBus)

	if _, err := fixture.lifecycle.LoadColony("s1", "honeycomb", nil); err != nil {
		t.Fatalf("LoadColony: %v", err)
	}
	if available := session.AvailableTriggers(); len(available) != 2 {
		t.Fatalf("available triggers after load = %+v, want 2", available)
	}

	// DELETE is the production unload path: it must still answer 200.
	recorder := httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/sessions/s1/colony", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unload status = %d body = %s, want 200", recorder.Code, recorder.Body.String())
	}

	if available := session.AvailableTriggers(); len(available) != 0 {
		t.Fatalf("available triggers after unload = %+v, want none", available)
	}
	if session.Colony != nil || session.ColonyID != "" || session.WorkerPath != "" {
		t.Fatalf("unload left colony=%v id=%q path=%q", session.Colony, session.ColonyID, session.WorkerPath)
	}
	if active := session.ActiveTriggerIDs(); len(active) != 0 {
		t.Fatalf("active triggers after unload = %v, want none", active)
	}
	if nextFire := session.TriggerNextFire(); len(nextFire) != 0 {
		t.Fatalf("next-fire map after unload = %v, want none", nextFire)
	}

	removed := capture.removedByID()
	if len(removed) != 2 {
		t.Fatalf("trigger_removed events = %v, want one per available trigger", removed)
	}
	hourly := removed["hourly-sweep"]
	if hourly == nil {
		t.Fatal("no trigger_removed event for hourly-sweep")
	}
	if hourly.StreamID != "queen" || hourly.Data["name"] != "Hourly sweep" || hourly.Data["trigger_type"] != "timer" {
		t.Fatalf("hourly trigger_removed = %q/%v", hourly.StreamID, hourly.Data)
	}
	hourlyConfig, ok := hourly.Data["trigger_config"].(map[string]any)
	if !ok || hourlyConfig["interval_minutes"] == nil {
		t.Fatalf("hourly trigger_config = %v, want the persisted interval", hourly.Data["trigger_config"])
	}
	if _, ok := hourlyConfig["next_fire_in"]; !ok {
		t.Fatalf("hourly trigger_config lost its live countdown: %v", hourlyConfig)
	}
	hook := removed["deploy-hook"]
	if hook == nil {
		t.Fatal("no trigger_removed event for deploy-hook")
	}
	if hook.Data["name"] != "deploy hook" || hook.Data["trigger_type"] != "webhook" {
		t.Fatalf("hook trigger_removed = %v", hook.Data)
	}

	// The session still exists without a colony: the route answers 409.
	recorder = httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/sessions/s1/colony", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("second unload status = %d body = %s, want 409", recorder.Code, recorder.Body.String())
	}
}

// Parity-exempt: upstream load_colony refuses a session that already holds a
// colony (framework/server/session_manager.py:1027) and unload clears
// available_triggers (:1299-1302); the Go loader swaps colonies on one session,
// so unload A then load B must leave only B's triggers. No Python test covers
// the swap.
func TestUnloadColonyThenLoadOtherColonyKeepsOnlyNewTriggers(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	colonyA := writeColonySpecFixture(t, fixture.home, "colony-a", lifecycleWorkerSpec)
	colonyB := writeColonySpecFixture(t, fixture.home, "colony-b", lifecycleWorkerSpec)
	writeColonyTriggersJSON(t, colonyA, []map[string]any{{
		"id":             "alpha-poll",
		"name":           "alpha poll",
		"trigger_type":   "timer",
		"trigger_config": map[string]any{"interval_minutes": 60},
		"task":           "poll a",
		"enabled":        true,
	}})
	writeColonyTriggersJSON(t, colonyB, []map[string]any{{
		"id":             "beta-poll",
		"name":           "beta poll",
		"trigger_type":   "timer",
		"trigger_config": map[string]any{"interval_minutes": 60},
		"task":           "poll b",
		"enabled":        true,
	}})

	session := hiveserver.NewSession("s1", eventbus.NewEventBus())
	fixture.manager.AddSession(session)
	capture := newColonyTriggerCapture(session.EventBus)

	if _, err := fixture.lifecycle.LoadColony("s1", "colony-a", nil); err != nil {
		t.Fatalf("LoadColony A: %v", err)
	}
	if available := session.AvailableTriggers(); len(available) != 1 || available[0].ID != "alpha-poll" {
		t.Fatalf("colony A triggers = %+v, want only alpha-poll", available)
	}

	if !fixture.lifecycle.UnloadColony("s1") {
		t.Fatal("UnloadColony A = false, want true")
	}
	if available := session.AvailableTriggers(); len(available) != 0 {
		t.Fatalf("unload A left triggers = %+v, want none", available)
	}

	if _, err := fixture.lifecycle.LoadColony("s1", "colony-b", nil); err != nil {
		t.Fatalf("LoadColony B: %v", err)
	}
	available := session.AvailableTriggers()
	if len(available) != 1 || available[0].ID != "beta-poll" {
		t.Fatalf("colony B triggers = %+v, want only beta-poll", available)
	}
	if _, ok := session.LookupTrigger("alpha-poll"); ok {
		t.Fatal("colony B inherited colony A's trigger definition")
	}
	if session.IsTriggerActive("alpha-poll") {
		t.Fatal("colony A's trigger is still active under colony B")
	}
	if !session.IsTriggerActive("beta-poll") {
		t.Fatal("colony B's enabled trigger was not activated")
	}

	removed := capture.removedByID()
	if len(removed) != 1 {
		t.Fatalf("trigger_removed events = %v, want only alpha-poll", removed)
	}
	if _, ok := removed["alpha-poll"]; !ok {
		t.Fatal("unload A published no trigger_removed for alpha-poll")
	}
	if _, ok := removed["beta-poll"]; ok {
		t.Fatal("beta-poll was reported removed although it was just restored")
	}
}

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/triggers"
)

func makeTestSession(id string) *Session {
	session := NewSession(id, nil)
	session.ColonyID = id
	session.WorkerPath = "/tmp/test_agent"
	session.LoadedAt = 1000000.0
	return session
}

func newAppWithSession(t *testing.T, session *Session) *App {
	t.Helper()
	manager := NewSessionManager()
	manager.AddSession(session)
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp home: %v", err)
	}
	return newAppForTest(t, AppDeps{Manager: manager, Home: home})
}

func newEmptyAppWithHome(t *testing.T) *App {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp home: %v", err)
	}
	return newAppForTest(t, AppDeps{Manager: NewSessionManager(), Home: home})
}

func deleteJSON(t *testing.T, app *App, target string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, target, nil))
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s body: %v", target, err)
	}
	return recorder.Code, body
}

type fakeColony struct {
	streamID string
	stats    map[string]any
}

func (f *fakeColony) StreamID() string { return f.streamID }

func (f *fakeColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "", nil
}

func (f *fakeColony) InjectInput(string, string) error { return nil }

func (f *fakeColony) GetStats() map[string]any { return f.stats }

type fakeTriggerClock struct {
	mono float64
	wall float64
}

func (c fakeTriggerClock) NowMonotonic() float64 { return c.mono }
func (c fakeTriggerClock) NowWall() float64      { return c.wall }

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_get_session_found
func TestGetSessionFound(t *testing.T) {
	app := newAppWithSession(t, makeTestSession("test_agent"))

	status, body, _ := getJSON(t, app, "/api/sessions/test_agent")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["session_id"] != "test_agent" {
		t.Fatalf("session_id = %#v, want test_agent", body["session_id"])
	}
	if body["has_worker"] != true {
		t.Fatalf("has_worker = %#v, want True", body["has_worker"])
	}
	if _, ok := body["entry_points"]; !ok {
		t.Fatalf("entry_points missing from %#v", body)
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_get_session_not_found
func TestGetSessionNotFound(t *testing.T) {
	app := newEmptyAppWithHome(t)

	status, body, _ := getJSON(t, app, "/api/sessions/nonexistent")
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["error"] != "Session 'nonexistent' not found" {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_stop_session
func TestStopSession(t *testing.T) {
	app := newAppWithSession(t, makeTestSession("test_agent"))

	status, body := deleteJSON(t, app, "/api/sessions/test_agent")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["stopped"] != true {
		t.Fatalf("stopped = %#v, want True", body["stopped"])
	}

	status, _, _ = getJSON(t, app, "/api/sessions/test_agent")
	if status != 404 {
		t.Fatalf("GET after stop = %d, want 404", status)
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_stop_session_not_found
func TestStopSessionNotFound(t *testing.T) {
	app := newEmptyAppWithHome(t)

	status, _ := deleteJSON(t, app, "/api/sessions/nonexistent")
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_session_stats
func TestSessionStats(t *testing.T) {
	session := makeTestSession("test_agent")
	session.Colony = &fakeColony{streamID: "colony_a", stats: map[string]any{
		"running":    true,
		"executions": 1,
	}}
	app := newAppWithSession(t, session)

	status, body, _ := getJSON(t, app, "/api/sessions/test_agent/stats")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["running"] != true {
		t.Fatalf("running = %#v, want True", body["running"])
	}
	if body["executions"] != float64(1) {
		t.Fatalf("executions = %#v, want 1", body["executions"])
	}
}

// Parity-exempt: SessionManager.is_loading answers 202 with the loading flag;
func TestGetSessionLoading(t *testing.T) {
	manager := NewSessionManager()
	manager.MarkLoading("cold_one")
	app := newAppForTest(t, AppDeps{Manager: manager})

	status, body, _ := getJSON(t, app, "/api/sessions/cold_one")
	if status != 202 {
		t.Fatalf("status = %d, want 202", status)
	}
	if body["session_id"] != "cold_one" || body["loading"] != true {
		t.Fatalf("body = %#v, want {session_id: cold_one, loading: true}", body)
	}
}

// Parity-exempt: get_cold_session_info's on-disk metadata projection has no
func TestGetSessionCold(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp home: %v", err)
	}
	queenDir := filepath.Join(home.QueensDir(), "default", "sessions", "cold_one")
	partsDir := filepath.Join(queenDir, "conversations", "parts")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatalf("mkdir parts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "0000000001.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write part: %v", err)
	}
	meta := map[string]any{
		"created_at":        1700000000.5,
		"agent_name":        "researcher",
		"agent_path":        "/tmp/e2e/researcher",
		"colony_spawned":    true,
		"spawned_colony_id": "colony_b",
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "meta.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	app := newAppForTest(t, AppDeps{Manager: NewSessionManager(), Home: home})
	status, body, _ := getJSON(t, app, "/api/sessions/cold_one")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["cold"] != true || body["live"] != false {
		t.Fatalf("cold/live = %#v/%#v, want True/False", body["cold"], body["live"])
	}
	if body["has_messages"] != true {
		t.Fatalf("has_messages = %#v, want True", body["has_messages"])
	}
	if body["created_at"] != 1700000000.5 {
		t.Fatalf("created_at = %#v, want the meta.json override", body["created_at"])
	}
	if body["agent_name"] != "researcher" || body["agent_path"] != "/tmp/e2e/researcher" {
		t.Fatalf("agent_name/agent_path = %#v/%#v", body["agent_name"], body["agent_path"])
	}
	if body["colony_spawned"] != true || body["spawned_colony_id"] != "colony_b" {
		t.Fatalf("colony fields = %#v/%#v", body["colony_spawned"], body["spawned_colony_id"])
	}
}

// Parity-exempt: the entry_points projection carries next_fire_in/next_fire_at
func TestGetSessionEntryPoints(t *testing.T) {
	session := makeTestSession("test_agent")
	session.TriggerClock = fakeTriggerClock{mono: 1000.0, wall: 5000.0}
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "daily",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"cron": "0 5 * * *"},
		Description:   "Daily check",
		Task:          "Run task",
	})
	session.SetTriggerNextFire("daily", 1030.5)
	lastFired := int64(1700000000000)
	session.RecordTriggerFire("daily", triggers.TriggerFireStat{
		FireCount:   3,
		LastFiredAt: &lastFired,
	})
	app := newAppWithSession(t, session)

	status, body, _ := getJSON(t, app, "/api/sessions/test_agent")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	entries, ok := body["entry_points"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entry_points = %#v, want one entry", body["entry_points"])
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("entry = %#v", entries[0])
	}
	if entry["id"] != "daily" || entry["name"] != "Daily check" || entry["entry_node"] != "" {
		t.Fatalf("identity fields = %#v", entry)
	}
	if entry["trigger_type"] != "timer" || entry["task"] != "Run task" {
		t.Fatalf("trigger fields = %#v", entry)
	}
	config, ok := entry["trigger_config"].(map[string]any)
	if !ok || config["cron"] != "0 5 * * *" {
		t.Fatalf("trigger_config = %#v", entry["trigger_config"])
	}
	if _, nested := config["next_fire_in"]; nested {
		t.Fatalf("next_fire_in leaked into trigger_config: %#v", config)
	}
	if entry["next_fire_in"] != 30.5 {
		t.Fatalf("next_fire_in = %#v, want 30.5", entry["next_fire_in"])
	}
	if entry["next_fire_at"] != float64(5030500) {
		t.Fatalf("next_fire_at = %#v, want 5030500", entry["next_fire_at"])
	}
	if entry["fire_count"] != float64(3) || entry["last_fired_at"] != float64(1700000000000) {
		t.Fatalf("fire stats = %#v/%#v", entry["fire_count"], entry["last_fired_at"])
	}
}

// Parity-exempt: handle_list_triggers wraps build_trigger_view; the Python
func TestListSessionTriggersRoute(t *testing.T) {
	session := makeTestSession("test_agent")
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "daily",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"cron": "0 5 * * *"},
		Task:          "Run task",
	})
	session.ActivateTrigger("daily")
	app := newAppWithSession(t, session)

	status, body, _ := getJSON(t, app, "/api/sessions/test_agent/triggers")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	list, ok := body["triggers"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("triggers = %#v, want one entry", body["triggers"])
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("trigger entry = %#v", list[0])
	}
	if entry["trigger_id"] != "daily" || entry["name"] != "daily" {
		t.Fatalf("identity fields = %#v", entry)
	}
	if entry["enabled"] != true {
		t.Fatalf("enabled = %#v, want True", entry["enabled"])
	}
}

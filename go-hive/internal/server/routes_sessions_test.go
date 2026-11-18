package server

import (
	"testing"
	"time"
)

type fakePhaseState struct {
	phase   string
	queenID string
	profile map[string]any
}

func (f fakePhaseState) Phase() string                { return f.phase }
func (f fakePhaseState) QueenID() string              { return f.queenID }
func (f fakePhaseState) QueenProfile() map[string]any { return f.profile }

type fakeQueenModel struct{ name string }

func (f fakeQueenModel) ModelName() string { return f.name }

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_list_sessions_empty
func TestListLiveSessionsEmpty(t *testing.T) {
	app := newAppForTest(t, AppDeps{})

	status, body, headers := getJSON(t, app, "/api/sessions")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", headers.Get("Content-Type"))
	}
	sessions, ok := body["sessions"].([]any)
	if !ok {
		t.Fatalf("sessions = %#v, want a list", body["sessions"])
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v, want []", sessions)
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_list_sessions_with_loaded
func TestListLiveSessionsWithLoaded(t *testing.T) {
	session := NewSession("test_agent", nil)
	session.LoadedAt = float64(time.Now().UnixNano()) / 1e9
	app := newAppForTest(t, AppDeps{
		LiveSessions: func() []*Session { return []*Session{session} },
	})

	status, body, _ := getJSON(t, app, "/api/sessions")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	sessions, ok := body["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("sessions = %#v, want one entry", body["sessions"])
	}
	entry, ok := sessions[0].(map[string]any)
	if !ok {
		t.Fatalf("session entry = %#v", sessions[0])
	}
	if entry["session_id"] != "test_agent" {
		t.Fatalf("session_id = %#v, want test_agent", entry["session_id"])
	}
}

// Parity-exempt: exercises the full _session_to_live_dict field set, which the
func TestSessionToLiveDictFields(t *testing.T) {
	loadedAt := float64(time.Now().UnixNano()) / 1e9
	session := NewSession("s1", nil)
	session.ColonyID = "colony_a"
	session.WorkerPath = "/tmp/colonies/colony_a"
	session.LoadedAt = loadedAt
	session.ColonySpawned = true
	session.SpawnedColonyID = "colony_b"
	session.LLM = fakeQueenModel{name: "openai/gpt-oss-120b"}
	session.PhaseState = fakePhaseState{
		phase:   "running",
		queenID: "queen-1",
		profile: map[string]any{"name": "Ada"},
	}

	entry := sessionToLiveDict(session)

	expected := map[string]any{
		"session_id":            "s1",
		"colony_id":             "colony_a",
		"has_worker":            true,
		"agent_path":            "/tmp/colonies/colony_a",
		"description":           "",
		"goal":                  "",
		"node_count":            0,
		"loaded_at":             loadedAt,
		"queen_phase":           "running",
		"queen_supports_images": false,
		"queen_id":              "queen-1",
		"queen_name":            "Ada",
		"colony_spawned":        true,
		"spawned_colony_id":     "colony_b",
	}
	for key, want := range expected {
		if got := entry[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	uptime, ok := entry["uptime_seconds"].(float64)
	if !ok || uptime < 0 || uptime > 5 {
		t.Errorf("uptime_seconds = %#v, want a small non-negative value", entry["uptime_seconds"])
	}
}

// Parity-exempt: the queen-only defaults of _session_to_live_dict (no colony,
func TestSessionToLiveDictQueenOnlyDefaults(t *testing.T) {
	session := NewSession("q1", nil)
	session.LoadedAt = float64(time.Now().UnixNano()) / 1e9

	entry := sessionToLiveDict(session)

	if entry["colony_id"] != nil {
		t.Errorf("colony_id = %#v, want None", entry["colony_id"])
	}
	if entry["has_worker"] != false {
		t.Errorf("has_worker = %#v, want False", entry["has_worker"])
	}
	if entry["agent_path"] != "" {
		t.Errorf("agent_path = %#v, want empty", entry["agent_path"])
	}
	if entry["queen_phase"] != "planning" {
		t.Errorf("queen_phase = %#v, want planning", entry["queen_phase"])
	}
	if entry["queen_supports_images"] != true {
		t.Errorf("queen_supports_images = %#v, want True for an empty model", entry["queen_supports_images"])
	}
	if entry["queen_id"] != nil || entry["queen_name"] != nil {
		t.Errorf("queen_id/queen_name = %#v/%#v, want None/None", entry["queen_id"], entry["queen_name"])
	}
	if entry["colony_spawned"] != false {
		t.Errorf("colony_spawned = %#v, want False", entry["colony_spawned"])
	}
	if entry["spawned_colony_id"] != nil {
		t.Errorf("spawned_colony_id = %#v, want None", entry["spawned_colony_id"])
	}
}

// Parity-exempt: colony-loaded default phase without a phase state mirrors
func TestSessionToLiveDictColonyWithoutPhaseState(t *testing.T) {
	session := NewSession("s2", nil)
	session.ColonyID = "colony_a"
	session.LoadedAt = float64(time.Now().UnixNano()) / 1e9

	entry := sessionToLiveDict(session)
	if entry["queen_phase"] != "staging" {
		t.Errorf("queen_phase = %#v, want staging", entry["queen_phase"])
	}
	if entry["queen_id"] != nil || entry["queen_name"] != nil {
		t.Errorf("queen_id/queen_name = %#v/%#v, want None/None", entry["queen_id"], entry["queen_name"])
	}
}

// Parity-exempt: SessionManager._sessions is a Python dict, so insertion order
func TestSessionManagerListSessionsOrder(t *testing.T) {
	manager := NewSessionManager()
	first := NewSession("first", nil)
	second := NewSession("second", nil)
	manager.AddSession(first)
	manager.AddSession(second)

	listed := manager.ListSessions()
	if len(listed) != 2 || listed[0].ID != "first" || listed[1].ID != "second" {
		t.Fatalf("ListSessions = %v, want [first second]", sessionIDs(listed))
	}

	replaced := NewSession("first", nil)
	manager.AddSession(replaced)
	listed = manager.ListSessions()
	if len(listed) != 2 || listed[0] != replaced || listed[1] != second {
		t.Fatalf("ListSessions after replace = %v", sessionIDs(listed))
	}

	if !manager.StopSession("first") {
		t.Fatal("StopSession(first) = false, want true")
	}
	listed = manager.ListSessions()
	if len(listed) != 1 || listed[0] != second {
		t.Fatalf("ListSessions after stop = %v, want [second]", sessionIDs(listed))
	}
	if manager.StopSession("first") {
		t.Fatal("StopSession on a removed session = true, want false")
	}
}

func sessionIDs(sessions []*Session) []string {
	out := make([]string, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, session.ID)
	}
	return out
}

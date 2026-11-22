package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"gopkg.in/yaml.v3"
)

func queenTestHome(t *testing.T) hivehome.Home {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	if err := os.MkdirAll(home.QueensDir(), 0o755); err != nil {
		t.Fatalf("mkdir queens: %v", err)
	}
	return home
}

func queenTestWriteProfile(t *testing.T, home hivehome.Home, queenID string, profile map[string]any) string {
	t.Helper()
	dir := home.QueenDir(queenID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir queen: %v", err)
	}
	data, err := yaml.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	path := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return path
}

func queenTestWriteSession(t *testing.T, home hivehome.Home, queenID, sessionID string, meta map[string]any) string {
	t.Helper()
	dir := home.QueenSessionDir(queenID, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if meta != nil {
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("marshal meta: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	return dir
}

func queenTestMux(home hivehome.Home, manager *SessionManager, mutate func(*QueenRouteDeps)) *http.ServeMux {
	deps := QueenRouteDeps{Home: home, Manager: manager}
	if mutate != nil {
		mutate(&deps)
	}
	mux := http.NewServeMux()
	RegisterQueenRoutes(mux, deps)
	return mux
}

type queenCreateRecorder struct {
	calls []QueenSessionCreateParams
	reply func(QueenSessionCreateParams) *Session
}

func (r *queenCreateRecorder) hook(params QueenSessionCreateParams) (*Session, error) {
	r.calls = append(r.calls, params)
	if r.reply != nil {
		return r.reply(params), nil
	}
	id := "session_" + strings.ReplaceAll(ptrString(params.QueenName), "queen_", "")
	if params.QueenResumeFrom != nil {
		id = *params.QueenResumeFrom
	}
	return &Session{ID: id, QueenName: ptrString(params.QueenName)}, nil
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func queenTestStringList(t *testing.T, value any) []string {
	t.Helper()
	if value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("not a list: %#v", value)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(string))
	}
	return out
}

// Parity: framework/server/tests/test_create_queen.py::test_create_queen_returns_the_new_queen_and_syncs_it
func TestQueenCreateReturnsTheNewQueenAndSyncsIt(t *testing.T) {
	home := queenTestHome(t)
	pushed := []string{}
	mux := queenTestMux(home, nil, func(deps *QueenRouteDeps) {
		deps.SchedulePush = func(kind, key string) { pushed = append(pushed, kind+"|"+key) }
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/create", `{"name":"Ada Lovelace","queen_id":"ada"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	entry, _ := body["queen"].(map[string]any)
	if entry["id"] != "ada" {
		t.Fatalf("queen = %#v", body["queen"])
	}
	if !reflect.DeepEqual(pushed, []string{"queen_profile|ada"}) {
		t.Fatalf("pushed = %#v", pushed)
	}
	if _, err := os.Stat(filepath.Join(home.QueenDir("ada"), "profile.yaml")); err != nil {
		t.Fatalf("profile.yaml missing: %v", err)
	}
}

// Parity: framework/server/tests/test_queen_catalog.py::test_a_removed_queen_leaves_the_catalog_and_comes_back_as_she_was
func TestARemovedQueenLeavesTheCatalogAndComesBackAsSheWas(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_mira", map[string]any{"name": "Mira", "title": "Cartographer"})
	mux := queenTestMux(home, nil, nil)

	if recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_legal/remove", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove legal status = %d", recorder.Code)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_mira/remove", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove mira status = %d", recorder.Code)
	}
	_, listed := doSessionJSON(t, mux, http.MethodGet, "/api/queen/profiles", "")
	listedIDs := queenIDs(t, listed)
	if listedIDs["queen_legal"] || listedIDs["queen_mira"] {
		t.Fatalf("removed queens still listed: %#v", listedIDs)
	}
	_, offered := doSessionJSON(t, mux, http.MethodGet, "/api/queen/removed", "")
	offeredIDs := queenIDs(t, offered)
	if len(offeredIDs) != 2 || !offeredIDs["queen_legal"] || !offeredIDs["queen_mira"] {
		t.Fatalf("offered = %#v", offeredIDs)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_mira/restore", ""); recorder.Code != http.StatusOK {
		t.Fatalf("restore status = %d", recorder.Code)
	}
	_, after := doSessionJSON(t, mux, http.MethodGet, "/api/queen/profiles", "")
	afterIDs := queenIDs(t, after)
	if !afterIDs["queen_mira"] || afterIDs["queen_legal"] {
		t.Fatalf("after = %#v", afterIDs)
	}
	if _, err := os.Stat(filepath.Join(home.QueenDir("queen_mira"), "profile.yaml")); err != nil {
		t.Fatalf("removing touched her files: %v", err)
	}
}

func queenIDs(t *testing.T, body map[string]any) map[string]bool {
	t.Helper()
	entries, ok := body["queens"].([]any)
	if !ok {
		t.Fatalf("queens = %#v", body["queens"])
	}
	out := map[string]bool{}
	for _, entry := range entries {
		object := entry.(map[string]any)
		out[object["id"].(string)] = true
	}
	return out
}

// Parity: framework/server/tests/test_queen_catalog.py::test_a_queen_hive_doesnt_know_cant_be_removed
func TestAQueenHiveDoesntKnowCantBeRemoved(t *testing.T) {
	home := queenTestHome(t)
	mux := queenTestMux(home, nil, nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_nobody/remove", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(home.QueensDir(), "catalog.json")); err == nil {
		t.Fatal("catalog.json must not be created for an unknown queen")
	}
}

var queenPersonaFixture = map[string]any{
	"name":        "Fox",
	"title":       "The painter",
	"core_traits": "<voice>\nShort lines.\n</voice>\n",
	"skills":      "drawing, teasing",
	"hidden_background": map[string]any{
		"past_wound":  "lost a sketchbook",
		"deep_motive": "to be remembered",
	},
}

// Parity: framework/server/tests/test_queen_persona.py::test_reading_her_persona_gives_the_fields_as_written
func TestReadingHerPersonaGivesTheFieldsAsWritten(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_x", queenPersonaFixture)
	mux := queenTestMux(home, nil, nil)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/persona", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !reflect.DeepEqual(body["persona"], queenPersonaFixture) {
		t.Fatalf("persona = %#v, want %#v", body["persona"], queenPersonaFixture)
	}
	missing, _ := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_nobody/persona", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
}

// Parity: framework/server/tests/test_queen_persona.py::test_replacing_fields_keeps_the_file_readable_and_a_snapshot
func TestReplacingFieldsKeepsTheFileReadableAndASnapshot(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_x", queenPersonaFixture)
	mux := queenTestMux(home, nil, nil)
	traits := "<voice>  \nShort lines.\nNo exclamation marks.  \n</voice>\n"

	recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_x/persona",
		`{"persona":{"core_traits":`+mustJSONString(t, traits)+`,"hidden_background":null}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	persona := body["persona"].(map[string]any)
	if persona["core_traits"] != "<voice>\nShort lines.\nNo exclamation marks.\n</voice>\n" {
		t.Fatalf("core_traits = %#v", persona["core_traits"])
	}
	if _, ok := persona["hidden_background"]; ok {
		t.Fatalf("hidden_background survived: %#v", persona)
	}
	text, err := os.ReadFile(filepath.Join(home.QueenDir("queen_x"), "profile.yaml"))
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if !strings.Contains(string(text), "core_traits: |\n  <voice>\n  Short lines.\n") {
		t.Fatalf("profile.yaml lost its literal block:\n%s", text)
	}
	matches, err := filepath.Glob(filepath.Join(home.QueenDir("queen_x"), "_profile_snapshots", "profile.yaml.*.pre-dashboard-edit"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("snapshots = %#v (%v)", matches, err)
	}
	snapshot, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var restored map[string]any
	if err := yaml.Unmarshal(snapshot, &restored); err != nil {
		t.Fatalf("snapshot is not YAML: %v", err)
	}
	if !reflect.DeepEqual(restored, queenPersonaFixture) {
		t.Fatalf("snapshot = %#v", restored)
	}
}

// mustJSONString marshals value for embedding in a JSON string literal.
// Distinct from routes_colonies_test.go's mustJSON, which returns []byte.
func mustJSONString(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

// Parity: framework/server/tests/test_queen_persona.py::test_a_bad_field_is_refused_and_nothing_is_written
func TestABadPersonaFieldIsRefusedAndNothingIsWritten(t *testing.T) {
	cases := []struct {
		persona string
		error   string
	}{
		{`{"name":"  "}`, "'name' can't be empty"},
		{`{"core_traits":3}`, "'core_traits' must be text"},
		{`{"world_lore":{"habitat":1}}`, "'world_lore' must map names to text"},
		{`{"behavior_triggers":"often"}`, "'behavior_triggers' must be a list of {trigger, reaction} texts"},
		{`{"behavior_triggers":[{"trigger":"praise"}]}`, "'behavior_triggers' must be a list of {trigger, reaction} texts"},
		{`{"voice":"x"}`, "Unknown persona field 'voice'"},
	}
	for _, testCase := range cases {
		home := queenTestHome(t)
		path := queenTestWriteProfile(t, home, "queen_x", queenPersonaFixture)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read profile: %v", err)
		}
		mux := queenTestMux(home, nil, nil)
		recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_x/persona", `{"persona":`+testCase.persona+`}`)
		if recorder.Code != http.StatusBadRequest || body["error"] != testCase.error {
			t.Fatalf("%s: status = %d, error = %#v", testCase.persona, recorder.Code, body["error"])
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read profile: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s: profile changed", testCase.persona)
		}
	}
}

// Parity: framework/server/tests/test_queen_persona.py::test_the_overview_counts_her_memories_and_sessions
func TestTheOverviewCountsHerMemoriesAndSessions(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_x", queenPersonaFixture)
	memoryDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	for name, kind := range map[string]string{"a.md": "preference", "b.md": "preference", "c.md": "feedback"} {
		content := "---\nname: " + name + "\ndescription: d\ntype: " + kind + "\n---\nbody"
		if err := os.WriteFile(filepath.Join(memoryDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write memory: %v", err)
		}
	}
	for _, session := range []string{"session_20260710_014648_aaaa1111", "session_20261020_013004_bbbb2222"} {
		dir := filepath.Join(home.QueenDir("queen_x"), "sessions", session)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir session: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{\"type\":\"x\"}\n"), 0o644); err != nil {
			t.Fatalf("write events: %v", err)
		}
	}
	mux := queenTestMux(home, nil, func(deps *QueenRouteDeps) {
		deps.MemoryDir = func(string) string { return memoryDir }
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/overview", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	memories := body["memory"].(map[string]any)
	if memories["count"] != float64(3) || memories["limit"] != float64(200) {
		t.Fatalf("memory = %#v", memories)
	}
	byType := memories["by_type"].(map[string]any)
	if byType["preference"] != float64(2) || byType["feedback"] != float64(1) {
		t.Fatalf("by_type = %#v", byType)
	}
	sessions := body["sessions"].(map[string]any)
	if sessions["count"] != float64(2) || sessions["first"] != "2026-07-10" {
		t.Fatalf("sessions = %#v", sessions)
	}
	if text, _ := sessions["last_active"].(string); text == "" || strings.Contains(text, ":") == false {
		t.Fatalf("last_active = %#v", sessions["last_active"])
	}
}

// Parity: framework/server/tests/test_queen_models.py::test_the_dashboard_sets_and_clears_her_model_and_her_sessions_follow
func TestTheDashboardSetsAndClearsHerModelAndHerSessionsFollow(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_growth", map[string]any{"name": "Growth", "title": "Head of Growth"})
	queenTestWriteProfile(t, home, "queen_sales", map[string]any{"name": "Sales", "title": "Head of Sales"})
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"provider": "anthropic", "model": "claude-x", "api_key": "sk-main"}})

	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "g1", QueenName: "queen_growth", LLM: "hers"})
	manager.AddSession(&Session{ID: "s1", QueenName: "queen_sales", LLM: "other"})
	checked := 0
	applied := []string{}
	mux := queenTestMux(home, manager, func(deps *QueenRouteDeps) {
		deps.DefaultModel = func() string { return "anthropic/claude-x" }
		deps.ValidateProviderKey = func(provider, apiKey, apiBase, model string) (bool, string) {
			checked++
			return true, ""
		}
		deps.ApplyQueenModel = func(session *Session, queenID string, defaultModel string) bool {
			applied = append(applied, session.ID+"|"+queenID+"|"+defaultModel)
			return true
		}
	})

	_, before := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_growth/llm", "")
	if before["section"] != nil || before["default_model"] != "anthropic/claude-x" {
		t.Fatalf("before = %#v", before)
	}
	own := map[string]any{"provider": "openai", "model": "archivist-1", "api_base": "https://models.invalid/v1", "api_key": "sk-own"}
	recorder, setBody := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_growth/llm", `{"section":`+mustJSONString(t, own)+`}`)
	if recorder.Code != http.StatusOK || setBody["sessions_switched"] != float64(1) {
		t.Fatalf("set status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if checked != 1 {
		t.Fatalf("validate called %d times", checked)
	}
	if !reflect.DeepEqual(applied, []string{"g1|queen_growth|anthropic/claude-x"}) {
		t.Fatalf("applied = %#v", applied)
	}
	_, after := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_growth/llm", "")
	if !reflect.DeepEqual(after["section"], own) {
		t.Fatalf("after = %#v", after)
	}
	_, cleared := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_growth/llm", `{"section":null}`)
	if cleared["section"] != nil {
		t.Fatalf("cleared = %#v", cleared)
	}
	config := readFeatureConfig(t, home)
	if _, ok := config["queen_llm"]; ok {
		t.Fatalf("queen_llm survived: %#v", config)
	}
}

// Parity: framework/server/tests/test_queen_models.py::test_a_bad_model_is_refused_and_nothing_is_written
func TestABadQueenModelIsRefusedAndNothingIsWritten(t *testing.T) {
	cases := []struct {
		queen  string
		body   string
		status int
		error  string
	}{
		{"queen_growth", `{"section":{"provider":"openai"}}`, http.StatusBadRequest, "'provider' and 'model' are required"},
		{"queen_growth", `{"section":"openai/x"}`, http.StatusBadRequest, "must be a JSON object or null"},
		{"queen_nobody", `{"section":{"provider":"openai","model":"m"}}`, http.StatusNotFound, "not found"},
		{"queen_growth", `{"section":{"provider":"openai","model":"m","api_base":"https://models.invalid/v1","api_key":"sk"}}`, http.StatusBadRequest, "Key check failed"},
	}
	for _, testCase := range cases {
		home := queenTestHome(t)
		queenTestWriteProfile(t, home, "queen_growth", map[string]any{"name": "Growth", "title": "Head of Growth"})
		writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"provider": "anthropic", "model": "claude-x"}})
		mux := queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) {
			deps.ValidateProviderKey = func(provider, apiKey, apiBase, model string) (bool, string) {
				return false, "401"
			}
		})
		recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/queen/"+testCase.queen+"/llm", testCase.body)
		if recorder.Code != testCase.status || !strings.Contains(queenStr(body["error"]), testCase.error) {
			t.Fatalf("%s: status = %d, error = %#v", testCase.body, recorder.Code, body["error"])
		}
		config := readFeatureConfig(t, home)
		if _, ok := config["queen_llm"]; ok {
			t.Fatalf("%s: queen_llm was written", testCase.body)
		}
	}
}

// Parity: framework/server/tests/test_api.py::test_select_queen_session_rejects_foreign_session
func TestSelectQueenSessionRejectsForeignSession(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_growth", "other_session", map[string]any{"queen_id": "queen_growth"})
	mux := queenTestMux(home, NewSessionManager(), nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"other_session"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// Parity: framework/server/tests/test_api.py::test_select_queen_session_returns_live_session_without_duplication
func TestSelectQueenSessionReturnsLiveSessionWithoutDuplication(t *testing.T) {
	home := queenTestHome(t)
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "queen_live", QueenName: "queen_technology"})
	manager.AddSession(&Session{ID: "other_live", QueenName: "queen_growth"})
	recorder, body := doSessionJSON(t,
		queenTestMux(home, manager, nil), http.MethodPost,
		"/api/queen/queen_technology/session/select", `{"session_id":"queen_live"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := map[string]any{"session_id": "queen_live", "queen_id": "queen_technology", "status": "live"}
	if !reflect.DeepEqual(body, expected) {
		t.Fatalf("body = %#v", body)
	}
	if manager.GetSession("other_live") == nil {
		t.Fatal("another queen's live session was stopped")
	}
}

// Parity: framework/server/tests/test_api.py::test_select_queen_session_restores_specific_history_session
func TestSelectQueenSessionRestoresSpecificHistorySession(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_technology", "queen_history", map[string]any{"queen_id": "queen_technology"})
	recorder0 := &queenCreateRecorder{reply: func(QueenSessionCreateParams) *Session {
		return &Session{ID: "queen_history", QueenName: "queen_technology"}
	}}
	mux := queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"queen_history"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := map[string]any{"session_id": "queen_history", "queen_id": "queen_technology", "status": "resumed"}
	if !reflect.DeepEqual(body, expected) {
		t.Fatalf("body = %#v", body)
	}
	if len(recorder0.calls) != 1 {
		t.Fatalf("create calls = %d", len(recorder0.calls))
	}
	call := recorder0.calls[0]
	if call.ColonyID != nil || call.QueenResumeFrom == nil || *call.QueenResumeFrom != "queen_history" ||
		call.InitialPrompt != nil || ptrString(call.QueenName) != "queen_technology" || call.InitialPhase != "independent" {
		t.Fatalf("params = %#v", call)
	}
}

// Parity: framework/server/tests/test_api.py::test_select_queen_session_restores_worker_backed_history
func TestSelectQueenSessionRestoresWorkerBackedHistory(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_technology", "worker_history", map[string]any{
		"queen_id":   "queen_technology",
		"agent_path": "/tmp/example-agent",
	})
	recorder0 := &queenCreateRecorder{reply: func(QueenSessionCreateParams) *Session {
		return &Session{ID: "worker_history", QueenName: "queen_technology"}
	}}
	mux := queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"worker_history"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["status"] != "resumed" {
		t.Fatalf("body = %#v", body)
	}
	call := recorder0.calls[0]
	if call.ColonyID == nil || *call.ColonyID != "example-agent" || call.InitialPhase != "colony" {
		t.Fatalf("params = %#v", call)
	}
}

// Parity: framework/server/tests/test_api.py::test_new_queen_session_creates_fresh_thread
func TestNewQueenSessionCreatesFreshThread(t *testing.T) {
	home := queenTestHome(t)
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "old_live", QueenName: "queen_growth"})
	recorder0 := &queenCreateRecorder{reply: func(QueenSessionCreateParams) *Session {
		return &Session{ID: "fresh_thread", QueenName: "queen_technology"}
	}}
	mux := queenTestMux(home, manager, func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session/new", `{"initial_phase":"independent"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := map[string]any{"session_id": "fresh_thread", "queen_id": "queen_technology", "status": "created"}
	if !reflect.DeepEqual(body, expected) {
		t.Fatalf("body = %#v", body)
	}
	if manager.GetSession("old_live") == nil {
		t.Fatal("another queen's live session was stopped")
	}
	if len(recorder0.calls) != 1 {
		t.Fatalf("create calls = %d", len(recorder0.calls))
	}
	call := recorder0.calls[0]
	if call.InitialPrompt != nil || ptrString(call.QueenName) != "queen_technology" || call.InitialPhase != "independent" || call.CRMSetup {
		t.Fatalf("params = %#v", call)
	}
}

// Parity: framework/server/tests/test_session_supersession.py::test_select_follows_supersession_chain_to_cold_successor
func TestSelectFollowsSupersessionChainToColdSuccessor(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_technology", "sess_a", map[string]any{"queen_id": "queen_technology", "superseded_by": "sess_b"})
	queenTestWriteSession(t, home, "queen_technology", "sess_b", map[string]any{"queen_id": "queen_technology", "superseded_by": "sess_c"})
	queenTestWriteSession(t, home, "queen_technology", "sess_c", map[string]any{"queen_id": "queen_technology"})
	recorder0 := &queenCreateRecorder{reply: func(QueenSessionCreateParams) *Session {
		return &Session{ID: "sess_c", QueenName: "queen_technology"}
	}}
	mux := queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"sess_a"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["session_id"] != "sess_c" || body["status"] != "resumed" {
		t.Fatalf("body = %#v", body)
	}
	call := recorder0.calls[0]
	if call.QueenResumeFrom == nil || *call.QueenResumeFrom != "sess_c" || call.InitialPhase != "independent" || call.ColonyID != nil {
		t.Fatalf("params = %#v", call)
	}
}

// Parity: framework/server/tests/test_session_supersession.py::test_select_follows_supersession_chain_to_live_successor
func TestSelectFollowsSupersessionChainToLiveSuccessor(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_technology", "sess_a", map[string]any{"queen_id": "queen_technology", "superseded_by": "sess_b"})
	queenTestWriteSession(t, home, "queen_technology", "sess_b", map[string]any{"queen_id": "queen_technology", "superseded_by": "sess_c"})
	queenTestWriteSession(t, home, "queen_technology", "sess_c", map[string]any{"queen_id": "queen_technology"})
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "sess_c", QueenName: "queen_technology"})
	recorder0 := &queenCreateRecorder{}
	recorder, body := doSessionJSON(t,
		queenTestMux(home, manager, func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook }),
		http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"sess_a"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := map[string]any{"session_id": "sess_c", "queen_id": "queen_technology", "status": "live"}
	if !reflect.DeepEqual(body, expected) {
		t.Fatalf("body = %#v", body)
	}
	if len(recorder0.calls) != 0 {
		t.Fatalf("minted a duplicate: %#v", recorder0.calls)
	}
}

// Parity: framework/server/tests/test_session_supersession.py::test_select_non_superseded_session_is_unaffected
func TestSelectNonSupersededSessionIsUnaffected(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_technology", "sess_plain", map[string]any{"queen_id": "queen_technology"})
	recorder0 := &queenCreateRecorder{reply: func(QueenSessionCreateParams) *Session {
		return &Session{ID: "sess_plain", QueenName: "queen_technology"}
	}}
	mux := queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"sess_plain"}`)
	if recorder.Code != http.StatusOK || body["session_id"] != "sess_plain" {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	if call := recorder0.calls[0]; call.QueenResumeFrom == nil || *call.QueenResumeFrom != "sess_plain" {
		t.Fatalf("params = %#v", call)
	}
}

// Parity: framework/server/tests/test_select_colony_redirect.py::test_select_cold_colony_session_redirects_without_resuming
func TestSelectColdColonySessionRedirectsWithoutResuming(t *testing.T) {
	home := queenTestHome(t)
	const sessionID = "session_20260719_121546_afe89313"
	if err := os.MkdirAll(home.ColonyQueenSessionDir("email_reply", "queen_technology", sessionID), 0o755); err != nil {
		t.Fatalf("mkdir colony session: %v", err)
	}
	recorder0 := &queenCreateRecorder{}
	recorder, body := doSessionJSON(t,
		queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook }),
		http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"`+sessionID+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := map[string]any{"session_id": sessionID, "queen_id": "queen_technology", "status": "colony", "colony_id": "email_reply"}
	if !reflect.DeepEqual(body, expected) {
		t.Fatalf("body = %#v", body)
	}
	if len(recorder0.calls) != 0 {
		t.Fatalf("resumed a colony session: %#v", recorder0.calls)
	}
	if _, err := os.Stat(home.QueenSessionDir("queen_technology", sessionID)); err == nil {
		t.Fatal("DM shadow dir was created")
	}
}

// Parity: framework/server/tests/test_select_colony_redirect.py::test_select_live_colony_session_redirects
func TestSelectLiveColonySessionRedirects(t *testing.T) {
	home := queenTestHome(t)
	const sessionID = "session_20260719_121546_afe89313"
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: sessionID, QueenName: "queen_technology", ColonyID: "email_reply"})
	recorder0 := &queenCreateRecorder{}
	recorder, body := doSessionJSON(t,
		queenTestMux(home, manager, func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook }),
		http.MethodPost, "/api/queen/queen_technology/session/select", `{"session_id":"`+sessionID+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := map[string]any{"session_id": sessionID, "queen_id": "queen_technology", "status": "colony", "colony_id": "email_reply"}
	if !reflect.DeepEqual(body, expected) {
		t.Fatalf("body = %#v", body)
	}
	if len(recorder0.calls) != 0 {
		t.Fatalf("resumed a colony session: %#v", recorder0.calls)
	}
}

// Parity: framework/server/tests/test_session_supersession.py::test_queen_session_skips_superseded_on_cold_resume
func TestQueenSessionSkipsSupersededOnColdResume(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_technology", "sess_new", map[string]any{"queen_id": "queen_technology"})
	queenTestWriteSession(t, home, "queen_technology", "sess_old", map[string]any{"queen_id": "queen_technology", "superseded_by": "sess_new"})
	recorder0 := &queenCreateRecorder{reply: func(QueenSessionCreateParams) *Session {
		return &Session{ID: "sess_new", QueenName: "queen_technology"}
	}}
	mux := queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["session_id"] != "sess_new" || body["status"] != "resumed" {
		t.Fatalf("body = %#v", body)
	}
	if call := recorder0.calls[0]; call.QueenResumeFrom == nil || *call.QueenResumeFrom != "sess_new" {
		t.Fatalf("params = %#v", call)
	}
}

// Parity: framework/server/tests/test_session_supersession.py::test_queen_session_skips_the_dm_that_became_a_colony
func TestQueenSessionSkipsTheDMThatBecameAColony(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteSession(t, home, "queen_technology", "session_20261019_090000_aaaa1111", map[string]any{"queen_id": "queen_technology"})
	queenTestWriteSession(t, home, "queen_technology", "session_20261020_090000_bbbb2222", map[string]any{
		"queen_id":       "queen_technology",
		"agent_path":     "/tmp/colonies/atlas",
		"colony_spawned": true,
	})
	recorder0 := &queenCreateRecorder{reply: func(QueenSessionCreateParams) *Session {
		return &Session{ID: "session_20261019_090000_aaaa1111", QueenName: "queen_technology"}
	}}
	mux := queenTestMux(home, NewSessionManager(), func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook })
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session", `{"initial_phase":"independent"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	call := recorder0.calls[0]
	if call.QueenResumeFrom == nil || *call.QueenResumeFrom != "session_20261019_090000_aaaa1111" || call.ColonyID != nil {
		t.Fatalf("params = %#v", call)
	}
}

// Parity-exempt: the live-DM short circuit of POST /api/queen/{id}/session has no Python route test; pins newest-loaded selection.
func TestQueenSessionReturnsTheMostRecentlyLoadedLiveDM(t *testing.T) {
	home := queenTestHome(t)
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "older", QueenName: "queen_technology", LoadedAt: 1})
	manager.AddSession(&Session{ID: "newer", QueenName: "queen_technology", LoadedAt: 5})
	manager.AddSession(&Session{ID: "colony", QueenName: "queen_technology", ColonyID: "atlas", LoadedAt: 9})
	recorder0 := &queenCreateRecorder{}
	recorder, body := doSessionJSON(t,
		queenTestMux(home, manager, func(deps *QueenRouteDeps) { deps.CreateSession = recorder0.hook }),
		http.MethodPost, "/api/queen/queen_technology/session", "")
	if recorder.Code != http.StatusOK || body["session_id"] != "newer" || body["status"] != "live" {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	if len(recorder0.calls) != 0 {
		t.Fatalf("minted a session: %#v", recorder0.calls)
	}
}

// Parity-exempt: PATCH /api/queen/{id}/profile has no Python route test; pins the reverse field transform and the sync push.
func TestQueenProfilePatchMergesAndSyncs(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_x", map[string]any{
		"name": "Fox", "title": "The painter",
		"world_lore": map[string]any{"habitat": "old studio", "lexicon": "color"},
	})
	pushed := []string{}
	refreshed := ""
	mux := queenTestMux(home, nil, func(deps *QueenRouteDeps) {
		deps.SchedulePush = func(kind, key string) { pushed = append(pushed, kind+"|"+key) }
		deps.RefreshQueenIdentity = func(queenID string, profile map[string]any) { refreshed = queenID }
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_x/profile",
		`{"summary":"new traits","signature_achievement":"a finished mural"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["summary"] != "new traits" || body["signature_achievement"] != "a finished mural. color" {
		t.Fatalf("body = %#v", body)
	}
	if !reflect.DeepEqual(pushed, []string{"queen_profile|queen_x"}) || refreshed != "queen_x" {
		t.Fatalf("pushed = %#v, refreshed = %q", pushed, refreshed)
	}
	profile, err := os.ReadFile(filepath.Join(home.QueenDir("queen_x"), "profile.yaml"))
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	var stored map[string]any
	if err := yaml.Unmarshal(profile, &stored); err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	if stored["core_traits"] != "new traits" {
		t.Fatalf("profile = %#v", stored)
	}
	world := stored["world_lore"].(map[string]any)
	if world["habitat"] != "a finished mural" || world["lexicon"] != "color" {
		t.Fatalf("world_lore = %#v", world)
	}
}

// Parity-exempt: POST /api/queen/initialize-preferences has no Python route test; pins the untouched-default overwrite rule.
func TestQueenInitializePreferencesAppliesOverridesOnce(t *testing.T) {
	home := queenTestHome(t)
	mux := queenTestMux(home, nil, nil)
	body := `{"queens":{"queen_legal":{"n":"Legal Eagle","bio":"Contracts first","p":{"kind":"emoji","value":"scale"}},"queen_nobody":{"n":"X"}}}`
	recorder, response := doSessionJSON(t, mux, http.MethodPost, "/api/queen/initialize-preferences", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !reflect.DeepEqual(queenTestStringList(t, response["initialized"]), []string{"queen_legal"}) {
		t.Fatalf("initialized = %#v", response["initialized"])
	}
	if !reflect.DeepEqual(queenTestStringList(t, response["skipped"]), []string{"queen_nobody"}) {
		t.Fatalf("skipped = %#v", response["skipped"])
	}
	profile, err := queenLoadProfile(QueenRouteDeps{Home: home}, "queen_legal")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile["name"] != "Legal Eagle" || profile["core_traits"] != "Contracts first" {
		t.Fatalf("profile = %#v", profile)
	}
	if _, ok := profile["portrait"]; !ok {
		t.Fatalf("portrait missing: %#v", profile)
	}

	again := `{"queens":{"queen_legal":{"n":"Someone Else"}}}`
	_, second := doSessionJSON(t, mux, http.MethodPost, "/api/queen/initialize-preferences", again)
	if !reflect.DeepEqual(queenTestStringList(t, second["skipped"]), []string{"queen_legal"}) {
		t.Fatalf("second = %#v", second)
	}
	profile, _ = queenLoadProfile(QueenRouteDeps{Home: home}, "queen_legal")
	if profile["name"] != "Legal Eagle" {
		t.Fatalf("user edit overwritten: %#v", profile)
	}
}

// Parity-exempt: the avatar routes have no Python route test; pins the JPEG/PNG allowlist, size cap, caching and idempotent delete.
func TestQueenAvatarUploadGetDelete(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_x", map[string]any{"name": "Fox", "title": "Painter"})
	mux := queenTestMux(home, nil, nil)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreatePart(mapHeader("form-data; name=\"avatar\"; filename=\"a.jpg\"", "image/jpeg"))
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}); err != nil {
		t.Fatalf("write part: %v", err)
	}
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/queen/queen_x/avatar", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/queen/queen_x/avatar", nil))
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("get = %d %q", get.Code, get.Header().Get("Cache-Control"))
	}
	if !bytes.Equal(get.Body.Bytes(), []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}) {
		t.Fatalf("bytes = %v", get.Body.Bytes())
	}

	bad := &bytes.Buffer{}
	badWriter := multipart.NewWriter(bad)
	badPart, _ := badWriter.CreatePart(mapHeader("form-data; name=\"avatar\"; filename=\"a.gif\"", "image/gif"))
	badPart.Write([]byte("GIF89a"))
	badWriter.Close()
	badRequest := httptest.NewRequest(http.MethodPost, "/api/queen/queen_x/avatar", bad)
	badRequest.Header.Set("Content-Type", badWriter.FormDataContentType())
	badRecorder := httptest.NewRecorder()
	mux.ServeHTTP(badRecorder, badRequest)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("bad type status = %d", badRecorder.Code)
	}

	del, delBody := doSessionJSON(t, mux, http.MethodDelete, "/api/queen/queen_x/avatar", "")
	if del.Code != http.StatusOK || delBody["has_avatar"] != false {
		t.Fatalf("delete = %d %#v", del.Code, delBody)
	}
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/queen/queen_x/avatar", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing avatar status = %d", missing.Code)
	}
}

func mapHeader(disposition, contentType string) map[string][]string {
	return map[string][]string{
		"Content-Disposition": {disposition},
		"Content-Type":        {contentType},
	}
}

// Parity-exempt: create-queen validation branches have no Python test beyond the happy path; pins 400/409 and the slug rules.
func TestQueenCreateRejectsTakenIDsAndBadInput(t *testing.T) {
	home := queenTestHome(t)
	mux := queenTestMux(home, nil, nil)
	cases := []struct {
		body   string
		status int
		error  string
	}{
		{`{"name":""}`, http.StatusBadRequest, "name is required"},
		{`{"name":"X","queen_id":"Bad ID"}`, http.StatusBadRequest, "queen_id must match"},
		{`{"name":"X","queen_id":"queen_legal"}`, http.StatusConflict, "queen id already exists"},
		{`[]`, http.StatusBadRequest, "Body must be an object"},
		{`not-json`, http.StatusBadRequest, "Invalid JSON body"},
	}
	for _, testCase := range cases {
		recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/create", testCase.body)
		if recorder.Code != testCase.status || !strings.Contains(queenStr(body["error"]), testCase.error) {
			t.Fatalf("%s: status = %d, error = %#v", testCase.body, recorder.Code, body["error"])
		}
	}
	_, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/create", `{"name":"Ada Lovelace"}`)
	entry := body["queen"].(map[string]any)
	if entry["id"] != "queen_ada_lovelace" {
		t.Fatalf("slug = %#v", entry["id"])
	}
}

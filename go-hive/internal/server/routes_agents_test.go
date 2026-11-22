package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func agentRoutesMux(home hivehome.Home, manager *SessionManager) *http.ServeMux {
	mux := http.NewServeMux()
	roots := agents.NewAllowedRoots(home.ColoniesDir(), home.Root, filepath.Join(home.Root, "agents"))
	RegisterAgentRoutes(mux, AgentRouteDeps{
		Manager: manager,
		Home:    home,
		Roots:   roots,
		// Hermetic: never read the developer's real shell config.
		EnsureCredentialEnv: func() {},
	})
	return mux
}

func writeAgentFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func makeColonyFixture(t *testing.T, home hivehome.Home, name string) string {
	t.Helper()
	colony := filepath.Join(home.ColoniesDir(), name)
	if err := os.MkdirAll(colony, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	writeAgentFixture(t, colony, "metadata.json",
		`{"queen_name":"queen_alpha","created_at":"2026-01-02T03:04:05+00:00","icon":"crown"}`)
	writeAgentFixture(t, colony, "worker.json",
		`{"name":"worker","description":"does work","tools":["a","b"]}`)
	return colony
}

// Parity-exempt: framework/agents/discovery.py and its routes have no Python tests; pins the discover payload fields against a loaded session.
func TestDiscoverAgentRouteReportsLoadedSessions(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeColonyFixture(t, home, "my_colony")
	resolved, err := agents.ResolvePath(colony)
	if err != nil {
		t.Fatalf("resolve colony: %v", err)
	}

	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "s1", WorkerPath: resolved})

	mux := agentRoutesMux(home, manager)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/discover", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	entries, ok := body["Your Agents"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("Your Agents = %#v", body["Your Agents"])
	}
	entry := entries[0].(map[string]any)
	if entry["path"] != resolved {
		t.Fatalf("path = %#v, want %q", entry["path"], resolved)
	}
	if entry["name"] != "My Colony" || entry["description"] != "does work" {
		t.Fatalf("name/description = %#v/%#v", entry["name"], entry["description"])
	}
	if entry["category"] != "Your Agents" {
		t.Fatalf("category = %#v", entry["category"])
	}
	if entry["session_count"] != float64(0) || entry["run_count"] != float64(0) {
		t.Fatalf("session/run counts = %#v/%#v", entry["session_count"], entry["run_count"])
	}
	if entry["node_count"] != float64(1) || entry["tool_count"] != float64(2) {
		t.Fatalf("node/tool counts = %#v/%#v", entry["node_count"], entry["tool_count"])
	}
	if entry["last_active"] != nil {
		t.Fatalf("last_active = %#v, want null", entry["last_active"])
	}
	if entry["created_at"] != "2026-01-02T03:04:05+00:00" || entry["icon"] != "crown" {
		t.Fatalf("created_at/icon = %#v/%#v", entry["created_at"], entry["icon"])
	}
	if entry["is_loaded"] != true {
		t.Fatalf("is_loaded = %#v, want true", entry["is_loaded"])
	}
	tags, ok := entry["tags"].([]any)
	if !ok || len(tags) != 0 {
		t.Fatalf("tags = %#v", entry["tags"])
	}
	workers, ok := entry["workers"].([]any)
	if !ok || len(workers) != 1 {
		t.Fatalf("workers = %#v", entry["workers"])
	}
	worker := workers[0].(map[string]any)
	if worker["name"] != "worker" || worker["tool_count"] != float64(2) || worker["colony_id"] != "my_colony" {
		t.Fatalf("worker = %#v", worker)
	}
}

// Parity-exempt: framework/agents/discovery.py and its routes have no Python tests; pins the empty groups payload and the unloaded flag.
func TestDiscoverAgentRouteEmptyAndUnloaded(t *testing.T) {
	emptyHome := hivehome.Home{Root: t.TempDir()}
	recorder, body := doSessionJSON(t, agentRoutesMux(emptyHome, nil), http.MethodGet, "/api/discover", "")
	if recorder.Code != http.StatusOK || len(body) != 0 {
		t.Fatalf("status/body = %d/%#v", recorder.Code, body)
	}

	home := hivehome.Home{Root: t.TempDir()}
	makeColonyFixture(t, home, "my_colony")
	recorder, body = doSessionJSON(t, agentRoutesMux(home, NewSessionManager()), http.MethodGet, "/api/discover", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	entry := body["Your Agents"].([]any)[0].(map[string]any)
	if entry["is_loaded"] != false {
		t.Fatalf("is_loaded = %#v, want false", entry["is_loaded"])
	}
}

// Parity-exempt: the delete-agent route has no Python tests; pins the soft-delete metadata write, its ASCII escaping and the discovery skip.
func TestDeleteAgentSoftDeleteMarksMetadata(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := filepath.Join(home.ColoniesDir(), "my_colony")
	if err := os.MkdirAll(colony, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	metadataPath := writeAgentFixture(t, colony, "metadata.json", `{"queen_name":"q","icon":"火箭"}`)
	writeAgentFixture(t, colony, "worker.json", `{"name":"w","tools":[]}`)

	mux := agentRoutesMux(home, NewSessionManager())
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `{"agent_path":"`+colony+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	resolved, err := agents.ResolvePath(colony)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if body["deleted"] != resolved || body["purged"] != false {
		t.Fatalf("body = %#v", body)
	}

	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"deleted": true`) {
		t.Fatalf("metadata missing deleted flag: %s", text)
	}
	if !strings.Contains(text, `\u706b\u7bad`) {
		t.Fatalf("metadata not ascii-escaped: %s", text)
	}
	if strings.Contains(text, "\n}") == false || strings.Contains(text, "火箭") {
		t.Fatalf("metadata formatting wrong: %s", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if decoded["deleted"] != true || decoded["queen_name"] != "q" {
		t.Fatalf("metadata = %#v", decoded)
	}

	_, discovered := doSessionJSON(t, mux, http.MethodGet, "/api/discover", "")
	if len(discovered) != 0 {
		t.Fatalf("soft-deleted colony was discovered: %#v", discovered)
	}
}

// Parity-exempt: the delete-agent route has no Python tests; pins purge removal and the missing-directory echo.
func TestDeleteAgentPurgeAndMissingDirectory(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeColonyFixture(t, home, "my_colony")

	mux := agentRoutesMux(home, NewSessionManager())
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `{"agent_path":"`+colony+`","purge":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["purged"] != true {
		t.Fatalf("purged = %#v", body["purged"])
	}
	if _, err := os.Stat(colony); !os.IsNotExist(err) {
		t.Fatalf("colony still exists: %v", err)
	}

	missing := filepath.Join(home.ColoniesDir(), "gone")
	recorder, body = doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `{"agent_path":"`+missing+`","purge":true}`)
	if recorder.Code != http.StatusOK || body["purged"] != true {
		t.Fatalf("missing status/body = %d/%#v", recorder.Code, body)
	}
}

// Parity-exempt: the delete-agent route has no Python tests; pins the body validation, traversal rejection and framework-agent guard.
func TestDeleteAgentRejects(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := agentRoutesMux(home, NewSessionManager())

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `{}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "agent_path is required" {
		t.Fatalf("missing path = %d/%#v", recorder.Code, body)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `{"agent_path":"colonies/../../etc/passwd"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d", recorder.Code)
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "allowed directory") {
		t.Fatalf("traversal error = %#v", body["error"])
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `not json`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("malformed body status = %d", recorder.Code)
	}

	frameworkAgent := filepath.Join(home.Root, "agents", "builtin")
	if err := os.MkdirAll(frameworkAgent, 0o755); err != nil {
		t.Fatalf("mkdir framework agent: %v", err)
	}
	recorder, body = doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `{"agent_path":"`+frameworkAgent+`"}`)
	if recorder.Code != http.StatusForbidden || body["error"] != "Cannot delete framework agents" {
		t.Fatalf("framework agent = %d/%#v", recorder.Code, body)
	}
}

// Parity-exempt: the metadata PATCH route has no Python tests; pins icon write-through, key preservation and ensure_ascii=False output.
func TestUpdateAgentMetadataWritesIcon(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeColonyFixture(t, home, "my_colony")
	metadataPath := filepath.Join(colony, "metadata.json")

	mux := agentRoutesMux(home, NewSessionManager())
	recorder, body := doSessionJSON(t, mux, http.MethodPatch, "/api/agents/metadata",
		`{"agent_path":"`+colony+`","icon":"🚀"}`)
	if recorder.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("status/body = %d/%#v", recorder.Code, body)
	}

	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "🚀") {
		t.Fatalf("icon not written verbatim: %s", text)
	}
	if strings.Contains(text, `\u`) {
		t.Fatalf("icon was ascii-escaped: %s", text)
	}
	if !strings.Contains(text, "\n  ") {
		t.Fatalf("metadata not indented: %s", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if decoded["icon"] != "🚀" || decoded["queen_name"] != "queen_alpha" || decoded["created_at"] != "2026-01-02T03:04:05+00:00" {
		t.Fatalf("metadata = %#v", decoded)
	}
}

// Parity-exempt: the metadata PATCH route has no Python tests; pins the 400/500 branches and the non-dict metadata failure.
func TestUpdateAgentMetadataRejects(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeColonyFixture(t, home, "my_colony")
	mux := agentRoutesMux(home, NewSessionManager())

	recorder, body := doSessionJSON(t, mux, http.MethodPatch, "/api/agents/metadata", `not json`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid JSON body" {
		t.Fatalf("invalid json = %d/%#v", recorder.Code, body)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodPatch, "/api/agents/metadata", `{"icon":"x"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "agent_path is required" {
		t.Fatalf("missing path = %d/%#v", recorder.Code, body)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodPatch, "/api/agents/metadata",
		`{"agent_path":"colonies/../../etc/passwd","icon":"x"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d", recorder.Code)
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "allowed directory") {
		t.Fatalf("traversal error = %#v", body["error"])
	}

	writeAgentFixture(t, colony, "metadata.json", `[]`)
	recorder, _ = doSessionJSON(t, mux, http.MethodPatch, "/api/agents/metadata",
		`{"agent_path":"`+colony+`","icon":"x"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("non-dict metadata status = %d", recorder.Code)
	}
}

// Parity: tests/test_validate_agent_path.py::TestHTTPEndpointsRejectMaliciousPaths::test_check_agent_credentials_rejects_traversal
func TestCheckAgentCredentialsRejectsTraversal(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(home.Root, "exports"), 0o755); err != nil {
		t.Fatalf("mkdir exports: %v", err)
	}

	recorder, body := doSessionJSON(t, agentRoutesMux(home, NewSessionManager()), http.MethodPost,
		"/api/credentials/check-agent", `{"agent_path":"exports/../../etc/passwd"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	message, _ := body["error"].(string)
	if !strings.Contains(message, "allowed directory") {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: only the traversal branch has an upstream HTTP test; pins the remaining check-agent branches.
func TestCheckAgentRemainingBranches(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeColonyFixture(t, home, "my_colony")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "")
	mux := agentRoutesMux(home, NewSessionManager())

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/check-agent", `{}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "agent_path is required" {
		t.Fatalf("missing path = %d/%#v", recorder.Code, body)
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodPost, "/api/credentials/check-agent", `not json`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("malformed body status = %d", recorder.Code)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodPost, "/api/credentials/check-agent",
		`{"agent_path":"`+colony+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid path status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	required, ok := body["required"].([]any)
	if !ok || len(required) != 0 {
		t.Fatalf("required = %#v, want []", body["required"])
	}
	if body["has_aden_key"] != false {
		t.Fatalf("has_aden_key = %#v", body["has_aden_key"])
	}
}

// Parity-exempt: no Python test pins aiohttp's json.loads leniency; pins that a body with trailing data or no value follows the same error branch as malformed JSON.
func TestAgentRoutesRejectTrailingDataAndEmptyBody(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeColonyFixture(t, home, "my_colony")
	mux := agentRoutesMux(home, NewSessionManager())

	cases := []struct {
		method string
		target string
		body   string
		status int
	}{
		{http.MethodDelete, "/api/agents", `{"agent_path":"` + colony + `"}{}`, http.StatusInternalServerError},
		{http.MethodDelete, "/api/agents", "", http.StatusInternalServerError},
		{http.MethodPatch, "/api/agents/metadata", `{"agent_path":"` + colony + `"} []`, http.StatusBadRequest},
		{http.MethodPatch, "/api/agents/metadata", "", http.StatusBadRequest},
		{http.MethodPost, "/api/credentials/check-agent", `{"agent_path":"` + colony + `"} null`, http.StatusInternalServerError},
		{http.MethodPost, "/api/credentials/check-agent", "", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		recorder, _ := doSessionJSON(t, mux, tc.method, tc.target, tc.body)
		if recorder.Code != tc.status {
			t.Fatalf("%s %q status = %d, want %d", tc.method, tc.body, recorder.Code, tc.status)
		}
	}

	recorder, body := doSessionJSON(t, mux, http.MethodPatch, "/api/agents/metadata", "null")
	if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
		t.Fatalf("null body = %d/%#v", recorder.Code, body)
	}
}

// Parity-exempt: no Python test covers metadata rewrites; pins that numbers survive PATCH and DELETE exactly instead of being reformatted through float64.
func TestAgentMetadataPreservesNumberLiterals(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeColonyFixture(t, home, "my_colony")
	metadataPath := filepath.Join(colony, "metadata.json")
	writeAgentFixture(t, colony, "metadata.json",
		`{"queen_name":"q","version":1.0,"big":123456789012345678901234567890}`)
	mux := agentRoutesMux(home, NewSessionManager())

	recorder, _ := doSessionJSON(t, mux, http.MethodPatch, "/api/agents/metadata",
		`{"agent_path":"`+colony+`","icon":"x"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d", recorder.Code)
	}
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 1.0`) {
		t.Fatalf("1.0 was reformatted: %s", raw)
	}
	if !strings.Contains(string(raw), `123456789012345678901234567890`) {
		t.Fatalf("big integer lost precision: %s", raw)
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodDelete, "/api/agents", `{"agent_path":"`+colony+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d", recorder.Code)
	}
	raw, err = os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("re-read metadata: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 1.0`) || !strings.Contains(string(raw), `"deleted": true`) {
		t.Fatalf("soft delete rewrote numbers: %s", raw)
	}
	if !strings.Contains(string(raw), `123456789012345678901234567890`) {
		t.Fatalf("soft delete lost precision: %s", raw)
	}
}

// Parity-exempt: json.loads semantics are covered by the Python runtime; pins decodeJSONBytes to a single value with preserved numeric literals.
func TestDecodeJSONBytesMatchesJSONLoads(t *testing.T) {
	value, err := decodeJSONBytes([]byte(`{"n":1.0,"big":123456789012345678901234567890}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("decoded = %#v", value)
	}
	if literal, ok := object["n"].(json.Number); !ok || literal.String() != "1.0" {
		t.Fatalf("n = %#v", object["n"])
	}
	if literal, ok := object["big"].(json.Number); !ok || literal.String() != "123456789012345678901234567890" {
		t.Fatalf("big = %#v", object["big"])
	}

	for _, body := range []string{"", "   ", "{}{}", `{"a":1} 2`, "not json"} {
		if _, err := decodeJSONBytes([]byte(body)); err == nil {
			t.Fatalf("decodeJSONBytes(%q) accepted invalid input", body)
		}
	}
	if value, err := decodeJSONBytes([]byte("null")); err != nil || value != nil {
		t.Fatalf("null = %#v/%v", value, err)
	}
}

package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func skillsTestDeps(t *testing.T) SkillsRouteDeps {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	if err := os.MkdirAll(home.QueensDir(), 0o755); err != nil {
		t.Fatalf("mkdir queens: %v", err)
	}
	if err := os.MkdirAll(home.ColoniesDir(), 0o755); err != nil {
		t.Fatalf("mkdir colonies: %v", err)
	}
	return SkillsRouteDeps{Home: home, Manager: NewSessionManager(), UserHome: t.TempDir()}
}

func skillsTestMux(deps SkillsRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterSkillsRoutes(mux, deps)
	return mux
}

func skillsTestSeedQueen(t *testing.T, deps SkillsRouteDeps, queenID string) string {
	t.Helper()
	dir := deps.Home.QueenDir(queenID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir queen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte("name: "+queenID+"\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return dir
}

func skillsTestSeedColony(t *testing.T, deps SkillsRouteDeps, colonyName, queenName string) string {
	t.Helper()
	dir := filepath.Join(deps.Home.ColoniesDir(), colonyName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	meta := "{}"
	if queenName != "" {
		meta = `{"queen_name":"` + queenName + `"}`
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	return dir
}

func skillsTestWriteSkillFile(t *testing.T, root, name, description, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	text := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(text), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return dir
}

func skillsTestDo(t *testing.T, mux *http.ServeMux, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

func skillsTestMultipart(t *testing.T, mux *http.ServeMux, fields map[string]string, filename string, content []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if content != nil {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/skills/upload", &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

func skillsTestRows(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := body[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want array", key, body[key])
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s entry = %#v, want object", key, item)
		}
		rows = append(rows, row)
	}
	return rows
}

func skillsTestFindRow(t *testing.T, rows []map[string]any, name string) map[string]any {
	t.Helper()
	for _, row := range rows {
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("row %q not found in %#v", name, rows)
	return nil
}

type skillsTestQueenNode struct {
	mu       sync.Mutex
	messages []string
}

func (n *skillsTestQueenNode) InjectEvent(text string, isClientInput bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, text)
	return nil
}

func (n *skillsTestQueenNode) Messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.messages))
	copy(out, n.messages)
	return out
}

func skillsTestSeedInstall(t *testing.T, deps SkillsRouteDeps, sessionID, name string) (string, *skills.InstallRequest) {
	t.Helper()
	sessionDir := deps.Home.QueenSessionDir("queen_ava", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	draft, errMsg := skills.BuildSkillDraft(
		name,
		"How Ava formats her reports.",
		"# House style\n\nLead with the answer.\n",
		[]map[string]any{{"path": "scripts/check.py", "content": "print('ok')\n"}},
	)
	if draft == nil {
		t.Fatalf("build draft: %s", errMsg)
	}
	request, err := skills.AddRequest(sessionDir, draft)
	if err != nil {
		t.Fatalf("add install request: %v", err)
	}
	return sessionDir, request
}

// ---------------------------------------------------------------------------
// Per-queen routes
// ---------------------------------------------------------------------------

// Parity: core/tests/test_routes_skills.py::test_get_queen_skills_returns_empty_for_fresh_queen
func TestSkillsRouteListQueenSkills(t *testing.T) {
	deps := skillsTestDeps(t)
	skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)

	recorder, body := skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["queen_id"] != "ops" {
		t.Fatalf("queen_id = %#v", body["queen_id"])
	}
	if body["all_defaults_disabled"] != false {
		t.Fatalf("all_defaults_disabled = %#v", body["all_defaults_disabled"])
	}
	rows := skillsTestRows(t, body, "skills")
	if len(rows) == 0 {
		t.Fatal("expected bundled framework skills in a fresh queen catalog")
	}
	for _, row := range rows {
		if row["name"] == "" || row["source_scope"] == "" {
			t.Fatalf("row missing identity fields: %#v", row)
		}
		if _, present := row["enabled"]; !present {
			t.Fatalf("row missing enabled: %#v", row)
		}
	}

	queenDir := deps.Home.QueenDir("ops")
	visibleDir := filepath.Join(queenDir, "skills", "visible-skill")
	if err := os.MkdirAll(visibleDir, 0o755); err != nil {
		t.Fatalf("mkdir visible skill: %v", err)
	}
	visibleMD := "---\nname: visible-skill\ndescription: Visibility fixture\nmetadata:\n  visibility:\n    - queen_ui\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(visibleDir, "SKILL.md"), []byte(visibleMD), 0o644); err != nil {
		t.Fatalf("write visible skill: %v", err)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	row := skillsTestFindRow(t, skillsTestRows(t, body, "skills"), "visible-skill")
	if row["source_scope"] != "queen_ui" || row["provenance"] != "user_ui_created" {
		t.Fatalf("visible row = %#v", row)
	}
	if row["editable"] != true || row["deletable"] != true {
		t.Fatalf("visible row edit flags = %#v", row)
	}
	if row["base_dir"] != resolveSkillsTestPath(t, visibleDir) {
		t.Fatalf("base_dir = %#v, want %q", row["base_dir"], resolveSkillsTestPath(t, visibleDir))
	}
	visibility, _ := row["visibility"].([]any)
	if len(visibility) != 1 || visibility[0] != "queen_ui" {
		t.Fatalf("visibility = %#v", row["visibility"])
	}

	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ghost/skills", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "queen 'ghost' not found" {
		t.Fatalf("unknown queen: status = %d, body = %#v", recorder.Code, body)
	}

	recorder, _ = skillsTestDo(t, mux, http.MethodGet, "/api/queen/..%2Fescape/skills", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("traversal queen id status = %d, want 404", recorder.Code)
	}
}

// Parity: core/tests/test_routes_skills.py::test_create_queen_skill_writes_file_and_override
func TestSkillsRouteCreateQueenSkill(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)

	payload := `{"name":"ops-runbook","description":"Runbook for ops","body":"## Steps\n1. Check\n","enabled":true}`
	request := httptest.NewRequest(http.MethodPost, "/api/queen/ops/skills", strings.NewReader(payload))
	request.Header.Set("X-User", "alice@example.com")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "ops-runbook" || body["enabled"] != true || body["replaced"] != false {
		t.Fatalf("body = %#v", body)
	}
	if body["provenance"] != "user_ui_created" {
		t.Fatalf("provenance = %#v", body["provenance"])
	}
	skillMD := filepath.Join(queenDir, "skills", "ops-runbook", "SKILL.md")
	if _, err := os.Stat(skillMD); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	store := skills.LoadSkillOverrideStore(filepath.Join(queenDir, "skills_overrides.json"), "queen:ops")
	entry := store.Get("ops-runbook")
	if entry == nil || entry.Provenance != skills.ProvenanceUserUICreated {
		t.Fatalf("override entry = %#v", entry)
	}
	if entry.Enabled == nil || !*entry.Enabled {
		t.Fatalf("override enabled = %#v", entry.Enabled)
	}
	if entry.CreatedBy == nil || *entry.CreatedBy != "alice@example.com" {
		t.Fatalf("created_by = %#v", entry.CreatedBy)
	}
	if entry.CreatedAt == nil {
		t.Fatal("created_at not recorded")
	}

	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills", `{"name":"ops-runbook","description":"d","body":"b"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills",
		`{"name":"ops-runbook","description":"Replaced","body":"new body","replace_existing":true}`)
	if recorder.Code != http.StatusCreated || body["replaced"] != true {
		t.Fatalf("replace status = %d, body = %#v", recorder.Code, body)
	}

	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills", `{"name":"Bad_Name","description":"d","body":"b"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid name status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills", `{"name":"no-desc","body":"b"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing description status = %d", recorder.Code)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills", `{not json`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d", recorder.Code)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ghost/skills", `{"name":"x","description":"d","body":"b"}`)
	if recorder.Code != http.StatusNotFound || body["error"] != "queen not found" {
		t.Fatalf("unknown queen: status = %d, body = %#v", recorder.Code, body)
	}
}

// Parity: core/tests/test_routes_skills.py::test_patch_queen_skill_toggles_enabled
func TestSkillsRoutePatchQueenSkill(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)
	skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills", `{"name":"ops-a","description":"a","body":"body"}`)

	recorder, body := skillsTestDo(t, mux, http.MethodPatch, "/api/queen/ops/skills/ops-a", `{"enabled":false}`)
	if recorder.Code != http.StatusOK || body["ok"] != true || body["enabled"] != false {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	store := skills.LoadSkillOverrideStore(filepath.Join(queenDir, "skills_overrides.json"), "queen:ops")
	entry := store.Get("ops-a")
	if entry == nil || entry.Enabled == nil || *entry.Enabled {
		t.Fatalf("entry = %#v", entry)
	}

	recorder, body = skillsTestDo(t, mux, http.MethodPatch, "/api/queen/ops/skills/ops-a", `{"enabled":"no"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "'enabled' must be a bool" {
		t.Fatalf("bad enabled: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodPatch, "/api/queen/ops/skills/ops-a", `{"param_overrides":[]}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "'param_overrides' must be an object" {
		t.Fatalf("bad param_overrides: status = %d, body = %#v", recorder.Code, body)
	}

	recorder, body = skillsTestDo(t, mux, http.MethodPatch, "/api/queen/ops/skills/ops-a",
		`{"enabled":true,"param_overrides":{"temperature":0.2},"notes":"hand tune","all_defaults_disabled":true}`)
	if recorder.Code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("full patch: status = %d, body = %#v", recorder.Code, body)
	}
	store = skills.LoadSkillOverrideStore(filepath.Join(queenDir, "skills_overrides.json"), "queen:ops")
	entry = store.Get("ops-a")
	if entry == nil || entry.Notes == nil || *entry.Notes != "hand tune" {
		t.Fatalf("notes = %#v", entry)
	}
	if entry.ParamOverrides["temperature"] != 0.2 {
		t.Fatalf("param_overrides = %#v", entry.ParamOverrides)
	}
	if !store.AllDefaultsDisabled {
		t.Fatal("all_defaults_disabled not persisted")
	}

	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills", "")
	if recorder.Code != http.StatusOK || body["all_defaults_disabled"] != true {
		t.Fatalf("list: status = %d, body = %#v", recorder.Code, body)
	}
	row := skillsTestFindRow(t, skillsTestRows(t, body, "skills"), "ops-a")
	if row["enabled"] != true || row["created_by"] != "ui" || row["created_at"] == nil {
		t.Fatalf("row = %#v", row)
	}
	if row["notes"] != "hand tune" {
		t.Fatalf("row notes = %#v", row["notes"])
	}
	params, _ := row["param_overrides"].(map[string]any)
	if params["temperature"] != 0.2 {
		t.Fatalf("row param_overrides = %#v", row["param_overrides"])
	}
	trusted := "trusted"
	entry.Trust = &trusted
	if err := store.Save(); err != nil {
		t.Fatalf("save trust: %v", err)
	}
	_, body = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills", "")
	row = skillsTestFindRow(t, skillsTestRows(t, body, "skills"), "ops-a")
	if row["trust"] != "trusted" {
		t.Fatalf("row trust = %#v", row["trust"])
	}
}

// Parity: core/tests/test_routes_skills.py::test_delete_queen_skill_removes_files, core/tests/test_routes_skills.py::test_delete_framework_skill_is_refused
func TestSkillsRouteDeleteQueenSkill(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)
	skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills", `{"name":"tmp-skill","description":"d","body":"body"}`)
	skillDir := filepath.Join(queenDir, "skills", "tmp-skill")
	if _, err := os.Stat(skillDir); err != nil {
		t.Fatalf("skill dir missing: %v", err)
	}

	recorder, body := skillsTestDo(t, mux, http.MethodDelete, "/api/queen/ops/skills/tmp-skill", "")
	if recorder.Code != http.StatusOK || body["removed"] != true {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatalf("skill dir still present: %v", err)
	}
	store := skills.LoadSkillOverrideStore(filepath.Join(queenDir, "skills_overrides.json"), "queen:ops")
	if _, tombstoned := store.DeletedUISkills["tmp-skill"]; !tombstoned {
		t.Fatal("delete did not leave a tombstone")
	}

	store.Upsert("hive.writing-hive-skills", &skills.OverrideEntry{Provenance: skills.ProvenanceFramework})
	if err := store.Save(); err != nil {
		t.Fatalf("save store: %v", err)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodDelete, "/api/queen/ops/skills/hive.writing-hive-skills", "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("framework delete status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodDelete, "/api/queen/ops/skills/Bad_Name", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid name status = %d", recorder.Code)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodDelete, "/api/queen/ghost/skills/x", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown queen status = %d", recorder.Code)
	}
}

// Parity-exempt: no upstream test covers the queen-scoped body read; pinned to core/framework/server/routes_skills.py::_handle_get_body
func TestSkillsRouteGetQueenSkillBody(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)
	skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills",
		`{"name":"ops-a","description":"A skill","body":"## Steps\n1. Check"}`)

	recorder, body := skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills/ops-a/body", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "ops-a" || body["description"] != "A skill" {
		t.Fatalf("body = %#v", body)
	}
	if body["body"] != "## Steps\n1. Check" {
		t.Fatalf("body text = %#v", body["body"])
	}
	if body["editable"] != true || body["source_scope"] != "queen_ui" {
		t.Fatalf("editable/scope = %#v/%#v", body["editable"], body["source_scope"])
	}
	if body["base_dir"] != resolveSkillsTestPath(t, filepath.Join(queenDir, "skills", "ops-a")) {
		t.Fatalf("base_dir = %#v", body["base_dir"])
	}

	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills/hive.writing-hive-skills/body", "")
	if recorder.Code != http.StatusOK || body["editable"] != false {
		t.Fatalf("framework body: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills/no-such-skill/body", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "skill 'no-such-skill' not found" {
		t.Fatalf("missing skill: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ops/skills/Bad_Name/body", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid name status = %d", recorder.Code)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodGet, "/api/queen/ghost/skills/ops-a/body", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown queen status = %d", recorder.Code)
	}
}

// Parity-exempt: no upstream test covers body writes; pinned to core/framework/server/routes_skills.py::_handle_put_body
func TestSkillsRoutePutQueenSkillBody(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)
	skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills",
		`{"name":"ops-a","description":"Original desc","body":"old"}`)

	recorder, body := skillsTestDo(t, mux, http.MethodPut, "/api/queen/ops/skills/ops-a/body",
		`{"body":"## New body\n","description":"New desc"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "ops-a" || body["installed_path"] == "" {
		t.Fatalf("body = %#v", body)
	}
	text, err := os.ReadFile(filepath.Join(queenDir, "skills", "ops-a", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(text), "description: New desc") || !strings.Contains(string(text), "## New body") {
		t.Fatalf("SKILL.md = %q", string(text))
	}

	recorder, body = skillsTestDo(t, mux, http.MethodPut, "/api/queen/ops/skills/ops-a/body", `{"body":"kept desc"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("omit description status = %d, body = %#v", recorder.Code, body)
	}
	text, _ = os.ReadFile(filepath.Join(queenDir, "skills", "ops-a", "SKILL.md"))
	if !strings.Contains(string(text), "description: New desc") {
		t.Fatalf("description not preserved: %q", string(text))
	}

	recorder, body = skillsTestDo(t, mux, http.MethodPut, "/api/queen/ops/skills/ops-a/body", `{"body":"   "}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "'body' is required" {
		t.Fatalf("empty body: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodPut, "/api/queen/ops/skills/hive.writing-hive-skills/body", `{"body":"nope"}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("framework put status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPut, "/api/queen/ghost/skills/x/body", `{"body":"x"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown queen status = %d", recorder.Code)
	}
}

// Parity-exempt: no upstream test covers rename; pinned to core/framework/server/routes_skills.py::_handle_rename
func TestSkillsRouteRenameQueenSkill(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)
	skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills",
		`{"name":"old-name","description":"Renamable","body":"body"}`)

	recorder, body := skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills/old-name/rename", `{"new_name":"new-name"}`)
	if recorder.Code != http.StatusOK || body["ok"] != true || body["new_name"] != "new-name" {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	if _, err := os.Stat(filepath.Join(queenDir, "skills", "old-name")); !os.IsNotExist(err) {
		t.Fatalf("old dir still present: %v", err)
	}
	text, err := os.ReadFile(filepath.Join(queenDir, "skills", "new-name", "SKILL.md"))
	if err != nil {
		t.Fatalf("read renamed SKILL.md: %v", err)
	}
	if !strings.Contains(string(text), "name: new-name") {
		t.Fatalf("frontmatter not renamed: %q", string(text))
	}
	store := skills.LoadSkillOverrideStore(filepath.Join(queenDir, "skills_overrides.json"), "queen:ops")
	if store.Get("old-name") != nil || store.Get("new-name") == nil {
		t.Fatalf("override entry not moved: %#v", store.Overrides)
	}

	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills/new-name/rename", `{"new_name":"new-name"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "new_name matches the current name" {
		t.Fatalf("same name: status = %d, body = %#v", recorder.Code, body)
	}
	skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills", `{"name":"taken","description":"d","body":"b"}`)
	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills/new-name/rename", `{"new_name":"taken"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("taken name: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills/hive.writing-hive-skills/rename", `{"new_name":"other"}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("framework rename: status = %d, body = %#v", recorder.Code, body)
	}

	store.Upsert("missing-copy", &skills.OverrideEntry{Provenance: skills.ProvenanceUserUICreated})
	if err := store.Save(); err != nil {
		t.Fatalf("save store: %v", err)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills/missing-copy/rename", `{"new_name":"other"}`)
	if recorder.Code != http.StatusNotFound || body["error"] != "skill 'missing-copy' has no editable copy for this queen" {
		t.Fatalf("missing copy: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ghost/skills/x/rename", `{"new_name":"y"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown queen status = %d", recorder.Code)
	}
}

// Parity-exempt: no upstream test covers reload; pinned to core/framework/server/routes_skills.py::_handle_reload
func TestSkillsRouteReloadQueenSkills(t *testing.T) {
	deps := skillsTestDeps(t)
	skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)

	recorder, body := skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills/reload", "")
	if recorder.Code != http.StatusOK || body["ok"] != true || body["target"] != "ops" || body["kind"] != "queen" {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPost, "/api/queen/ghost/skills/reload", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown queen status = %d", recorder.Code)
	}
}

// ---------------------------------------------------------------------------
// Aggregated library
// ---------------------------------------------------------------------------

// Parity: core/tests/test_routes_skills.py::test_colony_authored_skills_surface_in_library
func TestSkillsRouteListAllSkills(t *testing.T) {
	deps := skillsTestDeps(t)
	skillsTestSeedQueen(t, deps, "ops")
	colonyDir := skillsTestSeedColony(t, deps, "research_one", "ops")
	mux := skillsTestMux(deps)
	skillsTestDo(t, mux, http.MethodPost, "/api/queen/ops/skills",
		`{"name":"queen-skill","description":"From the queen UI","body":"body"}`)
	skillsTestWriteSkillFile(t, filepath.Join(colonyDir, "skills"), "flat-colony-skill", "Flat layout", "body")
	skillsTestWriteSkillFile(t, filepath.Join(colonyDir, ".hive", "skills"), "legacy-colony-skill", "Legacy nested", "body")

	recorder, body := skillsTestDo(t, mux, http.MethodGet, "/api/skills", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	rows := skillsTestRows(t, body, "skills")
	for _, name := range []string{"flat-colony-skill", "legacy-colony-skill"} {
		row := skillsTestFindRow(t, rows, name)
		if row["source_scope"] != "colony_ui" || row["provenance"] != "queen_created" {
			t.Fatalf("%s row = %#v", name, row)
		}
		owner, _ := row["owner"].(map[string]any)
		if owner == nil || owner["type"] != "colony" || owner["id"] != "research_one" || owner["name"] != "research_one" {
			t.Fatalf("%s owner = %#v", name, row["owner"])
		}
		if row["editable"] != true || row["deletable"] != true {
			t.Fatalf("%s editable = %#v", name, row)
		}
	}
	queenRow := skillsTestFindRow(t, rows, "queen-skill")
	if queenRow["source_scope"] != "queen_ui" || queenRow["provenance"] != "user_ui_created" {
		t.Fatalf("queen row = %#v", queenRow)
	}
	owner, _ := queenRow["owner"].(map[string]any)
	if owner == nil || owner["type"] != "queen" || owner["id"] != "ops" {
		t.Fatalf("queen owner = %#v", queenRow["owner"])
	}
	visible, _ := queenRow["visible_to"].(map[string]any)
	if visible == nil {
		t.Fatalf("visible_to = %#v", queenRow["visible_to"])
	}
	if !skillsTestContains(visible["queens"], "ops") || !skillsTestContains(visible["colonies"], "research_one") {
		t.Fatalf("visible_to = %#v", visible)
	}

	queens := skillsTestRows(t, body, "queens")
	if len(queens) != 1 || queens[0]["id"] != "ops" || queens[0]["name"] != "ops" {
		t.Fatalf("queens = %#v", queens)
	}
	colonies := skillsTestRows(t, body, "colonies")
	if len(colonies) != 1 || colonies[0]["name"] != "research_one" || colonies[0]["queen_id"] != "ops" {
		t.Fatalf("colonies = %#v", colonies)
	}
}

func skillsTestContains(value any, want string) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// Parity: core/tests/test_routes_skills.py::test_list_scopes_enumerates_queens_and_colonies
func TestSkillsRouteListScopes(t *testing.T) {
	deps := skillsTestDeps(t)
	skillsTestSeedQueen(t, deps, "ops")
	skillsTestSeedColony(t, deps, "research_one", "ops")
	skillsTestSeedColony(t, deps, "orphan_colony", "")
	mux := skillsTestMux(deps)

	recorder, body := skillsTestDo(t, mux, http.MethodGet, "/api/skills/scopes", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	queens := skillsTestRows(t, body, "queens")
	if len(queens) != 1 || queens[0]["id"] != "ops" || queens[0]["name"] != "ops" {
		t.Fatalf("queens = %#v", queens)
	}
	colonies := skillsTestRows(t, body, "colonies")
	if len(colonies) != 2 {
		t.Fatalf("colonies = %#v", colonies)
	}
	byName := map[string]map[string]any{}
	for _, row := range colonies {
		byName[row["name"].(string)] = row
	}
	if byName["research_one"]["queen_id"] != "ops" {
		t.Fatalf("research_one = %#v", byName["research_one"])
	}
	if byName["orphan_colony"]["queen_id"] != nil {
		t.Fatalf("orphan_colony = %#v", byName["orphan_colony"])
	}
}

// Parity-exempt: no upstream test covers the scope-less detail; pinned to core/framework/server/routes_skills.py::handle_get_skill_detail
func TestSkillsRouteGetSkillDetail(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	skillsTestWriteSkillFile(t, filepath.Join(queenDir, "skills"), "queen-skill", "Detail me", "body text")
	colonyDir := skillsTestSeedColony(t, deps, "research_one", "ops")
	skillsTestWriteSkillFile(t, filepath.Join(colonyDir, ".hive", "skills"), "legacy-colony-skill", "Legacy nested", "body")
	mux := skillsTestMux(deps)

	recorder, body := skillsTestDo(t, mux, http.MethodGet, "/api/skills/queen-skill", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "queen-skill" || body["description"] != "Detail me" {
		t.Fatalf("body = %#v", body)
	}
	text, _ := body["body"].(string)
	if !strings.Contains(text, "name: queen-skill") || !strings.Contains(text, "body text") {
		t.Fatalf("body text = %#v", body["body"])
	}
	if body["base_dir"] != resolveSkillsTestPath(t, filepath.Join(queenDir, "skills", "queen-skill")) {
		t.Fatalf("base_dir = %#v", body["base_dir"])
	}
	if _, present := body["editable"]; present {
		t.Fatalf("scope-less detail must not set editable: %#v", body)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/skills/legacy-colony-skill", "")
	if recorder.Code != http.StatusOK || body["source_scope"] != "colony_ui" {
		t.Fatalf("colony detail: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodGet, "/api/skills/Bad_Name", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid name status = %d", recorder.Code)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/skills/ghost-skill", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "skill 'ghost-skill' not found" {
		t.Fatalf("missing skill: status = %d, body = %#v", recorder.Code, body)
	}
}

// ---------------------------------------------------------------------------
// Upload
// ---------------------------------------------------------------------------

// Parity: core/tests/test_routes_skills.py::test_upload_markdown_places_in_user_library
func TestSkillsRouteUploadMarkdownUser(t *testing.T) {
	deps := skillsTestDeps(t)
	mux := skillsTestMux(deps)
	skillMD := []byte("---\nname: from-upload\ndescription: Uploaded skill\n---\n\n## Body\nHi.\n")

	recorder, body := skillsTestMultipart(t, mux, map[string]string{"scope": "user", "enabled": "true"}, "SKILL.md", skillMD)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "from-upload" || body["enabled"] != true || body["scope"] != "user" || body["target_id"] != nil {
		t.Fatalf("body = %#v", body)
	}
	installed := filepath.Join(deps.Home.Root, "skills", "from-upload", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("uploaded SKILL.md missing: %v", err)
	}
	recorder, _ = skillsTestMultipart(t, mux, map[string]string{"scope": "user"}, "SKILL.md", skillMD)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate upload status = %d", recorder.Code)
	}
	recorder, body = skillsTestMultipart(t, mux, map[string]string{"scope": "user", "replace_existing": "true"}, "SKILL.md", skillMD)
	if recorder.Code != http.StatusCreated || body["replaced"] != true {
		t.Fatalf("replace upload: status = %d, body = %#v", recorder.Code, body)
	}

	recorder, body = skillsTestMultipart(t, mux, map[string]string{"scope": "user"}, "SKILL.md", nil)
	if recorder.Code != http.StatusBadRequest || body["error"] != "missing 'file' part" {
		t.Fatalf("missing file: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestMultipart(t, mux, map[string]string{"scope": "colony"}, "SKILL.md", skillMD)
	if recorder.Code != http.StatusBadRequest || body["error"] != "scope must be user|queen" {
		t.Fatalf("bad scope: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestMultipart(t, mux, map[string]string{"scope": "queen"}, "SKILL.md", skillMD)
	if recorder.Code != http.StatusBadRequest || body["error"] != "target_id required for scope=queen" {
		t.Fatalf("missing target: status = %d, body = %#v", recorder.Code, body)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/skills/upload", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	plain := httptest.NewRecorder()
	mux.ServeHTTP(plain, request)
	if plain.Code != http.StatusBadRequest {
		t.Fatalf("non-multipart status = %d", plain.Code)
	}
}

// Parity: core/tests/test_routes_skills.py::test_upload_zip_bundle_places_in_queen_scope
func TestSkillsRouteUploadZipQueen(t *testing.T) {
	deps := skillsTestDeps(t)
	queenDir := skillsTestSeedQueen(t, deps, "ops")
	mux := skillsTestMux(deps)

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	skillFile, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatalf("zip skill: %v", err)
	}
	_, _ = skillFile.Write([]byte("---\nname: zipped-skill\ndescription: From zip\n---\n\nbody\n"))
	helper, err := writer.Create("scripts/helper.py")
	if err != nil {
		t.Fatalf("zip helper: %v", err)
	}
	_, _ = helper.Write([]byte("print('hi')\n"))
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	recorder, body := skillsTestMultipart(t, mux,
		map[string]string{"scope": "queen", "target_id": "ops", "enabled": "true"},
		"bundle.zip", buffer.Bytes())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "zipped-skill" || body["scope"] != "queen" || body["target_id"] != "ops" {
		t.Fatalf("body = %#v", body)
	}
	skillDir := filepath.Join(queenDir, "skills", "zipped-skill")
	if _, err := os.Stat(filepath.Join(skillDir, "scripts", "helper.py")); err != nil {
		t.Fatalf("zip supporting file missing: %v", err)
	}
	store := skills.LoadSkillOverrideStore(filepath.Join(queenDir, "skills_overrides.json"), "queen:ops")
	if entry := store.Get("zipped-skill"); entry == nil || entry.Provenance != skills.ProvenanceUserUICreated {
		t.Fatalf("override entry = %#v", entry)
	}

	recorder, body = skillsTestMultipart(t, mux,
		map[string]string{"scope": "queen", "target_id": "ghost"}, "SKILL.md",
		[]byte("---\nname: x\ndescription: d\n---\n\nbody\n"))
	if recorder.Code != http.StatusNotFound || body["error"] != "queen not found" {
		t.Fatalf("unknown queen: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = skillsTestMultipart(t, mux,
		map[string]string{"scope": "queen", "target_id": "ops"}, "broken.zip",
		append(append([]byte{}, skillsRouteZipMagic...), []byte("not a zip")...))
	if recorder.Code != http.StatusBadRequest || body["error"] != "invalid zip file" {
		t.Fatalf("invalid zip: status = %d, body = %#v", recorder.Code, body)
	}
}

// ---------------------------------------------------------------------------
// A queen's requests to add a skill to the shared library
// ---------------------------------------------------------------------------

// Parity: core/tests/test_routes_skills.py::test_pending_skill_installs_carry_the_skill_and_whether_it_replaces_one
func TestSkillsRouteListSkillInstalls(t *testing.T) {
	deps := skillsTestDeps(t)
	sessionDir, request := skillsTestSeedInstall(t, deps, "session_ava_1", "house-style")
	_, fresh := skillsTestSeedInstall(t, deps, "session_ava_1", "fresh-skill")
	if err := os.MkdirAll(filepath.Join(deps.Home.Root, "skills", "house-style"), 0o755); err != nil {
		t.Fatalf("mkdir library skill: %v", err)
	}
	mux := skillsTestMux(deps)

	recorder, body := skillsTestDo(t, mux, http.MethodGet, "/api/sessions/"+filepath.Base(sessionDir)+"/skill-installs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	rows := skillsTestRows(t, body, "requests")
	if len(rows) != 2 {
		t.Fatalf("requests = %#v", rows)
	}
	if rows[0]["name"] != "house-style" || rows[0]["replaces"] != true {
		t.Fatalf("first row = %#v", rows[0])
	}
	if rows[1]["name"] != "fresh-skill" || rows[1]["replaces"] != false || rows[1]["id"] != fresh.ID {
		t.Fatalf("second row = %#v", rows[1])
	}
	if rows[0]["id"] != request.ID {
		t.Fatalf("request id = %#v, want %s", rows[0]["id"], request.ID)
	}
	files, _ := rows[0]["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %#v", rows[0]["files"])
	}
	file, _ := files[0].(map[string]any)
	if file["path"] != "scripts/check.py" || file["content"] != "print('ok')\n" {
		t.Fatalf("file = %#v", file)
	}

	recorder, body = skillsTestDo(t, mux, http.MethodGet, "/api/sessions/session_nobody/skill-installs", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "session not found" {
		t.Fatalf("unknown session: status = %d, body = %#v", recorder.Code, body)
	}
}

// Parity: core/tests/test_routes_skills.py::test_approving_installs_the_stored_skill_and_tells_the_queen
func TestSkillsRouteApproveSkillInstall(t *testing.T) {
	deps := skillsTestDeps(t)
	sessionDir, request := skillsTestSeedInstall(t, deps, "session_ava_1", "house-style")
	node := &skillsTestQueenNode{}
	bus := eventbus.NewEventBus()
	var decided []map[string]any
	bus.Subscribe([]eventbus.EventType{eventbus.EventSkillInstallDecided}, func(event *eventbus.AgentEvent) {
		decided = append(decided, event.Data)
	})
	deps.Manager.AddSession(&Session{
		ID:            filepath.Base(sessionDir),
		QueenDir:      sessionDir,
		QueenExecutor: &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}},
		EventBus:      bus,
	})
	mux := skillsTestMux(deps)

	url := "/api/sessions/" + filepath.Base(sessionDir) + "/skill-installs/" + request.ID
	recorder, body := skillsTestDo(t, mux, http.MethodPost, url, `{"decision":"approve"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "house-style" || body["decision"] != "approve" || body["queen_told"] != true {
		t.Fatalf("body = %#v", body)
	}
	installed := filepath.Join(deps.Home.Root, "skills", "house-style")
	text, err := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if err != nil {
		t.Fatalf("installed SKILL.md: %v", err)
	}
	if !strings.Contains(string(text), "Lead with the answer.") {
		t.Fatalf("SKILL.md = %q", string(text))
	}
	if helper, err := os.ReadFile(filepath.Join(installed, "scripts", "check.py")); err != nil || string(helper) != "print('ok')\n" {
		t.Fatalf("helper = %q, err = %v", string(helper), err)
	}
	if pending := skills.PendingRequests(sessionDir); len(pending) != 0 {
		t.Fatalf("pending after approve = %#v", pending)
	}
	messages := node.Messages()
	if len(messages) != 1 || !strings.Contains(messages[0], "approved") || !strings.Contains(messages[0], "house-style") {
		t.Fatalf("queen messages = %#v", messages)
	}
	if len(decided) != 1 || decided[0]["request_id"] != request.ID || decided[0]["decision"] != "approve" {
		t.Fatalf("decided events = %#v", decided)
	}

	recorder, _ = skillsTestDo(t, mux, http.MethodPost, url, `{"decision":"approve"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("second decision status = %d", recorder.Code)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPost,
		"/api/sessions/"+filepath.Base(sessionDir)+"/skill-installs/nope", `{"decision":"approve"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown request status = %d", recorder.Code)
	}
	recorder, body = skillsTestDo(t, mux, http.MethodPost, url, `{"decision":"maybe"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "decision must be 'approve' or 'deny'" {
		t.Fatalf("bad decision: status = %d, body = %#v", recorder.Code, body)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPost, "/api/sessions/session_nobody/skill-installs/x", `{"decision":"approve"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d", recorder.Code)
	}
}

// Parity: core/tests/test_routes_skills.py::test_denying_installs_nothing
func TestSkillsRouteDenySkillInstall(t *testing.T) {
	deps := skillsTestDeps(t)
	sessionDir, request := skillsTestSeedInstall(t, deps, "session_ava_2", "house-style")
	mux := skillsTestMux(deps)

	recorder, body := skillsTestDo(t, mux, http.MethodPost,
		"/api/sessions/"+filepath.Base(sessionDir)+"/skill-installs/"+request.ID, `{"decision":"deny"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["queen_told"] != false || body["decision"] != "deny" {
		t.Fatalf("body = %#v", body)
	}
	if _, err := os.Stat(filepath.Join(deps.Home.Root, "skills", "house-style")); !os.IsNotExist(err) {
		t.Fatalf("denied skill was installed: %v", err)
	}
	if pending := skills.PendingRequests(sessionDir); len(pending) != 0 {
		t.Fatalf("pending after deny = %#v", pending)
	}
	recorder, _ = skillsTestDo(t, mux, http.MethodPost,
		"/api/sessions/"+filepath.Base(sessionDir)+"/skill-installs/"+request.ID, `{not json`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d", recorder.Code)
	}
}

// resolveSkillsTestPath mirrors ParsedSkill.Location/BaseDir's Path.resolve()
// semantics (skills.resolveSkillPath): expectations built from t.TempDir()
// must resolve /var -> /private/var on macOS the same way the parser does.
func resolveSkillsTestPath(t *testing.T, path string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

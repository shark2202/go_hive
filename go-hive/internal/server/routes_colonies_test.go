package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	triggers_store "github.com/shark2202/go_hive/go-hive/internal/triggers"
)

func makeRouteColony(t *testing.T, home hivehome.Home, name string, deleted bool) {
	t.Helper()
	colonyDir := filepath.Join(home.ColoniesDir(), name)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	meta := map[string]any{"colony_id": name, "queen_name": "tester"}
	if deleted {
		meta["deleted"] = true
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyDir, "worker.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write worker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyDir, "marker.txt"), []byte(name), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	agentDir := filepath.Join(home.Root, "agents", name)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "worker.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write agent worker: %v", err)
	}
}

func discoveredColonyNames(home hivehome.Home) map[string]struct{} {
	names := map[string]struct{}{}
	for _, entries := range agents.DiscoverAgents(home.ColoniesDir()) {
		for _, entry := range entries {
			names[filepath.Base(entry.Path)] = struct{}{}
		}
	}
	return names
}

// Parity: framework/server/tests/test_colony_delete.py::test_soft_delete_hides_from_discover_but_keeps_files
func TestSoftDeleteHidesFromDiscoverButKeepsFiles(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "alpha", false)
	if _, ok := discoveredColonyNames(home)["alpha"]; !ok {
		t.Fatalf("alpha should be discoverable before delete")
	}
	recorder, body := serveColonyRequest(colonyMux(deps), httptest.NewRequest(http.MethodDelete, "/api/colonies/alpha", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if body["deleted"] != "alpha" || body["purged"] != false {
		t.Fatalf("body = %v", body)
	}
	if _, ok := discoveredColonyNames(home)["alpha"]; ok {
		t.Fatalf("alpha should be hidden after soft delete")
	}
	if !isColonySoftDeleted(home, "alpha") {
		t.Fatalf("metadata deleted flag missing")
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "alpha", "marker.txt")); string(data) != "alpha" {
		t.Fatalf("marker = %q", data)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "agents", "alpha", "worker.json")); err != nil {
		t.Fatalf("agent worker missing: %v", err)
	}
}

// Parity: framework/server/tests/test_colony_delete.py::test_purge_removes_both_dirs
func TestPurgeRemovesBothDirs(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "beta", false)
	recorder, body := serveColonyRequest(colonyMux(deps), httptest.NewRequest(http.MethodDelete, "/api/colonies/beta?purge=true", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["purged"] != true {
		t.Fatalf("body = %v", body)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "beta")); !os.IsNotExist(err) {
		t.Fatalf("colony dir should be removed")
	}
	if _, err := os.Stat(filepath.Join(home.Root, "agents", "beta")); !os.IsNotExist(err) {
		t.Fatalf("agent dir should be removed")
	}
	if _, ok := discoveredColonyNames(home)["beta"]; ok {
		t.Fatalf("beta should not be discoverable")
	}
}

// Parity: framework/server/tests/test_colony_delete.py::test_delete_missing_colony_is_idempotent
func TestDeleteMissingColonyIsIdempotent(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	mux := colonyMux(deps)
	softRecorder, softBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodDelete, "/api/colonies/ghost", nil))
	if softRecorder.Code != http.StatusOK || softBody["purged"] != false {
		t.Fatalf("soft = %d %v", softRecorder.Code, softBody)
	}
	purgeRecorder, purgeBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodDelete, "/api/colonies/ghost?purge=true", nil))
	if purgeRecorder.Code != http.StatusOK || purgeBody["purged"] != true {
		t.Fatalf("purge = %d %v", purgeRecorder.Code, purgeBody)
	}
}

// Parity: framework/server/tests/test_colony_delete.py::test_purge_only_present_dir_is_removed
func TestPurgeOnlyPresentDirIsRemoved(t *testing.T) {
	deps, home := colonyTestDeps(t)
	agentDir := filepath.Join(home.Root, "agents", "gamma")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "worker.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder, _ := serveColonyRequest(colonyMux(deps), httptest.NewRequest(http.MethodDelete, "/api/colonies/gamma?purge=true", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("agent dir should be removed")
	}
}

// Parity: framework/server/tests/test_colony_delete.py::test_invalid_colony_id_rejected
func TestInvalidColonyIDRejected(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	recorder, _ := serveColonyRequest(colonyMux(deps), httptest.NewRequest(http.MethodDelete, "/api/colonies/BadName", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colony_rename_soft_deleted.py::test_vacate_parks_soft_deleted_colony
func TestVacateParksSoftDeletedColony(t *testing.T) {
	_, home := colonyTestDeps(t)
	makeRouteColony(t, home, "ghost", true)
	parked, err := vacateSoftDeletedColony(home, "ghost")
	if err != nil || parked != "ghost.deleted.1" {
		t.Fatalf("parked = %q, err %v", parked, err)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "ghost")); !os.IsNotExist(err) {
		t.Fatalf("ghost should be moved")
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "ghost.deleted.1", "marker.txt")); string(data) != "ghost" {
		t.Fatalf("parked marker = %q", data)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "agents", "ghost.deleted.1", "worker.json")); err != nil {
		t.Fatalf("parked agent missing: %v", err)
	}
}

// Parity: framework/server/tests/test_colony_rename_soft_deleted.py::test_vacate_is_noop_for_live_colony
func TestVacateIsNoopForLiveColony(t *testing.T) {
	_, home := colonyTestDeps(t)
	makeRouteColony(t, home, "alive", false)
	parked, err := vacateSoftDeletedColony(home, "alive")
	if err != nil || parked != "" {
		t.Fatalf("parked = %q, err %v", parked, err)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "alive")); err != nil {
		t.Fatalf("live colony moved: %v", err)
	}
}

// Parity: framework/server/tests/test_colony_rename_soft_deleted.py::test_vacate_is_noop_for_missing_colony
func TestVacateIsNoopForMissingColony(t *testing.T) {
	_, home := colonyTestDeps(t)
	parked, err := vacateSoftDeletedColony(home, "nope")
	if err != nil || parked != "" {
		t.Fatalf("parked = %q, err %v", parked, err)
	}
}

// Parity: framework/server/tests/test_colony_rename_soft_deleted.py::test_vacate_picks_next_free_suffix
func TestVacatePicksNextFreeSuffix(t *testing.T) {
	_, home := colonyTestDeps(t)
	makeRouteColony(t, home, "ghost", true)
	if err := os.MkdirAll(filepath.Join(home.ColoniesDir(), "ghost.deleted.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	parked, err := vacateSoftDeletedColony(home, "ghost")
	if err != nil || parked != "ghost.deleted.2" {
		t.Fatalf("parked = %q, err %v", parked, err)
	}
}

// Parity: framework/server/tests/test_colony_rename_soft_deleted.py::test_rename_onto_soft_deleted_name_succeeds
func TestRenameOntoSoftDeletedNameSucceeds(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "keep", false)
	makeRouteColony(t, home, "taken", true)
	recorder, body := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/keep/rename", map[string]any{"new_name": "taken"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if body["renamed"] != true || body["old_name"] != "keep" || body["new_name"] != "taken" {
		t.Fatalf("body = %v", body)
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "taken", "marker.txt")); string(data) != "keep" {
		t.Fatalf("marker = %q", data)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "agents", "taken", "worker.json")); err != nil {
		t.Fatalf("agent worker missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "keep")); !os.IsNotExist(err) {
		t.Fatalf("old colony dir should be gone")
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "taken.deleted.1", "marker.txt")); string(data) != "taken" {
		t.Fatalf("parked marker = %q", data)
	}
}

// Parity: framework/server/tests/test_colony_rename_soft_deleted.py::test_rename_onto_live_name_still_conflicts
func TestRenameOntoLiveNameStillConflicts(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "keep", false)
	makeRouteColony(t, home, "live", false)
	recorder, _ := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/keep/rename", map[string]any{"new_name": "live"}))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "keep")); err != nil {
		t.Fatalf("keep should survive: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "live", "marker.txt")); string(data) != "live" {
		t.Fatalf("live marker = %q", data)
	}
}

// Parity: framework/server/tests/test_colony_rename_soft_deleted.py::test_rename_onto_soft_deleted_name_succeeds
// Go-only assertion: sessions bound to the old colony dir are stopped before the move.
func TestRenameStopsSessionsUnderOldColony(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "keep", false)
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "s1", WorkerPath: filepath.Join(home.Root, "agents", "keep", "worker")})
	deps.Manager = manager
	recorder, _ := serveColonyRequest(colonyMux(deps), colonyRequest(t, http.MethodPost, "/api/colonies/keep/rename", map[string]any{"new_name": "moved"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if manager.GetSession("s1") != nil {
		t.Fatalf("session bound to old colony dir should be stopped")
	}
}

// Parity-exempt: 上游无 reveal 路由测试；用注入的 launcher seam 钉住路径与错误分支。
func TestRevealColonyFolderOpensResolvedDir(t *testing.T) {
	deps, home := colonyTestDeps(t)
	makeRouteColony(t, home, "acme", false)
	var opened string
	deps.Reveal = func(path string) error {
		opened = path
		return nil
	}
	mux := colonyMux(deps)
	recorder, body := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/acme/reveal", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if opened != filepath.Join(home.ColoniesDir(), "acme") || body["path"] != opened {
		t.Fatalf("opened = %q, body = %v", opened, body)
	}

	missingRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/ghost/reveal", nil))
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", missingRecorder.Code)
	}
	invalidRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/Bad/reveal", nil))
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want 400", invalidRecorder.Code)
	}
	deps.Reveal = func(string) error { return errors.New("launcher boom") }
	failRecorder, failBody := serveColonyRequest(colonyMux(deps), httptest.NewRequest(http.MethodPost, "/api/colonies/acme/reveal", nil))
	if failRecorder.Code != http.StatusInternalServerError || failBody["error"] != "launcher boom" {
		t.Fatalf("failure = %d %v", failRecorder.Code, failBody)
	}
}

// Parity-exempt: 上游 _ensure_minimal_colony 无 Python 测试；此用例钉住 scaffold 的落盘形态。
func TestScaffoldColonyCreatesMinimalFiles(t *testing.T) {
	deps, home := colonyTestDeps(t)
	mux := colonyMux(deps)
	recorder, body := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/new_colony/scaffold", map[string]any{"queen_name": "queen_x"}))
	if recorder.Code != http.StatusOK || body["scaffolded"] != true || body["colony_id"] != "new_colony" {
		t.Fatalf("status = %d, body %v", recorder.Code, body)
	}
	meta, err := readJSONObject(filepath.Join(home.ColoniesDir(), "new_colony", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if meta["name"] != "new_colony" || meta["queen_name"] != "queen_x" || meta["scaffolded"] != true {
		t.Fatalf("metadata = %v", meta)
	}
	worker, err := readJSONObject(filepath.Join(home.ColoniesDir(), "new_colony", "worker.json"))
	if err != nil {
		t.Fatal(err)
	}
	if worker["name"] != "New Colony" || worker["description"] != "Colony new_colony" {
		t.Fatalf("worker = %v", worker)
	}
	goal, _ := worker["goal"].(map[string]any)
	if goal["description"] != "Work on tasks for colony new_colony" {
		t.Fatalf("goal = %v", worker["goal"])
	}

	invalidRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/Bad/scaffold", nil))
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", invalidRecorder.Code)
	}
}

// Parity: framework/server/session_manager.py::_ensure_minimal_colony
func TestScaffoldColonyCreatesDurableLayout(t *testing.T) {
	deps, home := colonyTestDeps(t)
	mux := colonyMux(deps)
	recorder, _ := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/full_layout/scaffold", map[string]any{}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	colonyDir := filepath.Join(home.ColoniesDir(), "full_layout")
	for _, dir := range []string{"skills", "runs"} {
		info, err := os.Stat(filepath.Join(colonyDir, dir))
		if err != nil || !info.IsDir() {
			t.Fatalf("scaffold did not create %s/: %v", dir, err)
		}
	}
	triggersData, err := os.ReadFile(filepath.Join(colonyDir, "triggers.json"))
	if err != nil {
		t.Fatalf("scaffold did not create triggers.json: %v", err)
	}
	var triggers any
	if err := json.Unmarshal(triggersData, &triggers); err != nil {
		t.Fatalf("triggers.json is not JSON: %v", err)
	}
	if list, ok := triggers.([]any); !ok || len(list) != 0 {
		t.Fatalf("triggers.json = %#v, want []", triggers)
	}

	// A second scaffold is idempotent: existing skill files and trigger
	// definitions are never overwritten.
	skillPath := filepath.Join(colonyDir, "skills", "keep_me.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: keep_me\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	savedTrigger := []map[string]any{{"id": "timer_1", "trigger_type": "timer", "enabled": true}}
	if err := triggers_store.WriteAgentTriggersJSON(colonyDir, savedTrigger); err != nil {
		t.Fatal(err)
	}
	recorder, _ = serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/full_layout/scaffold", map[string]any{}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("second scaffold status = %d", recorder.Code)
	}
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("scaffold removed an existing skill: %v", err)
	}
	kept := triggers_store.ReadAgentTriggersJSON(colonyDir)
	if len(kept) != 1 || kept[0]["id"] != "timer_1" {
		t.Fatalf("scaffold overwrote triggers.json: %#v", kept)
	}
}

func colonyWithMetadata(t *testing.T, home hivehome.Home, name string) {
	t.Helper()
	dir := filepath.Join(home.ColoniesDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"colony_id": name, "queen_name": "queen_technology"})
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

type fakeProfileColony struct {
	bound map[string][]string
}

func (f *fakeProfileColony) StreamID() string { return "colony:acme" }

func (f *fakeProfileColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "", nil
}

func (f *fakeProfileColony) InjectInput(string, string) error { return nil }

func (f *fakeProfileColony) BoundWorkersForProfile(profile string) []string {
	return f.bound[profile]
}

// Parity-exempt: 上游 worker_profiles 路由无 Python 测试；此用例钉住默认档、增删改与 404。
func TestWorkerProfilesRoundTrip(t *testing.T) {
	deps, home := colonyTestDeps(t)
	colonyWithMetadata(t, home, "acme")
	mux := colonyMux(deps)

	listRecorder, listBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colonies/acme/worker_profiles", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list = %d", listRecorder.Code)
	}
	profiles, _ := listBody["worker_profiles"].([]any)
	if len(profiles) != 1 || profiles[0].(map[string]any)["name"] != "default" {
		t.Fatalf("default profiles = %v", profiles)
	}

	upsertRecorder, upsertBody := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/acme/worker_profiles", map[string]any{
		"name":             "slack_work",
		"task":             "handle slack",
		"integrations":     map[string]any{"slack": "work"},
		"concurrency_hint": 2,
	}))
	if upsertRecorder.Code != http.StatusCreated {
		t.Fatalf("upsert = %d, body %s", upsertRecorder.Code, upsertRecorder.Body.String())
	}
	upserted, _ := upsertBody["worker_profiles"].([]any)
	if len(upserted) != 2 {
		t.Fatalf("profiles after upsert = %v", upserted)
	}
	meta, _ := readJSONObject(filepath.Join(home.ColoniesDir(), "acme", "metadata.json"))
	if !strings.Contains(string(mustJSON(t, meta["worker_profiles"])), "slack_work") {
		t.Fatalf("metadata worker_profiles = %v", meta["worker_profiles"])
	}

	updateRecorder, updateBody := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/acme/worker_profiles", map[string]any{"name": "slack_work", "task": "updated"}))
	if updateRecorder.Code != http.StatusCreated {
		t.Fatalf("update = %d", updateRecorder.Code)
	}
	updated, _ := updateBody["worker_profiles"].([]any)
	if len(updated) != 2 {
		t.Fatalf("upsert duplicated profile: %v", updated)
	}
	if updated[1].(map[string]any)["task"] != "updated" {
		t.Fatalf("task not updated: %v", updated[1])
	}

	invalidRecorder, _ := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/acme/worker_profiles", map[string]any{"name": "Bad Name"}))
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid name = %d", invalidRecorder.Code)
	}
	nonObjectRecorder, nonObjectBody := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/acme/worker_profiles", []int{1}))
	if nonObjectRecorder.Code != http.StatusBadRequest || nonObjectBody["error"] != "body must be a JSON object" {
		t.Fatalf("non-object = %d %v", nonObjectRecorder.Code, nonObjectBody)
	}

	defaultDelete, defaultBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodDelete, "/api/colonies/acme/worker_profiles/default", nil))
	if defaultDelete.Code != http.StatusBadRequest || defaultBody["error"] != "cannot delete the default worker profile" {
		t.Fatalf("default delete = %d %v", defaultDelete.Code, defaultBody)
	}
	deleteRecorder, deleteBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodDelete, "/api/colonies/acme/worker_profiles/slack_work", nil))
	if deleteRecorder.Code != http.StatusOK || deleteBody["deleted"] != true {
		t.Fatalf("delete = %d %v", deleteRecorder.Code, deleteBody)
	}
	missingRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodDelete, "/api/colonies/acme/worker_profiles/nope", nil))
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing delete = %d", missingRecorder.Code)
	}
	unknownColony, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colonies/ghost/worker_profiles", nil))
	if unknownColony.Code != http.StatusNotFound {
		t.Fatalf("unknown colony = %d", unknownColony.Code)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// Parity-exempt: Go 侧装配冒烟；确认三组路由可注册到同一 mux 且互不冲突。
func TestColonyRouteFamiliesCoexistOnOneMux(t *testing.T) {
	mux := http.NewServeMux()
	deps, home := colonyTestDeps(t)
	RegisterColonyRoutes(mux, deps)
	RegisterColonyToolRoutes(mux, ColonyToolsDeps{Home: home})
	RegisterExecutionRoutes(mux, ExecutionRouteDeps{Home: home, Manager: NewSessionManager()})

	probes := []struct {
		method string
		target string
		want   int
	}{
		{http.MethodGet, "/api/colonies/tools-index", http.StatusOK},
		{http.MethodGet, "/api/colony/ghost/tools", http.StatusNotFound},
		{http.MethodPost, "/api/sessions/sid-1/presence", http.StatusNotFound},
		{http.MethodPost, "/api/colonies/ghost/rename", http.StatusBadRequest},
		{http.MethodDelete, "/api/colonies/ghost", http.StatusOK},
	}
	for _, probe := range probes {
		recorder, _ := serveColonyRequest(mux, httptest.NewRequest(probe.method, probe.target, strings.NewReader("{}")))
		if recorder.Code != probe.want {
			t.Fatalf("%s %s = %d, want %d", probe.method, probe.target, recorder.Code, probe.want)
		}
	}
}

// Parity-exempt: 上游 worker_profiles 路由无 Python 测试；此用例钉住绑定中的 worker 拒绝删除。
func TestWorkerProfileDeleteBoundWorkerConflict(t *testing.T) {
	deps, home := colonyTestDeps(t)
	colonyWithMetadata(t, home, "acme")
	profileStore := []WorkerProfile{{Name: defaultWorkerProfileName}, {Name: "slack_work"}}
	if _, err := saveWorkerProfiles(home, "acme", profileStore); err != nil {
		t.Fatal(err)
	}
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "s1", ColonyID: "acme", Colony: &fakeProfileColony{bound: map[string][]string{"slack_work": {"worker-1"}}}})
	deps.Manager = manager
	recorder, body := serveColonyRequest(colonyMux(deps), httptest.NewRequest(http.MethodDelete, "/api/colonies/acme/worker_profiles/slack_work", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	bound, _ := body["bound_workers"].([]any)
	if len(bound) != 1 || bound[0] != "worker-1" {
		t.Fatalf("bound_workers = %v", body)
	}
}

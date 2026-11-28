package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

type fakeColonyWorkerSurface struct {
	workers     []WorkerInfoView
	batches     map[string]WorkerBatchView
	colonyID    string
	storagePath string
	skills      []*skills.ParsedSkill
	skillsOK    bool
	tools       []ColonyTool
	providers   map[string]string
}

func (f *fakeColonyWorkerSurface) ListWorkers() []WorkerInfoView { return f.workers }

func (f *fakeColonyWorkerSurface) Worker(workerID string) (WorkerInfoView, WorkerBatchView, bool) {
	for _, info := range f.workers {
		if info.ID == workerID {
			return info, f.batches[workerID], true
		}
	}
	return WorkerInfoView{}, WorkerBatchView{}, false
}

func (f *fakeColonyWorkerSurface) ColonyID() string    { return f.colonyID }
func (f *fakeColonyWorkerSurface) StoragePath() string { return f.storagePath }

func (f *fakeColonyWorkerSurface) Skills() ([]*skills.ParsedSkill, bool) {
	return f.skills, f.skillsOK
}

func (f *fakeColonyWorkerSurface) Tools() ([]ColonyTool, map[string]string) {
	return f.tools, f.providers
}

func colonyWorkerTestDeps(t *testing.T, surface ColonyWorkerReadSurface) (ColonyWorkerDeps, *SessionManager, hivehome.Home) {
	t.Helper()
	home := serverTestHome(t)
	manager := NewSessionManager()
	deps := ColonyWorkerDeps{
		Manager: manager,
		Home:    home,
		Tasks:   tasks.NewTaskStore(home.Root),
		Runtime: func(*Session) ColonyWorkerReadSurface { return surface },
	}
	return deps, manager, home
}

func colonyWorkerDo(t *testing.T, deps ColonyWorkerDeps, method, target string, body any) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterColonyWorkerRoutes(mux, deps)
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder, recorder.Body.Bytes()
}

func decodeColonyWorkerBody(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode response %q: %v", string(raw), err)
	}
}

// Parity: framework/server/tests/test_colony_worker_routes.py::test_inspect_live_worker_includes_batch_and_tasks
func TestInspectLiveWorkerIncludesBatchAndTasks(t *testing.T) {
	batch := WorkerBatchView{BatchID: "batch_1", BatchIndex: 2, BatchSize: 5}
	surface := &fakeColonyWorkerSurface{
		colonyID: "test_colony",
		workers: []WorkerInfoView{{
			ID:          "w1",
			Task:        "do a thing",
			Status:      colony.WorkerRunning,
			StartedAt:   1000.0,
			ProfileName: "profile-x",
			Batch:       batch,
		}},
		batches: map[string]WorkerBatchView{"w1": batch},
	}
	deps, manager, _ := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/w1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}

	var payload struct {
		Worker struct {
			WorkerID string `json:"worker_id"`
			Task     string `json:"task"`
			Status   string `json:"status"`
			Batch    struct {
				BatchID    string `json:"batch_id"`
				BatchIndex int    `json:"batch_index"`
				BatchSize  int    `json:"batch_size"`
				WorkerSeq  int    `json:"worker_seq"`
			} `json:"batch"`
			Tasks []map[string]any `json:"tasks"`
			Goal  *string          `json:"goal"`
		} `json:"worker"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	worker := payload.Worker
	if worker.WorkerID != "w1" {
		t.Errorf("worker_id = %q, want w1", worker.WorkerID)
	}
	if worker.Status != "running" {
		t.Errorf("status = %q, want running", worker.Status)
	}
	if worker.Batch.BatchID != "batch_1" || worker.Batch.BatchIndex != 2 || worker.Batch.BatchSize != 5 || worker.Batch.WorkerSeq != 0 {
		t.Errorf("batch = %+v, want batch_1/2/5/0", worker.Batch)
	}
	if len(worker.Tasks) != 0 {
		t.Errorf("tasks = %v, want empty", worker.Tasks)
	}
	if worker.Tasks == nil {
		t.Errorf("tasks = null, want []")
	}
	if worker.Goal != nil {
		t.Errorf("goal = %v, want null", *worker.Goal)
	}
}

// Parity: framework/server/tests/test_colony_worker_routes.py::test_inspect_unknown_worker_returns_404
func TestInspectUnknownWorkerReturns404(t *testing.T) {
	surface := &fakeColonyWorkerSurface{colonyID: "test_colony"}
	deps, manager, _ := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/ghost", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", recorder.Code, string(raw))
	}
	var body map[string]any
	decodeColonyWorkerBody(t, raw, &body)
	if body["error"] != "unknown worker: ghost" {
		t.Errorf("error = %v, want unknown worker: ghost", body["error"])
	}
}

// Parity: framework/server/tests/test_colony_worker_routes.py::test_inspect_historical_worker_reads_disk
func TestInspectHistoricalWorkerReadsDisk(t *testing.T) {
	surface := &fakeColonyWorkerSurface{colonyID: "test_colony"}
	deps, manager, home := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	workerDir := filepath.Join(home.ColoniesDir(), "test_colony", "workers", "w_old")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	writeServerTestJSON(t, filepath.Join(workerDir, "meta.json"), map[string]any{
		"task":         "scrape page 7",
		"spawned_at":   1234.0,
		"profile_name": "profile-x",
		"batch_id":     "batch_9",
		"batch_index":  1,
		"batch_size":   4,
	})
	writeServerTestJSON(t, filepath.Join(workerDir, "result.json"), map[string]any{
		"status":  "success",
		"summary": "done",
	})

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/w_old", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}

	var payload struct {
		Worker struct {
			WorkerID string         `json:"worker_id"`
			Status   string         `json:"status"`
			Task     string         `json:"task"`
			Result   map[string]any `json:"result"`
			Batch    struct {
				BatchID string `json:"batch_id"`
			} `json:"batch"`
		} `json:"worker"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	worker := payload.Worker
	if worker.WorkerID != "w_old" {
		t.Errorf("worker_id = %q, want w_old", worker.WorkerID)
	}
	if worker.Status != "historical" {
		t.Errorf("status = %q, want historical", worker.Status)
	}
	if worker.Task != "scrape page 7" {
		t.Errorf("task = %q, want scrape page 7", worker.Task)
	}
	if worker.Result["status"] != "success" || worker.Result["summary"] != "done" {
		t.Errorf("result = %v, want status/summary passthrough", worker.Result)
	}
	if worker.Batch.BatchID != "batch_9" {
		t.Errorf("batch_id = %q, want batch_9", worker.Batch.BatchID)
	}
}

func writeColonyWorkerParts(t *testing.T, partsDir string, parts []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatalf("mkdir parts dir: %v", err)
	}
	for _, part := range parts {
		seq, _ := part["seq"].(int)
		name := padPartIndex(seq)
		writeServerTestJSON(t, filepath.Join(partsDir, name), part)
	}
}

// Parity: framework/server/tests/test_colony_worker_routes.py::test_worker_conversation_returns_transcript
func TestWorkerConversationReturnsTranscript(t *testing.T) {
	surface := &fakeColonyWorkerSurface{colonyID: "test_colony"}
	deps, manager, home := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	partsDir := filepath.Join(home.ColoniesDir(), "test_colony", "workers", "w_conv", "conversations", "parts")
	writeColonyWorkerParts(t, partsDir, []map[string]any{
		{"seq": 0, "role": "user", "content": "go scrape page 7"},
		{"seq": 1, "role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"function": map[string]any{"name": "browser_click", "arguments": `{"sel": "a"}`}},
		}},
		{"seq": 2, "role": "tool", "content": `{"ok": true}`, "tool_use_id": "call_1"},
	})

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/w_conv/conversation", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}

	var body struct {
		WorkerID  string `json:"worker_id"`
		Total     int    `json:"total"`
		Truncated bool   `json:"truncated"`
		Messages  []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"tool_calls"`
			ToolUseID string `json:"tool_use_id"`
		} `json:"messages"`
	}
	decodeColonyWorkerBody(t, raw, &body)
	if body.WorkerID != "w_conv" {
		t.Errorf("worker_id = %q, want w_conv", body.WorkerID)
	}
	if body.Total != 3 {
		t.Errorf("total = %d, want 3", body.Total)
	}
	if body.Truncated {
		t.Errorf("truncated = true, want false")
	}
	roles := []string{}
	for _, message := range body.Messages {
		roles = append(roles, message.Role)
	}
	if len(roles) != 3 || roles[0] != "user" || roles[1] != "assistant" || roles[2] != "tool" {
		t.Fatalf("roles = %v, want [user assistant tool]", roles)
	}
	if len(body.Messages[1].ToolCalls) != 1 || body.Messages[1].ToolCalls[0].Name != "browser_click" ||
		body.Messages[1].ToolCalls[0].Arguments != `{"sel": "a"}` {
		t.Errorf("tool_calls = %+v, want flattened browser_click", body.Messages[1].ToolCalls)
	}
	if body.Messages[2].ToolUseID != "call_1" {
		t.Errorf("tool_use_id = %q, want call_1", body.Messages[2].ToolUseID)
	}
}

// Parity: framework/server/tests/test_colony_worker_routes.py::test_worker_conversation_unknown_returns_404
func TestWorkerConversationUnknownReturns404(t *testing.T) {
	surface := &fakeColonyWorkerSurface{colonyID: "test_colony"}
	deps, manager, _ := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/ghost/conversation", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", recorder.Code, string(raw))
	}
}

// Parity: framework/server/tests/test_colony_worker_routes.py::test_inspect_live_worker_surfaces_seeded_goal_and_tasks
func TestInspectLiveWorkerSurfacesSeededGoalAndTasks(t *testing.T) {
	surface := &fakeColonyWorkerSurface{
		colonyID: "test_colony",
		workers: []WorkerInfoView{{
			ID:     "w1",
			Task:   "do a thing",
			Status: colony.WorkerRunning,
		}},
	}
	deps, manager, home := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	if err := os.MkdirAll(filepath.Join(home.ColoniesDir(), "test_colony", "workers", "w1"), 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	store := tasks.NewTaskStore(home.Root)
	deps.Tasks = store
	if err := store.SetGoal("w1", "Checking 6 Instagram profiles"); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, err := store.CreateTasksBatch("w1", []tasks.TaskSpec{{Subject: "triage"}}, nil); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/w1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}

	var payload struct {
		Worker struct {
			Goal  *string `json:"goal"`
			Tasks []struct {
				Subject string `json:"subject"`
			} `json:"tasks"`
		} `json:"worker"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if payload.Worker.Goal == nil || *payload.Worker.Goal != "Checking 6 Instagram profiles" {
		t.Errorf("goal = %v, want seeded goal", payload.Worker.Goal)
	}
	if len(payload.Worker.Tasks) != 1 || payload.Worker.Tasks[0].Subject != "triage" {
		t.Errorf("tasks = %+v, want [triage]", payload.Worker.Tasks)
	}
}

// Parity: framework/server/tests/test_colony_worker_routes.py::test_completed_in_memory_worker_keeps_goal_in_list
func TestCompletedInMemoryWorkerKeepsGoalInList(t *testing.T) {
	surface := &fakeColonyWorkerSurface{
		colonyID: "test_colony",
		workers: []WorkerInfoView{{
			ID:     "w_done",
			Task:   "do a thing",
			Status: colony.WorkerCompleted,
		}},
	}
	deps, manager, home := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	if err := os.MkdirAll(filepath.Join(home.ColoniesDir(), "test_colony", "workers", "w_done"), 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	store := tasks.NewTaskStore(home.Root)
	deps.Tasks = store
	if err := store.SetGoal("w_done", "Reviewing 6 Instagram leads"); err != nil {
		t.Fatalf("set goal: %v", err)
	}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}

	var payload struct {
		Workers []struct {
			Status      string          `json:"status"`
			Goal        *string         `json:"goal"`
			TaskSummary json.RawMessage `json:"task_summary"`
		} `json:"workers"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if len(payload.Workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(payload.Workers))
	}
	worker := payload.Workers[0]
	if worker.Status != "completed" {
		t.Errorf("status = %q, want completed", worker.Status)
	}
	if worker.Goal == nil || *worker.Goal != "Reviewing 6 Instagram leads" {
		t.Errorf("goal = %v, want seeded goal", worker.Goal)
	}
	if len(worker.TaskSummary) != 0 {
		t.Errorf("task_summary = %s, want absent for a completed worker", string(worker.TaskSummary))
	}
}

// Parity-exempt: upstream only drives the in-memory list; the colony/ and legacy
// workers/ disk walks are covered here because no Python test exercises them.
func TestListWorkersMergesColonyAndLegacyDisk(t *testing.T) {
	surface := &fakeColonyWorkerSurface{
		colonyID: "test_colony",
		workers: []WorkerInfoView{{
			ID:     "w_known",
			Task:   "known in memory",
			Status: colony.WorkerCompleted,
		}},
	}
	deps, manager, home := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	colonyWorkers := filepath.Join(home.ColoniesDir(), "test_colony", "workers")
	mine := filepath.Join(colonyWorkers, "w_mine")
	other := filepath.Join(colonyWorkers, "w_other")
	known := filepath.Join(colonyWorkers, "w_known")
	for _, dir := range []string{mine, other, known} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeServerTestJSON(t, filepath.Join(mine, "meta.json"), map[string]any{
		"task":             "mine",
		"queen_session_id": "s1",
		"spawned_at":       1500.0,
		"goal":             "My goal",
		"batch_id":         "batch_7",
	})
	writeServerTestJSON(t, filepath.Join(other, "meta.json"), map[string]any{
		"task":             "other",
		"queen_session_id": "s2",
	})
	writeServerTestJSON(t, filepath.Join(known, "meta.json"), map[string]any{
		"task":             "known",
		"queen_session_id": "s1",
	})

	legacyWorkers := filepath.Join(home.Root, "queens", "q1", "sessions", "s1", "workers")
	legacy := filepath.Join(legacyWorkers, "w_legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy worker: %v", err)
	}
	legacyParts := filepath.Join(legacy, "conversations", "parts")
	writeColonyWorkerParts(t, legacyParts, []map[string]any{
		{"seq": 0, "role": "user", "content": "hello"},
		{"seq": 1, "role": "user", "content": "legacy task"},
	})

	session := NewSession("s1", nil)
	session.QueenDir = filepath.Join(home.Root, "queens", "q1", "sessions", "s1")
	manager.AddSession(session)

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}

	var payload struct {
		Workers []struct {
			WorkerID string `json:"worker_id"`
			Task     string `json:"task"`
			Status   string `json:"status"`
			Goal     any    `json:"goal"`
		} `json:"workers"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	found := map[string]struct {
		task   string
		status string
	}{}
	for _, worker := range payload.Workers {
		found[worker.WorkerID] = struct {
			task   string
			status string
		}{task: worker.Task, status: worker.Status}
	}
	if _, ok := found["w_other"]; ok {
		t.Errorf("w_other belongs to another queen session and must be filtered out")
	}
	if entry, ok := found["w_mine"]; !ok || entry.status != "historical" || entry.task != "mine" {
		t.Errorf("w_mine = %+v, want historical/mine", entry)
	}
	if entry, ok := found["w_known"]; !ok || entry.status != "completed" || entry.task != "known in memory" {
		t.Errorf("w_known = %+v, want the in-memory completed entry once", entry)
	}
	if entry, ok := found["w_legacy"]; !ok || entry.status != "historical" || entry.task != "legacy task" {
		t.Errorf("w_legacy = %+v, want historical/legacy task", entry)
	}
}

// Parity-exempt: no upstream test drives GET /colony/skills; upstream serializes
// ParsedSkill fields in the catalog's insertion order, pinned here.
func TestColonySkillsRouteShapesAndSorts(t *testing.T) {
	surface := &fakeColonyWorkerSurface{
		skillsOK: true,
		skills: []*skills.ParsedSkill{
			{Name: "zeta", Description: "z", Location: "/z/SKILL.md", BaseDir: "/z", SourceScope: "preset"},
			{Name: "alpha", Description: "a", Location: "/a/SKILL.md", BaseDir: "/a", SourceScope: "framework"},
		},
	}
	deps, manager, _ := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/colony/skills", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		Skills []map[string]any `json:"skills"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if len(payload.Skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(payload.Skills))
	}
	if payload.Skills[0]["name"] != "alpha" || payload.Skills[1]["name"] != "zeta" {
		t.Errorf("skills order = %v, want sorted by name", payload.Skills)
	}
	if payload.Skills[0]["base_dir"] != "/a" || payload.Skills[0]["source_scope"] != "framework" {
		t.Errorf("skill fields = %v, want base_dir/source_scope surfaced", payload.Skills[0])
	}
}

// Parity-exempt: no upstream test drives GET /colony/skills without a runtime
// catalog; this pins the {"skills": []} degradation.
func TestColonySkillsRouteWithoutCatalog(t *testing.T) {
	surface := &fakeColonyWorkerSurface{skillsOK: false}
	deps, manager, _ := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/colony/skills", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		Skills []map[string]any `json:"skills"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if payload.Skills == nil || len(payload.Skills) != 0 {
		t.Errorf("skills = %v, want []", payload.Skills)
	}
}

// Parity-exempt: no upstream test drives GET /colony/tools; pins the
// credential-provider mapping and the system-tool tag.
func TestColonyToolsRouteMapsProviders(t *testing.T) {
	surface := &fakeColonyWorkerSurface{
		tools: []ColonyTool{
			{Name: "hubspot_search", Description: "search"},
			{Name: "read_file", Description: "read"},
			{Name: "get_current_time", Description: "clock"},
		},
		providers: map[string]string{"hubspot_search": "hubspot"},
	}
	deps, manager, _ := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/colony/tools", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		Tools []struct {
			Name     string  `json:"name"`
			Provider *string `json:"provider"`
		} `json:"tools"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if len(payload.Tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(payload.Tools))
	}
	if payload.Tools[0].Name != "get_current_time" || payload.Tools[0].Provider == nil || *payload.Tools[0].Provider != "system" {
		t.Errorf("tools[0] = %+v, want get_current_time/system", payload.Tools[0])
	}
	providers := map[string]any{}
	for _, tool := range payload.Tools {
		if tool.Provider == nil {
			providers[tool.Name] = nil
			continue
		}
		providers[tool.Name] = *tool.Provider
	}
	if providers["hubspot_search"] != "hubspot" {
		t.Errorf("hubspot_search provider = %v, want hubspot", providers["hubspot_search"])
	}
	if value, ok := providers["read_file"]; !ok || value != nil {
		t.Errorf("read_file provider = %v, want null", value)
	}
}

// Parity-exempt: no upstream test drives the routes without a loaded colony
// runtime; this pins resolve_session's 404 and the empty-list degradation.
func TestColonyWorkerRoutesWithoutRuntime(t *testing.T) {
	home := serverTestHome(t)
	manager := NewSessionManager()
	deps := ColonyWorkerDeps{Manager: manager, Home: home, Tasks: tasks.NewTaskStore(home.Root)}

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/missing/workers", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", recorder.Code, string(raw))
	}
	var missing map[string]any
	decodeColonyWorkerBody(t, raw, &missing)
	if missing["error"] != "Session 'missing' not found" {
		t.Errorf("error = %v, want Session 'missing' not found", missing["error"])
	}

	manager.AddSession(NewSession("s1", nil))
	recorder, raw = colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var payload struct {
		Workers []any `json:"workers"`
	}
	decodeColonyWorkerBody(t, raw, &payload)
	if payload.Workers == nil || len(payload.Workers) != 0 {
		t.Errorf("workers = %v, want []", payload.Workers)
	}
}

// Parity: framework/server/routes_colony_workers.py::_walk_colony_workers
// (attribution by meta.json queen_session_id, started_at from spawned_at,
// batch coordinates from batch_index/batch_size/worker_seq)
//
// Worker surface: the meta.json the Go colony runtime writes must be readable
// by the routes that render a terminated worker — otherwise a worker that ran
// fine disappears from the dashboard the moment its runtime is gone.
func TestWorkerSurfaceReadsColonyMetaLineage(t *testing.T) {
	surface := &fakeColonyWorkerSurface{colonyID: "test_colony"}
	deps, manager, home := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	workerDir := filepath.Join(home.ColoniesDir(), "test_colony", "workers", "w_lineage")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	// Exactly the shape writeWorkerMeta produces.
	writeServerTestJSON(t, filepath.Join(workerDir, "meta.json"), map[string]any{
		"worker_id":        "w_lineage",
		"queen_session_id": "s1",
		"queen_name":       "queen_growth",
		"colony_id":        "test_colony",
		"spawned_at":       1700000000.25,
		"task":             "scrape page 7",
		"profile_name":     "researcher",
		"goal":             "Scraping page 7",
		"batch_id":         "rpw_20260101T000000Z_abcd1234",
		"batch_index":      2,
		"batch_size":       3,
		"worker_seq":       5,
	})
	writeServerTestJSON(t, filepath.Join(workerDir, "result.json"), map[string]any{
		"status":  "success",
		"summary": "done",
	})

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var list struct {
		Workers []struct {
			WorkerID       string  `json:"worker_id"`
			Status         string  `json:"status"`
			StartedAt      float64 `json:"started_at"`
			QueenSessionID string  `json:"queen_session_id"`
			QueenName      string  `json:"queen_name"`
			Batch          struct {
				BatchID    string `json:"batch_id"`
				BatchIndex int    `json:"batch_index"`
				BatchSize  int    `json:"batch_size"`
				WorkerSeq  int    `json:"worker_seq"`
			} `json:"batch"`
		} `json:"workers"`
	}
	decodeColonyWorkerBody(t, raw, &list)
	var entry *struct {
		WorkerID       string  `json:"worker_id"`
		Status         string  `json:"status"`
		StartedAt      float64 `json:"started_at"`
		QueenSessionID string  `json:"queen_session_id"`
		QueenName      string  `json:"queen_name"`
		Batch          struct {
			BatchID    string `json:"batch_id"`
			BatchIndex int    `json:"batch_index"`
			BatchSize  int    `json:"batch_size"`
			WorkerSeq  int    `json:"worker_seq"`
		} `json:"batch"`
	}
	for index := range list.Workers {
		if list.Workers[index].WorkerID == "w_lineage" {
			entry = &list.Workers[index]
		}
	}
	if entry == nil {
		t.Fatalf("w_lineage missing from the workers list: %s", string(raw))
	}
	if entry.Status != "historical" {
		t.Errorf("status = %q, want historical", entry.Status)
	}
	if entry.StartedAt != 1700000000.25 {
		// spawned_at must win over the directory mtime.
		t.Errorf("started_at = %v, want the meta spawned_at (1700000000.25)", entry.StartedAt)
	}
	if entry.QueenSessionID != "s1" || entry.QueenName != "queen_growth" {
		t.Errorf("lineage = %q/%q, want s1/queen_growth", entry.QueenSessionID, entry.QueenName)
	}
	if entry.Batch.BatchID != "rpw_20260101T000000Z_abcd1234" || entry.Batch.BatchIndex != 2 ||
		entry.Batch.BatchSize != 3 || entry.Batch.WorkerSeq != 5 {
		t.Errorf("batch = %+v, want the rpw_ id with 2/3 and worker_seq 5", entry.Batch)
	}

	recorder, raw = colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/w_lineage", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var detail struct {
		Worker struct {
			WorkerID       string `json:"worker_id"`
			QueenSessionID string `json:"queen_session_id"`
			QueenName      string `json:"queen_name"`
			Batch          struct {
				WorkerSeq int `json:"worker_seq"`
			} `json:"batch"`
		} `json:"worker"`
	}
	decodeColonyWorkerBody(t, raw, &detail)
	if detail.Worker.QueenSessionID != "s1" || detail.Worker.QueenName != "queen_growth" {
		t.Errorf("detail lineage = %q/%q, want s1/queen_growth", detail.Worker.QueenSessionID, detail.Worker.QueenName)
	}
	if detail.Worker.Batch.WorkerSeq != 5 {
		t.Errorf("detail worker_seq = %d, want 5", detail.Worker.Batch.WorkerSeq)
	}
}

// Parity: framework/server/routes_colony_workers.py::_walk_colony_workers
// ("Workers without a meta.json are skipped because we can't attribute them
// safely")
//
// Negative regression: a meta.json without queen_session_id is NOT attributed
// to the session. That is what the pre-fix Go writer produced, and it is why a
// Go-spawned worker used to vanish from the surface once its runtime was gone —
// the walk must keep refusing unattributable directories rather than list every
// worker in the colony.
func TestWorkerSurfaceSkipsUnattributedLegacyMeta(t *testing.T) {
	surface := &fakeColonyWorkerSurface{colonyID: "test_colony"}
	deps, manager, home := colonyWorkerTestDeps(t, surface)
	manager.AddSession(NewSession("s1", nil))

	workersDir := filepath.Join(home.ColoniesDir(), "test_colony", "workers")
	legacy := filepath.Join(workersDir, "w_unattributed")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	// The old Go shape: no queen_session_id, no queen_name, no spawned_at.
	writeServerTestJSON(t, filepath.Join(legacy, "meta.json"), map[string]any{
		"worker_id":    "w_unattributed",
		"colony_id":    "test_colony",
		"task":         "orphan",
		"batch_id":     "batch_3",
		"batch_index":  0,
		"batch_size":   2,
		"profile_name": "",
	})

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var list struct {
		Workers []struct {
			WorkerID string `json:"worker_id"`
		} `json:"workers"`
	}
	decodeColonyWorkerBody(t, raw, &list)
	for _, worker := range list.Workers {
		if worker.WorkerID == "w_unattributed" {
			t.Fatalf("w_unattributed has no queen_session_id and must not be attributed to s1: %s", string(raw))
		}
	}

	// The detail read still resolves the directory directly, and reports the
	// missing lineage as null rather than inventing one.
	recorder, raw = colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s1/workers/w_unattributed", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var detail struct {
		Worker struct {
			QueenSessionID any `json:"queen_session_id"`
			QueenName      any `json:"queen_name"`
		} `json:"worker"`
	}
	decodeColonyWorkerBody(t, raw, &detail)
	if detail.Worker.QueenSessionID != nil || detail.Worker.QueenName != nil {
		t.Fatalf("detail lineage = %v/%v, want null/null for an unattributed meta",
			detail.Worker.QueenSessionID, detail.Worker.QueenName)
	}
}

// metaSurfaceLLM is a one-line completion source: enough for a spawned worker
// to run a turn and terminate without tool calls.
type metaSurfaceLLM struct{}

func (metaSurfaceLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{Content: "done", StopReason: "stop"}, nil
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn (meta.json)
// Parity: framework/server/routes_colony_workers.py::_walk_colony_workers
//
// Round trip: workers spawned by the real Go colony runtime must be readable by
// the HTTP worker surface purely from disk. The surface used here reports no
// in-memory workers, so a worker only shows up if the meta.json the runtime
// wrote carries the lineage the walk filters on.
func TestWorkerSurfaceRoundTripsSpawnedWorkerMeta(t *testing.T) {
	deps, manager, home := colonyWorkerTestDeps(t, &fakeColonyWorkerSurface{colonyID: "roundtrip_colony"})
	colonyDir := filepath.Join(home.ColoniesDir(), "roundtrip_colony")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}

	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "roundtrip", Name: "Roundtrip"},
		StoragePath: colonyDir,
		LLM:         metaSurfaceLLM{},
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		StreamID:  "s_roundtrip",
		QueenName: "queen_growth",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start colony: %v", err)
	}

	ids, err := runtime.SpawnBatch(context.Background(), []colony.BatchTask{
		{Task: "first"},
		{Task: "second"},
	})
	if err != nil {
		runtime.Stop()
		t.Fatalf("spawn batch: %v", err)
	}
	for _, id := range ids {
		if worker := runtime.GetWorker(id); worker != nil {
			worker.Wait()
		}
	}
	// The runtime going away is the point: from here only disk can answer.
	runtime.Stop()

	manager.AddSession(NewSession("s_roundtrip", nil))

	recorder, raw := colonyWorkerDo(t, deps, http.MethodGet, "/api/sessions/s_roundtrip/workers", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, string(raw))
	}
	var list struct {
		Workers []struct {
			WorkerID       string `json:"worker_id"`
			Status         string `json:"status"`
			QueenSessionID string `json:"queen_session_id"`
			QueenName      string `json:"queen_name"`
			Batch          struct {
				BatchID    string `json:"batch_id"`
				BatchIndex int    `json:"batch_index"`
				BatchSize  int    `json:"batch_size"`
			} `json:"batch"`
		} `json:"workers"`
	}
	decodeColonyWorkerBody(t, raw, &list)

	found := map[string]int{}
	for _, worker := range list.Workers {
		if worker.Status != "historical" {
			t.Fatalf("worker %s status = %q, want historical", worker.WorkerID, worker.Status)
		}
		if worker.QueenSessionID != "s_roundtrip" || worker.QueenName != "queen_growth" {
			t.Fatalf("worker %s lineage = %q/%q, want s_roundtrip/queen_growth",
				worker.WorkerID, worker.QueenSessionID, worker.QueenName)
		}
		if worker.Batch.BatchSize != len(ids) {
			t.Fatalf("worker %s batch_size = %d, want %d", worker.WorkerID, worker.Batch.BatchSize, len(ids))
		}
		if !strings.HasPrefix(worker.Batch.BatchID, "rpw_") {
			t.Fatalf("worker %s batch_id = %q, want an rpw_ prefix", worker.WorkerID, worker.Batch.BatchID)
		}
		found[worker.WorkerID] = worker.Batch.BatchIndex
	}
	if len(found) != len(ids) {
		t.Fatalf("listed %d of %d spawned workers: %s", len(found), len(ids), string(raw))
	}
	seen := map[int]struct{}{}
	for id, index := range found {
		if index < 1 || index > len(ids) {
			t.Fatalf("worker %s batch_index = %d, want 1..%d", id, index, len(ids))
		}
		if _, duplicate := seen[index]; duplicate {
			t.Fatalf("batch_index %d assigned to more than one worker", index)
		}
		seen[index] = struct{}{}
	}
}

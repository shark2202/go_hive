package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tasks"
)

// newTaskRoutesApp builds an app whose task store points at the same temp
// hive root the rest of the app reads, so the route table is exercised with
// the real wiring (deps.Home → TaskStore) instead of the process default.
func newTaskRoutesApp(t *testing.T) (*App, *tasks.TaskStore) {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp home: %v", err)
	}
	app := newAppForTest(t, AppDeps{Home: home})
	return app, tasks.NewTaskStore(home.Root)
}

func postTaskRoutesJSON(t *testing.T, app *App, target, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, target, reader))
	var parsed map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode %s body: %v", target, err)
	}
	return recorder.Code, parsed
}

func taskRouteMaps(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["tasks"].([]any)
	if !ok {
		t.Fatalf("tasks = %#v, want array", body["tasks"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		task, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("task entry = %#v, want object", item)
		}
		out = append(out, task)
	}
	return out
}

func taskRouteInts(t *testing.T, value any) []int {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %#v, want array", value)
	}
	out := make([]int, 0, len(raw))
	for _, item := range raw {
		n, ok := item.(float64)
		if !ok {
			t.Fatalf("array entry = %#v, want number", item)
		}
		out = append(out, int(n))
	}
	return out
}

// Parity: core/framework/server/routes_tasks.py::handle_get_task_list
// Upstream test: core/framework/tasks/tests/test_e2e.py::test_rest_get_task_list_404
func TestTaskListSnapshotNotFound(t *testing.T) {
	app, _ := newTaskRoutesApp(t)

	status, body, _ := getJSON(t, app, "/api/sessions/nope/tasks")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["session_id"] != "nope" {
		t.Fatalf("session_id = %v, want nope", body["session_id"])
	}
	if got := body["tasks"]; len(got.([]any)) != 0 {
		t.Fatalf("tasks = %#v, want []", got)
	}
}

// Parity: core/framework/server/routes_tasks.py::handle_get_task_list
// Upstream test: core/framework/tasks/tests/test_e2e.py::test_rest_get_task_list_after_create
func TestTaskListSnapshotShapeAfterCreate(t *testing.T) {
	app, store := newTaskRoutesApp(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTask("s1", tasks.CreateTaskSpec{Subject: "abc"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	status, body, headers := getJSON(t, app, "/api/sessions/s1/tasks")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ct := headers.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if body["session_id"] != "s1" || body["role"] != "session" {
		t.Fatalf("snapshot header = %v/%v, want s1/session", body["session_id"], body["role"])
	}

	meta, ok := body["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want object", body["meta"])
	}
	if meta["role"] != "session" || meta["schema_version"] != float64(1) {
		t.Fatalf("meta = %#v, want role=session schema_version=1", meta)
	}
	for _, key := range []string{"creator_agent_id", "goal"} {
		if value, present := meta[key]; !present || value != nil {
			t.Fatalf("meta[%q] = %#v, want explicit null", key, value)
		}
	}

	records := taskRouteMaps(t, body)
	if len(records) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(records))
	}
	record := records[0]
	if record["id"] != float64(1) || record["subject"] != "abc" || record["description"] != "" {
		t.Fatalf("task identity = %#v, want id=1 subject=abc empty description", record)
	}
	if record["status"] != "pending" {
		t.Fatalf("status = %v, want pending", record["status"])
	}
	for _, key := range []string{"active_form", "owner"} {
		if value, present := record[key]; !present || value != nil {
			t.Fatalf("task[%q] = %#v, want explicit null", key, value)
		}
	}
	if blocks, ok := record["blocks"].([]any); !ok || len(blocks) != 0 {
		t.Fatalf("blocks = %#v, want []", record["blocks"])
	}
	if blockedBy, ok := record["blocked_by"].([]any); !ok || len(blockedBy) != 0 {
		t.Fatalf("blocked_by = %#v, want []", record["blocked_by"])
	}
	if metadata, ok := record["metadata"].(map[string]any); !ok || len(metadata) != 0 {
		t.Fatalf("metadata = %#v, want {}", record["metadata"])
	}
	if created, _ := record["created_at"].(float64); created <= 0 {
		t.Fatalf("created_at = %#v, want positive epoch seconds", record["created_at"])
	}
}

// Parity: core/framework/server/routes_tasks.py::handle_get_task_list
// Upstream source: framework/tasks/store.py::sweep_idle_tasks.
func TestTaskListSnapshotSweepsIdleInProgress(t *testing.T) {
	app, store := newTaskRoutesApp(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	record, err := store.CreateTask("s1", tasks.CreateTaskSpec{Subject: "stale"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	inProgress := tasks.TaskStatusInProgress
	store.UpdateTask("s1", record.ID, tasks.UpdateTaskOptions{Status: &inProgress})

	// Age the record past the 30-minute idle threshold on disk.
	docPath, err := store.DocPath("s1")
	if err != nil {
		t.Fatalf("DocPath: %v", err)
	}
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode doc: %v", err)
	}
	entries := doc["tasks"].([]any)
	entries[0].(map[string]any)["updated_at"] = tasks.NowSeconds() - 3600
	updated, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode doc: %v", err)
	}
	if err := os.WriteFile(docPath, updated, 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	bus := eventbus.NewEventBus()
	tasks.SetDefaultEventBus(bus)
	t.Cleanup(func() { tasks.SetDefaultEventBus(nil) })
	var received []*eventbus.AgentEvent
	bus.Subscribe([]eventbus.EventType{eventbus.EventTaskUpdated}, func(ev *eventbus.AgentEvent) {
		received = append(received, ev)
	})

	status, body, _ := getJSON(t, app, "/api/sessions/s1/tasks")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	records := taskRouteMaps(t, body)
	if len(records) != 1 || records[0]["status"] != string(tasks.TaskStatusAbandoned) {
		t.Fatalf("snapshot = %#v, want one abandoned task", records)
	}
	if len(received) != 1 || received[0].Data["task_id"] != 1 {
		t.Fatalf("sweep events = %#v, want one task_updated for id 1", received)
	}
}

// Parity: core/framework/server/routes_tasks.py::handle_clear_completed_tasks
// Upstream test: core/framework/tasks/tests/test_e2e.py::test_rest_clear_completed_archives_and_reports_ids
func TestTaskListClearCompletedArchivesOnlyCompleted(t *testing.T) {
	app, store := newTaskRoutesApp(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTasksBatch("s1", []tasks.TaskSpec{
		{Subject: "done-1"}, {Subject: "done-2"}, {Subject: "open"},
	}, nil); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	completed := tasks.TaskStatusCompleted
	store.UpdateTask("s1", 1, tasks.UpdateTaskOptions{Status: &completed})
	store.UpdateTask("s1", 2, tasks.UpdateTaskOptions{Status: &completed})

	status, body := postTaskRoutesJSON(t, app, "/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["session_id"] != "s1" {
		t.Fatalf("session_id = %v, want s1", body["session_id"])
	}
	archived := taskRouteInts(t, body["archived"])
	sort.Ints(archived)
	if len(archived) != 2 || archived[0] != 1 || archived[1] != 2 {
		t.Fatalf("archived = %v, want [1 2]", archived)
	}

	_, snapshot, _ := getJSON(t, app, "/api/sessions/s1/tasks")
	byID := map[int]map[string]any{}
	for _, task := range taskRouteMaps(t, snapshot) {
		byID[int(task["id"].(float64))] = task
	}
	if byID[1]["status"] != "archived" || byID[2]["status"] != "archived" {
		t.Fatalf("archived statuses = %v, %v; want archived", byID[1]["status"], byID[2]["status"])
	}
	if byID[3]["status"] != "pending" {
		t.Fatalf("task 3 status = %v, want pending", byID[3]["status"])
	}
}

// Parity: core/framework/server/routes_tasks.py::handle_clear_completed_tasks
// Upstream test: core/framework/tasks/tests/test_e2e.py::test_rest_clear_completed_noop_and_unknown_session
func TestTaskListClearCompletedNoopUnknownSession(t *testing.T) {
	app, store := newTaskRoutesApp(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTask("s1", tasks.CreateTaskSpec{Subject: "open"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	status, body := postTaskRoutesJSON(t, app, "/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if archived := taskRouteInts(t, body["archived"]); len(archived) != 0 {
		t.Fatalf("archived = %v, want []", archived)
	}

	status, body = postTaskRoutesJSON(t, app, "/api/sessions/never-existed/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("unknown session status = %d, want 200", status)
	}
	if archived := taskRouteInts(t, body["archived"]); len(archived) != 0 {
		t.Fatalf("unknown session archived = %v, want []", archived)
	}
}

// Parity: core/framework/server/routes_tasks.py::handle_clear_completed_tasks
// Upstream test: core/framework/tasks/tests/test_e2e.py::test_rest_clear_completed_emits_task_updated_per_record
func TestTaskListClearCompletedEmitsTaskUpdated(t *testing.T) {
	app, store := newTaskRoutesApp(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTasksBatch("s1", []tasks.TaskSpec{{Subject: "a"}, {Subject: "b"}}, nil); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	completed := tasks.TaskStatusCompleted
	store.UpdateTask("s1", 1, tasks.UpdateTaskOptions{Status: &completed})
	store.UpdateTask("s1", 2, tasks.UpdateTaskOptions{Status: &completed})

	bus := eventbus.NewEventBus()
	tasks.SetDefaultEventBus(bus)
	t.Cleanup(func() { tasks.SetDefaultEventBus(nil) })
	var received []*eventbus.AgentEvent
	bus.Subscribe([]eventbus.EventType{eventbus.EventTaskUpdated}, func(ev *eventbus.AgentEvent) {
		received = append(received, ev)
	})

	status, _ := postTaskRoutesJSON(t, app, "/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	ids := make([]int, 0, len(received))
	for _, event := range received {
		id, ok := event.Data["task_id"].(int)
		if !ok {
			t.Fatalf("task_id = %#v, want int", event.Data["task_id"])
		}
		after, ok := event.Data["after"].(map[string]any)
		if !ok || after["status"] != "archived" {
			t.Fatalf("after = %#v, want archived task", event.Data["after"])
		}
		fields, ok := event.Data["fields"].([]string)
		if !ok || len(fields) != 1 || fields[0] != "status" {
			t.Fatalf("fields = %#v, want [status]", event.Data["fields"])
		}
		ids = append(ids, id)
	}
	sort.Ints(ids)
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("event task ids = %v, want [1 2]", ids)
	}
}

// Parity: core/framework/server/routes_tasks.py::handle_unarchive_task_list
// Upstream test: core/framework/tasks/tests/test_e2e.py::test_rest_clear_completed_then_unarchive_round_trip
func TestTaskListClearCompletedThenUnarchiveRoundTrip(t *testing.T) {
	app, store := newTaskRoutesApp(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTask("s1", tasks.CreateTaskSpec{Subject: "finished work"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	completed := tasks.TaskStatusCompleted
	store.UpdateTask("s1", 1, tasks.UpdateTaskOptions{Status: &completed})

	status, cleared := postTaskRoutesJSON(t, app, "/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("clear status = %d, want 200", status)
	}
	if archived := taskRouteInts(t, cleared["archived"]); len(archived) != 1 || archived[0] != 1 {
		t.Fatalf("archived = %v, want [1]", archived)
	}

	status, restored := postTaskRoutesJSON(t, app, "/api/sessions/s1/tasks/unarchive", `{"task_ids": [1]}`)
	if status != http.StatusOK {
		t.Fatalf("unarchive status = %d, want 200", status)
	}
	if ids := taskRouteInts(t, restored["restored"]); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("restored = %v, want [1]", ids)
	}

	_, snapshot, _ := getJSON(t, app, "/api/sessions/s1/tasks")
	records := taskRouteMaps(t, snapshot)
	if len(records) != 1 || records[0]["status"] != "completed" {
		t.Fatalf("round-trip snapshot = %#v, want one completed task", records)
	}
	if _, present := records[0]["metadata"].(map[string]any)["archived_from"]; present {
		t.Fatalf("metadata still carries archived_from: %#v", records[0]["metadata"])
	}
}

// Parity: core/framework/server/routes_tasks.py::handle_unarchive_task_list
func TestTaskListUnarchiveRejectsInvalidTaskIDs(t *testing.T) {
	app, _ := newTaskRoutesApp(t)

	for _, body := range []string{"", `{}`, `{"task_ids": ["1"]}`, `{"task_ids": 1}`, `{"task_ids": [1.5]}`} {
		status, parsed := postTaskRoutesJSON(t, app, "/api/sessions/s1/tasks/unarchive", body)
		if status != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, status)
		}
		if parsed["error"] != "task_ids must be a list of integers" {
			t.Fatalf("body %q error = %v", body, parsed["error"])
		}
		if parsed["session_id"] != "s1" {
			t.Fatalf("body %q session_id = %v", body, parsed["session_id"])
		}
	}

	// An empty list is valid and restores nothing.
	status, parsed := postTaskRoutesJSON(t, app, "/api/sessions/s1/tasks/unarchive", `{"task_ids": []}`)
	if status != http.StatusOK {
		t.Fatalf("empty list status = %d, want 200", status)
	}
	if restored := taskRouteInts(t, parsed["restored"]); len(restored) != 0 {
		t.Fatalf("restored = %v, want []", restored)
	}
}

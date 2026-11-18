package tasks

// Parity: framework/tasks/tests/test_e2e.py

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

func e2eSubscribe(t *testing.T, eventType eventbus.EventType, received *[]*eventbus.AgentEvent) *eventbus.EventBus {
	t.Helper()
	bus := eventbus.NewEventBus()
	SetDefaultEventBus(bus)
	t.Cleanup(func() { SetDefaultEventBus(nil) })
	bus.Subscribe([]eventbus.EventType{eventType}, func(ev *eventbus.AgentEvent) {
		*received = append(*received, ev)
	})
	return bus
}

func newTaskRouteFixture(t *testing.T) (*httptest.Server, *TaskStore) {
	t.Helper()
	store := NewTaskStore(t.TempDir())
	previous := GetTaskStore()
	SetDefaultTaskStore(store)
	t.Cleanup(func() { SetDefaultTaskStore(previous) })

	mux := http.NewServeMux()
	RegisterTaskRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, store
}

func e2eGetJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, decodeJSONBody(t, resp.Body)
}

func e2ePostJSON(t *testing.T, url string, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	resp, err := http.Post(url, "application/json", reader)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, decodeJSONBody(t, resp.Body)
}

func decodeJSONBody(t *testing.T, reader io.Reader) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.NewDecoder(reader).Decode(&parsed); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return parsed
}

func e2eTaskMaps(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["tasks"].([]any)
	if !ok {
		t.Fatalf("body[\"tasks\"] = %#v, want array", body["tasks"])
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

func e2eInts(t *testing.T, value any) []int {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		if ints, ok := value.([]int); ok {
			return ints
		}
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

// Parity: framework/tasks/tests/test_e2e.py::test_task_created_emits_event
func TestTaskCreatedEmitsEvent(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)

	var received []*eventbus.AgentEvent
	e2eSubscribe(t, eventbus.EventTaskCreated, &received)

	withTestContext(t, "alice", "s1")
	invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":  "test goal",
		"tasks": []any{map[string]any{"subject": "hello"}},
	})

	if len(received) != 1 {
		t.Fatalf("received %d events, want 1", len(received))
	}
	event := received[0]
	if event.Type != eventbus.EventTaskCreated {
		t.Fatalf("event type = %q, want %q", event.Type, eventbus.EventTaskCreated)
	}
	task, ok := event.Data["task"].(map[string]any)
	if !ok {
		t.Fatalf("event task = %#v, want object", event.Data["task"])
	}
	if task["subject"] != "hello" {
		t.Fatalf("task subject = %v, want hello", task["subject"])
	}
	if event.Data["session_id"] != "s1" {
		t.Fatalf("session_id = %v, want s1", event.Data["session_id"])
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_task_updated_emits_event
func TestTaskUpdatedEmitsEvent(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)

	var received []*eventbus.AgentEvent
	e2eSubscribe(t, eventbus.EventTaskUpdated, &received)

	withTestContext(t, "alice", "s1")
	invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":  "test goal",
		"tasks": []any{map[string]any{"subject": "x"}},
	})
	invokeTool(t, reg, TaskUpdate, map[string]any{
		"id":     1,
		"status": string(TaskStatusInProgress),
	})

	if len(received) < 1 {
		t.Fatalf("received %d events, want at least 1", len(received))
	}
	if received[0].Type != eventbus.EventTaskUpdated {
		t.Fatalf("event type = %q, want %q", received[0].Type, eventbus.EventTaskUpdated)
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_rest_get_task_list_404
func TestRESTGetTaskList404(t *testing.T) {
	server, _ := newTaskRouteFixture(t)
	status, body := e2eGetJSON(t, server.URL+"/api/sessions/nope/tasks")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["session_id"] != "nope" {
		t.Fatalf("session_id = %v, want nope", body["session_id"])
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_rest_get_task_list_after_create
func TestRESTGetTaskListAfterCreate(t *testing.T) {
	server, store := newTaskRouteFixture(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTask("s1", CreateTaskSpec{Subject: "abc"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	status, body := e2eGetJSON(t, server.URL+"/api/sessions/s1/tasks")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["session_id"] != "s1" {
		t.Fatalf("session_id = %v, want s1", body["session_id"])
	}
	if body["role"] != "session" {
		t.Fatalf("role = %v, want session", body["role"])
	}
	tasks := e2eTaskMaps(t, body)
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}
	if tasks[0]["subject"] != "abc" {
		t.Fatalf("subject = %v, want abc", tasks[0]["subject"])
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_rest_clear_completed_archives_and_reports_ids
func TestRESTClearCompletedArchivesAndReportsIDs(t *testing.T) {
	server, store := newTaskRouteFixture(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTasksBatch("s1", []TaskSpec{
		{Subject: "done-1"}, {Subject: "done-2"}, {Subject: "open"},
	}, nil); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	completed := TaskStatusCompleted
	store.UpdateTask("s1", 1, UpdateTaskOptions{Status: &completed})
	store.UpdateTask("s1", 2, UpdateTaskOptions{Status: &completed})

	status, body := e2ePostJSON(t, server.URL+"/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["session_id"] != "s1" {
		t.Fatalf("session_id = %v, want s1", body["session_id"])
	}
	archived := e2eInts(t, body["archived"])
	sort.Ints(archived)
	if len(archived) != 2 || archived[0] != 1 || archived[1] != 2 {
		t.Fatalf("archived = %v, want [1 2]", archived)
	}

	_, snapshot := e2eGetJSON(t, server.URL+"/api/sessions/s1/tasks")
	byID := map[int]map[string]any{}
	for _, task := range e2eTaskMaps(t, snapshot) {
		id, _ := task["id"].(float64)
		byID[int(id)] = task
	}
	if byID[1]["status"] != "archived" || byID[2]["status"] != "archived" {
		t.Fatalf("archived statuses = %v, %v; want archived", byID[1]["status"], byID[2]["status"])
	}
	if byID[3]["status"] != "pending" {
		t.Fatalf("task 3 status = %v, want pending", byID[3]["status"])
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_rest_clear_completed_noop_and_unknown_session
func TestRESTClearCompletedNoopAndUnknownSession(t *testing.T) {
	server, store := newTaskRouteFixture(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTask("s1", CreateTaskSpec{Subject: "open"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	status, body := e2ePostJSON(t, server.URL+"/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if archived := e2eInts(t, body["archived"]); len(archived) != 0 {
		t.Fatalf("archived = %v, want []", archived)
	}

	status, body = e2ePostJSON(t, server.URL+"/api/sessions/never-existed/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("unknown session status = %d, want 200", status)
	}
	if archived := e2eInts(t, body["archived"]); len(archived) != 0 {
		t.Fatalf("unknown session archived = %v, want []", archived)
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_rest_clear_completed_emits_task_updated_per_record
func TestRESTClearCompletedEmitsTaskUpdatedPerRecord(t *testing.T) {
	server, store := newTaskRouteFixture(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTasksBatch("s1", []TaskSpec{{Subject: "a"}, {Subject: "b"}}, nil); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	completed := TaskStatusCompleted
	store.UpdateTask("s1", 1, UpdateTaskOptions{Status: &completed})
	store.UpdateTask("s1", 2, UpdateTaskOptions{Status: &completed})

	var received []*eventbus.AgentEvent
	e2eSubscribe(t, eventbus.EventTaskUpdated, &received)

	status, _ := e2ePostJSON(t, server.URL+"/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	ids := make([]int, 0, len(received))
	for _, event := range received {
		id, ok := event.Data["task_id"].(int)
		if !ok {
			t.Fatalf("task_id = %#v, want int", event.Data["task_id"])
		}
		ids = append(ids, id)
		after, ok := event.Data["after"].(map[string]any)
		if !ok {
			t.Fatalf("after = %#v, want object", event.Data["after"])
		}
		if after["status"] != "archived" {
			t.Fatalf("after status = %v, want archived", after["status"])
		}
		fields, ok := event.Data["fields"].([]string)
		if !ok || len(fields) != 1 || fields[0] != "status" {
			t.Fatalf("fields = %#v, want [status]", event.Data["fields"])
		}
	}
	sort.Ints(ids)
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("event task ids = %v, want [1 2]", ids)
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_rest_clear_completed_then_unarchive_round_trip
func TestRESTClearCompletedThenUnarchiveRoundTrip(t *testing.T) {
	server, store := newTaskRouteFixture(t)
	if _, err := store.EnsureTaskList("s1", nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTask("s1", CreateTaskSpec{Subject: "finished work"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	completed := TaskStatusCompleted
	store.UpdateTask("s1", 1, UpdateTaskOptions{Status: &completed})

	status, cleared := e2ePostJSON(t, server.URL+"/api/sessions/s1/tasks/clear-completed", "")
	if status != http.StatusOK {
		t.Fatalf("clear status = %d, want 200", status)
	}
	if archived := e2eInts(t, cleared["archived"]); len(archived) != 1 || archived[0] != 1 {
		t.Fatalf("archived = %v, want [1]", archived)
	}

	status, restored := e2ePostJSON(t,
		server.URL+"/api/sessions/s1/tasks/unarchive", `{"task_ids": [1]}`)
	if status != http.StatusOK {
		t.Fatalf("unarchive status = %d, want 200", status)
	}
	if ids := e2eInts(t, restored["restored"]); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("restored = %v, want [1]", ids)
	}

	_, snapshot := e2eGetJSON(t, server.URL+"/api/sessions/s1/tasks")
	tasks := e2eTaskMaps(t, snapshot)
	if len(tasks) != 1 || tasks[0]["status"] != "completed" {
		t.Fatalf("round-trip snapshot = %v, want one completed task", tasks)
	}
}

// Parity-exempt: subprocess helper body for test_durability_across_subprocesses.
func TestDurabilitySubprocessHelper(t *testing.T) {
	mode := os.Getenv("HIVE_DUR_HELPER")
	if mode == "" {
		t.Skip("helper process only")
	}
	store := NewTaskStore(os.Getenv("HIVE_DUR_HELPER_ROOT"))
	switch mode {
	case "write":
		record, err := store.CreateTask("sess_dur", CreateTaskSpec{Subject: "persisted"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		fmt.Printf("HELPER_RESULT:%d\n", record.ID)
	case "read":
		records := store.ListTasks("sess_dur")
		subject := ""
		if len(records) > 0 {
			subject = records[0].Subject
		}
		fmt.Printf("HELPER_RESULT:%d %s\n", len(records), subject)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func runDurabilityHelper(t *testing.T, mode, root string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurabilitySubprocessHelper$")
	cmd.Env = append(os.Environ(),
		"HIVE_DUR_HELPER="+mode,
		"HIVE_DUR_HELPER_ROOT="+root,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper %s failed: %v\n%s", mode, err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "HELPER_RESULT:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "HELPER_RESULT:"))
		}
	}
	t.Fatalf("helper %s produced no result line:\n%s", mode, out)
	return ""
}

// Parity: framework/tasks/tests/test_e2e.py::test_durability_across_subprocesses
func TestDurabilityAcrossSubprocesses(t *testing.T) {
	root := t.TempDir()
	written := runDurabilityHelper(t, "write", root)
	if written != "1" {
		t.Fatalf("written id = %q, want 1", written)
	}

	read := runDurabilityHelper(t, "read", root)
	if read != "1 persisted" {
		t.Fatalf("read result = %q, want %q", read, "1 persisted")
	}
}

// Parity: framework/tasks/tests/test_e2e.py::test_graceful_no_op_preserves_files
func TestGracefulNoOpPreservesFiles(t *testing.T) {
	root := t.TempDir()
	store := NewTaskStore(root)
	sessionID := "sess_g"
	if _, err := store.EnsureTaskList(sessionID, nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	record, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "x"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	pre := collectJSONFiles(t, root)
	post := collectJSONFiles(t, root)

	if len(post) != len(pre) {
		t.Fatalf("json files before = %v, after = %v", keysOf(pre), keysOf(post))
	}
	for name, before := range pre {
		after, ok := post[name]
		if !ok {
			t.Fatalf("file %q disappeared", name)
		}
		if string(after) != string(before) {
			t.Fatalf("file %q changed bytes", name)
		}
	}
	if record.ID != 1 {
		t.Fatalf("record.ID = %d, want 1", record.ID)
	}
}

func collectJSONFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[entry.Name()] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func keysOf(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

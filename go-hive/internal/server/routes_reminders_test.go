package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/tasks"
)

// newReminderRoutesMux builds a standalone route table pointed at a temp hive
// root. The task reminder source reads the process-wide task store (parity
// with framework.tasks.get_task_store()), so the default store is pointed at
// the same root for the duration of the test.
func newReminderRoutesMux(t *testing.T) (*http.ServeMux, *ReminderRuntime, *tasks.TaskStore, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HIVE_HOME", root)
	tasks.ResetSessionDirCache()
	previous := tasks.GetTaskStore()
	store := tasks.NewTaskStore(root)
	tasks.SetDefaultTaskStore(store)
	t.Cleanup(func() {
		tasks.SetDefaultTaskStore(previous)
		tasks.ResetSessionDirCache()
	})

	runtime := NewReminderRuntime()
	mux := http.NewServeMux()
	RegisterReminderRoutes(mux, ReminderRouteDeps{Runtime: runtime})
	return mux, runtime, store, root
}

func reminderGET(t *testing.T, mux *http.ServeMux, target string) (int, map[string]any, http.Header) {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s body: %v", target, err)
	}
	return recorder.Code, body, recorder.Header()
}

func reminderDELETE(t *testing.T, mux *http.ServeMux, target string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, target, nil))
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s body: %v", target, err)
	}
	return recorder.Code, body
}

func reminderItems(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["reminders"].([]any)
	if !ok {
		t.Fatalf("reminders = %#v, want array", body["reminders"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("reminder entry = %#v, want object", item)
		}
		out = append(out, entry)
	}
	return out
}

func reminderBodyContains(items []map[string]any, needle string) bool {
	for _, item := range items {
		body, _ := item["body"].(string)
		if strings.Contains(body, needle) {
			return true
		}
	}
	return false
}

// Parity: framework/tasks/tests/test_reminders.py::test_render_block_empty_when_nothing_to_say
func TestReminderRoutesEmptyWhenNothingDue(t *testing.T) {
	mux, _, _, _ := newReminderRoutesMux(t)

	status, body, headers := reminderGET(t, mux, "/api/sessions/s-empty/reminders")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ct := headers.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if body["session_id"] != "s-empty" {
		t.Fatalf("session_id = %v, want s-empty", body["session_id"])
	}
	if items := reminderItems(t, body); len(items) != 0 {
		t.Fatalf("reminders = %#v, want []", items)
	}
	if _, present := body["block"]; present {
		t.Fatalf("empty reminder set must not carry a block: %#v", body["block"])
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_post_tool_use_shows_snapshot
func TestReminderRoutesTurnCompleteQueuesSnapshotThenDrains(t *testing.T) {
	mux, runtime, store, _ := newReminderRoutesMux(t)
	if _, err := store.CreateTask("s1", tasks.CreateTaskSpec{Subject: "verify the sheet"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// One task-write turn marks tracking as begun; three quiet turns pass the
	// four-turn warmup; a tool turn then makes the changed task list due.
	runtime.TurnComplete("s1", []string{"task_create"})
	for i := 0; i < 3; i++ {
		runtime.TurnComplete("s1", nil)
	}
	produced := runtime.TurnComplete("s1", []string{"read_file"})
	if len(produced) != 1 {
		t.Fatalf("produced = %d reminders, want 1", len(produced))
	}
	if produced[0].Source != "tasks" || produced[0].Point != "post_tool_use" {
		t.Fatalf("reminder source/point = %q/%q, want tasks/post_tool_use", produced[0].Source, produced[0].Point)
	}

	status, body, _ := reminderGET(t, mux, "/api/sessions/s1/reminders")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	items := reminderItems(t, body)
	if len(items) != 1 {
		t.Fatalf("len(reminders) = %d, want 1", len(items))
	}
	if !reminderBodyContains(items, "verify the sheet") {
		t.Fatalf("reminder body missing task subject: %#v", items[0]["body"])
	}
	if !reminderBodyContains(items, "Here are the existing tasks:") {
		t.Fatalf("reminder body missing snapshot header: %#v", items[0]["body"])
	}
	block, _ := body["block"].(string)
	if !strings.HasPrefix(block, "<system-reminder>") || !strings.HasSuffix(block, "</system-reminder>") {
		t.Fatalf("block = %q, want a wrapped system-reminder", block)
	}

	// Reading drains (ReminderHub.take_pending): the next read is empty.
	_, second, _ := reminderGET(t, mux, "/api/sessions/s1/reminders")
	if items := reminderItems(t, second); len(items) != 0 {
		t.Fatalf("second read = %#v, want []", items)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_stop_emits_untracked_drift
func TestReminderRoutesIdleBoundaryQueuesDriftNudge(t *testing.T) {
	mux, runtime, _, _ := newReminderRoutesMux(t)

	// Five quiet turns: the session never created a task, so the stop point
	// emits the "untracked" drift nudge (UNTRACKED_TURNS default 5).
	for i := 0; i < 5; i++ {
		runtime.TurnComplete("s-drift", nil)
	}

	status, body, _ := reminderGET(t, mux, "/api/sessions/s-drift/reminders")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	items := reminderItems(t, body)
	if len(items) != 1 {
		t.Fatalf("len(reminders) = %d, want 1", len(items))
	}
	if items[0]["point"] != "stop" {
		t.Fatalf("point = %v, want stop", items[0]["point"])
	}
	if !reminderBodyContains(items, "haven't created any tasks") {
		t.Fatalf("reminder body missing untracked nudge: %#v", items[0]["body"])
	}
}

// Parity-exempt: the peek (?drain=0) and DELETE controls are a Go-only HTTP
// surface over ReminderHub.pending — upstream drains via take_pending and
// clears via stop, but exposes neither over REST.
func TestReminderRoutesPeekDoesNotConsumeAndDismissClears(t *testing.T) {
	mux, runtime, _, _ := newReminderRoutesMux(t)
	for i := 0; i < 5; i++ {
		runtime.TurnComplete("s-peek", nil)
	}
	if got := runtime.PendingLen("s-peek"); got != 1 {
		t.Fatalf("PendingLen = %d, want 1", got)
	}

	_, peeked, _ := reminderGET(t, mux, "/api/sessions/s-peek/reminders?drain=0")
	first := reminderItems(t, peeked)
	if len(first) != 1 {
		t.Fatalf("peek = %#v, want one reminder", first)
	}
	_, peekedAgain, _ := reminderGET(t, mux, "/api/sessions/s-peek/reminders?drain=0")
	second := reminderItems(t, peekedAgain)
	if len(second) != 1 || second[0]["id"] != first[0]["id"] {
		t.Fatalf("second peek = %#v, want the same reminder", second)
	}

	status, dismissed := reminderDELETE(t, mux, "/api/sessions/s-peek/reminders")
	if status != http.StatusOK {
		t.Fatalf("dismiss status = %d, want 200", status)
	}
	if dismissed["cleared"] != float64(1) {
		t.Fatalf("cleared = %v, want 1", dismissed["cleared"])
	}
	_, drained, _ := reminderGET(t, mux, "/api/sessions/s-peek/reminders")
	if items := reminderItems(t, drained); len(items) != 0 {
		t.Fatalf("after dismiss = %#v, want []", items)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_reminder_state_survives_resume
func TestReminderRoutesStateFileSurvivesRuntimeRestart(t *testing.T) {
	_, runtime, store, root := newReminderRoutesMux(t)
	if _, err := store.CreateTask("s1", tasks.CreateTaskSpec{Subject: "persisted work"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	runtime.TurnComplete("s1", []string{"task_create"})
	for i := 0; i < 3; i++ {
		runtime.TurnComplete("s1", nil)
	}
	if produced := runtime.TurnComplete("s1", []string{"read_file"}); len(produced) != 1 {
		t.Fatalf("first runtime produced %d reminders, want 1", len(produced))
	}

	dir, err := tasks.SessionStorageDir("s1", root)
	if err != nil {
		t.Fatalf("SessionStorageDir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, tasks.StateFilename))
	if err != nil {
		t.Fatalf("read reminder_state.json: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode reminder_state.json: %v", err)
	}
	if persisted["turns_total"] != float64(5) {
		t.Fatalf("turns_total = %v, want 5", persisted["turns_total"])
	}
	if persisted["last_snapshot_fingerprint"] == nil {
		t.Fatalf("last_snapshot_fingerprint = nil, want the shown fingerprint")
	}

	// A fresh runtime loads that state on its first render: the unchanged list
	// is not re-shown, so the restart does not duplicate the snapshot.
	restarted := NewReminderRuntime()
	restartedMux := http.NewServeMux()
	RegisterReminderRoutes(restartedMux, ReminderRouteDeps{Runtime: restarted})
	if produced := restarted.TurnComplete("s1", []string{"read_file"}); len(produced) != 0 {
		t.Fatalf("restarted runtime produced %d reminders, want 0", len(produced))
	}
	_, body, _ := reminderGET(t, restartedMux, "/api/sessions/s1/reminders")
	if items := reminderItems(t, body); len(items) != 0 {
		t.Fatalf("restarted read = %#v, want []", items)
	}
}

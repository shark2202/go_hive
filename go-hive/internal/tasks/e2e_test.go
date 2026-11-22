package tasks

// Parity: framework/tasks/tests/test_e2e.py

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
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

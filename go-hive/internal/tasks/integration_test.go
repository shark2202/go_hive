package tasks

// Parity: framework/tasks/tests/test_integration.py

import "testing"

func integrationStatusPtr(status TaskStatus) *TaskStatus { return &status }

func integrationCreate(t *testing.T, reg *Registry, goal string, subject string) map[string]any {
	t.Helper()
	return invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":  goal,
		"tasks": []any{map[string]any{"subject": subject}},
	})
}

// Parity: framework/tasks/tests/test_integration.py::test_session_tools_write_to_own_session_list
func TestSessionToolsWriteToOwnSessionList(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	reg := NewRegistry()
	RegisterTaskTools(reg, store, nil)

	withTestContext(t, "alice", "sess1")
	integrationCreate(t, reg, "test goal", "my work")

	sessionTasks := store.ListTasks("sess1")
	if len(sessionTasks) != 1 {
		t.Fatalf("len(sess1 tasks) = %d, want 1", len(sessionTasks))
	}
	if sessionTasks[0].Subject != "my work" {
		t.Fatalf("subject = %q, want %q", sessionTasks[0].Subject, "my work")
	}
}

// Parity: framework/tasks/tests/test_integration.py::test_workers_have_independent_session_lists
func TestWorkersHaveIndependentSessionLists(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	workerIDs := []string{"w1", "w2", "w3"}
	for _, wid := range workerIDs {
		workerReg := NewRegistry()
		RegisterTaskTools(workerReg, store, nil)
		restore := SetExecContext(ExecContext{AgentID: wid, SessionID: wid})
		integrationCreate(t, workerReg, "goal for "+wid, "setup for "+wid)
		invokeTool(t, workerReg, TaskUpdate, map[string]any{
			"id":     1,
			"status": string(TaskStatusInProgress),
		})
		restore()
	}

	for _, wid := range workerIDs {
		workerTasks := store.ListTasks(wid)
		if len(workerTasks) != 1 {
			t.Fatalf("len(%s tasks) = %d, want 1", wid, len(workerTasks))
		}
		if workerTasks[0].Owner == nil || *workerTasks[0].Owner != wid {
			t.Fatalf("%s owner = %v, want %q (auto-stamped on in_progress)", wid, workerTasks[0].Owner, wid)
		}
		if want := "setup for " + wid; workerTasks[0].Subject != want {
			t.Fatalf("%s subject = %q, want %q", wid, workerTasks[0].Subject, want)
		}
	}
}

// Parity: framework/tasks/tests/test_integration.py::test_resume_persisted_handle
func TestResumePersistedHandle(t *testing.T) {
	root := t.TempDir()
	store := NewTaskStore(root)
	sessionID := "sess_persistent"

	if _, err := store.EnsureTaskList(sessionID, nil); err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "a"}); err != nil {
		t.Fatalf("CreateTask(a): %v", err)
	}
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "b"}); err != nil {
		t.Fatalf("CreateTask(b): %v", err)
	}

	store2 := NewTaskStore(root)
	records := store2.ListTasks(sessionID)
	subjects := make([]string, 0, len(records))
	for _, record := range records {
		subjects = append(subjects, record.Subject)
	}
	if len(subjects) != 2 || subjects[0] != "a" || subjects[1] != "b" {
		t.Fatalf("subjects = %v, want [a b]", subjects)
	}
}

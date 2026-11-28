package tasks

// Parity: framework/tasks/tests/test_tools.py

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func newTestToolRegistry(store *TaskStore, pivot *PivotHandler) *Registry {
	reg := NewRegistry()
	RegisterTaskTools(reg, store, pivot)
	return reg
}

func invokeTool(t *testing.T, reg *Registry, name string, inputs map[string]any) map[string]any {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	return tool.Execute(inputs)
}

func invokeCreateOne(t *testing.T, reg *Registry, goal string, fields map[string]any) map[string]any {
	return invokeTool(t, reg, TaskCreate, map[string]any{
		"tasks": []any{fields},
		"goal":  goal,
	})
}

func withTestContext(t *testing.T, agentID, sessionID string) {
	t.Helper()
	restore := SetExecContext(ExecContext{AgentID: agentID, SessionID: sessionID})
	t.Cleanup(restore)
}

func clearTestHooks(t *testing.T) {
	t.Helper()
	ClearHooks("")
	t.Cleanup(func() { ClearHooks("") })
}

func resultInts(t *testing.T, result map[string]any, key string) []int {
	t.Helper()
	raw, ok := result[key].([]int)
	if !ok {
		t.Fatalf("result[%q] = %#v, want []int", key, result[key])
	}
	return raw
}

func taskMap(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	task, ok := result["task"].(map[string]any)
	if !ok {
		t.Fatalf("result[\"task\"] = %#v, want map", result["task"])
	}
	return task
}

func metadataMap(t *testing.T, task map[string]any) map[string]any {
	t.Helper()
	metadata, ok := task["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("task[\"metadata\"] = %#v, want map", task["metadata"])
	}
	return metadata
}

func taskListMaps(t *testing.T, result map[string]any) []map[string]any {
	t.Helper()
	raw, ok := result["tasks"].([]any)
	if !ok {
		t.Fatalf("result[\"tasks\"] = %#v, want []any", result["tasks"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("task entry = %#v, want map", item)
		}
		out = append(out, m)
	}
	return out
}

func successFlag(t *testing.T, result map[string]any) bool {
	t.Helper()
	success, ok := result["success"].(bool)
	if !ok {
		t.Fatalf("result[\"success\"] = %#v, want bool", result["success"])
	}
	return success
}

// Parity: core/framework/tasks/tests/test_tools.py::test_create_then_list
func TestCreateThenList(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	result := invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "Plan retrieval"})
	if !successFlag(t, result) {
		t.Fatalf("create failed: %#v", result)
	}
	if got := fmt.Sprint(resultInts(t, result, "task_ids")); got != "[1]" {
		t.Fatalf("task_ids = %s, want [1]", got)
	}

	result2 := invokeTool(t, reg, TaskList, map[string]any{})
	if got := result2["count"]; got != 1 {
		t.Fatalf("count = %v, want 1", got)
	}
	tasks := taskListMaps(t, result2)
	if tasks[0]["subject"] != "Plan retrieval" {
		t.Fatalf("subject = %v, want Plan retrieval", tasks[0]["subject"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_update_in_progress_auto_owner
func TestUpdateInProgressAutoOwner(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "x"})
	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "in_progress"})
	if !successFlag(t, result) {
		t.Fatalf("update failed: %#v", result)
	}
	task := taskMap(t, result)
	if task["status"] != "in_progress" {
		t.Fatalf("status = %v, want in_progress", task["status"])
	}
	if task["owner"] != "agent_a" {
		t.Fatalf("owner = %v, want agent_a (auto-filled)", task["owner"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_update_status_deleted
func TestUpdateStatusDeleted(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "x"})
	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "deleted"})
	if !successFlag(t, result) {
		t.Fatalf("delete failed: %#v", result)
	}
	if result["deleted"] != true {
		t.Fatalf("deleted = %v, want true", result["deleted"])
	}
	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 0 {
		t.Fatalf("count = %v, want 0", listed["count"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_task_list_hides_archived_from_agent
func TestTaskListHidesArchivedFromAgent(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "stays"})
	invokeTool(t, reg, TaskCreate, map[string]any{
		"tasks": []any{map[string]any{"subject": "archive me"}},
	})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 2, "status": "archived"})
	invokeTool(t, reg, TaskCreate, map[string]any{
		"tasks": []any{map[string]any{"subject": "new plan"}},
	})

	result := invokeTool(t, reg, TaskList, map[string]any{})
	tasks := taskListMaps(t, result)
	subjects := make([]string, 0, len(tasks))
	for _, task := range tasks {
		subjects = append(subjects, fmt.Sprint(task["subject"]))
	}
	want := map[string]bool{"stays": true, "new plan": true}
	if len(subjects) != len(want) {
		t.Fatalf("subjects = %v, want %v", subjects, []string{"new plan", "stays"})
	}
	for _, subject := range subjects {
		if !want[subject] {
			t.Fatalf("unexpected subject %q in %v", subject, subjects)
		}
	}
	if result["count"] != 2 {
		t.Fatalf("count = %v, want 2", result["count"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_agent_can_archive_via_task_update
func TestAgentCanArchiveViaTaskUpdate(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "Ship v1", map[string]any{"subject": "done thing"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "completed"})
	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "archived"})
	if !successFlag(t, result) {
		t.Fatalf("archive failed: %#v", result)
	}
	task := taskMap(t, result)
	if task["status"] != "archived" {
		t.Fatalf("status = %v, want archived", task["status"])
	}
	metadata := metadataMap(t, task)
	if metadata["archived_from"] != "completed" {
		t.Fatalf("archived_from = %v, want completed", metadata["archived_from"])
	}
	if metadata["archived_goal"] != "Ship v1" {
		t.Fatalf("archived_goal = %v, want Ship v1", metadata["archived_goal"])
	}

	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 0 {
		t.Fatalf("default list count = %v, want 0", listed["count"])
	}
	withArch := invokeTool(t, reg, TaskList, map[string]any{"include_archived": true})
	if withArch["count"] != 1 {
		t.Fatalf("include_archived count = %v, want 1", withArch["count"])
	}
	if taskListMaps(t, withArch)[0]["status"] != "archived" {
		t.Fatalf("include_archived status = %v, want archived", taskListMaps(t, withArch)[0]["status"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_agent_can_restore_archived_via_task_update
func TestAgentCanRestoreArchivedViaTaskUpdate(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "x"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "archived"})

	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "pending"})
	if !successFlag(t, result) {
		t.Fatalf("restore failed: %#v", result)
	}
	task := taskMap(t, result)
	if task["status"] != "pending" {
		t.Fatalf("status = %v, want pending", task["status"])
	}
	metadata := metadataMap(t, task)
	if _, ok := metadata["archived_from"]; ok {
		t.Fatalf("archived_from not stripped: %#v", metadata)
	}
	if _, ok := metadata["archived_at"]; ok {
		t.Fatalf("archived_at not stripped: %#v", metadata)
	}

	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 1 {
		t.Fatalf("count = %v, want 1", listed["count"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_get_returns_full_record
func TestGetReturnsFullRecord(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{
		"subject":     "x",
		"description": "full body",
	})
	result := invokeTool(t, reg, TaskGet, map[string]any{"id": 1})
	if got := taskMap(t, result)["description"]; got != "full body" {
		t.Fatalf("description = %v, want full body", got)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_task_not_found_is_not_error
func TestTaskNotFoundIsNotError(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 42, "subject": "ghost"})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_create_batch_creates_n_tasks_atomically
func TestCreateBatchCreatesNTasksAtomically(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"goal": "test goal",
		"tasks": []any{
			map[string]any{"subject": "step 1", "active_form": "Doing 1"},
			map[string]any{"subject": "step 2"},
			map[string]any{"subject": "step 3"},
		},
	})
	if !successFlag(t, result) {
		t.Fatalf("create failed: %#v", result)
	}
	if got := fmt.Sprint(resultInts(t, result, "task_ids")); got != "[1 2 3]" {
		t.Fatalf("task_ids = %s, want [1 2 3]", got)
	}
	message := fmt.Sprint(result["message"])
	if !strings.Contains(message, "#1-#3") && !strings.Contains(message, "#1, #2, #3") {
		t.Fatalf("message missing id range: %q", message)
	}
	if !strings.Contains(message, "Mark #1 in_progress") {
		t.Fatalf("message missing steering nudge: %q", message)
	}

	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 3 {
		t.Fatalf("count = %v, want 3", listed["count"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_create_batch_rejects_empty
func TestCreateBatchRejectsEmpty(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "a", "s")

	result := invokeTool(t, reg, TaskCreate, map[string]any{"tasks": []any{}})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
	if errText := fmt.Sprint(result["error"]); !strings.Contains(errText, "non-empty") {
		t.Fatalf("error = %q, want substring non-empty", errText)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_create_batch_rejects_malformed_spec_atomically
func TestCreateBatchRejectsMalformedSpecAtomically(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "a", "s")

	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"goal": "test goal",
		"tasks": []any{
			map[string]any{"subject": "good"},
			map[string]any{"subject": ""},
		},
	})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 0 {
		t.Fatalf("count = %v, want 0", listed["count"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_create_batch_hook_blocks_rolls_back_whole_batch
func TestCreateBatchHookBlocksRollsBackWholeBatch(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)

	_ = RegisterHook(HookTaskCreated, func(ctx TaskHookContext) error {
		if ctx.Task.Subject == "block me" {
			return NewBlockingHookError("policy")
		}
		return nil
	})

	withTestContext(t, "a", "s")
	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"goal": "test goal",
		"tasks": []any{
			map[string]any{"subject": "ok 1"},
			map[string]any{"subject": "block me"},
			map[string]any{"subject": "ok 3"},
		},
	})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
	if errText := fmt.Sprint(result["error"]); !strings.Contains(errText, "rolled back") {
		t.Fatalf("error = %q, want substring rolled back", errText)
	}
	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 0 {
		t.Fatalf("count = %v, want 0", listed["count"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_create_batch_then_single_create_keeps_id_monotonic
func TestCreateBatchThenSingleCreateKeepsIDMonotonic(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "a", "s")

	invokeTool(t, reg, TaskCreate, map[string]any{
		"goal": "test goal",
		"tasks": []any{
			map[string]any{"subject": "a"},
			map[string]any{"subject": "b"},
			map[string]any{"subject": "c"},
		},
	})
	result := invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "d"})
	if got := fmt.Sprint(resultInts(t, result, "task_ids")); got != "[4]" {
		t.Fatalf("task_ids = %s, want [4]", got)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_completion_suffix_points_to_next_pending
func TestCompletionSuffixPointsToNextPending(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "step 1"})
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "step 2"})
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "step 3"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "in_progress"})
	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "completed"})
	if !successFlag(t, result) {
		t.Fatalf("complete failed: %#v", result)
	}
	message := fmt.Sprint(result["message"])
	if !strings.Contains(message, "Next pending: #2") {
		t.Fatalf("message missing next pending: %q", message)
	}
	if !strings.Contains(message, "step 2") {
		t.Fatalf("message missing next subject: %q", message)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_completion_suffix_signals_all_done
func TestCompletionSuffixSignalsAllDone(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "only step"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "in_progress"})
	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "completed"})
	if message := fmt.Sprint(result["message"]); !strings.Contains(message, "All tasks complete") {
		t.Fatalf("message = %q, want all-done suffix", message)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_completion_suffix_skips_blocked_pending
func TestCompletionSuffixSkipsBlockedPending(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")

	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "prereq"})
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "blocked dep"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 2, "add_blocked_by": []any{1}})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "in_progress"})
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "extra step"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 3, "status": "in_progress"})
	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 3, "status": "completed"})
	if message := fmt.Sprint(result["message"]); !strings.Contains(message, "Still in progress: #1") {
		t.Fatalf("message = %q, want in-progress sibling note", message)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_hook_blocks_task_created
func TestHookBlocksTaskCreated(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)

	_ = RegisterHook(HookTaskCreated, func(ctx TaskHookContext) error {
		return NewBlockingHookError("test policy")
	})

	withTestContext(t, "agent_a", "sess_1")
	result := invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "will be aborted"})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 0 {
		t.Fatalf("count = %v, want 0", listed["count"])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_hook_blocks_task_completed
func TestHookBlocksTaskCompleted(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)

	_ = RegisterHook(HookTaskCompleted, func(ctx TaskHookContext) error {
		return NewBlockingHookError("nope")
	})

	withTestContext(t, "agent_a", "sess_1")
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "x"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "in_progress"})
	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "completed"})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
	got := invokeTool(t, reg, TaskGet, map[string]any{"id": 1})
	if status := taskMap(t, got)["status"]; status != "in_progress" {
		t.Fatalf("status = %v, want in_progress (completion vetoed)", status)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_first_task_create_requires_goal
func TestFirstTaskCreateRequiresGoal(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "a", "fresh_session")

	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"tasks": []any{map[string]any{"subject": "x"}},
	})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
	if errText := strings.ToLower(fmt.Sprint(result["error"])); !strings.Contains(errText, "goal") {
		t.Fatalf("error = %q, want goal hint", errText)
	}

	result = invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":  "research competitor pricing",
		"tasks": []any{map[string]any{"subject": "x"}},
	})
	if !successFlag(t, result) {
		t.Fatalf("create failed: %#v", result)
	}
	meta := store.GetMeta("fresh_session")
	if meta == nil {
		t.Fatal("meta = nil, want stored meta")
	}
	if meta.Goal == nil || *meta.Goal != "research competitor pricing" {
		t.Fatalf("meta.goal = %v, want research competitor pricing", meta.Goal)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_followup_task_create_inherits_goal
func TestFollowupTaskCreateInheritsGoal(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "a", "s")

	invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":  "anchor",
		"tasks": []any{map[string]any{"subject": "a"}},
	})
	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"tasks": []any{map[string]any{"subject": "b"}},
	})
	if !successFlag(t, result) {
		t.Fatalf("create failed: %#v", result)
	}
	meta := store.GetMeta("s")
	if meta == nil {
		t.Fatal("meta = nil, want stored meta")
	}
	if meta.Goal == nil || *meta.Goal != "anchor" {
		t.Fatalf("meta.goal = %v, want anchor", meta.Goal)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_task_list_surfaces_goal
func TestTaskListSurfacesGoal(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "a", "s")

	invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":  "ship the launch",
		"tasks": []any{map[string]any{"subject": "x"}},
	})
	result := invokeTool(t, reg, TaskList, map[string]any{})
	if result["goal"] != "ship the launch" {
		t.Fatalf("goal = %v, want ship the launch", result["goal"])
	}
	lines, ok := result["lines"].([]string)
	if !ok {
		t.Fatalf("lines = %#v, want []string", result["lines"])
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Goal: ship the launch") {
		t.Fatalf("lines[0] = %q, want Goal prefix", lines[0])
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_pivot_handler_routes_when_field_set
func TestPivotHandlerRoutesWhenFieldSet(t *testing.T) {
	store := NewTaskStore(t.TempDir())

	var handlerCalls []map[string]any
	pivot := &PivotHandler{
		FieldName:        "new_session",
		FieldDescription: "test",
		Handle: func(goal string, handoff any, tasks []any) map[string]any {
			handlerCalls = append(handlerCalls, map[string]any{
				"goal":    goal,
				"handoff": handoff,
				"tasks":   tasks,
			})
			return map[string]any{"success": true, "routed": "new_session", "task_ids": []int{99}}
		},
	}
	reg := newTestToolRegistry(store, pivot)
	withTestContext(t, "a", "s")

	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":        "new goal",
		"handoff":     "full context here",
		"new_session": true,
		"tasks":       []any{map[string]any{"subject": "x"}},
	})
	if !successFlag(t, result) {
		t.Fatalf("pivot failed: %#v", result)
	}
	if result["routed"] != "new_session" {
		t.Fatalf("routed = %v, want new_session", result["routed"])
	}
	if len(handlerCalls) != 1 {
		t.Fatalf("handler calls = %d, want 1", len(handlerCalls))
	}
	call := handlerCalls[0]
	if call["goal"] != "new goal" || call["handoff"] != "full context here" {
		t.Fatalf("handler args = %#v", call)
	}
	tasksArg, ok := call["tasks"].([]any)
	if !ok || len(tasksArg) != 1 {
		t.Fatalf("handler tasks = %#v, want 1 entry", call["tasks"])
	}
	if subject := tasksArg[0].(map[string]any)["subject"]; subject != "x" {
		t.Fatalf("handler task subject = %v, want x", subject)
	}
	if local := store.ListTasks("s"); len(local) != 0 {
		t.Fatalf("local list = %d tasks, want 0", len(local))
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_pivot_handler_field_name_is_per_handler
func TestPivotHandlerFieldNameIsPerHandler(t *testing.T) {
	store := NewTaskStore(t.TempDir())

	var captured []string
	pivot := &PivotHandler{
		FieldName:        "new_colony",
		FieldDescription: "colony pivot",
		Handle: func(goal string, handoff any, tasks []any) map[string]any {
			captured = append(captured, goal)
			return map[string]any{"success": true, "routed": "new_colony"}
		},
	}
	reg := newTestToolRegistry(store, pivot)

	tool, ok := reg.Get(TaskCreate)
	if !ok {
		t.Fatal("task_create not registered")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want map", tool.Parameters["properties"])
	}
	if _, ok := properties["new_colony"]; !ok {
		t.Fatal("new_colony missing from task_create schema")
	}
	if _, ok := properties["new_session"]; ok {
		t.Fatal("new_session unexpectedly present in task_create schema")
	}

	withTestContext(t, "a", "s")
	invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":       "sibling colony goal",
		"handoff":    "brief",
		"new_colony": true,
		"tasks":      []any{map[string]any{"subject": "x"}},
	})
	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":        "ignored second call",
		"new_session": true,
		"tasks":       []any{map[string]any{"subject": "y"}},
	})
	if !successFlag(t, result) {
		t.Fatalf("local create failed: %#v", result)
	}
	if _, routed := result["routed"]; routed {
		t.Fatalf("result = %#v, want local create", result)
	}
	if len(captured) != 1 || captured[0] != "sibling colony goal" {
		t.Fatalf("captured = %v, want [sibling colony goal]", captured)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_pivot_requires_goal_on_pivot_call
func TestPivotRequiresGoalOnPivotCall(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	pivot := &PivotHandler{
		FieldName:        "new_session",
		FieldDescription: "test",
		Handle: func(goal string, handoff any, tasks []any) map[string]any {
			return map[string]any{"success": true}
		},
	}
	reg := newTestToolRegistry(store, pivot)
	withTestContext(t, "a", "s")

	invokeTool(t, reg, TaskCreate, map[string]any{
		"goal":  "original",
		"tasks": []any{map[string]any{"subject": "x"}},
	})
	result := invokeTool(t, reg, TaskCreate, map[string]any{
		"handoff":     "brief",
		"new_session": true,
		"tasks":       []any{map[string]any{"subject": "y"}},
	})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}
	if errText := strings.ToLower(fmt.Sprint(result["error"])); !strings.Contains(errText, "goal") {
		t.Fatalf("error = %q, want goal hint", errText)
	}
}

// Parity: core/framework/tasks/tests/test_tools.py::test_hook_blocks_task_completed_never_writes
func TestHookBlocksTaskCompletedNeverWrites(t *testing.T) {
	clearTestHooks(t)
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)

	_ = RegisterHook(HookTaskCompleted, func(ctx TaskHookContext) error {
		return NewBlockingHookError("nope")
	})

	withTestContext(t, "agent_a", "sess_1")
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "x"})
	invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "in_progress"})
	before := store.GetTask("sess_1", 1)
	if before == nil {
		t.Fatal("task #1 missing after in_progress write")
	}
	tsBefore := before.UpdatedAt

	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "completed"})
	if successFlag(t, result) {
		t.Fatalf("success = true, want false")
	}

	after := store.GetTask("sess_1", 1)
	if after == nil {
		t.Fatal("task #1 missing after veto")
	}
	if after.Status != TaskStatusInProgress {
		t.Fatalf("status = %v, want in_progress", after.Status)
	}
	if after.UpdatedAt != tsBefore {
		t.Fatalf("updated_at changed (%v -> %v): transient write happened", tsBefore, after.UpdatedAt)
	}
}

// Parity: core/framework/tasks/tools/session_tools.py::_make_update_executor
// (TaskStatus(status_in) rejects an unknown value before the store is
// touched; Go must not persist it or Python reads the document as corrupt).
func TestUpdateRejectsInvalidStatus(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "x"})

	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "bogus"})
	if successFlag(t, result) {
		t.Fatalf("invalid status accepted: %#v", result)
	}
	if got, _ := result["error"].(string); !strings.Contains(got, "'bogus' is not a valid TaskStatus") {
		t.Fatalf("error = %q", got)
	}

	path, err := store.DocPath("sess_1")
	if err != nil {
		t.Fatalf("DocPath: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile tasks.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("tasks.json unreadable: %v", err)
	}
	records, _ := doc["tasks"].([]any)
	if len(records) != 1 {
		t.Fatalf("records = %#v", doc["tasks"])
	}
	if status := records[0].(map[string]any)["status"]; status != "pending" {
		t.Fatalf("persisted status = %v, want pending", status)
	}
	listed := invokeTool(t, reg, TaskList, map[string]any{})
	if listed["count"] != 1 {
		t.Fatalf("task list count = %v, want 1", listed["count"])
	}

	// The rejection must not wedge the tool: a subsequent valid update still
	// lands, and the file stays readable.
	ok := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": "completed"})
	if !successFlag(t, ok) {
		t.Fatalf("valid status update after rejection failed: %#v", ok)
	}
	if rec := store.GetTask("sess_1", 1); rec == nil || rec.Status != TaskStatusCompleted {
		t.Fatalf("record = %#v, want completed", rec)
	}
}

// Parity: core/framework/tasks/tools/session_tools.py::_make_update_executor
// (a non-string status cannot construct a TaskStatus either).
func TestUpdateRejectsNonStringStatus(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	reg := newTestToolRegistry(store, nil)
	withTestContext(t, "agent_a", "sess_1")
	invokeCreateOne(t, reg, "test goal", map[string]any{"subject": "x"})

	result := invokeTool(t, reg, TaskUpdate, map[string]any{"id": 1, "status": 123})
	if successFlag(t, result) {
		t.Fatalf("non-string status accepted: %#v", result)
	}
	if got, _ := result["error"].(string); !strings.Contains(got, "123 is not a valid TaskStatus") {
		t.Fatalf("error = %q", got)
	}
	if rec := store.GetTask("sess_1", 1); rec == nil || rec.Status != TaskStatusPending {
		t.Fatalf("record = %#v, want pending", rec)
	}
}

// Parity: core/framework/tasks/store.py — pydantic rejects the whole document
// on read, so the Go writer refuses a record with an unknown status.
func TestWriteDocRefusesInvalidStatus(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	doc := &TaskListDocument{
		Meta: NewTaskListMeta(),
		Tasks: []*TaskRecord{{
			ID:      1,
			Subject: "x",
			Status:  TaskStatus("bogus"),
		}},
	}
	if err := store.writeDoc("sess_1", doc); err == nil {
		t.Fatal("writeDoc accepted an invalid status")
	}
}

// Parity: core/framework/tasks/tools/colony_tools.py::_make_update_executor
// (the template updater constructs TaskStatus the same way).
func TestColonyTemplateUpdateRejectsInvalidStatus(t *testing.T) {
	store := NewTaskStore(t.TempDir())
	listID := "colony:test"
	added := makeColonyAddExecutor(store, listID)(map[string]any{"subject": "x"})
	if success, _ := added["success"].(bool); !success {
		t.Fatalf("add failed: %#v", added)
	}

	updated := makeColonyUpdateExecutor(store, listID)(map[string]any{"id": 1, "status": "bogus"})
	if success, _ := updated["success"].(bool); success {
		t.Fatalf("invalid status accepted: %#v", updated)
	}
	if got, _ := updated["error"].(string); !strings.Contains(got, "'bogus' is not a valid TaskStatus") {
		t.Fatalf("error = %q", got)
	}
	if rec := store.GetTask(listID, 1); rec == nil || rec.Status != TaskStatusPending {
		t.Fatalf("record = %#v, want pending", rec)
	}
}

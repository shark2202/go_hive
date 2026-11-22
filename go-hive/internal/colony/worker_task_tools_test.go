package colony

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// workerTaskPayload decodes a task tool result into the dict shape both the
// queen and worker surfaces return.
func workerTaskPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("task tool payload is not JSON (%q): %v", content, err)
	}
	return payload
}

// Parity: framework/agents/queen/worker_definition.py (workers have
// task_create / task_update / task_list / task_get)
func TestWorkerSurfaceAdvertisesTaskTools(t *testing.T) {
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
	})
	names := toolNames(runtime.workerToolDefs())
	for _, want := range []string{"task_create", "task_update", "task_list", "task_get"} {
		if !hasTool(names, want) {
			t.Fatalf("worker surface %v is missing %s", names, want)
		}
	}
}

// Parity-exempt: the Go port binds the task list per worker; upstream's
// session id is the worker stream, so two workers never share a list.
func TestWorkerTaskListsAreIsolatedPerWorker(t *testing.T) {
	root := t.TempDir()
	first := newWorkerTaskToolSet(filepath.Join(root, "workers", "w_alpha"), "w_alpha")
	second := newWorkerTaskToolSet(filepath.Join(root, "workers", "w_bravo"), "w_bravo")
	if first == nil || second == nil {
		t.Fatal("worker task tool sets were not built")
	}
	if got := len(first.Defs()); got != 4 {
		t.Fatalf("worker task tool defs = %d, want 4", got)
	}

	content, isError, handled := first.Execute("task_create", map[string]any{
		"goal":  "ship the worker task list",
		"tasks": []any{map[string]any{"subject": "write the test"}},
	})
	if !handled || isError {
		t.Fatalf("task_create: handled=%v isError=%v content=%s", handled, isError, content)
	}
	payload := workerTaskPayload(t, content)
	if success, _ := payload["success"].(bool); !success {
		t.Fatalf("task_create did not succeed: %s", content)
	}

	if content, _, _ := second.Execute("task_list", map[string]any{}); content == "" {
		t.Fatal("second worker's task_list returned nothing")
	} else if list := workerTaskPayload(t, content); len(list["tasks"].([]any)) != 0 {
		t.Fatalf("second worker sees the first worker's tasks: %s", content)
	}
	list, _, _ := first.Execute("task_list", map[string]any{})
	rows, _ := workerTaskPayload(t, list)["tasks"].([]any)
	if len(rows) != 1 {
		t.Fatalf("first worker's task list = %s, want one task", list)
	}

	// The list lives under the worker's own directory, not in a shared place.
	stored, err := tasks.NewTaskStore(filepath.Join(root, "workers", "w_alpha")).DocPath("w_alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("task list is not under the worker directory (%s): %v", stored, err)
	}
	if !strings.HasPrefix(stored, filepath.Join(root, "workers", "w_alpha")) {
		t.Fatalf("task list escaped the worker directory: %s", stored)
	}
}

// Parity-exempt: the executor route is what the agent loop actually calls; the
// binding above is useless if the runtime never dispatches to it.
func TestWorkerExecutorRoutesTaskTools(t *testing.T) {
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "host executor"}, nil
		},
	})
	workerDir := filepath.Join(runtime.StoragePath(), "workers", "w_task")
	worker := NewWorker("w_task", "plan the work", "worker:w_task")
	worker.StoragePath = workerDir
	executor := runtime.executorFor(worker)

	result, err := executor(agentloop.ToolCallRequest{
		Name:  "task_create",
		Input: map[string]any{"goal": "route to the worker list", "tasks": []any{map[string]any{"subject": "first step"}}},
	})
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	if result.IsError {
		t.Fatalf("task_create through the executor failed: %s", result.Content)
	}
	if payload := workerTaskPayload(t, result.Content); payload["success"] != true {
		t.Fatalf("task_create payload = %s", result.Content)
	}

	// A name the task tools do not own still reaches the host executor.
	other, err := executor(agentloop.ToolCallRequest{Name: "read_file", Input: map[string]any{"path": "x"}})
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	if other.Content != "host executor" {
		t.Fatalf("read_file fell through to %q, want the host executor", other.Content)
	}
}

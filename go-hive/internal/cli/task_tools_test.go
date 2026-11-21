package cli

// Session task tools through the per-session catalogue: creation, listing,
// status transitions and per-session isolation, exercised end to end on the
// same path the chat runtime uses (Defs -> Execute).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tasks"
	"github.com/aden-hive/hive-go/internal/toolgolden"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// newTaskCatalogue builds one session catalogue whose root is a real
// queens/<queen>/sessions/<session_id> directory, so the task store resolves
// the same directory the runtime hands the tools.
func newTaskCatalogue(t *testing.T, homeRoot, queen, sessionID string) (*sessionToolCatalogue, string) {
	t.Helper()
	sessionDir := filepath.Join(homeRoot, "queens", queen, "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	catalogue := newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), hivehome.Home{Root: homeRoot})
	return catalogue, sessionDir
}

// runTaskTool executes one task tool through the catalogue and returns the
// decoded JSON payload, failing when the call reports a tool error.
func runTaskTool(t *testing.T, catalogue *sessionToolCatalogue, name string, input map[string]any) map[string]any {
	t.Helper()
	output, isError, err := catalogue.Execute(context.Background(), name, input)
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if isError {
		t.Fatalf("%s: tool error: %s", name, output)
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(output), &payload); jsonErr != nil {
		t.Fatalf("%s: output is not JSON (%q): %v", name, output, jsonErr)
	}
	return payload
}

// Parity-exempt: Go 特有的会话工具表接线守护测试，上游无对应物
func TestSessionTaskToolsAreWiredIntoTheCatalogue(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session-wiring")
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: filepath.Dir(root)})

	defined := map[string]bool{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = true
	}
	for _, name := range tasks.AllTaskTools {
		if !defined[name] {
			t.Fatalf("task tool %q missing from the catalogue defs", name)
		}
		if _, builtinName := catalogue.builtinNames[name]; builtinName {
			t.Fatalf("task tool %q collided with a builtin", name)
		}
		if !catalogue.taskTools.Has(name) {
			t.Fatalf("task tool %q is not dispatchable", name)
		}
	}
	for _, def := range catalogue.taskTools.Defs() {
		if def.Description == "" {
			t.Fatalf("task tool %q has no description", def.Name)
		}
		if schemaType, ok := def.Parameters["type"]; !ok || schemaType != "object" {
			t.Fatalf("task tool %q schema type = %v, want object", def.Name, def.Parameters["type"])
		}
	}
}

// Parity: framework/tasks/tests/test_tools.py::test_create_then_list, framework/tasks/tests/test_tools.py::test_update_in_progress_auto_owner
func TestSessionTaskToolsCreateListUpdateAndComplete(t *testing.T) {
	homeRoot := t.TempDir()
	catalogue, sessionDir := newTaskCatalogue(t, homeRoot, "default", "sess_tasks")

	created := runTaskTool(t, catalogue, tasks.TaskCreate, map[string]any{
		"goal": "ship the session task tools",
		"tasks": []any{
			map[string]any{"subject": "wire the tools"},
			map[string]any{"subject": "verify the plan"},
		},
	})
	if created["success"] != true {
		t.Fatalf("task_create: %v", created)
	}
	ids, ok := created["task_ids"].([]any)
	if !ok || len(ids) != 2 || ids[0].(float64) != 1 || ids[1].(float64) != 2 {
		t.Fatalf("task_ids = %v, want [1 2]", created["task_ids"])
	}

	listed := runTaskTool(t, catalogue, tasks.TaskList, map[string]any{})
	if listed["count"].(float64) != 2 {
		t.Fatalf("task_list count = %v, want 2", listed["count"])
	}
	if listed["goal"] != "ship the session task tools" {
		t.Fatalf("task_list goal = %v", listed["goal"])
	}
	records := listed["tasks"].([]any)
	if subject := records[0].(map[string]any)["subject"]; subject != "wire the tools" {
		t.Fatalf("task #1 subject = %v", subject)
	}

	updated := runTaskTool(t, catalogue, tasks.TaskUpdate, map[string]any{
		"id":     1,
		"status": string(tasks.TaskStatusInProgress),
	})
	taskBody, ok := updated["task"].(map[string]any)
	if !ok {
		t.Fatalf("task_update task = %#v", updated["task"])
	}
	if taskBody["status"] != "in_progress" {
		t.Fatalf("status = %v, want in_progress", taskBody["status"])
	}
	if taskBody["owner"] != taskToolAgentID {
		t.Fatalf("owner = %v, want %q (auto-filled on in_progress)", taskBody["owner"], taskToolAgentID)
	}

	completed := runTaskTool(t, catalogue, tasks.TaskUpdate, map[string]any{
		"id":     1,
		"status": string(tasks.TaskStatusCompleted),
	})
	if got := completed["task"].(map[string]any)["status"]; got != "completed" {
		t.Fatalf("status = %v, want completed", got)
	}

	final := runTaskTool(t, catalogue, tasks.TaskList, map[string]any{})
	records = final["tasks"].([]any)
	if got := records[0].(map[string]any)["status"]; got != "completed" {
		t.Fatalf("listed task #1 status = %v, want completed", got)
	}

	// The list must live in the session's own directory, not a fallback.
	if _, err := os.Stat(filepath.Join(sessionDir, tasks.DocFilename)); err != nil {
		t.Fatalf("task list not stored in the session directory: %v", err)
	}
}

// Parity: framework/tasks/tests/test_integration.py::test_workers_have_independent_session_lists
func TestSessionTaskToolsAreIsolatedPerSession(t *testing.T) {
	homeRoot := t.TempDir()
	catalogueA, dirA := newTaskCatalogue(t, homeRoot, "default", "sess_a")
	catalogueB, dirB := newTaskCatalogue(t, homeRoot, "default", "sess_b")

	created := runTaskTool(t, catalogueA, tasks.TaskCreate, map[string]any{
		"goal":  "session A work",
		"tasks": []any{map[string]any{"subject": "only in A"}},
	})
	if created["success"] != true {
		t.Fatalf("task_create on session A: %v", created)
	}

	listedB := runTaskTool(t, catalogueB, tasks.TaskList, map[string]any{})
	if listedB["count"].(float64) != 0 {
		t.Fatalf("session B sees %v tasks, want 0", listedB["count"])
	}
	listedA := runTaskTool(t, catalogueA, tasks.TaskList, map[string]any{})
	if listedA["count"].(float64) != 1 {
		t.Fatalf("session A sees %v tasks, want 1", listedA["count"])
	}
	if _, err := os.Stat(filepath.Join(dirA, tasks.DocFilename)); err != nil {
		t.Fatalf("session A has no task list: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirB, tasks.DocFilename)); !os.IsNotExist(err) {
		t.Fatalf("session B task list exists unexpectedly: %v", err)
	}
}

// Parity: framework/tasks/tests/test_tools.py::test_first_task_create_requires_goal
func TestSessionTaskToolsRequireGoalOnFreshList(t *testing.T) {
	homeRoot := t.TempDir()
	catalogue, _ := newTaskCatalogue(t, homeRoot, "default", "sess_goal")

	payload := runTaskTool(t, catalogue, tasks.TaskCreate, map[string]any{
		"tasks": []any{map[string]any{"subject": "ungoaled"}},
	})
	if payload["success"] != false {
		t.Fatalf("goal-less create reported success: %v", payload)
	}
	if _, ok := payload["error"].(string); !ok {
		t.Fatalf("goal-less create has no error message: %v", payload)
	}
	listed := runTaskTool(t, catalogue, tasks.TaskList, map[string]any{})
	if listed["count"].(float64) != 0 {
		t.Fatalf("rejected create left %v tasks behind", listed["count"])
	}
}

// Parity-exempt: the upstream task tools get this validation from pydantic
// before the tool body runs, so there is no Python test target to cite.
//
// TestSessionTaskToolsValidateArgumentsLikePython pins the two tool-surface
// validations the output-contract lane added: Python's task tools are pydantic
// models, so a bad id or an unknown status raises before the store is touched
// and reaches the model as an is_error JSON envelope. The port used to coerce
// a non-numeric id to 0 and to accept any status string.
func TestSessionTaskToolsValidateArgumentsLikePython(t *testing.T) {
	homeRoot := t.TempDir()
	catalogue, _ := newTaskCatalogue(t, homeRoot, "default", "sess_validate")

	cases := []struct {
		name      string
		tool      string
		input     map[string]any
		wantError string
	}{
		{
			name:      "unknown status",
			tool:      tasks.TaskUpdate,
			input:     map[string]any{"id": "1", "status": "bogus"},
			wantError: "'bogus' is not a valid TaskStatus",
		},
		{
			name:      "non-numeric id",
			tool:      tasks.TaskGet,
			input:     map[string]any{"id": "no-such-task"},
			wantError: "invalid task id: 'no-such-task' (expected an integer)",
		},
		{
			name:      "fractional id",
			tool:      tasks.TaskUpdate,
			input:     map[string]any{"id": 1.5, "status": "completed"},
			wantError: "invalid task id: 1.5 (expected an integer)",
		},
		{
			name:      "missing id",
			tool:      tasks.TaskGet,
			input:     map[string]any{},
			wantError: "invalid task id: missing (expected an integer)",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output, isError, err := catalogue.Execute(context.Background(), testCase.tool, testCase.input)
			if err != nil {
				t.Fatalf("%s: transport error: %v", testCase.tool, err)
			}
			if !isError {
				t.Fatalf("%s(%v) = %q, want an is_error result", testCase.tool, testCase.input, output)
			}
			var payload map[string]any
			if jsonErr := json.Unmarshal([]byte(output), &payload); jsonErr != nil {
				t.Fatalf("%s: output is not JSON (%q): %v", testCase.tool, output, jsonErr)
			}
			if payload["error"] != testCase.wantError {
				t.Fatalf("%s error = %v, want %q", testCase.tool, payload["error"], testCase.wantError)
			}
		})
	}

	// A numeric string id stays valid: Python's int("999") succeeds and the
	// not-found envelope is a normal (non-error) result.
	payload := runTaskTool(t, catalogue, tasks.TaskGet, map[string]any{"id": "999"})
	if payload["success"] != false || payload["task_id"].(float64) != 999 {
		t.Fatalf("numeric-string id payload = %v", payload)
	}
}

// Parity-exempt: Go-side golden harness; replays the captured task scenarios in
// docs/tool-output-contract.md.
//
// TestTaskToolErrorsMatchCapturedGolden compares the two validation envelopes
// with the committed capture: the bad-status text is byte-identical to Python's
// pydantic message, the bad-id envelope is the reviewed divergence
// (docs/tool-output-contract.md).
func TestTaskToolErrorsMatchCapturedGolden(t *testing.T) {
	scenarios, err := toolgolden.LoadScenarios("../../../docs/e2e-assets/tool-output/results.json")
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	homeRoot := t.TempDir()
	catalogue, _ := newTaskCatalogue(t, homeRoot, "default", "sess_golden")

	cases := []struct {
		id    string
		tool  string
		input map[string]any
	}{
		{id: "task_update_bad_status", tool: tasks.TaskUpdate, input: map[string]any{"id": "2", "status": "bogus"}},
		{id: "task_get_bad_id", tool: tasks.TaskGet, input: map[string]any{"id": "no-such-task"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.id, func(t *testing.T) {
			golden, ok := scenarios[testCase.id]
			if !ok {
				t.Fatalf("scenario %s missing from the capture", testCase.id)
			}
			output, isError, err := catalogue.Execute(context.Background(), testCase.tool, testCase.input)
			if err != nil {
				t.Fatalf("%s: %v", testCase.tool, err)
			}
			content := output
			if isError {
				content = "ERROR: " + output
			}
			got := toolgolden.Canonicalize(content, toolgolden.DefaultOptions())
			if !got.Equal(golden.Go) {
				t.Fatalf("canonical envelope drifted from the capture.\n got: %#v\nwant: %#v", got, golden.Go)
			}
		})
	}
}

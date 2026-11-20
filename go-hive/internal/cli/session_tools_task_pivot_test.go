package cli

// task_create(new_session=true) wiring: the model-visible definition must be the
// Python contract, and the argument must actually fork a successor session —
// through the same catalogue executor the chat runtime calls.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tasks"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// taskCreatePythonContract decodes the one field this test needs from the frozen
// Python contract: task_create's model-visible schema.
func taskCreatePythonContract(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join("..", "..", "..", "contracts", "tool-definitions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("contract manifest not present (%v)", err)
	}
	var doc struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("contract manifest is unreadable: %v", err)
	}
	for _, tool := range doc.Tools {
		if tool.Name == tasks.TaskCreate {
			return map[string]any{"description": tool.Description, "input_schema": tool.InputSchema}
		}
	}
	t.Fatalf("%s missing from the contract manifest", tasks.TaskCreate)
	return nil
}

func findDef(t *testing.T, defs []llm.ToolDef, name string) llm.ToolDef {
	t.Helper()
	for _, def := range defs {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("%s missing from the catalogue", name)
	return llm.ToolDef{}
}

// TestTaskCreatePivotDefinitionMatchesPythonContract pins the queen's
// task_create exactly to the Python model-visible contract (4 arguments incl.
// new_session + handoff) and keeps the plain 2-argument shape for catalogues
// without a pivot (workers).
// Parity: core/framework/tasks/tests/test_tools.py::test_pivot_handler_field_name_is_per_handler
// (the field-name/schema coupling) — the byte-exact strings come from the frozen
// contract manifest rather than a Python unit test.
func TestTaskCreatePivotDefinitionMatchesPythonContract(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	root := filepath.Join(home.Root, "session-1")
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	session := hiveserver.NewSession("session-1", eventbus.NewEventBus())
	session.QueenName = "default"
	session.QueenDir = filepath.Join(home.Root, "queens", "default", "sessions", "session-1")

	// Without a pivot the schema stays the plain worker shape.
	plain := findDef(t, catalogue.Defs(), tasks.TaskCreate)
	plainProps := plain.Parameters["properties"].(map[string]any)
	if _, present := plainProps["new_session"]; present {
		t.Fatal("new_session must not be advertised without a pivot handler")
	}

	enableTaskPivot(catalogue, session, taskPivotWiring{
		manager: hiveserver.NewSessionManager(),
		home:    home,
		create: func(hiveserver.CreateSessionParams) (*hiveserver.Session, error) {
			return hiveserver.NewSession("successor", eventbus.NewEventBus()), nil
		},
	})

	def := findDef(t, catalogue.Defs(), tasks.TaskCreate)
	contract := taskCreatePythonContract(t)
	if def.Description != contract["description"].(string) {
		t.Fatalf("description is not the Python contract (got %d bytes, want %d)",
			len(def.Description), len(contract["description"].(string)))
	}
	got, err := json.Marshal(def.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(contract["input_schema"].(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("schema is not the Python contract\ngot:  %s\nwant: %s", got, want)
	}
}

// TestTaskCreateNewSessionForksThroughTheExecutor drives the real catalogue
// executor: a task_create with new_session=true and a handoff must fork a
// successor (factory called, old session superseded) and answer with the
// Python tool-result shape; a blank handoff must refuse before forking.
// Parity-exempt: the fork itself has no Python unit test — it is covered
// end-to-end by core/framework/server/tests/test_session_supersession.py. This
// asserts the Go wiring at the catalogue-executor seam.
func TestTaskCreateNewSessionForksThroughTheExecutor(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	oldDir := home.QueenSessionDir("default", "session-1")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(oldDir, builtin.New(oldDir), home)
	session := hiveserver.NewSession("session-1", eventbus.NewEventBus())
	session.QueenName = "default"
	session.QueenDir = oldDir

	var created []hiveserver.CreateSessionParams
	enableTaskPivot(catalogue, session, taskPivotWiring{
		manager: hiveserver.NewSessionManager(),
		home:    home,
		create: func(params hiveserver.CreateSessionParams) (*hiveserver.Session, error) {
			created = append(created, params)
			id := ""
			if params.SessionID != nil {
				id = *params.SessionID
			}
			record := hiveserver.NewSession(id, eventbus.NewEventBus())
			record.QueenName = "default"
			record.QueenDir = home.QueenSessionDir("default", id)
			return record, nil
		},
	})

	// Blank handoff: refuse, do not fork.
	content, isError, err := catalogue.Execute(context.Background(), tasks.TaskCreate, map[string]any{
		"goal":        "new work",
		"new_session": true,
		"tasks":       []any{map[string]any{"subject": "step one"}},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if isError || !strings.Contains(content, "requires a `handoff`") {
		t.Fatalf("blank-handoff result = %q (isError=%v)", content, isError)
	}
	if len(created) != 0 {
		t.Fatalf("factory called with a blank handoff: %#v", created)
	}

	// A real handoff forks and answers with the upstream payload shape.
	content, isError, err = catalogue.Execute(context.Background(), tasks.TaskCreate, map[string]any{
		"goal":        "new work",
		"new_session": true,
		"handoff":     "the user wants campaign X; account acme; done means 10 leads",
		"tasks":       []any{map[string]any{"subject": "step one"}, map[string]any{"subject": "step two"}},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if isError {
		t.Fatalf("fork reported an error: %s", content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v (%s)", err, content)
	}
	if payload["success"] != true || payload["new_session"] != true {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["instruction"] == nil || !strings.Contains(payload["message"].(string), "silently swapped") {
		t.Fatalf("payload message/instruction missing: %#v", payload)
	}
	if len(created) != 1 {
		t.Fatalf("factory calls = %d, want 1", len(created))
	}
	if created[0].InitialPrompt == nil || !strings.Contains(*created[0].InitialPrompt, "task_list") {
		t.Fatalf("successor kickoff prompt = %v", created[0].InitialPrompt)
	}
	if session.SupersededBy == "" {
		t.Fatal("old session must be marked superseded")
	}
	if _, statErr := os.Stat(filepath.Join(home.QueenSessionDir("default", session.SupersededBy), "tasks.json")); statErr != nil {
		t.Fatalf("successor task plan not seeded: %v", statErr)
	}
}

// TestTaskCreateNonPivotSkipsFork: without new_session the executor creates
// tasks in the current session, as before.
// Parity: core/framework/tasks/tests/test_tools.py::test_pivot_handler_field_name_is_per_handler
// (a call without the pivot field stays a local create).
func TestTaskCreateNonPivotSkipsFork(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	root := filepath.Join(home.Root, "session-1")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	session := hiveserver.NewSession("session-1", eventbus.NewEventBus())
	session.QueenName = "default"
	session.QueenDir = root
	factoryCalls := 0
	enableTaskPivot(catalogue, session, taskPivotWiring{
		manager: hiveserver.NewSessionManager(),
		home:    home,
		create: func(hiveserver.CreateSessionParams) (*hiveserver.Session, error) {
			factoryCalls++
			return nil, nil
		},
	})

	content, isError, err := catalogue.Execute(context.Background(), tasks.TaskCreate, map[string]any{
		"goal":  "local work",
		"tasks": []any{map[string]any{"subject": "step one"}},
	})
	if err != nil || isError {
		t.Fatalf("local create failed: %v %s", err, content)
	}
	if factoryCalls != 0 {
		t.Fatalf("fork ran without new_session (factory calls = %d)", factoryCalls)
	}
	records := tasks.NewTaskStore(home.Root).ListTasks("session-1")
	if len(records) != 1 {
		t.Fatalf("local tasks = %d, want 1", len(records))
	}
}

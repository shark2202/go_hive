package colony

// Overseer task-reminder coverage: upstream's overseer is a full AgentLoop
// whose task source reads the session list; this port scopes the overseer's
// task tools to the colony storage path, so the reminder has to be bound to
// that same scope.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// Parity: core/framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (the overseer's task source reads its own colony-rooted plan)
func TestOverseerTaskReminderReadsTheOverseerPlan(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_overseer_tasks",
	})
	worker := NewWorker("overseer:colony_overseer_tasks", "", "overseer")
	worker.Persistent = true
	if colony.overseerTasks == nil {
		t.Fatal("colony runtime has no overseer task set")
	}
	content, isError, err := colony.overseerTasks.Execute("task_create", map[string]any{
		"goal":  "steer the colony",
		"tasks": []any{map[string]any{"subject": "review worker reports"}},
	})
	if err != nil || isError {
		t.Fatalf("overseer task_create err=%v isError=%v: %s", err, isError, content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("overseer task_create payload: %v", err)
	}
	if success, _ := payload["success"].(bool); !success {
		t.Fatalf("overseer task_create failed: %s", content)
	}

	tools := colony.workerTools(worker)
	worker.Context = colony.buildContext(worker, colony.agentSpec)
	loop := agentloop.NewAgentLoop(agentloop.DefaultLoopConfig(), colony.executorFor(worker))
	stop := colony.startOverseerReminders(loop, worker, loop.Config, tools)
	defer stop()

	// The loop re-binds from the AgentContext at turn start; do the same and
	// prove applies_to sees the overseer's task tools there.
	turnCtx := agentloop.NewReminderAgentContext(worker.Context)
	loop.Hub.Bind(turnCtx)
	if names := loop.Hub.ActiveSourceNames(); !containsName(names, "tasks") {
		t.Fatalf("overseer active sources = %v, want the task reminder bound", names)
	}
	items := loop.Hub.Collect(agentloop.SESSION_START, turnCtx, nil, "")
	if len(items) != 1 || items[0].Source != "tasks" {
		t.Fatalf("overseer session-start reminders = %+v, want one tasks snapshot", items)
	}
	body := items[0].Body
	for _, want := range []string{
		"Goal: steer the colony",
		"#1. [pending] review worker reports",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("overseer task snapshot missing %q:\n%s", want, body)
		}
	}
}

// Parity: core/framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (a real overseer turn receives its own task snapshot; not just registration)
func TestOverseerTaskReminderReachesARealOverseerTurn(t *testing.T) {
	model := newScriptedLLM(textResponse("Standing by."))
	runtime := newOverseerParkRuntime(t, model, eventbus.NewEventBus())

	content, isError, err := runtime.overseerTasks.Execute("task_create", map[string]any{
		"goal":  "steer the live colony",
		"tasks": []any{map[string]any{"subject": "review worker reports"}},
	})
	if err != nil || isError {
		t.Fatalf("overseer task_create err=%v isError=%v: %s", err, isError, content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("overseer task_create payload: %v", err)
	}
	if success, _ := payload["success"].(bool); !success {
		t.Fatalf("overseer task_create failed: %s", content)
	}

	if _, err := runtime.StartOverseer(context.Background(), overseerParkSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForColony(t, 5*time.Second, func() bool {
		_, ok := model.firstCall()
		return ok
	}) {
		t.Fatal("the overseer never made its first model call")
	}
	call, _ := model.firstCall()
	joined := ""
	for _, message := range call.Messages {
		joined += message.Content + "\n"
	}
	for _, want := range []string{
		"<system-reminder>",
		"Goal: steer the live colony",
		"#1. [pending] review worker reports",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("overseer first request missing %q:\n%s", want, joined)
		}
	}
}

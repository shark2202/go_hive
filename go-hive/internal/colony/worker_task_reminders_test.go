package colony

// Worker/overseer task-reminder coverage.
//
// Upstream runs one process-wide task store, so TaskReminderSource reads a
// worker's task list by session id alone. The Go port scopes worker and
// overseer task tools to their own stores, so the reminder must be bound to
// that same (store, session id) pair or it renders from an empty list. These
// tests drive the production wiring and read the reminder out of it.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func newWorkerTaskReminderColony(t *testing.T, streamID string) (*ColonyRuntime, *Worker) {
	t.Helper()
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    streamID,
	})
	worker := NewWorker("w1", "do the thing", "worker:w1")
	if err := os.MkdirAll(colony.workerStorage(worker.ID), 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	// A real worker carries an AgentContext; AgentLoop.ExecuteTurn re-binds
	// the hub from it, so the tests do the same below.
	worker.Context = colony.buildContext(worker, colony.agentSpec)
	return colony, worker
}

// bindWorkerTurnReminders reproduces what AgentLoop.ExecuteTurn does at the
// start of a turn: build the reminder view from the worker's AgentContext and
// bind the hub to it.
func bindWorkerTurnReminders(t *testing.T, worker *Worker, loop *agentloop.AgentLoop, tools []llm.ToolDef) *agentloop.ReminderAgentContext {
	t.Helper()
	worker.Context.AvailableTools = tools
	ctx := agentloop.NewReminderAgentContext(worker.Context)
	loop.Hub.Bind(ctx)
	return ctx
}

// createWorkerTasks writes a plan through the worker's real task tool set —
// the same store/session binding the worker's executor uses.
func createWorkerTasks(t *testing.T, colony *ColonyRuntime, worker *Worker, goal string, subjects ...string) {
	t.Helper()
	set := newWorkerTaskToolSet(colony.workerStorage(worker.ID), worker.ID)
	specs := make([]any, 0, len(subjects))
	for _, subject := range subjects {
		specs = append(specs, map[string]any{"subject": subject})
	}
	content, isError, handled := set.Execute("task_create", map[string]any{
		"goal":  goal,
		"tasks": specs,
	})
	if !handled || isError {
		t.Fatalf("task_create handled=%v isError=%v: %s", handled, isError, content)
	}
	payload := workerTaskPayload(t, content)
	if success, _ := payload["success"].(bool); !success {
		t.Fatalf("task_create failed: %s", content)
	}
}

// Parity: core/framework/tasks/reminders.py::TaskReminderSource.render
func TestWorkerTaskReminderRendersTheWorkersOwnPlan(t *testing.T) {
	colony, worker := newWorkerTaskReminderColony(t, "colony_worker_tasks")
	createWorkerTasks(t, colony, worker, "finish the worker plan", "collect inputs", "write the report")

	tools := workerTaskToolDefs()
	loop, stop := colony.newWorkerAgentLoop(worker, agentloop.DefaultLoopConfig(), tools)
	defer stop()
	loop.Hub.Stop()

	ctx := bindWorkerTurnReminders(t, worker, loop, tools)
	items := loop.Hub.Collect(agentloop.SESSION_START, ctx, nil, "")
	if len(items) != 1 || items[0].Source != "tasks" {
		t.Fatalf("worker session-start reminders = %+v, want one tasks snapshot", items)
	}
	body := items[0].Body
	for _, want := range []string{
		"Goal: finish the worker plan",
		"Here are the existing tasks:",
		"#1. [pending] collect inputs",
		"#2. [pending] write the report",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("worker task snapshot missing %q:\n%s", want, body)
		}
	}
}

// Parity: core/framework/tasks/reminders.py::TaskReminderSource.render (the
// POST_TOOL_USE drift path after a task_update)
func TestWorkerTaskUpdateReminderRendersForTheWorkerSession(t *testing.T) {
	colony, worker := newWorkerTaskReminderColony(t, "colony_worker_task_update")
	createWorkerTasks(t, colony, worker, "close out the worker plan", "ship the change")

	tools := workerTaskToolDefs()
	loop, stop := colony.newWorkerAgentLoop(worker, agentloop.DefaultLoopConfig(), tools)
	defer stop()
	loop.Hub.Stop()

	// Warm past the reminder warmup, then complete the only task and let one
	// non-task turn pass: the drift nudge's all_done branch is due.
	loop.Hub.ObserveTurn([]string{"task_create"})
	for i := 0; i < 3; i++ {
		loop.Hub.ObserveTurn(nil)
	}
	set := newWorkerTaskToolSet(colony.workerStorage(worker.ID), worker.ID)
	content, isError, handled := set.Execute("task_update", map[string]any{
		"id":     1,
		"status": string("completed"),
	})
	if !handled || isError {
		t.Fatalf("task_update handled=%v isError=%v: %s", handled, isError, content)
	}
	loop.Hub.ObserveTurn([]string{"task_update"})
	loop.Hub.ObserveTurn(nil)

	ctx := bindWorkerTurnReminders(t, worker, loop, tools)
	items := loop.Hub.Collect(agentloop.POST_TOOL_USE, ctx, nil, "")
	if len(items) != 1 || items[0].Source != "tasks" {
		t.Fatalf("worker post-tool reminders = %+v, want one task drift nudge", items)
	}
	body := items[0].Body
	for _, want := range []string{"Goal: close out the worker plan", "No active task"} {
		if !strings.Contains(body, want) {
			t.Fatalf("worker task drift nudge missing %q:\n%s", want, body)
		}
	}
}

// Parity: core/framework/agent_loop/idle_nudge.py::_open_tasks/_session_goal
// (worker AgentContext carries the providers, not just the ticker's view)
func TestWorkerLoopInstallsTaskReminderProvidersOnTheTurnContext(t *testing.T) {
	colony, worker := newWorkerTaskReminderColony(t, "colony_worker_provider_context")
	createWorkerTasks(t, colony, worker, "carry the worker plan", "collect inputs", "write the report")

	tools := workerTaskToolDefs()
	loop, stop := colony.newWorkerAgentLoop(worker, agentloop.DefaultLoopConfig(), tools)
	defer stop()
	loop.Hub.Stop()

	ctx := worker.Context
	if ctx == nil {
		t.Fatal("worker has no AgentContext")
	}
	if ctx.SessionID == nil || *ctx.SessionID != worker.ID {
		t.Fatalf("worker session id = %v, want %q (the scope its task tools write)", ctx.SessionID, worker.ID)
	}
	if ctx.OpenTasksProvider == nil || ctx.SessionGoalProvider == nil {
		t.Fatal("worker AgentContext is missing OpenTasksProvider / SessionGoalProvider")
	}
	open := ctx.OpenTasksProvider()
	if len(open) != 2 || open[0].Subject != "collect inputs" || open[1].Subject != "write the report" {
		t.Fatalf("worker open tasks = %+v, want the two pending tasks from its own store", open)
	}
	if goal := ctx.SessionGoalProvider(); goal != "carry the worker plan" {
		t.Fatalf("worker session goal = %q, want the goal its task tools wrote", goal)
	}
}

// Parity: core/framework/agent_loop/idle_nudge.py::IdleNudgeSource.render
// (parked_open_tasks body plus the goal prefix)
func TestWorkerIdleNudgeParkedOpenTasksCarriesGoalAndNamesTasks(t *testing.T) {
	colony, worker := newWorkerTaskReminderColony(t, "colony_worker_idle_tasks")
	createWorkerTasks(t, colony, worker, "land the worker plan", "collect inputs", "write the report")

	tools := workerTaskToolDefs()
	cfg := agentloop.DefaultLoopConfig()
	loop, stop := colony.newWorkerAgentLoop(worker, cfg, tools)
	defer stop()
	loop.Hub.Stop()

	ctx := bindWorkerTurnReminders(t, worker, loop, tools)
	// A questionless park with no recorded reason is the branch that splits
	// on open work; a TURN_DONE park is a silent park and never reaches it.
	reason := agentloop.ParkReasonUnknown
	items := loop.Hub.CollectWithSignals(agentloop.IDLE_TICK, ctx, &agentloop.LoopSignals{
		IdleSeconds:   121,
		AwaitingInput: true,
		ParkReason:    &reason,
	}, nil, "")
	if len(items) != 1 || items[0].Source != "idle_nudge" {
		t.Fatalf("worker idle reminders = %+v, want one parked_open_tasks nudge", items)
	}
	body := items[0].Body
	for _, want := range []string{
		"Your goal: land the worker plan. ",
		`#1 "collect inputs"`,
		`#2 "write the report"`,
		"task list still has open work",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("idle nudge missing %q:\n%s", want, body)
		}
	}
	if got := items[0].Meta["substate"]; got != "parked_open_tasks" {
		t.Fatalf("nudge substate = %v, want parked_open_tasks", got)
	}
	if got := items[0].Meta["cap"]; got != cfg.SessionIdleNudgeMaxPerSession {
		t.Fatalf("nudge cap = %v, want the full cap %d for an UNKNOWN park", got, cfg.SessionIdleNudgeMaxPerSession)
	}
}

// Parity: core/framework/agent_loop/idle_nudge.py::IdleNudgeSource.render
// (TURN_DONE with no open tasks stays suppressed)
func TestWorkerIdleNudgeSuppressesTurnDoneWithoutOpenTasks(t *testing.T) {
	colony, worker := newWorkerTaskReminderColony(t, "colony_worker_idle_empty")

	tools := workerTaskToolDefs()
	loop, stop := colony.newWorkerAgentLoop(worker, agentloop.DefaultLoopConfig(), tools)
	defer stop()
	loop.Hub.Stop()

	ctx := bindWorkerTurnReminders(t, worker, loop, tools)
	reason := agentloop.ParkReasonTurnDone
	items := loop.Hub.CollectWithSignals(agentloop.IDLE_TICK, ctx, &agentloop.LoopSignals{
		IdleSeconds:   121,
		AwaitingInput: true,
		ParkReason:    &reason,
	}, nil, "")
	if len(items) != 0 {
		t.Fatalf("TURN_DONE with no open tasks produced %+v, want nothing", items)
	}
}

// Parity-exempt: Go-side real-worker evidence for the task reminder. Upstream
// has no worker-task-reminder test module; this drives a worker through the
// production SpawnBatch path with a scripted model that calls task_create, and
// reads the reminder out of the persisted worker conversation.
func TestWorkerTaskReminderReachesARealWorkerTurn(t *testing.T) {
	// Reminder warmup suppresses the first four turns, so the worker needs a
	// little room to reach the task_create that makes the snapshot due.
	call := func(id, name string, input map[string]any) llm.ToolCall {
		raw, _ := json.Marshal(input)
		return llm.ToolCall{ID: id, Name: name, Arguments: string(raw)}
	}
	provider := &trackerScriptLLM{calls: []llm.ToolCall{
		call("call_list_1", "task_list", map[string]any{}),
		call("call_list_2", "task_list", map[string]any{}),
		call("call_list_3", "task_list", map[string]any{}),
		call("call_list_4", "task_list", map[string]any{}),
		trackerToolCall("task_create", map[string]any{
			"goal":  "track the assignment",
			"tasks": []any{map[string]any{"subject": "collect data"}},
		}),
		trackerReportCall("collected"),
	}}
	runtime, _ := trackerTestColony(t, provider, "taskReminderWorkerColony")
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ids, err := runtime.SpawnBatchWithOptions(context.Background(),
		[]BatchTask{{Task: "track the assignment"}},
		SpawnBatchOptions{LoopConfigOverrides: map[string]any{"max_iterations": 10}},
	)
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	trackerWaitReports(t, runtime, ids)

	parts, err := runtime.workerConversationStore(ids[0]).ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	joined := ""
	for _, part := range parts {
		if content, _ := part["content"].(string); content != "" {
			joined += content + "\n"
		}
	}
	for _, want := range []string{"Goal: track the assignment", "#1. [pending] collect data"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("worker conversation missing %q:\n%s", want, joined)
		}
	}
}

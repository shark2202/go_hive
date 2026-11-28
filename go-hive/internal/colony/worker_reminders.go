package colony

// Worker-side reminder wiring.
//
// Workers run the same AgentLoop as the queen, but until now they were built
// as a bare &AgentLoop{} with a nil Hub: no task reminder, no idle nudge, no
// ticker. A worker is short-lived, yet its single ExecuteTurn can run for
// minutes (long tool calls, slow model responses), which is exactly the span
// the idle source is meant to cover. This file gives every worker loop the
// same hub shape the chat session uses and stops the ticker with the run.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.__init__ (the loop owns
// a ReminderHub; the session runtime registers sources on it)

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// workerReminderClock is the worker's activity clock: the idle source reads it
// to decide whether the worker has gone quiet. Every tool execution marks
// progress, so a worker actively using tools is never nudged.
type workerReminderClock struct {
	lastProgress atomic.Int64
}

func newWorkerReminderClock() *workerReminderClock {
	clock := &workerReminderClock{}
	clock.mark()
	return clock
}

func (c *workerReminderClock) mark() {
	c.lastProgress.Store(time.Now().UnixNano())
}

func (c *workerReminderClock) idleSeconds() float64 {
	last := c.lastProgress.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last)).Seconds()
}

// workerTaskReminderSource bridges tasks.TaskReminderSource (whose Render takes
// tasks.TaskReminderContext) onto agentloop.ReminderSource. It is the worker
// twin of the chat runtime's adapter; both wrap the one ported policy. The
// inner source is store-scoped so it reads the worker's own task list rather
// than the process-wide store.
//
// Parity: framework/tasks/reminders.py::TaskReminderSource
type workerTaskReminderSource struct {
	inner *tasks.TaskReminderSource
}

func (s *workerTaskReminderSource) Name() string { return s.inner.Name() }

func (s *workerTaskReminderSource) Points() []agentloop.ReminderPoint {
	return s.inner.Points()
}

func (s *workerTaskReminderSource) AppliesTo(agentCtx *agentloop.ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	return s.inner.AppliesTo(&tasks.TaskReminderAgentContext{
		SessionID:      agentCtx.SessionID,
		AvailableTools: agentCtx.AvailableTools,
	})
}

func (s *workerTaskReminderSource) ObserveTurn(toolNames []string) {
	s.inner.ObserveTurn(toolNames)
}

func (s *workerTaskReminderSource) Render(rctx agentloop.ReminderContext) (string, bool) {
	if rctx.AgentCtx == nil {
		return "", false
	}
	return s.inner.Render(tasks.TaskReminderContext{
		Point: rctx.Point,
		AgentCtx: &tasks.TaskReminderAgentContext{
			SessionID:      rctx.AgentCtx.SessionID,
			AvailableTools: rctx.AgentCtx.AvailableTools,
		},
		ToolNames: rctx.ToolNames,
	})
}

func reminderToolsFromDefs(defs []llm.ToolDef) []agentloop.Tool {
	out := make([]agentloop.Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, agentloop.Tool{Name: def.Name, Description: def.Description})
	}
	return out
}

// reminderColonyBinding adapts the colony's tracker binding to the reminder
// view: nil when the runtime has no tracker yet, so tracker-driven sources
// self-skip instead of rendering an empty snapshot.
//
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_resolve_binding
func (r *ColonyRuntime) reminderColonyBinding() *agentloop.ColonyBinding {
	if r == nil {
		return nil
	}
	binding := r.TrackerBinding()
	if binding == nil || strings.TrimSpace(binding.TrackerDB) == "" {
		return nil
	}
	return &agentloop.ColonyBinding{TrackerDB: binding.TrackerDB}
}

// activeWorkerReminders maps the colony's worker list onto the reminder view's
// ActiveWorker rows, keeping only workers still in flight.
func (r *ColonyRuntime) activeWorkerReminders() []agentloop.ActiveWorker {
	out := []agentloop.ActiveWorker{}
	if r == nil {
		return out
	}
	for _, worker := range r.ListWorkers() {
		if !worker.Status.IsActive() {
			continue
		}
		out = append(out, agentloop.ActiveWorker{
			ID:     worker.WorkerID,
			Status: worker.Status.String(),
			Task:   worker.Task,
		})
	}
	return out
}

// reminderColonyStats reports the colony's active/total worker counts for the
// fleet snapshot at tool-budget checkpoints.
func (r *ColonyRuntime) reminderColonyStats() agentloop.ColonyStats {
	if r == nil {
		return agentloop.ColonyStats{}
	}
	stats := r.GetStats()
	return agentloop.ColonyStats{
		Active: intStatValue(stats["active_workers"]),
		Total:  intStatValue(stats["total_workers"]),
	}
}

func intStatValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

// workerTaskStore resolves the task store a worker's own task tools write
// (newWorkerTaskToolSet): the worker's directory, with the worker id as the
// session id, so the list lands under <workerDir>/_misc/<workerID>.
func (r *ColonyRuntime) workerTaskStore(worker *Worker) *tasks.TaskStore {
	if r == nil || worker == nil || worker.ID == "" {
		return nil
	}
	workerDir := worker.StoragePath
	if workerDir == "" {
		workerDir = r.workerStorage(worker.ID)
	}
	return tasks.NewTaskStore(workerDir)
}

// idleTasksFromStore maps a task store's records onto the idle nudge's task
// view. Read failures degrade to an empty list (upstream _open_tasks is
// best-effort); completed/archived filtering stays in the source.
//
// Parity: framework/agent_loop/idle_nudge.py::_open_tasks
func idleTasksFromStore(store *tasks.TaskStore, sessionID string) []agentloop.IdleTask {
	if store == nil || sessionID == "" {
		return nil
	}
	records := store.ListTasks(sessionID)
	out := make([]agentloop.IdleTask, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		out = append(out, agentloop.IdleTask{
			ID:      strconv.Itoa(record.ID),
			Subject: record.Subject,
			Status:  string(record.Status),
		})
	}
	return out
}

// sessionGoalFromStore reads the task list's anchor goal for the idle nudge's
// goal prefix. A missing list/goal degrades to "" (no prefix).
//
// Parity: framework/agent_loop/idle_nudge.py::_session_goal
func sessionGoalFromStore(store *tasks.TaskStore, sessionID string) string {
	if store == nil || sessionID == "" {
		return ""
	}
	meta := store.GetMeta(sessionID)
	if meta == nil || meta.Goal == nil {
		return ""
	}
	return *meta.Goal
}

// attachWorkerTaskReminderProviders installs the worker's task providers on
// its AgentContext. AgentLoop.ExecuteTurn re-binds the hub from that context
// (bindReminders), so providers set only on a pre-built ReminderAgentContext
// would be replaced before the first render.
func attachWorkerTaskReminderProviders(ctx *agentloop.AgentContext, store *tasks.TaskStore, workerID string) {
	if ctx == nil {
		return
	}
	// The idle source's _open_tasks/_session_goal guards skip an empty
	// session id. Upstream's worker agent context carries its session id;
	// here it is the same worker id the task tools write under.
	if ctx.SessionID == nil && workerID != "" {
		sessionID := workerID
		ctx.SessionID = &sessionID
	}
	ctx.OpenTasksProvider = func() []agentloop.IdleTask {
		return idleTasksFromStore(store, workerID)
	}
	ctx.SessionGoalProvider = func() string {
		return sessionGoalFromStore(store, workerID)
	}
}

// workerReminderContext is the worker's reminder view. Its task providers read
// the same store the worker's task tools write, so the idle nudge's
// parked_open_tasks variant sees open work and the goal prefix resolves.
func (r *ColonyRuntime) workerReminderContext(worker *Worker, tools []llm.ToolDef) *agentloop.ReminderAgentContext {
	reminderCtx := &agentloop.ReminderAgentContext{
		SessionID:      fmt.Sprintf("%s/%s", r.streamID, worker.ID),
		ColonyID:       r.streamID,
		AvailableTools: reminderToolsFromDefs(tools),
	}
	// The worker tracker nudge resolves the colony's tracker DB through the
	// same binding the tracker tools write to. A runtime without a provisioned
	// tracker returns nil, so the source self-skips.
	reminderCtx.ColonyBindingProvider = r.reminderColonyBinding
	store := r.workerTaskStore(worker)
	workerID := worker.ID
	reminderCtx.OpenTasksProvider = func() []agentloop.IdleTask {
		return idleTasksFromStore(store, workerID)
	}
	reminderCtx.SessionGoalProvider = func() string {
		return sessionGoalFromStore(store, workerID)
	}
	return reminderCtx
}

// newWorkerAgentLoop builds a worker loop whose Hub carries the task reminder
// and the idle nudge. The idle ticker is started here and stopped by the
// returned cleanup; a parked reminder is drained back into the loop's input
// queue as a <system-reminder> block so it is not lost when the worker's
// ExecuteTurn is still in flight.
func (r *ColonyRuntime) newWorkerAgentLoop(worker *Worker, cfg agentloop.LoopConfig, tools []llm.ToolDef) (*agentloop.AgentLoop, func()) {
	clock := newWorkerReminderClock()
	baseExec := r.executorFor(worker)
	executor := func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		clock.mark()
		return baseExec(req)
	}
	loop := agentloop.NewAgentLoop(cfg, executor)
	// A resumed worker rebuilds its loop over the saved conversation; the
	// cursor also carries the cumulative tool-call count and the doom
	// fingerprint window, so the restarted run continues the same budget and
	// repetition history instead of refilling both.
	//
	// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
	// Parity: framework/agent_loop/agent_loop.py::execute (restored counters)
	if worker.conversation != nil {
		_ = loop.RestoreCursorState(worker.conversation)
	}
	// P0-14: a worker's cross-turn replay publishes tool_call_replay_detected
	// on the colony bus, the same bus its doom-loop sink and turn emitter use.
	//
	// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected
	if r.bus != nil {
		loop.ReplayEvents = r.bus
	}
	r.attachToolLiveness(loop)
	// Install the transport-level stream watchdog on the worker's LLM seam.
	// A provider without streaming support is left untouched.
	streamingLLM := r.wireStreamingLLM(worker, cfg)
	if loop.Hub == nil {
		return loop, func() {}
	}

	hub := loop.Hub
	// The worker's task reminder reads and persists against the worker's own
	// task store + session id, the exact scope its task tools write. The
	// providers also go on the AgentContext, which the loop re-binds from at
	// the start of the first turn.
	taskStore := r.workerTaskStore(worker)
	attachWorkerTaskReminderProviders(worker.Context, taskStore, worker.ID)
	hub.Register(&workerTaskReminderSource{inner: tasks.NewTaskReminderSourceFor(taskStore, worker.ID)})
	// Worker-role tracker cadence (upstream WorkerTrackerSnapshotReminderSource:
	// workers fill the queen's tables, so the nudge is cadence-driven and
	// self-skips when the tracker has no rows).
	hub.Register(agentloop.NewWorkerTrackerSnapshotReminderSource())
	// The stream-stall source is registered on every loop upstream. The
	// watchdog itself lives in the completer adapter installed by
	// wireStreamingLLM, so a real trip reaches this source's continue-nudge.
	hub.Register(agentloop.NewStreamStallSource(cfg.ContinueNudgeMaxPerTurn, cfg.ContinueNudgeEnabled))
	idle := agentloop.NewIdleNudgeSource(agentloop.IdleNudgeConfig{
		BudgetSeconds:         cfg.SessionIdleNudgeSeconds,
		MaxNudges:             cfg.SessionIdleNudgeMaxPerSession,
		AwaitingBudgetSeconds: cfg.SessionIdleNudgeAwaitingSeconds,
		BrokenBudgetSeconds:   cfg.SessionIdleNudgeBrokenSeconds,
	})
	hub.Register(idle)

	reminderCtx := r.workerReminderContext(worker, tools)
	hub.Bind(reminderCtx)
	hub.Start(reminderCtx, func() *agentloop.LoopSignals {
		// Real stream state: the idle-nudge slow-TTFT branch and the
		// watchdog-owned inter-event branch both read these fields, so a
		// worker streaming right now must not look idle between turns.
		return loopSignalsSnapshot(agentloop.LoopSignals{
			IdleSeconds:    clock.idleSeconds(),
			FirstEventSeen: true,
		}, streamingLLM)
	}, func() {
		items := hub.TakePending()
		if len(items) == 0 {
			return
		}
		bodies := make([]string, 0, len(items))
		for _, item := range items {
			if body := strings.TrimSpace(item.Body); body != "" {
				bodies = append(bodies, body)
			}
		}
		block := agentloop.WrapReminder(bodies)
		if block != "" {
			loop.InjectEvent(block, false, nil)
		}
	})
	return loop, func() { hub.Stop() }
}

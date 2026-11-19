package server

// Reminder-hub wiring for the chat session runtime.
//
// The loop owns the ReminderHub; this file registers the sources the queen
// session needs (task snapshots/drift, the colony parallel nudge, active
// workers, the deferred-tool manifest, the skills catalog, and the temporal
// idle nudge), fills the ReminderAgentContext providers from the session's own
// stores, and starts the hub's ticker with a lifecycle bound to ChatRuntime.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.__init__
// Parity: framework/server/queen_orchestrator.py (queen agent context wiring)

import (
	"strconv"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tasks"
)

// configureReminderProviders fills the reminder view straight from the live
// session stores. Every provider returns empty when its store has nothing so
// the matching source self-skips (no tasks file, no colony, no worker).
func (r *ChatRuntime) configureReminderProviders() {
	if r == nil || r.agentCtx == nil {
		return
	}
	if r.sessionID != "" {
		sessionID := r.sessionID
		r.agentCtx.SessionID = &sessionID
	}
	r.agentCtx.OpenTasksProvider = r.openIdleTasks
	r.agentCtx.SessionGoalProvider = r.sessionGoal
	if r.deps.SkillsCatalog != nil {
		r.agentCtx.QueenSkillsCatalogProvider = r.deps.SkillsCatalog
	} else {
		// Explicit empty provider: the catalog source stays registered but
		// self-skips until a real catalog is supplied.
		r.agentCtx.QueenSkillsCatalogProvider = func() string { return "" }
	}
	// The colony-phase gate reads the tools the current turn advertises:
	// run_worker is filtered out while no colony is bound, so the parallel
	// nudge cannot fire for a plain chat session.
	r.agentCtx.DynamicToolsProvider = func() []llm.ToolDef { return r.toolsForTurn() }
	r.agentCtx.ActiveWorkersProvider = r.activeWorkers
	r.agentCtx.ColonyStatsProvider = r.colonyStats
	r.setColonyBinding()
}

// configureReminders registers the per-session sources on the loop's hub and
// starts its ticker. The ticker is stopped by ChatRuntime.Close.
func (r *ChatRuntime) configureReminders(override *agentloop.IdleNudgeConfig) {
	if r == nil || r.loop == nil || r.loop.Hub == nil {
		return
	}
	cfg := agentloop.IdleNudgeConfig{
		BudgetSeconds:         r.loop.Config.SessionIdleNudgeSeconds,
		MaxNudges:             r.loop.Config.SessionIdleNudgeMaxPerSession,
		AwaitingBudgetSeconds: r.loop.Config.SessionIdleNudgeAwaitingSeconds,
		BrokenBudgetSeconds:   r.loop.Config.SessionIdleNudgeBrokenSeconds,
	}
	if override != nil {
		cfg = *override
	}
	r.idleSource = agentloop.NewIdleNudgeSource(cfg)

	hub := r.loop.Hub
	hub.Register(&taskReminderSourceAdapter{inner: tasks.NewTaskReminderSource()})
	hub.Register(agentloop.NewColonyParallelNudgeSource())
	hub.Register(agentloop.NewActiveWorkersReminderSource())
	hub.Register(agentloop.NewSearchableToolsReminderSource())
	hub.Register(agentloop.NewSkillsCatalogReminderSource())
	hub.Register(r.idleSource)

	r.hub = hub
	ctx := agentloop.NewReminderAgentContext(r.agentCtx)
	hub.Bind(ctx)
	hub.Start(ctx, r.reminderSignals, r.wakeForReminder)
}

// setColonyBinding resolves the live colony's id for the reminder context.
// Called at construction, before any colony lock is held.
func (r *ChatRuntime) setColonyBinding() {
	runtime := r.boundColonyRuntime()
	if runtime == nil {
		r.agentCtx.ColonyID = nil
		return
	}
	colonyID := runtime.StreamID()
	if colonyID == "" {
		r.agentCtx.ColonyID = nil
		return
	}
	r.agentCtx.ColonyID = &colonyID
}

// fireReminderPoint consults the hub at a lifecycle point and places the
// wrapped block into the conversation. The hub decides whether anything is
// due; the runtime only delivers it.
//
// Parity: framework/agent_loop/agent_loop.py::_fire_reminder
func (r *ChatRuntime) fireReminderPoint(point agentloop.ReminderPoint, userText string, toolNames []string) string {
	if r == nil || r.hub == nil || r.conv == nil {
		return ""
	}
	block := r.hub.Fire(point, agentloop.NewReminderAgentContext(r.agentCtx), toolNames, userText)
	if block == "" {
		return ""
	}
	_, _ = r.conv.AddUserMessage(block, agentloop.UserMessageOptions{IsSystemReminder: true})
	return block
}

// reminderSignals is the snapshot the hub ticker hands to temporal sources.
//
// Parity: framework/agent_loop/agent_loop.py::_loop_signals
func (r *ChatRuntime) reminderSignals() *agentloop.LoopSignals {
	if r == nil {
		return &agentloop.LoopSignals{}
	}
	idle := 0.0
	if last := r.lastProgress.Load(); last > 0 {
		idle = time.Since(time.Unix(0, last)).Seconds()
	}
	awaiting := r.awaiting.Load()
	r.parkMu.Lock()
	reason := r.parkReason
	r.parkMu.Unlock()
	var parkReason *agentloop.ParkReason
	if awaiting {
		if reason == "" {
			reason = agentloop.ParkReasonUnknown
		}
		parked := reason
		parkReason = &parked
	}
	return &agentloop.LoopSignals{
		IdleSeconds:    idle,
		AwaitingInput:  awaiting,
		ParkReason:     parkReason,
		StreamActive:   r.turnActive.Load(),
		FirstEventSeen: r.firstEvent.Load(),
	}
}

// wakeForReminder wakes the worker after the ticker parks a reminder. The run
// loop turns that wake into a message-less reminder turn.
func (r *ChatRuntime) wakeForReminder() {
	if r == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *ChatRuntime) markProgress() {
	if r == nil {
		return
	}
	r.lastProgress.Store(time.Now().UnixNano())
}

func (r *ChatRuntime) markTurnStart() {
	if r == nil {
		return
	}
	r.markProgress()
	r.awaiting.Store(false)
	r.turnActive.Store(true)
	// The Go transport is non-streaming, so the first event is the completed
	// response; marking it now keeps the slow-TTFT branch from nudging every
	// long model call.
	r.firstEvent.Store(true)
}

func (r *ChatRuntime) markTurnEnd() {
	if r == nil {
		return
	}
	r.turnActive.Store(false)
	r.firstEvent.Store(false)
	r.markProgress()
}

func (r *ChatRuntime) setParkState(reason agentloop.ParkReason, awaiting bool) {
	if r == nil {
		return
	}
	r.parkMu.Lock()
	r.parkReason = reason
	r.parkMu.Unlock()
	r.awaiting.Store(awaiting)
}

// openIdleTasks is the idle nudge's view of the session task list. Archived
// and completed records are dropped by the source itself; a missing list is
// "no data", not an error.
func (r *ChatRuntime) openIdleTasks() []agentloop.IdleTask {
	if r == nil || r.sessionID == "" {
		return nil
	}
	records, ok := tasks.ReadRecords(r.sessionID)
	if !ok {
		return nil
	}
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

func (r *ChatRuntime) sessionGoal() string {
	if r == nil || r.sessionID == "" {
		return ""
	}
	return tasks.ReadGoal(r.sessionID)
}

// activeWorkers lists the colony workers still in flight. No colony (or no
// active worker) returns nil so the active-workers reminder stays silent.
func (r *ChatRuntime) activeWorkers() []agentloop.ActiveWorker {
	runtime := r.boundColonyRuntime()
	if runtime == nil {
		return nil
	}
	workers := runtime.ListWorkers()
	out := make([]agentloop.ActiveWorker, 0, len(workers))
	for _, worker := range workers {
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

func (r *ChatRuntime) colonyStats() agentloop.ColonyStats {
	runtime := r.boundColonyRuntime()
	if runtime == nil {
		return agentloop.ColonyStats{}
	}
	stats := runtime.GetStats()
	return agentloop.ColonyStats{
		Active: intStat(stats["active_workers"]),
		Total:  intStat(stats["total_workers"]),
	}
}

func intStat(value any) int {
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

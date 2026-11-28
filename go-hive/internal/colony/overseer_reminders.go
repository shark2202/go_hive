package colony

// Reminder wiring for the colony's persistent overseer.
//
// The overseer is the colony's lead (a persistent Worker wrapping the queen's
// AgentLoop — upstream ColonyRuntime.start_overseer builds a full AgentLoop, so
// its hub carries the shipped sources and each source's applies_to decides
// whether it fires). The Go port built the overseer loop bare, with no hub at
// all; this file gives it the queen-role sources whose providers the colony
// runtime can honestly supply:
//
//   - colony_parallel     POST_TOOL_USE   (queen only; needs run_worker in the tool list)
//   - active_workers      USER_PROMPT_SUBMIT (queen only)
//   - tracker_snapshot    POST_TOOL_USE   (queen only; needs the colony tracker binding)
//   - colony_worker_snapshot TOOL_BUDGET_CHECKPOINT (queen only; fleet stats)
//   - tasks               five points     (the overseer's own plan via overseerTaskSet)
//   - stream_stall        STREAM_STALLED  (all loops upstream)
//
// Deliberately NOT registered here, with reasons:
//   - idle_nudge: the chat runtime that fronts a colony session already owns
//     the session-level idle ticker and relays its wake to the overseer; a
//     second ticker would double-nudge the same session.
//   - skills_catalog: the chat runtime relays the queen-scoped catalog with the
//     first client message (relaySkillsCatalogBlock), which is how the overseer
//     receives it today.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (the overseer wraps a full AgentLoop, whose hub registers every source).

import (
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// overseerTaskReminderScope is the (store, session id) pair the overseer's
// task tools write. It rides the same overseerTaskSet the tool surface binds,
// so the reminder reads exactly the plan the overseer maintains.
func (r *ColonyRuntime) overseerTaskReminderScope() (*tasks.TaskStore, string) {
	if r == nil {
		return nil, overseerTaskSessionID
	}
	if r.overseerTasks != nil {
		return r.overseerTasks.store, r.overseerTasks.sessionID
	}
	return tasks.NewTaskStore(r.storagePath), overseerTaskSessionID
}

// overseerReminderContext is the queen-shaped reminder view the overseer hub
// binds. Its task source is scoped to the overseer's own plan.
func (r *ColonyRuntime) overseerReminderContext(worker *Worker, tools []llm.ToolDef) *agentloop.ReminderAgentContext {
	return &agentloop.ReminderAgentContext{
		IsQueenStream:              true,
		SessionID:                  fmt.Sprintf("%s/%s", r.streamID, worker.ID),
		ColonyID:                   r.streamID,
		AvailableTools:             reminderToolsFromDefs(tools),
		ColonyBindingProvider:      r.reminderColonyBinding,
		ActiveWorkersProvider:      r.activeWorkerReminders,
		ColonyStatsProvider:        r.reminderColonyStats,
		QueenSkillsCatalogProvider: nil,
	}
}

// startOverseerReminders registers the overseer's sources, binds them to a
// queen-shaped reminder context and returns the teardown. The overseer has no
// temporal source registered (see the file header), so this starts no ticker;
// the returned teardown still clears any parked reminder so a stopped colony
// leaks nothing.
func (r *ColonyRuntime) startOverseerReminders(loop *agentloop.AgentLoop, worker *Worker, cfg agentloop.LoopConfig, tools []llm.ToolDef) func() {
	if loop == nil || loop.Hub == nil {
		return func() {}
	}
	hub := loop.Hub
	// Install the transport-level stream watchdog on the overseer's LLM seam,
	// exactly like a worker: a stalled overseer stream trips STREAM_STALLED and
	// the loop's continue-nudge re-streams the same inner turn. The adapter
	// also records live StreamActive / FirstEventSeen state for Signals().
	// (No session-level idle ticker is started here — see the file header — so
	// the chat runtime that fronts the colony owns that snapshot cadence.)
	r.wireStreamingLLM(worker, cfg)
	// AgentLoop.ExecuteTurn re-binds the hub from the worker's AgentContext,
	// so the overseer's tool list has to be visible there for applies_to (the
	// task source needs its task_write tools).
	if worker.Context != nil {
		worker.Context.AvailableTools = append([]llm.ToolDef(nil), tools...)
	}
	hub.Register(agentloop.NewColonyParallelNudgeSource())
	hub.Register(agentloop.NewActiveWorkersReminderSource())
	hub.Register(agentloop.NewTrackerSnapshotReminderSource())
	hub.Register(agentloop.NewColonyWorkerSnapshotReminderSource())
	// The overseer's task tools write its colony-rooted plan
	// (overseerTaskSet); the reminder source reads that same store/session.
	store, sessionID := r.overseerTaskReminderScope()
	hub.Register(&workerTaskReminderSource{inner: tasks.NewTaskReminderSourceFor(store, sessionID)})
	hub.Register(agentloop.NewStreamStallSource(cfg.ContinueNudgeMaxPerTurn, cfg.ContinueNudgeEnabled))

	hub.Bind(r.overseerReminderContext(worker, tools))
	return func() { hub.Stop() }
}

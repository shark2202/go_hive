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
//   - stream_stall        STREAM_STALLED  (all loops upstream)
//
// Deliberately NOT registered here, with reasons:
//   - idle_nudge: the chat runtime that fronts a colony session already owns
//     the session-level idle ticker and relays its wake to the overseer; a
//     second ticker would double-nudge the same session.
//   - skills_catalog: the chat runtime relays the queen-scoped catalog with the
//     first client message (relaySkillsCatalogBlock), which is how the overseer
//     receives it today.
//   - tasks: the task reminder reads the process-wide task store, while the
//     overseer's own task list lives under the colony's storage path
//     (overseerTaskSet); registering it would be decorative. Tracked as a
//     known gap in go_hive/docs/reminders-coverage.md.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (the overseer wraps a full AgentLoop, whose hub registers every source).

import (
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

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
	hub.Register(agentloop.NewColonyParallelNudgeSource())
	hub.Register(agentloop.NewActiveWorkersReminderSource())
	hub.Register(agentloop.NewTrackerSnapshotReminderSource())
	hub.Register(agentloop.NewColonyWorkerSnapshotReminderSource())
	hub.Register(agentloop.NewStreamStallSource(cfg.ContinueNudgeMaxPerTurn, cfg.ContinueNudgeEnabled))

	reminderCtx := &agentloop.ReminderAgentContext{
		IsQueenStream:              true,
		SessionID:                  fmt.Sprintf("%s/%s", r.streamID, worker.ID),
		ColonyID:                   r.streamID,
		AvailableTools:             reminderToolsFromDefs(tools),
		ColonyBindingProvider:      r.reminderColonyBinding,
		ActiveWorkersProvider:      r.activeWorkerReminders,
		ColonyStatsProvider:        r.reminderColonyStats,
		QueenSkillsCatalogProvider: nil,
	}
	hub.Bind(reminderCtx)
	return func() { hub.Stop() }
}

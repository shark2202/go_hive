package server

// Reminder-hub wiring for the chat session runtime.
//
// The loop owns the ReminderHub; this file registers the sources the queen
// session needs (task snapshots/drift, the colony parallel nudge, active
// workers, the tracker snapshot, the deferred-tool manifest, the skills
// catalog, and the temporal idle nudge), fills the ReminderAgentContext
// providers from the session's own stores, and starts the hub's ticker with a
// lifecycle bound to ChatRuntime.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.__init__
// Parity: framework/server/queen_orchestrator.py (queen agent context wiring)

import (
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
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
	r.agentCtx.ColonyBindingProvider = r.colonyBinding
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
	//
	// The tier always carries the flat lazy split when the catalogue supplies
	// one (independent phase: 24 always-on + searchable remainder); a colony
	// binding makes the colony-phase surface eager again, matching upstream's
	// phase lists. Without those deps it keeps the legacy MCP-only split.
	if tier := newChatToolTier(r.deps.Root, r.toolsForTurn, r.deps.AlwaysOnToolNames, r.deps.DeferredTools, chatManifestFilter{
		Names:    r.deps.ManifestToolNames,
		Filtered: r.deps.ManifestFiltered,
	}, func() bool {
		return r.boundColonyRuntime() != nil
	}); tier != nil {
		r.toolTier = tier
		// MCP tools are advertised by name only until search_tools loads
		// them; this is what gives the searchable-tools reminder content.
		r.agentCtx.ToolTierState = tier.statePtr()
		r.agentCtx.SearchableToolsProvider = tier.searchableTools
		r.agentCtx.LoadedToolNamesProvider = tier.loadedToolNames
		r.agentCtx.DynamicToolsProvider = tier.toolDefs
	} else {
		// No MCP loader means no deferred schemas today; still expose the
		// provider so the chat context has the same shape as a tiered queen.
		r.agentCtx.SearchableToolsProvider = func() []agentloop.Tool { return nil }
		r.agentCtx.LoadedToolNamesProvider = func() []string { return nil }
		r.agentCtx.DynamicToolsProvider = func() []llm.ToolDef { return r.toolsForTurn() }
	}
	r.agentCtx.ActiveWorkersProvider = r.activeWorkers
	r.agentCtx.ColonyStatsProvider = r.colonyStats
	// Memory notes push (framework/agent_loop/memory_reminders): "wait" picks
	// through the same scoped recall the background mode uses, "index" lists
	// the two note scopes, and the settings provider is read at each message
	// so a change in configuration.json or HIVE_MEMORY_RECALL reaches running
	// sessions from their next message.
	//
	// Parity: core/framework/server/queen_orchestrator.py::_recall_for_message
	// Parity: core/framework/server/queen_orchestrator.py::_memory_index
	// Parity: core/framework/config.py::get_memory_recall_config
	r.agentCtx.MemoryRecallProvider = r.memoryRecallForMessage
	r.agentCtx.MemoryIndexProvider = r.memoryIndexScopes
	r.agentCtx.MemoryRecallSettingsProvider = r.memoryRecallSettings
	// Turn context (framework/server/turn_context.py + queen_orchestrator.py):
	// a queen stream gets the started packages' aggregator; a worker stream has
	// no turn context. No installed+enabled package leaves the provider nil and
	// the reminder source self-skips.
	if r.agentCtx.IsQueenStream() {
		if runtime := r.turnContextRuntime(); runtime != nil && !runtime.Empty() {
			queenName := r.queenName
			r.agentCtx.TurnContextProvider = runtime.ForSession(func() string { return queenName })
		}
	}
	r.setColonyBinding()
}

// memoryRecallSettings is the memory_recall section read at each message
// (HIVE_MEMORY_RECALL over the configured mode).
//
// Parity: core/framework/config.py::get_memory_recall_config
func (r *ChatRuntime) memoryRecallSettings() agentloop.MemoryRecallSettings {
	config := memory.GetMemoryRecallConfig(r.deps.HomeRoot)
	return agentloop.MemoryRecallSettings{Mode: config.Mode, WaitSeconds: config.WaitSeconds}
}

// memoryIndexScopes lists her notes, shared and her own ("index" mode).
//
// Parity: core/framework/server/queen_orchestrator.py::_memory_index
func (r *ChatRuntime) memoryIndexScopes() []agentloop.MemoryNoteScope {
	listing := memory.MemoryIndex([]memory.MemoryIndexScope{
		{Label: "Shared with every queen", Dir: r.globalMemoryDir},
		{Label: "Yours alone", Dir: r.queenMemoryDir},
	})
	out := make([]agentloop.MemoryNoteScope, 0, len(listing))
	for _, scope := range listing {
		entries := make([]agentloop.MemoryNoteEntry, 0, len(scope.Entries))
		for _, entry := range scope.Entries {
			entries = append(entries, agentloop.MemoryNoteEntry{Name: entry.Name, Line: entry.Line})
		}
		out = append(out, agentloop.MemoryNoteScope{Label: scope.Label, Folder: scope.Folder, Entries: entries})
	}
	return out
}

// memoryRecallForMessage is the model's pick for a message ("wait" mode): the
// same scoped recall blocks background mode injects, rendered by the source.
// Upstream _recall_for_message refreshes the phase_state cache in place, so the
// pick is kept for a later background-mode delivery too.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_for_message
func (r *ChatRuntime) memoryRecallForMessage(text string) string {
	if r == nil || r.globalMemoryDir == "" || text == "" {
		return ""
	}
	globalBlock, queenBlock := r.recallBlocksForQuery(text)
	r.storeRecallBlocks(globalBlock, queenBlock)
	return joinRecallBlocks(globalBlock, queenBlock)
}

// recallBlocksForQuery picks memories for a query, one pair as upstream's
// build_scoped_recall_blocks returns them: global block, then queen block.
//
// Parity: framework/agents/queen/recall_selector.py::build_scoped_recall_blocks
func (r *ChatRuntime) recallBlocksForQuery(query string) (string, string) {
	return memory.BuildScopedRecallBlocks(
		r.ctx,
		query,
		r.deps.LLM,
		r.globalMemoryDir,
		r.queenMemoryDir,
		r.queenName,
		3,
		3,
		llm.AuxMaxTokens(),
	)
}

// turnContextRuntime is the hive home's started turn-context packages. A serve
// process starts them at boot (startServeBackgroundServices); a one-shot CLI
// chat starts them here, at the first session that can answer a message.
func (r *ChatRuntime) turnContextRuntime() *TurnContextRuntime {
	if r == nil || strings.TrimSpace(r.deps.HomeRoot) == "" {
		return nil
	}
	home, err := hivehome.ResolveAt(r.deps.HomeRoot)
	if err != nil {
		return nil
	}
	return StartTurnContexts(home)
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
	hub.Register(agentloop.NewTrackerSnapshotReminderSource())
	// Queen fleet snapshot at tool-budget checkpoints: the queen learning what
	// the rest of the colony is doing while she grinds through her own tools.
	hub.Register(agentloop.NewColonyWorkerSnapshotReminderSource())
	hub.Register(agentloop.NewSearchableToolsReminderSource())
	hub.Register(agentloop.NewSkillsCatalogReminderSource())
	// Installed turn-context packages put their text ahead of each user turn;
	// the source is silent until a session runtime wires a provider.
	hub.Register(agentloop.NewTurnContextReminderSource())
	// Memory notes: "index" lists every note's manifest and "wait" shows the
	// model's pick before she answers (framework.agent_loop.memory_reminders);
	// each source acts only while its mode is set, read at each message.
	hub.Register(agentloop.NewMemoryIndexReminderSource())
	hub.Register(agentloop.NewMemoryRecallReminderSource())
	hub.Register(r.idleSource)
	// Stream-stall recovery: the streaming completer's watchdog reports a trip
	// as a typed error, and the loop consults this source at STREAM_STALLED
	// before re-streaming the same turn.
	hub.Register(agentloop.NewStreamStallSource(r.loop.Config.ContinueNudgeMaxPerTurn, r.loop.Config.ContinueNudgeEnabled))

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
	r.persistParkState()
}

// colonyBinding resolves the tracker binding of the session's bound colony for
// the tracker-snapshot reminder. No colony (or a colony whose tracker has not
// been provisioned yet) returns nil, so the source self-skips.
//
// Parity: framework/agent_loop/tracker_snapshot_reminder.py::_resolve_binding
func (r *ChatRuntime) colonyBinding() *agentloop.ColonyBinding {
	if r == nil {
		return nil
	}
	runtime := r.boundColonyRuntime()
	if runtime == nil {
		return nil
	}
	binding := runtime.TrackerBinding()
	if binding == nil || binding.TrackerDB == "" {
		return nil
	}
	return &agentloop.ColonyBinding{TrackerDB: binding.TrackerDB}
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

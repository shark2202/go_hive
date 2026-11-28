package agentloop

// Parity: framework/agent_loop/reminders.py ReminderPoint.
type ReminderPoint string

const (
	SESSION_START          ReminderPoint = "session_start"
	USER_PROMPT_SUBMIT     ReminderPoint = "user_prompt_submit"
	POST_TOOL_USE          ReminderPoint = "post_tool_use"
	TOOL_BUDGET_CHECKPOINT ReminderPoint = "tool_budget_checkpoint"
	PRE_COMPACT            ReminderPoint = "pre_compact"
	POST_COMPACT           ReminderPoint = "post_compact"
	STOP                   ReminderPoint = "stop"
	IDLE_TICK              ReminderPoint = "idle_tick"
	STREAM_STALLED         ReminderPoint = "stream_stalled"
)

type Tool struct {
	Name        string
	Description string
}

type ReminderAgentContext struct {
	IsQueenStream              bool
	SearchableToolsProvider    func() []Tool
	LoadedToolNamesProvider    func() []string
	QueenSkillsCatalogProvider func() string
	AvailableTools             []Tool
	DynamicToolsProvider       func() []Tool
	SessionID                  string
	ColonyID                   string
	OpenTasksProvider          func() []IdleTask
	SessionGoalProvider        func() string
	SentinelAutopilotProvider  func() bool
	// ColonyBindingProvider resolves the session's live colony tracker
	// binding, or nil when no colony is attached. The tracker-snapshot
	// source self-skips on nil; a late colony attach is re-bound through
	// ReminderHub.Bind.
	ColonyBindingProvider func() *ColonyBinding
	// ActiveWorkersProvider lists the colony workers still in flight.
	// Nil (or an empty list) means the active-workers reminder self-skips.
	ActiveWorkersProvider func() []ActiveWorker
	// ColonyStatsProvider reports the colony's active/total worker counts for
	// snapshots. Nil reports an empty colony.
	ColonyStatsProvider func() ColonyStats
	// TurnContextProvider is the queen's turn-context hook
	// (framework/server/turn_context.py): it takes the user's message and
	// returns the text to place ahead of it, or "" for nothing. Nil means
	// no package is installed+enabled, and the turn-context reminder
	// source self-skips.
	TurnContextProvider func(string) string
	// PastConversationRecallProvider returns the dated excerpts of earlier
	// conversations related to a user message, or "" when nothing clears
	// the relevance bar. Nil means the past-conversations reminder source
	// self-skips.
	//
	// Parity: framework/agent_loop/types.py AgentContext.past_conversation_recall_provider
	PastConversationRecallProvider func(string) string
	// MemoryRecallProvider picks the memory notes for a user message
	// ("wait" mode, framework.agent_loop.memory_reminders). Nil means the
	// wait-mode source self-skips.
	MemoryRecallProvider func(string) string
	// MemoryIndexProvider lists every memory note's manifest line
	// ("index" mode). Nil means the index-mode source self-skips.
	MemoryIndexProvider func() []MemoryNoteScope
	// MemoryRecallSettingsProvider reads memory_recall at each message, so
	// a change in Settings reaches running sessions. Nil keeps the
	// defaults ("index", 10s).
	MemoryRecallSettingsProvider func() MemoryRecallSettings
	// ChatAppProvider reports the chat app this conversation comes through
	// ("" / nil on the dashboard), the value the tasks reminder source's
	// chat-app gate reads. Upstream's ReminderContext.agent_ctx exposes
	// ``chat_app_provider=lambda: session.channel`` to the same source.
	//
	// Parity: framework/agent_loop/types.py AgentContext.chat_app_provider,
	// framework/server/queen_orchestrator.py (chat_app_provider)
	ChatAppProvider func() any
}

// ActiveWorker is one in-flight colony worker as the reminder sources read it.
//
// Parity: framework/agent_loop/active_workers_reminder.py::_list_active_workers
type ActiveWorker struct {
	ID     string
	Status string
	Task   string
}

// ColonyStats is the worker count snapshot the colony reminder sources read.
//
// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py
type ColonyStats struct {
	Active int
	Total  int
}

// Parity: framework/tasks/models.py TaskRecord (only the idle-nudge fields).
type IdleTask struct {
	ID      string
	Subject string
	Status  string
}

type ReminderContext struct {
	Point     ReminderPoint
	AgentCtx  *ReminderAgentContext
	ToolNames []string
	UserText  string
	// Parity: ReminderContext.signals.
	Signals *LoopSignals
}

type ReminderSource interface {
	Name() string
	Points() []ReminderPoint
	AppliesTo(agentCtx *ReminderAgentContext) bool
	Render(rctx ReminderContext) (string, bool)
}

func ToolsProvider(tools ...Tool) func() []Tool {
	fixed := append([]Tool(nil), tools...)
	return func() []Tool { return fixed }
}

func ToolNamesProvider(names ...string) func() []string {
	fixed := append([]string(nil), names...)
	return func() []string { return fixed }
}

func CatalogProvider(catalog string) func() string {
	return func() string { return catalog }
}

func hasReminderPoint(points []ReminderPoint, point ReminderPoint) bool {
	for _, p := range points {
		if p == point {
			return true
		}
	}
	return false
}

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
	// ActiveWorkersProvider lists the colony workers still in flight.
	// Nil (or an empty list) means the active-workers reminder self-skips.
	ActiveWorkersProvider func() []ActiveWorker
	// ColonyStatsProvider reports the colony's active/total worker counts for
	// snapshots. Nil reports an empty colony.
	ColonyStatsProvider func() ColonyStats
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

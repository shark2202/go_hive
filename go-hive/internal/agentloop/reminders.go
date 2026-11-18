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

package agentloop

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: framework/agent_loop/internals/types.py::TriggerEvent
type TriggerEvent struct {
	TriggerType string
	SourceID    string
	Payload     map[string]any
	Timestamp   float64
}

func NewTriggerEvent(triggerType, sourceID string) TriggerEvent {
	return TriggerEvent{
		TriggerType: triggerType,
		SourceID:    sourceID,
		Payload:     map[string]any{},
		Timestamp:   float64(time.Now().UnixNano()) / 1e9,
	}
}

type AgentSpec struct {
	ID          string
	Name        string
	Description string

	AgentType string

	InputKeys          []string
	OutputKeys         []string
	NullableOutputKeys []string

	ReportSchema map[string]any

	SystemPrompt     *string
	Tools            []string
	ToolAccessPolicy string
	Model            *string

	Function *string
	Routes   map[string]string

	MaxRetries int
	RetryOn    []string

	MaxVisits int

	ClientFacing bool

	SuccessCriteria *string
	SkipJudge       bool
}

func NewAgentSpec(id, name, description string) AgentSpec {
	return AgentSpec{
		ID:               id,
		Name:             name,
		Description:      description,
		AgentType:        "event_loop",
		ToolAccessPolicy: "explicit",
		MaxRetries:       3,
	}
}

func (s AgentSpec) IsQueen() bool { return s.ID == "queen" }

func (s AgentSpec) SupportsDirectUserIO() bool { return s.IsQueen() }

func DeprecatedClientFacingWarning(spec AgentSpec) string {
	if spec.ClientFacing && !spec.IsQueen() {
		return fmt.Sprintf(
			"Agent '%s' sets deprecated client_facing=True. "+
				"Non-queen direct human I/O is no longer supported; route worker "+
				"questions and approvals through queen escalation instead.",
			spec.ID,
		)
	}
	return ""
}

func WarnIfDeprecatedClientFacing(spec AgentSpec) {
	if warning := DeprecatedClientFacingWarning(spec); warning != "" {
		log.Printf("%s", warning)
	}
}

type ColonyBinding struct {
	TrackerDB string
}

type AgentContext struct {
	Runtime   any
	AgentID   string
	AgentSpec AgentSpec

	InputData map[string]any

	LLM            llm.Completer
	AvailableTools []llm.ToolDef

	GoalContext string
	Goal        any

	MaxTokens int

	Attempt     int
	MaxAttempts int

	RuntimeLogger any
	PauseEvent    any

	AccountsPrompt string

	IdentityPrompt string
	Narrative      string
	MemoryPrompt   string

	EventTriggered bool

	ExecutionID string
	RunID       string

	StreamID string

	SessionID *string
	ColonyID  *string

	DynamicToolsProvider         any
	DynamicPromptProvider        any
	DynamicPromptSuffixProvider  func() string
	DynamicMemoryProvider        func() string
	DynamicSkillsCatalogProvider func() string

	SkillsCatalogPrompt string
	ProtocolsPrompt     string
	SkillDirs           []string

	IterationMetadataProvider any

	ActiveWorkersProvider          any
	PastConversationRecallProvider any
	ChatAppProvider                any
	TurnContextProvider            any
	ColonyBindingProvider          func() *ColonyBinding
	ColonyStatsProvider            any

	CRMSetup bool

	SearchableToolsProvider    any
	QueenSkillsCatalogProvider any
	LoadedToolNamesProvider    any

	// Reminder providers the server session runtime fills from its own stores.
	// They are funnelled into ReminderAgentContext by NewReminderAgentContext;
	// nil means "no data" and the matching reminder source self-skips.
	OpenTasksProvider         func() []IdleTask
	SessionGoalProvider       func() string
	SentinelAutopilotProvider func() bool

	ToolTierState any

	StopAfterToolBatch func() bool

	PivotPayloadSink any

	// EscalationSink publishes ESCALATION_REQUESTED when this node calls the
	// synthetic escalate tool. The colony runtime wires its own bus here; nil
	// means the stream has no escalation channel and the call is refused with
	// the upstream error instead of being silently dropped.
	//
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (escalate)
	EscalationSink EscalationSink

	// Observer receives live turn progress so a host can mirror the loop's
	// upstream EventBus emissions without this package importing the bus.
	Observer LoopObserver
}

// LoopObserver receives live progress from ExecuteTurn. The server's chat
// runtime implements it to publish the same events the upstream agent loop
// emits (text, tool calls, per-LLM-call usage) while the turn is running.
//
// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// Parity: framework/agent_loop/internals/event_publishing.py::publish_llm_turn_complete
// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_started
// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_completed
type LoopObserver interface {
	// IterationStarted fires once per inner turn, right before the LLM call.
	IterationStarted(iteration int)
	// LLMTurnComplete fires after each successful LLM call with its usage.
	LLMTurnComplete(completion llm.Completion, iteration int)
	// AssistantText fires for each assistant message the model produced.
	AssistantText(content, snapshot string, iteration int)
	// ToolCallStarted fires immediately before a tool executes.
	ToolCallStarted(call ToolCallRequest)
	// ToolCallCompleted fires with the result the tool returned.
	ToolCallCompleted(call ToolCallRequest, result ToolResult)
}

type CurrentToolNamer interface {
	CurrentToolNames() []string
}

func (c *AgentContext) EffectiveRunID() *string {
	if c == nil || c.RunID == "" {
		return nil
	}
	id := c.RunID
	return &id
}

func (c *AgentContext) IsQueenStream() bool {
	if c == nil {
		return false
	}
	return c.StreamID == "queen" || c.AgentSpec.IsQueen()
}

func (c *AgentContext) EmitsClientIO() bool { return c.IsQueenStream() }

func (c *AgentContext) SupportsDirectUserIO() bool {
	return c.IsQueenStream() && !c.EventTriggered
}

type AgentResult struct {
	Success bool
	Output  map[string]any
	Error   *string

	NextAgent   *string
	RouteReason *string

	TokensUsed    int
	LatencyMS     int
	ToolCallsUsed int

	ValidationErrors []string

	Conversation any

	ExitReason string

	ReliabilityStats map[string]int
}

func NewAgentResult(success bool) AgentResult {
	return AgentResult{Success: success, Output: map[string]any{}, ExitReason: "?"}
}

func (r AgentResult) ToSummary(_ any) string {
	if !r.Success {
		if r.Error != nil {
			return "Failed: " + *r.Error
		}
		return "Failed: None"
	}
	if len(r.Output) == 0 {
		return "Completed (no output)"
	}
	parts := []string{fmt.Sprintf("Completed with %d outputs:", len(r.Output))}
	count := 0
	for key, value := range r.Output {
		if count >= 5 {
			break
		}
		valueStr := fmt.Sprintf("%v", value)
		if len(valueStr) > 100 {
			valueStr = valueStr[:100] + "..."
		}
		parts = append(parts, "  - "+key+": "+valueStr)
		count++
	}
	return strings.Join(parts, "\n")
}

type AgentReport struct {
	Status  string
	Summary string
	Data    map[string]any
}

type AgentProtocol interface {
	Execute(runtimeCtx context.Context, ctx *AgentContext) (AgentResult, error)
	ValidateInput(ctx *AgentContext) []string
}

func ValidateAgentInput(ctx *AgentContext) []string {
	errors := []string{}
	if ctx == nil {
		return errors
	}
	for _, key := range ctx.AgentSpec.InputKeys {
		if _, ok := ctx.InputData[key]; !ok {
			errors = append(errors, "Missing required input: "+key)
		}
	}
	return errors
}

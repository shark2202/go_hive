package agentloop

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
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
	// InputDataOrder is the insertion order of InputData keys. Upstream builds
	// the initial user message by iterating ctx.input_data.items() (Python dict
	// order); a Go map cannot carry that order, so producers that care about
	// the task frame's field order record it here. BuildInitialMessage emits
	// these keys first, then any remaining keys in deterministic sorted order.
	// Nil keeps the historical sorted-only behaviour.
	//
	// Parity: framework/agent_loop/agent_loop.py::_build_initial_message
	InputDataOrder []string

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
	// DynamicSystemPromptProvider rebuilds this context's system prompt before
	// each model call, returning the cache-stable static prefix and the
	// dynamic suffix. The loop updates the conversation only when the rendered
	// text actually changed, so a stable prompt costs no cache invalidation.
	//
	// It is the Go opt-in for upstream's per-iteration prompt refresh
	// (framework/agent_loop/agent_loop.py::_execute_impl step 6b3): the colony
	// worker lifecycle wires it to BuildSystemPromptPartsForContext so the live
	// skills catalog reaches an in-flight worker. Nil means the conversation's
	// prompt is caller-owned — the queen's static prompt and the overseer keep
	// their own, upstream's dynamic-prompt-provider bypass.
	DynamicSystemPromptProvider func() (string, string)

	SkillsCatalogPrompt string
	ProtocolsPrompt     string
	SkillDirs           []string

	IterationMetadataProvider any
	// Phase reports the queen's live phase ("independent" / "colony"). The
	// framework-level colony dispatch gates read it (suggest_colony is
	// independent-only, task_create(new_colony=true) is colony-only); nil
	// skips the gate, as upstream does when iteration_metadata_provider is
	// None.
	//
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (phase gate)
	Phase func() string

	ActiveWorkersProvider          any
	PastConversationRecallProvider any
	ChatAppProvider                any
	TurnContextProvider            any
	ColonyBindingProvider          func() *ColonyBinding
	ColonyStatsProvider            any

	// Memory notes push (memory_recall.mode "wait"/"index"): the pick for a
	// message, the full note listing, and the settings read at each message.
	// Nil means the matching source self-skips.
	MemoryRecallProvider         func(string) string
	MemoryIndexProvider          func() []MemoryNoteScope
	MemoryRecallSettingsProvider func() MemoryRecallSettings

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

// RequestMessageRecorder is the optional companion seam to LoopObserver: the
// loop reports how many messages the provider request carried, before the
// assistant message and tool results are appended. The chat emitter publishes
// llm_turn_complete after the tool batch (upstream's timing) and uses this
// snapshot to report the request it actually sent, as upstream's
// _compute_turn_diagnostics does with the captured request_messages.
//
// Parity: framework/agent_loop/agent_loop.py::execute (_compute_turn_diagnostics)
type RequestMessageRecorder interface {
	NoteRequestMessages(count int)
}

// ToolBatchContextObserver is the optional companion seam to LoopObserver for
// the context readouts upstream publishes while a tool batch drains: one
// post_tool_call frame after each tool result lands in the conversation, and
// one post_tool_results frame once the whole batch is stored. The frames are
// emitted in call order after the corresponding tool_call_completed, matching
// upstream's in-loop publication; observers that do not implement the seam
// simply skip the readouts.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (publish_context_usage)
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
type ToolBatchContextObserver interface {
	// ToolResultStored fires after one executed tool result was appended.
	ToolResultStored()
	// ToolBatchStored fires once after the batch's results were appended.
	ToolBatchStored()
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

// InputDataPair is one ordered input_data entry. Callers that know the
// insertion order (a JSON task frame, a colony worker's task+binding payload)
// use SetInputData instead of assigning the map directly so BuildInitialMessage
// can preserve it.
type InputDataPair struct {
	Key   string
	Value any
}

// SetInputData replaces InputData and records the pairs' insertion order.
// A duplicate key keeps its first position; later duplicate pairs override the
// value.
//
// Parity: framework/agent_loop/agent_loop.py::_build_initial_message
func (c *AgentContext) SetInputData(pairs ...InputDataPair) {
	if c == nil {
		return
	}
	c.InputData = make(map[string]any, len(pairs))
	order := make([]string, 0, len(pairs))
	seen := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		if pair.Key == "" {
			continue
		}
		if _, ok := seen[pair.Key]; !ok {
			seen[pair.Key] = struct{}{}
			order = append(order, pair.Key)
		}
		c.InputData[pair.Key] = pair.Value
	}
	c.InputDataOrder = order
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

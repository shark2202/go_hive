package hive

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// LoopConfig is one agent loop's budget and guard configuration.
//
// The zero value is usable: the loop falls back to the upstream defaults for
// every unset field. DefaultLoopConfig returns the shipped default profile;
// QueenLoopConfig and ColonyQueenLoopConfig return the queen profiles.
//
// Parity: framework/agent_loop/internals/types.py::LoopConfig
type LoopConfig = agentloop.LoopConfig

// DefaultLoopConfig is the generic loop profile (50 iterations, 180k context,
// 30/50 tool-call envelope).
//
// Parity: framework/agent_loop/agent_loop.py LoopConfig defaults
func DefaultLoopConfig() LoopConfig {
	return agentloop.DefaultLoopConfig()
}

// QueenLoopConfig is an independent queen's profile: an effectively unbounded
// iteration budget with the 30/150 tool-call envelope.
//
// Parity: core/framework/agents/queen/agent.py::queen_loop_config
func QueenLoopConfig() LoopConfig {
	return agentloop.QueenLoopConfig()
}

// ColonyQueenLoopConfig is the colony queen's (overseer) profile: the same
// unbounded loop with the wider 50/500 envelope.
//
// Parity: core/framework/agents/queen/agent.py::queen_colony_loop_config
func ColonyQueenLoopConfig() LoopConfig {
	return agentloop.ColonyQueenLoopConfig()
}

// AgentSpec describes one agent: identity, prompt, declared tools and judge
// contract.
//
// Parity: framework/agent_loop/types.py::AgentSpec
type AgentSpec = agentloop.AgentSpec

// NewAgentSpec builds a spec with the runtime's defaults (agent type
// "event_loop", explicit tool access, 3 retries).
//
// Parity: framework/agent_loop/types.py::AgentSpec
func NewAgentSpec(id, name, description string) AgentSpec {
	return agentloop.NewAgentSpec(id, name, description)
}

// AgentReport is the payload an agent sends through report_to_parent.
//
// Parity: framework/agent_loop/types.py::AgentReport
type AgentReport = agentloop.AgentReport

// AgentResult is the terminal result of one agent execution.
//
// Parity: framework/agent_loop/types.py::AgentResult
type AgentResult = agentloop.AgentResult

// Message is one stored conversation turn.
//
// Parity: framework/agent_loop/conversation.py::Message
type Message = agentloop.Message

// ToolCallRecord is one executed tool call inside a turn.
//
// Parity-exempt: Go-only execution record; Python keeps the same data in
// plain tool-result dictionaries.
type ToolCallRecord = agentloop.ToolCallRecord

// ParkReason names why a turn parked instead of finishing naturally.
//
// Parity: framework/agent_loop/reminders.py ParkReason
type ParkReason = agentloop.ParkReason

// Park reasons the public loop can report. The values are the runtime's
// canonical strings.
const (
	ParkAskUser      = agentloop.ParkReasonAskUser
	ParkTurnDone     = agentloop.ParkReasonTurnDone
	ParkUserStopped  = agentloop.ParkReasonUserStopped
	ParkLLMError     = agentloop.ParkReasonLLMError
	ParkEmptyReplies = agentloop.ParkReasonEmptyResponses
	ParkDoomLoop     = agentloop.ParkReasonDoomLoop
)

// ToolUse is one tool invocation the model requested, handed to a
// ToolExecutor.
//
// Parity: framework/llm/provider.py::ToolUse
type ToolUse = agentloop.ToolCallRequest

// ToolResult is what a ToolExecutor returns for one ToolUse.
//
// The runtime's loop and tool registry each carry a slightly different result
// shape internally; this is the union a derivative project sees.
//
// Parity: framework/llm/provider.py::ToolResult
type ToolResult struct {
	ToolUseID      string
	Content        string
	IsError        bool
	ImageContent   []map[string]any
	IsSkillContent bool
	SpilloverPath  string
	Attachments    []Attachment
}

// Attachment is one file a tool surfaced for the user.
//
// Parity-exempt: Go-only attachment reference (tools surface); Python returns
// the same information inside the tool result content.
type Attachment = tools.AttachmentRef

// ToolExecutor runs one tool call for the loop. The loop calls it with no
// context argument; derive deadlines from the loop's own turn context when
// needed, exactly as the colony runtime's registry adapter does.
//
// Parity: framework/agent_loop/agent_loop.py tool_executor
type ToolExecutor func(ToolUse) (ToolResult, error)

// AgentContext is the per-turn input to an agent loop: who is running, with
// which model, tools, input data and prompt fragments.
//
// This is a curated view of the runtime context. Advanced hooks (dynamic
// providers, observer seams, escalation sinks, skill catalogs owned by the
// server) are intentionally not part of the public contract.
//
// Parity: framework/agent_loop/types.py::AgentContext
type AgentContext struct {
	AgentID        string
	AgentSpec      AgentSpec
	InputData      map[string]any
	InputDataOrder []string
	LLM            Model
	Tools          []Tool
	GoalContext    string
	Goal           *Goal

	// SystemPrompt seeds the conversation in Loop.Run. It is not part of the
	// Python AgentContext (which receives a Conversation) and is ignored by
	// Loop.ExecuteTurn.
	//
	// Parity-exempt: Go-only convenience for Loop.Run.
	SystemPrompt string

	MaxTokens int

	StreamID    string
	SessionID   *string
	ColonyID    *string
	ExecutionID string
	RunID       string

	AccountsPrompt string
	IdentityPrompt string
	Narrative      string
	MemoryPrompt   string

	SkillsCatalogPrompt string
	ProtocolsPrompt     string
	SkillDirs           []string

	EventTriggered bool
	Attempt        int
	MaxAttempts    int
}

func (a *AgentContext) toInternal() *agentloop.AgentContext {
	if a == nil {
		return nil
	}
	internal := &agentloop.AgentContext{
		AgentID:             a.AgentID,
		AgentSpec:           a.AgentSpec,
		InputData:           a.InputData,
		InputDataOrder:      a.InputDataOrder,
		LLM:                 a.LLM,
		GoalContext:         a.GoalContext,
		MaxTokens:           a.MaxTokens,
		StreamID:            a.StreamID,
		SessionID:           a.SessionID,
		ColonyID:            a.ColonyID,
		ExecutionID:         a.ExecutionID,
		RunID:               a.RunID,
		AccountsPrompt:      a.AccountsPrompt,
		IdentityPrompt:      a.IdentityPrompt,
		Narrative:           a.Narrative,
		MemoryPrompt:        a.MemoryPrompt,
		SkillsCatalogPrompt: a.SkillsCatalogPrompt,
		ProtocolsPrompt:     a.ProtocolsPrompt,
		SkillDirs:           a.SkillDirs,
		EventTriggered:      a.EventTriggered,
		Attempt:             a.Attempt,
		MaxAttempts:         a.MaxAttempts,
	}
	if len(a.Tools) > 0 {
		internal.AvailableTools = toolDefs(a.Tools)
	}
	if a.Goal != nil {
		internal.Goal = a.Goal
	}
	return internal
}

// TurnResult is the outcome of one ExecuteTurn call.
//
// Parity-exempt: Go-only turn result; Python's execute() returns AgentResult
// and reports through on_report.
type TurnResult struct {
	Success       bool
	FinalText     string
	InnerTurns    int
	ToolResults   []ToolCallRecord
	ToolCallsUsed int
	// Parked reports that the loop stopped on a park reason instead of a
	// natural answer.
	Parked     bool
	ParkReason ParkReason
}

func turnResultFromInternal(in agentloop.TurnResult) TurnResult {
	out := TurnResult{
		Success:       in.Success,
		FinalText:     in.FinalText,
		InnerTurns:    in.InnerTurns,
		ToolResults:   in.ToolResults,
		ToolCallsUsed: in.ToolCallsUsed,
	}
	if in.ParkReason != nil {
		out.Parked = true
		out.ParkReason = *in.ParkReason
	}
	return out
}

// Conversation is one agent's transcript.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation
type Conversation struct {
	inner *agentloop.NodeConversation
}

// NewConversation starts an empty conversation with a system prompt.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation
func NewConversation(systemPrompt string) *Conversation {
	return &Conversation{inner: agentloop.NewNodeConversation(systemPrompt)}
}

// AddUserMessage appends a user turn.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation.add_user_message
func (c *Conversation) AddUserMessage(content string) (Message, error) {
	return c.inner.AddUserMessage(content, agentloop.UserMessageOptions{})
}

// AddAssistantMessage appends an assistant turn.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation.add_assistant_message
func (c *Conversation) AddAssistantMessage(content string, toolCalls []map[string]any) (Message, error) {
	return c.inner.AddAssistantMessage(content, toolCalls, agentloop.AssistantMessageOptions{})
}

// Messages returns a copy of the transcript.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation.messages
func (c *Conversation) Messages() []Message {
	return c.inner.Messages()
}

// MessageCount is the number of stored turns.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation.message_count
func (c *Conversation) MessageCount() int {
	return c.inner.MessageCount()
}

// SystemPrompt is the conversation's current system prompt.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation.system_prompt
func (c *Conversation) SystemPrompt() string {
	return c.inner.SystemPrompt()
}

// LastAssistantText is the most recent assistant text, if any.
//
// Parity-exempt: Go-only transcript accessor; Python callers walk Messages().
func (c *Conversation) LastAssistantText() string {
	return c.inner.LastAssistantText()
}

// EstimateTokens is the conversation's approximate token footprint.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation.estimate_tokens
func (c *Conversation) EstimateTokens() int {
	return c.inner.EstimateTokens()
}

// Clear empties the transcript.
//
// Parity: framework/agent_loop/conversation.py::NodeConversation.clear
func (c *Conversation) Clear() error {
	return c.inner.Clear()
}

// Loop runs one agent's turns. It is a thin facade over the runtime loop: it
// does not add a second execution engine.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop
type Loop struct {
	inner *agentloop.AgentLoop
}

// NewLoop builds a loop with the given configuration and tool executor. A nil
// executor is allowed for a text-only agent.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop
func NewLoop(config LoopConfig, executor ToolExecutor) *Loop {
	var internal agentloop.ToolExecutor
	if executor != nil {
		internal = func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			result, err := executor(req)
			if err != nil {
				return agentloop.ToolResult{}, err
			}
			return result.toInternal(), nil
		}
	}
	return &Loop{inner: agentloop.NewAgentLoop(config, internal)}
}

// ExecuteTurn runs one turn against an existing conversation. tools overrides
// the context's Tools when non-nil; nil keeps the context's declared tools.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.execute
func (l *Loop) ExecuteTurn(ctx context.Context, agent *AgentContext, conv *Conversation, tools []Tool) (TurnResult, error) {
	if l == nil || l.inner == nil {
		return TurnResult{}, errNilLoop
	}
	if ctx == nil {
		return TurnResult{}, errNilContext
	}
	if conv == nil || conv.inner == nil {
		return TurnResult{}, errNilConversation
	}
	var defs []llm.ToolDef
	if tools != nil {
		defs = toolDefs(tools)
	}
	result, err := l.inner.ExecuteTurn(ctx, agent.toInternal(), conv.inner, defs)
	return turnResultFromInternal(result), err
}

// Run is the one-call convenience: it builds a conversation from
// agent.SystemPrompt, then executes one turn. Use ExecuteTurn when the
// transcript must survive across turns.
//
// Parity-exempt: Go-only convenience over AgentLoop.execute.
func (l *Loop) Run(ctx context.Context, agent *AgentContext) (TurnResult, error) {
	if agent == nil {
		return TurnResult{}, errNilAgentContext
	}
	prompt := agent.SystemPrompt
	if prompt == "" && agent.AgentSpec.SystemPrompt != nil {
		prompt = *agent.AgentSpec.SystemPrompt
	}
	conv := NewConversation(prompt)
	return l.ExecuteTurn(ctx, agent, conv, nil)
}

// ToolCallsUsed is the loop's cumulative tool-call counter.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.tool_calls_used
func (l *Loop) ToolCallsUsed() int {
	if l == nil || l.inner == nil {
		return 0
	}
	return l.inner.ToolCallsUsed()
}

// Stats returns the loop's internal counters.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.stats
func (l *Loop) Stats() map[string]int {
	if l == nil || l.inner == nil {
		return nil
	}
	return l.inner.Stats()
}

func (r ToolResult) toInternal() agentloop.ToolResult {
	return agentloop.ToolResult{
		Content:        r.Content,
		IsError:        r.IsError,
		IsSkillContent: r.IsSkillContent,
		SpilloverPath:  r.SpilloverPath,
		ImageContent:   r.ImageContent,
	}
}

package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: framework/agent_loop/agent_loop.py LoopConfig (defaults mirror
type LoopConfig struct {
	MaxIterations        int
	MaxContextTokens     int
	ToolWaitLimitSeconds float64
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.grace_iterations.
	GraceIterations int
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.tool_call_budget.
	ToolCallBudget int
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.tool_call_hard_multiple.
	ToolCallHardMultiple int
	// ToolCallHardLimit, when > 0, is the absolute per-turn-loop hard stop. It
	// overrides ToolCallBudget*ToolCallHardMultiple so a role profile can pin an
	// exact cap (the queen's 30 soft / 50 hard, the audit's numbers); zero keeps
	// the upstream multiple rule. Ignored when ToolCallBudget is 0.
	ToolCallHardLimit int
	// Parity: framework/agent_loop/internals/types.py
	ToolCallLifetimeBudget int
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.judge_every_n_turns.
	JudgeEveryNTurns int
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeSeconds float64
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeMaxPerSession int
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeAwaitingSeconds float64
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeBrokenSeconds float64
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.continue_nudge_enabled.
	ContinueNudgeEnabled bool
	// Parity: framework/agent_loop/internals/types.py
	ContinueNudgeMaxPerTurn int
	// Parity: framework/agent_loop/internals/types.py
	ToolCallTimeoutSeconds float64
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.tool_timeout_overrides.
	// ToolTimeoutOverrides maps a tool-name prefix to its timeout.
	ToolTimeoutOverrides map[string]float64
	// ToolCallGate paces each tool execution against the configured call
	// limits (rate_limits.tools.* in hive.config.json). NewAgentLoop installs
	// the process-wide limiter when this is nil; it is a no-op until a bucket
	// is configured. A non-nil no-op disables pacing for a loop.
	ToolCallGate func(ctx context.Context, toolName string) error
	// SessionCostLimitUSD is the optional hard cap on cumulative session
	// spend. Zero (the default) disables it. Once the loop's recorded spend
	// reaches the cap, the next ExecuteTurn returns *SessionCostLimitError
	// before any model call.
	SessionCostLimitUSD float64
	// ParallelReadOnlyTools runs a tool batch concurrently when every call in
	// it is explicitly marked ConcurrencySafe (llm.ToolDef.ConcurrencySafe).
	// DefaultLoopConfig enables it, matching upstream; an unsafe or unmarked
	// call keeps the whole batch sequential either way.
	ParallelReadOnlyTools bool
	// MaxToolResultChars caps the LLM-visible tool result. A result above it is
	// replaced with a preview while the full payload is written to
	// SpilloverDir. Zero disables the cap.
	MaxToolResultChars int
	// SpilloverDir, when set, stores an oversized non-error tool result on disk
	// under a monotonic filename; the model-visible result then cites the file
	// so the full payload can be read back.
	SpilloverDir string
}

// Parity: framework/agent_loop/internals/types.py LoopConfig defaults.
func DefaultLoopConfig() LoopConfig {
	return LoopConfig{
		MaxIterations:        50,
		MaxContextTokens:     180_000,
		ToolWaitLimitSeconds: 600,
		GraceIterations:      0,
		// The queen profile: a soft checkpoint every 30 tool calls in one
		// turn-loop, a hard stop at 50. Upstream's queen runs the neutral
		// 0/unbounded default, so this is the port's fix for the audit gap
		// ("Queen 30/50 软检查点"): without it softBudgetCheckpointBlock never
		// fires in a chat session. Worker profiles override both fields.
		ToolCallBudget:                  30,
		ToolCallHardLimit:               50,
		ToolCallHardMultiple:            5,
		ToolCallLifetimeBudget:          0,
		JudgeEveryNTurns:                1,
		SessionIdleNudgeSeconds:         120.0,
		SessionIdleNudgeMaxPerSession:   3,
		SessionIdleNudgeAwaitingSeconds: 120.0,
		SessionIdleNudgeBrokenSeconds:   30.0,
		ContinueNudgeEnabled:            true,
		ContinueNudgeMaxPerTurn:         3,
		ToolCallTimeoutSeconds:          60.0,
		ToolTimeoutOverrides:            map[string]float64{"browser_": 180.0},
		// Spend cap: opt-in, read from HIVE_SESSION_COST_LIMIT_USD once per
		// config build. Unset means 0 (disabled), matching the shipped default.
		SessionCostLimitUSD: SessionCostLimitFromEnv(),
		// Match upstream: safe (explicitly marked) tools may run together.
		// The loop still keeps a batch sequential when any call is unmarked.
		ParallelReadOnlyTools: true,
		MaxToolResultChars:    30_000,
	}
}

// Parity: framework/llm/provider.py ToolUse (id/name/input).
type ToolCallRequest struct {
	ID    string
	Name  string
	Input map[string]any
}

type ToolExecutor func(ToolCallRequest) (ToolResult, error)

type ToolCallRecord struct {
	ToolUseID string
	ToolName  string
	ToolInput map[string]any
	Content   string
	IsError   bool
}

type TurnResult struct {
	Success       bool
	FinalText     string
	InnerTurns    int
	ToolResults   []ToolCallRecord
	ToolCallsUsed int
	// ParkReason is the park the loop raised on its own, when it did: a
	// deterministic doom loop stops the turn instead of returning to the model.
	// Nil means the turn ended on a model answer or an ordinary error.
	ParkReason *ParkReason
}

// Parity: framework/agent_loop/agent_loop.py _GRACE_TERMINAL_TOOLS,
var graceTerminalTools = map[string]struct{}{
	"report_to_parent": {},
	"tracker_upsert":   {},
	"task_update":      {},
}

const (
	graceSkipMessage = "[Skipped — this is your final (grace) iteration. Only report_to_parent, tracker_upsert, and task_update may execute. Call report_to_parent now with whatever status you have (success, partial, or failed) — do not start new work.]"

	graceReminderBody = "[final iteration] Your iteration budget is exhausted. This is your LAST turn. Call report_to_parent(status=<success|partial|failed>, summary=<one paragraph>, data=<optional>) NOW to deliver your results.\n\nIf you still need to persist findings to shared state you may also call tracker_upsert or task_update. ALL OTHER TOOLS WILL BE SKIPPED — do not start new work; consolidate what you have and report."

	toolBudgetGraceReminderBody = "[tool-call budget reached] You have used your full tool-call budget for this task. This is your LAST turn. Call report_to_parent(...) NOW to deliver your partial results to the queen.\n\nIf you still need to persist findings you may also call tracker_upsert or task_update. ALL OTHER TOOLS WILL BE SKIPPED — do not start new work; consolidate what you have and report."

	lifetimeBudgetDeferMessage = "[Not executed — you reached your cumulative tool-call budget for this task. This is not an error and the call did not fail. You are out of work budget: next turn winds down — report_to_parent now (tracker_upsert / task_update also allowed) instead of re-issuing this call.]"

	hardStopDeferMessage = "[Not executed — the tool-call budget hard stop was reached before this call. This is not an error and the call did not fail; re-issue it on the next turn if still needed.]"
)

type injectedInput struct {
	content     string
	isClient    bool
	images      []map[string]any
	correlation string
}

// Parity: framework/agent_loop/agent_loop.py AgentLoop (the subset covering
type AgentLoop struct {
	Config       LoopConfig
	ToolExecutor ToolExecutor
	Hub          *ReminderHub

	// Judge evaluates each turn against AgentSpec.SuccessCriteria. Nil (the
	// default) disables judge evaluation.
	Judge Judge
	// JudgeEvents receives one verdict event per evaluation. Nil disables
	// event publication.
	JudgeEvents JudgeEventWriter
	// OnJudgeVerdict, when set, observes every normalized verdict after the
	// event is published. The colony runtime uses it to turn RETRY/ESCALATE
	// feedback into improvement signals; nil is a no-op.
	OnJudgeVerdict func(verdict JudgeVerdict, iteration int)

	// DoomLoop watches for identical consecutive tool calls and returns one
	// advisory reminder per streak. It never interrupts the turn.
	DoomLoop *DoomLoopDetector

	// ParkSink, when set, observes every park the loop raises itself (a doom
	// loop past its park threshold) with the reason and a one-line detail. The
	// server wires it to the session event bus so the park is published instead
	// of ending in a log line. Nil is a no-op.
	ParkSink func(reason ParkReason, detail string)

	now func() time.Time

	mu      sync.Mutex
	queue   []injectedInput
	pending []string
	unread  bool
	bound   bool

	stateMu             sync.Mutex
	toolCallsUsed       int
	sessionCostUSD      float64
	counters            map[string]int
	inGrace             bool
	graceStartIteration *int
	graceAnnounced      bool

	spillMu      sync.Mutex
	spillCounter int
}

func NewAgentLoop(config LoopConfig, executor ToolExecutor) *AgentLoop {
	if config.ToolCallGate == nil {
		config.ToolCallGate = defaultToolCallGate
	}
	loop := &AgentLoop{
		Config:       config,
		ToolExecutor: executor,
		Hub:          NewReminderHub(),
		DoomLoop:     NewDoomLoopDetector(),
	}
	loop.spillCounter = restoreSpillCounter(config.SpilloverDir)
	return loop
}

func (l *AgentLoop) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *AgentLoop) stamp() string {
	return l.clock().Format("2006-01-02 15:04 MST")
}

func (l *AgentLoop) maxIterations() int {
	if l.Config.MaxIterations > 0 {
		return l.Config.MaxIterations
	}
	return 50
}

func (l *AgentLoop) waitLimit() float64 {
	if l.Config.ToolWaitLimitSeconds > 0 {
		return l.Config.ToolWaitLimitSeconds
	}
	return 600
}

func (l *AgentLoop) waitTick() time.Duration { return time.Second }

// Parity: AgentLoop.tool_calls_used property.
func (l *AgentLoop) ToolCallsUsed() int {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.toolCallsUsed
}

// Parity: AgentLoop._config.tool_call_lifetime_budget reads in the
func (l *AgentLoop) LifetimeBudget() int {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.Config.ToolCallLifetimeBudget
}

// Parity: AgentLoop.stats.
func (l *AgentLoop) Counter(key string) int {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.counters[key]
}

// Parity: AgentLoop.stats.
func (l *AgentLoop) Stats() map[string]int {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	out := make(map[string]int, len(l.counters))
	for key, value := range l.counters {
		out[key] = value
	}
	return out
}

// Parity: Worker._loop_budget_limited / _build_result budget_limited
func (l *AgentLoop) BudgetLimited() bool {
	return l.Counter("tool_lifetime_budget_grace") > 0
}

// Parity: AgentLoop.apply_lifetime_budget_cap.
func (l *AgentLoop) ApplyLifetimeBudgetCap(newBudget int) bool {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	current := l.Config.ToolCallLifetimeBudget
	if newBudget <= 0 || current <= 0 || newBudget >= current {
		return false
	}
	if l.Config.GraceIterations <= 0 {
		return false
	}
	l.Config.ToolCallLifetimeBudget = newBudget
	return true
}

func (l *AgentLoop) bumpLocked(key string, amount int) {
	if l.counters == nil {
		l.counters = map[string]int{}
	}
	l.counters[key] += amount
}

func (l *AgentLoop) bumpBy(key string, amount int) {
	if amount <= 0 {
		return
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.bumpLocked(key, amount)
}

// Parity: AgentLoop.inject_event. A client message with content also joins the
func (l *AgentLoop) InjectEvent(content string, isClientInput bool, images []map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if isClientInput && content != "" {
		l.pending = append(l.pending, content)
	}
	l.queue = append(l.queue, injectedInput{
		content:  content,
		isClient: isClientInput,
		images:   images,
	})
}

// Parity: AgentLoop._input_waiting.
func (l *AgentLoop) InputWaiting() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unread || len(l.queue) > 0
}

// Parity: AgentLoop._inject_queued_input. USER_PROMPT_SUBMIT fires before the
func (l *AgentLoop) injectQueuedInput(agentCtx *ReminderAgentContext, conv *NodeConversation) (bool, error) {
	l.mu.Lock()
	if len(l.queue) == 0 {
		l.mu.Unlock()
		return false, nil
	}
	userText := strings.Join(l.pending, "\n")
	l.pending = nil
	l.mu.Unlock()

	l.fireReminder(USER_PROMPT_SUBMIT, agentCtx, conv, userText)

	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.mu.Unlock()
			return true, nil
		}
		item := l.queue[0]
		l.queue = l.queue[1:]
		l.mu.Unlock()

		stamp := l.stamp()
		var stamped string
		if item.isClient {
			if item.content != "" {
				stamped = fmt.Sprintf("[%s] %s", stamp, item.content)
			} else {
				stamped = fmt.Sprintf("[%s]", stamp)
			}
		} else {
			stamped = fmt.Sprintf("[%s] [External event] %s", stamp, item.content)
		}
		if _, err := conv.AddUserMessage(stamped, UserMessageOptions{
			IsClientInput: item.isClient,
			ImageContent:  item.images,
		}); err != nil {
			return true, err
		}
	}
}

// compactConversationIfNeeded summarizes the conversation in place when it
// crosses its compaction threshold, firing the reminder points around the
// summary the way upstream _compact does. Best-effort: a failed
// summary leaves the conversation untouched (the next LLM call then either
// fits or reports its own context error).
//
// Parity: framework/agent_loop/agent_loop.py::_compact
func (l *AgentLoop) compactConversationIfNeeded(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, reminderCtx *ReminderAgentContext) {
	if conv == nil || agentCtx == nil || agentCtx.LLM == nil || !conv.NeedsCompaction() {
		return
	}
	l.fireReminder(PRE_COMPACT, reminderCtx, conv, "")
	maxContext := l.Config.MaxContextTokens
	if maxContext <= 0 {
		maxContext = DefaultMaxContextTokens
	}
	spec := agentCtx.AgentSpec
	summary, err := LlmCompact(ctx, agentCtx.LLM, conv.Messages(), LlmCompactOptions{
		Spec:             &spec,
		MaxContextTokens: maxContext,
	})
	if err != nil || strings.TrimSpace(summary) == "" {
		return
	}
	if err := conv.Compact(summary, CompactOptions{}); err != nil {
		return
	}
	l.fireReminder(POST_COMPACT, reminderCtx, conv, "")
}

// Parity: AgentLoop._fire_reminder (best-effort: failures never break the turn).
func (l *AgentLoop) fireReminder(point ReminderPoint, agentCtx *ReminderAgentContext, conv *NodeConversation, userText string) bool {
	if l.Hub == nil {
		return false
	}
	block, energized := l.Hub.FireEnergized(point, agentCtx, nil, userText)
	if block == "" {
		return false
	}
	_, _ = conv.AddUserMessage(block, UserMessageOptions{})
	return energized
}

// Parity: AgentLoop._DYNAMIC_REFRESH_SYNTHETIC_NAMES.
var DynamicRefreshSyntheticNames = map[string]struct{}{
	"ask_user":         {},
	"credentials":      {},
	"sentinel_setup":   {},
	"escalate":         {},
	"collect_result":   {},
	"report_to_parent": {},
	"search_tools":     {},
}

// Parity: AgentLoop._refresh_dynamic_tools. Returns the live list unchanged
func (l *AgentLoop) RefreshDynamicTools(agentCtx *AgentContext, tools []llm.ToolDef) []llm.ToolDef {
	if agentCtx == nil {
		return tools
	}
	provided, ok := dynamicToolsFrom(agentCtx.DynamicToolsProvider)
	if !ok {
		return tools
	}
	providedNames := make(map[string]struct{}, len(provided))
	for _, tool := range provided {
		providedNames[tool.Name] = struct{}{}
	}
	out := make([]llm.ToolDef, 0, len(provided)+len(tools))
	out = append(out, provided...)
	for _, tool := range tools {
		if _, synthetic := DynamicRefreshSyntheticNames[tool.Name]; !synthetic {
			continue
		}
		if _, dup := providedNames[tool.Name]; dup {
			continue
		}
		out = append(out, tool)
	}
	return out
}

func dynamicToolsFrom(provider any) ([]llm.ToolDef, bool) {
	switch p := provider.(type) {
	case nil:
		return nil, false
	case func() []llm.ToolDef:
		return p(), true
	case []llm.ToolDef:
		return p, true
	case func() []Tool:
		return toolDefsFromReminderTools(p()), true
	case []Tool:
		return toolDefsFromReminderTools(p), true
	case func() []string:
		names := p()
		out := make([]llm.ToolDef, 0, len(names))
		for _, name := range names {
			out = append(out, llm.ToolDef{Name: name})
		}
		return out, true
	case []string:
		out := make([]llm.ToolDef, 0, len(p))
		for _, name := range p {
			out = append(out, llm.ToolDef{Name: name})
		}
		return out, true
	default:
		return nil, false
	}
}

func toolDefsFromReminderTools(tools []Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(tools))
	for _, tool := range tools {
		out = append(out, llm.ToolDef{Name: tool.Name, Description: tool.Description})
	}
	return out
}

// Parity: AgentLoop._build_initial_message. Python preserves input_data
func BuildInitialMessage(agentCtx *AgentContext) string {
	if agentCtx == nil {
		return ""
	}
	parts := make([]string, 0, len(agentCtx.InputData)+1)
	seen := make(map[string]struct{}, len(agentCtx.InputData))
	keys := make([]string, 0, len(agentCtx.InputData))
	for key := range agentCtx.InputData {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := agentCtx.InputData[key]
		if value == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %v", key, value))
		seen[key] = struct{}{}
	}
	for _, key := range agentCtx.AgentSpec.InputKeys {
		if _, ok := seen[key]; ok {
			continue
		}
		if value, ok := agentCtx.InputData[key]; ok && value != nil {
			parts = append(parts, fmt.Sprintf("%s: %v", key, value))
		}
	}
	if agentCtx.GoalContext != "" {
		parts = append(parts, "\nGoal: "+agentCtx.GoalContext)
	}
	return strings.Join(parts, "\n")
}

func (l *AgentLoop) bindReminders(agentCtx *ReminderAgentContext) {
	l.mu.Lock()
	already := l.bound
	l.bound = true
	l.mu.Unlock()
	if already || l.Hub == nil {
		return
	}
	l.Hub.Bind(agentCtx)
}

// Parity: AgentLoop.execute bringup + _run_turn_loop (single outer iteration).
func (l *AgentLoop) ExecuteTurn(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, tools []llm.ToolDef) (out TurnResult, outErr error) {
	defer func() { out.ToolCallsUsed = l.ToolCallsUsed() }()

	if agentCtx == nil || conv == nil {
		return TurnResult{Success: false}, fmt.Errorf("agent loop: nil agent context or conversation")
	}
	if agentCtx.LLM == nil {
		return TurnResult{Success: false}, fmt.Errorf("agent loop: nil LLM")
	}
	// Spend cap: refuse the turn before any model call. The check is at the
	// turn boundary so the previous turn's work is already persisted.
	if spent, limit, exceeded := l.sessionCostLimitExceeded(); exceeded {
		l.bumpBy(sessionCostBlockedCounter, 1)
		return TurnResult{Success: false}, &SessionCostLimitError{SpentUSD: spent, LimitUSD: limit}
	}
	reminderCtx := NewReminderAgentContext(agentCtx)
	l.bindReminders(reminderCtx)
	// A new turn is a new attempt: a repetition streak from the previous turn
	// says nothing about this one.
	l.DoomLoop.Reset()

	l.mu.Lock()
	l.unread = false
	l.mu.Unlock()

	if conv.MessageCount() == 0 {
		initial := BuildInitialMessage(agentCtx)
		l.fireReminder(SESSION_START, reminderCtx, conv, initial)
		if initial != "" {
			if _, err := conv.AddUserMessage(fmt.Sprintf("[%s] %s", l.stamp(), initial), UserMessageOptions{}); err != nil {
				return TurnResult{Success: false}, err
			}
		}
		if conv.MessageCount() == 0 {
			if initial == "" {
				initial = "Hello"
			}
			if _, err := conv.AddUserMessage(fmt.Sprintf("[%s] %s", l.stamp(), initial), UserMessageOptions{}); err != nil {
				return TurnResult{Success: false}, err
			}
		}
	}

	if tools == nil {
		tools = append([]llm.ToolDef(nil), agentCtx.AvailableTools...)
	}
	tools = l.ensureSearchToolsTool(agentCtx, tools)

	model := modelName(agentCtx)
	result := TurnResult{Success: true}
	limit := l.maxIterations()
	graceIterations := l.Config.GraceIterations
	totalIterations := limit + graceIterations
	// softBudgetReminders tracks how many soft checkpoints this turn-loop has
	// already emitted; each new budget multiple escalates by one.
	softBudgetReminders := 0

	for inner := 0; inner < totalIterations; inner++ {
		if err := ctx.Err(); err != nil {
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}

		l.stateMu.Lock()
		lifetimeBudget := l.Config.ToolCallLifetimeBudget
		budgetExhausted := lifetimeBudget > 0 && l.toolCallsUsed >= lifetimeBudget
		l.inGrace = inner >= limit || budgetExhausted
		inGrace := l.inGrace
		if inGrace && l.graceStartIteration == nil {
			start := inner
			l.graceStartIteration = &start
		}
		graceStart := l.graceStartIteration
		l.stateMu.Unlock()

		reportTerminated := agentCtx.StopAfterToolBatch != nil && agentCtx.StopAfterToolBatch()
		if graceStart != nil && !reportTerminated && inner-*graceStart >= maxInt(1, graceIterations) {
			break
		}

		if inGrace && !l.graceAnnounced && !reportTerminated {
			l.graceAnnounced = true
			l.bumpBy("grace_iteration_entered", 1)
			body := graceReminderBody
			if budgetExhausted {
				l.bumpBy("tool_lifetime_budget_grace", 1)
				body = toolBudgetGraceReminderBody
			}
			block := WrapReminder([]string{body})
			if _, err := conv.AddUserMessage(block, UserMessageOptions{IsSystemReminder: true}); err != nil {
				return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
			}
		}

		l.mu.Lock()
		l.unread = false
		l.mu.Unlock()

		// Parity: framework/agent_loop/agent_loop.py::_publish_iteration
		if agentCtx.Observer != nil {
			agentCtx.Observer.IterationStarted(inner + 1)
		}

		tools = l.RefreshDynamicTools(agentCtx, tools)
		l.compactConversationIfNeeded(ctx, agentCtx, conv, reminderCtx)
		messages := conv.ToLLMMessages(model)
		if len(messages) > 0 && messages[len(messages)-1]["role"] == "assistant" {
			if _, err := conv.AddUserMessage("[Continue working on your current task.]", UserMessageOptions{}); err != nil {
				return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
			}
			messages = conv.ToLLMMessages(model)
		}

		request := llm.CompletionRequest{
			Messages:  toChatMessages(messages),
			System:    conv.SystemPrompt(),
			Tools:     tools,
			MaxTokens: agentCtx.MaxTokens,
		}
		completion, err := agentCtx.LLM.AComplete(ctx, request)
		if err != nil {
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}
		// Record spend as soon as the completion lands so the hard cap sees
		// it even when the turn later fails or is cancelled.
		l.AddSessionCost(completion.CostUSD)
		// Parity: framework/agent_loop/agent_loop.py::_publish_text_delta +
		// _publish_llm_turn_complete. The Go transport is non-streaming, so the
		// complete assistant message is published as one snapshot instead of
		// per-chunk deltas; the event shape is unchanged.
		if agentCtx.Observer != nil {
			if completion.Content != "" {
				agentCtx.Observer.AssistantText(completion.Content, completion.Content, inner+1)
			}
			agentCtx.Observer.LLMTurnComplete(completion, inner+1)
		}

		toolCalls := completion.ToolCalls
		callDicts := toolCallDicts(toolCalls)
		if completion.Content != "" || len(callDicts) > 0 {
			if _, err := conv.AddAssistantMessage(completion.Content, callDicts, AssistantMessageOptions{}); err != nil {
				return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
			}
		}

		names := make([]string, 0, len(toolCalls))
		for _, tc := range toolCalls {
			names = append(names, tc.Name)
		}
		if l.Hub != nil {
			l.Hub.ObserveTurn(names)
		}

		if len(toolCalls) == 0 {
			result.FinalText = completion.Content
			result.InnerTurns = inner + 1
			if l.shouldJudge(agentCtx, inner, true, false) {
				verdict := l.evaluateJudge(ctx, agentCtx, conv, completion.Content, result.ToolResults, inner)
				switch verdict.Action {
				case JudgeAccept:
					return result, nil
				case JudgeEscalate:
					result.Success = false
					return result, fmt.Errorf("agent loop: judge escalated: %s", verdict.Feedback)
				default:
					if err := injectJudgeFeedback(conv, verdict); err != nil {
						result.Success = false
						return result, err
					}
					continue
				}
			}
			return result, nil
		}

		injected, err := l.injectQueuedInput(reminderCtx, conv)
		if err != nil {
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}
		l.mu.Lock()
		l.unread = injected
		l.mu.Unlock()

		results := make([]ToolResult, len(toolCalls))

		softInterval, hardLimit := l.toolBudgetLimits()

		toolCallCount := 0
		limitHit := false
		lifetimeLimitHit := false
		var graceSkippedIdx []int
		var deferredIdx []int
		pending := make([]pendingToolCall, 0, len(toolCalls))

		for i, tc := range toolCalls {
			call := ToolCallRequest{ID: tc.ID, Name: tc.Name, Input: parseToolInput(tc.Arguments)}
			if inGrace {
				if _, terminal := graceTerminalTools[tc.Name]; !terminal {
					graceSkippedIdx = append(graceSkippedIdx, i)
					continue
				}
			}
			toolCallCount++
			if hardLimit > 0 && toolCallCount > hardLimit {
				limitHit = true
				deferredIdx = append(deferredIdx, i)
				continue
			}
			l.stateMu.Lock()
			used := l.toolCallsUsed
			l.stateMu.Unlock()
			if lifetimeBudget > 0 && !inGrace && used >= lifetimeBudget {
				limitHit = true
				lifetimeLimitHit = true
				deferredIdx = append(deferredIdx, i)
				continue
			}
			l.stateMu.Lock()
			l.toolCallsUsed++
			l.stateMu.Unlock()

			// Parity: framework/agent_loop/agent_loop.py::_publish_tool_started.
			// Started events fire in call order even when the batch below runs
			// concurrently; completed events follow the same order.
			if agentCtx.Observer != nil {
				agentCtx.Observer.ToolCallStarted(call)
			}
			pending = append(pending, pendingToolCall{index: i, call: call})
		}

		l.executeToolBatch(ctx, agentCtx, pending, results, tools)

		toolFailure, doomLoopReminder, doomPark := l.observeToolBatch(agentCtx, pending, results)

		if len(graceSkippedIdx) > 0 {
			l.bumpBy("grace_iteration_skipped", len(graceSkippedIdx))
			for _, i := range graceSkippedIdx {
				results[i] = ToolResult{Content: graceSkipMessage}
			}
		}

		if limitHit {
			deferMessage := hardStopDeferMessage
			if lifetimeLimitHit {
				deferMessage = lifetimeBudgetDeferMessage
			}
			l.bumpBy("tool_budget_deferred", len(deferredIdx))
			for _, i := range deferredIdx {
				results[i] = ToolResult{Content: deferMessage}
			}
			l.injectToolBudgetStop(reminderCtx, conv, len(deferredIdx), lifetimeBudget, hardLimit, lifetimeLimitHit, names)
		}

		tail := ""
		if l.Hub != nil {
			tail = l.Hub.Fire(POST_TOOL_USE, reminderCtx, names, "")
		}
		// Parity: framework/agent_loop/agent_loop.py::_render_tool_budget_checkpoint
		// (the soft checkpoint rides the batch tail and never defers work).
		var checkpoint string
		checkpoint, softBudgetReminders = l.softBudgetCheckpointBlock(
			reminderCtx, names, toolCallCount, softInterval, hardLimit, softBudgetReminders)
		if checkpoint != "" {
			if tail == "" {
				tail = checkpoint
			} else {
				tail = tail + "\n\n" + checkpoint
			}
		}
		if doomLoopReminder != "" {
			if tail == "" {
				tail = doomLoopReminder
			} else {
				tail = tail + "\n\n" + doomLoopReminder
			}
		}
		records, err := l.storeToolBatch(conv, toolCalls, results, tail)
		if err != nil {
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}
		result.ToolResults = append(result.ToolResults, records...)

		// A deterministic doom loop stops the turn here: the model has re-issued
		// the same call past the park threshold, so the park replaces another
		// advisory-only reminder.
		if doomPark.parked {
			return l.parkOnDoomLoop(result, inner, completion, doomPark)
		}

		if agentCtx.StopAfterToolBatch != nil && agentCtx.StopAfterToolBatch() {
			result.InnerTurns = inner + 1
			return result, nil
		}

		if l.shouldJudge(agentCtx, inner, false, toolFailure) {
			verdict := l.evaluateJudge(ctx, agentCtx, conv, completion.Content, records, inner)
			switch verdict.Action {
			case JudgeAccept:
				result.FinalText = completion.Content
				result.InnerTurns = inner + 1
				return result, nil
			case JudgeEscalate:
				result.Success = false
				result.FinalText = completion.Content
				result.InnerTurns = inner + 1
				return result, fmt.Errorf("agent loop: judge escalated: %s", verdict.Feedback)
			default:
				if err := injectJudgeFeedback(conv, verdict); err != nil {
					result.Success = false
					return result, err
				}
			}
		}

		if _, err := l.injectQueuedInput(reminderCtx, conv); err != nil {
			return TurnResult{Success: false, InnerTurns: inner + 1, ToolResults: result.ToolResults}, err
		}
	}

	return TurnResult{Success: false, InnerTurns: totalIterations, ToolResults: result.ToolResults},
		fmt.Errorf("agent loop: exceeded %d inner turns", limit)
}

// Parity: AgentLoop._run_turn_loop hard-stop advisory (tool_budget reminder).
func (l *AgentLoop) injectToolBudgetStop(reminderCtx *ReminderAgentContext, conv *NodeConversation, deferred, lifetimeBudget, hardLimit int, lifetime bool, names []string) {
	var bodies []string
	if lifetime {
		bodies = append(bodies, fmt.Sprintf(
			"%d tool call(s) were deferred: you reached your cumulative tool-call budget of %d for this whole task (not a per-turn cap). Nothing failed — the deferred calls are listed above with a neutral result.\n\nYou are out of work budget. Next turn is a wind-down: dispatch is restricted to report_to_parent / tracker_upsert / task_update. Consolidate what you have and call report_to_parent now — do not start new work.",
			deferred, lifetimeBudget))
	} else {
		bodies = append(bodies, fmt.Sprintf(
			"%d tool call(s) were deferred: you hit the tool-call budget hard stop of %d calls for this turn-loop. Nothing failed — the deferred calls are listed above with a neutral result.\n\nHitting the hard stop is a strong signal to step back, not to plough on. Next turn: consolidate (batch related work, drop redundant calls), and if the current approach isn't converging, switch to a different approach or consult the user rather than re-issuing the same calls.",
			deferred, hardLimit))
	}
	if l.Hub != nil {
		for _, reminder := range l.Hub.Collect(TOOL_BUDGET_CHECKPOINT, reminderCtx, names, "") {
			if reminder.Body != "" {
				bodies = append(bodies, reminder.Body)
			}
		}
	}
	block := WrapReminder(bodies)
	if block == "" {
		return
	}
	_, _ = conv.AddUserMessage(block, UserMessageOptions{IsSystemReminder: true})
}

// pendingToolCall is one call admitted to execute this iteration. index keeps
// the call's position in the batch so results and events stay in call order.
type pendingToolCall struct {
	index int
	call  ToolCallRequest
}

// executeToolBatch runs the admitted calls. A batch goes concurrent only when
// the config opts in AND every call is explicitly marked ConcurrencySafe;
// otherwise it stays sequential. results is written by index, never appended,
// so concurrent writes cannot reorder the batch.
//
// Parity-exempt: upstream splits tool calls into a concurrent safe batch and
// a serial batch; this Go port keeps the batch together and only goes
// concurrent when every call is explicitly marked safe.
func (l *AgentLoop) executeToolBatch(ctx context.Context, agentCtx *AgentContext, pending []pendingToolCall, results []ToolResult, tools []llm.ToolDef) {
	if len(pending) == 0 {
		return
	}
	if !l.Config.ParallelReadOnlyTools || len(pending) < 2 || !allToolsConcurrencySafe(pending, tools) {
		for _, item := range pending {
			results[item.index] = l.executeTool(ctx, agentCtx, item.call)
		}
		return
	}
	var wg sync.WaitGroup
	for _, item := range pending {
		wg.Add(1)
		go func(item pendingToolCall) {
			defer wg.Done()
			results[item.index] = l.executeTool(ctx, agentCtx, item.call)
		}(item)
	}
	wg.Wait()
}

// allToolsConcurrencySafe reports whether every pending call names a tool the
// batch was advertised with as ConcurrencySafe. An unadvertised name is not
// safe: the model cannot mark a tool safe by asking for it.
func allToolsConcurrencySafe(pending []pendingToolCall, tools []llm.ToolDef) bool {
	if len(tools) == 0 {
		return false
	}
	safe := make(map[string]struct{}, len(tools))
	for _, def := range tools {
		if def.ConcurrencySafe {
			safe[def.Name] = struct{}{}
		}
	}
	for _, item := range pending {
		if _, ok := safe[item.call.Name]; !ok {
			return false
		}
	}
	return true
}

// observeToolBatch consumes a finished batch in call order: it flags failures,
// runs the deterministic doom-loop guard, and publishes completed events. Doing
// this after the batch (rather than inside each goroutine) keeps event and
// reminder order stable even when executeToolBatch ran concurrently.
func (l *AgentLoop) observeToolBatch(agentCtx *AgentContext, pending []pendingToolCall, results []ToolResult) (bool, string, doomLoopPark) {
	toolFailure := false
	doomLoopReminder := ""
	park := doomLoopPark{}
	for _, item := range pending {
		if results[item.index].IsError {
			toolFailure = true
		}
		// Parity-exempt: deterministic doom-loop guard. Upstream's stall
		// check is judge-based; this is the no-model half (same call, same
		// arguments, same result) and it appends a reminder before it parks.
		if warning := l.DoomLoop.Observe(item.call.Name, item.call.Input); warning != "" {
			doomLoopReminder = warning
			l.bumpBy("doom_loop_warned", 1)
		}
		if !park.parked && l.DoomLoop.ShouldPark() {
			park = doomLoopPark{tool: item.call.Name, streak: l.DoomLoop.Streak(), parked: true}
		}
		if agentCtx.Observer != nil {
			agentCtx.Observer.ToolCallCompleted(item.call, results[item.index])
		}
	}
	return toolFailure, doomLoopReminder, park
}

// Parity: AgentLoop._execute_tool (the non-background, non-preflight subset).
func (l *AgentLoop) executeTool(ctx context.Context, agentCtx *AgentContext, req ToolCallRequest) ToolResult {
	if len(agentCtx.SkillDirs) > 0 {
		if result, ok := InterceptSkillFileRead(req.Name, req.Input, agentCtx.SkillDirs); ok {
			return result
		}
	}
	if result, handled := l.executeTieredTool(agentCtx, req); handled {
		return result
	}
	// Pace every real execution (skill-file reads and the local tool-tier
	// loader above are local and free).
	// A rejection becomes a retryable result; the tool never runs.
	if result, blocked := l.paceToolCall(ctx, req.Name); blocked {
		return result
	}
	if req.Name == "terminal_job_logs" && truthy(req.Input["wait_until_exit"]) {
		return l.applyToolResultSpillover(l.waitForJob(ctx, req), req.Name)
	}
	return l.applyToolResultSpillover(l.callExecutor(ctx, req), req.Name)
}

// Parity: AgentLoop._resolve_tool_timeout.
func (l *AgentLoop) ResolveToolTimeout(toolName string) float64 {
	overrides := l.Config.ToolTimeoutOverrides
	if overrides == nil {
		overrides = map[string]float64{"browser_": 180.0}
	}
	best := ""
	for prefix := range overrides {
		if strings.HasPrefix(toolName, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		return overrides[best]
	}
	if l.Config.ToolCallTimeoutSeconds > 0 {
		return l.Config.ToolCallTimeoutSeconds
	}
	return 60
}

// Parity: AgentLoop._execute_tool_inner (execute_tool with the resolved
func (l *AgentLoop) callExecutor(ctx context.Context, req ToolCallRequest) ToolResult {
	if l.ToolExecutor == nil {
		return ToolResult{Content: fmt.Sprintf("No executor registered for tool '%s'", req.Name), IsError: true}
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}
	timeout := l.ResolveToolTimeout(req.Name)
	if timeout <= 0 {
		result, err := l.ToolExecutor(req)
		if err != nil {
			return ToolResult{Content: err.Error(), IsError: true}
		}
		return result
	}

	type outcome struct {
		result ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := l.ToolExecutor(req)
		done <- outcome{result: result, err: err}
	}()
	timer := time.NewTimer(time.Duration(timeout * float64(time.Second)))
	defer timer.Stop()
	select {
	case out := <-done:
		if out.err != nil {
			return ToolResult{Content: out.err.Error(), IsError: true}
		}
		return out.result
	case <-ctx.Done():
		return ToolResult{Content: ctx.Err().Error(), IsError: true}
	case <-timer.C:
		return ToolResult{Content: fmt.Sprintf(
			"Tool '%s' timed out after %ds and was abandoned. The call may still be "+
				"finishing on the tool server; do not kill, restart, or inspect any "+
				"processes. Recommended: wait ~30s, retry once with a smaller/simpler "+
				"request; if it times out again, report the timeout and move on to "+
				"other work.", req.Name, int(timeout)), IsError: true}
	}
}

// Parity: AgentLoop._wait_for_job. The wait ends early when a message arrives.
func (l *AgentLoop) waitForJob(ctx context.Context, req ToolCallRequest) ToolResult {
	inputs := req.Input
	deadline := l.clock().Add(time.Duration(l.waitBudget(inputs["wait_timeout_sec"]) * float64(time.Second)))
	statusCheck := ToolCallRequest{
		ID:    req.ID,
		Name:  req.Name,
		Input: map[string]any{"job_id": inputs["job_id"], "max_bytes": 0},
	}
	for l.clock().Before(deadline) && !l.InputWaiting() {
		if jobStatus(l.callExecutor(ctx, statusCheck)) != "running" {
			break
		}
		select {
		case <-ctx.Done():
			return ToolResult{Content: ctx.Err().Error(), IsError: true}
		case <-time.After(l.waitTick()):
		}
	}
	readInputs := make(map[string]any, len(inputs)+1)
	for key, value := range inputs {
		readInputs[key] = value
	}
	readInputs["wait_until_exit"] = false
	return l.callExecutor(ctx, ToolCallRequest{ID: req.ID, Name: req.Name, Input: readInputs})
}

func (l *AgentLoop) waitBudget(requested any) float64 {
	limit := l.waitLimit()
	seconds, ok := asFloat(requested)
	if !ok {
		return limit
	}
	if seconds < 0 {
		seconds = 0
	}
	if seconds > limit {
		seconds = limit
	}
	return seconds
}

func jobStatus(result ToolResult) string {
	var payload any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		return ""
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	status, _ := object["status"].(string)
	return status
}

func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%f", &f); err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return true
	}
}

func parseToolInput(arguments string) map[string]any {
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return map[string]any{}
	}
	if input == nil {
		return map[string]any{}
	}
	return input
}

func toolCallDicts(calls []llm.ToolCall) []map[string]any {
	if len(calls) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		out = append(out, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	return out
}

func toChatMessages(messages []map[string]any) []llm.ChatMessage {
	out := make([]llm.ChatMessage, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		chat := llm.ChatMessage{Role: role, Content: contentString(message["content"])}
		if id, ok := message["tool_call_id"].(string); ok {
			chat.ToolCallID = id
		}
		if rawCalls, ok := message["tool_calls"].([]map[string]any); ok {
			for _, rawCall := range rawCalls {
				chat.ToolCalls = append(chat.ToolCalls, chatToolCall(rawCall))
			}
		}
		out = append(out, chat)
	}
	return out
}

func chatToolCall(raw map[string]any) llm.ToolCall {
	call := llm.ToolCall{}
	if id, ok := raw["id"].(string); ok {
		call.ID = id
	}
	if function, ok := raw["function"].(map[string]any); ok {
		call.Name, _ = function["name"].(string)
		call.Arguments, _ = function["arguments"].(string)
	}
	return call
}

func contentString(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []map[string]any:
		return joinContentBlocks(value)
	case []any:
		blocks := make([]map[string]any, 0, len(value))
		for _, item := range value {
			if block, ok := item.(map[string]any); ok {
				blocks = append(blocks, block)
			}
		}
		return joinContentBlocks(blocks)
	default:
		return fmt.Sprint(value)
	}
}

func joinContentBlocks(blocks []map[string]any) string {
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text, ok := block["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

// NewReminderAgentContext is the AgentContext -> reminder-source view the hub
// binds to. Exported so the server session runtime can start the hub's ticker
// with the same context the loop binds at its first turn.
//
// Parity: the AgentContext attributes ReminderHub sources reach for
func NewReminderAgentContext(agentCtx *AgentContext) *ReminderAgentContext {
	if agentCtx == nil {
		return &ReminderAgentContext{}
	}
	out := &ReminderAgentContext{
		IsQueenStream:              agentCtx.IsQueenStream(),
		SearchableToolsProvider:    searchableToolsFrom(agentCtx.SearchableToolsProvider),
		LoadedToolNamesProvider:    stringSliceProvider(agentCtx.LoadedToolNamesProvider),
		QueenSkillsCatalogProvider: stringProvider(agentCtx.QueenSkillsCatalogProvider),
		AvailableTools:             reminderToolsFromDefs(agentCtx.AvailableTools),
		DynamicToolsProvider:       reminderToolsFromProvider(agentCtx.DynamicToolsProvider),
		OpenTasksProvider:          agentCtx.OpenTasksProvider,
		SessionGoalProvider:        agentCtx.SessionGoalProvider,
		SentinelAutopilotProvider:  agentCtx.SentinelAutopilotProvider,
		ActiveWorkersProvider:      activeWorkersFrom(agentCtx.ActiveWorkersProvider),
		ColonyStatsProvider:        colonyStatsFrom(agentCtx.ColonyStatsProvider),
	}
	if agentCtx.SessionID != nil {
		out.SessionID = *agentCtx.SessionID
	}
	if agentCtx.ColonyID != nil {
		out.ColonyID = *agentCtx.ColonyID
	}
	return out
}

func reminderToolsFromProvider(provider any) func() []Tool {
	switch p := provider.(type) {
	case nil:
		return nil
	case func() []Tool:
		return p
	case []Tool:
		fixed := append([]Tool(nil), p...)
		return func() []Tool { return fixed }
	case func() []llm.ToolDef:
		return func() []Tool { return reminderToolsFromDefs(p()) }
	case []llm.ToolDef:
		fixed := append([]llm.ToolDef(nil), p...)
		return func() []Tool { return reminderToolsFromDefs(fixed) }
	default:
		return nil
	}
}

func activeWorkersFrom(provider any) func() []ActiveWorker {
	switch p := provider.(type) {
	case nil:
		return nil
	case func() []ActiveWorker:
		return p
	case []ActiveWorker:
		fixed := append([]ActiveWorker(nil), p...)
		return func() []ActiveWorker { return fixed }
	case func() []map[string]any:
		return func() []ActiveWorker {
			rows := p()
			out := make([]ActiveWorker, 0, len(rows))
			for _, row := range rows {
				out = append(out, ActiveWorker{
					ID:     stringValue(row["worker_id"]),
					Status: stringValue(row["status"]),
					Task:   stringValue(row["task"]),
				})
			}
			return out
		}
	default:
		return nil
	}
}

func colonyStatsFrom(provider any) func() ColonyStats {
	switch p := provider.(type) {
	case nil:
		return nil
	case func() ColonyStats:
		return p
	case ColonyStats:
		fixed := p
		return func() ColonyStats { return fixed }
	case func() map[string]any:
		return func() ColonyStats {
			stats := p()
			active, _ := stats["active"].(int)
			total, _ := stats["total"].(int)
			if value, ok := asFloat(stats["active"]); ok {
				active = int(value)
			}
			if value, ok := asFloat(stats["total"]); ok {
				total = int(value)
			}
			return ColonyStats{Active: active, Total: total}
		}
	default:
		return nil
	}
}

// stringValue is the lenient string reader providers share.
func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func searchableToolsFrom(provider any) func() []Tool {
	switch p := provider.(type) {
	case func() []Tool:
		return p
	case []Tool:
		fixed := append([]Tool(nil), p...)
		return func() []Tool { return fixed }
	case func() []llm.ToolDef:
		return func() []Tool {
			return reminderToolsFromDefs(p())
		}
	case []llm.ToolDef:
		fixed := append([]llm.ToolDef(nil), p...)
		return func() []Tool { return reminderToolsFromDefs(fixed) }
	default:
		return nil
	}
}

func reminderToolsFromDefs(defs []llm.ToolDef) []Tool {
	out := make([]Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, Tool{Name: def.Name, Description: def.Description})
	}
	return out
}

func stringSliceProvider(provider any) func() []string {
	switch p := provider.(type) {
	case func() []string:
		return p
	case []string:
		fixed := append([]string(nil), p...)
		return func() []string { return fixed }
	default:
		return nil
	}
}

func stringProvider(provider any) func() string {
	switch p := provider.(type) {
	case func() string:
		return p
	case string:
		return func() string { return p }
	default:
		return nil
	}
}

func modelName(agentCtx *AgentContext) string {
	if namer, ok := agentCtx.LLM.(llm.ModelNamer); ok {
		return namer.ModelName()
	}
	if agentCtx.AgentSpec.Model != nil {
		return *agentCtx.AgentSpec.Model
	}
	return ""
}

// storeToolBatch rebuilds the per-call records for a finished tool batch and
// adds every result to the conversation, appending the reminder tail to the
// last non-parking call. Extracted from ExecuteTurn so the turn body stays
// within the repo's function-size budget.
func (l *AgentLoop) storeToolBatch(conv *NodeConversation, toolCalls []llm.ToolCall, results []ToolResult, tail string) ([]ToolCallRecord, error) {
	records := make([]ToolCallRecord, 0, len(toolCalls))
	for i, tc := range toolCalls {
		records = append(records, ToolCallRecord{
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: parseToolInput(tc.Arguments),
			Content:   results[i].Content,
			IsError:   results[i].IsError,
		})
	}
	targetID := ""
	if tail != "" {
		for i := len(toolCalls) - 1; i >= 0; i-- {
			if toolCalls[i].Name != "ask_user" && toolCalls[i].Name != "escalate" {
				targetID = toolCalls[i].ID
				break
			}
		}
	}
	for i, tc := range toolCalls {
		stored := results[i].Content
		if tail != "" && tc.ID == targetID {
			stored = stored + "\n\n" + tail
		}
		opts := ToolResultOptions{
			IsError:        results[i].IsError,
			IsSkillContent: results[i].IsSkillContent,
		}
		if results[i].SpilloverPath != "" {
			spillover := results[i].SpilloverPath
			opts.SpilloverPath = &spillover
		}
		if _, err := conv.AddToolResult(tc.ID, stored, opts); err != nil {
			return records, err
		}
		records[i].Content = stored
	}
	return records, nil
}

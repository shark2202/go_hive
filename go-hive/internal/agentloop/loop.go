package agentloop

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hiveclock"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// Parity: framework/llm/provider.py ToolUse (id/name/input).
type ToolCallRequest struct {
	ID    string
	Name  string
	Input map[string]any
}

type ToolExecutor func(ToolCallRequest) (ToolResult, error)

type ToolCallRecord struct {
	ToolUseID      string
	ToolName       string
	ToolInput      map[string]any
	Content        string
	IsError        bool
	StartTimestamp string
	DurationS      float64
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
	// PendingColonySuggestion is the payload of a successful suggest_colony
	// intercept (DM popup): {colony_id, reason}. Non-nil means the turn parked
	// on the user (host park reason colony_suggestion) and the host must emit
	// COLONY_SUGGESTION_REQUESTED with this payload.
	PendingColonySuggestion map[string]any
	// PendingColonyPivot is the payload of a successful
	// task_create(new_colony=true) intercept: {goal, handoff, tasks,
	// source_phase}. It is the rich stash the accept path seeds the new
	// colony from; the emitted event carries goal/handoff/task_count only.
	PendingColonyPivot map[string]any
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

	// OnStreamStallNudge, when set, observes every continue-nudge the loop
	// injected after a stream watchdog trip. The chat runtime publishes the
	// stream-level diagnostic events from it; nil is a no-op.
	OnStreamStallNudge func(nudge StreamStallNudge)

	// OnContextCompacted, when set, observes every compaction the loop
	// completed with the upstream CONTEXT_COMPACTED payload. The chat runtime
	// publishes the event on the session bus, whose reflection subscriber
	// starts one long memory pass before the compacted turns are forgotten.
	// Nil is a no-op.
	//
	// Parity: framework/agent_loop/internals/compaction.py::log_compaction
	OnContextCompacted func(payload ContextCompacted)

	// OnContextCompactionStarted, when set, observes the start of every
	// compaction pass with upstream's CONTEXT_COMPACTION_STARTED payload. The
	// chat runtime publishes it so the UI can show a multi-minute compaction
	// as busy instead of frozen. Nil is a no-op.
	//
	// Parity: framework/agent_loop/internals/compaction.py::compact
	OnContextCompactionStarted func(payload ContextCompactionStarted)

	// DoomLoop watches for identical consecutive tool calls and returns one
	// reminder per streak. The per-call guard keeps the P0-14 colony contract
	// (warn 3 / escalate / park 5).
	DoomLoop *DoomLoopDetector
	// DoomWindow is the turn-level fingerprint window from upstream's 6f'
	// guard: it catches a repeated *batch* shape ([A, B] every turn) that never
	// builds a per-call streak. It runs alongside DoomLoop; when the per-call
	// guard warns, the window is cleared for that turn so one repetition is
	// never reported twice.
	//
	// Parity: framework/agent_loop/internals/stall_detector.py::is_tool_doom_loop
	DoomWindow *DoomLoopWindow

	// VisionCaptioner, when set, captions the image content of tool results for
	// a main model that cannot accept image blocks: the caption (with viewport
	// coordinates) replaces the images so the model can act on what the tool
	// saw. Nil keeps images flowing to the model as before.
	VisionCaptioner VisionCaptioner

	// ParkSink, when set, observes every park the loop raises itself (a doom
	// loop past its park threshold) with the reason and a one-line detail. The
	// server wires it to the session event bus so the park is published instead
	// of ending in a log line. Nil is a no-op.
	ParkSink func(reason ParkReason, detail string)

	// NodeStalledSink, when set, publishes the response-text stall guard's
	// node_stalled signal. It is separate from ParkSink: upstream publishes
	// node_stalled without parking the loop, and the host maps this callback
	// onto the same session bus event. Nil is a no-op.
	NodeStalledSink NodeStalledSink

	// DoomLoopSink, when set, observes the first tool doom-loop detection of
	// each turn-loop (the streak that crossed the warning threshold) with the
	// repeating tool and its streak. The colony runtime wires it to the colony
	// event bus so the detection is published as node_tool_doom_loop alongside
	// the ESCALATION_REQUESTED the same detection raises. Nil is a no-op.
	DoomLoopSink DoomLoopSink

	// ReplayEvents, when set, publishes tool_call_replay_detected for a
	// cross-turn repeat the soft replay tier noticed (the call still runs).
	// eventbus.EventBus satisfies the interface; nil keeps the loop silent.
	// The host wires the same session/colony bus its other events use.
	ReplayEvents ReplayEventWriter

	// ToolProber and ToolKiller are the loop's handles onto the tool executor's
	// shared MCP connection pool: the executor closures hosts pass to
	// NewAgentLoop cannot carry methods, so the probe/kill pair is wired here
	// instead. On a tool timeout the loop probes first and only force-
	// disconnects a server that is provably dead (or unprobeable), which is
	// exactly upstream's tool_result_handler policy — reclaiming a hung MCP
	// connection without killing a merely-slow shared transport. Nil is the
	// pre-wiring behaviour: probe reads "unknown", nothing is disconnected.
	//
	// Parity: framework/agent_loop/internals/tool_result_handler.py
	// (probe_for_tool / kill_for_tool on the tool executor)
	ToolProber tools.LivenessProber
	ToolKiller tools.ToolKiller

	now func() time.Time

	mu      sync.Mutex
	queue   []injectedInput
	pending []string
	unread  bool
	bound   bool
	// inputReady wakes a loop parked by an escalate call the moment guidance
	// is injected; signalInputReady lazily creates it so a zero loop works.
	inputReady chan struct{}
	// queenInputRequested mirrors upstream's loop-local
	// queen_input_requested: the last tool batch escalated and the turn must
	// park before its next model call until the queen replies.
	queenInputRequested bool
	// queenInputRequestID is the request the park belongs to (diagnostics).
	queenInputRequestID string
	// doomLoopEscalated latches the one automatic doom-loop escalation this
	// turn-loop may publish: the detector already warns once per streak, and
	// the latch keeps a worker that thrashes through several streaks from
	// flooding the queen's inbox. Reset at the next turn boundary.
	doomLoopEscalated bool
	// doomLoopReported latches the node_tool_doom_loop publication for the same
	// reason: the event describes "this turn hit a doom loop", not every later
	// streak of it. Reset at the next turn boundary.
	doomLoopReported bool
	// compactionFailures counts consecutive LLM-compaction failures; at
	// DefaultMaxConsecutiveCompactionFailures the summarizer is skipped until a
	// later pass succeeds (upstream's module-level _failure_counts). Guarded by
	// l.mu because a host may stop/resume a turn from another goroutine.
	compactionFailures int
	// lastCompactionAt is the previous successful compaction, for upstream's
	// recompaction-chain warning.
	lastCompactionAt time.Time
	// workerTextOnlyStreak counts consecutive text-only worker turns in this
	// turn-loop; any turn that issues a tool call resets it. Reset at the turn
	// boundary so a parked/resumed worker starts a fresh grace window.
	workerTextOnlyStreak int
	// silentToolStreak counts consecutive turns that issued tool calls with no
	// assistant text (upstream's _silent_tool_streak). Every multiple of
	// SilentToolStreakThreshold injects the narration nudge; assistant text
	// resets it. Per turn-loop, as upstream's loop-local counter is.
	silentToolStreak int
	// consecutiveEmptyTurns counts consecutive model calls that produced no
	// text, no tool calls and no output (upstream's _consecutive_empty_turns).
	// The stall-detection threshold parks the turn on EMPTY_RESPONSES. Per
	// turn-loop, as upstream's loop-local counter is.
	consecutiveEmptyTurns int

	stateMu       sync.Mutex
	toolCallsUsed int
	// cursorIteration is the 0-based event-loop iteration index the last
	// checkpoint reached (upstream's cursor "iteration"). It advances once per
	// inner turn and is restored from the cursor, so a resumed run continues
	// the max_iterations budget instead of refilling it. cursorIterationKnown
	// records whether the cursor actually carried the field: a legacy cursor
	// that predates it has no information about consumed iterations, so the
	// loop treats it as fresh (start 0) rather than inventing one.
	cursorIteration      int
	cursorIterationKnown bool
	// restoredNodeID and restoredOutputs are the cursor fields upstream's
	// OutputAccumulator.restore reads back. The conversation stays the Go
	// port's source of truth for set_output values, so restoredOutputs only
	// fills keys the transcript can no longer reconstruct (for example after a
	// compaction dropped the set_output pair) when the next checkpoint is
	// written.
	restoredNodeID  string
	restoredOutputs map[string]any
	// resumeDoomWindow is the one-shot signal set by RestoreCursorState when
	// the cursor carried a non-empty fingerprint window: the next turn
	// boundary keeps it (the resumed mid-turn continuation) instead of
	// clearing it; every later turn clears as usual.
	resumeDoomWindow    bool
	sessionCostUSD      float64
	sessionTokensUsed   int
	counters            map[string]int
	inGrace             bool
	graceStartIteration *int
	graceAnnounced      bool
	runtimeLogState     runtimeLogState

	spillMu      sync.Mutex
	spillCounter int

	// bgMu guards the background-tool registry. The counter and map are
	// loop-scoped (upstream's _bg_counter / _background_calls), mutex-guarded
	// here because a parallel read-only batch can start and collect
	// concurrently.
	bgMu            sync.Mutex
	backgroundCalls map[string]*backgroundCall
	bgCounter       int
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
		DoomWindow:   NewDoomLoopWindow(),
	}
	loop.spillCounter = restoreSpillCounter(config.SpilloverDir)
	return loop
}

func (l *AgentLoop) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return hiveclock.Now()
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
	if isClientInput && content != "" {
		l.pending = append(l.pending, content)
	}
	l.queue = append(l.queue, injectedInput{
		content:  content,
		isClient: isClientInput,
		images:   images,
	})
	l.mu.Unlock()
	// Wake a loop parked on an escalation: the guidance it is waiting for just
	// arrived. Non-blocking, so an unwatched signal never wedges a caller.
	l.signalInputReady()
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

// compactionCircuitOpen reports whether consecutive failures have tripped the
// LLM-compaction breaker.
func (l *AgentLoop) compactionCircuitOpen() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.compactionFailures >= DefaultMaxConsecutiveCompactionFailures
}

// recompactionChainDetected reports whether this compaction follows the last
// successful one within upstream's warning window.
func (l *AgentLoop) recompactionChainDetected() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.lastCompactionAt.IsZero() && l.clock().Sub(l.lastCompactionAt) < RecompactionWarningWindow
}

// recordCompactionFailure increments the consecutive-failure counter.
func (l *AgentLoop) recordCompactionFailure() {
	l.mu.Lock()
	l.compactionFailures++
	l.mu.Unlock()
	l.bumpBy(compactionFailureCounter, 1)
}

// recordCompactionSuccess resets the breaker and records the success time.
func (l *AgentLoop) recordCompactionSuccess() {
	l.mu.Lock()
	l.compactionFailures = 0
	l.lastCompactionAt = l.clock()
	l.mu.Unlock()
}

// writeCompactionReport is the HIVE_COMPACTION_DEBUG sink: best-effort, and a
// missing HIVE_HOME is the only silent skip.
func writeCompactionReport(agentCtx *AgentContext, ratioBefore, ratioAfter float64, inventory []map[string]any) {
	home, err := hivehome.Resolve()
	if err != nil {
		return
	}
	level := CompactionLevel(ratioBefore, ratioAfter)
	WriteCompactionDebugLog(home.Root, agentCtx.AgentID, agentCtx.AgentSpec.Name, agentCtx.StreamID,
		ratioBefore, ratioAfter, level, inventory)
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

// BuildInitialMessage mirrors upstream _build_initial_message: every non-nil
// input_data entry, in insertion order, then the declared InputKeys that are
// not already covered, then the Goal line.
//
// A Go map cannot carry insertion order, so InputDataOrder carries it when the
// producer knows it. Keys absent from that list are emitted afterwards in
// sorted order, which keeps the historical deterministic fallback.
//
// Parity: framework/agent_loop/agent_loop.py::_build_initial_message
func BuildInitialMessage(agentCtx *AgentContext) string {
	if agentCtx == nil {
		return ""
	}
	parts := make([]string, 0, len(agentCtx.InputData)+1)
	seen := make(map[string]struct{}, len(agentCtx.InputData))
	emit := func(key string) {
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		value, ok := agentCtx.InputData[key]
		if !ok || value == nil {
			return
		}
		parts = append(parts, fmt.Sprintf("%s: %v", key, value))
		seen[key] = struct{}{}
	}
	for _, key := range agentCtx.InputDataOrder {
		emit(key)
	}
	remaining := make([]string, 0, len(agentCtx.InputData))
	for key := range agentCtx.InputData {
		if _, ok := seen[key]; !ok {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	for _, key := range remaining {
		emit(key)
	}
	for _, key := range agentCtx.AgentSpec.InputKeys {
		emit(key)
	}
	if agentCtx.GoalContext != "" {
		parts = append(parts, "\nGoal: "+agentCtx.GoalContext)
	}
	return strings.Join(parts, "\n")
}

// bindReminders binds the hub once per loop and resets per-turn source state
// on every call — the loop invokes it at the start of each turn, which is where
// upstream resets its stream-stall source.
func (l *AgentLoop) bindReminders(agentCtx *ReminderAgentContext) {
	l.mu.Lock()
	already := l.bound
	l.bound = true
	l.mu.Unlock()
	if l.Hub == nil {
		return
	}
	l.Hub.ResetTurn()
	if already {
		return
	}
	l.Hub.Bind(agentCtx)
}

// ensureInitialMessage fires SESSION_START and materialises the first user
// message (the built task frame, or a bare Hello) on an empty transcript.
func (l *AgentLoop) ensureInitialMessage(agentCtx *AgentContext, conv *NodeConversation, reminderCtx *ReminderAgentContext) error {
	if conv.MessageCount() != 0 {
		return nil
	}
	initial := BuildInitialMessage(agentCtx)
	l.fireReminder(SESSION_START, reminderCtx, conv, initial)
	if initial == "" {
		initial = "Hello"
	}
	if _, err := conv.AddUserMessage(fmt.Sprintf("[%s] %s", l.stamp(), initial), UserMessageOptions{}); err != nil {
		return err
	}
	return nil
}

// Parity: AgentLoop.execute bringup + _run_turn_loop (single outer iteration).
func (l *AgentLoop) ExecuteTurn(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, tools []llm.ToolDef) (out TurnResult, outErr error) {
	defer func() {
		out.ToolCallsUsed = l.ToolCallsUsed()
		// The turn boundary is the last checkpoint: every return path
		// (answer, error, park, cancellation) leaves the iteration counter,
		// node id, outputs, cumulative counter and fingerprint window on disk.
		l.PersistCursorStateFor(conv, agentCtx)
	}()

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
	// says nothing about this one, and neither does its escalation park. The
	// one exception is a window restored from the cursor: the first turn after
	// a cold start is the interrupted turn's continuation, and upstream
	// resumes it with its recent_tool_fingerprints intact.
	l.DoomLoop.Reset()
	if l.DoomWindow != nil {
		if l.takeResumeDoomWindow() {
			l.persistDoomWindow(conv)
		} else {
			l.clearDoomWindow(conv)
		}
	}
	l.clearQueenInputRequested()
	// The worker stall grace window is per turn-loop: guidance that resumed a
	// parked worker must not inherit the streak that parked it.
	l.workerTextOnlyStreak = 0
	// The silent-tool and empty-response streaks are loop-local in upstream's
	// execute(), so a resumed turn starts both from zero.
	l.silentToolStreak = 0
	l.consecutiveEmptyTurns = 0

	l.mu.Lock()
	l.unread = false
	l.doomLoopEscalated = false
	l.doomLoopReported = false
	l.mu.Unlock()

	if err := l.ensureInitialMessage(agentCtx, conv, reminderCtx); err != nil {
		return TurnResult{Success: false}, err
	}

	tools = l.prepareTurnTools(agentCtx, tools)

	model := modelName(agentCtx)
	result := TurnResult{Success: true}
	limit := l.maxIterations()
	graceIterations := l.Config.GraceIterations
	totalIterations := limit + graceIterations
	// softBudgetReminders tracks how many soft checkpoints this turn-loop has
	// already emitted; each new budget multiple escalates by one.
	softBudgetReminders := 0
	// recentResponses is the upstream stall window for this turn-loop. It is
	// restored from the cursor so a resumed worker keeps the same signal.
	recentResponses := l.restoreRecentResponses(conv)
	stallThreshold := l.stallDetectionThreshold()
	stallSimilarity := l.stallSimilarityThreshold()

	startIteration := l.cursorStartIteration()
	for inner := startIteration; inner < totalIterations; inner++ {
		iterStart := time.Now()
		if err := ctx.Err(); err != nil {
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}

		l.stateMu.Lock()
		lifetimeBudget := l.Config.ToolCallLifetimeBudget
		budgetExhausted := lifetimeBudget > 0 && l.toolCallsUsed >= lifetimeBudget
		l.inGrace = inner >= limit || budgetExhausted
		// The index advances at iteration start (upstream's counter); it is
		// flushed when the batch completes and again at the turn boundary.
		l.cursorIteration, l.cursorIterationKnown = inner, true
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
			if err := l.announceGrace(conv, budgetExhausted); err != nil {
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
		// Parity: framework/agent_loop/agent_loop.py::_execute_impl step 6b3
		// (dynamic prompt refresh) — the worker lifecycle supplies the hook.
		l.refreshSystemPrompt(agentCtx, conv)
		l.compactConversationIfNeeded(ctx, agentCtx, conv, reminderCtx)
		completion, requestMessages, err := l.requestCompletion(ctx, agentCtx, conv, model, tools)
		if err != nil {
			// A stalled stream is recoverable: nudge, then re-stream this same
			// inner turn (upstream's continue-nudge recovery path).
			if l.injectStreamStallNudge(err, reminderCtx, conv) {
				inner--
				continue
			}
			// Parity: framework/agent_loop/agent_loop.py::execute (LLM error).
			l.logRuntimeLLMFailure(agentCtx, ctx, err, iterStart, inner, agentCtx.Attempt)
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}
		// Record spend as soon as the completion lands so the hard cap sees
		// it even when the turn later fails or is cancelled.
		l.AddSessionCost(completion.CostUSD)
		// Token telemetry: upstream's AgentResult.tokens_used is the sum of
		// every completion's prompt+completion tokens, and the worker report
		// carries it. Accumulate here, where the completion lands, so a turn
		// that later fails or is cancelled still reports what it burned.
		l.AddSessionTokens(completion.InputTokens, completion.OutputTokens)
		// usage_ratio compares against a single-prompt budget, so it takes
		// this request's prompt size, not the billing sum above.
		l.recordPromptTokenCount(conv, completion)
		// Parity: framework/agent_loop/agent_loop.py::_publish_text_delta. The
		// Go transport is non-streaming, so the complete assistant message is
		// published as one snapshot instead of per-chunk deltas; the event
		// shape is unchanged. llm_turn_complete is NOT published here: upstream
		// publishes it after _run_turn_loop returns, i.e. after the tool batch
		// (see the per-iteration publish below).
		if agentCtx.Observer != nil && completion.Content != "" {
			agentCtx.Observer.AssistantText(completion.Content, completion.Content, inner+1)
		}

		toolCalls := completion.ToolCalls
		callDicts := toolCallDicts(toolCalls)
		if completion.Content != "" || len(callDicts) > 0 {
			assistantOptions := AssistantMessageOptions{}
			if completion.ReasoningContent != "" {
				reasoning := completion.ReasoningContent
				assistantOptions.ReasoningContent = &reasoning
			}
			// Signed reasoning blocks ride the assistant turn so the next
			// request can echo them back. Anthropic rejects a follow-up that
			// drops a signed thinking block, and this is the only place the
			// completion's blocks reach the conversation (the transport
			// projects them back into the request from there).
			//
			// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
			// (FinishEvent.thinking_blocks -> Message(thinking_blocks=...))
			if len(completion.ThinkingBlocks) > 0 {
				assistantOptions.ThinkingBlocks = completion.ThinkingBlocks
			}
			if _, err := conv.AddAssistantMessage(completion.Content, callDicts, assistantOptions); err != nil {
				return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
			}
		}

		// 6e'''. Empty-response guard. Upstream runs this before its stall,
		// doom-loop and judge steps, and a truly empty turn never appends to
		// the similarity window. A queued user message makes the turn a real
		// one again (the next model call reads it); otherwise the guard nudges
		// the model, and at the stall-detection threshold it parks the turn
		// with EMPTY_RESPONSES instead of reporting a completed turn.
		//
		// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e'''
		// empty response guard)
		if len(toolCalls) == 0 && completion.Content == "" {
			if l.InputWaiting() {
				injected, injectErr := l.injectQueuedInput(reminderCtx, conv)
				if injectErr != nil {
					return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, injectErr
				}
				l.mu.Lock()
				l.unread = injected
				l.mu.Unlock()
				if injected {
					continue
				}
			}
			done, emptyErr := l.settleEmptyTurn(agentCtx, conv, completion, inner, tools, requestMessages, &result)
			if emptyErr != nil {
				result.Success = false
				result.InnerTurns = inner + 1
				return result, emptyErr
			}
			if done {
				return result, nil
			}
			continue
		}

		// 6f. Response-text stall detection. This is the upstream n-gram
		// guard, not the tool-fingerprint doom loop below: append the same
		// assistant text the loop stores, keep the configured window, and
		// fail the turn when every consecutive pair is similar enough.
		recentResponses = append(recentResponses, completion.Content)
		if len(recentResponses) > stallThreshold {
			recentResponses = recentResponses[len(recentResponses)-stallThreshold:]
		}
		l.persistRecentResponses(conv, recentResponses)
		if IsResponseStalled(recentResponses, stallThreshold, stallSimilarity) {
			return l.responseStallEndsTurn(agentCtx, completion, inner, iterStart, &result)
		}

		names := make([]string, 0, len(toolCalls))
		for _, tc := range toolCalls {
			names = append(names, tc.Name)
		}
		if l.Hub != nil {
			l.Hub.ObserveTurn(names)
		}

		if len(toolCalls) == 0 {
			done, err := l.settleTextOnlyTurn(ctx, agentCtx, conv, reminderCtx, requestMessages, completion, inner, tools, &result, iterStart)
			if err != nil {
				result.Success = false
				result.InnerTurns = inner + 1
				return result, err
			}
			if done {
				return result, nil
			}
			continue
		}

		// Any turn that issued tool calls is progress; it resets the worker's
		// text-only streak (upstream resets on real_tool_results/outputs_set).
		l.workerTextOnlyStreak = 0

		injected, err := l.injectQueuedInput(reminderCtx, conv)
		if err != nil {
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}
		l.mu.Lock()
		l.unread = injected
		l.mu.Unlock()

		softInterval, hardLimit := l.toolBudgetLimits()
		batch := l.admitToolCalls(agentCtx, toolCalls, tools, inGrace, lifetimeBudget, hardLimit)
		// Admission is where tool_calls_used changes; checkpoint it before
		// any call runs so a crash mid-batch cannot refill the budget.
		l.persistToolCallsUsed(conv)
		// Replay breaker: a model that re-issued the same (tool, canonical
		// args) past the threshold has the batch refused here, before any
		// side effect runs. A trip clears batch.pending, so the executor below
		// is a no-op and only the stubbed results are stored.
		l.applyReplayBreaker(conv, &batch)

		timings := l.executeToolBatch(ctx, agentCtx, batch.pending, batch.results, tools, len(toolCalls))

		toolFailure, doomLoopReminder, doomPark := l.observeToolBatch(conv, agentCtx, batch.pending, batch.batch, batch.results)
		if batch.replayPark.parked {
			doomPark = batch.replayPark
		}

		for _, item := range batch.pending {
			batch.started[item.index] = true
		}

		l.applyBatchSkips(reminderCtx, conv, batch.results, batch.graceSkippedIdx, batch.deferredIdx, batch.limitHit, batch.lifetimeLimitHit, lifetimeBudget, hardLimit, names)

		var tail string
		tail, softBudgetReminders = l.toolBatchTail(
			reminderCtx, names, batch.toolCallCount, softInterval, hardLimit, softBudgetReminders, doomLoopReminder)
		records, err := l.storeToolBatch(ctx, agentCtx, conv, toolCalls, batch.results, tail, batch.started, timings, batch.coercedInputs)
		if err != nil {
			return TurnResult{Success: false, InnerTurns: inner, ToolResults: result.ToolResults}, err
		}
		// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop —
		// screenshot eviction and mid-turn pruning run after the batch is
		// stored and before the batch-end context readout, so the readout
		// reports the reclaimed size.
		l.maintainContextAfterBatch(conv)
		if observer, ok := agentCtx.Observer.(ToolBatchContextObserver); ok {
			observer.ToolBatchStored()
		}
		// Parity: framework/agent_loop/agent_loop.py::execute — llm_turn_complete
		// follows the stored batch (and its context readouts), before any park
		// or judge the batch triggers.
		if agentCtx.Observer != nil {
			agentCtx.Observer.LLMTurnComplete(completion, inner+1)
		}
		result.ToolResults = append(result.ToolResults, records...)
		l.logLLMTurn(agentCtx, conv, inner+1, requestMessages, completion, callDicts, records, tools)

		if finished, finishErr := l.finishToolBatchTurn(ctx, agentCtx, conv, reminderCtx, completion,
			records, timings, toolFailure, doomPark, batch.colonyPark, batch.pending, batch.results,
			iterStart, inner, &result); finished != nil {
			return *finished, finishErr
		}
		// The batch is stored and the turn continues: checkpoint the iteration
		// the loop just finished before the next model call, so a restart
		// resumes at the next index instead of replaying this one.
		l.persistCursorIteration(conv, agentCtx)
	}

	return TurnResult{Success: false, InnerTurns: totalIterations, ToolResults: result.ToolResults},
		fmt.Errorf("agent loop: exceeded %d inner turns", limit)
}

// refreshSystemPrompt rebuilds the conversation's system prompt from the live
// context when the context supplies a rebuild hook, and swaps it in only when
// the rendered text changed. A stable prompt therefore keeps the cached prefix
// warm across iterations; a catalog reload (ReloadSkills / a queen UI override)
// reaches an in-flight worker on its next model call.
//
// This is the Go side of upstream's dynamic prompt refresh
// (framework/agent_loop/agent_loop.py::_execute_impl step 6b3). Upstream runs
// it whenever any dynamic provider is wired and rebuilds from context when
// dynamic_prompt_provider is absent; the Go hook is explicit because the queen
// chat session owns its static prompt and must not be rebuilt here.
func (l *AgentLoop) refreshSystemPrompt(agentCtx *AgentContext, conv *NodeConversation) {
	if agentCtx == nil || conv == nil || agentCtx.DynamicSystemPromptProvider == nil {
		return
	}
	static, suffix, ok := callSystemPromptProvider(agentCtx.DynamicSystemPromptProvider)
	if !ok {
		return
	}
	if JoinSystemPromptParts(static, suffix) == conv.SystemPrompt() && suffix == conv.SystemPromptDynamicSuffix() {
		return
	}
	conv.UpdateSystemPrompt(static, &suffix)
}

// callSystemPromptProvider calls a prompt provider, recovering a panic the way
// upstream's try/except around its dynamic providers does: a broken provider
// leaves the conversation prompt unchanged, never a dead turn.
func callSystemPromptProvider(provider func() (string, string)) (static, suffix string, ok bool) {
	ok = true
	defer func() {
		if recover() != nil {
			static, suffix, ok = "", "", false
		}
	}()
	static, suffix = provider()
	return static, suffix, ok
}

// llmMessages renders the conversation for the next model call. A
// conversation that already ends on an assistant message gets one continuation
// nudge before it is rendered: the next call must not start on an assistant
// turn.
func (l *AgentLoop) llmMessages(conv *NodeConversation, model string) ([]map[string]any, error) {
	messages := conv.ToLLMMessages(model)
	if len(messages) > 0 && messages[len(messages)-1]["role"] == "assistant" {
		if _, err := conv.AddUserMessage("[Continue working on your current task.]", UserMessageOptions{}); err != nil {
			return nil, err
		}
		messages = conv.ToLLMMessages(model)
	}
	return messages, nil
}

// requestCompletion renders the request messages and asks the provider for one
// completion. It reports the rendered request size to an observer that records
// it (the chat emitter): llm_turn_complete is published after the tool batch,
// and upstream still reports the request it actually sent, so the count must
// be captured before the assistant and tool messages are appended.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (request build)
// Parity: framework/agent_loop/agent_loop.py::execute (_compute_turn_diagnostics(request_messages=...))
func (l *AgentLoop) requestCompletion(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, model string, tools []llm.ToolDef) (llm.Completion, []map[string]any, error) {
	messages, err := l.llmMessages(conv, model)
	if err != nil {
		return llm.Completion{}, nil, err
	}
	if recorder, ok := agentCtx.Observer.(RequestMessageRecorder); ok {
		recorder.NoteRequestMessages(len(messages))
	}
	request := llm.CompletionRequest{
		Messages:  toChatMessages(messages),
		System:    conv.SystemPrompt(),
		Tools:     tools,
		MaxTokens: agentCtx.MaxTokens,
	}
	completion, err := agentCtx.LLM.AComplete(ctx, request)
	return completion, messages, err
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

// parallelReadOnlyCap bounds how many concurrency-safe tool calls run at
// once. Upstream puts an asyncio.Semaphore(_PARALLEL_CAP = 10) around its
// concurrent read-only batch; the port keeps the same ceiling so a large
// fan-out cannot hit MCP servers with unbounded parallelism.
const parallelReadOnlyCap = 10

// executeToolBatch runs the admitted calls. A batch goes concurrent only when
// the config opts in AND every call is explicitly marked ConcurrencySafe;
// otherwise it stays sequential. results is written by index, never appended,
// so concurrent writes cannot reorder the batch.
//
// Parity-exempt: upstream splits tool calls into a concurrent safe batch and
// a serial batch; this Go port keeps the batch together and only goes
// concurrent when every call is explicitly marked safe. The in-flight ceiling
// matches upstream's 10.
func (l *AgentLoop) executeToolBatch(ctx context.Context, agentCtx *AgentContext, pending []pendingToolCall, results []ToolResult, tools []llm.ToolDef, nCalls int) []toolTiming {
	timings := make([]toolTiming, nCalls)
	if len(pending) == 0 {
		return timings
	}
	if !l.Config.ParallelReadOnlyTools || len(pending) < 2 || !allToolsConcurrencySafe(pending, tools) {
		for _, item := range pending {
			l.runTimedTool(ctx, agentCtx, item, results, timings)
		}
		return timings
	}
	sem := make(chan struct{}, parallelReadOnlyCap)
	var wg sync.WaitGroup
	for _, item := range pending {
		wg.Add(1)
		go func(item pendingToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			l.runTimedTool(ctx, agentCtx, item, results, timings)
		}(item)
	}
	wg.Wait()
	return timings
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
// runs the deterministic doom-loop guard, and reports the batch's failure state
// to the caller. Tool completion and the trailing llm_turn_complete are
// published by storeToolBatch, after each result has landed in the
// conversation: upstream emits tool_call_completed, the post_tool_call context
// readout and finally llm_turn_complete in that order, and a subscriber must
// see the conversation grow before the readout claims its size.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (per-tool publication)
func (l *AgentLoop) observeToolBatch(conv *NodeConversation, agentCtx *AgentContext, pending []pendingToolCall, batch []ToolCallRequest, results []ToolResult) (bool, string, doomLoopPark) {
	toolFailure := false
	doomLoopReminder := ""
	park := doomLoopPark{}
	// Soft replay tier: a repetition below the breaker threshold still
	// executes, but its stored result carries the steer. This runs before
	// storeToolBatch reads the results by index.
	l.applyReplaySteers(conv, agentCtx, pending, batch, results)
	guarded := make([]ToolCallRequest, 0, len(pending))
	perCallWarned := false
	for _, item := range pending {
		if results[item.index].IsError {
			toolFailure = true
		}
		// Exempt tools (polls, idempotent reads, synthetic control tools) are
		// invisible to the fingerprint guard: they repeat by design, and
		// counting them parks a healthy job poll. Upstream clears its
		// fingerprint window on a turn that carried no guarded call, so an
		// exempt-only batch breaks this chain too.
		if l.Config.ReplayExempt(item.call.Name) {
			continue
		}
		guarded = append(guarded, item.call)
		// Parity-exempt: deterministic doom-loop guard. Upstream's stall
		// check is judge-based; this is the no-model half (same call, same
		// arguments, same result) and it appends a reminder before it parks.
		// The first detection of a streak is also upstream's automatic
		// escalation producer (the one channel parallel workers keep): the
		// loop tells the queen once, then keeps working.
		if warning := l.DoomLoop.Observe(item.call.Name, item.call.Input); warning != "" {
			perCallWarned = true
			doomLoopReminder = warning
			l.bumpBy("doom_loop_warned", 1)
			// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f'
			// tool doom loop -> emit_tool_doom_loop at the same detection that
			// raises the escalation).
			l.reportDoomLoop(item.call.Name, l.DoomLoop.Streak(), doomLoopDescription(item.call.Name, l.DoomLoop.Streak()))
			if notice := l.escalateDoomLoop(agentCtx, item.call.Name, l.DoomLoop.Streak()); notice != "" {
				doomLoopReminder = doomLoopReminder + "\n\n" + notice
			}
		}
		if !park.parked && l.DoomLoop.ShouldPark() {
			park = doomLoopPark{tool: item.call.Name, streak: l.DoomLoop.Streak(), parked: true}
		}
	}
	if len(guarded) == 0 {
		l.DoomLoop.Reset()
		l.clearDoomWindow(conv)
		return toolFailure, doomLoopReminder, park
	}
	// The per-call guard owns a repetition it already warned about; clearing
	// the window keeps one repetition from being reported by both guards.
	if perCallWarned {
		l.clearDoomWindow(conv)
		return toolFailure, doomLoopReminder, park
	}
	// Turn-level window: catches a repeated batch shape the per-call streak
	// cannot see. It is additive to the per-call P0-14 contract above.
	if l.DoomWindow != nil {
		description := l.DoomWindow.ObserveTurn(guarded)
		l.persistDoomWindow(conv)
		if description != "" && !park.parked {
			l.bumpBy("doom_loop_window_detected", 1)
			tool := guarded[0].Name
			streak := l.DoomWindow.Depth()
			l.reportDoomLoop(tool, streak, description)
			park = doomLoopPark{
				tool:        tool,
				streak:      streak,
				parked:      true,
				window:      true,
				description: description,
			}
		}
	}
	return toolFailure, doomLoopReminder, park
}

// batchAskedUser reports whether a finished tool batch successfully called
// ask_user. Upstream raises its loop-local user_input_requested flag on a
// successful call only: a malformed ask_user stays an ordinary tool error, so
// the model gets the message and can correct itself within the same turn.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
func batchAskedUser(pending []pendingToolCall, results []ToolResult) bool {
	for _, item := range pending {
		if item.call.Name == AskUserToolName && !results[item.index].IsError {
			return true
		}
	}
	return false
}

// batchCollectedCredential reports whether a finished tool batch successfully
// called credentials(action="collect"). Upstream raises the same
// user_input_requested flag in the credentials branch as it does for ask_user:
// collect opens the secure form and the turn must end instead of calling the
// model again. attach/detach are ordinary non-blocking results.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (credentials branch)
func batchCollectedCredential(pending []pendingToolCall, results []ToolResult) bool {
	for _, item := range pending {
		if item.call.Name != "credentials" || results[item.index].IsError {
			continue
		}
		action, _ := item.call.Input["action"].(string)
		if strings.ToLower(strings.TrimSpace(action)) == "collect" {
			return true
		}
	}
	return false
}

// batchStopsTurn reports whether a finished tool batch ends the turn before
// the next model call: a successful ask_user parks the host on the user's
// answer (upstream's user_input_requested early return), a successful
// credentials(collect) parks it on the secure form the same way, and a
// worker's explicit report terminates the worker.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (credentials branch)
func batchStopsTurn(agentCtx *AgentContext, pending []pendingToolCall, results []ToolResult) bool {
	if batchAskedUser(pending, results) {
		return true
	}
	if batchCollectedCredential(pending, results) {
		return true
	}
	return agentCtx.StopAfterToolBatch != nil && agentCtx.StopAfterToolBatch()
}

// Parity: AgentLoop._execute_tool (the non-background, non-preflight subset).
func (l *AgentLoop) executeTool(ctx context.Context, agentCtx *AgentContext, req ToolCallRequest) ToolResult {
	// set_output is no longer supported upstream: the live loop rejects it
	// before any executor or framework dispatcher sees the call and stores
	// the error result, so the model learns to report through conversation.
	// The builder/handler remain for the legacy synthetic surface only.
	//
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (set_output branch)
	if req.Name == SetOutputToolName {
		return ToolResult{
			Content: "set_output is no longer available. Report your results via conversation instead.",
			IsError: true,
		}
	}
	// collect_result is the synthetic poll for backgrounded calls: the loop
	// answers it itself, never the executor. Handled first, as upstream does,
	// so tool tiering cannot refuse it as "not loaded".
	//
	// Parity: framework/agent_loop/agent_loop.py::AgentLoop._execute_tool
	if req.Name == CollectResultToolName {
		return l.applyToolResultSpillover(l.collectBackgroundResult(ctx, req), req.Name)
	}
	if len(agentCtx.SkillDirs) > 0 {
		if result, ok := InterceptSkillFileRead(req.Name, req.Input, agentCtx.SkillDirs); ok {
			return result
		}
	}
	if req.Name == EscalateToolName {
		return l.executeEscalateTool(agentCtx, req)
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
	// A background-capable tool runs off the turn's critical path: this
	// returns a bg_* handle (or the real result when the work beats the grace
	// window) and collect_result later hands back the outcome.
	//
	// Parity: framework/agent_loop/agent_loop.py::AgentLoop._execute_tool
	if l.Config.BackgroundTool(req.Name) {
		return l.applyToolResultSpillover(l.startBackgroundTool(ctx, req), req.Name)
	}
	if req.Name == "terminal_job_logs" && truthy(req.Input["wait_until_exit"]) {
		return l.applyToolResultSpillover(l.waitForJob(ctx, req), req.Name)
	}
	return l.applyToolResultSpillover(l.callExecutor(ctx, req), req.Name)
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
		if blocks := multimodalContentBlocks(message["content"]); len(blocks) > 0 {
			chat.ContentParts = blocks
		}
		if id, ok := message["tool_call_id"].(string); ok {
			chat.ToolCallID = id
		}
		if rawCalls, ok := message["tool_calls"].([]map[string]any); ok {
			for _, rawCall := range rawCalls {
				chat.ToolCalls = append(chat.ToolCalls, chatToolCall(rawCall))
			}
		}
		// Reasoning rides assistant steps only; reasoningForThisTurn has
		// already dropped it from earlier turns upstream of this projection.
		if reasoning, ok := message["reasoning_content"].(string); ok {
			chat.ReasoningContent = reasoning
		}
		// Signed reasoning blocks are echoed back verbatim on the assistant
		// step that produced them; the Anthropic transport re-emits them as
		// thinking content blocks ahead of text/tool_use and the OpenAI
		// payload strips the key. Dropping them here reproduces the
		// "signature required" 400 on the follow-up call.
		//
		// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict
		if blocks := mapSlice(message["thinking_blocks"]); len(blocks) > 0 {
			chat.ThinkingBlocks = blocks
		}
		out = append(out, chat)
	}
	return out
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

// storeToolBatch has moved to tool_batch_store.go (loop.go crossed its
// 1500-line freeze budget when the coercion wiring landed; the repo rule is
// to split, never to raise the waiver).

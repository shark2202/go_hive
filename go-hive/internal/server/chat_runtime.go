package server

// Parity: framework/server/routes_execution.py::deliver_to_queen (queen node runtime)

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/queen"
	"github.com/aden-hive/hive-go/internal/sentinel"
	"github.com/aden-hive/hive-go/internal/soul"
)

// chatStreamID and chatNodeID are the upstream queen identifiers: the
// dashboard's session SSE channel only treats a stream as client-facing when
// it is the queen stream.
const (
	chatStreamID = "queen"
	chatNodeID   = "queen"
)

// chatIdentityPrompt is the fallback identity for a chat session whose queen
// has no profile on disk (the shared "default" queen). A bound queen gets the
// formatted profile persona instead — see chatQueenIdentityPrompt.
const chatIdentityPrompt = "You are a queen agent in Hive, working directly with the" +
	" user in an interactive chat session. Answer the user, and use the available" +
	" tools when they help. Prefer doing the work over describing it. Be concise."

var (
	// ErrChatRuntimeNoLLM is returned when a runtime is built without a model.
	ErrChatRuntimeNoLLM = errors.New("server: no LLM configured for chat")
	// ErrChatRuntimeNoToolExecutor guards against advertising tools the
	// session cannot actually run.
	ErrChatRuntimeNoToolExecutor = errors.New("server: chat runtime has tools but no tool executor")
	// ErrChatRuntimeNoStore is returned when neither a conversation store nor
	// a root directory is supplied, which would silently drop the transcript.
	ErrChatRuntimeNoStore = errors.New("server: chat runtime needs a conversation store or root")
	// ErrChatRuntimeClosed is returned by Deliver after Close.
	ErrChatRuntimeClosed = errors.New("server: chat runtime is closed")
)

// ChatRuntimeDeps are injected so internal/server never imports a concrete tool
// package (nor builds providers): the loader owns those adapters.
type ChatRuntimeDeps struct {
	LLM     llm.Completer
	Tools   []llm.ToolDef
	Execute func(ctx context.Context, name string, input map[string]any) (content string, isError bool, err error)
	Store   agentloop.ConversationStore
	Root    string
	// Bus is the session's event bus. When set, the runtime mirrors the
	// upstream agent loop's emissions (execution / loop lifecycle, assistant
	// text, tool calls, per-LLM-call usage) so the dashboard's session SSE
	// channel and /events/history can follow the turn instead of hanging on
	// "Working…" until the next reload.
	Bus *eventbus.EventBus
	// SessionID scopes published execution ids and the session event log.
	SessionID string
	// QueenName scopes recalled memory and reflection to this queen. Empty
	// means the shared "default" queen.
	QueenName string
	// HomeRoot is HIVE_HOME. When set, the runtime recalls global + queen
	// memory each turn and schedules memory reflection after turns.
	HomeRoot string
	// IdleNudge overrides the idle-nudge budgets the session hub starts with.
	// Nil uses the loop's session_idle_nudge_* defaults.
	IdleNudge *agentloop.IdleNudgeConfig
	// SkillsCatalog supplies the queen's rendered skills catalog for the
	// skills reminder. Nil (or an empty return) keeps that source silent.
	SkillsCatalog func() string
	// Colony is the session's live colony runtime. When set (at construction
	// or later through Session.BindChatColony), the queen gains run_worker and
	// every worker report arrives as a [WORKER_REPORT] user turn.
	Colony ColonyRuntime
}

// chatTurnRequest is one queued turn: a real client message, an external
// event (a worker report) the runtime delivers as a non-client user message,
// a temporal reminder the hub parked while the session was idle, or a
// framework trigger (timer tick / webhook hit).
type chatTurnRequest struct {
	message     string
	clientInput bool
	// reminderWake is a message-less turn that only drains reminders the hub
	// parked (idle nudge). It never answers a pending ask_user question.
	reminderWake bool
	// trigger is the framework TriggerEvent that woke this turn. Its payload
	// frames the user message; the event itself is never counted as client
	// input.
	trigger *agentloop.TriggerEvent
}

// ChatRuntime is the per-session queen: one AgentLoop, one conversation, one
// worker goroutine that drains accepted user messages in arrival order.
type ChatRuntime struct {
	deps ChatRuntimeDeps

	loop     *agentloop.AgentLoop
	agentCtx *agentloop.AgentContext
	conv     *agentloop.NodeConversation
	store    agentloop.ConversationStore
	// tools is deps.Tools plus the ask_user synthetic the queen calls when she
	// needs a decision from the user.
	tools []llm.ToolDef

	bus       *eventbus.EventBus
	sessionID string
	model     string
	// streamLLM is non-nil when the configured provider can stream. It wraps
	// deps.LLM for the agent loop and publishes per-frame client_output_delta
	// events; auxiliary callers keep using deps.LLM directly.
	streamLLM *chatStreamingCompleter

	queenName       string
	globalMemoryDir string
	queenMemoryDir  string
	reflection      *memory.ReflectionScheduler
	// lastRecall is the block injected last turn; recall is re-injected only
	// when its content changes (same guard as the upstream _last_injected_recall).
	lastRecall string
	// lastPastRecall is the past-conversation excerpt block injected last
	// turn; the same content is not repeated on the next message.
	lastPastRecall string
	// timelineEnabled is set when the memory_timeline feature is on for this
	// home; timelineAfterTurn then extracts new user turns in the background.
	timelineEnabled bool
	timelineMu      sync.Mutex
	lastTimelineRun time.Time
	// hub is the loop's reminder hub (one per session). Sources are registered
	// once at construction; the temporal idle ticker is started there and
	// stopped by Close.
	hub        *agentloop.ReminderHub
	idleSource *agentloop.IdleNudgeSource
	// unregisterReminders removes this runtime's hub from the REST reminder
	// mirror when the session closes.
	unregisterReminders func()
	// sentinelSource is the parked-queen ladder (nudge / done / escalate)
	// registered on the hub when a process-wide Sentinel manager exists.
	sentinelSource *sentinel.EscalationSource

	// Reminder signals: the ticker reads these through reminderSignals so the
	// idle nudge sees the same park/idle state the upstream loop publishes.
	lastProgress atomic.Int64
	turnActive   atomic.Bool
	firstEvent   atomic.Bool
	awaiting     atomic.Bool
	parkMu       sync.Mutex
	parkReason   agentloop.ParkReason

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	queue   []chatTurnRequest
	closed  bool
	lastErr error

	// colonyMu guards the session colony binding. The binding is late: the
	// chat runtime is built before a session's colony host is attached.
	colonyMu      sync.Mutex
	colonyRuntime *colony.ColonyRuntime
	colonyUnsub   func()
	// colonyBridgeUnsub mirrors the bound colony's lifecycle events onto the
	// session bus; colonyBridgeStream guards against re-subscribing the same
	// runtime on every session lookup.
	colonyBridgeUnsub  func()
	colonyBridgeStream string
	bridgeMu           sync.Mutex
	// pendingQuestions is the ask_user question the current turn parked on.
	// It survives until the next user message (the answer) starts a turn.
	pendingQuestions []map[string]any

	wake chan struct{}
	done chan struct{}
}

func NewChatRuntime(deps ChatRuntimeDeps) (*ChatRuntime, error) {
	if deps.LLM == nil {
		return nil, ErrChatRuntimeNoLLM
	}
	if len(deps.Tools) > 0 && deps.Execute == nil {
		return nil, ErrChatRuntimeNoToolExecutor
	}
	store := deps.Store
	if store == nil {
		if deps.Root == "" {
			return nil, ErrChatRuntimeNoStore
		}
		store = agentloop.NewFileConversationStore(filepath.Join(deps.Root, "conversations"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	runtime := &ChatRuntime{
		deps:      deps,
		store:     store,
		bus:       deps.Bus,
		sessionID: deps.SessionID,
		tools:     chatToolsWithAskUser(deps.Tools),
		ctx:       ctx,
		cancel:    cancel,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	runtime.markProgress()
	runtime.streamLLM = newChatStreamingCompleter(deps.LLM)
	// A session that has not run a turn is parked at turn_done: without this
	// the idle ticker would auto-start an untouched session after the
	// between-turns budget. A persisted pending_input (an unanswered ask_user
	// question, or a broken turn) restores the wait instead of forgetting it.
	if !runtime.restoreParkState() {
		runtime.setParkState(agentloop.ParkReasonTurnDone, true)
	}
	runtime.queenName = strings.TrimSpace(deps.QueenName)
	if runtime.queenName == "" {
		runtime.queenName = "default"
	}
	// Parity: core/framework/server/queen_orchestrator.py::initialize_memory_scopes
	// Memory scopes only exist under a HIVE_HOME; without one the runtime keeps
	// its pre-memory behaviour (no recall, no reflection) instead of guessing.
	if deps.HomeRoot != "" {
		runtime.globalMemoryDir = memory.GlobalMemoryDir(deps.HomeRoot)
		runtime.queenMemoryDir = memory.QueenMemoryDir(deps.HomeRoot, runtime.queenName)
		_ = memory.InitMemoryDir(runtime.globalMemoryDir)
		_ = memory.InitMemoryDir(runtime.queenMemoryDir)
		// Parity: core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
		if deps.Root != "" {
			runtime.reflection = &memory.ReflectionScheduler{
				SessionDir: deps.Root,
				LLM:        deps.LLM,
				GlobalDir:  runtime.globalMemoryDir,
				QueenDir:   runtime.queenMemoryDir,
				QueenID:    runtime.queenName,
				Config:     func() memory.ReflectionConfig { return chatMemoryReflectionConfig(deps.HomeRoot) },
				// A finished pass sediments its conclusions into her soul.
				OnReflect: runtime.sedimentSoul,
			}
			runtime.reflection.InitializeReflectionState()
		}
	}

	loopLLM := deps.LLM
	if runtime.streamLLM != nil {
		loopLLM = runtime.streamLLM
	}
	agentCtx := &agentloop.AgentContext{
		AgentID: "queen",
		AgentSpec: agentloop.AgentSpec{
			ID:        "queen",
			Name:      "Queen",
			AgentType: "event_loop",
		},
		LLM:            loopLLM,
		AvailableTools: runtime.tools,
		IdentityPrompt: chatQueenIdentityPrompt(deps.HomeRoot, runtime.queenName),
		InputData:      map[string]any{},
		StreamID:       "queen",
		MaxTokens:      8192,
	}
	systemPrompt := agentloop.BuildSystemPromptForContext(agentCtx, agentloop.PromptOverrides{})

	conv, err := agentloop.RestoreConversation(store, nil, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	if conv == nil {
		conv = agentloop.NewNodeConversationWithOptions(agentloop.ConversationOptions{
			SystemPrompt: systemPrompt,
			Store:        store,
		})
	} else {
		conv.UpdateSystemPrompt(systemPrompt, nil)
	}

	runtime.agentCtx = agentCtx
	runtime.conv = conv
	runtime.loop = agentloop.NewAgentLoop(chatLoopConfig(deps.Root), runtime.executeTool)
	if deps.Colony != nil {
		runtime.BindColony(deps.Colony)
	}
	// Register the per-session reminder sources and start the hub's idle
	// ticker; Close stops it with the rest of the session.
	runtime.configureReminderProviders()
	// Sentinel registers before the hub binds its agent context: binding
	// filters the consulted sources, so a source added afterwards would never
	// be polled. The autopilot gate it sets is read by that same binding.
	runtime.configureSentinel()
	runtime.configureReminders(deps.IdleNudge)
	runtime.startTimeline()
	// Publish the session's loop hub for the REST reminder mirror. The mirror
	// is read-only; the loop remains the only consumer that drains it.
	runtime.unregisterReminders = registerSessionReminderHub(runtime.sessionID, runtime.hub)

	if namer, ok := deps.LLM.(llm.ModelNamer); ok {
		runtime.model = namer.ModelName()
	}
	// The dashboard's /events/history reads <session dir>/events.jsonl — the
	// same file the bus appends every published event to. Wiring it here keeps
	// the live SSE channel and the reload history consistent.
	if runtime.bus != nil && deps.Root != "" {
		if err := runtime.bus.SetSessionLog(filepath.Join(deps.Root, "events.jsonl"), 0); err != nil {
			log.Printf("chat runtime %s: session event log unavailable: %v", deps.SessionID, err)
		}
	}

	go runtime.run()
	return runtime, nil
}

// Deliver queues a user message and wakes the worker. It never blocks on a
// running turn: turns are serialized by the queue, not by the caller.
func (r *ChatRuntime) Deliver(message string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	return r.enqueueTurn(chatTurnRequest{message: message, clientInput: true})
}

// InjectWorkerReport queues a worker's [WORKER_REPORT] block as the queen's
// next turn. It is delivered as a non-client ("[External event]") user message,
// exactly like the upstream AgentLoop.inject_event(notification) the worker
// report handler calls, and never counted as a real user message.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (inject_event)
func (r *ChatRuntime) InjectWorkerReport(report string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(report) == "" {
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: report, clientInput: false})
}

// InjectEvent queues content as the queen's next turn. A client input is a
// real user message; anything else is delivered as an "[External event]" user
// turn and never counted as a real user message. This is the QueenNode seam
// the session's trigger timers and the Sentinel manager resume path call.
//
// Parity: framework/agent_loop/agent_loop.py::inject_event
func (r *ChatRuntime) InjectEvent(content string, isClientInput bool) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if isClientInput {
		return r.Deliver(content)
	}
	return r.enqueueTurn(chatTurnRequest{message: content, clientInput: false})
}

// InjectTrigger queues a framework trigger (timer tick or webhook hit) as the
// queen's next turn. Triggers are delivered as their own user message, tagged
// is_trigger so the dashboard renders a banner instead of the raw block; they
// are never counted as client input.
//
// Parity: framework/agent_loop/agent_loop.py::inject_trigger
// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func (r *ChatRuntime) InjectTrigger(event agentloop.TriggerEvent) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	pending := event
	if err := r.enqueueTurn(chatTurnRequest{trigger: &pending}); err != nil {
		return err
	}
	// The colony's persistent overseer is the second lead for a bound colony;
	// give it the same trigger so it is not a Queen with no input source.
	r.forwardTriggerToOverseer(pending)
	return nil
}

func (r *ChatRuntime) enqueueTurn(request chatTurnRequest) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrChatRuntimeClosed
	}
	r.queue = append(r.queue, request)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// LastError reports the outcome of the most recent turn (nil after a
// successful turn).
func (r *ChatRuntime) LastError() error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

// Transcript reads the persisted conversation parts from the store.
func (r *ChatRuntime) Transcript() ([]map[string]any, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	return r.store.ReadParts()
}

// Close stops the worker, cancels the in-flight turn, drains any background
// memory reflection (with a final pass so the last turns are persisted), and
// releases the store.
func (r *ChatRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	r.colonyMu.Lock()
	if r.colonyUnsub != nil {
		r.colonyUnsub()
		r.colonyUnsub = nil
	}
	r.colonyRuntime = nil
	r.colonyMu.Unlock()
	r.syncColonyEventBridge(nil)

	r.cancel()
	<-r.done
	// The reminder ticker is owned by the session: stop it before tearing the
	// conversation down so it cannot park (or wake) anything afterwards.
	if r.hub != nil {
		r.hub.Stop()
	}
	if r.unregisterReminders != nil {
		r.unregisterReminders()
		r.unregisterReminders = nil
	}
	// Parity: core/framework/server/session_manager.py::SessionManager.stop_session
	// Teardown cancels the queen's reflection triggers and runs the final
	// reflection: reflections run on the scheduler's own context, so this
	// waits for one already in flight instead of aborting it mid-write.
	if r.reflection != nil {
		r.reflection.Close(memory.ReflectionCloseGrace)
	}
	if r.bus != nil {
		// Flushes any pending delta snapshots and releases the session log fd.
		r.bus.CloseSessionLog()
	}
	if r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *ChatRuntime) run() {
	defer close(r.done)
	for {
		r.mu.Lock()
		if len(r.queue) == 0 {
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-r.wake:
				// A temporal reminder (idle nudge) may have parked while the
				// worker slept. Run it as an energized, message-less turn so
				// the model acts on it instead of leaving it unread.
				if r.hub != nil && r.hub.PendingLen() > 0 {
					r.runTurn(chatTurnRequest{reminderWake: true})
				}
				continue
			case <-r.ctx.Done():
				return
			}
		}
		message := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		r.runTurn(message)
	}
}

// runTurn mirrors the upstream drain: the user message joins the conversation
// stamped with its arrival time, per-queen recall rides along as a
// <system-reminder>, then the loop runs until the model stops calling tools.
// Each step is mirrored onto the session event bus with the upstream
// agent-loop event names so the dashboard follows the turn live. A
// reminderWake turn has no user message: it drains a reminder the idle ticker
// parked and runs the model so the agent acts on it.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input
func (r *ChatRuntime) runTurn(request chatTurnRequest) {
	message := request.message
	if request.reminderWake {
		message = "Reminder"
	}
	executionID := newChatExecutionID()
	emitter := r.newTurnEmitter(executionID)
	if emitter != nil {
		emitter.executionStarted(message)
		r.agentCtx.ExecutionID = executionID
		r.agentCtx.Observer = emitter
		defer func() { r.agentCtx.Observer = nil }()
	}
	// A streaming provider publishes its text as frames while the loop's LLM
	// call is in flight. Without a live bus there is no consumer, so the
	// non-streaming path stays in charge.
	if r.streamLLM != nil && emitter != nil {
		r.streamLLM.beginTurn(r.conv.SystemPrompt(), emitter.StreamTextDelta)
		defer r.streamLLM.endTurn()
	}

	stamp := time.Now().Format("2006-01-02 15:04 MST")
	if request.trigger != nil {
		// A trigger is delivered as upstream's drained trigger batch: the
		// block carries its own timestamp, so it is inserted verbatim rather
		// than taking the "[stamp] [External event]" prefix.
		message = formatTriggerTurn(*request.trigger, stamp)
	}
	r.markTurnStart()
	defer r.markTurnEnd()
	// A real user message or a trigger answers any outstanding ask_user park:
	// both unblock the pending-input wait upstream (`_await_user_input` clears
	// its stash when the wait returns), so clear it before this turn can
	// record a new question. An external report is not an answer, so it
	// leaves the parked question in place.
	if request.clientInput || request.trigger != nil {
		r.clearPendingQuestions()
	}
	if request.clientInput {
		// Parity: framework/agent_loop/agent_loop.py::inject_event — a real
		// user message re-arms the idle nudge's per-variant caps and closes an
		// open Sentinel escalation for this session.
		if r.idleSource != nil {
			r.idleSource.Reset()
		}
		r.notifySentinelLocalResume()
	}
	if request.reminderWake {
		if !r.injectHubReminders() {
			emitter.executionCompleted(0, string(agentloop.ParkReasonTurnDone))
			r.setParkState(agentloop.ParkReasonTurnDone, true)
			return
		}
	} else {
		// Parity: framework/agent_loop/agent_loop.py::execute — SESSION_START
		// fires before the first user message so the situational frame
		// precedes the request.
		if r.conv.MessageCount() == 0 {
			r.fireReminderPoint(agentloop.SESSION_START, message, nil)
		}
		// Parity: framework/agent_loop/agent_loop.py::_drain_injection_queue
		// — USER_PROMPT_SUBMIT fires before the queued user message is added.
		// A trigger is drained separately upstream, so it never fires this
		// point (there is no user prompt to frame).
		if request.trigger == nil {
			r.fireReminderPoint(agentloop.USER_PROMPT_SUBMIT, message, nil)
		}
		stamped := "[" + stamp + "] " + message
		switch {
		case request.trigger != nil:
			// formatTriggerTurn already stamped the block, matching the
			// upstream drain which inserts the batch verbatim.
			stamped = message
		case !request.clientInput:
			stamped = "[" + stamp + "] [External event] " + message
		}
		if _, err := r.conv.AddUserMessage(stamped, agentloop.UserMessageOptions{
			IsClientInput: request.clientInput,
			IsTrigger:     request.trigger != nil,
		}); err != nil {
			emitter.executionFailed(err)
			r.setParkState(agentloop.ParkReasonUnknown, true)
			r.setLastError(err)
			return
		}
		// The ticker may have parked a reminder while the session waited for
		// this message; drain it into the turn before the model reads it.
		r.injectHubReminders()
	}
	// Recall is a user-input step upstream; an external worker report must not
	// consume (or re-query) the queen's memory. Reminders still ride along.
	if request.clientInput {
		r.injectRecall(message, stamp)
		r.injectPastConversationRecall(message, stamp)
	}
	result, err := r.loop.ExecuteTurn(r.ctx, r.agentCtx, r.conv, r.toolsForTurn())
	if err != nil {
		// A cancelled turn (session teardown) is not a failure worth
		// broadcasting: the runtime is going away, not the model failing.
		if r.ctx.Err() == nil {
			emitter.executionFailed(err)
			r.setParkState(agentloop.ParkReasonLLMError, true)
		}
		r.setLastError(err)
		return
	}
	if !result.Success {
		err := errors.New("server: chat turn did not complete")
		emitter.executionFailed(err)
		r.setParkState(agentloop.ParkReasonUnknown, true)
		r.setLastError(err)
		return
	}
	// Parity: framework/agent_loop/agent_loop.py::_await_user_input — a turn
	// that asked the user a question parks on ask_user, not turn_done, so the
	// snapshot and idle nudge treat it as an external wait.
	parkReason := agentloop.ParkReasonTurnDone
	if r.hasPendingQuestions() {
		parkReason = agentloop.ParkReasonAskUser
	}
	emitter.executionCompleted(result.InnerTurns, string(parkReason))
	// Parity: framework/agent_loop/agent_loop.py::execute — STOP fires after a
	// text-only turn (no tool result can carry a tail).
	if len(result.ToolResults) == 0 {
		r.fireReminderPoint(agentloop.STOP, "", nil)
	}
	r.setParkState(parkReason, true)
	// Parity: core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
	if r.reflection != nil {
		r.reflection.OnTurnDone(r.ctx, "queen", "turn_done")
	}
	r.timelineAfterTurn()
	r.setLastError(nil)
}

// newChatExecutionID returns the per-turn execution id the loop and the
// published events carry. Parity: framework/host/execution_manager.py uses a
// fresh uuid per queen execution.
func newChatExecutionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("exec-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// chatTurnEmitter publishes one turn's progress onto the session bus using
// the upstream event names and payload shapes. Nil-receiver methods are
// no-ops so a session without a bus pays no branching cost at each call site.
//
// Parity: framework/agent_loop/internals/event_publishing.py
// Parity: framework/host/execution_manager.py::_run_execution
type chatTurnEmitter struct {
	bus           *eventbus.EventBus
	streamID      string
	nodeID        string
	executionID   string
	model         string
	maxIterations int
	// stream links the emitter to the streaming adapter so the loop's
	// end-of-call AssistantText does not republish text that already went out
	// frame by frame.
	stream *chatStreamingCompleter
}

func (r *ChatRuntime) newTurnEmitter(executionID string) *chatTurnEmitter {
	if r == nil || r.bus == nil {
		return nil
	}
	maxIterations := 0
	if r.loop != nil {
		maxIterations = r.loop.Config.MaxIterations
	}
	return &chatTurnEmitter{
		bus:           r.bus,
		streamID:      chatStreamID,
		nodeID:        chatNodeID,
		executionID:   executionID,
		model:         r.model,
		maxIterations: maxIterations,
		stream:        r.streamLLM,
	}
}

// executionStarted opens the turn: execution_started, an EXECUTING loop state
// (the snapshot derives is_executing from it), and the loop-started boundary.
func (e *chatTurnEmitter) executionStarted(message string) {
	if e == nil {
		return
	}
	e.bus.EmitExecutionStarted(e.streamID, e.executionID,
		map[string]any{"message": message}, nil, nil)
	e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "executing", &e.executionID, nil, nil, nil)
	maxIterations := e.maxIterations
	if maxIterations <= 0 {
		maxIterations = agentloop.DefaultLoopConfig().MaxIterations
	}
	e.bus.EmitNodeLoopStarted(e.streamID, e.nodeID, &e.executionID, &maxIterations)
}

// Parity: framework/agent_loop/agent_loop.py::_publish_iteration
func (e *chatTurnEmitter) IterationStarted(iteration int) {
	if e == nil {
		return
	}
	if e.stream != nil {
		e.stream.noteIteration(iteration)
	}
	e.bus.EmitNodeLoopIteration(e.streamID, e.nodeID, iteration, &e.executionID, nil)
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// (queen streams publish client_output_delta, not llm_text_delta)
func (e *chatTurnEmitter) AssistantText(content, snapshot string, iteration int) {
	if e == nil {
		return
	}
	// The streaming adapter already published this iteration's text frame by
	// frame; republishing the full message would duplicate the bubble.
	if e.stream != nil && e.stream.textStreamed(iteration) {
		return
	}
	e.StreamTextDelta(content, snapshot, iteration)
}

// StreamTextDelta publishes one text frame. It is the sink the streaming
// adapter calls for each upstream increment (Content is that increment and
// Snapshot the accumulated answer).
func (e *chatTurnEmitter) StreamTextDelta(content, snapshot string, iteration int) {
	if e == nil {
		return
	}
	e.bus.EmitClientOutputDelta(e.streamID, e.nodeID, content, snapshot, &e.executionID, &iteration, 0)
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_llm_turn_complete
func (e *chatTurnEmitter) LLMTurnComplete(completion llm.Completion, iteration int) {
	if e == nil {
		return
	}
	model := completion.Model
	if model == "" {
		model = e.model
	}
	e.bus.EmitLLMTurnComplete(e.streamID, e.nodeID,
		completion.StopReason, model,
		completion.InputTokens, completion.OutputTokens,
		completion.CachedTokens, completion.CacheCreationTokens,
		completion.CostUSD, nil,
		&e.executionID, &iteration, nil, nil, nil, nil)
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_started
func (e *chatTurnEmitter) ToolCallStarted(call agentloop.ToolCallRequest) {
	if e == nil {
		return
	}
	e.bus.EmitToolCallStarted(e.streamID, e.nodeID, call.ID, call.Name, call.Input, &e.executionID)
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_completed
func (e *chatTurnEmitter) ToolCallCompleted(call agentloop.ToolCallRequest, result agentloop.ToolResult) {
	if e == nil {
		return
	}
	e.bus.EmitToolCallCompleted(e.streamID, e.nodeID, call.ID, call.Name, result.Content, result.IsError, &e.executionID)
}

// executionCompleted closes a successful turn. loop_state_changed lands last:
// the dashboard clears its "Working…" spinner on it (and on
// execution_completed), and the snapshot then reports idle instead of busy.
// parkReason distinguishes a plain end-of-turn from a turn that parked on an
// ask_user question.
func (e *chatTurnEmitter) executionCompleted(iterations int, parkReason string) {
	if e == nil {
		return
	}
	if parkReason == "" {
		parkReason = string(agentloop.ParkReasonTurnDone)
	}
	e.bus.EmitNodeLoopCompleted(e.streamID, e.nodeID, iterations, &e.executionID)
	e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "awaiting_user", &e.executionID, &parkReason, nil, nil)
	e.bus.EmitExecutionCompleted(e.streamID, e.executionID,
		map[string]any{"status": "completed"}, nil, nil)
}

// executionFailed makes a broken turn visible: execution_failed carries the
// error text (the dashboard renders it as a system bubble from history) and
// the loop state drops out of EXECUTING so the spinner stops.
func (e *chatTurnEmitter) executionFailed(err error) {
	if e == nil || err == nil {
		return
	}
	message := err.Error()
	parkReason := "llm_error"
	e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "interrupted", &e.executionID, &parkReason, nil, nil)
	e.bus.EmitExecutionFailed(e.streamID, e.executionID, message, nil, nil)
}

// injectRecall appends the global + queen-scoped recall blocks to the current
// turn as a <system-reminder>, after the user message and before the model
// reads the conversation. Upstream delivers the same block through
// AgentLoop.inject_event(wake=False), which the drain writes as a non-client
// "[stamp] [External event] ..." user message — not into the system prompt, so
// the cached history prefix stays byte-stable.
//
// Parity: core/framework/server/queen_orchestrator.py::_inject_recall_if_changed
func (r *ChatRuntime) injectRecall(query, stamp string) {
	if r.globalMemoryDir == "" {
		return
	}
	globalBlock, queenBlock := memory.BuildScopedRecallBlocks(
		r.ctx,
		query,
		r.deps.LLM,
		r.globalMemoryDir,
		r.queenMemoryDir,
		r.queenName,
		3,
		3,
		llm.ResolveAuxMaxTokens(0),
	)
	parts := make([]string, 0, 2)
	if globalBlock != "" {
		parts = append(parts, globalBlock)
	}
	if queenBlock != "" {
		parts = append(parts, queenBlock)
	}
	block := strings.Join(parts, "\n\n")
	if block == "" || block == r.lastRecall {
		return
	}
	r.lastRecall = block
	reminder := "<system-reminder>\nRecalled memories relevant to the latest " +
		"user message (supersedes earlier recall reminders):\n\n" +
		block + "\n</system-reminder>"
	// Recall is context, never a reason to fail a turn: a write error leaves
	// the conversation as it was and the turn runs without the reminder.
	_, _ = r.conv.AddUserMessage("["+stamp+"] [External event] "+reminder, agentloop.UserMessageOptions{})
}

// injectHubReminders drains reminders the hub parked (the idle ticker) into the
// conversation as <system-reminder> blocks. It runs on the turn goroutine, so
// conversation writes never race the loop.
//
// Parity: framework/agent_loop/agent_loop.py::_drain_reminder_hub
func (r *ChatRuntime) injectHubReminders() bool {
	if r == nil || r.hub == nil || r.conv == nil {
		return false
	}
	items := r.hub.TakePending()
	if len(items) == 0 {
		return false
	}
	injected := false
	for _, item := range items {
		block := agentloop.WrapReminder([]string{item.Body})
		if block == "" {
			continue
		}
		if _, err := r.conv.AddUserMessage(block, agentloop.UserMessageOptions{IsSystemReminder: true}); err != nil {
			continue
		}
		injected = true
	}
	return injected
}

func (r *ChatRuntime) setLastError(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
}

func (r *ChatRuntime) executeTool(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	// A completed tool call is forward progress: feed the idle clock so the
	// ticker does not nudge a session that is actively working.
	r.markProgress()
	if req.Name == "ask_user" {
		return r.executeAskUser(req.Input), nil
	}
	if r.mcpToolBlocked(req.Name) {
		return mcpToolRefusal(req.Name), nil
	}
	if r.deps.Execute == nil {
		return agentloop.ToolResult{
			Content: fmt.Sprintf("No executor registered for tool '%s'", req.Name),
			IsError: true,
		}, nil
	}
	content, isError, err := r.deps.Execute(r.toolExecuteContext(), req.Name, req.Input)
	if err != nil {
		return agentloop.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return agentloop.ToolResult{Content: content, IsError: isError}, nil
}

// toolsForTurn advertises the session tools for one turn. run_worker is
// colony-gated: without a bound colony the queen does not see the def at all
// (a direct call still returns an actionable no-colony error from the
// executor).
//
// Parity: framework/tools/queen_lifecycle_tools.py::register_queen_lifecycle_tools
// (the run_worker tool only reaches a colony-phase registry)
func (r *ChatRuntime) toolsForTurn() []llm.ToolDef {
	runtime := r.boundColonyRuntime()
	if runtime != nil {
		return filterToolsByColonyAllowlist(runtime, r.tools)
	}
	out := make([]llm.ToolDef, 0, len(r.tools))
	for _, def := range r.tools {
		if def.Name == colony.RunWorkerToolName {
			continue
		}
		out = append(out, def)
	}
	return out
}

// toolExecuteContext carries the bound colony down to the catalogue's
// run_worker executor. The catalogue is built before the binding exists, so
// the runtime is resolved per call rather than captured at construction.
func (r *ChatRuntime) toolExecuteContext() context.Context {
	// Workers belong to the colony, not to this chat turn: drop the runtime's
	// cancellation so a session teardown does not kill in-flight work the
	// colony still owns. The colony's own Stop() remains the lifecycle owner.
	ctx := context.WithoutCancel(r.ctx)
	if runtime := r.boundColonyRuntime(); runtime != nil {
		return colony.WithRunWorkerSpawner(ctx, runtime)
	}
	return ctx
}

// BindColony attaches (or clears) this session's colony runtime. Attaching
// subscribes the queen to the colony's SUBAGENT_REPORT events, which arrive as
// [WORKER_REPORT] turns; detaching unsubscribes. It is idempotent and safe to
// call from session lookup paths.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (subscribe)
func (r *ChatRuntime) BindColony(runtime ColonyRuntime) {
	if r == nil {
		return
	}
	changed := r.bindColony(colonyRuntimeOf(runtime))
	// Mirror worker lifecycle events onto the session bus even when the
	// concrete runtime is unchanged (a session lookup re-invokes BindColony).
	r.syncColonyEventBridge(runtime)
	if changed {
		// The hub filters sources by the bound colony when it binds, so a late
		// attach (create-with-colony, load_colony, fork) must re-bind or the
		// colony-gated sources — parallel nudge, active workers, Sentinel
		// autopilot — would never be consulted.
		r.rebindReminderContext()
	}
}

// bindColony stores the concrete runtime and reports whether it changed.
func (r *ChatRuntime) bindColony(concrete *colony.ColonyRuntime) bool {
	r.colonyMu.Lock()
	defer r.colonyMu.Unlock()
	if r.colonyRuntime == concrete {
		return false
	}
	if r.colonyUnsub != nil {
		r.colonyUnsub()
		r.colonyUnsub = nil
	}
	r.colonyRuntime = concrete
	if concrete == nil {
		r.setBoundColonyID(nil)
		return true
	}
	unsubscribe, err := colony.SubscribeQueenReportsWithWorker(concrete, func(workerID, report string) {
		// Reports never fail a session: a closed runtime (queen session
		// torn down while a worker finished) simply drops the turn.
		_ = r.InjectWorkerReport(report)
		// The persistent overseer is the colony's own lead: give it the same
		// report (except its own) so it is not a tool surface without input.
		r.submitWorkerReportToOverseer(workerID, report)
	})
	if err != nil {
		log.Printf("chat runtime %s: worker report subscription unavailable: %v", r.sessionID, err)
		r.setBoundColonyID(concrete)
		return true
	}
	r.colonyUnsub = unsubscribe
	r.setBoundColonyID(concrete)
	return true
}

// rebindReminderContext re-filters the hub's active sources against the live
// agent context. It runs after the colony binding changes; without it a source
// whose AppliesTo is colony-gated would stay excluded from the bound list.
func (r *ChatRuntime) rebindReminderContext() {
	if r == nil || r.hub == nil || r.agentCtx == nil {
		return
	}
	r.hub.Bind(agentloop.NewReminderAgentContext(r.agentCtx))
}

// setBoundColonyID refreshes the reminder context's colony id. The caller
// holds colonyMu, so this must not call boundColonyRuntime.
func (r *ChatRuntime) setBoundColonyID(runtime *colony.ColonyRuntime) {
	if r == nil || r.agentCtx == nil {
		return
	}
	if runtime == nil || runtime.StreamID() == "" {
		r.agentCtx.ColonyID = nil
		return
	}
	colonyID := runtime.StreamID()
	r.agentCtx.ColonyID = &colonyID
}

// boundColonyRuntime returns the concrete colony runtime whose workers this
// queen can dispatch, or nil when no colony is attached.
func (r *ChatRuntime) boundColonyRuntime() *colony.ColonyRuntime {
	if r == nil {
		return nil
	}
	r.colonyMu.Lock()
	defer r.colonyMu.Unlock()
	return r.colonyRuntime
}

// colonyRuntimeOf unwraps the session ColonyRuntime seam: the CLI's colony
// host exposes Runtime(), and tests may pass the concrete runtime directly.
func colonyRuntimeOf(runtime ColonyRuntime) *colony.ColonyRuntime {
	switch typed := runtime.(type) {
	case nil:
		return nil
	case *colony.ColonyRuntime:
		return typed
	case interface{ Runtime() *colony.ColonyRuntime }:
		return typed.Runtime()
	}
	return nil
}

// chatToolsWithAskUser returns the session's tool list plus the ask_user
// synthetic. The queen's loop is the only client-facing stream, so she always
// gets the question tool — even a session with no integration tools.
//
// Parity: framework/agent_loop/internals/synthetic_tools.py::build_ask_user_tool
func chatToolsWithAskUser(tools []llm.ToolDef) []llm.ToolDef {
	for _, tool := range tools {
		if tool.Name == "ask_user" {
			return tools
		}
	}
	out := make([]llm.ToolDef, 0, len(tools)+1)
	out = append(out, tools...)
	out = append(out, agentloop.BuildAskUserTool())
	return out
}

// chatQueenIdentityPrompt resolves the session queen's persona from her
// profile. Upstream formats the live identity with max_examples=0 so the
// roleplay examples stay out of the system prompt; a queen with no profile on
// disk (the shared "default" queen) keeps the generic chat identity.
//
// Parity: framework/server/queen_orchestrator.py::_queen_identity_hook
// Parity: framework/agents/queen/queen_profiles.py::format_queen_identity_prompt
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
func chatQueenIdentityPrompt(homeRoot, queenName string) string {
	identity := chatIdentityPrompt
	if homeRoot != "" {
		profile, err := queen.LoadQueenProfile(hivehome.Home{Root: homeRoot}.QueensDir(), queenName)
		if err != nil {
			// The shared "default" queen ships no profile; that is expected. A
			// named queen that cannot load is worth one diagnostic line.
			if queenName != "default" {
				log.Printf("chat queen %s: profile unavailable, using the generic identity: %v", queenName, err)
			}
		} else {
			maxExamples := 0
			if prompt := strings.TrimSpace(queen.FormatQueenIdentityPrompt(profile, &maxExamples)); prompt != "" {
				identity = prompt
			}
		}
	}
	// Her soul rides the static prompt right under her identity, as upstream:
	// identity + soul + base prompt. It is not a per-turn reminder because it
	// changes at most once a day and belongs in the cached prefix.
	//
	// Parity: core/framework/agents/queen/soul.py::prompt_for
	if homeRoot != "" {
		home := hivehome.Home{Root: homeRoot}
		if soulPrompt := soul.PromptFor(HiveConfig(home), home.QueensDir(), queenName); soulPrompt != "" {
			identity = identity + "\n\n" + soulPrompt
		}
	}
	return identity
}

// executeAskUser records the question the turn is parking on and announces it
// on the session bus. The next user message is the answer: runTurn clears the
// pending state when it starts, and the turn that produced the question ends
// parked on ask_user.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
func (r *ChatRuntime) executeAskUser(input map[string]any) agentloop.ToolResult {
	questions, problem := normalizeAskUserQuestions(input)
	if problem != "" {
		return agentloop.ToolResult{Content: problem, IsError: true}
	}
	r.mu.Lock()
	r.pendingQuestions = questions
	r.mu.Unlock()
	// Persist immediately: if the process dies before the turn's end checkpoint,
	// the question is still the wait a restarted session must re-enter.
	r.persistParkState()
	if r.bus != nil {
		executionID := r.agentCtx.ExecutionID
		parkReason := string(agentloop.ParkReasonAskUser)
		r.bus.EmitClientInputRequested(chatStreamID, chatNodeID, &executionID, questions, &parkReason)
	}
	return agentloop.ToolResult{Content: "Waiting for user input..."}
}

// normalizeAskUserQuestions applies the same self-healing the upstream
// ask_user branch does: coalesce each question to {id, prompt, options?},
// recover options a model crammed into the prompt, and drop option lists too
// short to be a real choice.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/agent_loop/internals/synthetic_tools.py::sanitize_ask_user_inputs
func normalizeAskUserQuestions(input map[string]any) ([]map[string]any, string) {
	const missingQuestions = "ERROR: ask_user requires a non-empty 'questions' array. " +
		"Each entry must have {id, prompt, options?}. Example: " +
		`{"questions": [{"id": "q1", "prompt": "What now?", "options": ["A", "B"]}]}`
	raw, ok := input["questions"]
	if !ok || raw == nil {
		return nil, missingQuestions
	}
	var entries []any
	switch typed := raw.(type) {
	case []any:
		entries = typed
	case []map[string]any:
		entries = make([]any, len(typed))
		for i, entry := range typed {
			entries[i] = entry
		}
	default:
		return nil, missingQuestions
	}
	if len(entries) == 0 {
		return nil, missingQuestions
	}

	questions := make([]map[string]any, 0, len(entries))
	for i, entry := range entries {
		question, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id := ""
		if rawID, present := question["id"]; present && rawID != nil {
			id = strings.TrimSpace(stringValue(rawID))
			if id == "" {
				id = fmt.Sprint(rawID)
			}
		}
		if id == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		rawPrompt := question["prompt"]
		if rawPrompt == nil {
			rawPrompt = question["question"]
		}
		prompt, recovered := agentloop.SanitizeAskUserInputs(rawPrompt, question["options"])
		options := askUserOptions(question["options"])
		if len(options) == 0 {
			options = recovered
		}
		if len(options) < 2 {
			options = nil
		}
		out := map[string]any{"id": id, "prompt": prompt}
		if len(options) > 0 {
			out["options"] = options
		}
		questions = append(questions, out)
	}
	if len(questions) == 0 {
		return nil, "ERROR: no valid question objects in 'questions'. " +
			"Each entry must be an object with 'id' and 'prompt'."
	}
	return questions, ""
}

// askUserOptions flattens the option shapes models emit into plain strings.
func askUserOptions(raw any) []string {
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := ""
		switch typed := item.(type) {
		case string:
			text = typed
		case map[string]any:
			for _, key := range []string{"label", "value", "text", "name"} {
				if candidate := strings.TrimSpace(stringValue(typed[key])); candidate != "" {
					text = candidate
					break
				}
			}
		case nil:
		default:
			text = fmt.Sprint(typed)
		}
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func (r *ChatRuntime) hasPendingQuestions() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pendingQuestions) > 0
}

func (r *ChatRuntime) clearPendingQuestions() {
	r.mu.Lock()
	r.pendingQuestions = nil
	r.mu.Unlock()
	r.clearParkPendingInput()
}

// sedimentSoul writes a finished reflection's durable conclusions into this
// queen's soul. Only the queen scope is read: global memories are shared by
// every queen, and a soul is one queen's own record. Nothing is written when
// the pass changed no memory, when she has no soul configured, or when her
// soul already carries every conclusion.
//
// Parity-exempt: Go-only sediment seam. Upstream revises a soul in its own
// daily chat-reading reflection (framework/agents/queen/soul_reflection.py);
// the Go port has no such pass and rides the memory reflection instead.
func (r *ChatRuntime) sedimentSoul(result memory.LoopResult) {
	if r == nil || r.deps.HomeRoot == "" || r.queenName == "" {
		return
	}
	home := hivehome.Home{Root: r.deps.HomeRoot}
	cfg := soul.GetSoulConfig(HiveConfig(home), r.queenName)
	if cfg == nil {
		return
	}
	conclusions := memory.ConclusionsFromReflection(result, []memory.ScopeDir{{Scope: "queen", Dir: r.queenMemoryDir}})
	if len(conclusions) == 0 {
		return
	}
	items := make([]soul.SedimentConclusion, 0, len(conclusions))
	for _, conclusion := range conclusions {
		items = append(items, soul.SedimentConclusion{Text: conclusion.Text, Source: conclusion.Filename})
	}
	if _, err := soul.Sediment(soul.SedimentOptions{
		QueensDir:   home.QueensDir(),
		QueenID:     r.queenName,
		Config:      cfg,
		Conclusions: items,
	}); err != nil {
		log.Printf("chat runtime %s: soul sediment skipped: %v", r.sessionID, err)
	}
}

// chatMemoryReflectionConfig reads the memory_reflection section of
// configuration.json, falling back to the same defaults as upstream.
//
// Parity: framework/config.py::get_memory_reflection_config
func chatMemoryReflectionConfig(homeRoot string) memory.ReflectionConfig {
	cfg := memory.DefaultReflectionConfig()
	section, _ := HiveConfig(hivehome.Home{Root: homeRoot})["memory_reflection"].(map[string]any)
	positive := func(key string, fallback int) int {
		value, ok := toFloat(section[key])
		if !ok || value < 1 || value != float64(int(value)) {
			return fallback
		}
		return int(value)
	}
	cfg.EveryTurns = positive("every_turns", cfg.EveryTurns)
	cfg.LongEvery = positive("long_every", cfg.LongEvery)
	if model, ok := section["model"].(string); ok && (model == "main" || model == "worker") {
		cfg.Model = model
	}
	return cfg
}

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
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/queen"
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
	// Reminders is the per-session reminder hub shared with the HTTP routes.
	// Nil disables reminder generation for this runtime.
	Reminders *ReminderRuntime
	// Colony is the session's live colony runtime. When set (at construction
	// or later through Session.BindChatColony), the queen gains run_worker and
	// every worker report arrives as a [WORKER_REPORT] user turn.
	Colony ColonyRuntime
}

// chatTurnRequest is one queued turn: a real client message, or an external
// event (a worker report) the runtime delivers as a non-client user message.
type chatTurnRequest struct {
	message     string
	clientInput bool
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

	queenName       string
	globalMemoryDir string
	queenMemoryDir  string
	reflection      *memory.ReflectionScheduler
	// lastRecall is the block injected last turn; recall is re-injected only
	// when its content changes (same guard as the upstream _last_injected_recall).
	lastRecall string
	reminders  *ReminderRuntime
	// turnTools collects the tool names one turn called, feeding the reminder
	// hub's post_tool_use / stop observations.
	turnTools []string

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
		reminders: deps.Reminders,
		tools:     chatToolsWithAskUser(deps.Tools),
		ctx:       ctx,
		cancel:    cancel,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
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
			}
			runtime.reflection.InitializeReflectionState()
		}
	}

	agentCtx := &agentloop.AgentContext{
		AgentID: "queen",
		AgentSpec: agentloop.AgentSpec{
			ID:        "queen",
			Name:      "Queen",
			AgentType: "event_loop",
		},
		LLM:            deps.LLM,
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
	runtime.loop = agentloop.NewAgentLoop(agentloop.DefaultLoopConfig(), runtime.executeTool)
	if deps.Colony != nil {
		runtime.BindColony(deps.Colony)
	}

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

	r.cancel()
	<-r.done
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
// agent-loop event names so the dashboard follows the turn live.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input
func (r *ChatRuntime) runTurn(request chatTurnRequest) {
	message := request.message
	executionID := newChatExecutionID()
	emitter := r.newTurnEmitter(executionID)
	if emitter != nil {
		emitter.executionStarted(message)
		r.agentCtx.ExecutionID = executionID
		r.agentCtx.Observer = emitter
		defer func() { r.agentCtx.Observer = nil }()
	}

	stamp := time.Now().Format("2006-01-02 15:04 MST")
	r.turnTools = nil
	// A real user message answers any outstanding ask_user: clear the park
	// before this turn can record a new question. An external report is not
	// an answer, so it leaves the parked question in place.
	if request.clientInput {
		r.clearPendingQuestions()
	}
	stamped := "[" + stamp + "] " + message
	if !request.clientInput {
		stamped = "[" + stamp + "] [External event] " + message
	}
	if _, err := r.conv.AddUserMessage(stamped, agentloop.UserMessageOptions{
		IsClientInput: request.clientInput,
	}); err != nil {
		emitter.executionFailed(err)
		r.setLastError(err)
		return
	}
	// Recall is a user-input step upstream; an external worker report must not
	// consume (or re-query) the queen's memory. Reminders still ride along.
	if request.clientInput {
		r.injectRecall(message, stamp)
	}
	r.injectReminders(stamp)
	result, err := r.loop.ExecuteTurn(r.ctx, r.agentCtx, r.conv, r.toolsForTurn())
	if err != nil {
		// A cancelled turn (session teardown) is not a failure worth
		// broadcasting: the runtime is going away, not the model failing.
		if r.ctx.Err() == nil {
			emitter.executionFailed(err)
		}
		r.setLastError(err)
		return
	}
	if !result.Success {
		err := errors.New("server: chat turn did not complete")
		emitter.executionFailed(err)
		r.setLastError(err)
		return
	}
	// Parity: framework/agent_loop/agent_loop.py::_await_user_input — a turn
	// that asked the user a question parks on ask_user, not turn_done, so the
	// snapshot and idle nudge treat it as an external wait.
	parkReason := string(agentloop.ParkReasonTurnDone)
	if r.hasPendingQuestions() {
		parkReason = string(agentloop.ParkReasonAskUser)
	}
	emitter.executionCompleted(result.InnerTurns, parkReason)
	// Parity: framework/agent_loop/reminders.py::observe_turn
	if r.reminders != nil && r.sessionID != "" {
		r.reminders.TurnComplete(r.sessionID, r.turnTools)
	}
	// Parity: core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
	if r.reflection != nil {
		r.reflection.OnTurnDone(r.ctx, "queen", "turn_done")
	}
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
	e.bus.EmitNodeLoopIteration(e.streamID, e.nodeID, iteration, &e.executionID, nil)
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// (queen streams publish client_output_delta, not llm_text_delta)
func (e *chatTurnEmitter) AssistantText(content, snapshot string, iteration int) {
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

// injectReminders drains the session's pending reminders into this turn as a
// system-reminder, after the user message and beside recall. The hub decides
// when a reminder exists; the runtime only delivers it.
//
// Parity: framework/agent_loop/reminders.py::take_pending
func (r *ChatRuntime) injectReminders(stamp string) {
	if r.reminders == nil || r.sessionID == "" {
		return
	}
	block := r.reminders.TakeBlock(r.sessionID)
	if block == "" {
		return
	}
	_, _ = r.conv.AddUserMessage("["+stamp+"] [External event] "+block, agentloop.UserMessageOptions{})
}

func (r *ChatRuntime) setLastError(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
}

func (r *ChatRuntime) executeTool(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	if r.reminders != nil {
		r.turnTools = append(r.turnTools, req.Name)
	}
	if req.Name == "ask_user" {
		return r.executeAskUser(req.Input), nil
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
	if r.boundColonyRuntime() != nil {
		return r.tools
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
	concrete := colonyRuntimeOf(runtime)
	r.colonyMu.Lock()
	defer r.colonyMu.Unlock()
	if r.colonyRuntime == concrete {
		return
	}
	if r.colonyUnsub != nil {
		r.colonyUnsub()
		r.colonyUnsub = nil
	}
	r.colonyRuntime = concrete
	if concrete == nil {
		return
	}
	unsubscribe, err := colony.SubscribeQueenReports(concrete, func(report string) {
		// Reports never fail a session: a closed runtime (queen session
		// torn down while a worker finished) simply drops the turn.
		_ = r.InjectWorkerReport(report)
	})
	if err != nil {
		log.Printf("chat runtime %s: worker report subscription unavailable: %v", r.sessionID, err)
		return
	}
	r.colonyUnsub = unsubscribe
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
func chatQueenIdentityPrompt(homeRoot, queenName string) string {
	if homeRoot == "" {
		return chatIdentityPrompt
	}
	profile, err := queen.LoadQueenProfile(hivehome.Home{Root: homeRoot}.QueensDir(), queenName)
	if err != nil {
		// The shared "default" queen ships no profile; that is expected. A
		// named queen that cannot load is worth one diagnostic line.
		if queenName != "default" {
			log.Printf("chat queen %s: profile unavailable, using the generic identity: %v", queenName, err)
		}
		return chatIdentityPrompt
	}
	maxExamples := 0
	prompt := strings.TrimSpace(queen.FormatQueenIdentityPrompt(profile, &maxExamples))
	if prompt == "" {
		return chatIdentityPrompt
	}
	return prompt
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

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
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

// chatStreamID and chatNodeID are the upstream queen identifiers: the
// dashboard's session SSE channel only treats a stream as client-facing when
// it is the queen stream.
const (
	chatStreamID = "queen"
	chatNodeID   = "queen"
)

// chatIdentityPrompt is the system prompt a chat session's queen starts with.
// The upstream queen prompt is composed from a full queen profile; P0 chat
// sessions have no profile selection yet, so the identity is the shared shape
// the loop and prompt builder expect rather than a roleplay persona.
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
}

// ChatRuntime is the per-session queen: one AgentLoop, one conversation, one
// worker goroutine that drains accepted user messages in arrival order.
type ChatRuntime struct {
	deps ChatRuntimeDeps

	loop     *agentloop.AgentLoop
	agentCtx *agentloop.AgentContext
	conv     *agentloop.NodeConversation
	store    agentloop.ConversationStore

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
	queue   []string
	closed  bool
	lastErr error

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
		AvailableTools: deps.Tools,
		IdentityPrompt: chatIdentityPrompt,
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
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrChatRuntimeClosed
	}
	r.queue = append(r.queue, message)
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
func (r *ChatRuntime) runTurn(message string) {
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
	if _, err := r.conv.AddUserMessage("["+stamp+"] "+message, agentloop.UserMessageOptions{
		IsClientInput: true,
	}); err != nil {
		emitter.executionFailed(err)
		r.setLastError(err)
		return
	}
	r.injectRecall(message, stamp)
	r.injectReminders(stamp)
	result, err := r.loop.ExecuteTurn(r.ctx, r.agentCtx, r.conv, r.deps.Tools)
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
	emitter.executionCompleted(result.InnerTurns)
	// Parity: framework/agent_loop/reminder_hub.py::observe_turn
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
func (e *chatTurnEmitter) executionCompleted(iterations int) {
	if e == nil {
		return
	}
	e.bus.EmitNodeLoopCompleted(e.streamID, e.nodeID, iterations, &e.executionID)
	turnDone := "turn_done"
	e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "awaiting_user", &e.executionID, &turnDone, nil, nil)
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
// Parity: framework/agent_loop/reminder_hub.py::take_pending
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
	if r.deps.Execute == nil {
		return agentloop.ToolResult{
			Content: fmt.Sprintf("No executor registered for tool '%s'", req.Name),
			IsError: true,
		}, nil
	}
	content, isError, err := r.deps.Execute(r.ctx, req.Name, req.Input)
	if err != nil {
		return agentloop.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return agentloop.ToolResult{Content: content, IsError: isError}, nil
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

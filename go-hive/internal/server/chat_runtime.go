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
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
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
		ctx:       ctx,
		cancel:    cancel,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
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

// Close stops the worker, cancels the in-flight turn, and releases the store.
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
// stamped with its arrival time, then the loop runs until the model stops
// calling tools. Each step is mirrored onto the session event bus with the
// upstream agent-loop event names so the dashboard follows the turn live.
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
	if _, err := r.conv.AddUserMessage("["+stamp+"] "+message, agentloop.UserMessageOptions{
		IsClientInput: true,
	}); err != nil {
		emitter.executionFailed(err)
		r.setLastError(err)
		return
	}
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

func (r *ChatRuntime) setLastError(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
}

func (r *ChatRuntime) executeTool(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
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

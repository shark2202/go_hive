package server

// Parity: framework/server/routes_execution.py::deliver_to_queen (queen node runtime)

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
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
}

// ChatRuntime is the per-session queen: one AgentLoop, one conversation, one
// worker goroutine that drains accepted user messages in arrival order.
type ChatRuntime struct {
	deps ChatRuntimeDeps

	loop     *agentloop.AgentLoop
	agentCtx *agentloop.AgentContext
	conv     *agentloop.NodeConversation
	store    agentloop.ConversationStore

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
		deps:   deps,
		store:  store,
		ctx:    ctx,
		cancel: cancel,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
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
// calling tools.
func (r *ChatRuntime) runTurn(message string) {
	stamp := time.Now().Format("2006-01-02 15:04 MST")
	if _, err := r.conv.AddUserMessage("["+stamp+"] "+message, agentloop.UserMessageOptions{
		IsClientInput: true,
	}); err != nil {
		r.setLastError(err)
		return
	}
	result, err := r.loop.ExecuteTurn(r.ctx, r.agentCtx, r.conv, r.deps.Tools)
	if err != nil {
		r.setLastError(err)
		return
	}
	if !result.Success {
		r.setLastError(errors.New("server: chat turn did not complete"))
		return
	}
	r.setLastError(nil)
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

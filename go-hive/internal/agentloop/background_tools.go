package agentloop

// Background tool handles.
//
// A tool named in LoopConfig.BackgroundTools runs off the turn's critical
// path: the call registers a handle and returns {"status":"started",
// "handle":"bg_N"} immediately, and the synthetic collect_result tool waits
// for the work and hands back the real result (or {"status":"pending"} when
// wait_seconds elapses or a new user message arrives first). This is what
// keeps a minutes-long call (image generation, a slow terminal command) from
// tripping the per-call timeout or blocking the conversation.
//
// Upstream registers the handle BEFORE its grace wait so a cancelled turn
// still leaves the work reachable; the Go port keeps that ordering and, since
// ToolExecutor carries no context, detaches the work from the turn's
// cancellation instead of orphaning it. The executor's own budget is
// BackgroundToolTimeoutSeconds, applied by callExecutorWithTimeout.
//
// Parity: framework/agent_loop/agent_loop.py _start_background_tool
// Parity: framework/agent_loop/agent_loop.py _collect_background_result

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const (
	// DefaultBackgroundToolTimeoutSeconds is the real-call budget for a
	// backgrounded tool. Kept below tools.MCPCallResultTimeout, same invariant
	// as the per-tool timeout overrides.
	//
	// Parity: framework/agent_loop/internals/types.py LoopConfig.background_tool_timeout_seconds
	DefaultBackgroundToolTimeoutSeconds = 235.0

	// DefaultBackgroundToolGraceSeconds is the ceiling a background-capable
	// call may run inline before it becomes a handle. Work that finishes
	// inside it returns on the original call and never mints a handle, so a
	// quick image_generate behaves exactly like a foreground tool.
	//
	// Parity: framework/agent_loop/internals/types.py LoopConfig.background_tool_grace_seconds
	DefaultBackgroundToolGraceSeconds = 5.0
)

// DefaultBackgroundTools returns the upstream default background set. A fresh
// map per call keeps a host that mutates its config from mutating every other
// loop's table.
//
// Parity: framework/agent_loop/internals/types.py LoopConfig.background_tools
func DefaultBackgroundTools() map[string]struct{} {
	return map[string]struct{}{
		"image_generate": {},
		"terminal_exec":  {},
	}
}

// BackgroundTool reports whether name runs in the background. A nil table
// means "the upstream defaults" (image_generate, terminal_exec); an explicit
// empty table disables backgrounding, mirroring ReplayExemptTools.
//
// Parity: framework/agent_loop/internals/types.py LoopConfig.background_tools
func (c LoopConfig) BackgroundTool(name string) bool {
	tools := c.BackgroundTools
	if tools == nil {
		tools = DefaultBackgroundTools()
	}
	_, ok := tools[name]
	return ok
}

// HasBackgroundTools reports whether any tool would background. Used to decide
// whether collect_result must be advertised.
//
// Parity: framework/agent_loop/agent_loop.py (build_collect_result_tool append
// gate: `if LoopConfig.background_tools`)
func (c LoopConfig) HasBackgroundTools() bool {
	if c.BackgroundTools == nil {
		return true
	}
	return len(c.BackgroundTools) > 0
}

// BackgroundToolBudget is the real-call timeout for a backgrounded tool. A
// non-positive config falls back to the upstream 235s default.
//
// Parity: framework/agent_loop/internals/types.py LoopConfig.background_tool_timeout_seconds
func (c LoopConfig) BackgroundToolBudget() float64 {
	if c.BackgroundToolTimeoutSeconds > 0 {
		return c.BackgroundToolTimeoutSeconds
	}
	return DefaultBackgroundToolTimeoutSeconds
}

// BackgroundToolGrace is the inline window before a background tool mints a
// handle. Zero (the struct zero value) backgrounds immediately, matching
// upstream's `if grace > 0` gate.
//
// Parity: framework/agent_loop/internals/types.py LoopConfig.background_tool_grace_seconds
func (c LoopConfig) BackgroundToolGrace() time.Duration {
	if c.BackgroundToolGraceSeconds <= 0 {
		return 0
	}
	return time.Duration(c.BackgroundToolGraceSeconds * float64(time.Second))
}

// backgroundCall is one in-flight (or finished-but-uncollected) backgrounded
// tool call. result is written before done is closed; close publishes it.
type backgroundCall struct {
	handle  string
	tool    string
	started time.Time
	done    chan struct{}
	result  ToolResult
}

// ensureCollectResultTool advertises the synthetic collect_result tool
// whenever background tools are configured, so a bg_* handle is always
// collectable. The flat default surface already carries the name; this covers
// worker/grouped loops whose tool list does not.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.execute
func (l *AgentLoop) ensureCollectResultTool(list []llm.ToolDef) []llm.ToolDef {
	if !l.Config.HasBackgroundTools() {
		return list
	}
	for _, def := range list {
		if def.Name == CollectResultToolName {
			return list
		}
	}
	return append(list, BuildCollectResultTool())
}

// registerBackgroundCall claims a handle and records the call BEFORE any work
// starts. Registering first (as upstream does, before its grace await) means a
// turn cancelled inside the grace window cannot orphan a live call: the handle
// is already reachable.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._start_background_tool
func (l *AgentLoop) registerBackgroundCall(tool string) *backgroundCall {
	l.bgMu.Lock()
	defer l.bgMu.Unlock()
	if l.backgroundCalls == nil {
		l.backgroundCalls = make(map[string]*backgroundCall)
	}
	l.bgCounter++
	call := &backgroundCall{
		handle:  fmt.Sprintf("bg_%d", l.bgCounter),
		tool:    tool,
		started: l.clock(),
		done:    make(chan struct{}),
	}
	l.backgroundCalls[call.handle] = call
	return call
}

func (l *AgentLoop) lookupBackgroundCall(handle string) *backgroundCall {
	l.bgMu.Lock()
	defer l.bgMu.Unlock()
	return l.backgroundCalls[handle]
}

// takeBackgroundCall removes and returns a handle. Removal is the claim: the
// first recoverer wins, so a result can never be delivered twice.
func (l *AgentLoop) takeBackgroundCall(handle string) (*backgroundCall, bool) {
	l.bgMu.Lock()
	defer l.bgMu.Unlock()
	call, ok := l.backgroundCalls[handle]
	if !ok {
		return nil, false
	}
	delete(l.backgroundCalls, handle)
	return call, true
}

// finishBackgroundCall publishes a completed result. Closing done under the
// lock pairs with the result write; receivers read result only after <-done.
func (l *AgentLoop) finishBackgroundCall(call *backgroundCall, result ToolResult) {
	l.bgMu.Lock()
	call.result = result
	close(call.done)
	l.bgMu.Unlock()
}

// backgroundToolCount reports the handles currently held (in-flight or
// finished-but-uncollected). Used by diagnostics and tests.
func (l *AgentLoop) backgroundToolCount() int {
	l.bgMu.Lock()
	defer l.bgMu.Unlock()
	return len(l.backgroundCalls)
}

// ReleaseBackgroundTools drops every uncollected handle: the session is
// closing, so no later turn can collect them. ToolExecutor carries no cancel
// hook, so an in-flight call still runs to its own budget and its result is
// discarded. Returns the number of handles dropped.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._background_calls
// (session teardown leaves the registry unreachable)
func (l *AgentLoop) ReleaseBackgroundTools() int {
	l.bgMu.Lock()
	defer l.bgMu.Unlock()
	dropped := len(l.backgroundCalls)
	l.backgroundCalls = nil
	return dropped
}

// runBackgroundCall executes one backgrounded call detached from the turn's
// cancellation and records the outcome for collect_result. A panic is turned
// into an error result instead of a permanently pending handle.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._start_background_tool
// (the detached asyncio task; cancellation does not cancel the work)
func (l *AgentLoop) runBackgroundCall(ctx context.Context, call *backgroundCall, req ToolCallRequest, budget float64) {
	result := ToolResult{IsError: true}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				payload, err := json.Marshal(map[string]any{
					"error": fmt.Sprintf("Background tool '%s' failed: %v", req.Name, recovered),
				})
				if err != nil {
					result = ToolResult{Content: fmt.Sprintf("Background tool '%s' failed: %v", req.Name, recovered), IsError: true}
					return
				}
				result = ToolResult{Content: string(payload), IsError: true}
			}
		}()
		// WithoutCancel keeps the request's values (session id, routing) while
		// letting the call outlive a stopped turn — upstream's detached task.
		result = l.callExecutorWithTimeout(context.WithoutCancel(ctx), req, budget)
	}()
	l.finishBackgroundCall(call, result)
}

// startBackgroundTool dispatches a background-capable call. Fast work returns
// its real result inside the grace window and never mints a handle; slower
// work returns the started envelope as soon as the window expires.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._start_background_tool
func (l *AgentLoop) startBackgroundTool(ctx context.Context, req ToolCallRequest) ToolResult {
	budget := l.Config.BackgroundToolBudget()
	// terminal_exec's own foreground wait must fit inside the background
	// budget, or the call can only ever end in a timeout; upstream refuses it
	// with an instructive error instead.
	if req.Name == "terminal_exec" && terminalForegroundWaitExceeds(req.Input, budget) {
		return ToolResult{
			Content: "Terminal foreground wait exceeds the tool-call budget. Use a shorter " +
				"auto_background_after_sec or terminal_job_start, then poll terminal_job_logs.",
			IsError: true,
		}
	}

	call := l.registerBackgroundCall(req.Name)
	go l.runBackgroundCall(ctx, call, req, budget)

	if grace := l.Config.BackgroundToolGrace(); grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-call.done:
			// Beat the window: hand back the real result and retire the handle,
			// since there is nothing left to collect.
			if finished, ok := l.takeBackgroundCall(call.handle); ok {
				return finished.result
			}
			// A concurrent poll claimed the handle first; it has the result,
			// so this call must not resurrect a dead handle.
			return call.result
		case <-ctx.Done():
			// The turn stopped inside the grace window. The handle is already
			// registered, so the work stays collectable instead of orphaned.
			return ToolResult{Content: ctx.Err().Error(), IsError: true}
		case <-timer.C:
		}
	}

	return ToolResult{Content: startedBackgroundPayload(call)}
}

// collectBackgroundResult answers one collect_result(handle) poll: wait for
// the work, up to wait_seconds (600s ceiling by default), ending early when a
// new message for the agent arrives. An unknown or already-collected handle is
// an error; a pending handle stays registered.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._collect_background_result
func (l *AgentLoop) collectBackgroundResult(ctx context.Context, req ToolCallRequest) ToolResult {
	handle, _ := req.Input["handle"].(string)
	call := l.lookupBackgroundCall(handle)
	if call == nil {
		return ToolResult{Content: unknownHandlePayload(handle), IsError: true}
	}

	deadline := l.clock().Add(time.Duration(l.waitBudget(req.Input["wait_seconds"]) * float64(time.Second)))
	for {
		select {
		case <-call.done:
			finished, ok := l.takeBackgroundCall(handle)
			if !ok {
				// Another collect claimed it between the wake and the take.
				return ToolResult{Content: unknownHandlePayload(handle), IsError: true}
			}
			return finished.result
		default:
		}

		remaining := deadline.Sub(l.clock())
		if remaining <= 0 || l.InputWaiting() {
			return ToolResult{Content: l.pendingBackgroundPayload(call)}
		}
		wait := l.waitTick()
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-call.done:
			timer.Stop()
		case <-ctx.Done():
			// The turn was cancelled mid-wait; the handle survives so a later
			// turn can still collect the result.
			timer.Stop()
			return ToolResult{Content: ctx.Err().Error(), IsError: true}
		case <-timer.C:
		}
	}
}

// Parity: framework/agent_loop/agent_loop.py::AgentLoop._start_background_tool
// (the terminal_exec foreground-wait guard)
func terminalForegroundWaitExceeds(input map[string]any, budget float64) bool {
	timeoutValue, hasTimeout := input["timeout_sec"]
	if !hasTimeout {
		timeoutValue = 60
	}
	autoValue, hasAuto := input["auto_background_after_sec"]
	if !hasAuto {
		autoValue = 30
	}
	timeoutSec, okTimeout := asFloat(timeoutValue)
	autoSec, okAuto := asFloat(autoValue)
	if !okTimeout || !okAuto {
		// Upstream's except branch: an unparseable wait is +inf, never over.
		return false
	}
	inline := math.Inf(1)
	for _, value := range []float64{timeoutSec, autoSec} {
		if value > 0 && value < inline {
			inline = value
		}
	}
	return inline+5 >= budget
}

func startedBackgroundPayload(call *backgroundCall) string {
	payload := map[string]any{
		"status": "started",
		"handle": call.handle,
		"tool":   call.tool,
		"note": fmt.Sprintf(
			"'%s' is running in the background (can take a few minutes). Call "+
				"collect_result(handle=\"%s\") to fetch it: it waits for the work and "+
				"returns the real result, or {\"status\":\"pending\"} if a new message "+
				"for you arrives first.",
			call.tool, call.handle,
		),
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"status":"started","handle":%q,"tool":%q}`, call.handle, call.tool)
	}
	return string(content)
}

func (l *AgentLoop) pendingBackgroundPayload(call *backgroundCall) string {
	payload := map[string]any{
		"status":          "pending",
		"handle":          call.handle,
		"elapsed_seconds": int(l.clock().Sub(call.started).Seconds()),
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"status":"pending","handle":%q}`, call.handle)
	}
	return string(content)
}

func unknownHandlePayload(handle string) string {
	payload := map[string]any{
		"error": fmt.Sprintf(
			"Unknown or already-collected handle: %q. It may have been collected already, or never started.",
			handle,
		),
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":"Unknown or already-collected handle: %q"}`, handle)
	}
	return string(content)
}

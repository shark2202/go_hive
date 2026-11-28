package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ToolCallEvent struct {
	ToolUseID string
	ToolName  string
	ToolInput map[string]any
}

type ContextualExecutor interface {
	Execute(ctx context.Context, toolUse ToolUse) (ToolResult, error)
}

type ExecutorFunc func(ctx context.Context, toolUse ToolUse) (ToolResult, error)

func (f ExecutorFunc) Execute(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
	return f(ctx, toolUse)
}

type LivenessProber interface {
	ProbeForTool(toolName string) string
}

type ToolKiller interface {
	KillForTool(toolName string) error
}

// Timeout liveness verdicts. Upstream's probe_for_tool returns one of these
// strings; "unknown" is also the fallback for an executor that cannot probe.
//
// Parity: framework/agent_loop/internals/tool_result_handler.py (probe verdicts)
const (
	TimeoutVerdictAlive     = "alive"
	TimeoutVerdictResetting = "resetting"
	TimeoutVerdictDead      = "dead"
	TimeoutVerdictUnknown   = "unknown"
)

var (
	executionContextMu      sync.RWMutex
	executionContextCurrent map[string]any
)

type ExecutionContextReset struct{ prev map[string]any }

func SetExecutionContext(values map[string]any) *ExecutionContextReset {
	executionContextMu.Lock()
	defer executionContextMu.Unlock()
	prev := executionContextCurrent
	next := make(map[string]any, len(prev)+len(values))
	for k, v := range prev {
		next[k] = v
	}
	for k, v := range values {
		next[k] = v
	}
	executionContextCurrent = next
	return &ExecutionContextReset{prev: prev}
}

func ResetExecutionContext(token *ExecutionContextReset) {
	if token == nil {
		return
	}
	executionContextMu.Lock()
	executionContextCurrent = token.prev
	executionContextMu.Unlock()
}

func GetExecutionContext() map[string]any {
	executionContextMu.RLock()
	defer executionContextMu.RUnlock()
	if executionContextCurrent == nil {
		return nil
	}
	out := make(map[string]any, len(executionContextCurrent))
	for k, v := range executionContextCurrent {
		out[k] = v
	}
	return out
}

type poolNameKey struct{}

type ToolPool struct {
	name    string
	sem     chan struct{}
	maxSize int
}

func NewToolPool(maxWorkers int, name string) *ToolPool {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if name == "" {
		name = "hive-tool"
	}
	return &ToolPool{name: name, sem: make(chan struct{}, maxWorkers), maxSize: maxWorkers}
}

func (p *ToolPool) Name() string { return p.name }

func (p *ToolPool) MaxWorkers() int { return p.maxSize }

func (p *ToolPool) run(ctx context.Context, fn func(context.Context) (ToolResult, error)) (ToolResult, error) {
	p.sem <- struct{}{}
	defer func() { <-p.sem }()
	return fn(ctx)
}

var (
	defaultPoolMu sync.Mutex
	defaultPool   *ToolPool
)

func ToolExecutorWorkers() int {
	workers := 64
	if raw := strings.TrimSpace(os.Getenv("HIVE_TOOL_EXECUTOR_WORKERS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			workers = n
		}
	}
	if workers < 8 {
		return 8
	}
	return workers
}

func SharedToolPool() *ToolPool {
	defaultPoolMu.Lock()
	defer defaultPoolMu.Unlock()
	if defaultPool == nil {
		defaultPool = NewToolPool(ToolExecutorWorkers(), "hive-tool")
	}
	return defaultPool
}

func SetToolPool(p *ToolPool) {
	defaultPoolMu.Lock()
	defaultPool = p
	defaultPoolMu.Unlock()
}

func ToolPoolNameFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(poolNameKey{}).(string)
	return name
}

func ExecuteTool(ctx context.Context, executor ContextualExecutor, tc ToolCallEvent, timeout time.Duration, skillDirs []string) (ToolResult, error) {
	if executor == nil {
		return ToolResult{
			ToolUseID: tc.ToolUseID,
			Content:   fmt.Sprintf("No tool executor configured for '%s'", tc.ToolName),
			IsError:   true,
		}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if result, handled, err := readSkillResource(tc, skillDirs); handled {
		return result, err
	}

	toolUse := ToolUse{ID: tc.ToolUseID, Name: tc.ToolName, Input: tc.ToolInput}
	pool := SharedToolPool()
	// The derived context is cancelled when this call gives up on the tool.
	// Without it a timed-out executor keeps running (and keeps holding its
	// pool slot) until its own internal deadline, which may be minutes.
	poolCtx, cancel := context.WithCancel(context.WithValue(ctx, poolNameKey{}, pool.Name()))
	defer cancel()

	type outcome struct {
		result ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := pool.run(poolCtx, func(runCtx context.Context) (ToolResult, error) {
			return executor.Execute(runCtx, toolUse)
		})
		done <- outcome{result: res, err: err}
	}()

	if timeout <= 0 {
		select {
		case out := <-done:
			return out.result, out.err
		case <-ctx.Done():
			cancel()
			return cancelledResult(tc, ctx.Err()), nil
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case out := <-done:
		return out.result, out.err
	case <-ctx.Done():
		cancel()
		return cancelledResult(tc, ctx.Err()), nil
	case <-timer.C:
		cancel()
		return timeoutResult(executor, tc, timeout), nil
	}
}

// cancelledResult reports a tool call abandoned because its context ended.
func cancelledResult(tc ToolCallEvent, cause error) ToolResult {
	return ToolResult{
		ToolUseID: tc.ToolUseID,
		Content:   fmt.Sprintf("Tool '%s' was cancelled: %v", tc.ToolName, cause),
		IsError:   true,
	}
}

func readSkillResource(tc ToolCallEvent, skillDirs []string) (ToolResult, bool, error) {
	if tc.ToolName != "view_file" || len(skillDirs) == 0 {
		return ToolResult{}, false, nil
	}
	rawPath, _ := tc.ToolInput["path"].(string)
	if rawPath == "" {
		return ToolResult{}, false, nil
	}
	resolved, err := filepath.Abs(rawPath)
	if err != nil {
		return ToolResult{}, false, nil
	}
	inside := false
	for _, dir := range skillDirs {
		root, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			inside = true
			break
		}
	}
	if !inside {
		return ToolResult{}, false, nil
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return ToolResult{
			ToolUseID: tc.ToolUseID,
			Content:   fmt.Sprintf("Could not read skill resource '%s': %v", rawPath, err),
			IsError:   true,
		}, true, nil
	}
	return ToolResult{
		ToolUseID:      tc.ToolUseID,
		Content:        string(content),
		IsSkillContent: filepath.Base(resolved) == "SKILL.md",
	}, true, nil
}

// TimeoutRecovery applies upstream's tool-timeout recovery policy to the
// shared connection behind toolName: probe liveness first, keep an "alive"
// server (the call was merely slow) and a "resetting" one (recovery already in
// flight), and force-disconnect only a server that failed its probe ("dead",
// or "unknown" for an executor that cannot probe). The probe/kill handles are
// both optional; a nil prober reads as unknown, a nil killer leaves the
// transport up.
//
// It returns the verdict so the caller can render its own result type (the
// agent loop publishes agentloop.ToolResult; ExecuteTool publishes the tools
// one).
//
// Parity: framework/agent_loop/internals/tool_result_handler.py (timeout
// handler: probe_for_tool / kill_for_tool).
func TimeoutRecovery(prober LivenessProber, killer ToolKiller, toolName string) string {
	verdict := TimeoutVerdictUnknown
	if prober != nil {
		verdict = prober.ProbeForTool(toolName)
	}
	switch verdict {
	case TimeoutVerdictAlive, TimeoutVerdictResetting:
		return verdict
	}
	if killer != nil {
		_ = killer.KillForTool(toolName)
	}
	return verdict
}

// TimeoutVerdictContent renders the model-facing message for one timeout
// verdict. The texts are the upstream ones verbatim: an "alive" server must
// steer the agent away from killing processes (2026-06-11 incident), and a
// failed probe must say the runtime is already reconnecting.
//
// Parity: framework/agent_loop/internals/tool_result_handler.py (timeout texts)
func TimeoutVerdictContent(toolName string, timeout time.Duration, verdict string) string {
	secs := int(timeout / time.Second)
	switch verdict {
	case TimeoutVerdictAlive:
		return fmt.Sprintf(
			"Tool '%s' timed out after %ds and the request "+
				"was abandoned. TRANSPORT STATUS: the tool server is RESPONSIVE "+
				"(liveness check passed) — it is still processing a slow operation, or "+
				"another agent's long call is queued ahead of yours on the shared "+
				"connection. This is a client-side timeout, NOT an application or "+
				"browser crash. DO NOT kill, restart, or inspect any processes, and DO "+
				"NOT use terminal commands to 'fix' anything — other agents share this "+
				"connection and manual intervention will break their work. Recommended: "+
				"wait ~30s, retry once with a smaller/simpler request; if it times out "+
				"again, report the timeout and move on to other work.",
			toolName, secs)
	case TimeoutVerdictResetting:
		return fmt.Sprintf(
			"Tool '%s' timed out after %ds. The shared "+
				"connection to its tool server is being reset AUTOMATICALLY by the "+
				"runtime. DO NOT kill or restart any processes and DO NOT attempt "+
				"manual cleanup — recovery is already in progress. Recommended: wait "+
				"30-60s, then retry the tool once; if it still fails, report the "+
				"transport problem and stop using these tools until it recovers.",
			toolName, secs)
	}
	return fmt.Sprintf(
		"Tool '%s' timed out after %ds and its tool server "+
			"failed a liveness check. The runtime is resetting that connection "+
			"AUTOMATICALLY. DO NOT kill, restart, or launch any processes and DO NOT "+
			"attempt manual cleanup — recovery is already in progress. Recommended: "+
			"wait 30-60s, retry the tool once; if it fails again, report the tool "+
			"server outage and move on to work that does not need this tool.",
		toolName, secs)
}

func timeoutResult(executor ContextualExecutor, tc ToolCallEvent, timeout time.Duration) ToolResult {
	var prober LivenessProber
	if candidate, ok := executor.(LivenessProber); ok {
		prober = candidate
	}
	var killer ToolKiller
	if candidate, ok := executor.(ToolKiller); ok {
		killer = candidate
	}
	verdict := TimeoutRecovery(prober, killer, tc.ToolName)
	return ToolResult{
		ToolUseID: tc.ToolUseID,
		Content:   TimeoutVerdictContent(tc.ToolName, timeout, verdict),
		IsError:   true,
	}
}

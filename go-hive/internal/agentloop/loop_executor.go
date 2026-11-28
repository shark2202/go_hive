package agentloop

// Executor path: per-tool timeout resolution, the executor call itself
// (foreground budget or an explicit background budget), and the
// terminal_job_logs wait loop. Split out of loop.go to stay under the
// file-size budget (internal/quality TestFileSizeBudget); behavior is
// unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// Parity: AgentLoop._resolve_tool_timeout.
func (l *AgentLoop) ResolveToolTimeout(toolName string) float64 {
	overrides := l.Config.ToolTimeoutOverrides
	if overrides == nil {
		// Zero-value LoopConfig: keep the same shipped overrides as
		// DefaultLoopConfig.
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
// per-call timeout; the background path passes its own budget).
func (l *AgentLoop) callExecutor(ctx context.Context, req ToolCallRequest) ToolResult {
	return l.callExecutorWithTimeout(ctx, req, l.ResolveToolTimeout(req.Name))
}

// callExecutorWithTimeout runs the executor with an explicit budget. The
// foreground path resolves it per tool; a backgrounded call passes
// BackgroundToolTimeoutSeconds instead.
func (l *AgentLoop) callExecutorWithTimeout(ctx context.Context, req ToolCallRequest, timeout float64) ToolResult {
	if l.ToolExecutor == nil {
		return ToolResult{Content: fmt.Sprintf("No executor registered for tool '%s'", req.Name), IsError: true}
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}
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
		// Parity: framework/agent_loop/internals/tool_result_handler.py
		// (timeout handler). Probe the shared connection before touching it:
		// an alive/resetting server keeps its transport, a dead or
		// unprobeable one is force-disconnected so the hung call — and any
		// MCP subprocess behind it — is reclaimed.
		verdict := tools.TimeoutRecovery(l.ToolProber, l.ToolKiller, req.Name)
		l.bumpBy("tool_timeout", 1)
		return ToolResult{
			Content: tools.TimeoutVerdictContent(req.Name, time.Duration(timeout*float64(time.Second)), verdict),
			IsError: true,
		}
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

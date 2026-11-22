package agentloop

// Tool-call pacing.
//
// rate_limits.tools.* in hive.config.json configures a request budget for tool
// executions. The loop consults its gate before each call reaches the
// executor; a rejection becomes a retryable tool result instead of running the
// tool and hoping the upstream tolerates it. Unconfigured means unlimited: the
// installed gate is a no-op until a bucket exists, and LoopConfig.ToolCallGate
// can be set to a no-op by tests that do not want the process-wide limiter.

import (
	"context"
	"errors"
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/ratelimit"
)

// defaultToolCallGate is what NewAgentLoop installs when LoopConfig leaves the
// gate nil. A variable so tests can observe/replace it without touching the
// per-loop config.
var defaultToolCallGate = ratelimit.AcquireToolCall

const toolRateLimitedMessage = "[Not executed — the %s tool-call rate limit rejected this call: %s " +
	"This is not a tool failure and the call did not run; retry it once the limit clears.]"

// paceToolCall applies the configured tool limit. blocked is true when the
// call must be replaced by the returned retryable result.
//
// Parity-exempt: Go-only execution of the rate_limits section; upstream has no
// tool-call pacing stage.
func (l *AgentLoop) paceToolCall(ctx context.Context, toolName string) (ToolResult, bool) {
	gate := l.Config.ToolCallGate
	if gate == nil {
		return ToolResult{}, false
	}
	if err := gate(ctx, toolName); err != nil {
		l.bumpBy("tool_rate_limited", 1)
		detail := err.Error()
		var limitErr *ratelimit.CallLimitError
		if errors.As(err, &limitErr) {
			detail = fmt.Sprintf("%s (retry in %.1fs)", limitErr.Key, limitErr.RetryAfterSeconds())
		}
		return ToolResult{
			Content: fmt.Sprintf(toolRateLimitedMessage, toolName, detail),
			IsError: true,
		}, true
	}
	return ToolResult{}, false
}

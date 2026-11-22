package agentloop

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/ratelimit"
)

// TestMain keeps the suite hermetic: NewAgentLoop installs the process-wide
// limiter, which would otherwise read the developer's hive.config.json.
//
// Parity-exempt: Go test harness.
func TestMain(m *testing.M) {
	ratelimit.SetCallLimiter(nil)
	os.Exit(m.Run())
}

// Parity-exempt: Go-only execution of rate_limits.tools.*; upstream has no
// tool-call pacing stage.
func TestToolRateLimitRejectionSkipsExecution(t *testing.T) {
	executed := 0
	executor := func(req ToolCallRequest) (ToolResult, error) {
		executed++
		return ToolResult{Content: "ran " + req.Name}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{"path":"a"}`}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations: 3,
		ToolCallGate: func(context.Context, string) error {
			return &ratelimit.CallLimitError{Key: "tools.read_file", RetryAfter: 2 * time.Second, PerMinute: 30}
		},
	}, executor)
	ctx := midturnAgentContext(completer, "read_file")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if executed != 0 {
		t.Fatalf("tool ran %d times despite the rate limit", executed)
	}
	if len(result.ToolResults) != 1 {
		t.Fatalf("tool results = %d, want 1", len(result.ToolResults))
	}
	record := result.ToolResults[0]
	if !record.IsError {
		t.Fatalf("rate-limited call reported success: %q", record.Content)
	}
	for _, want := range []string{"Not executed", "rate limit", "retry in 2.0s"} {
		if !strings.Contains(record.Content, want) {
			t.Fatalf("result %q missing %q", record.Content, want)
		}
	}
	if got := loop.Counter("tool_rate_limited"); got != 1 {
		t.Fatalf("tool_rate_limited = %d, want 1", got)
	}
}

// Parity-exempt: Go-only end-to-end wiring — a configured limiter must reach
// the loop through NewAgentLoop's default gate.
func TestConfiguredToolLimitReachesTheLoop(t *testing.T) {
	ratelimit.SetCallLimiter(ratelimit.NewCallLimiter(
		[]ratelimit.CallLimitSpec{{Key: ratelimit.CallBucketTools, PerMinute: 1}},
		ratelimit.CallLimiterOptions{MaxWait: 0},
	))
	t.Cleanup(func() { ratelimit.SetCallLimiter(nil) })

	executed := 0
	executor := func(req ToolCallRequest) (ToolResult, error) {
		executed++
		return ToolResult{Content: "ran " + req.Name}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"a"}`},
			{ID: "call_2", Name: "read_file", Arguments: `{"path":"b"}`},
		}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, executor)
	ctx := midturnAgentContext(completer, "read_file")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if executed != 1 {
		t.Fatalf("executed %d tools, want 1 (the second call is over the minute budget)", executed)
	}
	if len(result.ToolResults) != 2 {
		t.Fatalf("tool results = %d, want 2", len(result.ToolResults))
	}
	if result.ToolResults[0].IsError {
		t.Fatalf("first call should have run: %q", result.ToolResults[0].Content)
	}
	if !result.ToolResults[1].IsError || !strings.Contains(result.ToolResults[1].Content, "rate limit") {
		t.Fatalf("second call = %q (isError=%v), want a retryable rate-limit result",
			result.ToolResults[1].Content, result.ToolResults[1].IsError)
	}
}

// Parity-exempt: Go-only default wiring.
func TestAgentLoopInstallsDefaultToolGate(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{}, nil)
	if loop.Config.ToolCallGate == nil {
		t.Fatal("NewAgentLoop left the tool gate nil")
	}
	if err := loop.Config.ToolCallGate(context.Background(), "read_file"); err != nil {
		t.Fatalf("unconfigured default gate paced a call: %v", err)
	}
}

// Parity-exempt: Go-only wiring — the gate must see one call per tool
// invocation, including tools that never reach the executor's happy path.
func TestToolGateCalledOncePerCall(t *testing.T) {
	seen := []string{}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{}`}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations: 3,
		ToolCallGate: func(_ context.Context, name string) error {
			seen = append(seen, name)
			return nil
		},
	}, executor)
	ctx := midturnAgentContext(completer, "read_file")
	conv := NewNodeConversation("You are a test assistant.")

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(seen) != 1 || seen[0] != "read_file" {
		t.Fatalf("gate calls = %v, want exactly [read_file]", seen)
	}
}

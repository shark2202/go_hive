package agentloop

// Probes for the 1.0.15 "a long wait costs no model turns" behavior. Upstream
// moved the wait for terminal_job_logs(wait_until_exit=True) and
// collect_result(handle) out of the tool call and into the loop: one-second
// status checks, up to tool_wait_limit_seconds (600 by default), ended by a
// new message for the agent. The poll cost used to be a model turn per repeat.
//
// See docs/releases/v1.0.15.md and
// core/framework/agent_loop/agent_loop.py::_wait_for_job / _wait_budget.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: core/tests/test_background_tools.py::test_a_job_wait_ends_at_its_timeout_or_a_new_message
// (the upstream test pins the shortening; the 600s ceiling itself is
// LoopConfig.tool_wait_limit_seconds in core/framework/agent_loop/internals/types.py).
func TestJobWaitBudgetDefaultsToTenMinutesAndOnlyShortensOnRequest(t *testing.T) {
	if got := DefaultLoopConfig().ToolWaitLimitSeconds; got != 600 {
		t.Fatalf("LoopConfig default tool_wait_limit_seconds = %v, want 600", got)
	}
	loop := NewAgentLoop(DefaultLoopConfig(), nil)
	if got := loop.waitTick(); got != time.Second {
		t.Fatalf("wait tick = %v, want 1s (upstream _WAIT_TICK_S)", got)
	}
	cases := []struct {
		name      string
		requested any
		want      float64
	}{
		{"absent request waits to the ceiling", nil, 600},
		{"an explicit shorter wait shortens", float64(30), 30},
		{"a longer request is capped at the ceiling", float64(1200), 600},
		{"negative clamps to zero", float64(-5), 0},
		{"garbage falls back to the ceiling", "abc", 600},
		{"exactly the ceiling is kept", float64(600), 600},
	}
	for _, tc := range cases {
		if got := loop.waitBudget(tc.requested); got != tc.want {
			t.Fatalf("%s: waitBudget(%#v) = %v, want %v", tc.name, tc.requested, got, tc.want)
		}
	}
	// A zeroed config falls back to the ceiling (the port's "zero means unset"
	// convention, same as MaxIterations). Production profile objects carry the
	// 600s default.
	cfg := DefaultLoopConfig()
	cfg.ToolWaitLimitSeconds = 0
	zeroed := NewAgentLoop(cfg, nil)
	if got := zeroed.waitBudget(nil); got != 600 {
		t.Fatalf("zeroed config: waitBudget(nil) = %v, want the 600s fallback", got)
	}
}

// Parity: core/tests/test_background_tools.py::test_a_job_wait_stays_out_of_the_tool_call_then_reads_the_output
func TestAJobWaitSpendsNoModelTurnPerStatusCheck(t *testing.T) {
	var (
		mu          sync.Mutex
		statusCalls int
		readCalls   int
		readInput   map[string]any
	)
	executor := func(req ToolCallRequest) (ToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if _, isStatusCheck := req.Input["max_bytes"]; isStatusCheck {
			statusCalls++
			status := "running"
			if statusCalls >= 3 {
				status = "exited"
			}
			return ToolResult{Content: `{"status":"` + status + `","data":""}`}, nil
		}
		readCalls++
		readInput = req.Input
		return ToolResult{Content: `{"status":"exited","data":"the output"}`}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "terminal_job_logs", Arguments: `{"job_id":"job_1","wait_until_exit":true}`}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor)
	ctx := midturnAgentContext(completer, "terminal_job_logs")
	conv := NewNodeConversation("You are a test assistant.")

	started := time.Now()
	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	mu.Lock()
	statuses, reads := statusCalls, readCalls
	finalInput := readInput
	mu.Unlock()
	if statuses != 3 || reads != 1 {
		t.Fatalf("executor saw %d status checks and %d reads, want 3 + 1", statuses, reads)
	}
	// Three one-second status checks inside the loop, and the model sees only
	// the two turns it would have had anyway: 0 extra model turns for the polls
	// (each repeat used to be a whole model turn).
	if got := completer.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (the waits must not cost a model turn)", got)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("wait returned after %v; three 1s ticks take at least 2s", elapsed)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("wait took %v, well past the 600s ceiling semantics", elapsed)
	}
	// The model's own read happens after the wait, with nothing left to wait for.
	if finalInput["job_id"] != "job_1" || finalInput["wait_until_exit"] != false {
		t.Fatalf("read inputs = %v, want job_id=job_1 with wait_until_exit=false", finalInput)
	}
	if len(finalInput) != 2 {
		t.Fatalf("read inputs carried %d keys (%v), want exactly 2", len(finalInput), finalInput)
	}
}

// Parity: core/tests/test_background_tools.py::test_a_job_that_cant_be_found_is_reported_without_waiting
func TestAJobThatCantBeFoundIsReportedWithoutWaiting(t *testing.T) {
	var (
		mu     sync.Mutex
		inputs []map[string]any
	)
	executor := func(req ToolCallRequest) (ToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		inputs = append(inputs, req.Input)
		return ToolResult{Content: `{"error":"unknown job_id: job_9"}`}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor)
	agentCtx := midturnAgentContext(completer, "terminal_job_logs")

	started := time.Now()
	result := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID:    "c1",
		Name:  "terminal_job_logs",
		Input: map[string]any{"job_id": "job_9", "wait_until_exit": true},
	})
	elapsed := time.Since(started)

	if elapsed > time.Second {
		t.Fatalf("an unknown job must not wait; took %v", elapsed)
	}
	mu.Lock()
	seen := append([]map[string]any(nil), inputs...)
	mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("executor calls = %d, want 2 (one status check + the read)", len(seen))
	}
	if !strings.Contains(result.Content, "unknown job_id") {
		t.Fatalf("result = %q, want the unknown-job error", result.Content)
	}
}

// Parity-exempt: 上游「task updates ride along with real tool calls」（a7ca0113）
// 由提示词与 provider 参数约束，是否批量由模型决定；Go 侧补探针：一轮响应里的
// task_update + 真实工具多 call 批次只花一个模型回合，两个结果一起回到下一轮。
func TestTaskUpdatesRideAlongWithARealToolInOneResponse(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
	)
	executor := func(req ToolCallRequest) (ToolResult, error) {
		mu.Lock()
		order = append(order, req.Name)
		mu.Unlock()
		if req.Name == "task_update" {
			return ToolResult{Content: `{"status":"updated"}`}, nil
		}
		return ToolResult{Content: `{"status":"tool-out"}`}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "c1", Name: "task_update", Arguments: `{"task_id":1,"status":"completed"}`},
			{ID: "c2", Name: "some_tool", Arguments: `{}`},
		}},
		{content: "done"},
	}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor)
	ctx := midturnAgentContext(completer, "task_update", "some_tool")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	if len(gotOrder) != 2 || gotOrder[0] != "task_update" || gotOrder[1] != "some_tool" {
		t.Fatalf("executor order = %v, want [task_update some_tool] in one batch", gotOrder)
	}
	// One response carried both calls; one model turn covers the batch.
	if got := completer.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (the task update must not spend its own turn)", got)
	}

	second := completer.request(1)
	assistantWithBoth := 0
	for _, message := range second.Messages {
		if len(message.ToolCalls) == 2 {
			assistantWithBoth++
		}
	}
	if assistantWithBoth != 1 {
		t.Fatalf("assistant messages carrying both tool calls = %d, want 1", assistantWithBoth)
	}
	updatedPos := positionOf(second.Messages, "updated")
	toolPos := positionOf(second.Messages, "tool-out")
	if updatedPos < 0 || toolPos < 0 {
		t.Fatalf("second call missing tool results (updated=%d, tool-out=%d)", updatedPos, toolPos)
	}
	if updatedPos >= toolPos {
		t.Fatalf("task_update result at %d should precede the real tool result at %d", updatedPos, toolPos)
	}
}

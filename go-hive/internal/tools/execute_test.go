package tools

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testToolCall(name string, input map[string]any) ToolCallEvent {
	if input == nil {
		input = map[string]any{}
	}
	return ToolCallEvent{ToolUseID: "test_id", ToolName: name, ToolInput: input}
}

// Parity: core/tests/test_tool_executor_isolation.py::test_tools_run_on_dedicated_hive_tool_pool
func TestToolsRunOnDedicatedHiveToolPool(t *testing.T) {
	var seenName string
	var seenMu sync.Mutex

	exec := ExecutorFunc(func(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
		name := ToolPoolNameFromContext(ctx)
		seenMu.Lock()
		seenName = name
		seenMu.Unlock()
		return NewToolResult(toolUse.ID, "ok"), nil
	})

	res, err := ExecuteTool(context.Background(), exec, testToolCall("t", nil), 10*time.Second, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q, want ok", res.Content)
	}
	seenMu.Lock()
	got := seenName
	seenMu.Unlock()
	if !strings.HasPrefix(got, "hive-tool") {
		t.Fatalf("pool name inside executor = %q, want hive-tool*", got)
	}
	if name := SharedToolPool().Name(); !strings.HasPrefix(name, "hive-tool") {
		t.Fatalf("shared pool name = %q, want hive-tool*", name)
	}
}

// Parity: core/tests/test_tool_executor_isolation.py::test_hung_tools_do_not_starve_the_default_pool
func TestHungToolsDoNotStarveTheDefaultPool(t *testing.T) {
	prev := SharedToolPool()
	SetToolPool(NewToolPool(2, "hive-tool-test"))
	defer SetToolPool(prev)

	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	hung := ExecutorFunc(func(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		return NewToolResult(toolUse.ID, "done"), nil
	})

	type outcome struct {
		res ToolResult
		err error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			res, err := ExecuteTool(context.Background(), hung, testToolCall("t", nil), 30*time.Second, nil)
			results <- outcome{res: res, err: err}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("hung tool did not start on the tool pool")
		}
	}

	apiDone := make(chan string, 1)
	start := time.Now()
	go func() { apiDone <- "api-ok" }()
	select {
	case got := <-apiDone:
		if got != "api-ok" {
			t.Fatalf("unrelated work returned %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("default work was blocked by hung tools")
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("default work took %v; tool pool starved it", elapsed)
	}

	close(release)
	for i := 0; i < 2; i++ {
		select {
		case out := <-results:
			if out.err != nil {
				t.Fatalf("hung tool error: %v", out.err)
			}
			if out.res.Content != "done" {
				t.Fatalf("hung tool content = %q, want done", out.res.Content)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("hung tool did not finish after release")
		}
	}
}

// Parity: core/tests/test_tool_context_propagation.py::test_execution_context_propagates_to_tool_executor
func TestExecutionContextPropagatesToToolExecutor(t *testing.T) {
	var captured map[string]any
	exec := ExecutorFunc(func(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
		captured = GetExecutionContext()
		return NewToolResult(toolUse.ID, "ok"), nil
	})

	token := SetExecutionContext(map[string]any{"data_dir": "/tmp/test_data"})
	defer ResetExecutionContext(token)

	res, err := ExecuteTool(context.Background(), exec, testToolCall("test_tool", map[string]any{"arg": "value"}), 10*time.Second, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q, want ok", res.Content)
	}
	if captured == nil {
		t.Fatal("execution context was nil inside the tool executor")
	}
	if got, _ := captured["data_dir"].(string); got != "/tmp/test_data" {
		t.Fatalf("data_dir = %v, want /tmp/test_data", captured["data_dir"])
	}
}

// Parity: core/tests/test_tool_context_propagation.py::test_execution_context_none_when_not_set
func TestExecutionContextNoneWhenNotSet(t *testing.T) {
	ResetExecutionContext(&ExecutionContextReset{})

	var captured map[string]any
	var seen bool
	exec := ExecutorFunc(func(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
		captured = GetExecutionContext()
		seen = true
		return NewToolResult(toolUse.ID, "ok"), nil
	})

	res, err := ExecuteTool(context.Background(), exec, testToolCall("test_tool", nil), 10*time.Second, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("content = %q, want ok", res.Content)
	}
	if !seen {
		t.Fatal("executor never ran")
	}
	if captured != nil {
		t.Fatalf("execution context = %v, want nil", captured)
	}
}

type countingProbeExecutor struct {
	verdict    string
	probeCalls atomic.Int32
	killCalls  atomic.Int32
	release    chan struct{}
}

func (e *countingProbeExecutor) Execute(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
	select {
	case <-e.release:
	case <-time.After(10 * time.Second):
	}
	return NewToolResult(toolUse.ID, "late"), nil
}

func (e *countingProbeExecutor) ProbeForTool(toolName string) string {
	e.probeCalls.Add(1)
	return e.verdict
}

func (e *countingProbeExecutor) KillForTool(toolName string) error {
	e.killCalls.Add(1)
	return nil
}

type countingKillOnlyExecutor struct {
	killCalls atomic.Int32
	release   chan struct{}
}

func (e *countingKillOnlyExecutor) Execute(ctx context.Context, toolUse ToolUse) (ToolResult, error) {
	select {
	case <-e.release:
	case <-time.After(10 * time.Second):
	}
	return NewToolResult(toolUse.ID, "late"), nil
}

func (e *countingKillOnlyExecutor) KillForTool(toolName string) error {
	e.killCalls.Add(1)
	return nil
}

const probeTimeout = 200 * time.Millisecond

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_alive_server_is_not_killed
func TestAliveServerIsNotKilled(t *testing.T) {
	exec := &countingProbeExecutor{verdict: "alive", release: make(chan struct{})}
	defer close(exec.release)

	res, err := ExecuteTool(context.Background(), exec, testToolCall("browser_evaluate", nil), probeTimeout, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("timeout result must be an error")
	}
	for _, needle := range []string{"timed out after", "DO NOT kill", "NOT an application or browser crash"} {
		if !strings.Contains(res.Content, needle) {
			t.Fatalf("message missing %q: %s", needle, res.Content)
		}
	}
	if got := exec.probeCalls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if got := exec.killCalls.Load(); got != 0 {
		t.Fatalf("kill calls = %d, want 0 (alive server must not be force-disconnected)", got)
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_resetting_server_is_not_killed_again
func TestResettingServerIsNotKilledAgain(t *testing.T) {
	exec := &countingProbeExecutor{verdict: "resetting", release: make(chan struct{})}
	defer close(exec.release)

	res, err := ExecuteTool(context.Background(), exec, testToolCall("browser_evaluate", nil), probeTimeout, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("timeout result must be an error")
	}
	for _, needle := range []string{"timed out after", "AUTOMATICALLY", "DO NOT kill"} {
		if !strings.Contains(res.Content, needle) {
			t.Fatalf("message missing %q: %s", needle, res.Content)
		}
	}
	if got := exec.killCalls.Load(); got != 0 {
		t.Fatalf("kill calls = %d, want 0 (in-flight reset must not be torn down again)", got)
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_dead_server_is_killed
func TestDeadServerIsKilled(t *testing.T) {
	exec := &countingProbeExecutor{verdict: "dead", release: make(chan struct{})}
	defer close(exec.release)

	res, err := ExecuteTool(context.Background(), exec, testToolCall("browser_evaluate", nil), probeTimeout, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("timeout result must be an error")
	}
	for _, needle := range []string{"timed out after", "DO NOT kill"} {
		if !strings.Contains(res.Content, needle) {
			t.Fatalf("message missing %q: %s", needle, res.Content)
		}
	}
	if got := exec.killCalls.Load(); got != 1 {
		t.Fatalf("kill calls = %d, want 1 (provably-dead server must be torn down)", got)
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_unknown_probe_falls_back_to_legacy_kill
func TestUnknownProbeFallsBackToLegacyKill(t *testing.T) {
	exec := &countingProbeExecutor{verdict: "unknown", release: make(chan struct{})}
	defer close(exec.release)

	res, err := ExecuteTool(context.Background(), exec, testToolCall("browser_evaluate", nil), probeTimeout, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("timeout result must be an error")
	}
	if got := exec.killCalls.Load(); got != 1 {
		t.Fatalf("kill calls = %d, want 1", got)
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_no_probe_support_falls_back_to_legacy_kill
func TestNoProbeSupportFallsBackToLegacyKill(t *testing.T) {
	exec := &countingKillOnlyExecutor{release: make(chan struct{})}
	defer close(exec.release)

	res, err := ExecuteTool(context.Background(), exec, testToolCall("browser_evaluate", nil), probeTimeout, nil)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("timeout result must be an error")
	}
	if got := exec.killCalls.Load(); got != 1 {
		t.Fatalf("kill calls = %d, want 1", got)
	}
}

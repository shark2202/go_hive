package agentloop

// Background-tool handle probes.
//
// A tool named in LoopConfig.BackgroundTools returns a bg_* handle immediately
// and runs off the turn's critical path; the synthetic collect_result tool
// waits for the work and returns the real result, or {"status":"pending"} when
// wait_seconds elapses or a new user message arrives first. The fakes here are
// deterministic: an executor blocked on a channel, and explicit budgets.
//
// Parity: core/tests/test_background_tools.py

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// backgroundTestConfig enables exactly the named background tools with no
// grace window, so every call mints a handle immediately.
func backgroundTestConfig(tools ...string) LoopConfig {
	cfg := DefaultLoopConfig()
	cfg.BackgroundTools = make(map[string]struct{}, len(tools))
	for _, name := range tools {
		cfg.BackgroundTools[name] = struct{}{}
	}
	cfg.BackgroundToolGraceSeconds = 0
	cfg.BackgroundToolTimeoutSeconds = 30
	return cfg
}

func decodeBackgroundPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("tool result is not JSON: %v (%q)", err, content)
	}
	return payload
}

func collectBackgroundRequest(handle string, waitSeconds any) ToolCallRequest {
	input := map[string]any{"handle": handle}
	if waitSeconds != nil {
		input["wait_seconds"] = waitSeconds
	}
	return ToolCallRequest{ID: "c_collect", Name: CollectResultToolName, Input: input}
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// onceCloser returns an idempotent close for a release channel: a test may
// unblock its fake executor mid-body and must still be able to close on the
// failure path.
func onceCloser(ch chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// TestBackgroundToolReturnsHandleWithoutBlockingTheTurn is the negative
// regression for the handle wiring: with the executor bounded at 2s, a build
// where executeTool runs the call inline cannot deliver a handle inside the
// 300ms deadline below — and the failure is a clean assertion, not a hang.
//
// Parity: core/tests/test_background_tools.py::test_background_dispatch_then_collect
func TestBackgroundToolReturnsHandleWithoutBlockingTheTurn(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	defer close(release)
	executor := func(ToolCallRequest) (ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		return ToolResult{Content: `{"images":[{"path":"/x/img.png"}],"usage":{"credits":8.5}}`}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")

	resultCh := make(chan ToolResult, 1)
	go func() {
		resultCh <- loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
			ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
		})
	}()

	var started ToolResult
	select {
	case started = <-resultCh:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("a background tool call blocked the turn instead of returning a handle")
	}
	payload := decodeBackgroundPayload(t, started.Content)
	if payload["status"] != "started" {
		t.Fatalf("status = %v, want started (%s)", payload["status"], started.Content)
	}
	if payload["handle"] != "bg_1" {
		t.Fatalf("handle = %v, want bg_1", payload["handle"])
	}
	if payload["tool"] != "image_generate" {
		t.Fatalf("tool = %v, want image_generate", payload["tool"])
	}
	if started.IsError {
		t.Fatalf("a started handle must not be an error: %s", started.Content)
	}
	waitForCondition(t, time.Second, func() bool { return atomic.LoadInt32(&calls) == 1 }, "the background executor to start")
	if got := loop.backgroundToolCount(); got != 1 {
		t.Fatalf("registered handles = %d, want 1", got)
	}
}

// Parity: core/tests/test_background_tools.py::test_background_dispatch_then_collect
func TestCollectResultReturnsTheRealResultExactlyOnce(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	closeRelease := onceCloser(release)
	defer closeRelease()
	resultBody := `{"images":[{"path":"/x/img.png"}],"usage":{"credits":8.5}}`
	executor := func(ToolCallRequest) (ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return ToolResult{Content: resultBody}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")

	started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	handle, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)
	if handle == "" {
		t.Fatalf("no handle in %s", started.Content)
	}

	// Still running → pending, and the handle survives (not consumed).
	pending := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 0))
	if status := decodeBackgroundPayload(t, pending.Content)["status"]; status != "pending" {
		t.Fatalf("status = %v, want pending (%s)", status, pending.Content)
	}
	if pending.IsError {
		t.Fatalf("a pending poll must not be an error: %s", pending.Content)
	}
	if got := loop.backgroundToolCount(); got != 1 {
		t.Fatalf("registered handles after pending = %d, want 1", got)
	}

	// Released → collect returns the REAL result and retires the handle.
	closeRelease()
	done := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 5))
	if done.Content != resultBody {
		t.Fatalf("collected content = %q, want %q", done.Content, resultBody)
	}
	if done.IsError {
		t.Fatalf("collected result flagged as error: %s", done.Content)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("registered handles after collect = %d, want 0", got)
	}

	// The same handle again is unknown/already collected — not a second run.
	again := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, nil))
	if !again.IsError || !strings.Contains(again.Content, "already-collected handle") {
		t.Fatalf("second collect = isError=%v %q, want the unknown-handle error", again.IsError, again.Content)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("executor calls = %d, want 1 (a handle must never run the tool twice)", got)
	}
}

// Parity: core/tests/test_background_tools.py::test_a_new_message_ends_the_wait_and_the_work_goes_on
func TestCollectResultReturnsPendingWhenANewMessageArrives(t *testing.T) {
	release := make(chan struct{})
	closeRelease := onceCloser(release)
	defer closeRelease()
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: "done"}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")
	handle := func() string {
		started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
			ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
		})
		value, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)
		return value
	}()

	// A message already injected but not yet answered counts: the agent has
	// not read it, so the wait ends at once.
	loop.InjectEvent("are you there?", true, nil)
	started := time.Now()
	pending := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 5))
	if status := decodeBackgroundPayload(t, pending.Content)["status"]; status != "pending" {
		t.Fatalf("status = %v, want pending (%s)", status, pending.Content)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("an already-waiting message ended the collect in %v, want an immediate pending", elapsed)
	}
	if got := loop.backgroundToolCount(); got != 1 {
		t.Fatalf("the pending handle must survive, handles = %d", got)
	}

	// A message that arrives DURING the wait ends it too.
	go func() {
		time.Sleep(50 * time.Millisecond)
		loop.InjectEvent("are you still there?", true, nil)
	}()
	started = time.Now()
	pending = loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 5))
	if status := decodeBackgroundPayload(t, pending.Content)["status"]; status != "pending" {
		t.Fatalf("mid-wait status = %v, want pending (%s)", status, pending.Content)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the mid-wait message ended the collect in %v, want ~one tick", elapsed)
	}

	// The work kept running: once released, the handle still collects. The
	// queued messages are cleared first, as the Python probe does — a real
	// turn would have consumed them before this collect.
	loop.mu.Lock()
	loop.queue = nil
	loop.pending = nil
	loop.unread = false
	loop.mu.Unlock()
	closeRelease()
	done := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 5))
	if done.Content != "done" {
		t.Fatalf("collected content = %q, want done", done.Content)
	}
}

// Parity: core/tests/test_background_tools.py::test_collect_waits_for_the_work_past_the_old_poll_limit
func TestCollectResultWaitsForTheWorkWithoutAPollBudget(t *testing.T) {
	release := make(chan struct{})
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: "done"}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")
	handle := func() string {
		started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
			ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
		})
		value, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)
		return value
	}()

	collected := make(chan ToolResult, 1)
	go func() {
		collected <- loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, nil))
	}()
	select {
	case early := <-collected:
		t.Fatalf("collect returned before the work did: %s", early.Content)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case done := <-collected:
		if done.Content != "done" {
			t.Fatalf("collected content = %q, want done", done.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collect did not return after the work completed")
	}
}

// Parity: core/tests/test_background_tools.py::test_collect_waits_for_the_work_past_the_old_poll_limit
// (the wait_seconds budget branch: a short explicit budget ends at the deadline)
func TestCollectResultHonorsWaitSeconds(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: "done"}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")
	started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	handle, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)

	begin := time.Now()
	pending := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 0.05))
	elapsed := time.Since(begin)
	if status := decodeBackgroundPayload(t, pending.Content)["status"]; status != "pending" {
		t.Fatalf("status = %v, want pending (%s)", status, pending.Content)
	}
	if elapsed < 40*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("wait_seconds=0.05 returned after %v", elapsed)
	}
}

// Parity: core/tests/test_background_tools.py::test_background_dispatch_then_collect
// (step 4: an unknown handle is an error, not a crash)
func TestCollectResultUnknownHandleIsAnError(t *testing.T) {
	loop := NewAgentLoop(DefaultLoopConfig(), func(ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "should not run"}, nil
	})
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, CollectResultToolName)

	result := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest("bg_999", nil))
	if !result.IsError {
		t.Fatalf("unknown handle must be an error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Unknown or already-collected handle") {
		t.Fatalf("result = %q, want the unknown-handle message", result.Content)
	}
}

// Parity-exempt: the Python suite asserts error propagation only via the task
// exception branch; this pins the ToolResult error path collect must relay.
func TestCollectResultSurfacesTheToolError(t *testing.T) {
	release := make(chan struct{})
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: `{"error":"image provider rejected the prompt"}`, IsError: true}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")
	started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	handle, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)

	close(release)
	collected := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 5))
	if !collected.IsError {
		t.Fatalf("a failed background tool must surface is_error: %s", collected.Content)
	}
	if !strings.Contains(collected.Content, "image provider rejected the prompt") {
		t.Fatalf("collected content = %q, want the tool's own error payload", collected.Content)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("registered handles after an error = %d, want 0", got)
	}
}

// Parity-exempt: the Python suite has no timeout probe for the background
// budget; the 235s default is pinned by TestLoopConfigPrefixTimeoutResolution.
func TestBackgroundToolTimeoutBecomesAnErrorResult(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: "late"}, nil
	}
	cfg := backgroundTestConfig("image_generate")
	cfg.BackgroundToolTimeoutSeconds = 0.05
	loop := NewAgentLoop(cfg, executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")
	started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	handle, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)

	collected := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 2))
	if !collected.IsError || !strings.Contains(collected.Content, "timed out after") {
		t.Fatalf("timed-out background tool = isError=%v %q, want the timeout error", collected.IsError, collected.Content)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("registered handles after a timeout = %d, want 0", got)
	}
}

// Parity-exempt: upstream documents that cancelled turns leave the handle
// reachable (the task is registered before the grace await) but ships no Go
// test for it; this pins the ported ordering.
func TestCancelDuringGraceWindowLeavesTheHandleCollectable(t *testing.T) {
	release := make(chan struct{})
	closeRelease := onceCloser(release)
	defer closeRelease()
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: "done"}, nil
	}
	cfg := backgroundTestConfig("image_generate")
	cfg.BackgroundToolGraceSeconds = 5
	loop := NewAgentLoop(cfg, executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	result := loop.executeTool(ctx, agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("a cancelled grace window returned after %v", elapsed)
	}
	if !result.IsError {
		t.Fatalf("a cancelled turn must return an error result, got %s", result.Content)
	}
	if got := loop.backgroundToolCount(); got != 1 {
		t.Fatalf("the cancelled turn must leave its handle registered, handles = %d", got)
	}

	closeRelease()
	loop.bgMu.Lock()
	handle := ""
	for registered := range loop.backgroundCalls {
		handle = registered
	}
	loop.bgMu.Unlock()
	if handle == "" {
		t.Fatal("the cancelled grace window left no registered handle")
	}
	done := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 5))
	if done.Content != "done" {
		t.Fatalf("collected content = %q, want done", done.Content)
	}
}

// Parity-exempt: Go-side cancellation of the collect wait (Python's outer
// CancelledError); the handle must survive for a later turn.
func TestCancelDuringCollectLeavesTheHandleCollectable(t *testing.T) {
	release := make(chan struct{})
	closeRelease := onceCloser(release)
	defer closeRelease()
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: "done"}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")
	started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	handle, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	result := loop.executeTool(ctx, agentCtx, collectBackgroundRequest(handle, 30))
	if !result.IsError {
		t.Fatalf("a cancelled collect must return an error result, got %s", result.Content)
	}
	if got := loop.backgroundToolCount(); got != 1 {
		t.Fatalf("the cancelled collect must keep the handle, handles = %d", got)
	}

	closeRelease()
	done := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, 5))
	if done.Content != "done" {
		t.Fatalf("collected content = %q, want done", done.Content)
	}
}

// Parity-exempt: session-teardown cleanup of uncollected handles is a Go-side
// wiring addition (ChatRuntime.Close calls it).
func TestReleaseBackgroundToolsDropsUncollectedHandles(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	executor := func(ToolCallRequest) (ToolResult, error) {
		<-release
		return ToolResult{Content: "done"}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")
	started := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	handle, _ := decodeBackgroundPayload(t, started.Content)["handle"].(string)

	if dropped := loop.ReleaseBackgroundTools(); dropped != 1 {
		t.Fatalf("released handles = %d, want 1", dropped)
	}
	if dropped := loop.ReleaseBackgroundTools(); dropped != 0 {
		t.Fatalf("second release = %d, want 0 (idempotent)", dropped)
	}
	collected := loop.executeTool(context.Background(), agentCtx, collectBackgroundRequest(handle, nil))
	if !collected.IsError || !strings.Contains(collected.Content, "Unknown or already-collected handle") {
		t.Fatalf("released handle = isError=%v %q, want unknown", collected.IsError, collected.Content)
	}
}

// Parity-exempt: the terminal_exec foreground-wait guard inside upstream's
// _start_background_tool has no standalone Python test; this pins the ported
// refusal (a foreground wait that cannot fit the background budget).
func TestTerminalExecForegroundWaitOverBudgetIsRefused(t *testing.T) {
	var calls int32
	loop := NewAgentLoop(backgroundTestConfig("terminal_exec"), func(ToolCallRequest) (ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		return ToolResult{Content: "should not run"}, nil
	})
	loop.Config.BackgroundToolTimeoutSeconds = 30
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "terminal_exec")

	result := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID:   "c1",
		Name: "terminal_exec",
		Input: map[string]any{
			"command":                   "sleep 600",
			"timeout_sec":               60,
			"auto_background_after_sec": 30,
		},
	})
	if !result.IsError || !strings.Contains(result.Content, "foreground wait") {
		t.Fatalf("over-budget foreground wait = isError=%v %q, want the refusal", result.IsError, result.Content)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("executor calls = %d, want 0 (the call must be refused before it runs)", got)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("registered handles = %d, want 0", got)
	}
}

// Parity: core/tests/test_background_tools.py::test_non_background_tool_runs_inline
func TestNonBackgroundToolRunsInline(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	executor := func(req ToolCallRequest) (ToolResult, error) {
		mu.Lock()
		calls = append(calls, req.Name)
		mu.Unlock()
		return ToolResult{Content: "ok"}, nil
	}
	cfg := backgroundTestConfig("image_generate")
	loop := NewAgentLoop(cfg, executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "terminal_rg")

	result := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "terminal_rg", Input: map[string]any{"pattern": "x"},
	})
	if result.Content != "ok" || result.IsError {
		t.Fatalf("foreground tool result = %+v, want ok", result)
	}
	mu.Lock()
	seen := append([]string(nil), calls...)
	mu.Unlock()
	if len(seen) != 1 || seen[0] != "terminal_rg" {
		t.Fatalf("executor calls = %v, want [terminal_rg]", seen)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("registered handles = %d, want 0", got)
	}
}

// Parity: core/tests/test_background_tools.py::test_background_dispatch_then_collect
// (upstream's grace window: fast work returns on the original call and never
// mints a handle, so a quick background-capable tool behaves like foreground)
func TestFastBackgroundToolReturnsItsRealResultInsideTheGraceWindow(t *testing.T) {
	executor := func(ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "fast"}, nil
	}
	cfg := backgroundTestConfig("image_generate")
	cfg.BackgroundToolGraceSeconds = 5
	loop := NewAgentLoop(cfg, executor)
	agentCtx := midturnAgentContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "unused"}}}, "image_generate")

	result := loop.executeTool(context.Background(), agentCtx, ToolCallRequest{
		ID: "c1", Name: "image_generate", Input: map[string]any{"prompt": "a bee"},
	})
	if result.Content != "fast" || result.IsError {
		t.Fatalf("fast background call = %+v, want the real result", result)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("a fast call must retire its handle, handles = %d", got)
	}
}

// Parity: core/tests/test_background_tools.py::test_background_dispatch_then_collect
// (the observable turn-level flow: turn 1 stores a started handle without
// blocking, a later turn's collect_result returns the real result once)
func TestBackgroundHandleSurvivesAcrossTurnsAndCollectsTheRealResult(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	closeRelease := onceCloser(release)
	defer closeRelease()
	resultBody := `{"images":[{"path":"/x/img.png"}]}`
	executor := func(ToolCallRequest) (ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return ToolResult{Content: resultBody}, nil
	}
	loop := NewAgentLoop(backgroundTestConfig("image_generate"), executor)
	conv := NewNodeConversation("You are a test assistant.")

	turnOne := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "image_generate", Arguments: `{"prompt":"a bee"}`}}},
		{content: "the image is generating"},
	}}
	agentCtx := midturnAgentContext(turnOne, "image_generate")
	agentCtx.AvailableTools = []llm.ToolDef{{Name: "image_generate", Parameters: map[string]any{"type": "object"}}}
	startedAt := time.Now()
	first, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil)
	if err != nil {
		t.Fatalf("first ExecuteTurn: %v", err)
	}
	if !first.Success {
		t.Fatalf("first turn failed: %+v", first)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("the turn waited %v on the background tool", elapsed)
	}
	toolMessages := 0
	for _, message := range conv.Messages() {
		if message.Role == RoleTool && strings.Contains(message.Content, `"status":"started"`) {
			if !strings.Contains(message.Content, `"handle":"bg_1"`) {
				t.Fatalf("started payload missing the handle: %s", message.Content)
			}
			toolMessages++
		}
	}
	if toolMessages != 1 {
		t.Fatalf("conversation carries %d started-tool messages, want 1", toolMessages)
	}

	// The work finishes between turns; the next turn's collect returns the
	// real payload, re-attached to the collect call.
	closeRelease()
	turnTwo := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: CollectResultToolName, Arguments: `{"handle":"bg_1","wait_seconds":5}`}}},
		{content: "here is your image"},
	}}
	agentCtx.LLM = turnTwo
	second, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil)
	if err != nil {
		t.Fatalf("second ExecuteTurn: %v", err)
	}
	if !second.Success {
		t.Fatalf("second turn failed: %+v", second)
	}
	collected := false
	for _, message := range conv.Messages() {
		if message.Role == RoleTool && message.Content == resultBody {
			collected = true
		}
	}
	if !collected {
		t.Fatalf("the collected real result never reached the conversation: %#v", conv.Messages())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("executor calls = %d, want 1 across both turns", got)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("registered handles = %d, want 0 after collect", got)
	}
}

// Parity: core/tests/test_background_tools.py::test_dynamic_refresh_preserves_collect_result
func TestBackgroundToolsAdvertiseCollectResultThroughTheTurn(t *testing.T) {
	newLoop := func(tools []llm.ToolDef, cfg LoopConfig) (*AgentLoop, *scriptedCompleter) {
		completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
		loop := NewAgentLoop(cfg, func(ToolCallRequest) (ToolResult, error) {
			return ToolResult{Content: "ok"}, nil
		})
		agentCtx := midturnAgentContext(completer, "image_generate", "terminal_rg")
		agentCtx.AvailableTools = tools
		if _, err := loop.ExecuteTurn(context.Background(), agentCtx, NewNodeConversation("You are a test assistant."), tools); err != nil {
			t.Fatalf("ExecuteTurn: %v", err)
		}
		return loop, completer
	}

	image := llm.ToolDef{Name: "image_generate", Parameters: map[string]any{"type": "object"}}
	loop, completer := newLoop([]llm.ToolDef{image}, DefaultLoopConfig())
	if names := toolNames(completer.request(0).Tools); !containsName(names, CollectResultToolName) {
		t.Fatalf("first request tools = %v, want collect_result advertised with background tools", names)
	}
	if got := loop.backgroundToolCount(); got != 0 {
		t.Fatalf("registered handles = %d, want 0", got)
	}

	// The flat default surface already carries collect_result; the append must
	// not duplicate it.
	withCollect := llm.ToolDef{Name: CollectResultToolName, Parameters: map[string]any{"type": "object"}}
	_, completer = newLoop([]llm.ToolDef{image, withCollect}, DefaultLoopConfig())
	names := toolNames(completer.request(0).Tools)
	occurrences := 0
	for _, name := range names {
		if name == CollectResultToolName {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("collect_result occurrences = %d, want 1 (%v)", occurrences, names)
	}

	// An explicit empty table disables backgrounding, so the synthetic is not
	// advertised into a surface that could never mint a handle.
	disabled := DefaultLoopConfig()
	disabled.BackgroundTools = map[string]struct{}{}
	_, completer = newLoop([]llm.ToolDef{image}, disabled)
	if names := toolNames(completer.request(0).Tools); containsName(names, CollectResultToolName) {
		t.Fatalf("request tools = %v; collect_result must not appear with backgrounding disabled", names)
	}
}

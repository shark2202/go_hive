package agentloop

// Worker text-only stall auto-fail, loop half.
//
// Parity: core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (6h worker stall)

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// stallWorkerContext is a parallel worker (worker:<id>) whose report owner is
// the test executor, mirroring what the colony runtime wires.
func stallWorkerContext(completer llm.Completer, streamID string) *AgentContext {
	ctx := midturnAgentContext(completer, ReportToParentToolName)
	ctx.StreamID = streamID
	ctx.ExecutionID = "exec_stall"
	return ctx
}

// recordReportExecution returns an executor that records report_to_parent
// inputs and a guard reporting whether a report has landed (the StopAfterToolBatch
// shape the colony runtime installs).
func recordReportExecution(reports *[]map[string]any) (ToolExecutor, func() bool) {
	var mu sync.Mutex
	reported := false
	executor := func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == ReportToParentToolName {
			mu.Lock()
			*reports = append(*reports, req.Input)
			reported = true
			mu.Unlock()
			return ToolResult{Content: "Report delivered to the overseer."}, nil
		}
		return ToolResult{Content: "ok"}, nil
	}
	return executor, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return reported
	}
}

// Parity: core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report
func TestParallelWorkerTextOnlyStallAutoFailsWithSyntheticReport(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "Hmm, let me think..."},
	}}
	var reports []map[string]any
	executor, reported := recordReportExecution(&reports)
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor)
	ctx := stallWorkerContext(completer, "worker:w1")
	ctx.StopAfterToolBatch = reported
	conv := NewNodeConversation("You are a test worker.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn result = %#v, want the clean report termination", result)
	}

	// Grace=2: the first two text-only turns are tolerated, the third
	// auto-fails. The run is bounded — no further model calls happen.
	if got := completer.callCount(); got != 3 {
		t.Fatalf("model calls = %d, want 3 (two grace turns + the failing turn)", got)
	}
	if len(reports) != 1 {
		t.Fatalf("report_to_parent deliveries = %d, want 1 synthetic report", len(reports))
	}
	report := reports[0]
	if report["status"] != "failed" {
		t.Fatalf("synthetic report status = %v, want failed", report["status"])
	}
	summary, _ := report["summary"].(string)
	if !strings.Contains(summary, "Auto-failed: 3 consecutive text-only turns") {
		t.Fatalf("summary = %q, want the upstream auto-fail signature", summary)
	}
	if !strings.Contains(summary, "Last text excerpt: Hmm, let me think...") {
		t.Fatalf("summary = %q, want the last text excerpt", summary)
	}
	data, _ := report["data"].(map[string]any)
	if data["auto_fail_reason"] != "stall_text_only" {
		t.Fatalf("data = %#v, want auto_fail_reason=stall_text_only", report["data"])
	}
	if toolUseID, _ := report["tool_use_id"].(string); !strings.HasPrefix(toolUseID, "auto_fail_") {
		t.Fatalf("tool_use_id = %q, want the auto_fail_ shape", toolUseID)
	}

	// The grace turns continued the loop: the follow-up requests carry the
	// continuation prompt rather than a fresh task.
	for _, index := range []int{1, 2} {
		if positionOf(completer.request(index).Messages, "[Continue working on your current task.]") < 0 {
			t.Fatalf("request %d lost the continuation nudge: %#v", index, completer.request(index).Messages)
		}
	}

	// Fail-fast: no escalation event, no synchronous wait.
	if got := loop.Counter(stallEscalatedCounter); got != 0 {
		t.Fatalf("escalations = %d, want 0 for a parallel worker", got)
	}
	if got := loop.Counter(stallAutoFailedCounter); got != 1 {
		t.Fatalf("worker_stall_auto_failed = %d, want 1", got)
	}
	if got := loop.Counter(stallGraceCounter); got != 2 {
		t.Fatalf("worker_stall_grace = %d, want 2", got)
	}
}

// Parity-exempt: the threshold is a LoopConfig knob upstream; this pins the
// off-by-one (grace=N tolerates N text-only turns, the N+1-th fails).
func TestWorkerStallGraceIsConfigurable(t *testing.T) {
	if got := DefaultLoopConfig().WorkerEscalationGraceTurns; got != DefaultWorkerEscalationGraceTurns {
		t.Fatalf("DefaultLoopConfig().WorkerEscalationGraceTurns = %d, want %d", got, DefaultWorkerEscalationGraceTurns)
	}
	for _, tc := range []struct {
		grace     int
		wantCalls int
	}{
		{grace: 0, wantCalls: 3}, // unset → the upstream default
		{grace: 1, wantCalls: 2},
		{grace: DefaultWorkerEscalationGraceTurns, wantCalls: 3},
		{grace: 4, wantCalls: 5},
	} {
		completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "thinking"}}}
		var reports []map[string]any
		executor, reported := recordReportExecution(&reports)
		loop := NewAgentLoop(LoopConfig{MaxIterations: 20, WorkerEscalationGraceTurns: tc.grace, StallDetectionThreshold: 999}, executor)
		ctx := stallWorkerContext(completer, "worker:w1")
		ctx.StopAfterToolBatch = reported
		conv := NewNodeConversation("You are a test worker.")

		result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
		if err != nil {
			t.Fatalf("grace=%d: ExecuteTurn: %v", tc.grace, err)
		}
		if !result.Success || len(reports) != 1 {
			t.Fatalf("grace=%d: result=%#v reports=%d, want one clean auto-fail report", tc.grace, result, len(reports))
		}
		if got := completer.callCount(); got != tc.wantCalls {
			t.Fatalf("grace=%d: model calls = %d, want %d", tc.grace, got, tc.wantCalls)
		}
		if got := loop.Counter(stallAutoFailedCounter); got != 1 {
			t.Fatalf("grace=%d: worker_stall_auto_failed = %d, want 1", tc.grace, got)
		}
	}
}

// Parity-exempt: upstream resets the streak on real_tool_results/outputs_set;
// this drives several text-only turns interleaved with tool calls past the
// grace bound and asserts the worker is never auto-failed.
func TestWorkerToolCallResetsTextOnlyStreak(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "planning"},
		{toolCalls: []llm.ToolCall{{ID: "t1", Name: "task_list", Arguments: `{}`}}},
		{content: "still planning"},
		{toolCalls: []llm.ToolCall{{ID: "t2", Name: "task_list", Arguments: `{"status":"pending"}`}}},
		{content: "nearly done"},
		{toolCalls: []llm.ToolCall{{ID: "report_1", Name: ReportToParentToolName, Arguments: `{"status":"success","summary":"varied work"}`}}},
	}}
	var reports []map[string]any
	executor, reported := recordReportExecution(&reports)
	loop := NewAgentLoop(LoopConfig{MaxIterations: 20}, executor)
	ctx := stallWorkerContext(completer, "worker:w1")
	ctx.StopAfterToolBatch = reported
	conv := NewNodeConversation("You are a test worker.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn result = %#v, want success via the model's own report", result)
	}
	if got := completer.callCount(); got != 6 {
		t.Fatalf("model calls = %d, want 6 (three text turns interleaved with work)", got)
	}
	if got := loop.Counter(stallAutoFailedCounter); got != 0 {
		t.Fatalf("worker_stall_auto_failed = %d, want 0 for a worker making progress", got)
	}
	if len(reports) != 1 || reports[0]["status"] != "success" {
		t.Fatalf("reports = %#v, want the model's success report", reports)
	}
}

// Parity-exempt: upstream's legacy path (stream_id="worker") emits
// ESCALATION_REQUESTED and parks; the resume resets the streak. No producer
// spawns a legacy stream in this port, so the loop semantics are pinned here.
func TestLegacyWorkerTextOnlyStallEscalatesAndParks(t *testing.T) {
	recorder := &escalationRecorder{requestID: "req-stall"}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "waiting for credentials"},
		{content: "still no credentials"},
		{content: "cannot make progress"},
		{content: "resumed after guidance"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 20}, func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	})
	ctx := stallWorkerContext(completer, "worker")
	ctx.EscalationSink = recorder.sink
	conv := NewNodeConversation("You are a test worker.")
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := loop.ExecuteTurn(runCtx, ctx, conv, nil)
		done <- err
	}()

	// Grace=2 then escalate: three text-only turns before the park.
	deadline := time.Now().Add(5 * time.Second)
	for recorder.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("legacy worker never escalated; model calls = %d", completer.callCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
	requests := recorder.snapshot()
	if len(requests) != 1 {
		t.Fatalf("escalations = %d, want exactly 1", len(requests))
	}
	if requests[0].Reason != stallEscalationReason {
		t.Fatalf("escalation reason = %q, want %q", requests[0].Reason, stallEscalationReason)
	}
	if requests[0].Context != "cannot make progress" || requests[0].StreamID != "worker" {
		t.Fatalf("escalation = %#v, want the worker stream and the last text", requests[0])
	}
	if got := loop.Counter(stallEscalatedCounter); got != 1 {
		t.Fatalf("worker_stall_escalated = %d, want 1", got)
	}
	if got := loop.Counter(stallAutoFailedCounter); got != 0 {
		t.Fatalf("worker_stall_auto_failed = %d, want 0 on the legacy path", got)
	}

	// Parked: no further model call until the queen answers.
	time.Sleep(150 * time.Millisecond)
	if got := completer.callCount(); got != 3 {
		t.Fatalf("model calls while parked = %d, want 3", got)
	}

	loop.InjectEvent("[QUEEN_REPLY] request_id=req-stall\nhere is the key", true, nil)

	// The reply resumes the worker; the resumed call reads the guidance and the
	// streak starts fresh (no immediate re-escalation).
	deadline = time.Now().Add(5 * time.Second)
	for completer.callCount() < 4 {
		if time.Now().After(deadline) {
			t.Fatal("worker did not resume after the queen's reply")
		}
		time.Sleep(10 * time.Millisecond)
	}
	resumed := completer.request(3)
	if positionOf(resumed.Messages, "[QUEEN_REPLY] request_id=req-stall") < 0 {
		t.Fatalf("queen reply missing from the resumed call: %#v", resumed.Messages)
	}
	if got := loop.Counter(stallAutoFailedCounter); got != 0 {
		t.Fatalf("worker_stall_auto_failed = %d, want 0 after the legacy resume", got)
	}

	// End the turn: the scripted model never terminates on its own, so
	// cancelling is the terminal event this test needs.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ExecuteTurn returned nil after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteTurn did not unwind after cancellation")
	}
}

// Parity-exempt: queen-stage streams keep their pre-existing text-only
// behavior (queen auto-block, judge natural stop); a stream with no host
// channel is not a worker. The stall policy must not apply to either.
func TestNonWorkerStreamsSkipTheStallPolicy(t *testing.T) {
	for _, tc := range []struct {
		streamID string
		// hostChannel wires a report owner + escalation sink. Queen-stage
		// streams get both and must still be excluded; the hostless stream
		// gets neither, matching upstream's _is_worker gate.
		hostChannel bool
	}{
		{streamID: "queen", hostChannel: true},
		{streamID: "overseer", hostChannel: true},
		{streamID: "judge", hostChannel: true},
		{streamID: "colony_plain", hostChannel: false},
	} {
		streamID := tc.streamID
		completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "hi"}}}
		recorder := &escalationRecorder{requestID: "never"}
		var toolCalls int
		reported := false
		loop := NewAgentLoop(LoopConfig{MaxIterations: 5}, func(req ToolCallRequest) (ToolResult, error) {
			toolCalls++
			return ToolResult{Content: "ok"}, nil
		})
		ctx := stallWorkerContext(completer, streamID)
		if tc.hostChannel {
			ctx.EscalationSink = recorder.sink
			ctx.StopAfterToolBatch = func() bool { return reported }
		}
		conv := NewNodeConversation("You are a test agent.")

		result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
		if err != nil {
			t.Fatalf("%s: ExecuteTurn: %v", streamID, err)
		}
		if !result.Success || result.FinalText != "hi" {
			t.Fatalf("%s: result = %#v, want the plain text answer", streamID, result)
		}
		if got := completer.callCount(); got != 1 {
			t.Fatalf("%s: model calls = %d, want 1 (no stall grace)", streamID, got)
		}
		if toolCalls != 0 || recorder.count() != 0 {
			t.Fatalf("%s: tools=%d escalations=%d, want none", streamID, toolCalls, recorder.count())
		}
		if got := loop.Counter(stallAutoFailedCounter) + loop.Counter(stallGraceCounter); got != 0 {
			t.Fatalf("%s: stall counters fired (%d)", streamID, got)
		}
	}
}

// count/snapshot helpers on the escalate test recorder.
func (r *escalationRecorder) count() int { return len(r.snapshot()) }

package agentloop

// Worker escalation channel, loop half: the escalate tool publishes through the
// host sink and parks the turn until the queen injects guidance.
//
// Parity: core/tests/test_event_loop_node.py::test_escalate_available_for_worker_stream (tool gate)
// Parity: core/tests/test_event_loop_node.py::test_escalate_not_available_for_queen_stream (refusal)

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

type escalationRecorder struct {
	mu        sync.Mutex
	requests  []EscalationRequest
	requestID string
	err       error
}

func (r *escalationRecorder) sink(req EscalationRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	if r.err != nil {
		return "", r.err
	}
	return r.requestID, nil
}

func (r *escalationRecorder) snapshot() []EscalationRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]EscalationRequest(nil), r.requests...)
}

func escalateCall(id, reason, contextText string) llm.ToolCall {
	arguments := `{"reason":` + quoteJSON(reason)
	if contextText != "" {
		arguments += `,"context":` + quoteJSON(contextText)
	}
	arguments += "}"
	return llm.ToolCall{ID: id, Name: "escalate", Arguments: arguments}
}

func quoteJSON(value string) string {
	replaced := strings.ReplaceAll(value, `\`, `\\`)
	replaced = strings.ReplaceAll(replaced, `"`, `\"`)
	return `"` + replaced + `"`
}

func escalateAgentContext(completer llm.Completer, streamID string) *AgentContext {
	ctx := midturnAgentContext(completer, "escalate", "report_to_parent")
	ctx.StreamID = streamID
	ctx.ExecutionID = "exec_escalation"
	return ctx
}

// Parity-exempt: upstream exercises the escalate dispatch through the
// event-loop queen-wait branch without a dedicated unit test; this asserts the
// same behaviour (emit, park, resume on injected guidance) directly.
func TestEscalateParksUntilQueenGuidanceArrives(t *testing.T) {
	recorder := &escalationRecorder{requestID: "req-abc"}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{escalateCall("c1", "Credential wall", "HTTP 401 calling the API")}},
		// The resumed turn reports: a text-only turn on a worker stream is a
		// stall now, so a worker that is done must say so through the report.
		{toolCalls: []llm.ToolCall{{ID: "report_1", Name: ReportToParentToolName, Arguments: `{"status":"success","summary":"Resumed with the queen's guidance."}`}}},
	}}
	reported := false
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == ReportToParentToolName {
			reported = true
			return ToolResult{Content: "Report delivered to the overseer."}, nil
		}
		return ToolResult{Content: "unexpected tool " + req.Name}, nil
	})
	// The legacy primary worker stream ("worker", no colon) is the shape
	// upstream keeps the escalate tool for; parallel workers (worker:<id>) are
	// refused (TestEscalateRefusedForParallelWorkerStream).
	ctx := escalateAgentContext(completer, "worker")
	ctx.EscalationSink = recorder.sink
	ctx.StopAfterToolBatch = func() bool { return reported }
	conv := NewNodeConversation("You are a test worker.")

	type outcome struct {
		result TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
		done <- outcome{result: result, err: err}
	}()

	// The sink fires before the park: the escalation must be recorded with the
	// worker's stream, execution and the tool's reason/context.
	deadline := time.Now().Add(5 * time.Second)
	for len(recorder.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("executeTurn never emitted ESCALATION_REQUESTED")
		}
		time.Sleep(10 * time.Millisecond)
	}
	requests := recorder.snapshot()
	if len(requests) != 1 {
		t.Fatalf("escalation requests = %d, want 1", len(requests))
	}
	if requests[0].StreamID != "worker" || requests[0].ExecutionID != "exec_escalation" {
		t.Fatalf("escalation request = %#v, want the worker stream and execution", requests[0])
	}
	if requests[0].Reason != "Credential wall" || requests[0].Context != "HTTP 401 calling the API" {
		t.Fatalf("escalation payload = %#v, want the tool's reason/context", requests[0])
	}

	// Still parked: no second model call until the queen answers.
	time.Sleep(150 * time.Millisecond)
	if got := completer.callCount(); got != 1 {
		t.Fatalf("model calls while parked = %d, want 1", got)
	}

	loop.InjectEvent("[QUEEN_REPLY] request_id=req-abc\nuse the staging key", true, nil)

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("ExecuteTurn: %v", out.err)
		}
		if !out.result.Success || !reported {
			t.Fatalf("turn result = %#v reported=%v, want the resumed report", out.result, reported)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteTurn stayed parked after the queen's reply")
	}

	if got := completer.callCount(); got != 2 {
		t.Fatalf("model calls after resume = %d, want 2", got)
	}
	second := completer.request(1)
	if positionOf(second.Messages, "[QUEEN_REPLY] request_id=req-abc") < 0 {
		t.Fatalf("queen reply missing from the resumed model call: %#v", second.Messages)
	}
	if positionOf(second.Messages, "Escalation requested to queen; waiting for guidance.") < 0 {
		t.Fatalf("tool result missing from the resumed model call: %#v", second.Messages)
	}
}

// Parity: core/tests/test_event_loop_node.py::test_escalate_not_available_for_queen_stream
func TestEscalateRefusedForQueenJudgeAndOverseerStreams(t *testing.T) {
	for _, streamID := range []string{"queen", "judge", "overseer"} {
		recorder := &escalationRecorder{requestID: "never"}
		completer := &scriptedCompleter{scenarios: []scriptedResponse{
			{toolCalls: []llm.ToolCall{escalateCall("c1", "help", "")}},
			{content: "carried on"},
		}}
		loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
			return ToolResult{Content: "ok"}, nil
		})
		ctx := escalateAgentContext(completer, streamID)
		ctx.EscalationSink = recorder.sink
		conv := NewNodeConversation("You are a test agent.")

		result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
		if err != nil {
			t.Fatalf("%s: ExecuteTurn: %v", streamID, err)
		}
		if !result.Success {
			t.Fatalf("%s: turn failed: %#v", streamID, result)
		}
		if len(recorder.snapshot()) != 0 {
			t.Fatalf("%s: escalation emitted for a non-worker stream", streamID)
		}
		record := result.ToolResults[0]
		if !record.IsError || !strings.Contains(record.Content, "only available to worker nodes/sub-agents") {
			t.Fatalf("%s: tool result = %q (isError=%v), want the upstream refusal", streamID, record.Content, record.IsError)
		}
		if got := completer.callCount(); got != 2 {
			t.Fatalf("%s: model calls = %d, want 2 (refusal must not park)", streamID, got)
		}
	}
}

// Parity-exempt: upstream's "EventBus unavailable" refusal has no standalone
// test; this pins the exact message and the no-park behaviour.
func TestEscalateWithoutSinkIsRefusedWithoutParking(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{escalateCall("c1", "help", "")}},
		{content: "carried on"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	})
	ctx := escalateAgentContext(completer, "worker")
	conv := NewNodeConversation("You are a test worker.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(result.ToolResults) == 0 || !result.ToolResults[0].IsError {
		t.Fatalf("tool results = %#v, want an error result", result.ToolResults)
	}
	if got := result.ToolResults[0].Content; got != escalationBusUnavailableMessage {
		t.Fatalf("tool result = %q, want %q", got, escalationBusUnavailableMessage)
	}
	if got := completer.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (a refused escalate must not park)", got)
	}
}

// Parity-exempt: upstream's shutdown-during-wait branch re-emits the escalation
// ("no input received") without a dedicated test; this pins that behaviour.
func TestEscalateWindDownEmitsUnansweredEscalation(t *testing.T) {
	recorder := &escalationRecorder{requestID: "req-lost"}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{escalateCall("c1", "Blocked", "needs a queen decision")}},
		{content: "never reached"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	})
	ctx := escalateAgentContext(completer, "worker")
	ctx.EscalationSink = recorder.sink
	conv := NewNodeConversation("You are a test worker.")
	runCtx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := loop.ExecuteTurn(runCtx, ctx, conv, nil)
		done <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for len(recorder.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("executeTurn never emitted ESCALATION_REQUESTED")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ExecuteTurn returned no error after the run was cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteTurn stayed parked after cancellation")
	}

	requests := recorder.snapshot()
	if len(requests) != 2 {
		t.Fatalf("escalation requests = %#v, want the original plus the wind-down one", requests)
	}
	last := requests[len(requests)-1]
	if last.Reason != "Blocked waiting for queen guidance - no input received" {
		t.Fatalf("wind-down escalation reason = %q, want the upstream no-guidance text", last.Reason)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (the escalate tool
// is gated off every parallel worker, stream_id="worker:<id>"). The port also
// refuses the call at dispatch: a hallucinated escalate must not park a
// fail-fast worker that has no queen waiting on it.
func TestEscalateRefusedForParallelWorkerStream(t *testing.T) {
	recorder := &escalationRecorder{requestID: "never"}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{escalateCall("c1", "help", "")}},
		// The refused call must not park; the worker carries on and reports.
		// (A text-only turn would now be a stall, not a completion.)
		{toolCalls: []llm.ToolCall{{ID: "report_1", Name: ReportToParentToolName, Arguments: `{"status":"success","summary":"carried on"}`}}},
	}}
	reported := false
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == ReportToParentToolName {
			reported = true
			return ToolResult{Content: "Report delivered to the overseer."}, nil
		}
		return ToolResult{Content: "ok"}, nil
	})
	ctx := escalateAgentContext(completer, "worker:abc123")
	ctx.EscalationSink = recorder.sink
	ctx.StopAfterToolBatch = func() bool { return reported }

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("You are a test worker."), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || !reported {
		t.Fatalf("turn result = %#v reported=%v, want a clean carry-on report", result, reported)
	}
	if requests := recorder.snapshot(); len(requests) != 0 {
		t.Fatalf("parallel worker escalation requests = %#v, want none", requests)
	}
	if len(result.ToolResults) == 0 || !result.ToolResults[0].IsError ||
		!strings.Contains(result.ToolResults[0].Content, "not available to colony workers") {
		t.Fatalf("tool result = %#v, want the parallel-worker refusal", result.ToolResults)
	}
	if got := completer.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (the refusal must not park)", got)
	}
}

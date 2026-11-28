package agentloop

// Regression tests for A5: upstream's turn-level duplicate-fingerprint window.
//
// The per-call DoomLoopDetector keeps the P0-14 colony contract (warn 3 /
// escalate / park 5). The window is additive: it fingerprints the whole batch
// per turn and trips when the same ordered batch shape repeats for
// tool_doom_loop_threshold consecutive turns — the shape a per-call streak
// cannot see when the batch alternates between different calls.
//
// Parity: framework/agent_loop/internals/stall_detector.py::is_tool_doom_loop
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' branch order)

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// batchRepeatScenario returns n identical two-call batches followed by a text
// turn, so a test can observe exactly the window under test.
func batchRepeatScenario(n int) []scriptedResponse {
	scenarios := make([]scriptedResponse, 0, n+1)
	for i := 0; i < n; i++ {
		scenarios = append(scenarios, scriptedResponse{toolCalls: []llm.ToolCall{
			{ID: "a" + strconv.Itoa(i), Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "b" + strconv.Itoa(i), Name: "read_file", Arguments: `{"path":"b.txt"}`},
		}})
	}
	return append(scenarios, scriptedResponse{content: "done"})
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' turn-level
// fingerprint window)
//
// TestDoomLoopWindowDetectsRepeatedBatchShape is the A5 load-bearing case: a
// model that re-issues [read_file(a), read_file(b)] every turn never builds a
// consecutive single-call streak, but upstream's turn-level window trips on the
// third identical batch. A colony worker escalates and keeps working.
func TestDoomLoopWindowDetectsRepeatedBatchShape(t *testing.T) {
	const turns = DefaultDoomLoopThreshold
	completer := &scriptedCompleter{scenarios: batchRepeatScenario(turns)}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor.execute)
	recorder := &escalationRecorder{requestID: "req-window"}
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "worker:batch"
	ctx.EscalationSink = recorder.sink
	ctx.StopAfterToolBatch = func() bool { return executor.count() >= 2*turns }
	conv := NewNodeConversation("system")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("worker window detection ended the turn: %+v", result)
	}
	if got := executor.count(); got != 2*turns {
		t.Fatalf("executed %d calls, want %d (every batch still runs for a worker)", got, 2*turns)
	}
	if got := loop.Counter("doom_loop_window_detected"); got != 1 {
		t.Fatalf("stats[doom_loop_window_detected] = %d, want 1", got)
	}
	// The per-call guard never saw a consecutive fingerprint, so the window is
	// the only detector that fired.
	if got := loop.Counter("doom_loop_warned"); got != 0 {
		t.Fatalf("stats[doom_loop_warned] = %d, want 0 (per-call streak never repeated)", got)
	}
	requests := recorder.snapshot()
	if len(requests) != 1 {
		t.Fatalf("escalations = %d, want 1: %#v", len(requests), requests)
	}
	if requests[0].Reason != "Tool doom loop detected" || requests[0].StreamID != "worker:batch" {
		t.Fatalf("escalation = %#v, want the worker doom loop", requests[0])
	}
	wantContext := "Doom loop detected: 3 identical consecutive tool calls (read_file, read_file)"
	if requests[0].Context != wantContext {
		t.Fatalf("escalation context = %q, want %q", requests[0].Context, wantContext)
	}
	// Upstream adds the warning user message and, for a worker, the escalation
	// notice; neither may ride a tool result tail.
	var sawWarning, sawNotice bool
	for _, message := range conv.Messages() {
		if message.Role != RoleUser {
			continue
		}
		if strings.Contains(message.Content, wantContext) &&
			strings.Contains(message.Content, "You are repeating the same tool calls") {
			sawWarning = true
		}
		if strings.Contains(message.Content, doomLoopEscalationNotice) {
			sawNotice = true
		}
	}
	if !sawWarning {
		t.Fatal("conversation is missing the upstream doom-loop warning user message")
	}
	if !sawNotice {
		t.Fatal("conversation is missing the worker escalation notice")
	}
	if result.ParkReason != nil {
		t.Fatalf("TurnResult.ParkReason = %v, want nil (workers escalate, not park)", result.ParkReason)
	}
}

// Parity: framework/agent_loop/internals/stall_detector.py::fingerprint_tool_calls
// (order-sensitive: [A, B] != [B, A]).
func TestDoomLoopWindowIsOrderSensitive(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "a0", Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "b0", Name: "read_file", Arguments: `{"path":"b.txt"}`},
		}},
		{toolCalls: []llm.ToolCall{
			{ID: "b1", Name: "read_file", Arguments: `{"path":"b.txt"}`},
			{ID: "a1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
		}},
		{toolCalls: []llm.ToolCall{
			{ID: "a2", Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "b2", Name: "read_file", Arguments: `{"path":"b.txt"}`},
		}},
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor.execute)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "worker:order"
	ctx.EscalationSink = func(EscalationRequest) (string, error) { return "req", nil }
	ctx.StopAfterToolBatch = func() bool { return executor.count() >= 6 }

	if _, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if got := loop.Counter("doom_loop_window_detected"); got != 0 {
		t.Fatalf("stats[doom_loop_window_detected] = %d, want 0 for reordered batches", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' text-only
// branch clears recent_tool_fingerprints)
//
// A text-only turn clears the window, exactly like upstream's
// recent_tool_fingerprints.clear() in the empty-batch branch.
func TestDoomLoopWindowTextOnlyTurnBreaksTheChain(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "a0", Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "b0", Name: "read_file", Arguments: `{"path":"b.txt"}`},
		}},
		{content: "thinking"},
		{toolCalls: []llm.ToolCall{
			{ID: "a1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "b1", Name: "read_file", Arguments: `{"path":"b.txt"}`},
		}},
		{toolCalls: []llm.ToolCall{
			{ID: "a2", Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "b2", Name: "read_file", Arguments: `{"path":"b.txt"}`},
		}},
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, WorkerEscalationGraceTurns: 2, StallDetectionThreshold: 999}, executor.execute)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "worker:chain"
	ctx.EscalationSink = func(EscalationRequest) (string, error) { return "req", nil }
	ctx.StopAfterToolBatch = func() bool { return executor.count() >= 6 }

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn failed: %+v", result)
	}
	if got := loop.Counter("doom_loop_window_detected"); got != 0 {
		t.Fatalf("stats[doom_loop_window_detected] = %d, want 0 after a text-only break", got)
	}
	if got := loop.Counter("doom_loop_warned"); got != 0 {
		t.Fatalf("stats[doom_loop_warned] = %d, want 0 (the batches alternate, then break)", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f'
// supports_direct_user_io -> await_user_input(DOOM_LOOP))
//
// A queen cannot escalate, so the window parks the turn on the user instead of
// running the repeated plan forever.
func TestDoomLoopWindowQueenParksAtThreshold(t *testing.T) {
	const turns = DefaultDoomLoopThreshold
	completer := &scriptedCompleter{scenarios: batchRepeatScenario(turns)}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor.execute)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "queen"

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if parked.ParkReason() != ParkReasonDoomLoop {
		t.Fatalf("ParkReason() = %q, want doom_loop", parked.ParkReason())
	}
	if got := executor.count(); got != 2*turns {
		t.Fatalf("executed %d calls, want %d (the window is post-execution)", got, 2*turns)
	}
	if got := loop.Counter("doom_loop_window_detected"); got != 1 {
		t.Fatalf("stats[doom_loop_window_detected] = %d, want 1", got)
	}
	if result.Success {
		t.Fatal("queen doom-loop park reports Success = true")
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
		t.Fatalf("TurnResult.ParkReason = %v, want doom_loop", result.ParkReason)
	}
}

// Parity-exempt: the per-call guard and its warn3/park5 contract are the port's
// P0-14 addition; the window yields to it to avoid double reporting.
//
// The per-call guard still owns single-call repeats: the window must not add a
// second warning or a second escalation to the P0-14 worker contract.
func TestDoomLoopWindowStandsDownWhenPerCallGuardWarns(t *testing.T) {
	const repeats = DefaultDoomLoopThreshold
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("write_file", `{"path":"notes.md"}`, repeats)}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor.execute)
	recorder := &escalationRecorder{requestID: "req-percall"}
	ctx := judgeTestContext(completer, nil, "write_file")
	ctx.StreamID = "worker:percall"
	ctx.EscalationSink = recorder.sink
	ctx.StopAfterToolBatch = func() bool { return executor.count() >= repeats }

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn failed: %+v", result)
	}
	if got := loop.Counter("doom_loop_warned"); got != 1 {
		t.Fatalf("stats[doom_loop_warned] = %d, want the per-call guard's one warning", got)
	}
	if got := loop.Counter("doom_loop_window_detected"); got != 0 {
		t.Fatalf("stats[doom_loop_window_detected] = %d, want 0 (per-call guard owns the streak)", got)
	}
	if requests := recorder.snapshot(); len(requests) != 1 {
		t.Fatalf("escalations = %d, want the per-call guard's one: %#v", len(requests), requests)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_tool_doom_loop with the window description)
//
// The window detection publishes the batch description upstream builds, not
// the per-call streak wording: the event and the escalation/park text must
// describe the same detection.
func TestDoomLoopWindowReportsTheBatchDescriptionThroughTheEventSink(t *testing.T) {
	const turns = DefaultDoomLoopThreshold
	completer := &scriptedCompleter{scenarios: batchRepeatScenario(turns)}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor.execute)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "worker:window-event"
	ctx.StopAfterToolBatch = func() bool { return executor.count() >= 2*turns }
	type report struct {
		tool        string
		streak      int
		description string
	}
	var reports []report
	loop.DoomLoopSink = func(tool string, streak int, description string) {
		reports = append(reports, report{tool: tool, streak: streak, description: description})
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("worker window detection ended the turn: %+v", result)
	}
	if len(reports) != 1 {
		t.Fatalf("doom-loop reports = %#v, want exactly 1 for the window detection", reports)
	}
	want := "Doom loop detected: 3 identical consecutive tool calls (read_file, read_file)"
	if reports[0].description != want {
		t.Fatalf("doom-loop description = %q, want %q", reports[0].description, want)
	}
	if reports[0].tool != "read_file" || reports[0].streak != turns {
		t.Fatalf("doom-loop report = %#v, want read_file at depth %d", reports[0], turns)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f'
// supports_direct_user_io -> await_user_input(DOOM_LOOP))
//
// The queen's park publishes through the server's ParkSink, so the same
// window description must ride the park detail the event is built from.
func TestDoomLoopWindowQueenParkCarriesTheBatchDescription(t *testing.T) {
	const turns = DefaultDoomLoopThreshold
	completer := &scriptedCompleter{scenarios: batchRepeatScenario(turns)}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor.execute)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "queen"
	type park struct {
		reason ParkReason
		detail string
	}
	var parks []park
	loop.ParkSink = func(reason ParkReason, detail string) {
		parks = append(parks, park{reason: reason, detail: detail})
	}

	_, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if len(parks) != 1 {
		t.Fatalf("parks = %#v, want exactly 1 for the window detection", parks)
	}
	want := "Doom loop detected: 3 identical consecutive tool calls (read_file, read_file)"
	if parks[0].reason != ParkReasonDoomLoop || parks[0].detail != want {
		t.Fatalf("park = %#v, want reason %q detail %q", parks[0], ParkReasonDoomLoop, want)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_trip_tool_breaker
//
// P0-14 half: a colony worker with no escalation channel has no automatic
// audience, so the pre-dispatch breaker refuses its third identical call.
func TestWorkerWithoutEscalationChannelGetsBreakerRefusal(t *testing.T) {
	const args = `{"path":"notes.md","content":"same"}`
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("write_file", args, DefaultDoomLoopThreshold)}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	ctx := judgeTestContext(completer, nil, "write_file")
	ctx.StreamID = "worker:nochannel"

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if !parked.Breaker {
		t.Fatal("worker without an escalation channel did not get the breaker refusal")
	}
	if got := executor.count(); got != DefaultDoomLoopThreshold-1 {
		t.Fatalf("executed %d calls, want %d (the third was refused)", got, DefaultDoomLoopThreshold-1)
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
		t.Fatalf("TurnResult.ParkReason = %v, want doom_loop", result.ParkReason)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_trip_tool_breaker
//
// P0-14 half: a queen never escalates, even when a host mistakenly wires a
// sink; the pre-dispatch breaker still refuses the third identical call.
func TestQueenWithEscalationSinkStillGetsBreakerRefusal(t *testing.T) {
	const args = `{"path":"notes.md","content":"same"}`
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("write_file", args, DefaultDoomLoopThreshold)}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	recorder := &escalationRecorder{requestID: "never"}
	ctx := judgeTestContext(completer, nil, "write_file")
	ctx.StreamID = "queen"
	ctx.EscalationSink = recorder.sink

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if !parked.Breaker {
		t.Fatal("queen with an escalation sink did not get the breaker refusal")
	}
	if got := executor.count(); got != DefaultDoomLoopThreshold-1 {
		t.Fatalf("executed %d calls, want %d (the third was refused)", got, DefaultDoomLoopThreshold-1)
	}
	if requests := recorder.snapshot(); len(requests) != 0 {
		t.Fatalf("queen escalation requests = %#v, want none", requests)
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
		t.Fatalf("TurnResult.ParkReason = %v, want doom_loop", result.ParkReason)
	}
}

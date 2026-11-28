package agentloop

// Regression tests for the replay breaker / exemption table (P0-14).
//
// Upstream runs a two-tier guard before dispatch (agent_loop.py:5443-5527):
// below ``tool_doom_loop_threshold`` a repeated identical call still executes
// but its result carries a "[Replay detected]" steer; at the threshold the
// breaker refuses the call, stubs the whole batch and parks as DOOM_LOOP. Both
// tiers and the turn-level doom-loop fingerprint guard skip
// ``LoopConfig.replay_exempt_tools`` (types.py:297-332).
//
// The gap these tests close: the port only had the post-execution detector, so
// (a) a healthy ``terminal_job_logs`` poll parked the turn at five reads and
// (b) a repeated side-effecting call ran every time the model re-issued it.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// replayRecorder is a tool executor that records the calls it actually ran and
// answers each with one canned result. It is the load-bearing assertion for
// "was this call refused before execution?".
type replayRecorder struct {
	mu     sync.Mutex
	calls  []ToolCallRequest
	result ToolResult
}

func (r *replayRecorder) execute(req ToolCallRequest) (ToolResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	res := r.result
	if res.Content == "" {
		res.Content = "ok"
	}
	return res, nil
}

func (r *replayRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *replayRecorder) countNamed(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := 0
	for _, call := range r.calls {
		if call.Name == name {
			seen++
		}
	}
	return seen
}

// replayToolResults collects the stored tool results in conversation order.
func replayToolResults(t *testing.T, conv *NodeConversation) []Message {
	t.Helper()
	var out []Message
	for _, message := range conv.Messages() {
		if message.Role == RoleTool {
			out = append(out, message)
		}
	}
	return out
}

// repeatedToolScenarios scripts n identical single-call responses followed by a
// text turn so the loop can finish.
func repeatedToolScenarios(name, args string, n int) []scriptedResponse {
	scenarios := make([]scriptedResponse, 0, n+1)
	for i := 0; i < n; i++ {
		scenarios = append(scenarios, scriptedResponse{toolCalls: []llm.ToolCall{
			{ID: name + "_" + strconv.Itoa(i), Name: name, Arguments: args},
		}})
	}
	return append(scenarios, scriptedResponse{content: "done"})
}

// Parity: framework/agent_loop/internals/types.py::LoopConfig.replay_exempt_tools
//
// The table is a single source of truth shared by the replay breaker and the
// doom-loop fingerprint guard, and it must survive a partially-built config
// (a nil map means "upstream defaults", not "no exemptions").
func TestReplayExemptionTableMirrorsUpstream(t *testing.T) {
	upstream := []string{
		"ask_user",
		"escalate",
		"collect_result",
		"terminal_job_logs",
		"terminal_job_manage",
		"terminal_output_get",
		"search_tools",
		"search_messages",
		"get_current_time",
		"get_account_info",
	}
	var zero LoopConfig
	for _, name := range upstream {
		if !zero.ReplayExempt(name) {
			t.Fatalf("zero-value LoopConfig does not exempt %q", name)
		}
	}
	// The port's documented breaker-only exemption: the read-only task tools its
	// worker reminder path asks the model to consult. The deterministic detector
	// still counts them (ReplayExempt is false), and task writes stay guarded by
	// both guards.
	for _, name := range []string{"task_list", "task_get"} {
		if !zero.ReplayBreakerExempt(name) {
			t.Fatalf("zero-value LoopConfig does not breaker-exempt the task read %q", name)
		}
		if zero.ReplayExempt(name) {
			t.Fatalf("%q must stay visible to the doom-loop detector", name)
		}
	}
	// Mutating tools stay guarded: they are the calls the breaker exists for.
	for _, name := range []string{"terminal_exec", "write_file", "edit_file", "send_email", "task_create", "task_update"} {
		if zero.ReplayExempt(name) || zero.ReplayBreakerExempt(name) {
			t.Fatalf("%q must stay guarded", name)
		}
	}
	// An explicit empty table is the documented opt-out.
	empty := LoopConfig{ReplayExemptTools: map[string]struct{}{}}
	if empty.ReplayExempt("terminal_job_logs") {
		t.Fatal("explicit empty exemption table still exempted a poll tool")
	}
	custom := LoopConfig{ReplayExemptTools: map[string]struct{}{"my_poll": {}}}
	if !custom.ReplayExempt("my_poll") {
		t.Fatal("custom exemption table did not exempt my_poll")
	}
	if custom.ReplayExempt("terminal_job_logs") {
		t.Fatal("custom exemption table fell back to the defaults")
	}

	config := DefaultLoopConfig()
	if !config.ReplayDetectorEnabled {
		t.Fatal("DefaultLoopConfig disabled the replay detector")
	}
	if got := config.ReplayDetectorWithinLastTurns; got != DefaultReplayDetectorWithinLastTurns {
		t.Fatalf("ReplayDetectorWithinLastTurns = %d, want %d", got, DefaultReplayDetectorWithinLastTurns)
	}
	if DefaultReplayDetectorWithinLastTurns != 3 {
		t.Fatalf("DefaultReplayDetectorWithinLastTurns = %d, want upstream's 3", DefaultReplayDetectorWithinLastTurns)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' doom-loop
// filter on replay_exempt_tools)
//
// (a) A successful poll repeated past the park threshold is a healthy turn.
// Before the exemption table, six identical terminal_job_logs reads parked the
// loop with ParkReasonDoomLoop at the fifth.
func TestExemptJobPollRepeatedPastTheParkThresholdIsNotParked(t *testing.T) {
	const polls = DefaultDoomLoopParkThreshold + 1
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("terminal_job_logs", `{"job_id":"job-1"}`, polls)}
	executor := &replayRecorder{result: ToolResult{Content: "still running"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	var parks []ParkReason
	loop.ParkSink = func(reason ParkReason, _ string) { parks = append(parks, reason) }
	conv := NewNodeConversation("system")

	result, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "terminal_job_logs"), conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("poll turn did not succeed: %+v", result)
	}
	if got := executor.countNamed("terminal_job_logs"); got != polls {
		t.Fatalf("polls executed = %d, want %d (every read is legitimate)", got, polls)
	}
	if len(parks) != 0 {
		t.Fatalf("ParkSink saw %v, want no park for an exempt poll", parks)
	}
	if result.ParkReason != nil {
		t.Fatalf("TurnResult.ParkReason = %v, want nil", *result.ParkReason)
	}
	if got := loop.Counter("doom_loop_parked"); got != 0 {
		t.Fatalf("stats[doom_loop_parked] = %d, want 0", got)
	}
	// Exempt tools are skipped by the soft tier too: no steer noise on reads.
	for _, record := range replayToolResults(t, conv) {
		if strings.Contains(record.Content, "[Replay detected") {
			t.Fatalf("exempt poll result carried a replay steer: %s", record.Content)
		}
	}
}

// Parity: framework/agent_loop/agent_loop.py::_trip_tool_breaker
// (test_breaker_trips_on_within_turn_duplicates: a single response with N
// identical calls collapses to zero executions)
//
// (b) A repeated failing side effect must not run N times. The batch-wide
// duplicate count reaches the threshold on the first call, so the whole batch
// is refused and every tool_use still gets an is_error stub.
func TestReplayBreakerRefusesRepeatedFailingSideEffect(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "exec_0", Name: "terminal_exec", Arguments: `{"command":"rm -rf build"}`},
		{ID: "exec_1", Name: "terminal_exec", Arguments: `{"command":"rm -rf build"}`},
		{ID: "exec_2", Name: "terminal_exec", Arguments: `{"command":"rm -rf build"}`},
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "unreachable"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "command failed", IsError: true}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	conv := NewNodeConversation("system")

	result, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "terminal_exec"), conv, nil)

	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if !parked.Breaker {
		t.Fatal("breaker park reports Breaker = false")
	}
	if parked.Tool != "terminal_exec" || parked.Streak != len(calls) {
		t.Fatalf("parked call = %q streak %d, want terminal_exec/%d", parked.Tool, parked.Streak, len(calls))
	}
	if parked.ParkReason() != ParkReasonDoomLoop {
		t.Fatalf("ParkReason() = %q, want doom_loop", parked.ParkReason())
	}
	if !strings.Contains(parked.Error(), "NOT executed") {
		t.Fatalf("breaker error does not say the call was refused: %s", parked.Error())
	}
	// The load-bearing assertion: no side effect ran.
	if got := executor.count(); got != 0 {
		t.Fatalf("side effect executed %d time(s), want 0", got)
	}
	stored := replayToolResults(t, conv)
	if len(stored) != len(calls) {
		t.Fatalf("stored tool results = %d, want one stub per tool_use (%d)", len(stored), len(calls))
	}
	for i, message := range stored {
		if !message.IsError {
			t.Fatalf("stub %d is not an error: %s", i, message.Content)
		}
		if !strings.Contains(message.Content, "Breaker tripped") &&
			!strings.Contains(message.Content, "cancelled") {
			t.Fatalf("stub %d does not explain the refusal: %s", i, message.Content)
		}
	}
	if result.Success {
		t.Fatal("parked turn reports Success = true")
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
		t.Fatalf("TurnResult.ParkReason = %v, want doom_loop", result.ParkReason)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 1 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 1", got)
	}
	if got := completer.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (the park stops the turn)", got)
	}
	// The model must see on resume that the call did not run.
	messages := conv.Messages()
	last := messages[len(messages)-1]
	if last.Role != RoleUser || !strings.Contains(last.Content, "Tool breaker tripped") {
		t.Fatalf("last message = %+v, want the [SYSTEM] breaker explanation", last)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (streak >=
// threshold trips unconditionally) and ::_trip_tool_breaker
//
// Cross-turn repeats are refused before dispatch for every stream shape, a
// colony worker included. The old port let a worker execute the third
// identical call and left the refusal to the doom-loop detector; upstream's
// breaker is unconditional, so the third side effect must not run. The park
// (DoomLoopParkedError) is the terminal bound, exactly as upstream's
// TurnCancelled park is.
func TestCrossTurnReplayBreakerRefusesWorkerThirdIdenticalCall(t *testing.T) {
	const args = `{"path":"notes.md","content":"same"}`
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("write_file", args, DefaultDoomLoopThreshold)}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	agentCtx := judgeTestContext(completer, nil, "write_file")
	agentCtx.StreamID = "worker:replay"
	// A worker keeps an escalation channel; it must not buy the call an
	// exemption the breaker does not have upstream.
	agentCtx.EscalationSink = func(EscalationRequest) (string, error) { return "escalation-1", nil }
	// Each turn ends after its one-call batch, so the repeats span turns.
	agentCtx.StopAfterToolBatch = func() bool { return true }
	conv := NewNodeConversation("system")

	var parked *DoomLoopParkedError
	for turn := 0; turn < DefaultDoomLoopThreshold; turn++ {
		result, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil)
		if err == nil {
			if !result.Success {
				t.Fatalf("turn %d ended without a park or a success: %+v", turn, result)
			}
			continue
		}
		var parkErr *DoomLoopParkedError
		if !errors.As(err, &parkErr) {
			t.Fatalf("turn %d: ExecuteTurn error = %v (%T), want *DoomLoopParkedError", turn, err, err)
		}
		parked = parkErr
		if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
			t.Fatalf("turn %d: TurnResult.ParkReason = %v, want doom_loop", turn, result.ParkReason)
		}
		break
	}
	if parked == nil {
		t.Fatalf("executed %d identical calls without a breaker trip; want the third refused",
			executor.count())
	}
	if !parked.Breaker || parked.Streak != DefaultDoomLoopThreshold || parked.Tool != "write_file" {
		t.Fatalf("park = %+v, want a write_file breaker trip at %d", parked, DefaultDoomLoopThreshold)
	}
	if got := executor.count(); got != DefaultDoomLoopThreshold-1 {
		t.Fatalf("executed %d calls, want %d (the third was refused before dispatch)",
			got, DefaultDoomLoopThreshold-1)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 1 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 1", got)
	}
	results := replayToolResults(t, conv)
	if len(results) != DefaultDoomLoopThreshold {
		t.Fatalf("stored tool results = %d, want one per tool_use (%d)", len(results), DefaultDoomLoopThreshold)
	}
	last := results[len(results)-1]
	if !last.IsError || !strings.HasPrefix(last.Content, "[Breaker tripped: write_file") {
		t.Fatalf("last tool result = %#v, want the breaker stub", last)
	}
	var systemNotes int
	for _, message := range conv.Messages() {
		if message.Role == RoleUser && strings.Contains(message.Content, "[SYSTEM] Tool breaker tripped") {
			systemNotes++
		}
	}
	if systemNotes != 1 {
		t.Fatalf("breaker system notes = %d, want 1 explaining the refusal", systemNotes)
	}
}

// Parity: framework/agent_loop/conversation.py::count_consecutive_completed_tool_calls
// ("A turn that does NOT include a matching call breaks the streak"; a text-only
// turn breaks it).
//
// The breaker shares the detector's reset boundary: a text-only assistant turn
// between two identical calls clears the streak, so the repeat after it is a
// fresh first occurrence and executes instead of being refused.
func TestCrossTurnReplayBreakerResetsAtTextOnlyTurn(t *testing.T) {
	const args = `{"path":"notes.md","content":"same"}`
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		toolCallResponse("a0", "write_file", args),
		{content: "pausing to reconsider"},
		toolCallResponse("a1", "write_file", args),
	}}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	agentCtx := judgeTestContext(completer, nil, "write_file")
	// A queen-shaped stream ends cleanly on the text-only turn; a worker would
	// nudge and continue inside the same turn, which is a different boundary.
	agentCtx.StreamID = "queen"
	agentCtx.StopAfterToolBatch = func() bool { return true }
	conv := NewNodeConversation("system")

	for turn := 0; turn < 3; turn++ {
		result, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil)
		if err != nil {
			t.Fatalf("turn %d: ExecuteTurn: %v", turn, err)
		}
		if !result.Success {
			t.Fatalf("turn %d = %+v, want success", turn, result)
		}
	}
	if got := executor.count(); got != 2 {
		t.Fatalf("executed %d calls, want 2 (the text-only turn reset the streak)", got)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 0 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 0 across the text-only boundary", got)
	}
}

// Parity: framework/agent_loop/internals/types.py::LoopConfig.replay_exempt_tools
//
// Negative regression for the cross-turn half: the exemption table still
// applies across turns, so a job poll that legitimately repeats identical
// calls executes every time instead of tripping the breaker.
func TestCrossTurnReplayBreakerStillExemptsPolls(t *testing.T) {
	const args = `{"job_id":"job-1"}`
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("terminal_job_logs", args, 5)}
	executor := &replayRecorder{result: ToolResult{Content: "running"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	agentCtx := judgeTestContext(completer, nil, "terminal_job_logs")
	agentCtx.StreamID = "worker:replay"
	agentCtx.StopAfterToolBatch = func() bool { return true }
	conv := NewNodeConversation("system")

	for turn := 0; turn < 5; turn++ {
		result, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil)
		if err != nil {
			t.Fatalf("turn %d: ExecuteTurn: %v", turn, err)
		}
		if !result.Success {
			t.Fatalf("turn %d = %+v, want success", turn, result)
		}
	}
	if got := executor.count(); got != 5 {
		t.Fatalf("executed %d polls, want 5 (the exemption is cross-turn)", got)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 0 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 0 for an exempt poll", got)
	}
}

// Parity: framework/agent_loop/internals/types.py::LoopConfig.replay_detector_enabled
//
// Negative regression: disabling the detector disables the cross-turn refusal
// too; the call executes even at the threshold.
func TestCrossTurnReplayBreakerHonorsDisabledDetector(t *testing.T) {
	const args = `{"path":"notes.md","content":"same"}`
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("write_file", args, DefaultDoomLoopThreshold)}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	config := DefaultLoopConfig()
	config.ReplayDetectorEnabled = false
	loop := NewAgentLoop(config, executor.execute)
	agentCtx := judgeTestContext(completer, nil, "write_file")
	agentCtx.StreamID = "worker:replay"
	agentCtx.StopAfterToolBatch = func() bool { return true }
	conv := NewNodeConversation("system")

	for turn := 0; turn < DefaultDoomLoopThreshold; turn++ {
		result, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil)
		if err != nil {
			t.Fatalf("turn %d: ExecuteTurn: %v", turn, err)
		}
		if !result.Success {
			t.Fatalf("turn %d = %+v, want success", turn, result)
		}
	}
	if got := executor.count(); got != DefaultDoomLoopThreshold {
		t.Fatalf("executed %d calls, want %d with the detector disabled", got, DefaultDoomLoopThreshold)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 0 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 0", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_trip_tool_breaker
// (cross-turn half). A stream with no escalation audience (the queen) refuses
// the third identical call before dispatch instead of running it and parking
// later; the queen's park is the terminal bound, exactly like upstream.
func TestCrossTurnReplayBreakerRefusesQueenThirdIdenticalCall(t *testing.T) {
	const args = `{"path":"notes.md","content":"same"}`
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("write_file", args, 3)}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	agentCtx := judgeTestContext(completer, nil, "write_file")
	agentCtx.StreamID = "queen"

	result, err := loop.ExecuteTurn(context.Background(),
		agentCtx, NewNodeConversation("system"), nil)

	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if !parked.Breaker || parked.Streak != DefaultDoomLoopThreshold {
		t.Fatalf("park = %+v, want a breaker trip at %d", parked, DefaultDoomLoopThreshold)
	}
	if got := executor.count(); got != DefaultDoomLoopThreshold-1 {
		t.Fatalf("executed %d calls, want %d (the third was refused before dispatch)", got, DefaultDoomLoopThreshold-1)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 1 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 1", got)
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
		t.Fatalf("TurnResult.ParkReason = %v, want doom_loop", result.ParkReason)
	}
}

// replayEventRecorder captures the tool_call_replay_detected payloads the loop
// hands to its writer.
type replayEventRecorder struct {
	mu     sync.Mutex
	events []replayEvent
}

type replayEvent struct {
	streamID    string
	nodeID      string
	toolName    string
	priorSeq    int
	executionID *string
}

func (r *replayEventRecorder) EmitToolCallReplayDetected(streamID, nodeID, toolName string, priorSeq int, executionID *string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, replayEvent{streamID, nodeID, toolName, priorSeq, executionID})
}

func (r *replayEventRecorder) snapshot() []replayEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]replayEvent(nil), r.events...)
}

// eventbus.EventBus must satisfy the loop's writer without this package
// importing the bus.
var _ ReplayEventWriter = (*eventbus.EventBus)(nil)

// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected and
// framework/agent_loop/agent_loop.py::_run_turn_loop (only a cross-turn prior
// emits; a purely in-batch duplicate does not).
//
// Two turns stay below the breaker threshold (prior 1 + in-batch 1 = 2), so the
// second repeat executes with the steer and publishes the event. A third would
// be refused before dispatch, which TestCrossTurnReplayBreakerRefusesWorker-
// ThirdIdenticalCall pins.
func TestReplaySteerEmitsToolCallReplayDetectedForCrossTurnPrior(t *testing.T) {
	const args = `{"path":"notes.md","content":"same"}`
	completer := &scriptedCompleter{scenarios: repeatedToolScenarios("write_file", args, 2)}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	writer := &replayEventRecorder{}
	loop.ReplayEvents = writer
	agentCtx := judgeTestContext(completer, nil, "write_file")
	agentCtx.StreamID = "worker:replay"
	agentCtx.EscalationSink = func(EscalationRequest) (string, error) { return "escalation-1", nil }
	agentCtx.StopAfterToolBatch = func() bool { return true }
	conv := NewNodeConversation("system")

	for turn := 0; turn < 2; turn++ {
		if _, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil); err != nil {
			t.Fatalf("turn %d: ExecuteTurn: %v", turn, err)
		}
	}
	events := writer.snapshot()
	if len(events) != 1 {
		t.Fatalf("replay events = %d, want 1 (the second turn had a prior success)", len(events))
	}
	for i, event := range events {
		if event.toolName != "write_file" {
			t.Fatalf("event[%d].toolName = %q, want write_file", i, event.toolName)
		}
		if event.streamID != "worker:replay" || event.nodeID != agentCtx.AgentID {
			t.Fatalf("event[%d] stamps = %q/%q", i, event.streamID, event.nodeID)
		}
		if event.priorSeq <= 0 {
			t.Fatalf("event[%d].priorSeq = %d, want the prior assistant message seq", i, event.priorSeq)
		}
		if event.executionID == nil || *event.executionID != "exec-1" {
			t.Fatalf("event[%d].executionID = %v, want exec-1", i, event.executionID)
		}
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (in-batch
// duplicates steer but have no prior to cite, so upstream emits no event).
func TestReplayInBatchDuplicateDoesNotEmitReplayDetectedEvent(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "r0", Name: "read_file", Arguments: `{"path":"a.txt"}`},
		{ID: "r1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	writer := &replayEventRecorder{}
	loop.ReplayEvents = writer

	if _, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "read_file"), NewNodeConversation("system"), nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if events := writer.snapshot(); len(events) != 0 {
		t.Fatalf("replay events = %+v, want none for an in-batch-only duplicate", events)
	}
	if got := loop.Counter("tool_call_replay_detected"); got != len(calls) {
		t.Fatalf("stats[tool_call_replay_detected] = %d, want %d", got, len(calls))
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (test_breaker_no_trip_on_different_args)
//
// (c) Unrelated calls are unaffected: the same tool with different arguments and
// a different tool in between never build a streak.
func TestReplayBreakerLeavesUnrelatedCallsAlone(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "r0", Name: "read_file", Arguments: `{"path":"a.txt"}`}}},
		{toolCalls: []llm.ToolCall{{ID: "r1", Name: "read_file", Arguments: `{"path":"b.txt"}`}}},
		{toolCalls: []llm.ToolCall{{ID: "r2", Name: "terminal_job_logs", Arguments: `{"job_id":"job-1"}`}}},
		{toolCalls: []llm.ToolCall{{ID: "r3", Name: "read_file", Arguments: `{"path":"a.txt"}`}}},
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	var parks []ParkReason
	loop.ParkSink = func(reason ParkReason, _ string) { parks = append(parks, reason) }

	result, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "read_file", "terminal_job_logs"), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("unrelated calls did not complete: %+v", result)
	}
	if got := executor.count(); got != 4 {
		t.Fatalf("executed %d calls, want 4 (none repeated consecutively)", got)
	}
	if len(parks) != 0 {
		t.Fatalf("ParkSink saw %v, want no park", parks)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 0 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 0", got)
	}
	if got := loop.Counter("tool_call_replay_detected"); got != 0 {
		t.Fatalf("stats[tool_call_replay_detected] = %d, want 0 (no repetition)", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (soft tier,
// replay_prefixes_by_id)
//
// Below the threshold the duplicate still executes, but the stored result says
// so — the soft tier is a steer, never a refusal.
func TestReplaySteerMarksInBatchDuplicatesWithoutRefusingThem(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "r0", Name: "read_file", Arguments: `{"path":"a.txt"}`},
		{ID: "r1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	conv := NewNodeConversation("system")

	result, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "read_file"), conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("duplicate below the threshold did not complete: %+v", result)
	}
	if got := executor.count(); got != len(calls) {
		t.Fatalf("executed %d calls, want %d (below the breaker threshold)", got, len(calls))
	}
	stored := replayToolResults(t, conv)
	if len(stored) != len(calls) {
		t.Fatalf("stored tool results = %d, want %d", len(stored), len(calls))
	}
	for i, message := range stored {
		if !strings.HasPrefix(message.Content, "[Replay detected: read_file matches 0 prior consecutive turn(s) + 1 duplicate(s) in this batch.") {
			t.Fatalf("result %d missing the replay steer: %s", i, message.Content)
		}
		if message.IsError {
			t.Fatalf("result %d was marked an error by the soft tier", i)
		}
	}
	if got := loop.Counter("tool_call_replay_detected"); got != len(calls) {
		t.Fatalf("stats[tool_call_replay_detected] = %d, want %d", got, len(calls))
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (replay_prefixes_by_id is applied only when `not result.is_error`; the
// detection and event still count/increment).
func TestReplaySteerSkipsErrorResultsButStillCountsDetection(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "e0", Name: "read_file", Arguments: `{"path":"missing.txt"}`},
		{ID: "e1", Name: "read_file", Arguments: `{"path":"missing.txt"}`},
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "not found", IsError: true}}
	loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
	conv := NewNodeConversation("system")

	result, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "read_file"), conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn failed: %+v", result)
	}
	for _, message := range replayToolResults(t, conv) {
		if strings.Contains(message.Content, "[Replay detected") {
			t.Fatalf("error result carried a replay steer: %s", message.Content)
		}
	}
	if got := loop.Counter("tool_call_replay_detected"); got != len(calls) {
		t.Fatalf("stats[tool_call_replay_detected] = %d, want %d (detection still counted)", got, len(calls))
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (batch_dup_count is a Counter over the whole tool_calls response, before the
// grace/budget gates skip any call). A hard stop that defers two of three
// identical calls must not hide the in-batch repetition from the breaker.
func TestReplayBatchDuplicateCountIncludesDeferredCalls(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "d0", Name: "terminal_exec", Arguments: `{"command":"rm -rf build"}`},
		{ID: "d1", Name: "terminal_exec", Arguments: `{"command":"rm -rf build"}`},
		{ID: "d2", Name: "terminal_exec", Arguments: `{"command":"rm -rf build"}`},
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "unreachable"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "ok"}}
	config := DefaultLoopConfig()
	config.ToolCallBudget = 1
	config.ToolCallHardLimit = 1
	loop := NewAgentLoop(config, executor.execute)
	conv := NewNodeConversation("system")

	result, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "terminal_exec"), conv, nil)
	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if !parked.Breaker {
		t.Fatal("breaker did not trip on the batch-wide duplicate count")
	}
	if got := executor.count(); got != 0 {
		t.Fatalf("executed %d calls, want 0 (the batch-wide dup trip refused the admitted call)", got)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 1 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 1", got)
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
		t.Fatalf("TurnResult.ParkReason = %v, want doom_loop", result.ParkReason)
	}
}

// Parity: framework/agent_loop/agent_loop.py (replay_detector_enabled=False)
//
// The legacy opt-out stays inert: with the detector off every repetition
// executes (the turn-level fingerprint guard is a separate, later guard).
func TestReplayDetectorDisabledExecutesEveryRepetition(t *testing.T) {
	const repeats = DefaultDoomLoopThreshold
	config := DefaultLoopConfig()
	config.ReplayDetectorEnabled = false
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "w0", Name: "write_file", Arguments: `{"path":"a.txt","content":"x"}`},
			{ID: "w1", Name: "write_file", Arguments: `{"path":"a.txt","content":"x"}`},
			{ID: "w2", Name: "write_file", Arguments: `{"path":"a.txt","content":"x"}`},
		}},
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "written"}}
	loop := NewAgentLoop(config, executor.execute)

	result, err := loop.ExecuteTurn(context.Background(),
		judgeTestContext(completer, nil, "write_file"), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("detector-off turn did not complete: %+v", result)
	}
	if got := executor.count(); got != repeats {
		t.Fatalf("executed %d calls, want %d with the detector off", got, repeats)
	}
	if got := loop.Counter("replay_breaker_tripped"); got != 0 {
		t.Fatalf("stats[replay_breaker_tripped] = %d, want 0", got)
	}
	if got := loop.Counter("tool_call_replay_detected"); got != 0 {
		t.Fatalf("stats[tool_call_replay_detected] = %d, want 0", got)
	}
}

// Parity-exempt: Go's worker task reminder path. Upstream's task tools live
// outside its replay exemption table, but its table's stated intent exempts
// "idempotent reads/observers". A Go worker's per-turn task reminder tells the
// worker to consult its own list, so a worker that reads task_list (or one task
// via task_get) on every reminder turn is doing what the reminder asked; the
// breaker must not park it. Task writes stay guarded, and the mutating case is
// covered by TestCrossTurnReplayBreakerRefusesWorkerThirdIdenticalCall.
func TestWorkerTaskReadsSurviveRepeatedReminderTurns(t *testing.T) {
	for _, tool := range []string{"task_list", "task_get"} {
		t.Run(tool, func(t *testing.T) {
			args := `{}`
			if tool == "task_get" {
				args = `{"id":1}`
			}
			completer := &scriptedCompleter{scenarios: repeatedToolScenarios(tool, args, 5)}
			executor := &replayRecorder{result: ToolResult{Content: "- #1. [pending] collect data"}}
			loop := NewAgentLoop(DefaultLoopConfig(), executor.execute)
			agentCtx := judgeTestContext(completer, nil, tool)
			agentCtx.StreamID = "worker:replay"
			agentCtx.StopAfterToolBatch = func() bool { return true }
			conv := NewNodeConversation("system")

			for turn := 0; turn < 5; turn++ {
				result, err := loop.ExecuteTurn(context.Background(), agentCtx, conv, nil)
				if err != nil {
					t.Fatalf("turn %d: ExecuteTurn: %v", turn, err)
				}
				if !result.Success {
					t.Fatalf("turn %d = %+v, want success", turn, result)
				}
			}
			if got := executor.count(); got != 5 {
				t.Fatalf("executed %d %s reads, want 5 (the reminder path is exempt)", got, tool)
			}
			if got := loop.Counter("replay_breaker_tripped"); got != 0 {
				t.Fatalf("stats[replay_breaker_tripped] = %d, want 0 for a task read", got)
			}
		})
	}
}

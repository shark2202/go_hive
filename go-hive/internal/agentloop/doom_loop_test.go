package agentloop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity-exempt: upstream detects stalls with a judge (framework/server/
// stall_check.py) and has no fingerprint rule; this locks the deterministic
// half — argument order must not disguise a repeated call.
func TestToolCallFingerprintIgnoresArgumentOrder(t *testing.T) {
	a := ToolCallFingerprint("read_file", map[string]any{"path": "a.txt", "limit": 10})
	b := ToolCallFingerprint("read_file", map[string]any{"limit": 10, "path": "a.txt"})
	if a != b {
		t.Fatalf("fingerprint depends on map order:\n%s\n%s", a, b)
	}
	if a == ToolCallFingerprint("read_file", map[string]any{"path": "a.txt", "limit": 11}) {
		t.Fatal("different arguments fingerprint the same")
	}
	if a == ToolCallFingerprint("write_file", map[string]any{"path": "a.txt", "limit": 10}) {
		t.Fatal("different tools fingerprint the same")
	}
}

// Parity-exempt: one reminder per streak, at the threshold, never on the calls
// before it — a second reminder for the same repetition would be noise.
func TestDoomLoopDetectorWarnsOncePerStreak(t *testing.T) {
	detector := NewDoomLoopDetector()
	call := map[string]any{"path": "a.txt"}
	for i := 1; i < DefaultDoomLoopThreshold; i++ {
		if warning := detector.Observe("read_file", call); warning != "" {
			t.Fatalf("call %d warned before the threshold: %s", i, warning)
		}
	}
	warning := detector.Observe("read_file", call)
	if warning == "" {
		t.Fatal("no warning at the threshold")
	}
	if !strings.Contains(warning, "`read_file`") || !strings.Contains(warning, "3 times") {
		t.Fatalf("warning does not name the call and the streak: %s", warning)
	}
	if again := detector.Observe("read_file", call); again != "" {
		t.Fatalf("second warning in the same streak: %s", again)
	}
	if got := detector.Warnings(); got != 1 {
		t.Fatalf("Warnings() = %d, want 1", got)
	}
	if !detector.Active() {
		t.Fatal("Active() = false inside the repeated streak")
	}

	// A different call breaks the streak; going back to the first call starts
	// a fresh one and can warn again.
	if warning := detector.Observe("read_file", map[string]any{"path": "b.txt"}); warning != "" {
		t.Fatalf("a changed call warned: %s", warning)
	}
	if got := detector.Streak(); got != 1 {
		t.Fatalf("Streak() = %d after a changed call, want 1", got)
	}
	if detector.Active() {
		t.Fatal("Active() = true after the streak broke")
	}
	for i := 1; i < DefaultDoomLoopThreshold; i++ {
		detector.Observe("read_file", call)
	}
	if warning := detector.Observe("read_file", call); warning == "" {
		t.Fatal("a fresh streak did not warn")
	}
	if got := detector.Warnings(); got != 2 {
		t.Fatalf("Warnings() = %d, want 2", got)
	}
}

// Parity-exempt: Reset is the per-turn boundary; the streak must not survive
// into the next user message.
func TestDoomLoopDetectorReset(t *testing.T) {
	detector := NewDoomLoopDetector()
	for i := 0; i < DefaultDoomLoopThreshold; i++ {
		detector.Observe("read_file", map[string]any{"path": "a.txt"})
	}
	if !detector.Active() {
		t.Fatal("detector is not active after a full streak")
	}
	detector.Reset()
	if detector.Active() || detector.Streak() != 0 {
		t.Fatalf("Reset left streak=%d active=%v", detector.Streak(), detector.Active())
	}
	if detector.Warnings() != 1 {
		t.Fatalf("Reset cleared the warning count: %d", detector.Warnings())
	}
	// The threshold is configurable for callers that want a looser rule.
	strict := &DoomLoopDetector{Threshold: 2}
	strict.Observe("read_file", map[string]any{"path": "a.txt"})
	if warning := strict.Observe("read_file", map[string]any{"path": "a.txt"}); warning == "" {
		t.Fatal("Threshold=2 did not warn on the second identical call")
	}
}

// Parity-exempt: upstream only has the judge-based stall check; this pins the
// deterministic park the port adds. Past the park threshold the turn stops with
// ParkReasonDoomLoop instead of returning to the model for another repetition.
func TestDoomLoopParksTheTurnAtTheParkThreshold(t *testing.T) {
	const path = `{"path":"status.txt"}`
	scenarios := make([]scriptedResponse, 0, DefaultDoomLoopParkThreshold+1)
	for i := 0; i < DefaultDoomLoopParkThreshold+1; i++ {
		scenarios = append(scenarios, scriptedResponse{toolCalls: []llm.ToolCall{
			{ID: "call_" + strconv.Itoa(i), Name: "read_file", Arguments: path},
		}})
	}
	scenarios = append(scenarios, scriptedResponse{content: "unreachable"})
	completer := &scriptedCompleter{scenarios: scenarios}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 12}, successToolExecutor)
	var reasons []ParkReason
	var details []string
	loop.ParkSink = func(reason ParkReason, detail string) {
		reasons = append(reasons, reason)
		details = append(details, detail)
	}
	ctx := judgeTestContext(completer, nil, "read_file")

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}
	if parked.Tool != "read_file" || parked.Streak != DefaultDoomLoopParkThreshold {
		t.Fatalf("parked call = %q streak %d, want read_file/%d",
			parked.Tool, parked.Streak, DefaultDoomLoopParkThreshold)
	}
	if parked.ParkReason() != ParkReasonDoomLoop {
		t.Fatalf("ParkReason() = %q, want doom_loop", parked.ParkReason())
	}
	if result.Success {
		t.Fatal("parked turn reports Success = true")
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonDoomLoop {
		t.Fatalf("TurnResult.ParkReason = %v, want doom_loop", result.ParkReason)
	}
	if got := loop.Counter("doom_loop_parked"); got != 1 {
		t.Fatalf("stats[doom_loop_parked] = %d, want 1", got)
	}
	if len(reasons) != 1 || reasons[0] != ParkReasonDoomLoop {
		t.Fatalf("ParkSink saw %v, want exactly one doom_loop park", reasons)
	}
	if len(details) != 1 || !strings.Contains(details[0], "read_file") {
		t.Fatalf("park detail = %v, want the repeating call named", details)
	}
	// The loop stopped: the model was asked exactly once per repetition, and the
	// scenario after the park threshold was never consumed.
	if got := completer.callCount(); got != DefaultDoomLoopParkThreshold {
		t.Fatalf("model calls = %d, want %d (the park stops the turn)", got, DefaultDoomLoopParkThreshold)
	}
	if result.InnerTurns != DefaultDoomLoopParkThreshold {
		t.Fatalf("InnerTurns = %d, want %d", result.InnerTurns, DefaultDoomLoopParkThreshold)
	}
	// The advisory still rides the tool result where the streak tripped it, so
	// the transcript explains the repetition before the turn stops.
	advisories := 0
	for _, record := range result.ToolResults {
		if strings.Contains(record.Content, "identical arguments") {
			advisories++
		}
	}
	if advisories != 1 {
		t.Fatalf("doom-loop advisory appeared on %d tool results, want exactly 1", advisories)
	}
}

// Parity-exempt: a detector configured with a strict warning threshold must not
// park at or before the warning — the advisory is the model's one chance to
// change course, so the park sits strictly after it.
func TestDoomLoopParkThresholdNeverSitsBelowTheWarning(t *testing.T) {
	detector := &DoomLoopDetector{Threshold: DefaultDoomLoopParkThreshold, ParkThreshold: 2}
	call := map[string]any{"path": "a.txt"}
	for i := 1; i < DefaultDoomLoopParkThreshold; i++ {
		detector.Observe("read_file", call)
		if detector.ShouldPark() {
			t.Fatalf("parked at streak %d, before the warning threshold", i)
		}
	}
	detector.Observe("read_file", call)
	if !detector.Active() {
		t.Fatalf("streak %d is not active", detector.Streak())
	}
	if detector.ShouldPark() {
		t.Fatalf("parked at streak %d, the same call that warned", detector.Streak())
	}
	detector.Observe("read_file", call)
	if !detector.ShouldPark() {
		t.Fatalf("streak %d did not park the call after the warning", detector.Streak())
	}
}

// Parity-exempt: the loop must notice the repetition and keep going. The model
// here answers, then repeats one identical call past the threshold, then
// finishes — the turn succeeds and the reminder rides on the tool result.
func TestLoopWarnsOnRepeatedToolCallWithoutStopping(t *testing.T) {
	call := llm.ToolCall{ID: "call_1", Name: "read_file", Arguments: `{"path":"status.txt"}`}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "let me look", toolCalls: []llm.ToolCall{call}},
		{content: "again", toolCalls: []llm.ToolCall{llm.ToolCall{ID: "call_2", Name: "read_file", Arguments: `{"path":"status.txt"}`}}},
		{content: "again", toolCalls: []llm.ToolCall{llm.ToolCall{ID: "call_3", Name: "read_file", Arguments: `{"path":"status.txt"}`}}},
		{content: "all done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 6}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "read_file")
	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if got := loop.DoomLoop.Warnings(); got != 1 {
		t.Fatalf("DoomLoop.Warnings() = %d, want 1", got)
	}
	if got := loop.Stats()["doom_loop_warned"]; got != 1 {
		t.Fatalf("stats[doom_loop_warned] = %d, want 1", got)
	}

	warned := 0
	for _, record := range result.ToolResults {
		if strings.Contains(record.Content, "identical arguments") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("reminder appeared on %d tool results, want exactly 1", warned)
	}
	// The loop ran to the model's own answer: no hard stop, no error.
	if result.FinalText != "all done" {
		t.Fatalf("FinalText = %q, want the model's final answer", result.FinalText)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_escalation_requested with reason "Tool doom loop detected").
// The port adds the deterministic park as the terminal bound, so the worker is
// told once at the warning threshold and stopped at the park threshold.
func TestDoomLoopEscalatesOnceThroughTheWorkerSink(t *testing.T) {
	const path = `{"path":"status.txt"}`
	scenarios := make([]scriptedResponse, 0, DefaultDoomLoopParkThreshold)
	for i := 0; i < DefaultDoomLoopParkThreshold; i++ {
		scenarios = append(scenarios, scriptedResponse{toolCalls: []llm.ToolCall{
			{ID: "call_" + strconv.Itoa(i), Name: "read_file", Arguments: path},
		}})
	}
	recorder := &escalationRecorder{requestID: "req-doom"}
	completer := &scriptedCompleter{scenarios: scenarios}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "worker:doom"
	ctx.ExecutionID = "exec_doom"
	ctx.EscalationSink = recorder.sink

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	var parked *DoomLoopParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("ExecuteTurn error = %v (%T), want *DoomLoopParkedError", err, err)
	}

	requests := recorder.snapshot()
	if len(requests) != 1 {
		t.Fatalf("escalation requests = %d, want exactly 1 (one per streak): %#v", len(requests), requests)
	}
	request := requests[0]
	if request.StreamID != "worker:doom" || request.ExecutionID != "exec_doom" {
		t.Fatalf("escalation request = %#v, want the worker stream and execution", request)
	}
	if request.Reason != "Tool doom loop detected" {
		t.Fatalf("escalation reason = %q, want the upstream reason text", request.Reason)
	}
	if !strings.Contains(request.Context, "read_file") || !strings.Contains(request.Context, "3 times") {
		t.Fatalf("escalation context = %q, want the repeating call and streak", request.Context)
	}
	if got := loop.Counter("doom_loop_escalated"); got != 1 {
		t.Fatalf("stats[doom_loop_escalated] = %d, want 1", got)
	}

	// The model is told the queen was informed, exactly once, on the tool
	// result that also carried the advisory.
	notices := 0
	for _, record := range result.ToolResults {
		if strings.Contains(record.Content, doomLoopEscalationNotice) {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("escalation notice appeared on %d tool results, want exactly 1", notices)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_tool_doom_loop). The host sink fires once per turn-loop, on the
// same detection that escalates, and names the repeating call and streak.
func TestDoomLoopReportsOnceThroughTheEventSink(t *testing.T) {
	const path = `{"path":"status.txt"}`
	scenarios := make([]scriptedResponse, 0, DefaultDoomLoopParkThreshold)
	for i := 0; i < DefaultDoomLoopParkThreshold; i++ {
		scenarios = append(scenarios, scriptedResponse{toolCalls: []llm.ToolCall{
			{ID: "call_" + strconv.Itoa(i), Name: "read_file", Arguments: path},
		}})
	}
	type report struct {
		tool   string
		streak int
	}
	var reports []report
	completer := &scriptedCompleter{scenarios: scenarios}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "read_file")
	loop.DoomLoopSink = func(tool string, streak int) {
		reports = append(reports, report{tool: tool, streak: streak})
	}

	if _, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil); err == nil {
		t.Fatal("ExecuteTurn succeeded, want the doom-loop park")
	}
	if len(reports) != 1 {
		t.Fatalf("doom-loop reports = %#v, want exactly 1 for the streak", reports)
	}
	if reports[0].tool != "read_file" || reports[0].streak != DefaultDoomLoopThreshold {
		t.Fatalf("doom-loop report = %#v, want read_file at the warning threshold %d",
			reports[0], DefaultDoomLoopThreshold)
	}
	description := DoomLoopDescription(reports[0].tool, reports[0].streak)
	if !strings.Contains(description, "read_file") || !strings.Contains(description, "3 times") {
		t.Fatalf("description = %q, want the repeating call and streak", description)
	}
	if got := loop.Counter("doom_loop_reported"); got != 1 {
		t.Fatalf("stats[doom_loop_reported] = %d, want 1", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom loop
// only escalates when the fingerprint really repeats). A turn that keeps
// changing its arguments must not publish anything.
func TestDoomLoopWithoutRepetitionPublishesNoEscalation(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"a.txt"}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: "read_file", Arguments: `{"path":"b.txt"}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c3", Name: "read_file", Arguments: `{"path":"c.txt"}`}}},
		{content: "all done"},
	}}
	recorder := &escalationRecorder{requestID: "never"}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 8}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "worker:varied"
	ctx.EscalationSink = recorder.sink

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn failed: %#v", result)
	}
	if requests := recorder.snapshot(); len(requests) != 0 {
		t.Fatalf("escalation requests = %#v, want none without a repeated call", requests)
	}
	if got := loop.Counter("doom_loop_escalated"); got != 0 {
		t.Fatalf("stats[doom_loop_escalated] = %d, want 0", got)
	}
	if got := loop.Counter("doom_loop_reported"); got != 0 {
		t.Fatalf("stats[doom_loop_reported] = %d, want 0", got)
	}
}

// Parity-exempt: the port latches the automatic escalation once per turn so a
// worker that thrashes through several repeated-call streaks cannot flood the
// queen's inbox. Upstream clears its fingerprint window after escalating and
// can therefore escalate every streak; the latch is the anti-storm half.
func TestDoomLoopEscalationIsAtMostOnePerTurn(t *testing.T) {
	callA := llm.ToolCall{ID: "a", Name: "read_file", Arguments: `{"path":"a.txt"}`}
	callB := llm.ToolCall{ID: "b", Name: "read_file", Arguments: `{"path":"b.txt"}`}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{callA}},
		{toolCalls: []llm.ToolCall{callA}},
		{toolCalls: []llm.ToolCall{callA}}, // streak 1 → escalation
		{toolCalls: []llm.ToolCall{callB}}, // streak reset
		{toolCalls: []llm.ToolCall{callB}},
		{toolCalls: []llm.ToolCall{callB}}, // streak 2 → warning, latched
		{content: "done"},
	}}
	recorder := &escalationRecorder{requestID: "req-one"}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StreamID = "worker:thrash"
	ctx.EscalationSink = recorder.sink
	reported := 0
	loop.DoomLoopSink = func(string, int) { reported++ }

	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn failed: %#v", result)
	}
	if got := loop.DoomLoop.Warnings(); got != 2 {
		t.Fatalf("warnings = %d, want 2 (two detected streaks)", got)
	}
	if requests := recorder.snapshot(); len(requests) != 1 {
		t.Fatalf("escalation requests = %d, want 1 (the per-turn latch): %#v", len(requests), requests)
	}
	if reported != 1 {
		t.Fatalf("doom-loop event reports = %d, want 1 (the per-turn latch despite two streaks)", reported)
	}
}

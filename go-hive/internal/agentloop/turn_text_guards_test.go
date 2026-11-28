package agentloop

// Turn guards keyed on "no assistant text": the empty-response park and the
// silent-tool nudge. Both are driven through ExecuteTurn so the tests pin the
// call site and its ordering, not just the helper.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' empty
// response guard). An empty model answer is not a completed turn: the loop
// nudges the model and streams again, and only the following real answer ends
// the turn.
func TestEmptyResponseIsNotACompletedTurn(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{},                              // ghost stream: no text, no tools
		{content: "here is the answer"}, // real answer
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if result.FinalText != "here is the answer" {
		t.Fatalf("FinalText = %q, want the real answer (the empty turn must not finish the turn)", result.FinalText)
	}
	if got := completer.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (the loop must re-stream after an empty turn)", got)
	}
	if got := loop.Counter("empty_response"); got != 1 {
		t.Fatalf("empty_response counter = %d, want 1", got)
	}
	nudges := 0
	for _, message := range conv.Messages() {
		if message.Role == RoleUser && strings.Contains(message.Content, emptyResponseNudge) {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("empty-response nudges = %d, want 1", nudges)
	}
	// The nudge must land before the answer it provoked.
	messages := conv.Messages()
	nudgeAt, answerAt := -1, -1
	for i, message := range messages {
		if strings.Contains(message.Content, emptyResponseNudge) {
			nudgeAt = i
		}
		if message.Role == RoleAssistant && message.Content == "here is the answer" {
			answerAt = i
		}
	}
	if nudgeAt < 0 || answerAt < 0 || nudgeAt > answerAt {
		t.Fatalf("nudge at %d, answer at %d: want the nudge first", nudgeAt, answerAt)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' ghost
// stream park: `_consecutive_empty_turns >= stall_detection_threshold` →
// `_await_user_input(reason=ParkReason.EMPTY_RESPONSES)`)
func TestConsecutiveEmptyResponsesParkOnEmptyResponses(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{}, {}, {}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 6, StallDetectionThreshold: 3}, nil)
	var parks []ParkReason
	loop.ParkSink = func(reason ParkReason, detail string) {
		parks = append(parks, reason)
	}
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err == nil {
		t.Fatalf("turn must fail with the empty-response park, got %+v", result)
	}
	var parked *EmptyResponseParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("error = %v, want *EmptyResponseParkedError", err)
	}
	if parked.Consecutive != 3 || parked.Threshold != 3 {
		t.Fatalf("park error = %+v, want consecutive 3 of threshold 3", parked)
	}
	if result.Success {
		t.Fatalf("parked turn reported success: %+v", result)
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonEmptyResponses {
		t.Fatalf("TurnResult.ParkReason = %v, want empty_responses", result.ParkReason)
	}
	if len(parks) != 1 || parks[0] != ParkReasonEmptyResponses {
		t.Fatalf("parks = %v, want one empty_responses", parks)
	}
	if got := loop.Counter("empty_response_parked"); got != 1 {
		t.Fatalf("empty_response_parked counter = %d, want 1", got)
	}
	// Two nudges before the park: the third consecutive ghost stream parks
	// instead of nudging again.
	nudges := 0
	for _, message := range conv.Messages() {
		if strings.Contains(message.Content, emptyResponseNudge) {
			nudges++
		}
	}
	if nudges != 2 {
		t.Fatalf("nudges before the park = %d, want 2", nudges)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' — an empty
// model answer is accepted when the node declares output_keys and every
// required key is already set; the all-outputs-set branch returns success with
// no nudge and no park instead of treating the ghost stream as a stall).
//
// The live loop rejects set_output, so the satisfied state is seeded as a
// historical successful call, the shape a restored conversation carries.
func TestCompleteOutputKeysAcceptEmptyResponse(t *testing.T) {
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddAssistantMessage("", []map[string]any{{
		"id": "so1",
		"function": map[string]any{
			"name":      "set_output",
			"arguments": `{"key":"summary","value":"ok"}`,
		},
	}}, AssistantMessageOptions{}); err != nil {
		t.Fatalf("AddAssistantMessage: %v", err)
	}
	if _, err := conv.AddToolResult("so1", "Output 'summary' set successfully.", ToolResultOptions{}); err != nil {
		t.Fatalf("AddToolResult: %v", err)
	}

	completer := &scriptedCompleter{scenarios: []scriptedResponse{{}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	var parks []ParkReason
	loop.ParkSink = func(reason ParkReason, detail string) {
		parks = append(parks, reason)
	}
	ctx := judgeTestContext(completer, nil)
	ctx.AgentSpec.OutputKeys = []string{"summary"}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v (the empty turn must be accepted, not failed)", err)
	}
	if !result.Success {
		t.Fatalf("result = %#v, want success on the all-outputs-set accept branch", result)
	}
	if result.FinalText != "" {
		t.Fatalf("FinalText = %q, want empty: the accepted turn carried no assistant text", result.FinalText)
	}
	if result.InnerTurns != 1 {
		t.Fatalf("InnerTurns = %d, want 1 (the ghost stream is accepted immediately)", result.InnerTurns)
	}
	if result.ParkReason != nil {
		t.Fatalf("ParkReason = %v, want nil on acceptance", *result.ParkReason)
	}
	if got := completer.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (acceptance must not re-stream)", got)
	}
	if len(parks) != 0 {
		t.Fatalf("parks = %v, want none on acceptance", parks)
	}
	for _, message := range conv.Messages() {
		if message.Role == RoleUser && strings.Contains(message.Content, emptyResponseNudge) {
			t.Fatalf("empty-response nudge injected despite complete output_keys: %q", message.Content)
		}
	}
	if got := loop.Counter("empty_response"); got != 0 {
		t.Fatalf("empty_response counter = %d, want 0 (accepted turns are not empty-response stalls)", got)
	}
	if got := loop.Counter("empty_response_parked"); got != 0 {
		t.Fatalf("empty_response_parked counter = %d, want 0", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' — the
// "e.g. after calling set_output in a previous turn" case: a later turn whose
// first model call is empty accepts because the accumulator already carries
// every required key).
func TestEmptyResponseAcceptsOutputsSetInPreviousTurn(t *testing.T) {
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddAssistantMessage("", []map[string]any{{
		"id": "so1",
		"function": map[string]any{
			"name":      "set_output",
			"arguments": `{"key":"summary","value":"previous turn"}`,
		},
	}}, AssistantMessageOptions{}); err != nil {
		t.Fatalf("AddAssistantMessage: %v", err)
	}
	if _, err := conv.AddToolResult("so1", "Output 'summary' set successfully.", ToolResultOptions{}); err != nil {
		t.Fatalf("AddToolResult: %v", err)
	}

	completer := &scriptedCompleter{scenarios: []scriptedResponse{{}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	ctx := judgeTestContext(completer, nil)
	ctx.AgentSpec.OutputKeys = []string{"summary"}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.InnerTurns != 1 || result.FinalText != "" {
		t.Fatalf("result = %#v, want a one-turn empty acceptance", result)
	}
	if got := completer.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
	for _, message := range conv.Messages() {
		if message.Role == RoleUser && strings.Contains(message.Content, emptyResponseNudge) {
			t.Fatalf("empty-response nudge injected despite a complete accumulator: %q", message.Content)
		}
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' — a declared
// key that is still unset keeps the ghost-stream guard: nudge below the
// threshold, park at it. The accept branch must only fire when the key set is
// complete.
func TestMissingOutputKeysEmptyResponseNudgesThenParks(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{
			ID:        "r1",
			Name:      "read_file",
			Arguments: `{"path":"a.txt"}`,
		}}},
		{}, // "summary" is still missing -> nudge
		{}, // still missing -> park at threshold 2
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 6, StallDetectionThreshold: 2}, successToolExecutor)
	var parks []ParkReason
	loop.ParkSink = func(reason ParkReason, detail string) {
		parks = append(parks, reason)
	}
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.AgentSpec.OutputKeys = []string{"summary", "notes"}
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err == nil {
		t.Fatalf("turn must fail with the empty-response park, got %+v", result)
	}
	var parked *EmptyResponseParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("error = %v, want *EmptyResponseParkedError", err)
	}
	if parked.Consecutive != 2 || parked.Threshold != 2 {
		t.Fatalf("park error = %+v, want consecutive 2 of threshold 2", parked)
	}
	if result.Success {
		t.Fatalf("parked turn reported success: %+v", result)
	}
	if result.ParkReason == nil || *result.ParkReason != ParkReasonEmptyResponses {
		t.Fatalf("TurnResult.ParkReason = %v, want empty_responses", result.ParkReason)
	}
	if len(parks) != 1 || parks[0] != ParkReasonEmptyResponses {
		t.Fatalf("parks = %v, want one empty_responses", parks)
	}
	nudges := 0
	for _, message := range conv.Messages() {
		if message.Role == RoleUser && strings.Contains(message.Content, emptyResponseNudge) {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("empty-response nudges = %d, want 1 before the park", nudges)
	}
	if got := loop.Counter("empty_response"); got != 2 {
		t.Fatalf("empty_response counter = %d, want 2", got)
	}
	if got := loop.Counter("empty_response_parked"); got != 1 {
		t.Fatalf("empty_response_parked counter = %d, want 1", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' —
// `has_real_outputs` gate: with no declared output_keys `missing` is trivially
// empty, so the accept branch must not fire and the forever-alive node keeps
// the nudge behavior).
func TestNoOutputKeysEmptyResponseStillNudges(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{},
		{content: "still here"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	ctx := judgeTestContext(completer, nil)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "still here" {
		t.Fatalf("result = %#v, want the turn to continue after the nudge", result)
	}
	if got := completer.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	nudges := 0
	for _, message := range conv.Messages() {
		if message.Role == RoleUser && strings.Contains(message.Content, emptyResponseNudge) {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("empty-response nudges = %d, want 1 (no output_keys must not accept)", nudges)
	}
	if got := loop.Counter("empty_response"); got != 1 {
		t.Fatalf("empty_response counter = %d, want 1", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6f' silent worker
// detection). Every silent_tool_streak_threshold consecutive tool-only turns
// inject one narration nudge.
func TestSilentToolTurnsGetANarrationNudge(t *testing.T) {
	cfg := LoopConfig{MaxIterations: 6, SilentToolStreakThreshold: 2}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "read_one", Arguments: `{}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: "read_two", Arguments: `{}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c3", Name: "read_three", Arguments: `{}`}}},
		{content: "all done"},
	}}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	}
	loop := NewAgentLoop(cfg, executor)
	ctx := midturnAgentContext(completer, "read_one", "read_two", "read_three")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	nudges := 0
	for _, message := range conv.Messages() {
		if message.Role != RoleUser || !strings.Contains(message.Content, "consecutive turns without any text output") {
			continue
		}
		nudges++
		if !strings.Contains(message.Content, "[SYSTEM] You have been calling tools for 2 consecutive turns") {
			t.Fatalf("nudge names the wrong streak: %q", message.Content)
		}
	}
	if nudges != 1 {
		t.Fatalf("silent-tool nudges = %d, want 1 (streak 2 of threshold 2)", nudges)
	}
	if got := loop.Counter("silent_tool_nudged"); got != 1 {
		t.Fatalf("silent_tool_nudged counter = %d, want 1", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6f' — any turn
// with assistant text resets the streak, so an interleaved narration never
// triggers the nudge)
func TestAssistantTextResetsTheSilentToolStreak(t *testing.T) {
	cfg := LoopConfig{MaxIterations: 6, SilentToolStreakThreshold: 2}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "read_one", Arguments: `{}`}}},
		{content: "checking the second source", toolCalls: []llm.ToolCall{{ID: "c2", Name: "read_two", Arguments: `{}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c3", Name: "read_three", Arguments: `{}`}}},
		{content: "all done"},
	}}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	}
	loop := NewAgentLoop(cfg, executor)
	ctx := midturnAgentContext(completer, "read_one", "read_two", "read_three")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	for _, message := range conv.Messages() {
		if strings.Contains(message.Content, "consecutive turns without any text output") {
			t.Fatalf("nudge injected despite narration: %q", message.Content)
		}
	}
	if got := loop.Counter("silent_tool_nudged"); got != 0 {
		t.Fatalf("silent_tool_nudged counter = %d, want 0", got)
	}
}

// Parity: framework/agent_loop/internals/types.py::LoopConfig
// (silent_tool_streak_threshold = 5, max_retained_screenshots = 2)
func TestTurnGuardConfigDefaults(t *testing.T) {
	cfg := DefaultLoopConfig()
	if cfg.SilentToolStreakThreshold != 5 {
		t.Fatalf("SilentToolStreakThreshold = %d, want 5", cfg.SilentToolStreakThreshold)
	}
	if cfg.MaxRetainedScreenshots != 2 {
		t.Fatalf("MaxRetainedScreenshots = %d, want 2", cfg.MaxRetainedScreenshots)
	}
	// A partially-built config (zero values) keeps the upstream defaults
	// through the loop's zero-means-default accessors.
	loop := NewAgentLoop(LoopConfig{}, nil)
	if got := loop.silentToolStreakThreshold(); got != DefaultSilentToolStreakThreshold {
		t.Fatalf("zero config silent threshold = %d, want %d", got, DefaultSilentToolStreakThreshold)
	}
	if got := loop.maxRetainedScreenshots(); got != DefaultMaxRetainedScreenshots {
		t.Fatalf("zero config screenshot bound = %d, want %d", got, DefaultMaxRetainedScreenshots)
	}
}

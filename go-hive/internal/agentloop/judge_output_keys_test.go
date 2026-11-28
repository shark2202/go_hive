package agentloop

// Regression tests for A6: upstream's implicit judge output-key gate.
//
// judge_pipeline.judge_turn Level 2 is local (no LLM call): tool results mean
// "keep working" (silent RETRY), missing declared keys produce the
// "Task incomplete" RETRY, and an all-nullable spec with nothing set produces
// the set_output hint. Level 2b (the criteria quality judge) only runs once
// the keys are satisfied.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
// Parity: framework/agent_loop/agent_loop.py::_get_missing_output_keys

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func judgeFeedbackMessages(conv *NodeConversation) []string {
	var messages []string
	for _, message := range conv.Messages() {
		if message.Role == RoleUser && strings.HasPrefix(message.Content, "[Judge feedback]: ") {
			messages = append(messages, message.Content)
		}
	}
	return messages
}

// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
// (Level 2: declared keys already satisfied -> local ACCEPT)
//
// The live loop rejects set_output (upstream removed the feature), so a
// satisfied accumulator now only comes from a conversation restored with a
// historical successful call. The first text-only turn accepts without an LLM
// judge call.
func TestImplicitJudgeAcceptsWhenOutputKeysAlreadySet(t *testing.T) {
	conv := NewNodeConversation("system")
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

	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	loop.JudgeEvents = events
	ctx := judgeTestContext(completer, nil)
	ctx.AgentSpec.OutputKeys = []string{"summary"}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "done" || result.InnerTurns != 1 {
		t.Fatalf("result = %#v, want a successful one-turn completion", result)
	}
	if feedback := judgeFeedbackMessages(conv); len(feedback) != 0 {
		t.Fatalf("judge feedback = %#v, want none when every key is set", feedback)
	}
	if events.count() != 1 {
		t.Fatalf("judge events = %d, want 1", events.count())
	}
	if events.events[0].judgeType != "implicit" || events.events[0].action != string(JudgeAccept) {
		t.Fatalf("event = %#v, want an implicit ACCEPT", events.events[0])
	}
}

// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
// (Level 2 tool_results -> RETRY with feedback=None)
//
// Level 2 order: a turn that produced tool results is silent RETRY — no
// [Judge feedback] message — even when output keys are still missing. The next
// text-only turn is the first one allowed to inject feedback.
func TestImplicitJudgeSilentRetryAfterToolBatch(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{
			ID:        "r1",
			Name:      "read_file",
			Arguments: `{"path":"a.txt"}`,
		}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, JudgeEveryNTurns: 1}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.AgentSpec.OutputKeys = []string{"summary"}
	conv := NewNodeConversation("system")

	_, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err == nil {
		t.Fatal("ExecuteTurn succeeded, want max-iterations after the missing-key retry")
	}
	feedback := judgeFeedbackMessages(conv)
	if len(feedback) != 1 {
		t.Fatalf("judge feedback messages = %d, want 1 (the tool-batch retry is silent): %#v",
			len(feedback), feedback)
	}
	if !strings.Contains(feedback[0], "Required outputs not yet produced") {
		t.Fatalf("feedback = %q, want the missing-key message", feedback[0])
	}
	if got := loop.Counter("judge_retry"); got != 2 {
		t.Fatalf("stats[judge_retry] = %d, want 2", got)
	}
}

// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
// (Level 2 all-nullable / none-set branch)
//
// All declared keys nullable and none set: upstream retries with the
// set_output hint instead of accepting an empty result.
func TestImplicitJudgeAllNullableNoValueRetries(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 1}, nil)
	ctx := judgeTestContext(completer, nil)
	ctx.AgentSpec.OutputKeys = []string{"notes", "status"}
	ctx.AgentSpec.NullableOutputKeys = []string{"notes", "status"}
	conv := NewNodeConversation("system")

	_, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err == nil {
		t.Fatal("ExecuteTurn succeeded, want max-iterations after the nullable-output retry")
	}
	feedback := judgeFeedbackMessages(conv)
	if len(feedback) != 1 {
		t.Fatalf("judge feedback messages = %d, want 1: %#v", len(feedback), feedback)
	}
	want := "No output keys have been set yet. Use set_output to set at least one of: ['notes', 'status']"
	if feedback[0] != "[Judge feedback]: "+want {
		t.Fatalf("feedback = %q, want %q", feedback[0], "[Judge feedback]: "+want)
	}
}

// Parity: framework/agent_loop/internals/types.py::OutputAccumulator
//
// OutputAccumulator mirrors handle_set_output: successful declared keys set,
// later calls override, failed/undeclared calls are ignored.
func TestOutputAccumulatorScansSuccessfulSetOutputCalls(t *testing.T) {
	conv := NewNodeConversation("system")
	mustAddAssistant := func(calls ...map[string]any) {
		t.Helper()
		if _, err := conv.AddAssistantMessage("", calls, AssistantMessageOptions{}); err != nil {
			t.Fatalf("AddAssistantMessage: %v", err)
		}
	}
	mustAddResult := func(id, content string, isError bool) {
		t.Helper()
		if _, err := conv.AddToolResult(id, content, ToolResultOptions{IsError: isError}); err != nil {
			t.Fatalf("AddToolResult: %v", err)
		}
	}

	mustAddAssistant(
		map[string]any{"id": "s1", "function": map[string]any{
			"name": "set_output", "arguments": `{"key":"summary","value":"first"}`,
		}},
		map[string]any{"id": "u1", "function": map[string]any{
			"name": "set_output", "arguments": `{"key":"undeclared","value":"ignored"}`,
		}},
	)
	mustAddResult("s1", "Output 'summary' set successfully.", false)
	mustAddResult("u1", "Output 'undeclared' set successfully.", false)

	mustAddAssistant(
		map[string]any{"id": "s2", "function": map[string]any{
			"name": "set_output", "arguments": `{"key":"summary","value":"second"}`,
		}},
		map[string]any{"id": "s3", "function": map[string]any{
			"name": "set_output", "arguments": `{"key":"summary","value":"failed"}`,
		}},
	)
	mustAddResult("s2", "Output 'summary' set successfully.", false)
	mustAddResult("s3", "Invalid output key", true)

	// An assistant call with no stored result yet (the in-flight turn) must not
	// count either.
	mustAddAssistant(map[string]any{"id": "s4", "function": map[string]any{
		"name": "set_output", "arguments": `{"key":"summary","value":"in-flight"}`,
	}})

	accumulator := conv.OutputAccumulator([]string{"summary"})
	if got := accumulator["summary"]; got != "second" {
		t.Fatalf("summary = %#v, want the later successful value %q", got, "second")
	}
	if _, ok := accumulator["undeclared"]; ok {
		t.Fatalf("undeclared key leaked into the accumulator: %#v", accumulator)
	}
	if len(accumulator) != 1 {
		t.Fatalf("accumulator = %#v, want only the declared key", accumulator)
	}
}

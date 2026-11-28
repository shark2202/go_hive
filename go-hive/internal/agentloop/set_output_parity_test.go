package agentloop

// Regression tests for set_output finalization. Upstream removed the tool from
// the live loop: a call is answered with an error result before any executor
// runs, so it can never satisfy the output-key accumulator. The builder and
// pure handler remain for the legacy synthetic surface.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (set_output branch)
// Parity: framework/agent_loop/internals/synthetic_tools.py::handle_set_output

import (
	"context"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (set_output branch)
//
// TestExecuteToolRejectsSetOutputWithoutCallingExecutor is the two-sided
// negative: the removed tool never reaches the executor, while the ordinary
// tool next to it still does.
func TestExecuteToolRejectsSetOutputWithoutCallingExecutor(t *testing.T) {
	var executed []string
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
		executed = append(executed, req.Name)
		return ToolResult{Content: "tool output"}, nil
	})
	ctx := judgeTestContext(&scriptedCompleter{}, nil, SetOutputToolName, "read_file")

	result := loop.executeTool(context.Background(), ctx, ToolCallRequest{
		ID:    "so1",
		Name:  SetOutputToolName,
		Input: map[string]any{"key": "summary", "value": "ok"},
	})
	if !result.IsError {
		t.Fatalf("set_output result = %#v, want an error", result)
	}
	want := "set_output is no longer available. Report your results via conversation instead."
	if result.Content != want {
		t.Fatalf("set_output content = %q, want %q", result.Content, want)
	}
	if len(executed) != 0 {
		t.Fatalf("executor ran for set_output: %v", executed)
	}

	result = loop.executeTool(context.Background(), ctx, ToolCallRequest{
		ID:    "r1",
		Name:  "read_file",
		Input: map[string]any{"path": "a.txt"},
	})
	if result.IsError || result.Content != "tool output" {
		t.Fatalf("read_file result = %#v, want the executor result", result)
	}
	if len(executed) != 1 || executed[0] != "read_file" {
		t.Fatalf("executed = %v, want exactly [read_file]", executed)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (set_output branch)
//
// TestSetOutputRejectionCannotSatisfyOutputKeys drives the rejection through a
// real turn: the stored result is an error, the executor sees only the ordinary
// tool, and the implicit judge gate keeps reporting the declared key missing.
func TestSetOutputRejectionCannotSatisfyOutputKeys(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "working"},
		{toolCalls: []llm.ToolCall{
			{ID: "so1", Name: SetOutputToolName, Arguments: `{"key":"summary","value":"ok"}`},
			{ID: "r1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
		}},
		{content: "done"},
	}}
	var executed []string
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
		executed = append(executed, req.Name)
		return ToolResult{Content: "tool output"}, nil
	})
	loop.JudgeEvents = events
	ctx := judgeTestContext(completer, nil, SetOutputToolName, "read_file")
	ctx.AgentSpec.OutputKeys = []string{"summary"}
	conv := NewNodeConversation("system")

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err == nil {
		t.Fatal("ExecuteTurn succeeded, want max-iterations while the declared key stays missing")
	}

	if len(executed) != 1 || executed[0] != "read_file" {
		t.Fatalf("executed = %v, want only [read_file]", executed)
	}
	var rejected *Message
	for _, message := range conv.Messages() {
		if message.Role == RoleTool && message.ToolUseID != nil && *message.ToolUseID == "so1" {
			stored := message
			rejected = &stored
			break
		}
	}
	if rejected == nil {
		t.Fatal("set_output rejection was not stored as a tool result")
	}
	if !rejected.IsError || rejected.Content != "set_output is no longer available. Report your results via conversation instead." {
		t.Fatalf("stored set_output result = %#v, want the upstream rejection", *rejected)
	}
	if accumulator := conv.OutputAccumulator([]string{"summary"}); len(accumulator) != 0 {
		t.Fatalf("accumulator = %#v, want the rejected key unset", accumulator)
	}

	feedback := judgeFeedbackMessages(conv)
	if len(feedback) != 2 {
		t.Fatalf("judge feedback messages = %d, want 2 (both text-only turns): %#v", len(feedback), feedback)
	}
	want := "Task incomplete. Required outputs not yet produced: ['summary']. " +
		"Follow your system prompt instructions to complete the work."
	for i, message := range feedback {
		if message != "[Judge feedback]: "+want {
			t.Fatalf("feedback[%d] = %q, want %q", i, message, "[Judge feedback]: "+want)
		}
	}
	if events.count() != 3 {
		t.Fatalf("judge events = %d, want 3 (two text retries + the silent tool retry)", events.count())
	}
	for i, event := range events.events {
		if event.judgeType != "implicit" {
			t.Fatalf("event[%d].judgeType = %q, want implicit", i, event.judgeType)
		}
	}
}

// Parity: framework/agent_loop/internals/synthetic_tools.py::handle_set_output
//
// TestHandleSetOutputKeepsLegacyAcceptSemantics pins the pure helper upstream
// still ships (even though the live loop rejects the tool): declared keys are
// acknowledged, undeclared keys are rejected with the Python list repr.
func TestHandleSetOutputKeepsLegacyAcceptSemantics(t *testing.T) {
	accepted := HandleSetOutput(map[string]any{"key": "summary", "value": "ok"}, []string{"summary"})
	if accepted.IsError {
		t.Fatalf("accepted result = %#v, want a success", accepted)
	}
	if accepted.Content != "Output 'summary' set successfully." {
		t.Fatalf("accepted content = %q", accepted.Content)
	}

	rejected := HandleSetOutput(map[string]any{"key": "nope", "value": "ok"}, []string{"summary"})
	if !rejected.IsError {
		t.Fatalf("rejected result = %#v, want an error", rejected)
	}
	if rejected.Content != "Invalid output key 'nope'. Valid keys: ['summary']" {
		t.Fatalf("rejected content = %q", rejected.Content)
	}
}

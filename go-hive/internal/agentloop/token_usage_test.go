package agentloop

import (
	"context"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// A21: the loop accumulates prompt+completion tokens for every model call so
// the colony worker report can carry upstream's WorkerResult.tokens_used
// (AgentResult.tokens_used = total_input_tokens + total_output_tokens).
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (AgentResult
// .tokens_used)
func TestLoopAccumulatesTokenUsage(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{
			toolCalls:    []llm.ToolCall{{ID: "t1", Name: "echo", Arguments: `{"value":"x"}`}},
			inputTokens:  100,
			outputTokens: 20,
		},
		{
			content:      "done",
			inputTokens:  50,
			outputTokens: 10,
		},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, successToolExecutor)
	result, err := loop.ExecuteTurn(
		context.Background(),
		midturnAgentContext(completer, "echo"),
		NewNodeConversation("system"),
		nil,
	)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("result = %#v, want success", result)
	}
	if got, want := loop.TokensUsed(), 180; got != want {
		t.Fatalf("TokensUsed = %d, want %d", got, want)
	}
	if got := loop.TokensUsed(); got != 180 {
		t.Fatalf("TokensUsed is not cumulative: %d", got)
	}
}

// API input-token accounting. Upstream feeds the provider-reported prompt size
// into the conversation on FinishEvent; the port previously only used the
// character estimate because UpdateTokenCount had no caller.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (FinishEvent branch)

type tokenCountProbe struct {
	conv *NodeConversation
	seen []int
}

func (p *tokenCountProbe) IterationStarted(int)                          {}
func (p *tokenCountProbe) LLMTurnComplete(llm.Completion, int)           {}
func (p *tokenCountProbe) ToolCallStarted(ToolCallRequest)               {}
func (p *tokenCountProbe) ToolCallCompleted(ToolCallRequest, ToolResult) {}
func (p *tokenCountProbe) AssistantText(_, _ string, _ int) {
	// AssistantText fires after the completion's usage was recorded and
	// before the assistant message append resets the count, the same window
	// upstream's FinishEvent handler runs in.
	p.seen = append(p.seen, p.conv.EstimateTokens())
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (FinishEvent branch)
func TestLoopRecordsCompletionInputTokens(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "done", inputTokens: 777},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, MaxContextTokens: 1000}, nil)
	ctx := judgeTestContext(completer, nil)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:     "system",
		MaxContextTokens: 1000,
	})
	if _, err := conv.AddUserMessage("hello", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	probe := &tokenCountProbe{conv: conv}
	ctx.Observer = probe

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(probe.seen) != 1 {
		t.Fatalf("AssistantText observations = %d, want 1", len(probe.seen))
	}
	if probe.seen[0] != 777 {
		t.Fatalf("estimate at completion = %d, want the provider count 777", probe.seen[0])
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (FinishEvent branch)
//
// TestLoopLeavesEstimateAloneWhenUsageIsAbsent: upstream guards the update with
// `input_tokens > 0`, so a provider that omits usage must not zero the count.
func TestLoopLeavesEstimateAloneWhenUsageIsAbsent(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, MaxContextTokens: 1000}, nil)
	ctx := judgeTestContext(completer, nil)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:     "system",
		MaxContextTokens: 1000,
	})
	if _, err := conv.AddUserMessage("hello", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	conv.UpdateTokenCount(500)
	probe := &tokenCountProbe{conv: conv}
	ctx.Observer = probe

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(probe.seen) != 1 || probe.seen[0] != 500 {
		t.Fatalf("estimate at completion = %v, want the preserved 500", probe.seen)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (FinishEvent branch)
//
// TestRecordPromptTokenCountIgnoresNonPositive is the helper-level guard: a
// zero/negative count never replaces the estimate.
func TestRecordPromptTokenCountIgnoresNonPositive(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{}, nil)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:     "system",
		MaxContextTokens: 1000,
	})
	loop.recordPromptTokenCount(conv, llm.Completion{InputTokens: 640})
	if got := conv.EstimateTokens(); got != 640 {
		t.Fatalf("EstimateTokens = %d, want 640", got)
	}
	loop.recordPromptTokenCount(conv, llm.Completion{InputTokens: 0})
	if got := conv.EstimateTokens(); got != 640 {
		t.Fatalf("EstimateTokens after zero usage = %d, want 640", got)
	}
}

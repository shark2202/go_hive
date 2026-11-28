package agentloop

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type scriptedResponse struct {
	content   string
	toolCalls []llm.ToolCall
	costUSD   float64
	// inputTokens/outputTokens feed the loop's token accumulator; tests that
	// assert AgentResult-style token telemetry set them.
	inputTokens  int
	outputTokens int
	// err, when set, is returned instead of a completion — the scripted way to
	// exercise the loop's error paths (e.g. a stream-stall recovery retry).
	err error
}

// Parity-exempt: upstream has no standalone test for this helper; it mirrors _ScriptedLLM
// from core/tests/test_midturn_input.py, used by
// test_a_message_sent_while_a_tool_runs_is_read_on_the_next_call and its siblings.
type scriptedCompleter struct {
	scenarios   []scriptedResponse
	duringFirst func()

	mu    sync.Mutex
	calls []llm.CompletionRequest
}

func (s *scriptedCompleter) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	index := len(s.calls) - 1
	first := len(s.calls) == 1
	s.mu.Unlock()

	if first && s.duringFirst != nil {
		s.duringFirst()
	}
	if index >= len(s.scenarios) {
		index = len(s.scenarios) - 1
	}
	scenario := s.scenarios[index]
	if scenario.err != nil {
		return llm.Completion{}, scenario.err
	}
	return llm.Completion{
		Content:      scenario.content,
		ToolCalls:    scenario.toolCalls,
		Model:        "mock",
		CostUSD:      scenario.costUSD,
		InputTokens:  scenario.inputTokens,
		OutputTokens: scenario.outputTokens,
	}, nil
}

func (s *scriptedCompleter) ModelName() string { return "mock" }

func (s *scriptedCompleter) request(i int) llm.CompletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

func (s *scriptedCompleter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func midturnAgentContext(completer llm.Completer, toolNames ...string) *AgentContext {
	tools := make([]llm.ToolDef, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, llm.ToolDef{
			Name:        name,
			Description: "a tool",
			Parameters:  map[string]any{"type": "object"},
		})
	}
	return &AgentContext{
		AgentID:        "agent",
		AgentSpec:      NewAgentSpec("agent", "Agent", "d"),
		InputData:      map[string]any{"task": "start"},
		LLM:            completer,
		AvailableTools: tools,
		StreamID:       "judge",
	}
}

func positionOf(messages []llm.ChatMessage, needle string) int {
	for i, message := range messages {
		if strings.Contains(message.Content, needle) {
			return i
		}
	}
	return -1
}

func allText(messages []llm.ChatMessage) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

// Parity: core/tests/test_midturn_input.py::test_a_message_sent_while_a_tool_runs_is_read_on_the_next_call
func TestAMessageSentWhileAToolRunsIsReadOnTheNextCall(t *testing.T) {
	const message = "are you still there"

	var loop *AgentLoop
	executor := func(req ToolCallRequest) (ToolResult, error) {
		loop.InjectEvent(message, true, nil)
		return ToolResult{Content: "tool output"}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "some_tool", Arguments: "{}"}}},
		{content: "yes"},
	}}
	loop = NewAgentLoop(LoopConfig{MaxIterations: 3}, executor)
	ctx := midturnAgentContext(completer, "some_tool")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	second := completer.request(1)
	toolPos := positionOf(second.Messages, "tool output")
	messagePos := positionOf(second.Messages, message)
	if toolPos < 0 || messagePos < 0 {
		t.Fatalf("tool output (%d) or message (%d) missing from second call", toolPos, messagePos)
	}
	if toolPos >= messagePos {
		t.Fatalf("tool output at %d should precede the message at %d", toolPos, messagePos)
	}
}

// Parity-exempt: upstream has no standalone test for this helper; it mirrors the
// fire_reminder override inside
// core/tests/test_midturn_input.py::test_a_message_mid_turn_gets_the_context_a_turns_first_message_gets.
type recordingPointSource struct {
	point ReminderPoint

	mu   sync.Mutex
	seen []string
}

func (r *recordingPointSource) Name() string                         { return "recording" }
func (r *recordingPointSource) Points() []ReminderPoint              { return []ReminderPoint{r.point} }
func (r *recordingPointSource) AppliesTo(*ReminderAgentContext) bool { return true }
func (r *recordingPointSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.Point != r.point {
		return "", false
	}
	r.mu.Lock()
	r.seen = append(r.seen, rctx.UserText)
	r.mu.Unlock()
	return "", false
}

func (r *recordingPointSource) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// Parity: core/tests/test_midturn_input.py::test_a_message_mid_turn_gets_the_context_a_turns_first_message_gets
func TestAMessageMidTurnGetsTheContextATurnsFirstMessageGets(t *testing.T) {
	const message = "are you still there"

	var loop *AgentLoop
	executor := func(req ToolCallRequest) (ToolResult, error) {
		loop.InjectEvent(message, true, nil)
		return ToolResult{Content: "tool output"}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "some_tool", Arguments: "{}"}}},
		{content: "yes"},
	}}
	source := &recordingPointSource{point: USER_PROMPT_SUBMIT}
	loop = NewAgentLoop(LoopConfig{MaxIterations: 3}, executor)
	loop.Hub.Register(source)
	ctx := midturnAgentContext(completer, "some_tool")
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	seen := source.recorded()
	if len(seen) != 1 || seen[0] != message {
		t.Fatalf("USER_PROMPT_SUBMIT user text = %q, want [%q]", seen, message)
	}

	loop.mu.Lock()
	pending := append([]string(nil), loop.pending...)
	loop.mu.Unlock()
	if len(pending) != 0 {
		t.Fatalf("pending user texts = %q, want empty", pending)
	}
}

// Parity: core/tests/test_midturn_input.py::test_a_message_sent_during_the_response_stops_its_tool_from_waiting
func TestAMessageSentDuringTheResponseStopsItsToolFromWaiting(t *testing.T) {
	const message = "are you still there"

	var (
		loop  *AgentLoop
		calls []map[string]any
	)
	executor := func(req ToolCallRequest) (ToolResult, error) {
		calls = append(calls, req.Input)
		return ToolResult{Content: `{"status": "running", "data": ""}`}, nil
	}
	completer := &scriptedCompleter{
		scenarios: []scriptedResponse{
			{toolCalls: []llm.ToolCall{{
				ID:        "c1",
				Name:      "terminal_job_logs",
				Arguments: `{"job_id": "job_1", "wait_until_exit": true}`,
			}}},
			{content: "yes"},
		},
		duringFirst: func() { loop.InjectEvent(message, true, nil) },
	}
	loop = NewAgentLoop(LoopConfig{MaxIterations: 3, ToolWaitLimitSeconds: 30}, executor)
	ctx := midturnAgentContext(completer, "terminal_job_logs")
	conv := NewNodeConversation("You are a test assistant.")

	started := time.Now()
	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	if elapsed >= 10*time.Second {
		t.Fatalf("turn took %s; the wait should have been skipped", elapsed)
	}
	if len(calls) != 1 {
		t.Fatalf("executor saw %d calls (%v), want 1", len(calls), calls)
	}
	if calls[0]["job_id"] != "job_1" || calls[0]["wait_until_exit"] != false {
		t.Fatalf("read inputs = %v, want job_id=job_1, wait_until_exit=false", calls[0])
	}
	if len(calls[0]) != 2 {
		t.Fatalf("read inputs carried %d keys (%v), want exactly 2", len(calls[0]), calls[0])
	}

	if !strings.Contains(allText(completer.request(1).Messages), message) {
		t.Fatalf("second call did not carry the message")
	}

	loop.mu.Lock()
	unread := loop.unread
	loop.mu.Unlock()
	if unread {
		t.Fatalf("input still marked unread after the turn read it")
	}
}

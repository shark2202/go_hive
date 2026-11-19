package agentloop

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

type recordingJudge struct {
	mu       sync.Mutex
	verdicts []JudgeVerdict
	requests []JudgeRequest
}

func (j *recordingJudge) Evaluate(_ context.Context, request JudgeRequest) (JudgeVerdict, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.requests = append(j.requests, request)
	index := len(j.requests) - 1
	if len(j.verdicts) == 0 {
		return JudgeVerdict{Action: JudgeAccept}, nil
	}
	if index >= len(j.verdicts) {
		index = len(j.verdicts) - 1
	}
	return j.verdicts[index], nil
}

func (j *recordingJudge) callCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.requests)
}

func (j *recordingJudge) request(index int) JudgeRequest {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.requests[index]
}

type judgeEventRecord struct {
	streamID    string
	nodeID      string
	action      string
	feedback    string
	judgeType   string
	iteration   int
	executionID *string
}

type recordingJudgeEvents struct {
	mu     sync.Mutex
	events []judgeEventRecord
}

func (w *recordingJudgeEvents) EmitJudgeVerdict(streamID, nodeID, action, feedback, judgeType string, iteration int, executionID *string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, judgeEventRecord{
		streamID:    streamID,
		nodeID:      nodeID,
		action:      action,
		feedback:    feedback,
		judgeType:   judgeType,
		iteration:   iteration,
		executionID: executionID,
	})
}

func (w *recordingJudgeEvents) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.events)
}

func judgeTestContext(completer llm.Completer, criteria *string, toolNames ...string) *AgentContext {
	tools := make([]llm.ToolDef, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, llm.ToolDef{Name: name, Description: "a tool"})
	}
	spec := NewAgentSpec("agent", "Agent", "deliver the task")
	spec.SuccessCriteria = criteria
	return &AgentContext{
		AgentID:        "agent",
		AgentSpec:      spec,
		InputData:      map[string]any{"task": "start"},
		LLM:            completer,
		AvailableTools: tools,
		StreamID:       "judge",
		ExecutionID:    "exec-1",
	}
}

func successToolExecutor(_ ToolCallRequest) (ToolResult, error) {
	return ToolResult{Content: "tool output"}, nil
}

// Parity: core/tests/test_event_loop_node.py::TestJudgeIntegration::test_judge_accept
func TestJudgeAcceptEndsTurn(t *testing.T) {
	criteria := "a final report exists"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	judge := &recordingJudge{}
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	loop.Judge = judge
	loop.JudgeEvents = events

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "done" || result.InnerTurns != 1 {
		t.Fatalf("result = %#v, want a successful one-turn completion", result)
	}
	if completer.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want 1", completer.callCount())
	}
	if judge.callCount() != 1 {
		t.Fatalf("judge calls = %d, want 1", judge.callCount())
	}
	request := judge.request(0)
	if request.Criteria != criteria {
		t.Fatalf("criteria = %q, want %q", request.Criteria, criteria)
	}
	if request.AssistantText != "done" {
		t.Fatalf("assistant text = %q, want %q", request.AssistantText, "done")
	}
	if request.AgentSpec.ID != "agent" {
		t.Fatalf("agent spec = %#v", request.AgentSpec)
	}
	if events.count() != 1 {
		t.Fatalf("judge events = %d, want 1", events.count())
	}
	event := events.events[0]
	if event.action != string(JudgeAccept) || event.judgeType != "custom" || event.iteration != 0 {
		t.Fatalf("event = %#v", event)
	}
}

// Parity: core/tests/test_event_loop_node.py::TestJudgeIntegration::test_judge_retry_then_accept
func TestJudgeRetryRunsOneMoreTurn(t *testing.T) {
	criteria := "tests were run"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "draft"},
		{content: "final"},
	}}
	judge := &recordingJudge{verdicts: []JudgeVerdict{
		{Action: JudgeRetry, Feedback: "run the test suite"},
		{Action: JudgeAccept},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Judge = judge

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "final" || result.InnerTurns != 2 {
		t.Fatalf("result = %#v, want success after a second turn", result)
	}
	if completer.callCount() != 2 {
		t.Fatalf("LLM calls = %d, want 2", completer.callCount())
	}
	if judge.callCount() != 2 {
		t.Fatalf("judge calls = %d, want 2", judge.callCount())
	}
	secondRequest := completer.request(1)
	if found := positionOf(secondRequest.Messages, "[Judge feedback]: run the test suite"); found < 0 {
		t.Fatalf("second LLM request is missing judge feedback: %#v", secondRequest.Messages)
	}
}

// Parity: core/tests/test_event_loop_node.py::TestJudgeIntegration::test_judge_escalate
func TestJudgeEscalateStopsTurnAndWritesEvent(t *testing.T) {
	criteria := "tone is professional"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "attempt"}}}
	judge := &recordingJudge{verdicts: []JudgeVerdict{
		{Action: JudgeEscalate, Feedback: "tone violation"},
	}}
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	loop.Judge = judge
	loop.JudgeEvents = events

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria), NewNodeConversation("system"), nil)
	if err == nil || !strings.Contains(err.Error(), "judge escalated") || !strings.Contains(err.Error(), "tone violation") {
		t.Fatalf("error = %v, want judge escalation with feedback", err)
	}
	if result.Success {
		t.Fatalf("result.Success = true, want false")
	}
	if result.InnerTurns != 1 {
		t.Fatalf("inner turns = %d, want 1", result.InnerTurns)
	}
	if completer.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want 1", completer.callCount())
	}
	if events.count() != 1 {
		t.Fatalf("judge events = %d, want 1", events.count())
	}
	event := events.events[0]
	if event.action != string(JudgeEscalate) || event.feedback != "tone violation" {
		t.Fatalf("event = %#v", event)
	}
	if event.executionID == nil || *event.executionID != "exec-1" {
		t.Fatalf("event execution id = %v", event.executionID)
	}
}

// Parity-exempt: the success-criteria gate is an explicit Go requirement; the
// upstream custom-judge path runs without success_criteria.
func TestJudgeWithoutCriteriaIsNeverCalled(t *testing.T) {
	blank := "   "
	skipCriteria := "must be complete"
	cases := []struct {
		name     string
		criteria *string
		skip     bool
	}{
		{name: "nil criteria"},
		{name: "blank criteria", criteria: &blank},
		{name: "skip judge", criteria: &skipCriteria, skip: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
			judge := &recordingJudge{}
			events := &recordingJudgeEvents{}
			loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
			loop.Judge = judge
			loop.JudgeEvents = events
			agentCtx := judgeTestContext(completer, tc.criteria)
			agentCtx.AgentSpec.SkipJudge = tc.skip

			result, err := loop.ExecuteTurn(context.Background(), agentCtx, NewNodeConversation("system"), nil)
			if err != nil {
				t.Fatalf("ExecuteTurn: %v", err)
			}
			if !result.Success {
				t.Fatalf("result = %#v, want success", result)
			}
			if judge.callCount() != 0 {
				t.Fatalf("judge calls = %d, want 0", judge.callCount())
			}
			if events.count() != 0 {
				t.Fatalf("judge events = %d, want 0", events.count())
			}
		})
	}
}

// Parity-exempt: the default-off contract is Go-specific; a nil Judge must
// leave the upstream completion path untouched.
func TestJudgeIsOffByDefault(t *testing.T) {
	criteria := "anything"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	loop.JudgeEvents = events

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "done" {
		t.Fatalf("result = %#v, want the untouched completion path", result)
	}
	if events.count() != 0 {
		t.Fatalf("judge events = %d, want 0", events.count())
	}
}

// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
func TestJudgeRunsEveryNTurns(t *testing.T) {
	criteria := "all work is complete"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "work", Arguments: "{}"}}},
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: "work", Arguments: "{}"}}},
		{content: "done"},
	}}
	judge := &recordingJudge{verdicts: []JudgeVerdict{
		{Action: JudgeRetry, Feedback: "keep going"},
		{Action: JudgeAccept},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5, JudgeEveryNTurns: 2}, successToolExecutor)
	loop.Judge = judge

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria, "work"), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.InnerTurns != 3 {
		t.Fatalf("result = %#v, want success after 3 turns", result)
	}
	if judge.callCount() != 2 {
		t.Fatalf("judge calls = %d, want 2 (one cadence hit + natural stop)", judge.callCount())
	}
	if got := judge.request(0).Iteration; got != 1 {
		t.Fatalf("first judge iteration = %d, want 1", got)
	}
	if got := judge.request(1).Iteration; got != 2 {
		t.Fatalf("second judge iteration = %d, want 2", got)
	}
}

// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
func TestJudgeRunsOnToolFailure(t *testing.T) {
	criteria := "the tool error is resolved"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "work", Arguments: "{}"}}},
		{content: "recovered"},
	}}
	judge := &recordingJudge{verdicts: []JudgeVerdict{
		{Action: JudgeRetry, Feedback: "retry the tool"},
		{Action: JudgeAccept},
	}}
	failing := func(_ ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "boom", IsError: true}, nil
	}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5, JudgeEveryNTurns: 10}, failing)
	loop.Judge = judge

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria, "work"), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "recovered" {
		t.Fatalf("result = %#v", result)
	}
	if judge.callCount() != 2 {
		t.Fatalf("judge calls = %d, want 2 (failure + natural stop)", judge.callCount())
	}
	first := judge.request(0)
	if first.Iteration != 0 || len(first.ToolResults) != 1 || !first.ToolResults[0].IsError {
		t.Fatalf("first judge request = %#v, want the failed tool result", first)
	}
}

// Parity-exempt: ConfigureJudge is the Go production wiring entry point;
// upstream constructs the Level 2 judge inline in judge_turn and always has an
// LLM in NodeContext.
func TestConfigureJudgeWiresAdapterOnlyWithCriteria(t *testing.T) {
	criteria := "the report names every risk"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "final report"},
		{content: `{"passes": true, "explanation": "criteria met"}`},
	}}
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)

	withoutCriteria := judgeTestContext(completer, nil)
	if loop.ConfigureJudge(withoutCriteria.AgentSpec, completer, events) {
		t.Fatal("ConfigureJudge = true without criteria, want false")
	}
	if loop.Judge != nil {
		t.Fatalf("Judge = %#v, want nil without criteria", loop.Judge)
	}
	if loop.ConfigureJudge(withoutCriteria.AgentSpec, nil, events) {
		t.Fatal("ConfigureJudge = true without a completer, want false")
	}

	agentCtx := judgeTestContext(completer, &criteria)
	if !loop.ConfigureJudge(agentCtx.AgentSpec, completer, events) {
		t.Fatal("ConfigureJudge = false with criteria and a completer")
	}
	result, err := loop.ExecuteTurn(context.Background(), agentCtx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "final report" {
		t.Fatalf("result = %#v", result)
	}
	if completer.callCount() != 2 {
		t.Fatalf("LLM calls = %d, want the turn plus one judge call", completer.callCount())
	}
	if events.count() != 1 {
		t.Fatalf("judge events = %d, want 1", events.count())
	}
	if event := events.events[0]; event.action != string(JudgeAccept) || event.judgeType != "custom" {
		t.Fatalf("event = %#v", event)
	}
}

// Parity: core/framework/agent_loop/internals/event_publishing.py::publish_judge_verdict
func TestJudgeVerdictEventCarriesUpstreamFields(t *testing.T) {
	criteria := "the report exists"
	feedback := strings.Repeat("feedback ", 30)
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	judge := &recordingJudge{verdicts: []JudgeVerdict{{Action: JudgeAccept, Feedback: feedback}}}
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, nil)
	loop.Judge = judge
	loop.JudgeEvents = events

	if _, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria), NewNodeConversation("system"), nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if events.count() != 1 {
		t.Fatalf("judge events = %d, want 1", events.count())
	}
	event := events.events[0]
	if event.streamID != "judge" || event.nodeID != "agent" {
		t.Fatalf("event stream/node = %q/%q, want judge/agent", event.streamID, event.nodeID)
	}
	if event.action != string(JudgeAccept) || event.judgeType != "custom" || event.iteration != 0 {
		t.Fatalf("event = %#v", event)
	}
	if event.executionID == nil || *event.executionID != "exec-1" {
		t.Fatalf("event execution id = %v", event.executionID)
	}
	if got := len([]rune(event.feedback)); got != 200 {
		t.Fatalf("event feedback length = %d, want the upstream 200-char preview", got)
	}
}

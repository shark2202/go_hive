package colony

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

func makeJudgeRuntime(t *testing.T, spec agentloop.AgentSpec, goal *schemas.Goal, provider llm.Completer, bus *eventbus.EventBus) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   spec,
		Goal:        goal,
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: bus,
		StreamID: "judge_test_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// judgeCallCount reports how many recorded requests carried the loop judge's
// prompt, independent of how many ordinary chat turns the scripted model saw.
func (m *scriptedLLM) judgeCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, request := range m.calls {
		if len(request.Messages) == 1 && strings.Contains(request.Messages[0].Content, "Respond with JSON") {
			count++
		}
	}
	return count
}

func assertWorkerConversationHasNoJudgeFeedback(t *testing.T, worker *Worker) {
	t.Helper()
	if worker.conversation == nil {
		t.Fatal("worker conversation = nil")
	}
	for _, message := range worker.conversation.Messages() {
		if strings.Contains(message.Content, "[Judge feedback]") {
			t.Fatalf("worker conversation carries judge feedback: %#v", message)
		}
	}
}

func goalWithCriteria() *schemas.Goal {
	return &schemas.Goal{
		ID:   "judge-goal",
		Name: "Judge goal",
		SuccessCriteria: []schemas.SuccessCriterion{{
			ID:          "mentions-hive",
			Description: "the worker output mentions the hive",
			Metric:      agentloop.GoalMetricOutputContains,
			Target:      "hive",
			Weight:      1,
		}},
	}
}

// Parity: framework/agent_loop/agent_loop.py::execute (judge=None on the colony
// worker loop). The colony goal declares criteria, but the Go-only inheritance
// seam is gone: a natural stop makes no judge call and injects no feedback.
func TestWorkerNaturalStopSkipsJudgeDespiteGoalCriteria(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(
		// A work turn (tool call) then the text-only stop sequence. The worker
		// stall policy owns this path in both runtimes; it must stay judge-free.
		toolResponse("t1", "task_list", `{}`),
		textResponse("the hive is ready"),
		textResponse("the hive looks calm"),
		textResponse("the hive is complete"),
	)
	runtime := makeJudgeRuntime(t, testAgentSpec(), goalWithCriteria(), provider, bus)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "natural stop"}}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Status != RunFailed || result.Failed != 1 {
		t.Fatalf("result = %#v, want the stall auto-fail report", result)
	}
	if outcome := result.WorkerResults[0]; outcome.Data["auto_fail_reason"] != "stall_text_only" {
		t.Fatalf("worker outcome = %#v, want the text-only stall report", outcome)
	}
	if provider.callCount() != 4 {
		t.Fatalf("LLM calls = %d, want the work turn plus three text-only turns", provider.callCount())
	}
	if got := provider.judgeCallCount(); got != 0 {
		t.Fatalf("judge calls = %d, want 0 on a colony worker natural stop", got)
	}
	if !eventually(t, 200*time.Millisecond, func() bool { return verdicts.count() == 0 }) {
		t.Fatalf("judge verdict events = %d, want 0", verdicts.count())
	}

	worker := runtime.GetWorker(runtime.ListWorkers()[0].WorkerID)
	assertWorkerConversationHasNoJudgeFeedback(t, worker)
	evaluation := worker.Result().GoalEvaluation
	if evaluation == nil || len(evaluation.Criteria) != 1 || !evaluation.Criteria[0].Met {
		t.Fatalf("goal evaluation = %#v, want the deterministic criterion executed and met", evaluation)
	}
}

// Parity: framework/host/worker.py::Worker._build_result (explicit report ends
// the turn). The terminal report path must stay judge-free when the colony goal
// declares criteria.
func TestWorkerExplicitReportSkipsJudgeDespiteGoalCriteria(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(reportResponse("success", "hive delivered", nil))
	runtime := makeJudgeRuntime(t, testAgentSpec(), goalWithCriteria(), provider, bus)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "report"}}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Status != RunCompleted || result.Succeeded != 1 {
		t.Fatalf("result = %#v, want one completed worker", result)
	}
	if provider.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want exactly the report turn", provider.callCount())
	}
	if got := provider.judgeCallCount(); got != 0 {
		t.Fatalf("judge calls = %d, want 0 on the explicit report path", got)
	}
	if !eventually(t, 200*time.Millisecond, func() bool { return verdicts.count() == 0 }) {
		t.Fatalf("judge verdict events = %d, want 0", verdicts.count())
	}

	worker := runtime.GetWorker(runtime.ListWorkers()[0].WorkerID)
	assertWorkerConversationHasNoJudgeFeedback(t, worker)
}

// Parity: core/framework/agents/queen/worker_definition.py::worker_goal
func TestWorkerLoopWithoutCriteriaNeverCallsJudge(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(reportResponse("success", "draft", nil))
	runtime := makeJudgeRuntime(t, testAgentSpec(), testGoal(), provider, bus)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "no criteria"}}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Status != RunCompleted || result.Succeeded != 1 {
		t.Fatalf("result = %#v, want one completed worker", result)
	}
	if provider.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want exactly the worker turn", provider.callCount())
	}
	if !eventually(t, 200*time.Millisecond, func() bool { return verdicts.count() == 0 }) {
		t.Fatalf("judge verdict events = %d, want 0 without criteria", verdicts.count())
	}
}

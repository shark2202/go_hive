package colony

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

func makeJudgeRuntime(t *testing.T, spec agentloop.AgentSpec, provider llm.Completer, bus *eventbus.EventBus) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   spec,
		Goal:        testGoal(),
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

// Parity: core/tests/test_event_loop_node.py::TestJudgeIntegration::test_judge_accept
func TestWorkerLoopRunsJudgeAndPublishesVerdict(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(
		textResponse("draft"),
		textResponse(`{"passes": true, "explanation": "criteria met"}`),
	)
	criteria := "the worker delivers a final answer"
	spec := testAgentSpec()
	spec.SuccessCriteria = &criteria
	runtime := makeJudgeRuntime(t, spec, provider, bus)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "judge alpha"}}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Status != RunCompleted || result.Succeeded != 1 {
		t.Fatalf("result = %#v, want one completed worker", result)
	}
	if provider.callCount() != 2 {
		t.Fatalf("LLM calls = %d, want the worker turn plus one judge call", provider.callCount())
	}
	if !eventually(t, time.Second, func() bool { return verdicts.count() == 1 }) {
		t.Fatalf("judge verdict events = %d, want 1", verdicts.count())
	}
	event := verdicts.snapshot()[0]
	if !strings.HasPrefix(event.StreamID, "worker:") {
		t.Fatalf("event stream = %q, want the worker stream", event.StreamID)
	}
	if event.NodeID == nil || *event.NodeID != "test_colony_agent" {
		t.Fatalf("event node = %v, want the agent spec id", event.NodeID)
	}
	if event.Data["action"] != string(agentloop.JudgeAccept) {
		t.Fatalf("event action = %v, want ACCEPT", event.Data["action"])
	}
	if event.Data["judge_type"] != "custom" {
		t.Fatalf("event judge_type = %v, want custom", event.Data["judge_type"])
	}
	if event.Data["iteration"] != 0 {
		t.Fatalf("event iteration = %v, want 0", event.Data["iteration"])
	}
}

// Parity: core/framework/agents/queen/worker_definition.py::worker_goal
func TestWorkerLoopWithoutCriteriaNeverCallsJudge(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(textResponse("draft"))
	runtime := makeJudgeRuntime(t, testAgentSpec(), provider, bus)

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

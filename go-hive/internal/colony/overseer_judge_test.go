package colony

import (
	"context"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

func makeOverseerJudgeRuntime(t *testing.T, goal *schemas.Goal, provider *scriptedLLM, bus *eventbus.EventBus) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        goal,
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: bus,
		StreamID: "overseer_judge_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (queen_spec skip_judge=True, AgentLoop judge=None). A goal with criteria must
// not re-enable judgment through the removed Go-only inheritance seam.
func TestOverseerNaturalStopSkipsJudgeDespiteGoalCriteria(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(textResponse("I will coordinate the hive."))
	runtime := makeOverseerJudgeRuntime(t, goalWithCriteria(), provider, bus)
	overseer, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !eventually(t, 2*time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never called the LLM")
	}
	if !eventually(t, 200*time.Millisecond, func() bool { return verdicts.count() == 0 }) {
		t.Fatalf("judge verdict events = %d, want 0", verdicts.count())
	}
	if provider.callCount() != 1 {
		t.Fatalf("provider calls = %d, want exactly the overseer turn", provider.callCount())
	}
	if got := provider.judgeCallCount(); got != 0 {
		t.Fatalf("judge calls = %d, want 0 on an overseer natural stop", got)
	}
	if overseer == nil || overseer.conversation == nil {
		t.Fatal("overseer conversation = nil")
	}
	assertWorkerConversationHasNoJudgeFeedback(t, overseer)
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
// (judge=None). No criteria and no goal criteria still costs one model call.
func TestOverseerWithoutCriteriaNeverCallsJudge(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(textResponse("I will coordinate the hive."))
	runtime := makeOverseerJudgeRuntime(t, testGoal(), provider, bus)
	if _, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !eventually(t, time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never called the LLM")
	}
	if !eventually(t, 200*time.Millisecond, func() bool { return verdicts.count() == 0 }) {
		t.Fatalf("judge verdict events = %d, want 0 without criteria", verdicts.count())
	}
	if provider.callCount() != 1 {
		t.Fatalf("provider calls = %d, want exactly the overseer turn", provider.callCount())
	}
}

package colony

import (
	"context"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/schemas"
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

// Parity-exempt: upstream has no overseer-specific criteria test; this pins the
// Go inheritance seam that arms the overseer's judge from the colony goal.
func TestOverseerInheritsGoalCriteriaAndRunsJudge(t *testing.T) {
	bus := eventbus.NewEventBus()
	verdicts := collectEvents(bus, eventbus.EventJudgeVerdict)
	provider := newScriptedLLM(
		textResponse("I will coordinate the hive."),
		textResponse(`{"passes": true, "explanation": "criteria met"}`),
	)
	goal := &schemas.Goal{
		ID:   "hive-goal",
		Name: "Hive goal",
		SuccessCriteria: []schemas.SuccessCriterion{{
			ID:          "c1",
			Description: "the overseer reports a plan",
			Weight:      1,
		}},
	}
	runtime := makeOverseerJudgeRuntime(t, goal, provider, bus)
	if _, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !eventually(t, 2*time.Second, func() bool { return verdicts.count() == 1 }) {
		t.Fatalf("judge verdict events = %d, want 1 (provider calls %d)", verdicts.count(), provider.callCount())
	}
	event := verdicts.snapshot()[0]
	if event.StreamID != "overseer" {
		t.Fatalf("verdict stream = %q, want the overseer stream", event.StreamID)
	}
	if event.Data["action"] != string(agentloop.JudgeAccept) {
		t.Fatalf("verdict action = %v, want ACCEPT", event.Data["action"])
	}
	if provider.callCount() != 2 {
		t.Fatalf("provider calls = %d, want the overseer turn plus one judge call", provider.callCount())
	}
}

// Parity-exempt: upstream has no overseer-specific criteria test; this pins
// that an overseer with no spec criteria and a goal with no criteria still
// costs exactly one model call (the judge stays off).
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

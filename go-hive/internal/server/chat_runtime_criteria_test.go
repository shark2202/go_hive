package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

// criteriaJudgeLLM answers the queen's turn with a plain completion and the
// loop judge's evaluation with an ACCEPT JSON payload. The judge prompt is the
// single-message "Respond with JSON" template from internal/llmjudge.
type criteriaJudgeLLM struct {
	mu         sync.Mutex
	chatCalls  int
	judgeCalls int
}

func isCriteriaJudgePrompt(req llm.CompletionRequest) bool {
	return len(req.Messages) == 1 && strings.Contains(req.Messages[0].Content, "Respond with JSON")
}

func (m *criteriaJudgeLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if isCriteriaJudgePrompt(req) {
		m.judgeCalls++
		return llm.Completion{Content: `{"passes": true, "explanation": "criteria met"}`, StopReason: "stop"}, nil
	}
	m.chatCalls++
	return llm.Completion{Content: "The honeycomb is ready.", StopReason: "stop"}, nil
}

func (m *criteriaJudgeLLM) counts() (chat, judge int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chatCalls, m.judgeCalls
}

// Parity: core/tests/test_event_loop_node.py::TestJudgeIntegration::test_judge_accept
func TestChatRuntimeConfiguredCriteriaRunsJudge(t *testing.T) {
	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	model := &criteriaJudgeLLM{}
	criteria := "the answer names the honeycomb"
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:             model,
		Root:            t.TempDir(),
		Bus:             bus,
		SessionID:       "session_20260101_000000_criteria01",
		SuccessCriteria: &criteria,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.loop.Judge == nil {
		t.Fatal("configured criteria left the loop judge off")
	}

	if err := runtime.Deliver("summarise the hive"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventJudgeVerdict, 1)
	verdicts := recorder.ofType(eventbus.EventJudgeVerdict)
	if len(verdicts) != 1 {
		t.Fatalf("judge verdicts = %d, want 1", len(verdicts))
	}
	if verdicts[0].Data["action"] != string(agentloop.JudgeAccept) {
		t.Fatalf("verdict action = %#v, want ACCEPT", verdicts[0].Data["action"])
	}
	chatCalls, judgeCalls := model.counts()
	if chatCalls != 1 || judgeCalls != 1 {
		t.Fatalf("model calls = chat %d / judge %d, want 1/1", chatCalls, judgeCalls)
	}
}

// Parity-exempt: upstream's queen spec has an empty success_criteria list; this
// pins the Go inheritance seam that arms the judge from a bound colony's goal.
func TestChatRuntimeInheritsColonyGoalCriteriaForJudge(t *testing.T) {
	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	model := &criteriaJudgeLLM{}
	goal := &schemas.Goal{
		ID:   "hive-goal",
		Name: "Hive goal",
		SuccessCriteria: []schemas.SuccessCriterion{{
			ID:          "c1",
			Description: "the answer names the honeycomb",
			Weight:      1,
		}},
	}
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "worker"},
		Goal:        goal,
		StoragePath: t.TempDir(),
		LLM:         model,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "criteria_colony",
	})
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_20260101_000000_criteria02",
		Colony:    colonyRuntime,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if !agentloop.HasSuccessCriteria(runtime.agentCtx.AgentSpec) {
		t.Fatalf("chat spec = %#v, want inherited colony criteria", runtime.agentCtx.AgentSpec)
	}
	if runtime.loop.Judge == nil {
		t.Fatal("inherited criteria left the loop judge off")
	}

	if err := runtime.Deliver("summarise the hive"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventJudgeVerdict, 1)
	chatCalls, judgeCalls := model.counts()
	if chatCalls != 1 || judgeCalls != 1 {
		t.Fatalf("model calls = chat %d / judge %d, want 1/1", chatCalls, judgeCalls)
	}
}

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

// criteriaJudgeLLM answers the queen's turn with a plain completion and records
// any request that carries the loop judge's prompt. The queen must never issue
// one, whether or not the session or its colony declares success criteria.
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

func assertNoJudgeFeedback(t *testing.T, runtime *ChatRuntime) {
	t.Helper()
	for _, message := range runtime.conv.Messages() {
		if strings.Contains(message.Content, "[Judge feedback]") {
			t.Fatalf("conversation carries judge feedback: %#v", message)
		}
	}
}

// Parity: framework/server/queen_orchestrator.py::_queen_loop (skip_judge=True)
func TestChatRuntimeQueenCriteriaDoesNotArmJudge(t *testing.T) {
	bus := eventbus.NewEventBus()
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
	if !runtime.agentCtx.AgentSpec.SkipJudge {
		t.Fatalf("queen spec SkipJudge = false, want true")
	}
	if runtime.loop.Judge != nil {
		t.Fatalf("queen loop Judge = %#v, want nil", runtime.loop.Judge)
	}

	if err := runtime.Deliver("summarise the hive"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts := waitForAssistantParts(t, runtime, 1)
	if got := parts[len(parts)-1]["content"]; got != "The honeycomb is ready." {
		t.Fatalf("assistant content = %#v, want the normal queen reply", got)
	}
	chatCalls, judgeCalls := model.counts()
	if chatCalls != 1 || judgeCalls != 0 {
		t.Fatalf("model calls = chat %d / judge %d, want 1/0", chatCalls, judgeCalls)
	}
	assertNoJudgeFeedback(t, runtime)
}

// Parity: framework/server/queen_orchestrator.py::_queen_loop (skip_judge=True)
// Parity: framework/agents/queen/nodes/__init__.py::queen_node (skip_judge=True)
func TestChatRuntimeColonyBoundQueenSkipsJudgeDespiteGoalCriteria(t *testing.T) {
	bus := eventbus.NewEventBus()
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
	if !runtime.agentCtx.AgentSpec.SkipJudge {
		t.Fatalf("colony-bound queen spec SkipJudge = false, want true")
	}
	if agentloop.HasSuccessCriteria(runtime.agentCtx.AgentSpec) {
		t.Fatalf("chat spec = %#v, want no inherited colony criteria", runtime.agentCtx.AgentSpec)
	}
	if runtime.loop.Judge != nil {
		t.Fatalf("colony-bound queen loop Judge = %#v, want nil", runtime.loop.Judge)
	}

	if err := runtime.Deliver("summarise the hive"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts := waitForAssistantParts(t, runtime, 1)
	if got := parts[len(parts)-1]["content"]; got != "The honeycomb is ready." {
		t.Fatalf("assistant content = %#v, want the normal queen reply", got)
	}
	chatCalls, judgeCalls := model.counts()
	if chatCalls != 1 || judgeCalls != 0 {
		t.Fatalf("model calls = chat %d / judge %d, want 1/0", chatCalls, judgeCalls)
	}
	assertNoJudgeFeedback(t, runtime)
}

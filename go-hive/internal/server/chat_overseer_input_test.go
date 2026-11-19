package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

type overseerRecordLLM struct {
	mu      sync.Mutex
	replies []string
	calls   []llm.CompletionRequest
}

func (m *overseerRecordLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	reply := "idle"
	if index := len(m.calls) - 1; index < len(m.replies) {
		reply = m.replies[index]
	}
	return llm.Completion{Content: reply, StopReason: "stop"}, nil
}

func (m *overseerRecordLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *overseerRecordLLM) sawUserText(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.calls {
		for _, message := range req.Messages {
			if message.Role == "user" && strings.Contains(message.Content, text) {
				return true
			}
		}
	}
	return false
}

// Parity-exempt: Go-side overseer input source. The persistent overseer had a
// Queen tool surface but no producer; bound sessions now forward worker
// reports and framework triggers into it.
func TestChatRuntimeFeedsWorkerReportsAndTriggersToOverseer(t *testing.T) {
	provider := &overseerRecordLLM{replies: []string{"idle", "handled", "handled"}}
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         provider,
		EventBus:    eventbus.NewEventBus(),
		StreamID:    "colony_overseer_input",
		StoragePath: t.TempDir(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
	})
	if err := colonyRuntime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(colonyRuntime.Stop)

	prompt := "You are the queen of this test colony."
	queenSpec := agentloop.AgentSpec{ID: "queen", Name: "Queen", AgentType: "event_loop", SystemPrompt: &prompt}
	if _, err := colonyRuntime.StartOverseer(context.Background(), queenSpec, nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !waitForServerCondition(2*time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       eventbus.NewEventBus(),
		Root:      t.TempDir(),
		SessionID: "s-overseer-input",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})

	report := eventbus.NewAgentEvent(eventbus.EventSubagentReport, "worker:1")
	report.Data = map[string]any{
		"worker_id":  "worker:1",
		"batch_id":   "batch_1",
		"batch_size": 1,
		"status":     "success",
		"summary":    "wrote two rows",
		"task":       "fill rows",
	}
	colonyRuntime.EventBus().Publish(report)
	if !waitForServerCondition(2*time.Second, func() bool { return provider.sawUserText("[WORKER_REPORT]") }) {
		t.Fatal("overseer never received the worker report")
	}
	if !provider.sawUserText("wrote two rows") {
		t.Fatal("overseer report is missing the worker summary")
	}

	trigger := agentloop.NewTriggerEvent("timer", "daily")
	if err := runtime.InjectTrigger(trigger); err != nil {
		t.Fatalf("InjectTrigger: %v", err)
	}
	if !waitForServerCondition(2*time.Second, func() bool { return provider.sawUserText("[TRIGGER: timer/daily]") }) {
		t.Fatal("overseer never received the framework trigger")
	}
}

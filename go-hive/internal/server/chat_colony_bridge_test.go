package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
)

// Parity-exempt: Go-side SSE bridge. A bound colony's lifecycle events live on
// its own bus; the session UI follows the session bus, so the bridge is the
// only thing that makes worker started/completed/failed visible.
func TestChatRuntimeBridgesColonyLifecycleToSessionBus(t *testing.T) {
	colonyBus := eventbus.NewEventBus()
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         &fakeChatLLM{},
		EventBus:    colonyBus,
		StreamID:    "colony_bridge",
		StoragePath: t.TempDir(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
	})
	if err := colonyRuntime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(colonyRuntime.Stop)

	sessionBus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Bus:       sessionBus,
		Root:      t.TempDir(),
		SessionID: "s-colony-bridge",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})

	var (
		mu            sync.Mutex
		types         = map[eventbus.EventType]bool{}
		bridgedColony string
	)
	subID := sessionBus.Subscribe([]eventbus.EventType{
		eventbus.EventExecutionStarted,
		eventbus.EventExecutionCompleted,
		eventbus.EventExecutionFailed,
		eventbus.EventSubagentReport,
	}, func(event *eventbus.AgentEvent) {
		if event == nil || event.StreamID == chatStreamID {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		types[event.Type] = true
		if event.ColonyID != nil {
			bridgedColony = *event.ColonyID
		}
	})
	t.Cleanup(func() { sessionBus.Unsubscribe(subID) })

	if _, err := colonyRuntime.SpawnBatch(context.Background(), []colony.BatchTask{{Task: "write one row"}}); err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}

	if !waitForServerCondition(5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return types[eventbus.EventExecutionStarted] && (types[eventbus.EventExecutionCompleted] || types[eventbus.EventSubagentReport])
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("session bus events = %v, want worker start + terminal", types)
	}
	mu.Lock()
	defer mu.Unlock()
	if bridgedColony != "colony_bridge" {
		t.Fatalf("bridged colony_id = %q, want colony_bridge", bridgedColony)
	}
}

func waitForServerCondition(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return condition()
}

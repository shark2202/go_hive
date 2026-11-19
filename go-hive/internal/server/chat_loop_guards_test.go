package server

import (
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
)

func loopGuardSession(t *testing.T, homeRoot string) (*ChatRuntime, *eventbus.EventBus) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_loop_guards",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() {
		runtime.stopLoopGuards()
		_ = runtime.Close()
	})
	return runtime, bus
}

// Parity-exempt: upstream subscribes the stall check in queen_orchestrator; the
// port's session manager is that seam, and this pins that handing a session out
// arms the guards exactly once and teardown removes the subscription.
func TestAddSessionArmsLoopGuards(t *testing.T) {
	home := serverTestHome(t)
	runtime, bus := loopGuardSession(t, home.Root)

	before := bus.SubscriptionCount()
	manager := NewSessionManager()
	session := NewSession("session_loop_guards", bus)
	session.Chat = runtime
	manager.AddSession(session)

	if runtime.loop == nil || runtime.loop.ParkSink == nil {
		t.Fatal("AddSession path: the doom-loop park sink was never wired")
	}

	after := bus.SubscriptionCount()
	if after != before+1 {
		t.Fatalf("subscriptions %d -> %d, want exactly one stall-check subscription", before, after)
	}
	// Every session lookup re-adds/binds; the guard must not stack.
	manager.AddSession(session)
	if got := bus.SubscriptionCount(); got != after {
		t.Fatalf("subscriptions after a second AddSession = %d, want %d", got, after)
	}
	if !manager.StopSession("session_loop_guards") {
		t.Fatal("StopSession reported the session was not registered")
	}
	if got := bus.SubscriptionCount(); got != before {
		t.Fatalf("subscriptions after StopSession = %d, want %d", got, before)
	}
}

// Parity-exempt: the node_tool_doom_loop event had no producer; this pins the
// doom-loop park publication and the generic park fallback.
func TestPublishLoopParkEmitsNodeToolDoomLoopEvent(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := &ChatRuntime{bus: bus, agentCtx: &agentloop.AgentContext{ExecutionID: "exec_1"}}

	doom := make(chan *eventbus.AgentEvent, 1)
	stalled := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventNodeToolDoomLoop}, func(event *eventbus.AgentEvent) {
		doom <- event
	})
	bus.Subscribe([]eventbus.EventType{eventbus.EventNodeStalled}, func(event *eventbus.AgentEvent) {
		stalled <- event
	})

	runtime.publishLoopPark(agentloop.ParkReasonDoomLoop, "`read_file` called 5 times in a row")
	select {
	case event := <-doom:
		if event.StreamID != chatStreamID {
			t.Fatalf("stream = %q, want %q", event.StreamID, chatStreamID)
		}
		if got := event.Data["description"]; got != "`read_file` called 5 times in a row" {
			t.Fatalf("description = %v", got)
		}
		if event.ExecutionID == nil || *event.ExecutionID != "exec_1" {
			t.Fatalf("execution id = %v, want exec_1", event.ExecutionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no node_tool_doom_loop event published")
	}

	runtime.publishLoopPark(agentloop.ParkReasonLLMError, "model call failed")
	select {
	case event := <-stalled:
		if event.Data["reason"] != string(agentloop.ParkReasonLLMError) {
			t.Fatalf("reason = %v, want llm_error", event.Data["reason"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("non-doom park did not publish node_stalled")
	}
}

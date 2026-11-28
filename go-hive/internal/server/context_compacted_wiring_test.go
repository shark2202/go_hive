package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

func contextCompactedTestRuntime(t *testing.T, scheduler *memory.ReflectionScheduler) (*ChatRuntime, *eventbus.EventBus) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime := &ChatRuntime{bus: bus, reflection: scheduler, ctx: context.Background()}
	runtime.wireContextCompactedReflection()
	t.Cleanup(func() {
		if runtime.contextCompactedUnsub != nil {
			runtime.contextCompactedUnsub()
			runtime.contextCompactedUnsub = nil
		}
	})
	return runtime, bus
}

// Parity: framework/agent_loop/internals/compaction.py::compact
func TestPublishContextCompactionStartedEventShape(t *testing.T) {
	runtime, bus := contextCompactedTestRuntime(t, nil)
	events := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventContextCompactionStarted}, func(event *eventbus.AgentEvent) {
		events <- event
	})

	runtime.publishContextCompactionStarted(agentloop.ContextCompactionStarted{
		UsageBefore:          91,
		MessageCount:         42,
		LLMCompactionSkipped: true,
	})
	event := <-events
	if event.StreamID != "queen" || event.NodeID == nil || *event.NodeID != "queen" {
		t.Fatalf("stream/node = %q/%v, want queen/queen", event.StreamID, event.NodeID)
	}
	want := map[string]any{
		"usage_before":           91,
		"message_count":          42,
		"llm_compaction_skipped": true,
	}
	if len(event.Data) != len(want) {
		t.Fatalf("event data = %#v, want exactly %#v", event.Data, want)
	}
	for key, value := range want {
		if event.Data[key] != value {
			t.Fatalf("event data[%q] = %v, want %v", key, event.Data[key], value)
		}
	}
}

// Parity: framework/server/routes_events.py::DEFAULT_EVENT_TYPES
//
// TestConfigureContextCompactedWiresBothCallbacks pins the production bridge:
// the loop owns no bus, so both compaction events must be installed here.
func TestConfigureContextCompactedWiresBothCallbacks(t *testing.T) {
	runtime := &ChatRuntime{bus: eventbus.NewEventBus(), loop: &agentloop.AgentLoop{}}
	runtime.configureContextCompacted()
	if runtime.loop.OnContextCompacted == nil {
		t.Fatal("OnContextCompacted was not wired")
	}
	if runtime.loop.OnContextCompactionStarted == nil {
		t.Fatal("OnContextCompactionStarted was not wired")
	}
	if runtime.contextCompactedUnsub != nil {
		runtime.contextCompactedUnsub()
	}
}

// Parity: framework/agent_loop/internals/compaction.py::log_compaction
func TestPublishContextCompactedEventShape(t *testing.T) {
	runtime, bus := contextCompactedTestRuntime(t, nil)

	events := make(chan *eventbus.AgentEvent, 2)
	bus.Subscribe([]eventbus.EventType{eventbus.EventContextCompacted}, func(event *eventbus.AgentEvent) {
		events <- event
	})

	runtime.publishContextCompacted(agentloop.ContextCompacted{
		Level:         "llm",
		UsageBefore:   91,
		UsageAfter:    42,
		TokensBeforeK: 164,
		TokensAfterK:  76,
	})
	event := <-events
	if event.StreamID != "queen" || event.NodeID == nil || *event.NodeID != "queen" {
		t.Fatalf("stream/node = %q/%v, want queen/queen", event.StreamID, event.NodeID)
	}
	want := map[string]any{
		"level":           "llm",
		"usage_before":    91,
		"usage_after":     42,
		"tokens_before_k": 164,
		"tokens_after_k":  76,
	}
	if len(event.Data) != len(want) {
		t.Fatalf("event data = %#v, want exactly %#v", event.Data, want)
	}
	for key, value := range want {
		if event.Data[key] != value {
			t.Fatalf("event data[%q] = %v, want %v", key, event.Data[key], value)
		}
	}

	inventory := []map[string]any{{"seq": 0, "role": "user", "content_chars": 10}}
	runtime.publishContextCompacted(agentloop.ContextCompacted{
		Level: "llm", UsageBefore: 120, UsageAfter: 40, TokensBeforeK: 240, TokensAfterK: 80,
		MessageInventory: inventory,
	})
	event = <-events
	if _, ok := event.Data["message_inventory"]; !ok {
		t.Fatalf("message_inventory missing from %#v", event.Data)
	}
}

// Parity: core/framework/agents/queen/reflection_agent.py::_on_compaction
func TestContextCompactedTriggersLongReflectionOnce(t *testing.T) {
	longCalls := 0
	scheduler := &memory.ReflectionScheduler{
		Config: func() memory.ReflectionConfig { return memory.ReflectionConfig{EveryTurns: 6, LongEvery: 4} },
		Spawn:  func(work func()) { work() },
		Long: func(context.Context) memory.LoopResult {
			longCalls++
			return memory.LoopResult{Success: true}
		},
	}
	runtime, _ := contextCompactedTestRuntime(t, scheduler)

	// level is not filtered: a prune_only compaction still runs the long pass,
	// exactly like upstream's _on_compaction.
	runtime.publishContextCompacted(agentloop.ContextCompacted{Level: "prune_only", UsageBefore: 90, UsageAfter: 90})

	if longCalls != 1 {
		t.Fatalf("long reflections = %d, want 1", longCalls)
	}
}

// Parity: core/framework/agents/queen/reflection_agent.py::_on_compaction
func TestContextCompactedSkipsLongReflectionWhileRunning(t *testing.T) {
	var longCalls int32
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	scheduler := &memory.ReflectionScheduler{
		Config: func() memory.ReflectionConfig { return memory.ReflectionConfig{EveryTurns: 6, LongEvery: 4} },
		Long: func(context.Context) memory.LoopResult {
			if atomic.AddInt32(&longCalls, 1) == 1 {
				close(started)
				<-release
				close(finished)
			}
			return memory.LoopResult{Success: true}
		},
	}
	runtime, _ := contextCompactedTestRuntime(t, scheduler)

	runtime.publishContextCompacted(agentloop.ContextCompacted{Level: "llm", UsageBefore: 90, UsageAfter: 40})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first compaction reflection never started")
	}

	// The second event lands while the first pass is in flight: the scheduler's
	// running guard must drop it instead of overlapping two memory writers.
	runtime.publishContextCompacted(agentloop.ContextCompacted{Level: "llm", UsageBefore: 88, UsageAfter: 40})
	if got := atomic.LoadInt32(&longCalls); got != 1 {
		t.Fatalf("long reflections before release = %d, want 1", got)
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("first compaction reflection never finished")
	}
	if got := atomic.LoadInt32(&longCalls); got != 1 {
		t.Fatalf("long reflections = %d, want 1", got)
	}
}

// Parity: core/framework/agents/queen/reflection_agent.py::_on_compaction (stream guard)
func TestContextCompactedIgnoresNonQueenStream(t *testing.T) {
	longCalls := 0
	scheduler := &memory.ReflectionScheduler{
		Config: func() memory.ReflectionConfig { return memory.ReflectionConfig{EveryTurns: 6, LongEvery: 4} },
		Spawn:  func(work func()) { work() },
		Long: func(context.Context) memory.LoopResult {
			longCalls++
			return memory.LoopResult{Success: true}
		},
	}
	_, bus := contextCompactedTestRuntime(t, scheduler)

	// Bypass the loop publisher: a worker-stream event must not reach the
	// queen's reflection (upstream checks event.stream_id == "queen").
	event := eventbus.NewAgentEvent(eventbus.EventContextCompacted, "worker-1")
	event.Data = map[string]any{"level": "llm"}
	bus.Publish(event)

	if longCalls != 0 {
		t.Fatalf("long reflections = %d, want 0 for a worker stream", longCalls)
	}
}

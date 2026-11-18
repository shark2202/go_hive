package eventbus

// Reliability probes for the event bus: subscribe/unsubscribe churn racing
// with Publish must stay race-free, never deadlock, and leave no subscription
// behind. This is the layer every SSE endpoint sits on.

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func reliabilityEvent(i int) *AgentEvent {
	event := NewAgentEvent(EventCredentialProviderConnected, "global")
	event.Data = map[string]any{"i": i}
	return event
}

// Parity-exempt: concurrency probe for concurrent subscriptions and publishes;
// no Python counterpart (the reference bus is single-threaded asyncio).
func TestConcurrentSubscribeUnsubscribePublish(t *testing.T) {
	bus := NewEventBus(WithMaxConcurrentHandlers(32), WithMaxHistory(512))
	var handled atomic.Int64

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for worker := 0; worker < 12; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for i := 0; i < 40; i++ {
					id := bus.Subscribe([]EventType{EventCredentialProviderConnected}, func(event *AgentEvent) {
						handled.Add(1)
					})
					bus.Publish(reliabilityEvent(worker))
					_ = bus.GetHistory(WithHistoryLimit(8))
					bus.Unsubscribe(id)
				}
			}(worker)
		}
		for worker := 0; worker < 4; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for i := 0; i < 80; i++ {
					bus.Publish(reliabilityEvent(worker))
				}
			}(worker)
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent subscribe/publish deadlocked")
	}
	if count := bus.SubscriptionCount(); count != 0 {
		t.Fatalf("subscriptions = %d after every owner unsubscribed", count)
	}
	if bus.GetStats()["subscriptions"] != 0 {
		t.Fatalf("stats subscriptions = %v, want 0", bus.GetStats()["subscriptions"])
	}
}

// Parity-exempt: a handler may unsubscribe itself from inside dispatch; the
// publisher must not hold its lock while handlers run.
func TestHandlerCanUnsubscribeDuringPublish(t *testing.T) {
	bus := NewEventBus()
	var id string
	id = bus.Subscribe([]EventType{EventCredentialProviderConnected}, func(event *AgentEvent) {
		bus.Unsubscribe(id)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		bus.Publish(reliabilityEvent(1))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish deadlocked when a handler unsubscribed itself")
	}
	if count := bus.SubscriptionCount(); count != 0 {
		t.Fatalf("subscriptions = %d, want 0", count)
	}
}

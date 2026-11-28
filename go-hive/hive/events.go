package hive

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// EventType is the name of one runtime event.
//
// Parity: framework/host/event_bus.py::EventType
type EventType = eventbus.EventType

// Event is one published runtime event.
//
// Parity: framework/host/event_bus.py::AgentEvent
type Event = eventbus.AgentEvent

// EventHandler receives published events. Handlers must not block: the bus
// dispatches them on its own goroutine with a concurrency bound.
//
// Parity: framework/host/event_bus.py EventHandler
type EventHandler = eventbus.EventHandler

// EventFilter narrows a subscription or a history query. Empty fields match
// everything.
//
// Parity: framework/host/event_bus.py EventBus.subscribe filters
type EventFilter struct {
	StreamID    string
	NodeID      string
	ExecutionID string
	ColonyID    string
}

// EventBus is the runtime's pub/sub bus. A colony owns one; a standalone loop
// user can build one and publish/subscribe to it directly.
//
// Parity: framework/host/event_bus.py::EventBus
type EventBus struct {
	inner *eventbus.EventBus
}

// NewEventBus builds an empty bus.
//
// Parity: framework/host/event_bus.py::EventBus
func NewEventBus() *EventBus {
	return &EventBus{inner: eventbus.NewEventBus()}
}

func (f EventFilter) options() []eventbus.SubscribeOption {
	opts := make([]eventbus.SubscribeOption, 0, 4)
	if f.StreamID != "" {
		opts = append(opts, eventbus.WithFilterStream(f.StreamID))
	}
	if f.NodeID != "" {
		opts = append(opts, eventbus.WithFilterNode(f.NodeID))
	}
	if f.ExecutionID != "" {
		opts = append(opts, eventbus.WithFilterExecution(f.ExecutionID))
	}
	if f.ColonyID != "" {
		opts = append(opts, eventbus.WithFilterColony(f.ColonyID))
	}
	return opts
}

// Subscribe registers a handler for the given event types and returns the
// subscription id used by Unsubscribe.
//
// Parity: framework/host/event_bus.py::EventBus.subscribe
func (b *EventBus) Subscribe(types []EventType, handler EventHandler, filter EventFilter) string {
	if b == nil || b.inner == nil {
		return ""
	}
	return b.inner.Subscribe(types, handler, filter.options()...)
}

// SubscriptionCount is the number of live subscriptions.
//
// Parity-exempt: Go-only introspection accessor.
func (b *EventBus) SubscriptionCount() int {
	if b == nil || b.inner == nil {
		return 0
	}
	return b.inner.SubscriptionCount()
}

// Unsubscribe removes a subscription by id.
//
// Parity: framework/host/event_bus.py::EventBus.unsubscribe
func (b *EventBus) Unsubscribe(id string) bool {
	if b == nil || b.inner == nil {
		return false
	}
	return b.inner.Unsubscribe(id)
}

// Publish delivers one event to every matching subscriber.
//
// Parity: framework/host/event_bus.py::EventBus.publish
func (b *EventBus) Publish(event *Event) {
	if b == nil || b.inner == nil {
		return
	}
	b.inner.Publish(event)
}

// History returns recorded events, newest first, bounded by limit. limit <= 0
// uses the runtime default (100).
//
// Parity: framework/host/event_bus.py::EventBus.get_history
func (b *EventBus) History(limit int) []*Event {
	if b == nil || b.inner == nil {
		return nil
	}
	if limit <= 0 {
		return b.inner.GetHistory()
	}
	return b.inner.GetHistory(eventbus.WithHistoryLimit(limit))
}

// WaitFor blocks until one matching event arrives or ctx ends.
//
// Parity: framework/host/event_bus.py::EventBus.wait_for
func (b *EventBus) WaitFor(ctx context.Context, eventType EventType, filter EventFilter) *Event {
	if b == nil || b.inner == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return b.inner.WaitFor(ctx, eventType, filter.options()...)
}

// NewEvent builds an empty event of the given type for a stream.
//
// Parity: framework/host/event_bus.py::AgentEvent
func NewEvent(eventType EventType, streamID string) *Event {
	return eventbus.NewAgentEvent(eventType, streamID)
}

// EventTypes lists every event type the runtime knows.
//
// Parity: framework/host/event_bus.py::EventType
func EventTypes() []EventType {
	return append([]EventType(nil), eventbus.AllEventTypes...)
}

// EventTypeByName resolves an event type from its wire name.
//
// Parity: framework/host/event_bus.py::EventType
func EventTypeByName(name string) (EventType, bool) {
	return eventbus.EventTypeByName(name)
}

// The event types a derivative host most often follows. The full set is in
// EventTypes.
const (
	EventExecutionStarted   = eventbus.EventExecutionStarted
	EventExecutionCompleted = eventbus.EventExecutionCompleted
	EventExecutionFailed    = eventbus.EventExecutionFailed
	EventLLMTextDelta       = eventbus.EventLLMTextDelta
	EventToolCallStarted    = eventbus.EventToolCallStarted
	EventToolCallCompleted  = eventbus.EventToolCallCompleted
	EventWorkerCompleted    = eventbus.EventWorkerCompleted
	EventJudgeVerdict       = eventbus.EventJudgeVerdict
	EventSessionTitleSet    = eventbus.EventSessionTitleUpdated
)

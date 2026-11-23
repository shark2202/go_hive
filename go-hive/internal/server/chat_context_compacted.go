package server

import (
	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// configureContextCompacted bridges the loop's completed compaction to the
// session bus and subscribes the queen's reflection trigger. The loop owns no
// bus, so the callback is the only bridge (the same role upstream's event_bus
// argument plays inside compact()).
//
// Parity: framework/agent_loop/internals/compaction.py::compact,
// core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
func (r *ChatRuntime) configureContextCompacted() {
	// A completed compaction is published on the session bus; the queen's
	// reflection subscriber turns it into one long memory pass.
	r.loop.OnContextCompacted = r.publishContextCompacted
	r.wireContextCompactedReflection()
}

// wireContextCompactedReflection subscribes the session bus to the loop's
// completed-compaction event. The subscription is queen-stream only, the same
// scope upstream's subscribe_reflection_triggers handler enforces.
//
// Parity: core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
func (r *ChatRuntime) wireContextCompactedReflection() {
	if r == nil || r.bus == nil || r.reflection == nil || r.contextCompactedUnsub != nil {
		return
	}
	subID := r.bus.Subscribe(
		[]eventbus.EventType{eventbus.EventContextCompacted},
		r.onContextCompactedEvent,
		eventbus.WithFilterStream(chatStreamID),
	)
	r.contextCompactedUnsub = func() { r.bus.Unsubscribe(subID) }
}

// publishContextCompacted publishes the loop's completed compaction on the
// session bus with upstream's payload fields.
//
// Parity: framework/agent_loop/internals/compaction.py::log_compaction
func (r *ChatRuntime) publishContextCompacted(payload agentloop.ContextCompacted) {
	if r == nil || r.bus == nil {
		return
	}
	event := eventbus.NewAgentEvent(eventbus.EventContextCompacted, chatStreamID)
	event.NodeID = eventbus.StrPtr(chatNodeID)
	event.Data = map[string]any{
		"level":           payload.Level,
		"usage_before":    payload.UsageBefore,
		"usage_after":     payload.UsageAfter,
		"tokens_before_k": payload.TokensBeforeK,
		"tokens_after_k":  payload.TokensAfterK,
	}
	// Upstream attaches the manifest only when pre_inventory is not None,
	// which is gated on the pre-compaction usage ratio reaching 1.0.
	if payload.MessageInventory != nil {
		event.Data["message_inventory"] = payload.MessageInventory
	}
	r.bus.Publish(event)
}

// onContextCompactedEvent turns one CONTEXT_COMPACTED event into the queen's
// long memory reflection. The scheduler itself skips the trigger while a
// reflection is already running or after Close, so the handler stays thin.
//
// Parity: core/framework/agents/queen/reflection_agent.py::_on_compaction
func (r *ChatRuntime) onContextCompactedEvent(event *eventbus.AgentEvent) {
	if r == nil || event == nil || event.StreamID != chatStreamID {
		return
	}
	if r.reflection != nil {
		r.reflection.OnCompaction(r.ctx, chatStreamID)
	}
}

package colony

// Worker tool-doom-loop telemetry, colony half.
//
// The deterministic detector already warns the model and escalates to the
// queen (worker_escalation.go). Upstream also publishes NODE_TOOL_DOOM_LOOP at
// the same detection point so the session event log and the SSE feed can show
// the repetition; its scoped StreamEventBus stamps the colony id on every
// event a runtime publishes. The worker's own loop carries no event bus, so
// the runtime is the publisher here: it stamps the worker stream (worker:<id>),
// the colony id and the execution id on the event.
//
// The event stays on the worker's stream, as upstream's does: the session
// bridge mirrors it onto the session bus (chat_colony_bridge.go), where the
// dashboard sees it with ?watch=worker:<id>, the session event log records it
// for GET /events/history, and a client that did not ask to watch the worker
// is spared the chatter exactly like every other worker-local event.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_tool_doom_loop)
// Parity: framework/host/colony_runtime.py::StreamEventBus (colony_id stamping)

import (
	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// workerDoomLoopSink returns the AgentLoop sink that publishes one
// node_tool_doom_loop event for a worker's first repeated-call detection of a
// turn-loop. Nil when the runtime has no bus or the worker is nil: a loop
// without a destination stays silent rather than stuck.
func (r *ColonyRuntime) workerDoomLoopSink(worker *Worker) agentloop.DoomLoopSink {
	if r == nil || r.bus == nil || worker == nil {
		return nil
	}
	streamID := worker.StreamID
	if streamID == "" {
		return nil
	}
	return func(tool string, streak int) {
		event := eventbus.NewAgentEvent(eventbus.EventNodeToolDoomLoop, streamID)
		event.NodeID = eventbus.StrPtr(streamID)
		if executionID := workerExecutionID(worker); executionID != "" {
			event.ExecutionID = &executionID
		}
		colonyID := r.streamID
		event.ColonyID = &colonyID
		event.Data = map[string]any{
			"description": agentloop.DoomLoopDescription(tool, streak),
		}
		r.bus.Publish(event)
	}
}

// workerExecutionID is the execution stamp the worker's other events carry
// (the live emitter falls back to the worker id when the context has none).
func workerExecutionID(worker *Worker) string {
	if worker == nil {
		return ""
	}
	if worker.Context != nil && worker.Context.ExecutionID != "" {
		return worker.Context.ExecutionID
	}
	return worker.ID
}

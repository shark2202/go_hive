package colony

// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// Parity: framework/agent_loop/internals/event_publishing.py::publish_llm_turn_complete
// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_started
// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_completed
//
// Worker turn progress. Upstream's loop publishes its text, token and tool
// events on whatever stream the worker runs under; the Go worker loop published
// none, so a spawned worker's live progress only existed inside its own
// conversation. The session bridge re-stamps these events onto the session
// stream (relayed_from=<worker id>) so the dashboard can follow a worker while
// it runs.

import (
	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// workerEmitter is the LoopObserver a spawned worker's turn runs under. It
// publishes the same event family the chat queen's emitter does, under the
// worker's own stream; the session bridge decides what reaches the client.
type workerEmitter struct {
	bus         *eventbus.EventBus
	streamID    string
	executionID string
}

func newWorkerEmitter(bus *eventbus.EventBus, streamID, executionID string) *workerEmitter {
	return &workerEmitter{bus: bus, streamID: streamID, executionID: executionID}
}

// IterationStarted is not published: a worker's inner counter is loop
// diagnostics, not client-facing progress.
func (e *workerEmitter) IterationStarted(int) {}

// AssistantText publishes the worker's message as client output on its own
// stream, the event the session bridge re-labels for the session pane.
func (e *workerEmitter) AssistantText(content, snapshot string, iteration int) {
	if e == nil || e.bus == nil || e.streamID == "" {
		return
	}
	e.bus.EmitClientOutputDelta(e.streamID, e.streamID, content, snapshot, &e.executionID, &iteration, 0)
}

// LLMTurnComplete carries the worker's token usage so the session's counters
// include work done by its colony.
func (e *workerEmitter) LLMTurnComplete(completion llm.Completion, iteration int) {
	if e == nil || e.bus == nil || e.streamID == "" {
		return
	}
	e.bus.EmitLLMTurnComplete(e.streamID, e.streamID,
		completion.StopReason, completion.Model,
		completion.InputTokens, completion.OutputTokens,
		completion.CachedTokens, completion.CacheCreationTokens,
		completion.CostUSD, nil,
		&e.executionID, &iteration, nil, nil, nil, nil)
}

// ToolCallStarted and ToolCallCompleted publish the worker's tool activity.
func (e *workerEmitter) ToolCallStarted(call agentloop.ToolCallRequest) {
	if e == nil || e.bus == nil || e.streamID == "" {
		return
	}
	e.bus.EmitToolCallStarted(e.streamID, e.streamID, call.ID, call.Name, call.Input, &e.executionID)
}

func (e *workerEmitter) ToolCallCompleted(call agentloop.ToolCallRequest, result agentloop.ToolResult) {
	if e == nil || e.bus == nil || e.streamID == "" {
		return
	}
	e.bus.EmitToolCallCompleted(e.streamID, e.streamID, call.ID, call.Name, result.Content, result.IsError, &e.executionID)
}

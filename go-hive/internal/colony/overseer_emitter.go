package colony

// Parity: framework/agent_loop/internals/event_publishing.py (queen stream)
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
//
// The persistent overseer is the colony's client-facing queen: upstream starts
// it with stream_id="overseer" and its turns publish the same client-facing
// event family a chat queen's turn does. The Go worker loop publishes no
// observer events, so an overseer turn was invisible to the session that
// front-ends it. This emitter publishes the overseer's text and turn
// boundaries on the colony bus; the session bridge mirrors them onto the
// session bus.

import (
	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// overseerEmitter is the LoopObserver the overseer's turns run under.
type overseerEmitter struct {
	bus         *eventbus.EventBus
	streamID    string
	executionID string
}

func newOverseerEmitter(bus *eventbus.EventBus, streamID, executionID string) *overseerEmitter {
	if streamID == "" {
		streamID = "overseer"
	}
	return &overseerEmitter{bus: bus, streamID: streamID, executionID: executionID}
}

// begin announces the turn the way a chat queen's turn opens it.
func (e *overseerEmitter) begin() {
	if e == nil || e.bus == nil {
		return
	}
	e.bus.EmitExecutionStarted(e.streamID, e.executionID, nil, nil, nil)
	e.bus.EmitLoopStateChanged(e.streamID, e.streamID, "executing", &e.executionID, nil, nil, nil)
}

// end closes the turn. It publishes the same awaiting_user loop state the chat
// emitter does, so the session snapshot reports why the turn yielded — an
// ask_user park, or an ordinary turn_done — instead of leaving the last
// "executing" state to age into a false stall. A failed turn reports
// execution_failed so the session does not keep a spinner forever.
func (e *overseerEmitter) end(err error, parkReason agentloop.ParkReason) {
	if e == nil || e.bus == nil {
		return
	}
	if err != nil {
		e.bus.EmitExecutionFailed(e.streamID, e.executionID, err.Error(), nil, nil)
		return
	}
	if parkReason == "" {
		parkReason = agentloop.ParkReasonTurnDone
	}
	reason := string(parkReason)
	e.bus.EmitLoopStateChanged(e.streamID, e.streamID, "awaiting_user", &e.executionID, &reason, nil, nil)
	e.bus.EmitExecutionCompleted(e.streamID, e.executionID, nil, nil, nil)
}

// IterationStarted is not published: the chat queen's emitter only announces
// iterations for its own loop diagnostics, and the session pane does not need
// the overseer's inner counter.
func (e *overseerEmitter) IterationStarted(int) {}

// AssistantText publishes the overseer's reply as client output, exactly the
// event the dashboard renders as the queen talking.
func (e *overseerEmitter) AssistantText(content, snapshot string, iteration int) {
	if e == nil || e.bus == nil {
		return
	}
	e.bus.EmitClientOutputDelta(e.streamID, e.streamID, content, snapshot, &e.executionID, &iteration, 0)
}

// LLMTurnComplete is not published: the chat emitter reports per-call usage for
// the session's token counters, and the overseer's usage would double-count a
// budget the session does not own.
func (e *overseerEmitter) LLMTurnComplete(llm.Completion, int) {}

// Tool events publish the overseer's tool activity on the colony bus. The
// session bridge consumes the completed result as the credential-miss signal
// (it does not re-mirror the overseer's own tool pills onto the session
// stream); a colony viewer still sees them.
func (e *overseerEmitter) ToolCallStarted(call agentloop.ToolCallRequest) {
	if e == nil || e.bus == nil {
		return
	}
	e.bus.EmitToolCallStarted(e.streamID, e.streamID, call.ID, call.Name, call.Input, &e.executionID)
}

func (e *overseerEmitter) ToolCallCompleted(call agentloop.ToolCallRequest, result agentloop.ToolResult) {
	if e == nil || e.bus == nil {
		return
	}
	e.bus.EmitToolCallCompleted(e.streamID, e.streamID, call.ID, call.Name, result.Content, result.IsError, &e.executionID)
}

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

// end closes the turn. A failed turn reports execution_failed so the session
// does not keep a spinner forever.
func (e *overseerEmitter) end(err error) {
	if e == nil || e.bus == nil {
		return
	}
	if err != nil {
		e.bus.EmitExecutionFailed(e.streamID, e.executionID, err.Error(), nil, nil)
	} else {
		e.bus.EmitExecutionCompleted(e.streamID, e.executionID, nil, nil, nil)
	}
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

// Tool events stay off the session stream: the overseer's tool pills are a
// colony-view concern, and a tool call carries no client-facing text.
func (e *overseerEmitter) ToolCallStarted(agentloop.ToolCallRequest) {}

func (e *overseerEmitter) ToolCallCompleted(agentloop.ToolCallRequest, agentloop.ToolResult) {}

package agentloop

// Worker escalation: the framework-level `escalate` tool.
//
// A worker that is blocked (missing credentials, an ambiguous constraint, a
// tool that keeps failing) escalates to the queen instead of guessing. The call
// is synthetic like ask_user: the loop never hands it to the host's tool
// executor. It publishes ESCALATION_REQUESTED through the host's sink, then
// parks the turn until the queen injects guidance (or the run is stopped).
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (escalate branch)
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (AWAITING_QUEEN)
// Parity: framework/agent_loop/internals/synthetic_tools.py::build_escalate_tool

import (
	"context"
	"strings"
)

// EscalationRequest is one worker->queen escalation. Its fields are exactly the
// ones the upstream ESCALATION_REQUESTED event carries; the loop hands them to
// the host sink so this package stays free of the event bus.
type EscalationRequest struct {
	StreamID    string
	NodeID      string
	ExecutionID string
	Reason      string
	Context     string
}

// EscalationSink publishes the host's ESCALATION_REQUESTED event for one
// escalation and returns the request_id stamped on it (the queen's
// reply_to_worker addresses the question by that id). A nil sink is upstream's
// "EventBus unavailable" case: the call is refused with an explicit error,
// never silently dropped.
type EscalationSink func(EscalationRequest) (requestID string, err error)

const (
	// escalationRefusalMessage is the upstream text for a non-worker caller.
	escalationRefusalMessage = "ERROR: escalate is only available to worker nodes/sub-agents, not queen/judge streams."
	// escalationBusUnavailableMessage is the upstream text when no event bus
	// can carry the request.
	escalationBusUnavailableMessage = "ERROR: EventBus unavailable. Could not emit escalation request."
	// escalationWaitingMessage is what the model reads back: the node is
	// parked until the queen injects guidance.
	escalationWaitingMessage = "Escalation requested to queen; waiting for guidance."
)

// escalationStreamEligible reports whether a stream may escalate. Upstream
// gates the tool to worker nodes: the queen, the judge and the colony overseer
// never get it, so a hallucinated call from one of them is refused instead of
// parked.
func escalationStreamEligible(streamID string) bool {
	switch streamID {
	case "", "queen", "judge", "overseer":
		return false
	}
	return true
}

// executeEscalateTool runs one escalate call in-loop.
func (l *AgentLoop) executeEscalateTool(agentCtx *AgentContext, req ToolCallRequest) ToolResult {
	if !escalationStreamEligible(agentCtx.StreamID) {
		return ToolResult{Content: escalationRefusalMessage, IsError: true}
	}
	if agentCtx.EscalationSink == nil {
		return ToolResult{Content: escalationBusUnavailableMessage, IsError: true}
	}
	requestID, err := agentCtx.EscalationSink(EscalationRequest{
		StreamID:    agentCtx.StreamID,
		NodeID:      agentCtx.StreamID,
		ExecutionID: agentCtx.ExecutionID,
		Reason:      strings.TrimSpace(toolInputString(req.Input, "reason")),
		Context:     strings.TrimSpace(toolInputString(req.Input, "context")),
	})
	if err != nil {
		return ToolResult{Content: escalationBusUnavailableMessage, IsError: true}
	}
	// An escalation without a request_id cannot be answered by
	// reply_to_worker; upstream still parks and tells the queen to relay by
	// hand (the handoff text covers that case).
	l.markQueenInputRequested(requestID)
	return ToolResult{Content: escalationWaitingMessage}
}

// emitUnansweredEscalation publishes the escalation upstream emits when a
// worker is wound down while still waiting for queen guidance: the question
// stays addressable for the session instead of vanishing with the worker.
func (l *AgentLoop) emitUnansweredEscalation(agentCtx *AgentContext) {
	if agentCtx == nil || agentCtx.EscalationSink == nil || !escalationStreamEligible(agentCtx.StreamID) {
		return
	}
	_, _ = agentCtx.EscalationSink(EscalationRequest{
		StreamID:    agentCtx.StreamID,
		NodeID:      agentCtx.StreamID,
		ExecutionID: agentCtx.ExecutionID,
		Reason:      "Blocked waiting for queen guidance - no input received",
		Context:     "Worker escalated but received no queen guidance before shutdown",
	})
}

// markQueenInputRequested records that this tool batch escalated, so the turn
// loop parks before its next model call. Upstream carries the same fact in the
// loop-local queen_input_requested flag.
func (l *AgentLoop) markQueenInputRequested(requestID string) {
	l.mu.Lock()
	l.queenInputRequested = true
	l.queenInputRequestID = requestID
	l.mu.Unlock()
}

// clearQueenInputRequested drops a stale park request at the start of a turn: a
// previous turn that ended (report_to_parent in the same batch, an error, a
// stop) must never park the next one.
func (l *AgentLoop) clearQueenInputRequested() {
	l.mu.Lock()
	l.queenInputRequested = false
	l.queenInputRequestID = ""
	l.mu.Unlock()
}

func (l *AgentLoop) takeQueenInputRequested() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	requested := l.queenInputRequested
	l.queenInputRequested = false
	l.queenInputRequestID = ""
	return requested
}

// inputSignal lazily creates the channel InjectEvent pings so a parked worker
// wakes the moment guidance lands. A zero AgentLoop built by hand in a test
// still gets a channel rather than a nil send.
func (l *AgentLoop) inputSignal() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inputReady == nil {
		l.inputReady = make(chan struct{}, 1)
	}
	return l.inputReady
}

func (l *AgentLoop) signalInputReady() {
	select {
	case l.inputSignal() <- struct{}{}:
	default:
	}
}

// awaitInjectedInput blocks until input is queued for this loop or ctx is done.
// It reports true when input arrived (the queen's guidance drains into the
// conversation on the loop's next boundary), false when the run was stopped.
func (l *AgentLoop) awaitInjectedInput(ctx context.Context) bool {
	if l.InputWaiting() {
		return true
	}
	signal := l.inputSignal()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-signal:
			if l.InputWaiting() {
				return true
			}
		}
	}
}

// settleQueenInputPark blocks the turn when its tool batch called escalate.
// It reports false when the run was stopped before guidance arrived; the caller
// then ends the turn. True means the loop may continue (no escalation, or the
// queen's reply is queued and drains into the conversation on the next
// boundary).
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6h”
// worker wait for queen guidance)
func (l *AgentLoop) settleQueenInputPark(ctx context.Context, agentCtx *AgentContext) bool {
	if !l.takeQueenInputRequested() {
		return true
	}
	if l.awaitInjectedInput(ctx) {
		return true
	}
	l.emitUnansweredEscalation(agentCtx)
	return false
}

func toolInputString(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, _ := input[key].(string)
	return value
}

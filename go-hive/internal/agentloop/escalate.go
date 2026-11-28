package agentloop

// Worker escalation: the framework-level `escalate` tool and the automatic
// doom-loop producer.
//
// Upstream 1.0.14 offers `escalate` only to the legacy primary worker
// (stream_id="worker", no colon): a worker that is blocked asks the queen and
// parks until guidance arrives. Parallel workers (stream_id="worker:<id>", what
// run_worker spawns here) report_to_parent and fail fast instead; the one
// escalation producer upstream leaves open for them is the tool doom-loop
// detector (escalateDoomLoop), which tells the queen without parking the
// worker.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (escalate branch)
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (AWAITING_QUEEN)
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (doom-loop producer)
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
	// EscalateToolName is the synthetic worker->queen escalate tool. Named here
	// so the tool surface builders and the dispatch share one literal.
	EscalateToolName = "escalate"
	// escalationRefusalMessage is the upstream text for a non-worker caller.
	escalationRefusalMessage = "ERROR: escalate is only available to worker nodes/sub-agents, not queen/judge streams."
	// escalationParallelWorkerRefusalMessage explains the refusal of a call
	// from a stream that reports to a parent (a colony worker).
	escalationParallelWorkerRefusalMessage = "ERROR: escalate is not available to colony workers. You report to the queen with report_to_parent; a tool doom loop is escalated automatically."
	// escalationBusUnavailableMessage is the upstream text when no event bus
	// can carry the request.
	escalationBusUnavailableMessage = "ERROR: EventBus unavailable. Could not emit escalation request."
	// escalationWaitingMessage is what the model reads back: the node is
	// parked until the queen injects guidance.
	escalationWaitingMessage = "Escalation requested to queen; waiting for guidance."
	// doomLoopEscalationNotice is the message upstream adds to the conversation
	// when the tool doom-loop detector escalates for a worker.
	doomLoopEscalationNotice = "[SYSTEM] Escalated tool doom loop to queen for intervention."
)

// escalationStreamEligible reports whether a stream may escalate. Upstream
// 1.0.14 gates the tool to the legacy primary worker: the queen, the judge and
// the colony overseer never get it, and neither does a worker that reports to a
// parent (stream_id="worker:<id>", the parallel fan-out shape). Parallel
// workers fail fast via report_to_parent; the one escalation producer upstream
// leaves open for them is the automatic tool doom-loop detector below.
//
// The gate is enforced at dispatch as well as on the advertised tool surface:
// a model can emit a call the surface never offered, and a hallucinated
// escalate must not park a fail-fast worker. Upstream's dispatch itself only
// refuses queen/judge; refusing worker:<id> here is the port's stricter half of
// the same surface rule (documented in docs/queen-lifecycle-tools.md §6).
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (tool surface gate
// is_parallel_worker / _reports_to_parent)
func escalationStreamEligible(streamID string) bool {
	switch streamID {
	case "", "queen", "judge", "overseer":
		return false
	}
	return !strings.HasPrefix(streamID, "worker:")
}

// doomLoopEscalationEligible reports whether the automatic tool-doom-loop
// producer may publish for this context. It is upstream's 6f' gate: the queen,
// the judge and a synthetic/event-triggered turn have a direct human or no
// live audience, so the escalation is suppressed; colony workers (including
// parallel worker:<id> streams, which the escalate tool itself refuses) keep
// this one automatic channel.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' escalation
// branch: not supports_direct_user_io, not event_triggered, stream not in
// ("queen", "judge"), event bus present)
func doomLoopEscalationEligible(agentCtx *AgentContext) bool {
	if agentCtx == nil || agentCtx.EscalationSink == nil || agentCtx.EventTriggered {
		return false
	}
	switch agentCtx.StreamID {
	case "", "queen", "judge", "overseer":
		return false
	}
	return true
}

// escalateDoomLoop publishes the automatic worker->queen escalation upstream
// raises when the tool doom-loop detector trips. This is upstream's second
// ESCALATION_REQUESTED producer (the first is the escalate tool), and the only
// one that covers parallel workers: the worker does not ask and does not wait —
// the queen is told and the worker keeps working, with the deterministic park
// at the higher threshold as the terminal bound.
//
// Dedup: the caller invokes this exactly when the detector crosses its warning
// threshold (Observe returns one warning per streak), and this method latches
// once per turn-loop so a worker thrashing through several streaks cannot flood
// the queen's inbox. A failed publish releases the latch so a later streak can
// retry. Nil sink (queen, judge, overseer, any host without a colony bus) is a
// no-op — the same "no live audience" gate upstream applies.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom loop
// -> emit_escalation_requested, reason "Tool doom loop detected")
func (l *AgentLoop) escalateDoomLoop(agentCtx *AgentContext, tool string, streak int) string {
	return l.escalateDoomLoopDescription(agentCtx, tool, streak, doomLoopDescription(tool, streak))
}

// escalateDoomLoopDescription is the same producer with an explicit context
// string, so the turn-level fingerprint window can send upstream's full
// "Doom loop detected: N identical consecutive tool calls (...)" description
// instead of the per-call streak wording.
func (l *AgentLoop) escalateDoomLoopDescription(agentCtx *AgentContext, tool string, streak int, description string) string {
	if l == nil || !doomLoopEscalationEligible(agentCtx) {
		return ""
	}
	l.mu.Lock()
	if l.doomLoopEscalated {
		l.mu.Unlock()
		return ""
	}
	l.doomLoopEscalated = true
	l.mu.Unlock()
	_, err := agentCtx.EscalationSink(EscalationRequest{
		StreamID:    agentCtx.StreamID,
		NodeID:      agentCtx.StreamID,
		ExecutionID: agentCtx.ExecutionID,
		Reason:      "Tool doom loop detected",
		Context:     description,
	})
	if err != nil {
		l.mu.Lock()
		l.doomLoopEscalated = false
		l.mu.Unlock()
		return ""
	}
	l.bumpBy("doom_loop_escalated", 1)
	return doomLoopEscalationNotice
}

// executeEscalateTool runs one escalate call in-loop.
func (l *AgentLoop) executeEscalateTool(agentCtx *AgentContext, req ToolCallRequest) ToolResult {
	if !escalationStreamEligible(agentCtx.StreamID) {
		if strings.HasPrefix(agentCtx.StreamID, "worker:") {
			return ToolResult{Content: escalationParallelWorkerRefusalMessage, IsError: true}
		}
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
	if agentCtx == nil || agentCtx.EscalationSink == nil {
		return
	}
	if !escalationStreamEligible(agentCtx.StreamID) {
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

// settleQueenInputPark blocks the turn when its tool batch called escalate (the
// legacy worker path; parallel workers never reach it). It reports false when
// the run was stopped before guidance arrived; the caller then ends the turn.
// True means the loop may continue (no escalation, or the queen's reply is
// queued and drains into the conversation on the next boundary).
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

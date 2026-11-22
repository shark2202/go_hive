package colony

// Worker -> queen escalation, colony half.
//
// A worker's escalate tool call is synthetic: the agent loop refuses/parks and
// asks its host to publish the event. This file is the host: it stamps the
// ESCALATION_REQUESTED event with the colony id (so the queen session's
// colony-scoped subscription accepts it) and a fresh request_id (so
// reply_to_worker can address the question), exactly like the upstream
// StreamEventBus + AgentLoop pair.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (escalate branch)
// Parity: framework/host/colony_runtime.py::StreamEventBus (colony_id stamping)
// Parity: framework/host/colony_runtime.py::ColonyRuntime.inject_input

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// EscalationStreams returns the stream ids that may escalate through this
// runtime: the colony's own workers. The persistent overseer is a queen-stage
// node and stays out, matching the upstream tool gate.
func (r *ColonyRuntime) escalationSinkForWorker() agentloop.EscalationSink {
	if r == nil {
		return nil
	}
	return r.requestWorkerEscalation
}

// requestWorkerEscalation publishes one ESCALATION_REQUESTED event and returns
// the request_id stamped on it. The queen session's routing
// (server.InstallWorkerEscalationRouting) records the entry and surfaces the
// handoff; reply_to_worker then injects guidance back into this runtime.
func (r *ColonyRuntime) requestWorkerEscalation(req agentloop.EscalationRequest) (string, error) {
	if r == nil || r.bus == nil {
		return "", errors.New("colony: no event bus on this runtime")
	}
	if req.StreamID == "" {
		return "", errors.New("colony: escalation without a worker stream")
	}
	requestID := newEscalationRequestID()
	colonyID := r.streamID
	event := eventbus.NewAgentEvent(eventbus.EventEscalationRequested, req.StreamID)
	event.NodeID = eventbus.StrPtr(req.NodeID)
	if req.ExecutionID != "" {
		executionID := req.ExecutionID
		event.ExecutionID = &executionID
	}
	event.ColonyID = &colonyID
	event.Data = map[string]any{
		"request_id": requestID,
		"reason":     req.Reason,
		"context":    req.Context,
	}
	r.bus.Publish(event)
	return requestID, nil
}

// newEscalationRequestID mints the per-question id reply_to_worker addresses.
// Upstream uses uuid4().hex; 16 random bytes carry the same collision budget.
func newEscalationRequestID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "escalation_" + hex.EncodeToString(raw)
	}
	return hex.EncodeToString(raw)
}

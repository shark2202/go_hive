package server

// Parity: framework/server/queen_orchestrator.py::install_worker_escalation_routing

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

const MaxPendingEscalations = 32

const queueFullReply = "[QUEEN_REPLY] queue_full — queen inbox saturated; proceed with best judgment or retry later."

// Parity: framework/server/queen_orchestrator.py::install_worker_escalation_routing
func InstallWorkerEscalationRouting(session *Session, colonyRuntime ...ColonyRuntime) string {
	handler := func(event *eventbus.AgentEvent) {
		handleWorkerEscalation(session, event)
	}

	runtime := session.Colony
	if len(colonyRuntime) > 0 && colonyRuntime[0] != nil {
		runtime = colonyRuntime[0]
	}
	if runtime != nil {
		subID, err := runtime.SubscribeToEvents(
			[]eventbus.EventType{eventbus.EventEscalationRequested},
			handler,
			eventbus.WithFilterColony(runtime.StreamID()),
		)
		if err == nil {
			return subID
		}
		log.Printf("Failed to install colony-scoped escalation sub: %v", err)
	}
	if session.EventBus == nil {
		return ""
	}
	return session.EventBus.Subscribe(
		[]eventbus.EventType{eventbus.EventEscalationRequested},
		handler,
	)
}

func handleWorkerEscalation(session *Session, event *eventbus.AgentEvent) {
	streamID := event.StreamID
	if !strings.HasPrefix(streamID, "worker:") {
		return
	}
	workerID := streamID[len("worker:"):]
	data := event.Data
	requestID := fieldString(data, "request_id")
	reason := strings.TrimSpace(fieldString(data, "reason"))
	contextText := strings.TrimSpace(fieldString(data, "context"))
	nodeLabel := "unknown_node"
	if event.NodeID != nil {
		nodeLabel = *event.NodeID
	}

	if session.PendingCount() >= MaxPendingEscalations {
		runtime := session.Colony
		if runtime != nil && workerID != "" {
			if err := runtime.InjectInput(workerID, queueFullReply); err != nil {
				log.Printf("Failed to send queue_full reply to worker %s: %v", workerID, err)
			}
		}
		return
	}

	if requestID != "" {
		var colonyID any
		if event.ColonyID != nil {
			colonyID = *event.ColonyID
		}
		session.RecordPendingEscalation(requestID, map[string]any{
			"request_id": requestID,
			"worker_id":  workerID,
			"colony_id":  colonyID,
			"node_id":    nodeLabel,
			"reason":     reason,
			"context":    contextText,
			"opened_at":  epochSeconds(time.Now()),
		})
	}

	lines := []string{"[WORKER_ESCALATION]"}
	if requestID != "" {
		lines = append(lines, "request_id: "+requestID)
	}
	workerLabel := workerID
	if workerLabel == "" {
		workerLabel = "unknown"
	}
	lines = append(lines, "worker_id: "+workerLabel)
	lines = append(lines, "node_id: "+nodeLabel)
	reasonLabel := reason
	if reasonLabel == "" {
		reasonLabel = "unspecified"
	}
	lines = append(lines, "reason: "+reasonLabel)
	if contextText != "" {
		lines = append(lines, "context:", contextText)
	}
	if requestID != "" {
		lines = append(lines, "Use reply_to_worker(request_id, reply) to unblock, or list_worker_questions() to see all pending.")
	} else {
		lines = append(lines, "No request_id — use inject_message(content=...) to relay guidance manually.")
	}
	handoff := strings.Join(lines, "\n")

	queenNode := session.QueenExecutor.QueenNode()
	if queenNode == nil {
		if session.EventBus != nil {
			executionID := session.ID
			session.EventBus.EmitClientOutputDelta("queen", "queen", handoff, handoff, &executionID, nil, 0)
			session.EventBus.EmitClientInputRequested("queen", "queen", &executionID, nil, nil)
		}
		return
	}

	if err := queenNode.InjectEvent(handoff, false); err != nil {
		log.Printf("Failed to inject worker escalation into queen: %v", err)
	}
}

func fieldString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	switch value := data[key].(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

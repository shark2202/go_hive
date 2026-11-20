package server

// Worker escalation inbox tools: list_worker_questions / reply_to_worker.
//
// Workers escalate through the framework-level `escalate` path, which emits
// ESCALATION_REQUESTED events stamped with a request_id. The session's
// colony-scoped subscription (InstallWorkerEscalationRouting) records each
// pending escalation on Session.PendingEscalations, keyed by request_id, so
// several concurrent waiters stay addressable. These two tools read and drain
// that inbox.
//
// Parity: framework/tools/queen_lifecycle_tools.py::list_worker_questions
// Parity: framework/tools/queen_lifecycle_tools.py::reply_to_worker

import (
	"sort"
)

// pendingQuestionEntries returns the inbox rows in arrival order. Python dicts
// preserve insertion order; Go maps do not, so RecordPendingEscalation keeps an
// explicit order and any entry inserted directly into the map (tests, older
// call sites) is appended in sorted key order.
func (s *Session) pendingQuestionEntries() []map[string]any {
	if s == nil {
		return nil
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if len(s.PendingEscalations) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]map[string]any, 0, len(s.PendingEscalations))
	for _, requestID := range s.pendingOrder {
		entry, ok := s.PendingEscalations[requestID]
		if !ok {
			continue
		}
		seen[requestID] = struct{}{}
		out = append(out, entry)
	}
	remaining := make([]string, 0, len(s.PendingEscalations))
	for requestID := range s.PendingEscalations {
		if _, ok := seen[requestID]; !ok {
			remaining = append(remaining, requestID)
		}
	}
	sort.Strings(remaining)
	for _, requestID := range remaining {
		out = append(out, s.PendingEscalations[requestID])
	}
	return out
}

// PendingEscalation returns one inbox entry by request_id.
func (s *Session) PendingEscalation(requestID string) (map[string]any, bool) {
	if s == nil {
		return nil, false
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	entry, ok := s.PendingEscalations[requestID]
	return entry, ok
}

// TakePendingEscalation removes and returns one inbox entry. The reply path
// drops the entry regardless of delivery (a failed delivery usually means the
// worker already terminated, so the entry must not linger).
func (s *Session) TakePendingEscalation(requestID string) (map[string]any, bool) {
	if s == nil {
		return nil, false
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	entry, ok := s.PendingEscalations[requestID]
	if !ok {
		return nil, false
	}
	delete(s.PendingEscalations, requestID)
	kept := s.pendingOrder[:0]
	for _, existing := range s.pendingOrder {
		if existing != requestID {
			kept = append(kept, existing)
		}
	}
	s.pendingOrder = kept
	return entry, true
}

// listWorkerQuestions renders the inbox the queen reads before replying.
//
// Parity: framework/tools/queen_lifecycle_tools.py::list_worker_questions
func (b *QueenLifecycleBinding) listWorkerQuestions() string {
	entries := b.session.pendingQuestionEntries()
	now := b.session.NowWall()
	pending := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		waiting := 0.0
		if opened, ok := floatValue(entry["opened_at"]); ok && opened > 0 {
			waiting = now - opened
			if waiting < 0 {
				waiting = 0
			}
		}
		pending = append(pending, map[string]any{
			"request_id":      entry["request_id"],
			"worker_id":       entry["worker_id"],
			"colony_id":       entry["colony_id"],
			"node_id":         entry["node_id"],
			"reason":          entry["reason"],
			"context_preview": charPrefix(stringValue(entry["context"]), 300),
			"waiting_seconds": roundTo1(waiting),
		})
	}
	return queenLifecycleJSON(map[string]any{"count": len(pending), "pending": pending})
}

// replyToWorker hands guidance back to the worker that asked, then drops the
// inbox entry.
//
// Parity: framework/tools/queen_lifecycle_tools.py::reply_to_worker
func (b *QueenLifecycleBinding) replyToWorker(input map[string]any) (string, bool) {
	if b.session.Colony == nil {
		return queenLifecycleJSON(map[string]any{"error": "No colony running in this session."}), true
	}
	if b.session.PendingEscalations == nil {
		return queenLifecycleJSON(map[string]any{"error": "Session has no escalation inbox."}), true
	}
	requestID := queenInputString(input, "request_id")
	reply := queenInputString(input, "reply")

	entry, ok := b.session.PendingEscalation(requestID)
	if !ok {
		return queenLifecycleJSON(map[string]any{
			"error":      "Unknown request_id. Call list_worker_questions() to see currently pending escalations.",
			"request_id": requestID,
		}), true
	}
	workerID := stringValue(entry["worker_id"])
	if workerID == "" {
		return queenLifecycleJSON(map[string]any{
			"error":      "Escalation entry is missing worker_id.",
			"request_id": requestID,
		}), true
	}

	// Format the reply so the waiting worker's conversation shows it as a
	// queen handoff rather than a raw user message.
	replyText := "[QUEEN_REPLY] request_id=" + requestID + "\n" + reply
	delivered := b.session.Colony.InjectInput(workerID, replyText) == nil

	// Drop the entry regardless of delivery.
	b.session.TakePendingEscalation(requestID)

	status := "worker_not_active"
	if delivered {
		status = "delivered"
	}
	return queenLifecycleJSON(map[string]any{
		"status":     status,
		"worker_id":  workerID,
		"request_id": requestID,
	}), false
}

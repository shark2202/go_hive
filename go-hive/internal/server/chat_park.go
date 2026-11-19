package server

// Durable park state for the chat runtime.
//
// A parked queen is not just an in-memory flag: the pending_input marker on the
// conversation cursor is the single source of truth a restarted runtime consults
// to decide whether to re-block on an unanswered ask_user question. It is
// written whenever the runtime parks (and cleared as soon as a real user message
// answers it), so a restart resumes the wait instead of silently continuing the
// last assistant turn.

import (
	"log"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

const (
	parkPendingInputKey = "pending_input"
	parkReasonKey       = "park_reason"
	parkParkedAtKey     = "parked_at"
)

// persistParkState mirrors the runtime's park onto <conversation>/cursor.json.
// Existing cursor fields (next_seq and the loop's own counters) are preserved.
//
// Parity: core/framework/agent_loop/internals/cursor_persistence.py::write_cursor
func (r *ChatRuntime) persistParkState() {
	if r == nil || r.store == nil {
		return
	}
	r.parkMu.Lock()
	reason := r.parkReason
	r.parkMu.Unlock()
	r.mu.Lock()
	questions := cloneParkQuestions(r.pendingQuestions)
	r.mu.Unlock()

	if len(questions) > 0 {
		reason = agentloop.ParkReasonAskUser
	}
	if reason == "" {
		reason = agentloop.ParkReasonUnknown
	}

	cursor, err := r.store.ReadCursor()
	if err != nil {
		log.Printf("chat runtime %s: read cursor for park state: %v", r.sessionID, err)
		return
	}
	if cursor == nil {
		cursor = map[string]any{}
	}
	pending := map[string]any{
		"reason":              string(reason),
		"emit_client_request": true,
	}
	if len(questions) > 0 {
		entries := make([]any, 0, len(questions))
		for _, question := range questions {
			entries = append(entries, question)
		}
		pending["questions"] = entries
	} else {
		pending["questions"] = nil
	}
	cursor[parkPendingInputKey] = pending
	cursor[parkReasonKey] = string(reason)
	cursor[parkParkedAtKey] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.store.WriteCursor(cursor); err != nil {
		log.Printf("chat runtime %s: persist park state: %v", r.sessionID, err)
	}
}

// clearParkPendingInput removes the persisted wait immediately. It is called
// when a user message or trigger answers the parked question, before the turn
// can fail or skip the next cursor checkpoint.
//
// Parity: core/framework/agent_loop/internals/cursor_persistence.py::write_cursor
func (r *ChatRuntime) clearParkPendingInput() {
	if r == nil || r.store == nil {
		return
	}
	cursor, err := r.store.ReadCursor()
	if err != nil {
		log.Printf("chat runtime %s: read cursor to clear park state: %v", r.sessionID, err)
		return
	}
	if cursor == nil {
		return
	}
	if _, present := cursor[parkPendingInputKey]; !present {
		return
	}
	cursor[parkPendingInputKey] = nil
	cursor[parkParkedAtKey] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.store.WriteCursor(cursor); err != nil {
		log.Printf("chat runtime %s: clear park state: %v", r.sessionID, err)
	}
}

// restoreParkState rehydrates a persisted park. It returns false when the
// cursor carries no pending_input marker, in which case the caller keeps the
// ordinary turn_done default.
//
// Parity: core/framework/agent_loop/internals/cursor_persistence.py::restore
func (r *ChatRuntime) restoreParkState() bool {
	if r == nil || r.store == nil {
		return false
	}
	cursor, err := r.store.ReadCursor()
	if err != nil || cursor == nil {
		return false
	}
	raw, present := cursor[parkPendingInputKey]
	if !present || raw == nil {
		return false
	}
	pending, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	reason := parkReasonFromCursor(pending[parkReasonKey])
	if reason == "" {
		reason = parkReasonFromCursor(pending["reason"])
	}
	if reason == "" {
		return false
	}
	questions := parkQuestionsFromCursor(pending["questions"])
	r.mu.Lock()
	r.pendingQuestions = questions
	r.mu.Unlock()
	if len(questions) > 0 {
		reason = agentloop.ParkReasonAskUser
	}
	r.setParkState(reason, true)
	return true
}

func parkReasonFromCursor(raw any) agentloop.ParkReason {
	text, _ := raw.(string)
	if text == "" {
		return ""
	}
	for _, reason := range agentloop.AllParkReasons {
		if string(reason) == text {
			return reason
		}
	}
	return agentloop.ParkReasonUnknown
}

func parkQuestionsFromCursor(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return cloneParkQuestions(typed)
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			question, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, cloneParkQuestion(question))
		}
		return out
	}
	return nil
}

func cloneParkQuestions(questions []map[string]any) []map[string]any {
	if len(questions) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(questions))
	for _, question := range questions {
		out = append(out, cloneParkQuestion(question))
	}
	return out
}

func cloneParkQuestion(question map[string]any) map[string]any {
	copied := make(map[string]any, len(question))
	for key, value := range question {
		copied[key] = value
	}
	return copied
}

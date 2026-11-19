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
	"github.com/aden-hive/hive-go/internal/eventbus"
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
	credentialForm := cloneParkForm(r.pendingCredentialForm)
	r.mu.Unlock()

	if credentialForm != nil {
		reason = agentloop.ParkReasonCredentialForm
	} else if len(questions) > 0 {
		reason = agentloop.ParkReasonAskUser
	}
	if reason == "" {
		reason = agentloop.ParkReasonUnknown
	}

	r.parkStoreMu.Lock()
	defer r.parkStoreMu.Unlock()
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
	if credentialForm != nil {
		pending["credential_form"] = credentialForm
	} else {
		pending["credential_form"] = nil
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
	r.parkStoreMu.Lock()
	defer r.parkStoreMu.Unlock()
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
	credentialForm := parkFormFromCursor(pending["credential_form"])
	r.mu.Lock()
	r.pendingQuestions = questions
	r.pendingCredentialForm = credentialForm
	r.mu.Unlock()
	if credentialForm != nil {
		reason = agentloop.ParkReasonCredentialForm
	} else if len(questions) > 0 {
		reason = agentloop.ParkReasonAskUser
	}
	r.setParkState(reason, true)
	return true
}

// applyOverseerAskUser records the question the colony's persistent overseer
// parked on. The overseer publishes client_input_requested on the colony bus
// (stream "overseer"); the session bridge mirrors it onto the session bus and
// calls this first, so both halves of the pair read and write one durable
// pending_input record in the session cursor instead of each keeping its own.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func (r *ChatRuntime) applyOverseerAskUser(event *eventbus.AgentEvent) bool {
	if r == nil || event == nil || event.Type != eventbus.EventClientInputRequested {
		return false
	}
	if event.StreamID != overseerStreamID || event.Data == nil {
		return false
	}
	questions := parkQuestionsFromCursor(event.Data["questions"])
	if len(questions) == 0 {
		return false
	}
	r.mu.Lock()
	r.pendingQuestions = questions
	// A question asked in the conversation supersedes a stale credential
	// form: the user is answering the overseer, not filling in a form.
	r.pendingCredentialForm = nil
	r.mu.Unlock()
	r.setParkState(agentloop.ParkReasonAskUser, true)
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

// cloneParkForm deep-copies the credential form payload (its fields list is a
// slice of maps, so a shallow copy would share mutable state with the event).
func cloneParkForm(form map[string]any) map[string]any {
	if len(form) == 0 {
		return nil
	}
	copied := make(map[string]any, len(form))
	for key, value := range form {
		copied[key] = value
	}
	if fields := cloneParkFieldList(form["fields"]); fields != nil {
		copied["fields"] = fields
	}
	return copied
}

// cloneParkFieldList normalises the field specs carried by an in-memory form
// or a decoded cursor entry into []any of independent maps.
func cloneParkFieldList(raw any) []any {
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []map[string]any:
		items = make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return nil
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		entry := make(map[string]any, len(object))
		for key, value := range object {
			entry[key] = value
		}
		out = append(out, entry)
	}
	return out
}

// parkFormFromCursor reads the credential form a parked turn waits on.
func parkFormFromCursor(raw any) map[string]any {
	form, ok := raw.(map[string]any)
	if !ok || len(form) == 0 {
		return nil
	}
	return cloneParkForm(form)
}

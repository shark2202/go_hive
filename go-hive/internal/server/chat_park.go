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

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
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
	// Snapshot the in-memory park under the runtime's lock, but write the
	// cursor through the store's atomic update: the conversation's message
	// persist also rewrites next_seq, and a plain read-then-write here could
	// carry away that writer's bump. Ordering between the park snapshot and
	// the write stays serialised by parkStoreMu.
	r.parkStoreMu.Lock()
	defer r.parkStoreMu.Unlock()
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
	parkedAt := time.Now().UTC().Format(time.RFC3339Nano)
	err := mutateConversationCursor(r.store, func(cursor map[string]any) {
		cursor[parkPendingInputKey] = pending
		cursor[parkReasonKey] = string(reason)
		cursor[parkParkedAtKey] = parkedAt
	})
	if err != nil {
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
	parkedAt := time.Now().UTC().Format(time.RFC3339Nano)
	// The presence check happens inside the atomic update: checking it on a
	// stale read could clear a park another writer just recorded.
	err := mutateConversationCursor(r.store, func(cursor map[string]any) {
		if _, present := cursor[parkPendingInputKey]; !present {
			return
		}
		cursor[parkPendingInputKey] = nil
		cursor[parkParkedAtKey] = parkedAt
	})
	if err != nil {
		log.Printf("chat runtime %s: clear park state: %v", r.sessionID, err)
	}
}

// mutateConversationCursor applies fn to the conversation cursor as one atomic
// read-modify-write. The park checkpoint and the conversation's message persist
// race on the same cursor; with separate read and write calls the later writer
// restores the earlier snapshot and drops the other's field.
func mutateConversationCursor(store agentloop.ConversationStore, fn func(cursor map[string]any)) error {
	if mutator, ok := store.(agentloop.CursorMutator); ok {
		return mutator.MutateCursor(fn)
	}
	// A store without the atomic seam (test stubs): keep the old pair, still
	// serialised between park writers by parkStoreMu.
	cursor, err := store.ReadCursor()
	if err != nil {
		return err
	}
	if cursor == nil {
		cursor = map[string]any{}
	}
	fn(cursor)
	return store.WriteCursor(cursor)
}

// restoreParkForNewRuntime picks the park a freshly constructed runtime starts
// with, mirroring the upstream cold-resume precedence:
//
//  1. a persisted pending_input wait (ask_user / credential_form / turn_done /
//     a broken park) is re-entered as-is;
//  2. a restart over a conversation that already ran whose cursor carries no
//     persisted wait is the cold-resume mid-turn shape — upstream §6b1½ parks
//     it on cold_interrupted until the user's next message instead of
//     silently resuming the in-flight work;
//  3. a brand-new session keeps the ordinary turn_done idle park.
//
// initialInputPending marks a create that delivers a prompt right after
// construction: upstream's guard only fires when the first iteration drained
// no user injection, so a pending prompt suppresses the cold_interrupted park.
// It returns true when the park came from disk (case 1 or 2), which is the
// caller's signal to re-announce the wait on the new bus.
//
// Parity: core/framework/agent_loop/agent_loop.py::execute (§6b1½ cold-resume mid-turn guard)
func (r *ChatRuntime) restoreParkForNewRuntime(initialInputPending bool) bool {
	if r == nil {
		return false
	}
	if r.restoreParkState() {
		return true
	}
	if r.coldResumeMidTurnShape(initialInputPending) {
		r.setParkState(agentloop.ParkReasonColdInterrupted, true)
		return true
	}
	r.setParkState(agentloop.ParkReasonTurnDone, true)
	return false
}

// coldResumeMidTurnShape reports the crash shape upstream's §6b1½ guard keys
// on: a conversation that already ran (parts on disk — upstream's restore
// requires message_count > 0) whose cursor carries NO pending_input wait, with
// no fresh prompt about to be injected. Workers are out of this path — the
// chat runtime is the queen loop (supports_direct_user_io).
//
// Parity: core/framework/agent_loop/agent_loop.py::execute (§6b1½ condition)
// Parity: core/framework/agent_loop/internals/cursor_persistence.py::restore
func (r *ChatRuntime) coldResumeMidTurnShape(initialInputPending bool) bool {
	if r == nil || r.store == nil || initialInputPending {
		return false
	}
	cursor, err := r.store.ReadCursor()
	if err != nil || cursor == nil {
		return false
	}
	if raw, present := cursor[parkPendingInputKey]; present && raw != nil {
		// A persisted wait exists (possibly one restoreParkState could not
		// classify); the mid-turn shape is specifically "no wait on disk".
		return false
	}
	parts, err := r.store.ReadParts()
	if err != nil || len(parts) == 0 {
		return false
	}
	return true
}

// announceRestoredPark re-announces a park restored from the cursor, the way
// upstream's restored `_await_user_input` re-enters its wait: the wait's
// client request is published again (client_input_requested — or the
// credential-form variant in its place) and the loop-state cell is set. A
// restarted runtime has an empty bus, so without this the session snapshot
// and the SSE first frame would report an idle session even though the
// cursor is parked (upstream re-emits here with the cursor's
// emit_client_request flag).
//
// Parity: core/framework/agent_loop/agent_loop.py::_await_user_input (restored-wait path)
// Parity: core/framework/agent_loop/internals/cursor_persistence.py::restore
func (r *ChatRuntime) announceRestoredPark() {
	if r == nil || r.bus == nil {
		return
	}
	r.parkMu.Lock()
	reason := r.parkReason
	r.parkMu.Unlock()
	if reason == "" {
		return
	}
	r.mu.Lock()
	questions := cloneParkQuestions(r.pendingQuestions)
	form := cloneParkForm(r.pendingCredentialForm)
	r.mu.Unlock()
	reasonText := string(reason)
	switch {
	case form != nil:
		r.bus.EmitCredentialFormRequested(chatStreamID, chatNodeID, nil, form)
	case parkAnnouncesClientRequest(reasonText):
		r.bus.EmitClientInputRequested(chatStreamID, chatNodeID, nil, questions, &reasonText)
	}
	// The park's authoritative activity, exactly as the live wait announces
	// it: a deliberate end-of-turn park is awaiting_user, a broken one is
	// interrupted (reason.Activity() is the single classifier).
	r.bus.EmitLoopStateChanged(chatStreamID, chatNodeID, string(reason.Activity()), nil, &reasonText, nil, questions)
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

// executeAskUser records the question the turn is parking on. The next user
// message is the answer: runTurn clears the pending state when it starts, and
// the turn that produced the question ends parked on ask_user, where the park
// boundary publishes client_input_requested with these same questions
// (upstream's _await_user_input announces the wait, not the tool handler).
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
func (r *ChatRuntime) executeAskUser(input map[string]any) agentloop.ToolResult {
	questions, problem := normalizeAskUserQuestions(input)
	if problem != "" {
		return agentloop.ToolResult{Content: problem, IsError: true}
	}
	r.mu.Lock()
	r.pendingQuestions = questions
	r.mu.Unlock()
	// Persist immediately: if the process dies before the turn's end checkpoint,
	// the question is still the wait a restarted session must re-enter.
	r.persistParkState()
	return agentloop.ToolResult{Content: "Waiting for user input..."}
}

// normalizeAskUserQuestions is the chat runtime's seam over the shared
// normaliser: the chat queen and the colony's persistent overseer must park on
// the exact same question shape, so the shaping lives in agentloop and both
// callers use it.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/agent_loop/internals/synthetic_tools.py::sanitize_ask_user_inputs
func normalizeAskUserQuestions(input map[string]any) ([]map[string]any, string) {
	return agentloop.NormalizeAskUserQuestions(input)
}

func (r *ChatRuntime) hasPendingQuestions() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pendingQuestions) > 0
}

// pendingParkReason classifies the wait the finished turn parked on: a
// credential form (credential_form) outranks an ask_user question, and an
// empty string means an ordinary turn_done. Both are external waits, so the
// idle nudge must not re-engage them.
//
// Parity: framework/agent_loop/reminders.py::ParkReason
func (r *ChatRuntime) pendingParkReason() agentloop.ParkReason {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingCredentialForm != nil {
		return agentloop.ParkReasonCredentialForm
	}
	if len(r.pendingQuestions) > 0 {
		return agentloop.ParkReasonAskUser
	}
	return ""
}

func (r *ChatRuntime) clearPendingQuestions() {
	r.mu.Lock()
	r.pendingQuestions = nil
	r.mu.Unlock()
	r.clearParkPendingInput()
}

// clearPendingCredentialForm drops a parked credential form once a real user
// message or trigger answers it (the same unblocking the ask_user park gets).
func (r *ChatRuntime) clearPendingCredentialForm() {
	r.mu.Lock()
	had := r.pendingCredentialForm != nil
	r.pendingCredentialForm = nil
	r.mu.Unlock()
	if had {
		r.clearParkPendingInput()
	}
}

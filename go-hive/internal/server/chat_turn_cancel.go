package server

// Stop lifecycle for the chat runtime.
//
// The cancel-queen route stops the queen without killing the session: it flags
// the explicit user-stop first, then cancels only the turn that is in flight.
// The idle-nudge gate reads the flag through reminderSignals from the very
// first tick after the cancelled turn parks the loop, and the next real client
// input lifts it when the next turn starts. Close remains the only owner of the
// runtime's lifetime context.
//
// Parity: framework/server/routes_execution.py::handle_cancel_queen
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.cancel_current_turn
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.mark_user_stopped

import (
	"context"
	"log"
)

// The production queen node is the chat runtime: both route assertions must
// hold for the real session assembly, not only for test doubles.
var (
	_ queenTurnCanceller = (*ChatRuntime)(nil)
	_ queenUserStopper   = (*ChatRuntime)(nil)
)

// MarkUserStopped records that the user explicitly stopped the queen. The route
// calls it before CancelCurrentTurn so the idle-nudge gate already sees the
// stop when the cancelled stream parks the loop; only a real client input
// clears it again. The flag and the loop's cumulative counters are persisted
// first: the runtime may be killed before the cancelled turn's own turn-end
// checkpoint runs, and a restart must still see the stop and not refill the
// tool-call budget.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.mark_user_stopped
func (r *ChatRuntime) MarkUserStopped() {
	if r == nil {
		return
	}
	r.userStopped.Store(true)
	r.persistLoopCursor()
	r.persistUserStopped(true)
}

// clearUserStopped lifts an explicit Stop for a real client input and persists
// the cleared flag, so a restart after this message cannot resurrect the stop.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.inject_event
func (r *ChatRuntime) clearUserStopped() {
	if r == nil {
		return
	}
	r.userStopped.Store(false)
	r.persistUserStopped(false)
}

// restoreUserStopped rehydrates the explicit-stop flag at runtime
// construction. The idle-nudge gate reads it from the very first tick, so a
// restart over a stopped session must not start nudging before the user
// speaks again.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
func (r *ChatRuntime) restoreUserStopped() {
	if r == nil || r.store == nil {
		return
	}
	cursor, err := r.store.ReadCursor()
	if err != nil || cursor == nil {
		return
	}
	stopped, _ := cursor[userStoppedCursorKey].(bool)
	r.userStopped.Store(stopped)
}

// persistUserStopped writes the stop flag without disturbing the cursor fields
// another writer owns (next_seq, recent_responses, pending_input, park state).
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
func (r *ChatRuntime) persistUserStopped(stopped bool) {
	if r == nil || r.store == nil {
		return
	}
	err := mutateConversationCursor(r.store, func(cursor map[string]any) {
		cursor[userStoppedCursorKey] = stopped
	})
	if err != nil {
		log.Printf("chat runtime %s: persist user_stopped=%v: %v", r.sessionID, stopped, err)
	}
}

// persistLoopCursor checkpoints the loop-owned cold-resume state (the
// cumulative tool-call count and the doom fingerprint window). It is called
// before a Stop cancels the turn so the interrupted turn's counters survive a
// process restart even when the turn never reaches its own turn-end write.
func (r *ChatRuntime) persistLoopCursor() {
	if r == nil || r.loop == nil || r.conv == nil {
		return
	}
	r.loop.PersistCursorState(r.conv)
}

// CancelCurrentTurn cancels the in-flight turn only: the turn's derived context
// aborts the in-flight LLM stream and the loop's next iteration check, while
// the runtime context and every queued turn stay untouched. The returned
// channel closes after the turn has unwound and its emitter/park cleanup has
// run, so the route can confirm the stop before answering. A runtime with no
// turn in flight returns nil (the route treats that as "nothing to await").
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.cancel_current_turn
func (r *ChatRuntime) CancelCurrentTurn() []<-chan struct{} {
	if r == nil {
		return nil
	}
	r.turnMu.Lock()
	defer r.turnMu.Unlock()
	if r.turnCancel == nil || r.turnDone == nil {
		return nil
	}
	// Cancelling under the same lock the turn installs/clears its handle
	// under: a turn that already finished cannot be hit, and a handle read
	// here belongs to the turn the route is stopping.
	r.turnCancel()
	return []<-chan struct{}{r.turnDone}
}

// beginTurn installs the cancellation handle for one runTurn and returns the
// context the loop must run under. Each turn derives its own context from the
// runtime's, so cancelling a finished turn's context can never reach a later
// turn.
func (r *ChatRuntime) beginTurn() (context.Context, uint64) {
	ctx, cancel := context.WithCancel(r.ctx)
	r.turnMu.Lock()
	r.turnSeq++
	seq := r.turnSeq
	r.turnCtx = ctx
	r.turnCancel = cancel
	r.turnDone = make(chan struct{})
	r.turnMu.Unlock()
	return ctx, seq
}

// endTurn releases a turn's cancellation handle and closes its done channel
// after the turn's cleanup (deferred after the emitter and park cleanup in
// runTurn). A stale call, e.g. a turn that somehow outlived a newer one, leaves
// the newer handle alone.
func (r *ChatRuntime) endTurn(seq uint64) {
	r.turnMu.Lock()
	defer r.turnMu.Unlock()
	if r.turnSeq != seq {
		return
	}
	if r.turnCancel != nil {
		r.turnCancel()
		r.turnCancel = nil
	}
	r.turnCtx = nil
	if r.turnDone != nil {
		close(r.turnDone)
		r.turnDone = nil
	}
}

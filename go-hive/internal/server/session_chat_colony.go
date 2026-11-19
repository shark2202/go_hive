package server

// Session <-> chat-runtime colony binding.
//
// The chat runtime is built before a session's colony host is attached
// (createSessionWithRuntime builds Chat, then sets record.Colony; load_colony
// attaches the colony later still). The binding therefore happens on the
// session record: whenever the session layer hands out a session that has both
// halves, the chat runtime adopts the colony, gains run_worker and starts
// receiving [WORKER_REPORT] turns.

// BindChatColony links the session's ChatRuntime to its live colony runtime.
// A nil colony unbinds (colony unloaded); an unchanged binding is a no-op.
func (s *Session) BindChatColony() {
	if s == nil || s.Chat == nil {
		return
	}
	s.Chat.BindColony(s.Colony)
}

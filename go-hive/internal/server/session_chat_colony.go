package server

// Session <-> chat-runtime colony binding.
//
// The chat runtime is built before a session's colony host is attached
// (createSessionWithRuntime builds Chat, then sets record.Colony; load_colony
// attaches the colony later still). The binding therefore happens on the
// session record: whenever the session layer hands out a session that has both
// halves, the chat runtime adopts the colony, gains run_worker and starts
// receiving [WORKER_REPORT] turns, and the worker escalation inbox is installed.

// BindChatColony links the session's ChatRuntime to its live colony runtime and
// keeps the worker escalation routing in step with it. A nil colony unbinds
// (colony unloaded); an unchanged binding is a no-op.
func (s *Session) BindChatColony() {
	if s == nil {
		return
	}
	if s.Chat != nil {
		s.Chat.BindColony(s.Colony)
	}
	s.syncWorkerEscalationRouting()
}

// syncWorkerEscalationRouting installs (or drops) the colony-scoped
// ESCALATION_REQUESTED subscription that fills the session's worker-question
// inbox. Without it a worker's escalate call has no reader: the question never
// reaches list_worker_questions and the worker parks forever.
//
// The install is idempotent — every session lookup calls BindChatColony — and
// follows the live colony: a session that swaps or unloads its colony gets a
// subscription on the new runtime, not a dangling one on the old.
//
// Parity: framework/server/queen_orchestrator.py::install_worker_escalation_routing
func (s *Session) syncWorkerEscalationRouting() {
	if s == nil {
		return
	}
	if s.Colony == nil {
		// Nothing to watch. Only a subscription this session installed for a
		// colony that has since gone is torn down here; an inbox installed
		// directly (tests, an older code path) stays until StopSession.
		if s.WorkerHandoffRuntime != nil {
			s.dropWorkerEscalationRouting()
		}
		return
	}
	if s.WorkerHandoffSub != "" && s.WorkerHandoffRuntime == s.Colony {
		return
	}
	s.dropWorkerEscalationRouting()
	s.WorkerHandoffSub = InstallWorkerEscalationRouting(s)
	s.WorkerHandoffRuntime = s.Colony
}

// DetachColony drops the session's colony binding: the chat runtime loses
// run_worker and the worker-report turns, and the escalation inbox is torn
// down with it. The caller stops the runtime first — this only rewires the
// session record so it no longer points at a colony that is gone.
func (s *Session) DetachColony() {
	if s == nil {
		return
	}
	s.Colony = nil
	s.ColonyID = ""
	s.WorkerPath = ""
	s.BindChatColony()
}

// dropWorkerEscalationRouting unsubscribes the inbox subscription from whichever
// bus carries it: the colony prefers a colony-scoped subscription on the
// runtime's own bus, so the session bus is only the fallback.
func (s *Session) dropWorkerEscalationRouting() {
	if s == nil || s.WorkerHandoffSub == "" {
		return
	}
	subID := s.WorkerHandoffSub
	s.WorkerHandoffSub = ""
	runtime := s.WorkerHandoffRuntime
	s.WorkerHandoffRuntime = nil
	if unsubscriber, ok := runtime.(colonyEventUnsubscriber); ok && unsubscriber.Unsubscribe(subID) {
		return
	}
	if s.EventBus != nil {
		s.EventBus.Unsubscribe(subID)
	}
}

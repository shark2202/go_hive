package server

// Parity: framework/loader/cli.py::_bridge_keepalive_loop
//
// The bridge pushes `{type:"notify", profile, text, tabId, action}` frames when
// the user hands a tab over (or detaches it) in the Chrome side panel. Upstream
// _on_notify routes them into the matching agent's inject_event with
// is_client_input=True: a queen session is matched by session.id, a worker by
// worker.id under each session's colony. The Go port keeps that lookup order
// and never invents a recipient — an unmatched frame is only logged.

import "log"

// DeliverBrowserNotify routes one bridge notify frame to the live agent whose
// id matches profile. It reports whether a live agent accepted the text.
//
// Queen match: session.ID == profile, injected through the session's queen
// node (ChatRuntime on chat sessions, the agent-loop node on preloaded ones).
// Worker match: colony.inject_input(profile, text), which errors on no match so
// the loop can keep probing other sessions. A matched queen whose node rejects
// the turn keeps the upstream behavior: log the failure and stop (the frame was
// addressed to that session).
func (m *SessionManager) DeliverBrowserNotify(profile, text string, extra map[string]any) bool {
	if m == nil {
		return false
	}
	for _, session := range m.ListSessions() {
		if session == nil {
			continue
		}
		if session.ID == profile {
			node := session.QueenExecutor.QueenNode()
			if node == nil {
				// Upstream: a matching id with no executor is not a
				// deliverable queen — keep scanning in case another
				// session owns the profile.
				continue
			}
			if err := node.InjectEvent(text, true); err != nil {
				log.Printf("notify: queen inject failed for session=%s: %v", session.ID, err)
			}
			return true
		}
		if session.Colony == nil {
			continue
		}
		if err := session.Colony.InjectInput(profile, text); err != nil {
			continue
		}
		return true
	}
	log.Printf("notify: no live agent matched profile=%s (extra=%v)", profile, extra)
	return false
}

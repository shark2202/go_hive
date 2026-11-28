package server

// Parity: framework/server/routes_sessions.py::handle_session_stats
// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_stats
// Parity: framework/host/event_bus.py::EventBus.get_stats

import "github.com/shark2202/go_hive/go-hive/internal/colony"

// sessionStatsEventBus reports the bus counters, keeping the schema intact
// (an object with an object-valued events_by_type) when the session has no bus
// wired yet — upstream calls get_stats() on a live bus and never emits null.
func sessionStatsEventBus(session *Session) map[string]any {
	if session != nil && session.EventBus != nil {
		return session.EventBus.GetStats()
	}
	return map[string]any{
		"total_events":   0,
		"subscriptions":  0,
		"events_by_type": map[string]int{},
	}
}

// sessionStatsColonyID mirrors upstream's colony_id, which is the colony stream
// id: a spawned colony reports its own id, and a queen-only session reports its
// session id (the id the Python server's per-session colony is stamped with).
func sessionStatsColonyID(session *Session) string {
	if session.ColonyID != "" {
		return session.ColonyID
	}
	return session.ID
}

// sessionStatsBaseline builds the session-side schema that overlays a colony's
// get_stats payload. It is only used when a colony is attached: upstream
// answers a colony-less session with {} (framework/server/routes_sessions.py::
// handle_session_stats), so the baseline is never rendered on its own.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_stats
func sessionStatsBaseline(session *Session) map[string]any {
	stats := map[string]any{
		"running":        false,
		"colony_id":      "",
		"active_workers": 0,
		"total_workers":  0,
		"triggers":       0,
		"event_bus":      sessionStatsEventBus(session),
		"adaptive_tool_budget": map[string]any{
			// Same env-backed default the colony runtime seeds itself with, so
			// the two sides cannot drift; nominal stays null until the adaptive
			// pass has a sample to apply, matching _budget_applied = None.
			"enabled": colony.DefaultAdaptiveToolBudget,
			"nominal": nil,
			"samples": 0,
		},
	}
	if session == nil {
		return stats
	}
	stats["running"] = session.Chat != nil
	stats["colony_id"] = sessionStatsColonyID(session)
	stats["triggers"] = len(session.AvailableTriggers())
	return stats
}

// buildSessionStats layers colony stats over the session baseline. Colony
// values win field-by-field (a partial provider payload still reports its own
// counters), extra colony keys pass through, and the nested event_bus /
// adaptive_tool_budget objects are merged instead of replaced so neither side
// can drop a key the other one owns.
func buildSessionStats(session *Session, colonyStats map[string]any) map[string]any {
	stats := sessionStatsBaseline(session)
	for key, value := range colonyStats {
		if nested, ok := stats[key].(map[string]any); ok {
			if overlay, ok := value.(map[string]any); ok {
				for nestedKey, nestedValue := range overlay {
					nested[nestedKey] = nestedValue
				}
				continue
			}
		}
		stats[key] = value
	}
	return stats
}

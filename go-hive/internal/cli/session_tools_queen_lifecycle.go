package cli

// Queen lifecycle wiring for queen chat sessions. The ten session-bound
// lifecycle tools (get_worker_status, inject_message, stop_worker,
// list/reply_to_worker, set/list/remove_trigger, update_worker_profile,
// list_browser_profiles) are registered on the queen's own session catalogue;
// a colony worker's catalogue never enables them, matching upstream's
// queen-only tool surface.
//
// Parity: framework/tools/queen_lifecycle_tools.py::register_queen_lifecycle_tools

import (
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

// enableQueenSessionTools turns on both queen-only surfaces for one chat
// session: queen mail (list_queens/message_queen) and the ten session-bound
// lifecycle tools. A colony worker's catalogue never comes through here.
func enableQueenSessionTools(tools *sessionToolCatalogue, manager *hiveserver.SessionManager, sessionID string, session *hiveserver.Session, home hivehome.Home) func() {
	onTurnStart := enableQueenMailTools(tools, manager, sessionID, session.QueenName)
	enableQueenLifecycleTools(tools, session, home)
	return onTurnStart
}

// enableQueenLifecycleTools puts the ten lifecycle tools plus the
// worker-monitoring tool on a queen session's catalogue. Called only for queen
// chat sessions (created, resumed, or the preloaded colony session) — never
// for a worker's declared-tool catalogue.
//
// These defs are appended after newSessionToolCatalogue already applied the
// same-name model-visible contract overlay, so the appended batch is passed
// through the same overlay here. The parity manifest freezes five of these
// names (remove_trigger / reply_to_worker / set_trigger / update_worker_profile
// / run_playbook) against the Python source definitions; without this call the
// manifest would govern every catalogue owner except the lifecycle table
// (docs/tool-contract-diff.md §13).
//
// get_worker_health_summary comes from worker_monitoring_tools.py upstream
// (registered only while a colony is bound); the catalogue carries it so a
// colony-bound turn advertises it, and server.colonyBoundQueenTool hides it
// while no colony runtime is attached.
func enableQueenLifecycleTools(tools *sessionToolCatalogue, session *hiveserver.Session, home hivehome.Home) {
	if tools == nil || session == nil || tools.queenLifecycle != nil {
		return
	}
	binding := hiveserver.NewQueenLifecycleBinding(session, home)
	healthSummary := hiveserver.QueenWorkerHealthSummaryToolDef()
	lifecycle := append(hiveserver.QueenLifecycleToolDefs(), healthSummary)
	defs := make([]llm.ToolDef, 0, len(lifecycle))
	for _, def := range lifecycle {
		if _, taken := tools.builtinNames[def.Name]; taken {
			continue
		}
		defs = append(defs, llm.ToolDef{
			Name:            def.Name,
			Description:     def.Description,
			Parameters:      def.Parameters,
			ConcurrencySafe: def.ConcurrencySafe,
		})
	}
	if len(defs) == 0 {
		return
	}
	tools.defs = append(tools.defs, applyToolContractParity(defs)...)
	tools.queenLifecycle = binding
}

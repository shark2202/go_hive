package cli

// Queen-mail wiring for `hive serve` and for every queen chat session. Kept
// out of loader.go so the serve entry point stays inside its frozen size
// budget; the behavior is one shared service per process.

import (
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// newQueenMailService builds the process's queen-mail service before any
// preloaded session exists and returns the create-session hook the adapter
// delivers letters through. The routes, the deliverer and the chat tools all
// see this one instance, so daily limits, tokens and return addresses agree.
func (r *Runner) newQueenMailService(
	manager *hiveserver.SessionManager,
	liveMu *sync.Mutex,
	live *[]*hiveserver.Session,
	home hivehome.Home,
	defaultModel string,
) (*hiveserver.QueenMailAdapter, func(hiveserver.CreateSessionParams) (*hiveserver.Session, error)) {
	createSession := func(params hiveserver.CreateSessionParams) (*hiveserver.Session, error) {
		return r.createChatSession(params, manager, liveMu, live, home, defaultModel)
	}
	adapter := hiveserver.NewQueenMailAdapter(hiveserver.QueenMailAdapterOptions{
		Home:          home,
		Manager:       manager,
		CreateSession: createSession,
	})
	manager.SetQueenMail(adapter)
	return adapter, createSession
}

// featureSessionsSnapshot projects the live-session slice onto the config
// route's feature view. The colony runtime (not just the id) must ride along:
// PUT /api/config/features hot-applies adaptive_tool_budget to it and counts
// only colonies whose flag actually changed, so an id-only snapshot reports
// colonies_applied: 0 forever.
//
// Parity: framework/server/routes_config.py::_apply_adaptive_budget_flag
func featureSessionsSnapshot(liveMu *sync.Mutex, live []*hiveserver.Session) []hiveserver.FeatureSession {
	if live == nil {
		return nil
	}
	if liveMu != nil {
		liveMu.Lock()
		defer liveMu.Unlock()
	}
	out := make([]hiveserver.FeatureSession, 0, len(live))
	for _, record := range live {
		feature := hiveserver.FeatureSession{ColonyID: record.ColonyID}
		if featureColony, ok := record.Colony.(hiveserver.FeatureColony); ok {
			feature.Colony = featureColony
		}
		out = append(out, feature)
	}
	return out
}

// enableQueenMailTools puts list_queens/message_queen on a queen session's
// catalogue when queen_mail is on, and returns the turn hook that starts a new
// chain of letters when someone other than a correspondent speaks. A colony
// worker's catalogue is never passed here, matching upstream's queen-only
// surface.
func enableQueenMailTools(tools *sessionToolCatalogue, manager *hiveserver.SessionManager, sessionID, queenName string) func() {
	adapter := manager.QueenMail()
	if adapter == nil {
		return nil
	}
	tools.enableQueenMail(adapter.Mail(), sessionID, queenName)
	return func() { adapter.Mail().ForgetCause(sessionID) }
}

package cli

// Queen-mail wiring for `hive serve` and for every queen chat session. Kept
// out of loader.go so the serve entry point stays inside its frozen size
// budget; the behavior is one shared service per process.

import (
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
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

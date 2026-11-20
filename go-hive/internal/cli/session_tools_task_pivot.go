package cli

// task_create(new_session=true) wiring.
//
// Upstream exposes the DM-phase pivot as an argument on task_create and routes
// it to framework/server/routes_execution.py::fork_queen_session_for_split:
// the queen forks a fresh session, the new context inherits only her handoff
// brief, and the plan she just wrote is seeded straight into it. The Go port
// keeps that shape: internal/tasks owns the argument and the routing,
// internal/server owns the fork, and this file connects them to a live session.
//
// Worker catalogues never call this, matching upstream's
// build_session_tools(pivot_handler=None): their task_create has no pivot field.

import (
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tasks"
)

// taskPivotWiring is the live-session capability the fork needs: the manager
// that tracks sessions, the hive home the successor lands under, and the
// session factory that starts it (the same one POST /api/sessions uses).
type taskPivotWiring struct {
	manager *hiveserver.SessionManager
	home    hivehome.Home
	create  func(hiveserver.CreateSessionParams) (*hiveserver.Session, error)
}

// enableQueenTools turns on the queen-only surfaces for one chat session and
// returns the turn hook they share. It lives here rather than in loader.go so
// the serve assembly file keeps its frozen size budget.
//
// A DM queen's task_create carries the new_session pivot; a colony-bound
// session does not (new scope there belongs to a new chat or a new colony, and
// upstream refuses the fork outright).
func (r *Runner) enableQueenTools(tools *sessionToolCatalogue, manager *hiveserver.SessionManager, sessionID string, record *hiveserver.Session, home hivehome.Home, liveMu *sync.Mutex, live *[]*hiveserver.Session, defaultModel string) func() {
	onTurnStart := enableQueenSessionTools(tools, manager, sessionID, record, home)
	if record != nil && record.ColonyID == "" {
		enableTaskPivot(tools, record, taskPivotWiring{
			manager: manager,
			home:    home,
			create: func(params hiveserver.CreateSessionParams) (*hiveserver.Session, error) {
				return r.createChatSession(params, manager, liveMu, live, home, defaultModel)
			},
		})
	}
	return onTurnStart
}

// enableTaskPivot installs the queen's new_session handler on the catalogue and
// swaps the model-visible task_create definition for the pivot-bearing one. It
// must run before the chat runtime is built from Defs().
func enableTaskPivot(tools *sessionToolCatalogue, session *hiveserver.Session, wiring taskPivotWiring) {
	if tools == nil || tools.taskTools == nil || session == nil || wiring.create == nil {
		return
	}
	handler := &tasks.PivotHandler{
		FieldName:          "new_session",
		FieldDescription:   tasks.NewSessionFieldDescription,
		HandoffDescription: tasks.HandoffDescription,
		Handle: func(goal string, handoff any, taskSpecs []any) map[string]any {
			text, _ := handoff.(string)
			if strings.TrimSpace(text) == "" {
				return map[string]any{
					"success": false,
					"error": "new_session=true requires a `handoff`, and you did not provide one. " +
						"The new session inherits NOTHING from this conversation — without a handoff it " +
						"starts blind. Call task_create again with a COMPLETE, objective handoff: the " +
						"user's goal in their terms, the concrete data (names, URLs, IDs, file paths, " +
						"the account to use, exact requirements), decisions made and options ruled " +
						"out, constraints, and what 'done' looks like.",
				}
			}
			specs := make([]map[string]any, 0, len(taskSpecs))
			for _, spec := range taskSpecs {
				if object, ok := spec.(map[string]any); ok {
					specs = append(specs, object)
				}
			}
			result, err := hiveserver.ForkQueenSessionWithHandoff(session, hiveserver.HandoffForkDeps{
				Manager:           wiring.manager,
				Home:              wiring.home,
				CreateSession:     wiring.create,
				GenerateSessionID: nil, // the server mints ids through its own store
			}, text, specs, goal)
			if err != nil {
				return map[string]any{"success": false, "error": err.Error()}
			}
			return hiveserver.HandoffPayload(result)
		},
	}
	tools.taskTools.setPivot(handler)
	if def, ok := tools.taskTools.createDef(); ok {
		for index := range tools.defs {
			if tools.defs[index].Name == tasks.TaskCreate {
				tools.defs[index] = def
			}
		}
	}
}

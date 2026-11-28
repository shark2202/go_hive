package cli

// Session create identity: which session id a live chat session runs under and
// which on-disk tree it continues. A create that carries queen_resume_from
// reopens the persisted session under its original id, so the dashboard's
// session-gone auto-resume (pages/queen-dm.tsx) can re-register the id /events
// already subscribes to instead of leaving it permanently 404.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	hivesession "github.com/shark2202/go_hive/go-hive/internal/session"
)

// chatSessionPlan is the resolved identity of a create-session request: the id
// the live session runs under, the queen whose tree it writes, and the
// persisted directory a resume reopens (empty when this is a new session).
type chatSessionPlan struct {
	SessionID  string
	QueenScope string
	ResumedDir string
	ResumeFrom string
}

// planChatSession validates the create-session routing inputs and picks the
// session id. A queen_resume_from that names an existing session under this
// session's own queen wins over session_id, as upstream:
// resolved_session_id = queen_resume_from or session_id. A resume whose target
// does not exist falls back to the ordinary create path rather than minting a
// ghost id with no transcript behind it.
//
// Parity: framework/server/session_manager.py::SessionManager.create_session
func planChatSession(params hiveserver.CreateSessionParams, home hivehome.Home) (chatSessionPlan, error) {
	requestedID := ""
	if params.SessionID != nil {
		requestedID = strings.TrimSpace(*params.SessionID)
		if !hivesession.ValidSegment(requestedID) {
			return chatSessionPlan{}, &hiveserver.SessionCreateValueError{Message: "invalid session_id"}
		}
	}

	queenName := ""
	if params.QueenName != nil {
		queenName = strings.TrimSpace(*params.QueenName)
	}
	// The transcript tree and the memory scope share one queen identifier: a
	// queen_growth session keeps its conversation under
	// queens/queen_growth/sessions and recalls queen_growth's memories. An
	// unbound session belongs to the shared "default" queen. queen_name comes
	// from request bodies and queen routes, so reject a name that would step
	// outside the queen tree rather than joining it raw.
	queenScope := chatSessionQueenScope(queenName)
	if !hivesession.ValidSegment(queenScope) {
		return chatSessionPlan{}, &hiveserver.SessionCreateValueError{Message: "invalid queen_name"}
	}

	resumeFrom := ""
	if params.QueenResumeFrom != nil {
		resumeFrom = strings.TrimSpace(*params.QueenResumeFrom)
		if resumeFrom != "" && !hivesession.ValidSegment(resumeFrom) {
			return chatSessionPlan{}, &hiveserver.SessionCreateValueError{Message: "invalid queen_resume_from"}
		}
	}
	// A resume reopens the persisted session under its original id. A
	// colony-bound resume resolves its tree from the colony layout upstream
	// (_queen_session_dir with colony_id): the forked-colony copy lives at
	// colonies/<c>/queens/<q>/sessions/<id>, and adopting a queen-DM copy
	// there would surface the wrong history. A queen-only resume stays inside
	// the queen's own tree.
	if resumeFrom != "" {
		if chatSessionColonyBound(params) {
			if dir := colonyResumableSessionDir(home, *params.ColonyID, queenScope, resumeFrom); dir != "" {
				return chatSessionPlan{
					SessionID:  resumeFrom,
					QueenScope: queenScope,
					ResumedDir: dir,
					ResumeFrom: resumeFrom,
				}, nil
			}
		} else if queenScope != "default" {
			if dir := resumableSessionDir(home, queenScope, resumeFrom); dir != "" {
				return chatSessionPlan{
					SessionID:  resumeFrom,
					QueenScope: queenScope,
					ResumedDir: dir,
					ResumeFrom: resumeFrom,
				}, nil
			}
		} else if dir, queen := persistedSessionQueen(home, resumeFrom); dir != "" {
			// A resume that carries no queen_name (the dashboard's
			// session-gone auto-resume) reopens the session under the queen
			// it was persisted with, so the id /events already subscribes to
			// comes back routable even after the create moved cold sessions
			// out of queens/default.
			return chatSessionPlan{
				SessionID:  resumeFrom,
				QueenScope: queen,
				ResumedDir: dir,
				ResumeFrom: resumeFrom,
			}, nil
		}
	}

	if requestedID != "" {
		return chatSessionPlan{SessionID: requestedID, QueenScope: queenScope}, nil
	}
	generated, err := hivesession.NewStore("").GenerateID(time.Now())
	if err != nil {
		return chatSessionPlan{}, err
	}
	return chatSessionPlan{SessionID: generated, QueenScope: queenScope}, nil
}

func chatSessionColonyBound(params hiveserver.CreateSessionParams) bool {
	return params.ColonyID != nil && strings.TrimSpace(*params.ColonyID) != ""
}

// resumableSessionDir returns the on-disk session directory queen_resume_from
// names, or "" when no such session exists under the requested queen. The
// lookup is queen-scoped — queens/<queen>/sessions/<id>, plus the legacy shared
// queens/default tree for sessions written before the queen-scoped layout — so
// a resume never adopts another queen's history.
//
// Parity: framework/server/session_manager.py::_find_queen_session_dir
func resumableSessionDir(home hivehome.Home, queenName, sessionID string) string {
	target := home.QueenSessionDir(queenName, sessionID)
	if isSessionDir(target) {
		return target
	}
	if queenName == "default" {
		return ""
	}
	legacy := home.QueenSessionDir("default", sessionID)
	if !isSessionDir(legacy) || !sessionDirQueenMatches(legacy, queenName) {
		return ""
	}
	return legacy
}

// colonyResumableSessionDir returns the colony-tree session dir
// queen_resume_from names inside colony_id, or "" when it is not there. A
// colony-bound session lives at colonies/<c>/queens/<q>/sessions/<id>
// upstream (_queen_session_dir with colony_id); the queen-DM trees are not a
// fallback here, because the same id can exist in a DM and must not be
// mistaken for the colony's copy.
//
// Parity: framework/server/session_manager.py::_queen_session_dir
func colonyResumableSessionDir(home hivehome.Home, colonyID, queenName, sessionID string) string {
	colonyID = strings.TrimSpace(colonyID)
	if colonyID == "" || !hivesession.ValidSegment(colonyID) {
		return ""
	}
	target := home.ColonyQueenSessionDir(colonyID, queenName, sessionID)
	if !isSessionDir(target) || !sessionDirQueenMatches(target, queenName) {
		return ""
	}
	return target
}

// persistedSessionQueen finds a persisted session across the queen DM trees and
// returns the tree holding it plus the queen it was written under. The queen is
// meta.json["queen_id"] when recorded, otherwise the queen directory holding
// the session (upstream's _resume_queen_name). A session with no explicit
// queen_name is resumed under that identity instead of the shared default tree.
//
// Parity: framework/server/session_manager.py::_resume_queen_name
func persistedSessionQueen(home hivehome.Home, sessionID string) (string, string) {
	entries, err := os.ReadDir(home.QueensDir())
	if err != nil {
		return "", ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := home.QueenSessionDir(entry.Name(), sessionID)
		if !isSessionDir(candidate) {
			continue
		}
		queen := entry.Name()
		if meta, ok := readJSONObject(filepath.Join(candidate, "meta.json")); ok {
			if recorded, isString := meta["queen_id"].(string); isString && strings.TrimSpace(recorded) != "" {
				queen = strings.TrimSpace(recorded)
			}
		}
		return candidate, queen
	}
	return "", ""
}

// sessionDirQueenMatches is the cross-queen guard for the legacy tree: when
// meta.json records the queen that wrote the session, a resume for a different
// queen must not adopt it. A session without a recorded queen_id predates the
// queen-scoped layout and stays readable by any queen, as chatSessionDir
// already treats the legacy tree.
func sessionDirQueenMatches(dir, queenName string) bool {
	meta, ok := readJSONObject(filepath.Join(dir, "meta.json"))
	if !ok {
		return true
	}
	recorded, _ := meta["queen_id"].(string)
	recorded = strings.TrimSpace(recorded)
	return recorded == "" || recorded == queenName
}

// resumeLiveSession returns the already-live session a resume targets, if any.
// A second create for an id the manager already holds must not build a second
// ChatRuntime writing the same transcript; the live one is the resume result.
// A same-id session under a different queen is a collision, not a resume, and
// is refused the way upstream raises "Session ... already exists".
//
// Parity: framework/server/session_manager.py::SessionManager._create_session_core
func resumeLiveSession(
	manager *hiveserver.SessionManager,
	plan chatSessionPlan,
	initialPrompt *string,
	initialPhase *string,
) (*hiveserver.Session, error) {
	if manager == nil || plan.ResumedDir == "" {
		return nil, nil
	}
	existing := manager.GetSession(plan.SessionID)
	if existing == nil {
		return nil, nil
	}
	if chatSessionQueenScope(existing.QueenName) != plan.QueenScope {
		return nil, &hiveserver.SessionCreateValueError{
			Message: fmt.Sprintf("Session '%s' already exists", plan.SessionID),
		}
	}
	// An explicit initial_phase on a resume retry moves the live session, the
	// same way the freshly built record would have been stamped below.
	if initialPhase != nil && strings.TrimSpace(*initialPhase) != "" {
		hiveserver.ApplyQueenPhase(existing, *initialPhase)
	}
	if initialPrompt != nil && strings.TrimSpace(*initialPrompt) != "" && existing.Chat != nil {
		if err := existing.Chat.Deliver(*initialPrompt); err != nil {
			return nil, err
		}
	}
	return existing, nil
}

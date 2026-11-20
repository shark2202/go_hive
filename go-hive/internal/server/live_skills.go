package server

// Live skills fan-out: the SessionManager side of skills.LiveSessionManager.
//
// Upstream's SessionManager exposes iter_colony_runtimes / iter_queen_sessions /
// list_sessions and routes_skills calls live_skill_runtimes(manager, queen_id)
// to reload every live catalog a skill change reaches. The Go SessionManager
// cannot satisfy skills.LiveSessionManager directly — its own ListSessions()
// returns []*Session — so LiveSkills() wraps it in the skills-facing adapter.
//
// Parity: framework/server/session_manager.py::SessionManager.iter_colony_runtimes
// Parity: framework/server/session_manager.py::SessionManager.iter_queen_sessions

import (
	"sync"

	"github.com/aden-hive/hive-go/internal/skills"
)

// liveSkillsManager adapts *SessionManager to the skills package's lookup
// contract. queenID nil means every live session (a shared-library change);
// non-nil means only that queen's sessions and colonies.
type liveSkillsManager struct {
	manager *SessionManager
}

// LiveSkills returns the reload-facing view of this manager's live sessions.
// Nil when the manager is nil, so a route without a session manager degrades
// to an empty fan-out instead of panicking.
func (m *SessionManager) LiveSkills() skills.LiveSessionManager {
	if m == nil {
		return nil
	}
	return liveSkillsManager{manager: m}
}

// ListSessions is every live session, in creation order.
func (a liveSkillsManager) ListSessions() []skills.LiveQueenSession {
	out := []skills.LiveQueenSession{}
	for _, session := range a.manager.ListSessions() {
		if session == nil || session.isClosed() {
			continue
		}
		out = append(out, liveQueenSession{session: session})
	}
	return out
}

// IterQueenSessions is the live sessions whose queen scope matches queenID.
// The queen association is Session.QueenName, the field the create/fork paths
// stamp from the request; a session without one (a preloaded colony session
// whose metadata names the queen but whose record does not) is only reached by
// the unfiltered fan-out.
func (a liveSkillsManager) IterQueenSessions(queenID *string) []skills.LiveQueenSession {
	out := []skills.LiveQueenSession{}
	if queenID == nil {
		return out
	}
	for _, session := range a.manager.ListSessions() {
		if session == nil || session.isClosed() || session.QueenName != *queenID {
			continue
		}
		out = append(out, liveQueenSession{session: session})
	}
	return out
}

// IterColonyRuntimes is the live colony runtimes a queen-scope (or global)
// reload reaches. A runtime without a ReloadSkills method is skipped: its
// catalog is read straight off disk by every consumer, so there is nothing
// cached to refresh and counting it would fake a reload that did not happen.
func (a liveSkillsManager) IterColonyRuntimes(queenID *string) []skills.SkillReloader {
	out := []skills.SkillReloader{}
	for _, session := range a.manager.ListSessions() {
		if session == nil || session.isClosed() || session.Colony == nil {
			continue
		}
		if queenID != nil && session.QueenName != *queenID {
			continue
		}
		reloader, ok := session.Colony.(interface {
			ReloadSkills() (map[string]any, error)
		})
		if !ok {
			continue
		}
		out = append(out, &sessionColonyReloader{session: session, reloader: reloader})
	}
	return out
}

// liveQueenSession is one live session as the skills package addresses it.
type liveQueenSession struct {
	session *Session
}

// PhaseSkillsManager is the session's reloadable catalog. Nil after the
// session closed, so a fan-out that snapshotted it earlier skips it.
func (s liveQueenSession) PhaseSkillsManager() skills.ReloadableSkillsManager {
	if s.session == nil || s.session.isClosed() {
		return nil
	}
	return s.session.PhaseSkillsManager()
}

// sessionLiveSkills is a session's catalog bound to the session, so a reload
// that raced StopSession degrades to a no-op instead of rebuilding a catalog
// nothing reads.
type sessionLiveSkills struct {
	session *Session
	inner   skills.ReloadableSkillsManager
}

func (m sessionLiveSkills) Reload() error {
	if m.session.isClosed() {
		return nil
	}
	return m.inner.Reload()
}

func (m sessionLiveSkills) SkillsCatalogPrompt() string {
	return m.inner.SkillsCatalogPrompt()
}

func (m sessionLiveSkills) MutationLock() *sync.Mutex {
	return m.inner.MutationLock()
}

// ReloadTarget names the session in reload reports.
func (m sessionLiveSkills) ReloadTarget() string {
	return "session:" + m.session.ID
}

// sessionColonyReloader reloads one live colony runtime for a session, unless
// the session closed first (StopSession stops the colony; reloading a stopped
// runtime would rebuild a catalog nobody sees).
type sessionColonyReloader struct {
	session  *Session
	reloader interface {
		ReloadSkills() (map[string]any, error)
	}
}

func (r *sessionColonyReloader) ReloadSkills() (map[string]any, error) {
	if r.session == nil || r.session.isClosed() || r.reloader == nil {
		return nil, nil
	}
	return r.reloader.ReloadSkills()
}

// ReloadTarget names the colony in reload reports.
func (r *sessionColonyReloader) ReloadTarget() string {
	return "colony:" + r.session.ColonyID
}

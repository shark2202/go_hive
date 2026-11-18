package skills

import (
	"log"
	"sync"
)

type SkillReloader interface {
	ReloadSkills() (map[string]any, error)
}

type ReloadableSkillsManager interface {
	Reload() error
	SkillsCatalogPrompt() string
	MutationLock() *sync.Mutex
}

type LiveQueenSession interface {
	PhaseSkillsManager() ReloadableSkillsManager
}

type LiveSessionManager interface {
	IterColonyRuntimes(queenID *string) []SkillReloader
	IterQueenSessions(queenID *string) []LiveQueenSession
	ListSessions() []LiveQueenSession
}

type ManagerReloadAdapter struct {
	mgr ReloadableSkillsManager
}

func NewManagerReloadAdapter(mgr ReloadableSkillsManager) *ManagerReloadAdapter {
	return &ManagerReloadAdapter{mgr: mgr}
}

func (a *ManagerReloadAdapter) SkillsManager() ReloadableSkillsManager { return a.mgr }

func (a *ManagerReloadAdapter) ReloadSkills() (map[string]any, error) {
	a.mgr.MutationLock().Lock()
	err := a.mgr.Reload()
	a.mgr.MutationLock().Unlock()
	if err != nil {
		return nil, err
	}
	return map[string]any{"catalog_chars": utf8Len(a.mgr.SkillsCatalogPrompt())}, nil
}

func LiveSkillRuntimes(sessionManager LiveSessionManager, queenID *string) (runtimes []SkillReloader) {
	runtimes = []SkillReloader{}
	if sessionManager == nil {
		return runtimes
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("skills: live manager lookup failed: %v", recovered)
		}
	}()

	runtimes = append(runtimes, sessionManager.IterColonyRuntimes(queenID)...)
	var sessions []LiveQueenSession
	if queenID != nil {
		sessions = sessionManager.IterQueenSessions(queenID)
	} else {
		sessions = sessionManager.ListSessions()
	}
	for _, session := range sessions {
		if mgr := session.PhaseSkillsManager(); mgr != nil {
			runtimes = append(runtimes, NewManagerReloadAdapter(mgr))
		}
	}
	return runtimes
}

func ReloadRuntimes(runtimes []SkillReloader) {
	var wg sync.WaitGroup
	for _, runtime := range runtimes {
		runtime := runtime
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Printf("reload_skills failed for runtime %v: %v", runtime, recovered)
				}
			}()
			if _, err := runtime.ReloadSkills(); err != nil {
				log.Printf("reload_skills failed for runtime %v: %v", runtime, err)
			}
		}()
	}
	wg.Wait()
}

func ReloadLiveSkills(sessionManager LiveSessionManager, queenID *string) {
	ReloadRuntimes(LiveSkillRuntimes(sessionManager, queenID))
}

package skills

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

type SkillReloader interface {
	ReloadSkills() (map[string]any, error)
}

// ReloadTargetNamer lets a runtime name itself in reload reports and logs. A
// runtime that does not implement it is reported by its Go type.
type ReloadTargetNamer interface {
	ReloadTarget() string
}

// ReloadTarget is one runtime's outcome in a fan-out: the stats it reported
// (catalog_chars, skill_dirs, ...) or the error it failed with.
type ReloadTarget struct {
	Target string         `json:"target"`
	Stats  map[string]any `json:"stats,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// ReloadReport is the outcome of one live-catalog fan-out. It exists so an HTTP
// caller can see how many live catalogs a reload actually reached instead of a
// bare ok; a per-runtime failure is recorded here and never fails the request.
type ReloadReport struct {
	Runtimes int
	Reloaded int
	Failed   int
	Errors   []string
	Targets  []ReloadTarget
}

// Dict renders the report for an HTTP response. Empty collections stay JSON
// arrays, not null, so a dashboard can iterate without null checks.
func (r ReloadReport) Dict() map[string]any {
	errorsOut := r.Errors
	if errorsOut == nil {
		errorsOut = []string{}
	}
	targets := r.Targets
	if targets == nil {
		targets = []ReloadTarget{}
	}
	return map[string]any{
		"runtimes": r.Runtimes,
		"reloaded": r.Reloaded,
		"failed":   r.Failed,
		"errors":   errorsOut,
		"targets":  targets,
	}
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

// ReloadTarget forwards the wrapped manager's identity so a session-scoped
// catalog shows up by name in reload reports instead of by Go type.
func (a *ManagerReloadAdapter) ReloadTarget() string {
	if namer, ok := a.mgr.(ReloadTargetNamer); ok {
		return namer.ReloadTarget()
	}
	return ""
}

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

// reloadTargetName is the identity a fan-out reports for one runtime.
func reloadTargetName(runtime SkillReloader) string {
	if namer, ok := runtime.(ReloadTargetNamer); ok {
		if name := strings.TrimSpace(namer.ReloadTarget()); name != "" {
			return name
		}
	}
	return fmt.Sprintf("%T", runtime)
}

// ReloadRuntimesReport reloads every runtime concurrently and returns the
// per-runtime outcome. One runtime that fails (or panics) is logged and does
// not stop the others, mirroring upstream's asyncio.gather over reload_one.
func ReloadRuntimesReport(runtimes []SkillReloader) ReloadReport {
	report := ReloadReport{Runtimes: len(runtimes)}
	results := make([]ReloadTarget, len(runtimes))
	targets := make([]string, len(runtimes))
	var wg sync.WaitGroup
	for index, runtime := range runtimes {
		index, runtime := index, runtime
		targets[index] = reloadTargetName(runtime)
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := ReloadTarget{Target: targets[index]}
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Printf("reload_skills failed for runtime %s: panic: %v", targets[index], recovered)
					entry.Stats = nil
					entry.Error = fmt.Sprintf("panic: %v", recovered)
				}
				results[index] = entry
			}()
			stats, err := runtime.ReloadSkills()
			if err != nil {
				log.Printf("reload_skills failed for runtime %s: %v", targets[index], err)
				entry.Error = err.Error()
				return
			}
			entry.Stats = stats
		}()
	}
	wg.Wait()

	report.Targets = results
	for _, entry := range results {
		if entry.Error != "" {
			report.Failed++
			report.Errors = append(report.Errors, entry.Target+": "+entry.Error)
			continue
		}
		report.Reloaded++
	}
	return report
}

// ReloadRuntimes reloads every runtime and drops the report; the parity caller
// (reload_runtimes) returns None, and a failure is only logged.
func ReloadRuntimes(runtimes []SkillReloader) {
	ReloadRuntimesReport(runtimes)
}

// ReloadLiveSkillsReport collects the live runtimes a skill change reaches and
// reloads them, returning the outcome for an HTTP caller to echo.
func ReloadLiveSkillsReport(sessionManager LiveSessionManager, queenID *string) ReloadReport {
	return ReloadRuntimesReport(LiveSkillRuntimes(sessionManager, queenID))
}

func ReloadLiveSkills(sessionManager LiveSessionManager, queenID *string) {
	ReloadLiveSkillsReport(sessionManager, queenID)
}

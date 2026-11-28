package server

// Queen-catalog skill watcher.
//
// Upstream's agent host starts its SkillsManager watcher when the queen host
// starts (framework/host/agent_host.py -> SkillsManager.start_watching). The
// Go port has no separate agent host: each live session owns its queen catalog
// (Session.LiveSkills), and the process-wide reload fan-out is
// skills.ReloadLiveSkills. This watcher closes the same gap for the queen
// catalog surface: it polls the scopes those catalogs read and rebuilds every
// live catalog after an edit, so a SKILL.md written outside the HTTP routes
// reaches the queen's next turn without /api/skills/reload.
//
// Parity: framework/host/agent_host.py (start -> _skills_manager.start_watching)
// Parity: framework/skills/manager.py::_watch_loop

import (
	"os"
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// Variables (not constants) so the server test can shorten the cadence.
var (
	queenSkillsWatchInterval = skills.DefaultWatchInterval
	queenSkillsWatchDebounce = skills.DefaultWatchDebounce
)

// startQueenSkillsWatcher starts the process's queen-catalog watcher. It is a
// no-op until the hive home has a queens tree: a fresh home is created by
// EnsureDirs before serve, and tests that never materialise one keep no
// background loop.
func (a *App) startQueenSkillsWatcher() {
	if a == nil || a.deps.Manager == nil || a.deps.Home.Root == "" {
		return
	}
	if info, err := os.Stat(a.deps.Home.QueensDir()); err != nil || !info.IsDir() {
		return
	}
	home := a.deps.Home
	manager := a.deps.Manager
	a.skillsWatcher = skills.StartWatcher(skills.WatcherOptions{
		Interval: queenSkillsWatchInterval,
		Debounce: queenSkillsWatchDebounce,
		Targets: func() skills.WatchTargets {
			return queenCatalogWatchTargets(home)
		},
		Reload: func() error {
			skills.ReloadLiveSkills(manager.LiveSkills(), nil)
			return nil
		},
	})
}

// StopSkillsWatcher ends the queen-catalog watcher. The serve process lives
// until exit, so production never calls it; tests and embedded servers use it
// to leave no polling loop behind.
func (a *App) StopSkillsWatcher() {
	if a == nil || a.skillsWatcher == nil {
		return
	}
	a.skillsWatcher.Stop()
	a.skillsWatcher = nil
}

// queenCatalogWatchTargets is the queen catalog's watched scope: the shared
// user roots (hive home skills, ~/.agents/skills, configured external skill
// dirs) plus the whole queens tree, recursively. Watching the tree rather than
// one queen's folder keeps every queen's skills/ and skills_overrides.json in
// one snapshot; a newly created queen is picked up by the next scan.
func queenCatalogWatchTargets(home hivehome.Home) skills.WatchTargets {
	if home.Root == "" {
		return skills.WatchTargets{}
	}
	dirs := []string{
		filepath.Join(home.Root, "skills"),
		home.QueensDir(),
	}
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		dirs = append(dirs, filepath.Join(userHome, ".agents", "skills"))
	}
	for _, raw := range configuredExternalSkillDirs(home) {
		expanded, err := skills.ExpandExternalPath(raw)
		if err != nil || expanded == "" {
			continue
		}
		dirs = append(dirs, expanded)
	}
	return skills.WatchTargets{
		Dirs:  dirs,
		Names: []string{"SKILL.md", "skills_overrides.json"},
	}
}

// configuredExternalSkillDirs reads configuration.json's external_skills list
// with the same shape skills.SkillDiscovery consumes.
func configuredExternalSkillDirs(home hivehome.Home) []string {
	raw, ok := HiveConfig(home)["external_skills"].([]any)
	if !ok {
		return nil
	}
	dirs := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && text != "" {
			dirs = append(dirs, text)
		}
	}
	return dirs
}

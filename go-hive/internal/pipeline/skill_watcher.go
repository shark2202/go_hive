package pipeline

import (
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// SkillWatcher watches a SkillsManager's discovered roots and reloads the
// manager when a SKILL.md or the queen overrides file changes — the Go
// counterpart of upstream's asyncio watchfiles task. The polling loop is
// shared with the colony runtime and queen-catalog watchers
// (skills.Watcher); this wrapper supplies the manager's roots and its
// mutation lock.
//
// Parity: core/framework/skills/manager.py::SkillsManager.start_watching
type SkillWatcher struct {
	inner *skills.Watcher
}

// StartSkillWatcher starts a watcher over the manager's discovered roots. It
// returns nil when there is nothing to watch, exactly as upstream's
// start_watching is a silent no-op without watchfiles or watched paths.
func StartSkillWatcher(manager *skills.SkillsManager, interval, debounce time.Duration) *SkillWatcher {
	if manager == nil || (len(manager.WatchedDirs()) == 0 && len(manager.WatchedFiles()) == 0) {
		return nil
	}
	inner := skills.StartWatcher(skills.WatcherOptions{
		Interval: interval,
		Debounce: debounce,
		Targets: func() skills.WatchTargets {
			return skills.WatchTargets{Dirs: manager.WatchedDirs(), Files: manager.WatchedFiles()}
		},
		Reload: func() error {
			lock := manager.MutationLock()
			lock.Lock()
			defer lock.Unlock()
			return manager.Reload()
		},
	})
	if inner == nil {
		return nil
	}
	return &SkillWatcher{inner: inner}
}

// Stop ends the watch loop and waits for it to exit. It is safe to call more
// than once and on a nil watcher.
func (w *SkillWatcher) Stop() {
	if w == nil || w.inner == nil {
		return
	}
	w.inner.Stop()
}

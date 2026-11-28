package skills

// Shared skill hot-reload watcher.
//
// Upstream starts the same SkillsManager watcher from three lifecycles: the
// pipeline's skill_registry stage, the colony runtime, and the agent host that
// owns a queen's catalog (framework/skills/manager.py::start_watching, called
// from pipeline/stages/skill_registry.py, host/colony_runtime.py and
// host/agent_host.py). The Go port has no watchfiles dependency, so every site
// polls the watched directories instead; this type is the one implementation
// behind all of them.

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultWatchInterval is the polling cadence when a caller passes no
	// interval.
	DefaultWatchInterval = time.Second
	// DefaultWatchDebounce matches upstream's watchfiles debounce: changes
	// must be quiet for this long before the catalog reloads.
	DefaultWatchDebounce = time.Second
)

// DefaultWatchedNames are the file names a directory scan fingerprints.
// SKILL.md is what every manager scope reads; a queen catalog also watches
// its skills_overrides.json.
var DefaultWatchedNames = []string{"SKILL.md"}

// FileStamp is one watched path's change fingerprint.
type FileStamp struct {
	ModTime time.Time
	Size    int64
}

// WatchSnapshot maps every watched file to its fingerprint at scan time.
type WatchSnapshot map[string]FileStamp

// WatchTargets is one watcher's current roots: directories scanned
// recursively for the named files, plus individual files (an override store
// that may not exist yet).
type WatchTargets struct {
	Dirs  []string
	Files []string
	// Names are the file names a directory scan keeps. Empty uses
	// DefaultWatchedNames.
	Names []string
}

// Snapshot fingerprints every watched file. A missing explicit file is kept
// with a zero stamp so its later creation counts as a change; a missing
// directory contributes nothing (and its later creation does, because the
// next scan finds its files).
func (t WatchTargets) Snapshot() WatchSnapshot {
	snapshot := WatchSnapshot{}
	names := t.Names
	if len(names) == 0 {
		names = DefaultWatchedNames
	}
	watched := make(map[string]struct{}, len(names))
	for _, name := range names {
		watched[name] = struct{}{}
	}
	for _, dir := range t.Dirs {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				// A disappearing directory is itself a change; the reload's
				// own discovery decides what remains.
				return nil
			}
			if _, ok := watched[entry.Name()]; !ok {
				return nil
			}
			if info, statErr := entry.Info(); statErr == nil {
				snapshot[path] = FileStamp{ModTime: info.ModTime(), Size: info.Size()}
			}
			return nil
		})
	}
	for _, path := range t.Files {
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			snapshot[path] = FileStamp{ModTime: info.ModTime(), Size: info.Size()}
		} else {
			snapshot[path] = FileStamp{}
		}
	}
	return snapshot
}

// Watcher polls a target set and reloads when it changes.
type Watcher struct {
	interval time.Duration
	debounce time.Duration
	targets  func() WatchTargets
	reload   func() error

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// WatcherOptions configures one watcher. Targets is re-evaluated after every
// reload (a reload can discover new roots); Reload runs while the watcher's
// debounce timer has expired, and a non-nil error is logged and swallowed,
// matching upstream keeping the previous prompts.
type WatcherOptions struct {
	Interval time.Duration
	Debounce time.Duration
	Targets  func() WatchTargets
	Reload   func() error
}

// StartWatcher starts polling. It returns nil when there is nothing to watch
// (no targets at all), exactly as upstream's start_watching is a silent no-op
// without watchfiles or watched paths.
func StartWatcher(opts WatcherOptions) *Watcher {
	if opts.Targets == nil {
		return nil
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultWatchInterval
	}
	debounce := opts.Debounce
	if debounce < 0 {
		debounce = 0
	}
	targets := opts.Targets()
	if len(targets.Dirs) == 0 && len(targets.Files) == 0 {
		return nil
	}
	watcher := &Watcher{
		interval: interval,
		debounce: debounce,
		targets:  opts.Targets,
		reload:   opts.Reload,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	// Take the baseline before returning so a write that races the goroutine's
	// first wake-up still counts as a change.
	baseline := targets.Snapshot()
	go watcher.run(baseline)
	return watcher
}

// Stop ends the watch loop and waits for it to exit. It is safe to call more
// than once and on a nil watcher.
func (w *Watcher) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

// run polls the targets, waits for a quiet debounce window after the last
// change, then reloads and rescans (a reload can discover new roots).
func (w *Watcher) run(snapshot WatchSnapshot) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	var pendingSince time.Time
	for {
		select {
		case <-w.stop:
			return
		case now := <-ticker.C:
			current := w.targets().Snapshot()
			if !snapshotsEqual(snapshot, current) {
				snapshot = current
				pendingSince = now
			}
			if pendingSince.IsZero() || now.Sub(pendingSince) < w.debounce {
				continue
			}
			pendingSince = time.Time{}
			if w.reload != nil {
				if err := w.reload(); err != nil {
					// Upstream logs and keeps the previous prompts.
					log.Printf("Skill reload failed; keeping previous prompts: %v", err)
				}
			}
			snapshot = w.targets().Snapshot()
		}
	}
}

func snapshotsEqual(left, right WatchSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for path, stamp := range left {
		if other, ok := right[path]; !ok || other != stamp {
			return false
		}
	}
	return true
}

package colony

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start ->
// SkillsManager.start_watching; a SKILL.md edit reloads the colony catalog
// every in-flight worker reads through its dynamic provider.
func TestColonyStartWatchesSkillsAndStopStopsTheWatcher(t *testing.T) {
	colonyDir := t.TempDir()
	writeColonySkill(t, colonyDir, "watcher-alpha", "Original description.")
	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath:         colonyDir,
		AgentSpec:           agentloop.AgentSpec{ID: "worker"},
		SkillsWatchInterval: 5 * time.Millisecond,
		SkillsWatchDebounce: 0,
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	if !strings.Contains(runtime.workerSkillsCatalogPrompt(), "watcher-alpha") {
		t.Fatalf("initial catalog missing the colony skill: %q", runtime.cachedSkillsCatalogPrompt())
	}

	writeColonySkill(t, colonyDir, "watcher-omega", "Renamed description.")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(runtime.cachedSkillsCatalogPrompt(), "watcher-omega") {
			runtime.Stop()
			runtime.skillsWatcherMu.Lock()
			watcher := runtime.skillsWatcher
			runtime.skillsWatcherMu.Unlock()
			if watcher != nil {
				t.Fatal("Stop must clear the colony skills watcher")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("catalog did not reload after the SKILL.md change: %q", runtime.cachedSkillsCatalogPrompt())
}

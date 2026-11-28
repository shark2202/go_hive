package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// Parity: framework/host/agent_host.py (start -> SkillsManager.start_watching).
// A SKILL.md edit under the queen scope reaches a live session's catalog
// without /api/skills/reload.
func TestQueenCatalogWatcherReloadsLiveSessionCatalog(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queenDir := home.QueenDir("queen_alpha")
	skillPath := filepath.Join(queenDir, "skills", "watcher-alpha", "SKILL.md")
	writeTestFile(t, skillPath, "---\nname: watcher-alpha\ndescription: Original description.\n---\n\nInstructions.\n")
	if err := os.MkdirAll(home.QueensDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := skills.NewSkillsManager(&skills.SkillsManagerConfig{
		QueenID:                "queen_alpha",
		QueenOverridesPath:     filepath.Join(queenDir, "skills_overrides.json"),
		ExtraScopeDirs:         []skills.ExtraScope{{Directory: filepath.Join(queenDir, "skills"), Label: "queen_ui", Priority: 2}},
		SkipCommunityDiscovery: true,
		Interactive:            false,
		HomeDir:                t.TempDir(),
		HiveHome:               home.Root,
	})
	manager.Load()

	prevInterval, prevDebounce := queenSkillsWatchInterval, queenSkillsWatchDebounce
	queenSkillsWatchInterval = 5 * time.Millisecond
	queenSkillsWatchDebounce = 0
	t.Cleanup(func() {
		queenSkillsWatchInterval = prevInterval
		queenSkillsWatchDebounce = prevDebounce
	})

	sessions := NewSessionManager()
	sessions.AddSession(&Session{ID: "session-alpha", LiveSkills: manager})
	app := NewApp(AppDeps{Home: home, Manager: sessions, FrontendDist: t.TempDir()})
	t.Cleanup(app.StopSkillsWatcher)
	if app.skillsWatcher == nil {
		t.Fatal("queen catalog watcher not started")
	}
	if !strings.Contains(manager.SkillsCatalogPrompt(), "watcher-alpha") {
		t.Fatalf("initial catalog = %q", manager.SkillsCatalogPrompt())
	}

	writeTestFile(t, skillPath, "---\nname: watcher-omega\ndescription: Renamed description.\n---\n\nInstructions.\n")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(manager.SkillsCatalogPrompt(), "watcher-omega") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queen catalog did not reload after the SKILL.md change: %q", manager.SkillsCatalogPrompt())
}

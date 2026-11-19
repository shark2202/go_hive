package cli

// Queen skills catalog injection for chat sessions.
//
// Upstream builds the queen's SkillsManager from her own skills folder (plus
// user scope) and the reminder source re-announces that catalog; the chat
// runtime does not bake it into the static prompt. The loader owns the disk
// layout, so the provider is built here and injected through ChatRuntimeDeps.
//
// Parity: framework/server/queen_orchestrator.py (queen SkillsManagerConfig:
// queen_id, queen_overrides_path, extra_scope_dirs label="queen_ui")

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/skills"
)

// queenSkillsCatalogProvider renders one session's queen catalog on first use
// and then serves that manager. The provider is per session (like upstream's
// queen SkillsManager) so a skill installed after an older session was created
// still reaches new sessions without a serve restart.
func queenSkillsCatalogProvider(home hivehome.Home, queenScope string) func() string {
	return queenSkillsCatalogProviderAt(home, queenScope, "")
}

func queenSkillsCatalogProviderAt(home hivehome.Home, queenScope, userHome string) func() string {
	root := strings.TrimSpace(home.Root)
	if root == "" {
		return func() string { return "" }
	}
	queen := strings.TrimSpace(queenScope)
	if queen == "" {
		queen = "default"
	}
	var once sync.Once
	var catalog string
	return func() string {
		once.Do(func() {
			queenDir := hivehome.Home{Root: root}.QueenDir(queen)
			manager := skills.NewSkillsManager(&skills.SkillsManagerConfig{
				QueenID:                queen,
				QueenOverridesPath:     filepath.Join(queenDir, "skills_overrides.json"),
				ExtraScopeDirs:         []skills.ExtraScope{{Directory: filepath.Join(queenDir, "skills"), Label: "queen_ui", Priority: 2}},
				SkipCommunityDiscovery: true,
				Interactive:            false,
				HomeDir:                userHome,
				HiveHome:               root,
			})
			manager.Load()
			catalog = manager.SkillsCatalogPrompt()
		})
		return catalog
	}
}

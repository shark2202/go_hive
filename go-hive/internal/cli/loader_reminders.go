package cli

// Queen skills catalog injection for chat sessions.
//
// Upstream builds the queen's SkillsManager from her own skills folder (plus
// user scope) and the reminder source re-announces that catalog; the chat
// runtime does not bake it into the static prompt. The loader owns the disk
// layout, so the provider is built here and injected through ChatRuntimeDeps.
//
// The provider is also the session's reload handle: nothing watches the skill
// folders, so /api/skills reload asks every live session's manager to rebuild —
// a session created before a skill landed would otherwise keep announcing the
// catalog it loaded at creation. The holder is handed to the session as
// Session.LiveSkills so the server can reach it (SessionManager reload fan-out).
//
// Parity: framework/server/queen_orchestrator.py (queen SkillsManagerConfig:
// queen_id, queen_overrides_path, extra_scope_dirs label="queen_ui")
// Parity: framework/skills/live_reload.py::ManagerReloadAdapter

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// queenSkillsCatalog is one session's live skills catalog. The manager is
// created on first use and then reloaded in place, so the injected provider
// sees the refreshed catalog on the next turn.
type queenSkillsCatalog struct {
	root     string
	queen    string
	userHome string

	once    sync.Once
	manager *skills.SkillsManager
	// fallback guards Reload for a holder without a hive home (degrade path):
	// the reload is a no-op but still satisfies the mutation-lock contract.
	fallback sync.Mutex
}

// queenSkillsCatalogProvider renders one session's queen catalog on first use
// and then serves that manager's (reloadable) prompt. The provider is per
// session (like upstream's queen SkillsManager) so a skill installed after an
// older session was created still reaches new sessions without a serve restart.
func queenSkillsCatalogProvider(home hivehome.Home, queenScope string) func() string {
	return queenSkillsCatalogAt(home, queenScope, "").Catalog
}

// queenSkillsCatalogAt builds the holder itself: the caller hands it to the
// session (Session.LiveSkills) so a live reload reaches the same manager whose
// prompt the provider announces.
func queenSkillsCatalogAt(home hivehome.Home, queenScope, userHome string) *queenSkillsCatalog {
	queen := strings.TrimSpace(queenScope)
	if queen == "" {
		queen = "default"
	}
	return &queenSkillsCatalog{
		root:     strings.TrimSpace(home.Root),
		queen:    queen,
		userHome: strings.TrimSpace(userHome),
	}
}

// queenSkillsCatalogProviderAt is the test seam that pins the user home.
func queenSkillsCatalogProviderAt(home hivehome.Home, queenScope, userHome string) func() string {
	return queenSkillsCatalogAt(home, queenScope, userHome).Catalog
}

// liveSkillsCatalog builds a session's reloadable catalog and records it on the
// session, so /api/skills reload rebuilds the same manager whose prompt the
// skills reminder announces. The caller feeds catalog.Catalog to the runtime.
func liveSkillsCatalog(record *hiveserver.Session, home hivehome.Home, queenScope string) *queenSkillsCatalog {
	catalog := queenSkillsCatalogAt(home, queenScope, "")
	if record != nil {
		record.LiveSkills = catalog
	}
	return catalog
}

// managerInstance lazily builds the session's queen manager. A holder without
// a hive home (a degrade path upstream never hits) yields nil, so the provider
// stays empty and Reload is a no-op instead of inventing a layout.
func (c *queenSkillsCatalog) managerInstance() *skills.SkillsManager {
	if c.root == "" {
		return nil
	}
	c.once.Do(func() {
		queenDir := hivehome.Home{Root: c.root}.QueenDir(c.queen)
		manager := skills.NewSkillsManager(&skills.SkillsManagerConfig{
			QueenID:                c.queen,
			QueenOverridesPath:     filepath.Join(queenDir, "skills_overrides.json"),
			ExtraScopeDirs:         []skills.ExtraScope{{Directory: filepath.Join(queenDir, "skills"), Label: "queen_ui", Priority: 2}},
			SkipCommunityDiscovery: true,
			Interactive:            false,
			HomeDir:                c.userHome,
			HiveHome:               c.root,
		})
		manager.Load()
		c.manager = manager
	})
	return c.manager
}

// SkillsCatalogPrompt is the session's current skills catalog prompt (the
// ReloadableSkillsManager contract). Reading after a Reload returns the
// rebuilt catalog.
func (c *queenSkillsCatalog) SkillsCatalogPrompt() string {
	if manager := c.managerInstance(); manager != nil {
		return manager.SkillsCatalogPrompt()
	}
	return ""
}

// Catalog is the provider handle the chat runtime deps receive.
func (c *queenSkillsCatalog) Catalog() string { return c.SkillsCatalogPrompt() }

// CatalogForPhase renders the session's catalog filtered for a queen phase
// ("independent" / "colony"): skills whose metadata.visibility list is present
// and excludes the phase drop out. The queen's reminder reads the LIVE phase on
// every render, exactly like upstream's QueenPhaseState.render_skills_catalog
// (skills_catalog_prompt_for_phase(self.phase)) — a session that forks mid-life
// re-announces the colony catalog on the next turn.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.render_skills_catalog
func (c *queenSkillsCatalog) CatalogForPhase(phase string) string {
	manager := c.managerInstance()
	if manager == nil {
		return ""
	}
	value := strings.TrimSpace(phase)
	if value == "" {
		return manager.SkillsCatalogPrompt()
	}
	return manager.SkillsCatalogPromptForPhase(&value)
}

// Reload rebuilds the catalog from disk. Callers hold MutationLock, matching
// the ReloadableSkillsManager contract.
func (c *queenSkillsCatalog) Reload() error {
	if manager := c.managerInstance(); manager != nil {
		return manager.Reload()
	}
	return nil
}

// MutationLock is the manager's mutation lock (the fallback when there is no
// manager), so the reload fan-out serialises against the same writes it does.
func (c *queenSkillsCatalog) MutationLock() *sync.Mutex {
	if manager := c.managerInstance(); manager != nil {
		return manager.MutationLock()
	}
	return &c.fallback
}

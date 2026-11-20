package skills

import (
	"log"
	"sync"
	"unicode/utf8"
)

type SkillsManagerConfig struct {
	SkillsConfig           *SkillsConfig
	ProjectRoot            string
	SkipCommunityDiscovery bool
	Interactive            bool
	QueenID                string
	QueenOverridesPath     string
	ExtraScopeDirs         []ExtraScope
	DefaultPresetSkills    map[string]struct{}
	HomeDir                string
	HiveHome               string
}

func DefaultSkillsManagerConfig() *SkillsManagerConfig {
	return &SkillsManagerConfig{
		SkillsConfig:        NewSkillsConfig(),
		Interactive:         true,
		DefaultPresetSkills: map[string]struct{}{},
	}
}

type SkillsManager struct {
	config *SkillsManagerConfig
	loaded bool

	catalog         *SkillCatalog
	allSkills       []*ParsedSkill
	catalogPrompt   string
	protocolsPrompt string
	allowlistedDirs []string
	defaultMgr      *DefaultSkillManager
	queenOverrides  *SkillOverrideStore

	watchedDirs  []string
	watchedFiles []string

	mutationLock sync.Mutex
	// snapshotMu guards the catalog snapshot a reload swaps in. A live reload
	// runs while queen turns read SkillsCatalogPrompt from another goroutine
	// (the skills reminder provider), so the swap must not race those reads.
	snapshotMu sync.RWMutex
}

func NewSkillsManager(config *SkillsManagerConfig) *SkillsManager {
	if config == nil {
		config = DefaultSkillsManagerConfig()
	}
	if config.SkillsConfig == nil {
		config.SkillsConfig = NewSkillsConfig()
	}
	if config.DefaultPresetSkills == nil {
		config.DefaultPresetSkills = map[string]struct{}{}
	}
	return &SkillsManager{config: config}
}

func NewSkillsManagerFromPrecomputed(catalogPrompt, protocolsPrompt string) *SkillsManager {
	return &SkillsManager{
		config:          DefaultSkillsManagerConfig(),
		loaded:          true,
		catalogPrompt:   catalogPrompt,
		protocolsPrompt: protocolsPrompt,
		allowlistedDirs: []string{},
	}
}

func (m *SkillsManager) Load() {
	m.snapshotMu.Lock()
	if m.loaded {
		m.snapshotMu.Unlock()
		return
	}
	m.loaded = true
	m.snapshotMu.Unlock()

	if err := m.doLoad(); err != nil {
		log.Printf("Skill system init failed (non-fatal): %v", err)
	}
}

func (m *SkillsManager) doLoad() error {
	skillsConfig := m.config.SkillsConfig

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        m.config.ProjectRoot,
		HomeDir:            m.config.HomeDir,
		HiveHome:           m.config.HiveHome,
		SkipFrameworkScope: false,
		ExtraScopes:        m.config.ExtraScopeDirs,
	})
	discovered := discovery.Discover()
	watchedDirs := discovery.ScannedDirectories()

	if m.config.ProjectRoot != "" && !m.config.SkipCommunityDiscovery {
		gate := NewTrustGate(nil, m.config.Interactive)
		discovered = gate.FilterAndGate(discovered, m.config.ProjectRoot)
	}

	var queenStore *SkillOverrideStore
	if m.config.QueenOverridesPath != "" {
		queenStore = LoadSkillOverrideStore(m.config.QueenOverridesPath, "queen:"+m.config.QueenID)
	}
	var watchedFiles []string
	if m.config.QueenOverridesPath != "" {
		watchedFiles = append(watchedFiles, m.config.QueenOverridesPath)
	}

	allSkills := make([]*ParsedSkill, len(discovered))
	copy(allSkills, discovered)
	discovered = ApplyManagerOverrides(discovered, skillsConfig, queenStore, m.config.DefaultPresetSkills)

	catalog := NewSkillCatalog(discovered)
	catalogPrompt := catalog.ToPrompt(nil)

	defaultMgr := NewDefaultSkillManager(skillsConfig)
	defaultMgr.Load()
	defaultMgr.LogActiveSkills()

	// Swap the whole snapshot at once so a concurrent reader never observes a
	// half-reloaded manager (new catalog with the old prompt, and so on).
	m.snapshotMu.Lock()
	m.queenOverrides = queenStore
	m.watchedDirs = watchedDirs
	m.watchedFiles = watchedFiles
	m.allSkills = allSkills
	m.catalog = catalog
	m.allowlistedDirs = catalog.AllowlistedDirs()
	m.defaultMgr = defaultMgr
	m.catalogPrompt = catalogPrompt
	m.protocolsPrompt = ""
	m.snapshotMu.Unlock()

	if catalogPrompt != "" {
		log.Printf("Skill system ready: catalog=%d chars", utf8.RuneCountInString(catalogPrompt))
	}
	return nil
}

func ApplyManagerOverrides(
	discovered []*ParsedSkill,
	skillsConfig *SkillsConfig,
	queenStore *SkillOverrideStore,
	defaultPresetSkills map[string]struct{},
) []*ParsedSkill {
	tombstones := map[string]struct{}{}
	if queenStore != nil {
		for name := range queenStore.DeletedUISkills {
			tombstones[name] = struct{}{}
		}
	}

	out := make([]*ParsedSkill, 0, len(discovered))
	for _, skill := range discovered {
		if _, dead := tombstones[skill.Name]; dead {
			continue
		}

		var explicit *bool
		masterDisabled := false
		if queenStore != nil {
			entry := queenStore.Get(skill.Name)
			if entry != nil && entry.Enabled != nil {
				explicit = entry.Enabled
			} else if queenStore.AllDefaultsDisabled {
				masterDisabled = true
			}
		}
		if explicit != nil && !*explicit {
			continue
		}
		if explicit != nil && *explicit {
			out = append(out, skill)
			continue
		}
		if skill.SourceScope == "preset" {
			if _, isDefault := defaultPresetSkills[skill.Name]; isDefault {
				out = append(out, skill)
			}
			continue
		}
		defaultEnabled := skillsConfig.IsDefaultEnabled(skill.Name)
		if masterDisabled && defaultEnabled && skill.SourceScope == "framework" {
			continue
		}
		if defaultEnabled {
			out = append(out, skill)
		}
	}
	return out
}

func (m *SkillsManager) QueenOverrides() *SkillOverrideStore {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.queenOverrides
}

func (m *SkillsManager) MutationLock() *sync.Mutex { return &m.mutationLock }

func (m *SkillsManager) Reload() error {
	m.snapshotMu.Lock()
	m.loaded = false
	m.snapshotMu.Unlock()

	err := m.doLoad()

	m.snapshotMu.Lock()
	m.loaded = true
	m.snapshotMu.Unlock()

	log.Printf("Skills reloaded: protocols=%d chars, catalog=%d chars",
		utf8.RuneCountInString(m.ProtocolsPrompt()), utf8.RuneCountInString(m.SkillsCatalogPrompt()))
	return err
}

func (m *SkillsManager) EnumerateSkillsWithSource() []*ParsedSkill {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	out := make([]*ParsedSkill, len(m.allSkills))
	copy(out, m.allSkills)
	return out
}

func (m *SkillsManager) SkillsCatalogPrompt() string {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.catalogPrompt
}

func (m *SkillsManager) SkillsCatalogPromptForPhase(phase *string) string {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	if m.catalog == nil || phase == nil {
		return m.catalogPrompt
	}
	return m.catalog.ToPrompt(phase)
}

func (m *SkillsManager) ProtocolsPrompt() string {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.protocolsPrompt
}

func (m *SkillsManager) AllowlistedDirs() []string {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	out := make([]string, len(m.allowlistedDirs))
	copy(out, m.allowlistedDirs)
	return out
}

func (m *SkillsManager) IsLoaded() bool {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.loaded
}

func (m *SkillsManager) Catalog() *SkillCatalog {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.catalog
}

func (m *SkillsManager) DefaultManager() *DefaultSkillManager {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.defaultMgr
}

func (m *SkillsManager) WatchedDirs() []string {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	out := make([]string, len(m.watchedDirs))
	copy(out, m.watchedDirs)
	return out
}

func (m *SkillsManager) WatchedFiles() []string {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	out := make([]string, len(m.watchedFiles))
	copy(out, m.watchedFiles)
	return out
}

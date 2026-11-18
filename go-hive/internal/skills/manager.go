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
	if m.loaded {
		return
	}
	m.loaded = true

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
	m.watchedDirs = discovery.ScannedDirectories()

	if m.config.ProjectRoot != "" && !m.config.SkipCommunityDiscovery {
		gate := NewTrustGate(nil, m.config.Interactive)
		discovered = gate.FilterAndGate(discovered, m.config.ProjectRoot)
	}

	var queenStore *SkillOverrideStore
	if m.config.QueenOverridesPath != "" {
		queenStore = LoadSkillOverrideStore(m.config.QueenOverridesPath, "queen:"+m.config.QueenID)
	}
	m.queenOverrides = queenStore
	m.watchedFiles = nil
	if m.config.QueenOverridesPath != "" {
		m.watchedFiles = append(m.watchedFiles, m.config.QueenOverridesPath)
	}

	m.allSkills = make([]*ParsedSkill, len(discovered))
	copy(m.allSkills, discovered)
	discovered = ApplyManagerOverrides(discovered, skillsConfig, queenStore, m.config.DefaultPresetSkills)

	catalog := NewSkillCatalog(discovered)
	m.catalog = catalog
	m.allowlistedDirs = catalog.AllowlistedDirs()
	catalogPrompt := catalog.ToPrompt(nil)

	defaultMgr := NewDefaultSkillManager(skillsConfig)
	defaultMgr.Load()
	defaultMgr.LogActiveSkills()
	m.defaultMgr = defaultMgr

	m.catalogPrompt = catalogPrompt
	m.protocolsPrompt = ""

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

func (m *SkillsManager) QueenOverrides() *SkillOverrideStore { return m.queenOverrides }

func (m *SkillsManager) MutationLock() *sync.Mutex { return &m.mutationLock }

func (m *SkillsManager) Reload() error {
	m.loaded = false
	err := m.doLoad()
	m.loaded = true
	log.Printf("Skills reloaded: protocols=%d chars, catalog=%d chars",
		utf8.RuneCountInString(m.protocolsPrompt), utf8.RuneCountInString(m.catalogPrompt))
	return err
}

func (m *SkillsManager) EnumerateSkillsWithSource() []*ParsedSkill {
	out := make([]*ParsedSkill, len(m.allSkills))
	copy(out, m.allSkills)
	return out
}

func (m *SkillsManager) SkillsCatalogPrompt() string { return m.catalogPrompt }

func (m *SkillsManager) SkillsCatalogPromptForPhase(phase *string) string {
	if m.catalog == nil || phase == nil {
		return m.catalogPrompt
	}
	return m.catalog.ToPrompt(phase)
}

func (m *SkillsManager) ProtocolsPrompt() string { return m.protocolsPrompt }

func (m *SkillsManager) AllowlistedDirs() []string {
	out := make([]string, len(m.allowlistedDirs))
	copy(out, m.allowlistedDirs)
	return out
}

func (m *SkillsManager) IsLoaded() bool { return m.loaded }

func (m *SkillsManager) Catalog() *SkillCatalog { return m.catalog }

func (m *SkillsManager) DefaultManager() *DefaultSkillManager { return m.defaultMgr }

func (m *SkillsManager) WatchedDirs() []string {
	out := make([]string, len(m.watchedDirs))
	copy(out, m.watchedDirs)
	return out
}

func (m *SkillsManager) WatchedFiles() []string {
	out := make([]string, len(m.watchedFiles))
	copy(out, m.watchedFiles)
	return out
}

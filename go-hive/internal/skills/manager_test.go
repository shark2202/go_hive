package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newIsolatedManager(t *testing.T, config *SkillsManagerConfig) *SkillsManager {
	t.Helper()
	home := t.TempDir()
	config.HomeDir = home
	config.HiveHome = filepath.Join(home, ".hive")
	config.ProjectRoot = ""
	config.SkipCommunityDiscovery = true
	config.Interactive = false
	manager := NewSkillsManager(config)
	manager.Load()
	return manager
}

func catalogHas(manager *SkillsManager, name string) bool {
	if manager.Catalog() == nil {
		return false
	}
	return manager.Catalog().Get(name) != nil
}

func enumeratedNames(manager *SkillsManager) map[string]bool {
	out := map[string]bool{}
	for _, skill := range manager.EnumerateSkillsWithSource() {
		out[skill.Name] = true
	}
	return out
}

// Parity: tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_override_disables_framework_default
func TestManagerOverrideDisablesFrameworkDefault(t *testing.T) {
	tmp := t.TempDir()
	fakeFw := filepath.Join(tmp, "fake_framework")
	writeDiscoverySkill(t, fakeFw, "hive.fake-protocol", "Fake default")

	overridesPath := filepath.Join(tmp, "queen_overrides.json")
	store := LoadSkillOverrideStore(overridesPath, "queen:q")
	store.Upsert("hive.fake-protocol", &OverrideEntry{
		Enabled:    boolPtr(false),
		Provenance: ProvenanceFramework,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	manager := newIsolatedManager(t, &SkillsManagerConfig{
		QueenID:            "q",
		QueenOverridesPath: overridesPath,
		ExtraScopeDirs: []ExtraScope{
			{Directory: fakeFw, Label: "framework", Priority: 0},
		},
	})

	if catalogHas(manager, "hive.fake-protocol") {
		t.Fatal("disabled skill should not be in the catalog")
	}
	if !enumeratedNames(manager)["hive.fake-protocol"] {
		t.Fatalf("enumeration missing hive.fake-protocol: %+v", manager.EnumerateSkillsWithSource())
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_queen_disable_applies_to_colony_ui_skills
func TestManagerQueenDisableAppliesToColonyUISkills(t *testing.T) {
	tmp := t.TempDir()
	colonySkills := filepath.Join(tmp, "colony_home", "skills")
	writeDiscoverySkill(t, colonySkills, "shared-skill", "desc")

	queenOverrides := filepath.Join(tmp, "queen_overrides.json")
	qstore := LoadSkillOverrideStore(queenOverrides, "queen:q")
	qstore.Upsert("shared-skill", &OverrideEntry{
		Enabled:    boolPtr(false),
		Provenance: ProvenanceUserUICreated,
	})
	if err := qstore.Save(); err != nil {
		t.Fatal(err)
	}

	manager := newIsolatedManager(t, &SkillsManagerConfig{
		QueenID:            "q",
		QueenOverridesPath: queenOverrides,
		ExtraScopeDirs: []ExtraScope{
			{Directory: colonySkills, Label: "colony_ui", Priority: 3},
		},
		SkillsConfig: NewSkillsConfig(),
	})

	if catalogHas(manager, "shared-skill") {
		t.Fatal("queen-disabled colony skill should not be in the catalog")
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_preset_scope_is_off_by_default
func TestManagerPresetScopeOffByDefault(t *testing.T) {
	tmp := t.TempDir()
	fakePresets := filepath.Join(tmp, "fake_presets")
	writeDiscoverySkill(t, fakePresets, "hive.x-automation", "X capability pack")
	writeDiscoverySkill(t, fakePresets, "hive.browser-automation", "Browser pack")

	manager := newIsolatedManager(t, &SkillsManagerConfig{
		ExtraScopeDirs: []ExtraScope{
			{Directory: fakePresets, Label: "preset", Priority: 1},
		},
	})

	if catalogHas(manager, "hive.x-automation") {
		t.Fatal("preset skill should ship off by default")
	}
	if catalogHas(manager, "hive.browser-automation") {
		t.Fatal("preset skill should ship off by default")
	}
	names := enumeratedNames(manager)
	if !names["hive.x-automation"] || !names["hive.browser-automation"] {
		t.Fatalf("enumeration = %+v", names)
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_preset_skill_enabled_via_explicit_override
func TestManagerPresetSkillEnabledViaExplicitOverride(t *testing.T) {
	tmp := t.TempDir()
	fakePresets := filepath.Join(tmp, "fake_presets")
	writeDiscoverySkill(t, fakePresets, "hive.x-automation", "desc")

	overridesPath := filepath.Join(tmp, "queen_overrides.json")
	store := LoadSkillOverrideStore(overridesPath, "queen:q")
	store.Upsert("hive.x-automation", &OverrideEntry{
		Enabled:    boolPtr(true),
		Provenance: ProvenancePreset,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	manager := newIsolatedManager(t, &SkillsManagerConfig{
		QueenID:            "q",
		QueenOverridesPath: overridesPath,
		ExtraScopeDirs: []ExtraScope{
			{Directory: fakePresets, Label: "preset", Priority: 1},
		},
	})

	if !catalogHas(manager, "hive.x-automation") {
		t.Fatal("explicitly enabled preset should be in the catalog")
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_role_default_preset_enabled_in_memory
func TestManagerRoleDefaultPresetEnabledInMemory(t *testing.T) {
	tmp := t.TempDir()
	fakePresets := filepath.Join(tmp, "fake_presets")
	writeDiscoverySkill(t, fakePresets, "hive.linkedin-message-campaign", "campaign")
	writeDiscoverySkill(t, fakePresets, "hive.x-automation", "x pack")

	manager := newIsolatedManager(t, &SkillsManagerConfig{
		QueenID: "queen_outbound",
		DefaultPresetSkills: map[string]struct{}{
			"hive.linkedin-message-campaign": {},
		},
		ExtraScopeDirs: []ExtraScope{
			{Directory: fakePresets, Label: "preset", Priority: 1},
		},
	})

	if !catalogHas(manager, "hive.linkedin-message-campaign") {
		t.Fatal("role-default preset should be enabled")
	}
	if catalogHas(manager, "hive.x-automation") {
		t.Fatal("non-default preset should stay off")
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_role_default_preset_disabled_by_explicit_override
func TestManagerRoleDefaultPresetDisabledByExplicitOverride(t *testing.T) {
	tmp := t.TempDir()
	fakePresets := filepath.Join(tmp, "fake_presets")
	writeDiscoverySkill(t, fakePresets, "hive.linkedin-message-campaign", "campaign")

	overridesPath := filepath.Join(tmp, "queen_overrides.json")
	store := LoadSkillOverrideStore(overridesPath, "queen:queen_outbound")
	store.Upsert("hive.linkedin-message-campaign", &OverrideEntry{
		Enabled:    boolPtr(false),
		Provenance: ProvenancePreset,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	manager := newIsolatedManager(t, &SkillsManagerConfig{
		QueenID:            "queen_outbound",
		QueenOverridesPath: overridesPath,
		DefaultPresetSkills: map[string]struct{}{
			"hive.linkedin-message-campaign": {},
		},
		ExtraScopeDirs: []ExtraScope{
			{Directory: fakePresets, Label: "preset", Priority: 1},
		},
	})

	if catalogHas(manager, "hive.linkedin-message-campaign") {
		t.Fatal("explicit disable should beat the role default")
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_reload_picks_up_store_change
func TestManagerReloadPicksUpStoreChange(t *testing.T) {
	tmp := t.TempDir()
	fw := filepath.Join(tmp, "fw")
	writeDiscoverySkill(t, fw, "alpha", "desc")
	path := filepath.Join(tmp, "queen.json")

	manager := newIsolatedManager(t, &SkillsManagerConfig{
		QueenID:            "q",
		QueenOverridesPath: path,
		ExtraScopeDirs: []ExtraScope{
			{Directory: fw, Label: "framework", Priority: 0},
		},
	})
	if !catalogHas(manager, "alpha") {
		t.Fatal("alpha should start enabled")
	}

	store := LoadSkillOverrideStore(path, "queen:q")
	store.Upsert("alpha", &OverrideEntry{
		Enabled:    boolPtr(false),
		Provenance: ProvenanceFramework,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	if catalogHas(manager, "alpha") {
		t.Fatal("reload should pick up the disable override")
	}
}

// Parity: tests/test_skill_integration.py::TestEndToEndPipeline::test_discovery_to_catalog_to_prompt
func TestEndToEndDiscoveryToCatalogToPrompt(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, ".agents", "skills", "my-tool")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: my-tool\ndescription: Tool for testing.\n---\n\n## Usage\nUse this tool when testing.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        tmp,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	skills := discovery.Discover()
	if len(skills) != 1 {
		t.Fatalf("skills = %d, want 1", len(skills))
	}

	catalog := NewSkillCatalog(skills)
	if catalog.SkillCount() != 1 {
		t.Fatalf("skill_count = %d", catalog.SkillCount())
	}

	prompt := catalog.ToPrompt(nil)
	if !strings.Contains(prompt, "<name>my-tool</name>") || !strings.Contains(prompt, "<description>Tool for testing.</description>") {
		t.Fatalf("prompt = %q", prompt)
	}
}

// Parity: tests/test_skill_integration.py::TestEndToEndPipeline::test_defaults_plus_community_skills
func TestEndToEndDefaultsPlusCommunitySkills(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, ".agents", "skills", "community-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: community-skill\ndescription: A community skill.\n---\n\nDo stuff.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        tmp,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	catalogPrompt := NewSkillCatalog(discovery.Discover()).ToPrompt(nil)

	manager := NewDefaultSkillManager(nil)
	manager.Load()
	protocolsPrompt := manager.BuildProtocolsPrompt()

	if !strings.Contains(catalogPrompt, "<available_skills>") || !strings.Contains(catalogPrompt, "<name>community-skill</name>") {
		t.Fatalf("catalog prompt = %q", catalogPrompt)
	}
	if !strings.Contains(protocolsPrompt, "## Operational Protocols") {
		t.Fatalf("protocols prompt = %q", protocolsPrompt)
	}
}

// Parity: tests/test_skill_integration.py::TestEndToEndPipeline::test_config_disables_defaults_keeps_community
func TestEndToEndConfigDisablesDefaultsKeepsCommunity(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, ".agents", "skills", "still-here")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: still-here\ndescription: Survives config.\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        tmp,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	catalog := NewSkillCatalog(discovery.Discover())

	config := NewSkillsConfig()
	config.AllDefaultsDisabled = true
	manager := NewDefaultSkillManager(config)
	manager.Load()

	catalogPrompt := catalog.ToPrompt(nil)
	protocolsPrompt := manager.BuildProtocolsPrompt()

	if !strings.Contains(catalogPrompt, "<name>still-here</name>") {
		t.Fatalf("catalog prompt = %q", catalogPrompt)
	}
	if protocolsPrompt != "" {
		t.Fatalf("protocols prompt = %q, want empty", protocolsPrompt)
	}
}

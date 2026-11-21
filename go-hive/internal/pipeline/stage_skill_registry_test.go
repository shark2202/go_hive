package pipeline

import (
	"path/filepath"
	"testing"
)

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// stage loads a SkillsManager and exposes it as the pipeline's skills output.
func TestSkillRegistryLoadsManager(t *testing.T) {
	_, home := testHome(t)
	projectRoot := filepath.Join(home, "agent")
	stage, err := BuildStage(map[string]any{
		"type": "skill_registry",
		"config": map[string]any{
			"project_root": projectRoot,
			"interactive":  false,
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	registry := stage.(*SkillRegistryStage)
	if err := registry.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if registry.SkillsManager == nil || !registry.SkillsManager.IsLoaded() {
		t.Fatal("skills manager not loaded")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// config keys map to SkillsManagerConfig (project_root, interactive,
// skills_config, extra_scope_dirs).
func TestSkillRegistryConfigParsing(t *testing.T) {
	stage, err := BuildStage(map[string]any{
		"type": "skill_registry",
		"config": map[string]any{
			"project_root": "/tmp/agent",
			"interactive":  false,
			"skills_config": map[string]any{
				"_all":     map[string]any{"enabled": false},
				"my-skill": map[string]any{"enabled": true},
			},
			"extra_scope_dirs": []any{
				map[string]any{"directory": "/tmp/colony_ui/skills", "label": "colony_ui", "priority": 3},
			},
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	registry := stage.(*SkillRegistryStage)
	if registry.ProjectRoot != "/tmp/agent" || registry.Interactive {
		t.Fatalf("parsed stage = %+v", registry)
	}
	// The master switch wins over per-skill enables, exactly like upstream's
	// is_default_enabled (all_defaults_disabled short-circuits first).
	if registry.SkillsConfig == nil || !registry.SkillsConfig.AllDefaultsDisabled {
		t.Fatal("skills_config._all.enabled=false must disable all defaults")
	}
	if registry.SkillsConfig.IsDefaultEnabled("my-skill") {
		t.Fatal("master switch must win over a per-skill enable")
	}
	perSkill, err := BuildStage(map[string]any{
		"type": "skill_registry",
		"config": map[string]any{
			"skills_config": map[string]any{
				"enabled-skill":  map[string]any{"enabled": true},
				"disabled-skill": map[string]any{"enabled": false},
			},
		},
	})
	if err != nil {
		t.Fatalf("build per-skill: %v", err)
	}
	perSkillConfig := perSkill.(*SkillRegistryStage).SkillsConfig
	if !perSkillConfig.IsDefaultEnabled("enabled-skill") || perSkillConfig.IsDefaultEnabled("disabled-skill") {
		t.Fatal("per-skill enable/disable not parsed")
	}
	if len(registry.ExtraScopeDirs) != 1 || registry.ExtraScopeDirs[0].Label != "colony_ui" || registry.ExtraScopeDirs[0].Priority != 3 {
		t.Fatalf("extra scopes = %+v", registry.ExtraScopeDirs)
	}
	if _, err := BuildStage(map[string]any{
		"type":   "skill_registry",
		"config": map[string]any{"extra_scope_dirs": "not-a-list"},
	}); err == nil {
		t.Fatal("non-list extra_scope_dirs must fail")
	}
	if _, err := BuildStage(map[string]any{
		"type":   "skill_registry",
		"config": map[string]any{"skills_config": "not-an-object"},
	}); err == nil {
		t.Fatal("non-object skills_config must fail")
	}
}

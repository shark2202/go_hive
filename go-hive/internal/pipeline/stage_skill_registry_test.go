package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	t.Cleanup(registry.StopWatching)
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

func writeProjectSkill(t *testing.T, root, name, description string) string {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nInstructions.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return path
}

// Parity: framework/pipeline/stages/skill_registry.py::SkillRegistryStage.initialize
// (start_watching): a SKILL.md edit reloads the manager and the next read of
// the catalog prompt reflects the new discovery.
func TestSkillRegistryWatcherReloadsCatalog(t *testing.T) {
	_, home := testHome(t)
	projectRoot := filepath.Join(home, "agent")
	skillPath := writeProjectSkill(t, projectRoot, "watcher-alpha", "Original description.")

	stage := &SkillRegistryStage{
		BaseStage:     BaseStage{StageName: "skill_registry", StageOrder: 60},
		ProjectRoot:   projectRoot,
		Interactive:   false,
		WatchInterval: 5 * time.Millisecond,
		WatchDebounce: 0,
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(stage.StopWatching)
	if stage.Watcher == nil {
		t.Fatal("watcher not started")
	}
	if !strings.Contains(stage.SkillsCatalogPrompt(), "watcher-alpha") {
		t.Fatalf("initial catalog missing the project skill: %s", stage.SkillsCatalogPrompt())
	}

	renamed := "---\nname: watcher-omega\ndescription: Renamed description.\n---\n\nInstructions.\n"
	if err := os.WriteFile(skillPath, []byte(renamed), 0o644); err != nil {
		t.Fatalf("rewrite SKILL.md: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stage.SkillsCatalogPrompt(), "watcher-omega") {
			stage.StopWatching()
			if stage.Watcher != nil {
				t.Fatal("StopWatching must clear the watcher")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("catalog did not reload after the SKILL.md change: %s", stage.SkillsCatalogPrompt())
}

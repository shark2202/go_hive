package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDiscoverySkill(t *testing.T, base, name, description string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nInstructions.\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_discover_project_skills
func TestDiscoverProjectSkills(t *testing.T) {
	root := t.TempDir()
	agentsSkills := filepath.Join(root, ".agents", "skills")
	writeDiscoverySkill(t, agentsSkills, "skill-a", "A test skill.")
	writeDiscoverySkill(t, agentsSkills, "skill-b", "A test skill.")

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        root,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	skills := discovery.Discover()
	if len(skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(skills))
	}
	names := map[string]bool{}
	for _, skill := range skills {
		names[skill.Name] = true
		if skill.SourceScope != "project" {
			t.Fatalf("source_scope = %q, want project", skill.SourceScope)
		}
	}
	if !names["skill-a"] || !names["skill-b"] {
		t.Fatalf("names = %v", names)
	}
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_hive_skills_path
func TestDiscoverHiveSkillsPath(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(root, ".hive", "skills"), "hive-skill", "A test skill.")

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        root,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	skills := discovery.Discover()
	if len(skills) != 1 || skills[0].Name != "hive-skill" {
		t.Fatalf("skills = %+v", skills)
	}
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_collision_project_overrides_user
func TestDiscoverCollisionProjectOverridesUser(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(root, "home", ".agents", "skills"), "shared-skill", "User version")
	writeDiscoverySkill(t, filepath.Join(root, "project", ".agents", "skills"), "shared-skill", "Project version")

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        filepath.Join(root, "project"),
		HomeDir:            filepath.Join(root, "home"),
		SkipFrameworkScope: true,
	})
	skills := discovery.Discover()
	var matching []*ParsedSkill
	for _, skill := range skills {
		if skill.Name == "shared-skill" {
			matching = append(matching, skill)
		}
	}
	if len(matching) != 1 {
		t.Fatalf("matching = %d, want 1", len(matching))
	}
	if matching[0].Description != "Project version" {
		t.Fatalf("description = %q", matching[0].Description)
	}
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_collision_hive_overrides_agents
func TestDiscoverCollisionHiveOverridesAgents(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(root, ".agents", "skills"), "override-test", "Agents version")
	writeDiscoverySkill(t, filepath.Join(root, ".hive", "skills"), "override-test", "Hive version")

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        root,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	skills := discovery.Discover()
	var matching []*ParsedSkill
	for _, skill := range skills {
		if skill.Name == "override-test" {
			matching = append(matching, skill)
		}
	}
	if len(matching) != 1 {
		t.Fatalf("matching = %d, want 1", len(matching))
	}
	if matching[0].Description != "Hive version" {
		t.Fatalf("description = %q", matching[0].Description)
	}
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_skips_git_and_node_modules
func TestDiscoverSkipsGitAndNodeModules(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".agents", "skills")
	writeDiscoverySkill(t, filepath.Join(skillsDir, ".git"), "git-skill", "A test skill.")
	writeDiscoverySkill(t, filepath.Join(skillsDir, "node_modules"), "npm-skill", "A test skill.")
	writeDiscoverySkill(t, skillsDir, "real-skill", "A test skill.")

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        root,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	names := map[string]bool{}
	for _, skill := range discovery.Discover() {
		names[skill.Name] = true
	}
	if !names["real-skill"] || names["git-skill"] || names["npm-skill"] {
		t.Fatalf("names = %v", names)
	}
}

// Parity-exempt: pins Python Path.is_dir() symlink-following (a symlinked skill
// directory such as ~/.agents/skills/ego-browser must be discovered); no
// upstream test covers the Go scanner.
func TestDiscoverSymlinkedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".agents", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := writeDiscoverySkill(t, filepath.Join(root, "store"), "ego-browser", "Symlinked browser skill.")
	if err := os.Symlink(installed, filepath.Join(skillsDir, "ego-browser")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// A symlink to a file must stay invisible: upstream only scans directories.
	target := filepath.Join(root, "not-a-skill.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(skillsDir, "file-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        root,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	names := map[string]bool{}
	for _, skill := range discovery.Discover() {
		names[skill.Name] = true
	}
	if !names["ego-browser"] {
		t.Fatalf("symlinked skill directory missing: %v", names)
	}
	if names["file-link"] {
		t.Fatalf("symlink to a file must not be scanned: %v", names)
	}
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_empty_scan
func TestDiscoverEmptyScan(t *testing.T) {
	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        t.TempDir(),
		SkipUserScope:      true,
		SkipFrameworkScope: true,
	})
	if skills := discovery.Discover(); len(skills) != 0 {
		t.Fatalf("skills = %+v", skills)
	}
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_framework_scope_loads_defaults
func TestDiscoverFrameworkScopeLoadsDefaults(t *testing.T) {
	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:   "",
		HomeDir:       t.TempDir(),
		HiveHome:      t.TempDir(),
		SkipUserScope: true,
	})
	skills := discovery.Discover()

	names := map[string]bool{}
	for _, skill := range skills {
		if skill.SourceScope == "framework" {
			names[skill.Name] = true
		}
	}
	if !names["hive.writing-hive-skills"] {
		t.Fatalf("framework names = %v", names)
	}
	if !names["hive.browser-automation"] {
		t.Fatalf("framework names = %v", names)
	}
}

// Parity: tests/test_skill_discovery.py::TestSkillDiscovery::test_max_depth_limit
func TestDiscoverMaxDepthLimit(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, ".agents", "skills", "a", "b", "c", "d", "e")
	writeDiscoverySkill(t, deep, "too-deep", "A test skill.")

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        root,
		SkipUserScope:      true,
		SkipFrameworkScope: true,
		MaxDepth:           2,
	})
	for _, skill := range discovery.Discover() {
		if skill.Name == "too-deep" {
			t.Fatalf("too-deep should be beyond max_depth")
		}
	}
}

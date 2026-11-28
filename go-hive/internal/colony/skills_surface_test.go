package colony

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// TestMain pins HOME/HIVE_HOME so the colony's new user/base skill scopes scan
// a pristine tree instead of the developer's real ~/.agents/skills. Individual
// tests override the pair with t.Setenv when they need a fixture home.
// Parity-exempt: Go test-process isolation for the user/framework skill
// scopes; upstream tests monkeypatch Path.home per test instead.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "hive-colony-skills-home-")
	if err != nil {
		os.Exit(1)
	}
	prevHome, hadHome := os.LookupEnv("HOME")
	prevHive, hadHive := os.LookupEnv("HIVE_HOME")
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("HIVE_HOME", filepath.Join(home, ".hive"))
	code := m.Run()
	if hadHome {
		_ = os.Setenv("HOME", prevHome)
	} else {
		_ = os.Unsetenv("HOME")
	}
	if hadHive {
		_ = os.Setenv("HIVE_HOME", prevHive)
	} else {
		_ = os.Unsetenv("HIVE_HOME")
	}
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// Parity: tests/test_skill_resources.py::TestSkillResourceBaseDir.test_node_context_skill_dirs_set
func TestWorkerSkillDirsAndCatalogComeFromColonySkillsFolder(t *testing.T) {
	colonyDir := t.TempDir()
	skillDir := filepath.Join(colonyDir, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: demo skill\n---\n\nDo the thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	if worker.Context == nil {
		t.Fatal("worker context is nil")
	}
	wantDir := filepath.Join(colonyDir, "skills")
	if !slices.Contains(worker.Context.SkillDirs, wantDir) {
		t.Fatalf("SkillDirs = %#v, want the colony root %s among them", worker.Context.SkillDirs, wantDir)
	}
	if !strings.Contains(worker.Context.SkillsCatalogPrompt, "<name>demo</name>") {
		t.Fatalf("skills catalog prompt = %q", worker.Context.SkillsCatalogPrompt)
	}
	catalog, ok := runtime.Skills()
	if !ok {
		t.Fatal("colony catalog unavailable")
	}
	byName := map[string]*skills.ParsedSkill{}
	for _, skill := range catalog {
		byName[skill.Name] = skill
	}
	if _, exists := byName["demo"]; !exists {
		t.Fatalf("colony catalog = %#v, want demo", catalog)
	}
	if _, exists := byName["hive.worker-delegation"]; !exists {
		t.Fatalf("colony catalog = %#v, want the framework worker-delegation skill", catalog)
	}
}

// Parity-exempt: upstream has no Go category-expansion path, and the explicit
// guard that keeps a worker from recursing into run_playbook is Go-specific.
func TestWorkerSurfaceExposesSkillReadsButNotRunPlaybook(t *testing.T) {
	colonyDir := t.TempDir()
	playbookTools := append([]llm.ToolDef{RunPlaybookToolDef()}, PlaybookControlToolDefs()...)
	declared := make([]string, 0, len(playbookTools)+1)
	declared = append(declared, skills.SkillViewToolName)
	for _, def := range playbookTools {
		declared = append(declared, def.Name)
	}
	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		AgentSpec: agentloop.AgentSpec{
			ID:    "worker",
			Tools: declared,
		},
		Tools:         append(append([]llm.ToolDef(nil), playbookTools...), skills.SkillViewToolDef()),
		DeclaredTools: append(append([]llm.ToolDef(nil), playbookTools...), skills.SkillViewToolDef()),
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w2", Task: "read a skill"})
	names := map[string]bool{}
	for _, def := range runtime.workerTools(worker) {
		names[def.Name] = true
	}
	if names[RunPlaybookToolName] {
		t.Fatal("worker surface exposed run_playbook")
	}
	for _, def := range PlaybookControlToolDefs() {
		if names[def.Name] {
			t.Fatalf("worker surface exposed queen-only playbook control %s", def.Name)
		}
	}
	if !names[skills.SkillViewToolName] || !names[skills.ListSkillsToolName] {
		t.Fatalf("worker surface missing skill reads: %#v", names)
	}
}

// writeColonySkill materialises one skill under a colony's skills/ folder.
func writeColonySkill(t *testing.T, colonyDir, name, description string) {
	t.Helper()
	skillDir := filepath.Join(colonyDir, "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSkillFile materialises one skill under an explicit skills/ root (the
// colony's or a queen's), with a body the read-tool assertions can tell apart.
func writeSkillFile(t *testing.T, skillsDir, name, description, skillBody string) {
	t.Helper()
	skillDir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + skillBody + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Parity: core/framework/host/colony_runtime.py::_build_default_skills_config
// Parity: core/tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_queen_disable_applies_to_colony_ui_skills
// Parity: core/tests/test_write_skill_tool.py::test_without_a_colony_it_writes_the_queens_own_skill_into_her_live_catalog
//
// A worker sees the owning queen's skill scope (queen_ui) ahead of the
// colony's own scope (colony_ui): both are announced and readable, and a
// colony skill overrides the queen's copy of the same name — the upstream
// scan order in _build_default_skills_config (07-memory-and-skills.md §4.2:
// "queen scope 对她的所有会话与 Colony 可见").
func TestWorkerSkillSurfaceSeesQueensScopeWithColonyOverride(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colonies", "demo-colony")
	queenDir := filepath.Join(root, "queens", "queen_ava")
	colonySkillsDir := filepath.Join(colonyDir, "skills")
	queenSkillsDir := filepath.Join(queenDir, "skills")
	writeSkillFile(t, colonySkillsDir, "colony-only", "colony skill", "Colony procedure.")
	writeSkillFile(t, queenSkillsDir, "queen-private", "queen skill", "Queen procedure.")
	writeSkillFile(t, queenSkillsDir, "shared", "queen copy", "Queen copy.")
	writeSkillFile(t, colonySkillsDir, "shared", "colony copy", "Colony copy.")

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		QueenDir:    queenDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	if worker.Context == nil {
		t.Fatal("worker context is nil")
	}
	ctx := worker.Context

	// The view_file allowlist covers every discovered scope; the colony's
	// folder still resolves before the queen's on a collision.
	if !slices.Contains(ctx.SkillDirs, colonySkillsDir) || !slices.Contains(ctx.SkillDirs, queenSkillsDir) {
		t.Fatalf("SkillDirs = %#v, want both %s and %s", ctx.SkillDirs, colonySkillsDir, queenSkillsDir)
	}
	if colonyIdx, queenIdx := slices.Index(ctx.SkillDirs, colonySkillsDir), slices.Index(ctx.SkillDirs, queenSkillsDir); queenIdx > colonyIdx {
		t.Fatalf("SkillDirs = %#v, want the queen scope before the colony scope", ctx.SkillDirs)
	}

	// The catalog announces both scopes; the colony copy wins the collision.
	for _, want := range []string{"<name>queen-private</name>", "<name>colony-only</name>", "<name>shared</name>"} {
		if !strings.Contains(ctx.SkillsCatalogPrompt, want) {
			t.Fatalf("catalog missing %s: %q", want, ctx.SkillsCatalogPrompt)
		}
	}
	colonyShared := filepath.Join(colonySkillsDir, "shared", "SKILL.md")
	queenShared := filepath.Join(queenSkillsDir, "shared", "SKILL.md")
	if !strings.Contains(ctx.SkillsCatalogPrompt, colonyShared) {
		t.Fatalf("catalog did not resolve 'shared' to the colony copy (%s): %q", colonyShared, ctx.SkillsCatalogPrompt)
	}
	if strings.Contains(ctx.SkillsCatalogPrompt, queenShared) {
		t.Fatalf("catalog kept the queen copy of 'shared' (%s): %q", queenShared, ctx.SkillsCatalogPrompt)
	}

	// The read tools reach a queen-only skill and return the colony copy for
	// the colliding name (scopedSkillRoots searches colony first, then queen).
	executor := runtime.executorFor(worker)
	read := func(name string) string {
		t.Helper()
		result, err := executor(agentloop.ToolCallRequest{
			Name:  skills.SkillViewToolName,
			Input: map[string]any{"skill_name": name},
		})
		if err != nil {
			t.Fatalf("skill_view %s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("skill_view %s errored: %s", name, result.Content)
		}
		return result.Content
	}
	if content := read("queen-private"); !strings.Contains(content, "Queen procedure.") {
		t.Fatalf("skill_view queen-private = %q, want the queen's procedure", content)
	}
	if content := read("shared"); !strings.Contains(content, "Colony copy.") || strings.Contains(content, "Queen copy.") {
		t.Fatalf("skill_view shared = %q, want the colony copy", content)
	}

	// The colony read surface lists the same visible set, with provenance.
	catalog, ok := runtime.Skills()
	if !ok {
		t.Fatal("colony Skills() reported no catalog")
	}
	byName := map[string]*skills.ParsedSkill{}
	for _, skill := range catalog {
		byName[skill.Name] = skill
	}
	if _, ok := byName["queen-private"]; !ok {
		t.Fatalf("catalog = %#v, want queen-private", catalog)
	}
	if shared, ok := byName["shared"]; !ok || shared.SourceScope != "colony_ui" {
		t.Fatalf("shared = %#v, want the colony_ui copy", byName["shared"])
	}

	// Reload rebuilds the same two-scope catalog and reports both roots.
	stats, err := runtime.ReloadSkills()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	dirs, _ := stats["skill_dirs"].([]string)
	if !slices.Contains(dirs, colonySkillsDir) || !slices.Contains(dirs, queenSkillsDir) {
		t.Fatalf("reload skill_dirs = %#v, want both %s and %s", dirs, colonySkillsDir, queenSkillsDir)
	}
}

// Parity: core/framework/host/colony_runtime.py::_build_default_skills_config
// Parity: core/tests/test_session_unified_colony_runtime.py::test_start_unified_colony_runtime_creates_real_colony
//
// A colony that has not materialised its own skills/ folder still surfaces the
// owning queen's scope to its workers (upstream's SkillsManager scans framework
// + user + queen_ui + colony_ui regardless of which directories exist).
func TestWorkerSkillSurfaceSeesQueenSkillsWithoutAColonySkillsFolder(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colonies", "fresh-colony")
	queenDir := filepath.Join(root, "queens", "queen_ava")
	writeSkillFile(t, filepath.Join(queenDir, "skills"), "queen-private", "queen skill", "Queen procedure.")

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		QueenDir:    queenDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	if worker.Context == nil {
		t.Fatal("worker context is nil")
	}
	if !strings.Contains(worker.Context.SkillsCatalogPrompt, "<name>queen-private</name>") {
		t.Fatalf("catalog = %q, want the queen's skill", worker.Context.SkillsCatalogPrompt)
	}
	wantDir := filepath.Join(queenDir, "skills")
	if !slices.Contains(worker.Context.SkillDirs, wantDir) {
		t.Fatalf("SkillDirs = %#v, want the queen root %s among them", worker.Context.SkillDirs, wantDir)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.reload_skills (a
// running worker picks the rebuilt catalog up on its next iteration via
// dynamic_skills_catalog_provider).
func TestColonyReloadSkillsRefreshesInFlightWorkerCatalog(t *testing.T) {
	colonyDir := t.TempDir()
	writeColonySkill(t, colonyDir, "demo", "demo skill")

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	provider := worker.Context.DynamicSkillsCatalogProvider
	if provider == nil {
		t.Fatal("worker context has no dynamic skills catalog provider")
	}
	if !strings.Contains(provider(), "<name>demo</name>") {
		t.Fatalf("provider catalog = %q", provider())
	}

	writeColonySkill(t, colonyDir, "extra", "second skill")
	if strings.Contains(provider(), "<name>extra</name>") {
		t.Fatal("provider saw the new skill before a reload")
	}

	stats, err := runtime.ReloadSkills()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	after := provider()
	if !strings.Contains(after, "<name>demo</name>") || !strings.Contains(after, "<name>extra</name>") {
		t.Fatalf("provider catalog after reload = %q, want both skills", after)
	}
	chars, ok := stats["catalog_chars"].(int)
	if !ok || chars == 0 {
		t.Fatalf("catalog_chars = %#v", stats["catalog_chars"])
	}
	dirs, ok := stats["skill_dirs"].([]string)
	if !ok || !slices.Contains(dirs, filepath.Join(colonyDir, "skills")) {
		t.Fatalf("skill_dirs = %#v", stats["skill_dirs"])
	}
}

// Parity-exempt: dropping a deleted skill folder is the Go memo's promise — an
// in-flight worker must not keep announcing a skill that no longer exists. The
// framework/user scopes survive, so only the colony copy disappears.
func TestColonyReloadSkillsDropsADeletedColonySkill(t *testing.T) {
	colonyDir := t.TempDir()
	writeColonySkill(t, colonyDir, "demo", "demo skill")

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	provider := worker.Context.DynamicSkillsCatalogProvider
	if provider == nil || !strings.Contains(provider(), "<name>demo</name>") {
		t.Fatalf("provider catalog = %q", provider())
	}

	if err := os.RemoveAll(filepath.Join(colonyDir, "skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ReloadSkills(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if catalog := provider(); strings.Contains(catalog, "<name>demo</name>") {
		t.Fatalf("provider catalog still announces the deleted demo skill: %q", catalog)
	}
}

// writeSkillWithVisibility materialises one skill with an optional
// metadata.visibility list, which phase filtering reads.
func writeSkillWithVisibility(t *testing.T, skillsDir, name, description string, visibility []string, body string) {
	t.Helper()
	skillDir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	frontmatter := "---\nname: " + name + "\ndescription: " + description + "\n"
	if len(visibility) > 0 {
		frontmatter += "metadata:\n  visibility: [" + strings.Join(visibility, ", ") + "]\n"
	}
	frontmatter += "---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(frontmatter), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeQueenProfile writes a custom queen's profile.yaml under the given
// queens/<id> home.
func writeQueenProfile(t *testing.T, queenDir, body string) {
	t.Helper()
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "profile.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Parity: framework/host/colony_runtime.py::_build_default_skills_config
// Parity: framework/skills/manager.py::_apply_overrides
//
// A worker's catalog covers framework, user and colony scopes, and its prompt
// is phase-filtered for "colony": a [colony] skill stays, an [independent] and
// a [worker] skill drop out while the unfiltered read catalog still carries
// them.
func TestWorkerCatalogCoversFrameworkUserAndColonyVisibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_HOME", filepath.Join(home, ".hive"))

	userSkills := filepath.Join(home, ".agents", "skills")
	writeSkillFile(t, userSkills, "user-local-skill", "user skill", "User procedure.")

	colonyDir := filepath.Join(home, "colonies", "scope-colony")
	colonySkills := filepath.Join(colonyDir, "skills")
	writeSkillWithVisibility(t, colonySkills, "colony-phase-skill", "colony phase skill", []string{"colony"}, "Colony phase procedure.")
	writeSkillWithVisibility(t, colonySkills, "independent-only", "independent skill", []string{"independent"}, "Independent procedure.")

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	if worker.Context == nil {
		t.Fatal("worker context is nil")
	}
	prompt := worker.Context.SkillsCatalogPrompt
	for _, want := range []string{
		"<name>hive.worker-delegation</name>", // framework, visibility [colony]
		"<name>hive.pdf</name>",               // framework, no visibility
		"<name>user-local-skill</name>",       // ~/.agents/skills
		"<name>colony-phase-skill</name>",     // colony_ui, visibility [colony]
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("catalog missing %s: %q", want, prompt)
		}
	}
	for _, gone := range []string{
		"<name>independent-only</name>",    // visibility [independent]
		"<name>hive.error-recovery</name>", // visibility [worker]
	} {
		if strings.Contains(prompt, gone) {
			t.Fatalf("catalog announced %s outside phase colony: %q", gone, prompt)
		}
	}
	if !slices.Contains(worker.Context.SkillDirs, userSkills) {
		t.Fatalf("SkillDirs = %#v, want the user scope %s among them", worker.Context.SkillDirs, userSkills)
	}
	if !slices.ContainsFunc(worker.Context.SkillDirs, func(dir string) bool {
		return strings.HasSuffix(filepath.ToSlash(dir), "/_default_skills")
	}) {
		t.Fatalf("SkillDirs = %#v, want the bundled framework scope among them", worker.Context.SkillDirs)
	}

	// The read route is the post-override manager catalog, not the
	// phase-filtered prompt: PhaseState only filters the prompt upstream.
	catalog, ok := runtime.Skills()
	if !ok {
		t.Fatal("colony Skills() reported no catalog")
	}
	byName := map[string]*skills.ParsedSkill{}
	for _, skill := range catalog {
		byName[skill.Name] = skill
	}
	for _, want := range []string{"independent-only", "hive.error-recovery", "user-local-skill", "colony-phase-skill"} {
		if _, exists := byName[want]; !exists {
			t.Fatalf("read catalog missing %s: %#v", want, catalog)
		}
	}
}

// Parity: framework/host/colony_runtime.py::_build_default_skills_config
//
// A custom queen's on-disk profile opts one preset pack in by default; every
// preset not named stays off. An empty list — and a colony with no queen
// profile at all — enables nothing.
func TestWorkerCatalogAppliesQueenProfileDefaultPresetSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_HOME", filepath.Join(home, ".hive"))

	queenDir := filepath.Join(home, "queens", "queen_custom")
	writeQueenProfile(t, queenDir, "title: Custom Queen\ndefault_preset_skills:\n  - hive.linkedin-automation\n")

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: filepath.Join(home, "colonies", "preset-colony"),
		QueenDir:    queenDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	prompt := worker.Context.SkillsCatalogPrompt
	if !strings.Contains(prompt, "<name>hive.linkedin-automation</name>") {
		t.Fatalf("catalog missing the role-default preset: %q", prompt)
	}
	for _, gone := range []string{"<name>hive.x-automation</name>", "<name>hive.chart-creation-foundations</name>"} {
		if strings.Contains(prompt, gone) {
			t.Fatalf("catalog announced a preset the profile did not opt in (%s): %q", gone, prompt)
		}
	}

	// An empty default_preset_skills list enables no preset at all.
	writeQueenProfile(t, queenDir, "title: Custom Queen\ndefault_preset_skills: []\n")
	emptyRuntime := NewColonyRuntime(RuntimeOptions{
		StoragePath: filepath.Join(home, "colonies", "preset-colony-empty"),
		QueenDir:    queenDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	emptyWorker := emptyRuntime.BuildWorker(WorkerBuildOptions{WorkerID: "w2", Task: "do it"})
	emptyPrompt := emptyWorker.Context.SkillsCatalogPrompt
	if strings.Contains(emptyPrompt, "<name>hive.linkedin-automation</name>") || strings.Contains(emptyPrompt, "<name>hive.x-automation</name>") {
		t.Fatalf("empty default_preset_skills still enabled a preset: %q", emptyPrompt)
	}

	// No queen profile: empty set, same as upstream's unknown queen.
	noQueen := NewColonyRuntime(RuntimeOptions{
		StoragePath: filepath.Join(home, "colonies", "preset-colony-none"),
		QueenDir:    filepath.Join(home, "queens", "queen_unknown"),
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	noQueenPrompt := noQueen.BuildWorker(WorkerBuildOptions{WorkerID: "w3", Task: "do it"}).Context.SkillsCatalogPrompt
	if strings.Contains(noQueenPrompt, "<name>hive.linkedin-automation</name>") || strings.Contains(noQueenPrompt, "<name>hive.x-automation</name>") {
		t.Fatalf("a queen without a profile enabled a preset: %q", noQueenPrompt)
	}
}

// Parity: framework/skills/manager.py::SkillDiscovery (later scopes win)
//
// A colony_ui skill overrides a framework skill of the same name in both the
// announced catalog and the read catalog.
func TestWorkerCatalogColonyScopeOverridesFrameworkSameName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_HOME", filepath.Join(home, ".hive"))

	colonyDir := filepath.Join(home, "colonies", "override-colony")
	colonySkills := filepath.Join(colonyDir, "skills")
	writeSkillFile(t, colonySkills, "hive.worker-delegation", "colony override", "Colony copy of worker delegation.")

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	colonyCopy := filepath.Join(colonySkills, "hive.worker-delegation", "SKILL.md")
	// macOS /var is a symlink to /private/var; ParsedSkill.Location is resolved.
	if resolved, err := filepath.EvalSymlinks(colonyCopy); err == nil {
		colonyCopy = resolved
	}
	if !strings.Contains(worker.Context.SkillsCatalogPrompt, colonyCopy) {
		t.Fatalf("catalog did not resolve hive.worker-delegation to the colony copy (%s): %q", colonyCopy, worker.Context.SkillsCatalogPrompt)
	}
	catalog, ok := runtime.Skills()
	if !ok {
		t.Fatal("colony Skills() reported no catalog")
	}
	var got *skills.ParsedSkill
	for _, skill := range catalog {
		if skill.Name == "hive.worker-delegation" {
			got = skill
			break
		}
	}
	if got == nil || got.SourceScope != "colony_ui" || got.Location != colonyCopy {
		t.Fatalf("hive.worker-delegation = %#v, want the colony_ui copy at %s", got, colonyCopy)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
//
// The persistent overseer carries the same phase-filtered catalog and read
// roots a spawned worker gets, so the colony's client-facing queen can follow
// framework procedures (e.g. worker-delegation) without a respawn.
func TestOverseerCatalogUsesColonySkillHierarchy(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(textResponse("idle"), textResponse("idle"), textResponse("idle")), eventbus.NewEventBus())
	worker, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if worker.Context == nil {
		t.Fatal("overseer context is nil")
	}
	if !strings.Contains(worker.Context.SkillsCatalogPrompt, "<name>hive.worker-delegation</name>") {
		t.Fatalf("overseer catalog missing the framework worker-delegation skill: %q", worker.Context.SkillsCatalogPrompt)
	}
	if worker.Context.DynamicSkillsCatalogProvider == nil {
		t.Fatal("overseer catalog has no dynamic provider")
	}
	if !slices.ContainsFunc(worker.Context.SkillDirs, func(dir string) bool {
		return strings.HasSuffix(filepath.ToSlash(dir), "/_default_skills")
	}) {
		t.Fatalf("overseer SkillDirs = %#v, want the bundled framework scope among them", worker.Context.SkillDirs)
	}
}

// Parity: core/tests/test_skill_overrides.py::TestSkillsManagerOverrides::test_queen_disable_applies_to_colony_ui_skills
//
// Colonies have no override store of their own: the owning queen's
// skills_overrides.json is the single authority, and its entries apply to
// skills discovered from a colony's dir (colony_ui) too. A disabled colony
// skill must drop out of both the announced catalog and the read surface, and
// come back after the store is edited and the runtime reloads.
func TestWorkerCatalogHonorsQueenDisableForColonySkills(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colonies", "demo-colony")
	queenDir := filepath.Join(root, "queens", "queen_ava")
	writeSkillFile(t, filepath.Join(colonyDir, "skills"), "shared-skill", "colony skill", "Colony procedure.")

	storePath := filepath.Join(queenDir, "skills_overrides.json")
	store := skills.LoadSkillOverrideStore(storePath, "queen:queen_ava")
	disabled := false
	store.Upsert("shared-skill", &skills.OverrideEntry{
		Enabled:    &disabled,
		Provenance: skills.ProvenanceUserUICreated,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		QueenDir:    queenDir,
		AgentSpec:   agentloop.AgentSpec{ID: "worker"},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "do it"})
	if worker.Context == nil {
		t.Fatal("worker context is nil")
	}
	if strings.Contains(worker.Context.SkillsCatalogPrompt, "<name>shared-skill</name>") {
		t.Fatalf("worker catalog announced a queen-disabled skill: %q", worker.Context.SkillsCatalogPrompt)
	}
	catalog, ok := runtime.Skills()
	if !ok {
		t.Fatal("colony Skills() reported no catalog")
	}
	for _, skill := range catalog {
		if skill.Name == "shared-skill" {
			t.Fatalf("read catalog announced a queen-disabled skill: %#v", skill)
		}
	}

	// Re-enabling is picked up by the next rebuild: the watcher fires on
	// skills_overrides.json changes, and ReloadSkills re-reads the store.
	enabled := true
	store.Upsert("shared-skill", &skills.OverrideEntry{
		Enabled:    &enabled,
		Provenance: skills.ProvenanceUserUICreated,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ReloadSkills(); err != nil {
		t.Fatalf("ReloadSkills: %v", err)
	}
	if got := worker.Context.DynamicSkillsCatalogProvider(); !strings.Contains(got, "<name>shared-skill</name>") {
		t.Fatalf("worker catalog missing the re-enabled skill after reload: %q", got)
	}
}

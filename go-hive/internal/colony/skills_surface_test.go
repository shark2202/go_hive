package colony

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/skills"
)

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
	if len(worker.Context.SkillDirs) != 1 || worker.Context.SkillDirs[0] != wantDir {
		t.Fatalf("SkillDirs = %#v, want [%s]", worker.Context.SkillDirs, wantDir)
	}
	if !strings.Contains(worker.Context.SkillsCatalogPrompt, "<name>demo</name>") {
		t.Fatalf("skills catalog prompt = %q", worker.Context.SkillsCatalogPrompt)
	}
	if catalog, ok := runtime.Skills(); !ok || len(catalog) != 1 || catalog[0].Name != "demo" {
		t.Fatalf("colony catalog = %#v, ok=%v", catalog, ok)
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

	// The view_file allowlist covers both scopes; the colony's folder stays
	// first so the read tools resolve a collision the same way the catalog does.
	if len(ctx.SkillDirs) != 2 || ctx.SkillDirs[0] != colonySkillsDir || ctx.SkillDirs[1] != queenSkillsDir {
		t.Fatalf("SkillDirs = %#v, want [%s %s]", ctx.SkillDirs, colonySkillsDir, queenSkillsDir)
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
	if len(dirs) != 2 || dirs[0] != colonySkillsDir || dirs[1] != queenSkillsDir {
		t.Fatalf("reload skill_dirs = %#v, want [%s %s]", dirs, colonySkillsDir, queenSkillsDir)
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
	if len(worker.Context.SkillDirs) != 1 || worker.Context.SkillDirs[0] != wantDir {
		t.Fatalf("SkillDirs = %#v, want [%s]", worker.Context.SkillDirs, wantDir)
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
	if !ok || len(dirs) != 1 || dirs[0] != filepath.Join(colonyDir, "skills") {
		t.Fatalf("skill_dirs = %#v", stats["skill_dirs"])
	}
}

// Parity-exempt: dropping the catalog when the folder disappears is the Go
// memo's promise — an in-flight worker must not keep announcing a deleted
// skill folder.
func TestColonyReloadSkillsDropsACatalogWhoseFolderIsGone(t *testing.T) {
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
	if catalog := provider(); catalog != "" {
		t.Fatalf("provider catalog = %q, want empty after the folder vanished", catalog)
	}
}

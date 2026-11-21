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

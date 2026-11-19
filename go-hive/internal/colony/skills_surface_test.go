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
	runtime := NewColonyRuntime(RuntimeOptions{
		StoragePath: colonyDir,
		AgentSpec: agentloop.AgentSpec{
			ID:    "worker",
			Tools: []string{RunPlaybookToolName, skills.SkillViewToolName},
		},
		Tools:         []llm.ToolDef{RunPlaybookToolDef(), skills.SkillViewToolDef()},
		DeclaredTools: []llm.ToolDef{RunPlaybookToolDef(), skills.SkillViewToolDef()},
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w2", Task: "read a skill"})
	names := map[string]bool{}
	for _, def := range runtime.workerTools(worker) {
		names[def.Name] = true
	}
	if names[RunPlaybookToolName] {
		t.Fatal("worker surface exposed run_playbook")
	}
	if !names[skills.SkillViewToolName] || !names[skills.ListSkillsToolName] {
		t.Fatalf("worker surface missing skill reads: %#v", names)
	}
}

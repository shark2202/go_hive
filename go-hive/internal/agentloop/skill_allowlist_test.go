package agentloop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeSkillReadInput(path string) map[string]any {
	return map[string]any{"path": path}
}

func writeSkillFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// Parity: tests/test_skill_allowlist.py::test_reads_file_in_skill_dir
func TestReadsFileInSkillDir(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	script := writeSkillFile(t, filepath.Join(skillDir, "scripts", "run.py"), "print('hello')")

	result, intercepted := InterceptSkillFileRead("view_file", makeSkillReadInput(script), []string{skillDir})
	if !intercepted {
		t.Fatal("file under a skill dir must be intercepted")
	}
	if result.Content != "print('hello')" {
		t.Errorf("content = %q, want %q", result.Content, "print('hello')")
	}
	if result.IsError {
		t.Error("successful skill read must not be an error")
	}
}

// Parity: tests/test_skill_allowlist.py::test_skill_md_read_marked_as_skill_content
func TestSkillMDReadMarkedAsSkillContent(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	skillMD := writeSkillFile(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: my-skill\ndescription: Test\n---\nInstructions.")

	result, intercepted := InterceptSkillFileRead("view_file", makeSkillReadInput(skillMD), []string{skillDir})
	if !intercepted {
		t.Fatal("SKILL.md must be intercepted")
	}
	if !result.IsSkillContent {
		t.Error("SKILL.md must be marked as skill content")
	}
	if result.IsError {
		t.Error("successful SKILL.md read must not be an error")
	}
}

// Parity: tests/test_skill_allowlist.py::test_non_skill_md_resource_not_marked
func TestNonSkillMDResourceNotMarked(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	ref := writeSkillFile(t, filepath.Join(skillDir, "references", "api.md"), "# API Reference")

	if _, intercepted := InterceptSkillFileRead("load_data", makeSkillReadInput(ref), []string{skillDir}); intercepted {
		t.Fatal("load_data must not be intercepted")
	}

	result, intercepted := InterceptSkillFileRead("view_file", makeSkillReadInput(ref), []string{skillDir})
	if !intercepted {
		t.Fatal("view_file resource under a skill dir must be intercepted")
	}
	if result.IsSkillContent {
		t.Error("a non-SKILL.md resource must not be marked as skill content")
	}
	if result.IsError {
		t.Error("successful resource read must not be an error")
	}
}

// Parity: tests/test_skill_allowlist.py::test_path_outside_skill_dir_goes_to_executor
func TestPathOutsideSkillDirGoesToExecutor(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	other := writeSkillFile(t, filepath.Join(base, "other", "file.txt"), "other content")

	if _, intercepted := InterceptSkillFileRead("view_file", makeSkillReadInput(other), []string{skillDir}); intercepted {
		t.Fatal("path outside skill dirs must fall through to the executor")
	}
}

// Parity: tests/test_skill_allowlist.py::test_no_skill_dirs_goes_to_executor
func TestNoSkillDirsGoesToExecutor(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	script := writeSkillFile(t, filepath.Join(skillDir, "scripts", "run.py"), "print('hello')")

	if _, intercepted := InterceptSkillFileRead("view_file", makeSkillReadInput(script), nil); intercepted {
		t.Fatal("empty skill dirs must send every call to the executor")
	}
}

// Parity: tests/test_skill_allowlist.py::test_missing_file_returns_error
func TestMissingSkillFileReturnsError(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	missing := filepath.Join(skillDir, "scripts", "missing.py")

	result, intercepted := InterceptSkillFileRead("view_file", makeSkillReadInput(missing), []string{skillDir})
	if !intercepted {
		t.Fatal("a path inside a skill dir must be intercepted even when missing")
	}
	if !result.IsError {
		t.Error("missing file must report is_error")
	}
	wantPrefix := fmt.Sprintf("Could not read skill resource '%s': ", missing)
	if !strings.HasPrefix(result.Content, wantPrefix) {
		t.Errorf("content = %q, want prefix %q", result.Content, wantPrefix)
	}
}

// Parity: tests/test_skill_allowlist.py::test_non_file_read_tool_goes_to_executor
func TestNonFileReadToolGoesToExecutor(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}

	if _, intercepted := InterceptSkillFileRead("web_search", makeSkillReadInput(filepath.Join(skillDir, "SKILL.md")), []string{skillDir}); intercepted {
		t.Fatal("non file-read tools must bypass the interceptor")
	}
}

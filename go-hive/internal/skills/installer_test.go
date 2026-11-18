package skills

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func makeTestSkillDir(t *testing.T, parent, name string) string {
	t.Helper()
	skillDir := filepath.Join(parent, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	content := "---\nname: " + name + "\ndescription: A test skill.\n---\n\n## Instructions\n\nDo things.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return skillDir
}

func makeTestParsedSkill(baseDir, name string) *ParsedSkill {
	return &ParsedSkill{
		Name:        name,
		Description: "Test skill.",
		Location:    filepath.Join(baseDir, "SKILL.md"),
		BaseDir:     baseDir,
		SourceScope: "user",
		Body:        "## Instructions",
	}
}

func fakeCloneFrom(src string) func(gitURL, targetPath, version string) error {
	return func(_, targetPath, _ string) error {
		if err := os.RemoveAll(targetPath); err != nil {
			return err
		}
		return copySkillDir(src, targetPath)
	}
}

func saveInstallHooks(t *testing.T) {
	t.Helper()
	origLookPath := lookPath
	origCloneShallow := cloneShallow
	origMakeTempDir := makeTempDir
	origRemoveAll := removeAll
	origSentinel := installNoticeSentinel
	origWriter := installNoticeWriter
	t.Cleanup(func() {
		lookPath = origLookPath
		cloneShallow = origCloneShallow
		makeTempDir = origMakeTempDir
		removeAll = origRemoveAll
		installNoticeSentinel = origSentinel
		installNoticeWriter = origWriter
	})
}

func assertSkillError(t *testing.T, err error, code SkillErrorCode) *SkillError {
	t.Helper()
	var skillErr *SkillError
	if !errors.As(err, &skillErr) {
		t.Fatalf("expected *SkillError, got %T: %v", err, err)
	}
	if skillErr.Code != code {
		t.Fatalf("expected code %s, got %s (%v)", code, skillErr.Code, skillErr)
	}
	return skillErr
}

// Parity: tests/test_skill_installer.py::test_copies_skill_dir_to_target
func TestInstallFromGitCopiesSkillDirToTarget(t *testing.T) {
	saveInstallHooks(t)
	tmp := t.TempDir()
	sourceRepo := filepath.Join(tmp, "repo")
	makeTestSkillDir(t, sourceRepo, ".")
	target := filepath.Join(tmp, "skills")

	cloneShallow = fakeCloneFrom(sourceRepo)
	lookPath = func(string) (string, error) { return "/usr/bin/git", nil }

	dest, err := InstallFromGit("https://example.com/skill.git", "my-skill", "", "", target)
	if err != nil {
		t.Fatalf("InstallFromGit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Errorf("installed SKILL.md missing: %v", err)
	}
	if want := filepath.Join(target, "my-skill"); dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
}

// Parity-exempt: no upstream test; caller-controlled skill names and --subdir
// must not reach paths outside the skills root / clone.
func TestSkillInstallPathsCannotEscape(t *testing.T) {
	base := t.TempDir()
	skillsDir := filepath.Join(base, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(base, "victim")
	marker := filepath.Join(victim, "keep.txt")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := RemoveInstalledSkill("../victim", skillsDir); err == nil {
		t.Fatal("RemoveInstalledSkill accepted a traversal name")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("traversal name removed the victim: %v", err)
	}
	if _, err := InstallFromGit("https://example.com/skill.git", "../escape", "", "", skillsDir); err == nil {
		t.Fatal("InstallFromGit accepted a traversal name")
	}
	source := makeTestParsedSkill(skillsDir, "src")
	if _, err := ForkSkill(source, "../fork", skillsDir); err == nil {
		t.Fatal("ForkSkill accepted a traversal name")
	}
}

// Parity-exempt: no upstream test; --subdir must resolve inside the clone.
func TestSkillInstallSubdirectoryStaysInsideClone(t *testing.T) {
	base := t.TempDir()
	clone := filepath.Join(base, "clone")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{clone, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("---\nname: evil\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"../outside", "nested/../../outside", outside} {
		if _, err := resolveInstallSource(clone, sub); err == nil {
			t.Fatalf("resolveInstallSource accepted %q", sub)
		}
	}
	if err := os.Symlink(outside, filepath.Join(clone, "link")); err == nil {
		if _, err := resolveInstallSource(clone, "link"); err == nil {
			t.Fatal("resolveInstallSource followed a symlink out of the clone")
		}
	}
	if got, err := resolveInstallSource(clone, "nested/skill"); err != nil {
		t.Fatalf("safe subdirectory rejected: %v", err)
	} else if got != filepath.Join(clone, "nested", "skill") {
		t.Fatalf("resolved = %q", got)
	}
}

// Parity-exempt: no upstream test; git must not parse a caller-controlled URL
// or ref as an option (--upload-pack executes a command).
func TestGitCloneArgvSeparatesOptions(t *testing.T) {
	args, err := gitCloneArgv("https://example.com/skill.git", "/tmp/dst", "v1.2.3")
	if err != nil {
		t.Fatalf("gitCloneArgv: %v", err)
	}
	if len(args) < 6 || args[len(args)-3] != "--" {
		t.Fatalf("args = %#v, want -- before repo", args)
	}
	if args[len(args)-2] != "https://example.com/skill.git" || args[len(args)-1] != "/tmp/dst" {
		t.Fatalf("args = %#v", args)
	}
	if _, err := gitCloneArgv("--upload-pack=touch /tmp/pwn", "/tmp/dst", ""); err == nil {
		t.Fatal("gitCloneArgv accepted a URL starting with '-'")
	}
	if _, err := gitCloneArgv("https://example.com/skill.git", "/tmp/dst", "--upload-pack=touch /tmp/pwn"); err == nil {
		t.Fatal("gitCloneArgv accepted a ref starting with '-'")
	}
}

// Parity: tests/test_skill_installer.py::test_raises_when_git_not_found
func TestInstallFromGitRaisesWhenGitNotFound(t *testing.T) {
	saveInstallHooks(t)
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	_, err := InstallFromGit("https://example.com/skill.git", "my-skill", "", "", filepath.Join(t.TempDir(), "skills"))
	skillErr := assertSkillError(t, err, SkillActivationFailed)
	if !strings.Contains(skillErr.Why, "git is not installed") {
		t.Errorf("why = %q, want it to contain %q", skillErr.Why, "git is not installed")
	}
}

// Parity: tests/test_skill_installer.py::test_raises_when_skill_md_missing
func TestInstallFromGitRaisesWhenSkillMDMissing(t *testing.T) {
	saveInstallHooks(t)
	tmp := t.TempDir()
	emptyRepo := filepath.Join(tmp, "empty_repo")
	if err := os.Mkdir(emptyRepo, 0o755); err != nil {
		t.Fatalf("mkdir empty repo: %v", err)
	}

	cloneShallow = fakeCloneFrom(emptyRepo)
	lookPath = func(string) (string, error) { return "/usr/bin/git", nil }

	_, err := InstallFromGit("https://example.com/skill.git", "my-skill", "deep-research", "", filepath.Join(tmp, "skills"))
	skillErr := assertSkillError(t, err, SkillNotFound)
	if skillErr.Code != SkillNotFound {
		t.Errorf("code = %s, want SKILL_NOT_FOUND", skillErr.Code)
	}
}

// Parity: tests/test_skill_installer.py::test_raises_when_target_already_exists
func TestInstallFromGitRaisesWhenTargetAlreadyExists(t *testing.T) {
	saveInstallHooks(t)
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "existing-skill"), 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	lookPath = func(string) (string, error) { return "/usr/bin/git", nil }

	_, err := InstallFromGit("https://example.com/skill.git", "existing-skill", "", "", skillsDir)
	skillErr := assertSkillError(t, err, SkillActivationFailed)
	if !strings.Contains(skillErr.Why, "already exists") {
		t.Errorf("why = %q, want it to contain %q", skillErr.Why, "already exists")
	}
}

// Parity: tests/test_skill_installer.py::test_cleans_temp_dir_on_clone_failure
func TestInstallFromGitCleansTempDirOnCloneFailure(t *testing.T) {
	saveInstallHooks(t)
	tmp := t.TempDir()

	originalMakeTempDir := makeTempDir
	var createdTmpDirs []string
	makeTempDir = func() (string, error) {
		dir, err := originalMakeTempDir()
		if err == nil {
			createdTmpDirs = append(createdTmpDirs, dir)
		}
		return dir, err
	}
	cloneShallow = func(gitURL, targetPath, version string) error {
		return NewSkillError(SkillActivationFailed, "clone failed", "network error", "check network")
	}
	lookPath = func(string) (string, error) { return "/usr/bin/git", nil }

	_, err := InstallFromGit("https://example.com/skill.git", "my-skill", "", "", filepath.Join(tmp, "skills"))
	if err == nil {
		t.Fatal("expected an error from failing clone")
	}
	if len(createdTmpDirs) == 0 {
		t.Fatal("expected at least one temp dir to be created")
	}
	for _, dir := range createdTmpDirs {
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Errorf("temp dir not cleaned: %s", dir)
		}
	}
}

// Parity: tests/test_skill_installer.py::test_removes_existing_skill
func TestRemoveInstalledSkillRemovesExistingSkill(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	skillDir := makeTestSkillDir(t, skillsDir, "my-skill")
	if _, err := os.Stat(skillDir); err != nil {
		t.Fatalf("setup: skill dir missing: %v", err)
	}

	removed, err := RemoveInstalledSkill("my-skill", skillsDir)
	if err != nil {
		t.Fatalf("RemoveInstalledSkill: %v", err)
	}
	if !removed {
		t.Error("removed = false, want true")
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Errorf("skill dir still exists: %v", err)
	}
}

// Parity: tests/test_skill_installer.py::test_returns_false_when_not_found
func TestRemoveInstalledSkillReturnsFalseWhenNotFound(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	if err := os.Mkdir(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}

	removed, err := RemoveInstalledSkill("nonexistent", skillsDir)
	if err != nil {
		t.Fatalf("RemoveInstalledSkill: %v", err)
	}
	if removed {
		t.Error("removed = true, want false")
	}
}

// Parity: tests/test_skill_installer.py::test_raises_on_permission_error
func TestRemoveInstalledSkillRaisesOnPermissionError(t *testing.T) {
	saveInstallHooks(t)
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "skills")
	makeTestSkillDir(t, skillsDir, "locked-skill")

	removeAll = func(string) error { return errors.New("permission denied") }

	_, err := RemoveInstalledSkill("locked-skill", skillsDir)
	skillErr := assertSkillError(t, err, SkillActivationFailed)
	if !strings.Contains(strings.ToLower(skillErr.Why), "permission") {
		t.Errorf("why = %q, want it to contain %q", skillErr.Why, "permission")
	}
}

// Parity: tests/test_skill_installer.py::test_copies_skill_to_new_name
func TestForkSkillCopiesSkillToNewName(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := makeTestSkillDir(t, filepath.Join(tmp, "sources"), "my-skill")
	source := makeTestParsedSkill(sourceDir, "my-skill")
	targetParent := filepath.Join(tmp, "skills")

	dest, err := ForkSkill(source, "my-skill-fork", targetParent)
	if err != nil {
		t.Fatalf("ForkSkill: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("dest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Errorf("forked SKILL.md missing: %v", err)
	}
}

// Parity: tests/test_skill_installer.py::test_rewrites_name_in_skill_md
func TestForkSkillRewritesNameInSkillMD(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := makeTestSkillDir(t, filepath.Join(tmp, "sources"), "original")
	source := makeTestParsedSkill(sourceDir, "original")
	targetParent := filepath.Join(tmp, "skills")

	dest, err := ForkSkill(source, "forked", targetParent)
	if err != nil {
		t.Fatalf("ForkSkill: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read forked SKILL.md: %v", err)
	}
	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		t.Fatalf("forked SKILL.md missing frontmatter: %q", content)
	}
	frontmatter := map[string]any{}
	if err := yaml.Unmarshal([]byte(parts[1]), &frontmatter); err != nil {
		t.Fatalf("parse forked frontmatter: %v", err)
	}
	if frontmatter["name"] != "forked" {
		t.Errorf("name = %v, want %q", frontmatter["name"], "forked")
	}
}

// Parity: tests/test_skill_installer.py::test_raises_when_dest_already_exists
func TestForkSkillRaisesWhenDestAlreadyExists(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := makeTestSkillDir(t, filepath.Join(tmp, "sources"), "my-skill")
	source := makeTestParsedSkill(sourceDir, "my-skill")
	targetParent := filepath.Join(tmp, "skills")
	if err := os.MkdirAll(filepath.Join(targetParent, "my-skill-fork"), 0o755); err != nil {
		t.Fatalf("mkdir fork target: %v", err)
	}

	_, err := ForkSkill(source, "my-skill-fork", targetParent)
	skillErr := assertSkillError(t, err, SkillActivationFailed)
	if !strings.Contains(skillErr.Why, "already exists") {
		t.Errorf("why = %q, want it to contain %q", skillErr.Why, "already exists")
	}
}

// Parity: tests/test_skill_installer.py::test_preserves_scripts_and_references
func TestForkSkillPreservesScriptsAndReferences(t *testing.T) {
	tmp := t.TempDir()
	sourceDir := makeTestSkillDir(t, filepath.Join(tmp, "sources"), "my-skill")
	scriptsDir := filepath.Join(sourceDir, "scripts")
	if err := os.Mkdir(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "run.sh"), []byte("#!/bin/sh\necho hi"), 0o644); err != nil {
		t.Fatalf("write run.sh: %v", err)
	}
	referencesDir := filepath.Join(sourceDir, "references")
	if err := os.Mkdir(referencesDir, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	if err := os.WriteFile(filepath.Join(referencesDir, "guide.md"), []byte("# Guide"), 0o644); err != nil {
		t.Fatalf("write guide.md: %v", err)
	}
	source := makeTestParsedSkill(sourceDir, "my-skill")
	targetParent := filepath.Join(tmp, "skills")

	dest, err := ForkSkill(source, "fork", targetParent)
	if err != nil {
		t.Fatalf("ForkSkill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts", "run.sh")); err != nil {
		t.Errorf("scripts/run.sh missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "references", "guide.md")); err != nil {
		t.Errorf("references/guide.md missing: %v", err)
	}
}

// Parity: tests/test_skill_installer.py::test_shown_on_first_call
func TestMaybeShowInstallNoticeShownOnFirstCall(t *testing.T) {
	saveInstallHooks(t)
	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, ".install_notice_shown")
	installNoticeSentinel = func() string { return sentinel }
	var out bytes.Buffer
	installNoticeWriter = &out

	MaybeShowInstallNotice()

	if !strings.Contains(out.String(), "Security Notice") {
		t.Errorf("notice output = %q, want it to contain %q", out.String(), "Security Notice")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel not created: %v", err)
	}
}

// Parity: tests/test_skill_installer.py::test_not_shown_on_second_call
func TestMaybeShowInstallNoticeNotShownOnSecondCall(t *testing.T) {
	saveInstallHooks(t)
	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, ".install_notice_shown")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		t.Fatalf("mkdir sentinel parent: %v", err)
	}
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatalf("touch sentinel: %v", err)
	}
	installNoticeSentinel = func() string { return sentinel }
	var out bytes.Buffer
	installNoticeWriter = &out

	MaybeShowInstallNotice()

	if strings.Contains(out.String(), "Security Notice") {
		t.Errorf("notice output = %q, want it NOT to contain %q", out.String(), "Security Notice")
	}
}

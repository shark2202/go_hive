package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"gopkg.in/yaml.v3"
)

const cloneInstallTimeout = 60 * time.Second

const installNoticeText = `─────────────────────────────────────────────────────────────
  Security Notice: Installing Third-Party Skills
─────────────────────────────────────────────────────────────
  Skills are instructions executed by AI agents. A malicious
  skill can manipulate agent behavior, exfiltrate data, or
  cause unintended actions.

  Only install skills from sources you trust. Review the
  SKILL.md before running it in a production environment.

  This notice is shown once. Use 'hive skill doctor' to audit
  installed skills at any time.
─────────────────────────────────────────────────────────────
`

var (
	lookPath                        = exec.LookPath
	cloneShallow                    = gitCloneShallow
	makeTempDir                     = func() (string, error) { return os.MkdirTemp("", "hive-skill-install-*") }
	removeAll                       = os.RemoveAll
	installNoticeWriter   io.Writer = os.Stdout
	installNoticeSentinel           = defaultInstallNoticeSentinel
)

func UserSkillsDir() string {
	home, err := hivehome.Resolve()
	if err != nil {
		return ""
	}
	return filepath.Join(home.Root, "skills")
}

func defaultInstallNoticeSentinel() string {
	home, err := hivehome.Resolve()
	if err != nil {
		return ""
	}
	return filepath.Join(home.Root, ".install_notice_shown")
}

func MaybeShowInstallNotice() {
	sentinel := installNoticeSentinel()
	if sentinel != "" {
		if _, err := os.Stat(sentinel); err == nil {
			return
		}
	}
	_, _ = io.WriteString(installNoticeWriter, installNoticeText+"\n")
	if sentinel == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		return
	}
	if f, err := os.OpenFile(sentinel, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
	}
}

// ValidSkillName reports whether name can be used as one directory name under
// the skills root. Registry entries and --name are caller-controlled, so a
// separator or dot segment must not reach filepath.Join.
func ValidSkillName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

// resolveInstallSource confines the optional --subdir inside the clone. A
// symlinked subdirectory is resolved and re-checked so it cannot point at, for
// example, /etc and be copied into the skills tree.
func resolveInstallSource(tmpDir, subdirectory string) (string, error) {
	if subdirectory == "" {
		return tmpDir, nil
	}
	subdirectory = strings.TrimSpace(subdirectory)
	if subdirectory == "" || filepath.IsAbs(subdirectory) {
		return "", fmt.Errorf("invalid skill subdirectory %q", subdirectory)
	}
	source := filepath.Join(tmpDir, filepath.FromSlash(subdirectory))
	rel, err := filepath.Rel(tmpDir, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("skill subdirectory escapes the clone: %q", subdirectory)
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		// The SKILL.md existence check reports a more useful error.
		return source, nil
	}
	rootResolved, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		rootResolved = tmpDir
	}
	rel, err = filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("skill subdirectory escapes the clone: %q", subdirectory)
	}
	return resolved, nil
}

func InstallFromGit(gitURL, skillName, subdirectory, version, targetDir string) (string, error) {
	if !ValidSkillName(skillName) {
		return "", NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Cannot install '%s'", skillName),
			"The skill name must be a single path segment (no '/', '\\', NUL, '.' or '..').",
			"Choose a safe --name and retry.",
		)
	}
	if _, err := lookPath("git"); err != nil {
		return "", NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Cannot install '%s' from %s", skillName, gitURL),
			"git is not installed or not on PATH.",
			"Install git (https://git-scm.com/) and retry.",
		)
	}

	baseDir := targetDir
	if baseDir == "" {
		baseDir = UserSkillsDir()
	}
	dest := filepath.Join(baseDir, skillName)
	if _, err := os.Stat(dest); err == nil {
		return "", NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Cannot install '%s'", skillName),
			fmt.Sprintf("Directory already exists: %s", dest),
			fmt.Sprintf("Run 'hive skill remove %s' first, or use a different --name.", skillName),
		)
	}

	tmpDir, err := makeTempDir()
	if err != nil {
		return "", err
	}
	defer func() { _ = removeAll(tmpDir) }()

	if err := cloneShallow(gitURL, tmpDir, version); err != nil {
		if skillErr := asSkillError(err); skillErr != nil {
			return "", skillErr
		}
		return "", installFailure(skillName, gitURL, err)
	}

	sourceDir, err := resolveInstallSource(tmpDir, subdirectory)
	if err != nil {
		return "", installFailure(skillName, gitURL, err)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "SKILL.md")); err != nil {
		label := subdirectory
		if label == "" {
			label = "/"
		}
		return "", NewSkillError(
			SkillNotFound,
			fmt.Sprintf("No SKILL.md found in '%s' of %s", label, gitURL),
			"The expected SKILL.md file is not present at the given path.",
			"Check the repository structure and use 'hive skill install --from <url>' with the correct subdirectory.",
		)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", installFailure(skillName, gitURL, err)
	}
	if err := copySkillDir(sourceDir, dest); err != nil {
		return "", installFailure(skillName, gitURL, err)
	}
	return dest, nil
}

func InstallFromRegistry(registryEntry map[string]any, targetDir, version string) (string, error) {
	name := registryEntryString(registryEntry, "name")
	gitURL := registryEntryString(registryEntry, "git_url")
	if name == "" || gitURL == "" {
		return "", NewSkillError(
			SkillNotFound,
			"Incomplete registry entry — missing 'name' or 'git_url'.",
			"The registry index entry does not contain all required fields.",
			"Report this issue to the registry maintainer.",
		)
	}

	resolvedVersion := version
	if resolvedVersion == "" {
		resolvedVersion = registryEntryString(registryEntry, "version")
	}
	subdirectory := registryEntryString(registryEntry, "subdirectory")

	return InstallFromGit(gitURL, name, subdirectory, resolvedVersion, targetDir)
}

func RemoveInstalledSkill(name, skillsDir string) (bool, error) {
	if !ValidSkillName(name) {
		return false, NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Invalid skill name '%s'", name),
			"The skill name must be a single path segment (no '/', '\\', NUL, '.' or '..').",
			"Pass the installed skill's directory name.",
		)
	}
	baseDir := skillsDir
	if baseDir == "" {
		baseDir = UserSkillsDir()
	}
	target := filepath.Join(baseDir, name)
	if _, err := os.Stat(target); err != nil {
		return false, nil
	}
	if err := removeAll(target); err != nil {
		return false, NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Failed to remove skill '%s' at %s", name, target),
			err.Error(),
			"Check file permissions and try again.",
		)
	}
	return true, nil
}

func ForkSkill(source *ParsedSkill, newName, targetDir string) (string, error) {
	if !ValidSkillName(newName) {
		return "", NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Invalid fork name '%s'", newName),
			"The skill name must be a single path segment (no '/', '\\', NUL, '.' or '..').",
			"Choose a safe --name and retry.",
		)
	}
	dest := filepath.Join(targetDir, newName)
	if _, err := os.Stat(dest); err == nil {
		return "", NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Cannot fork to '%s'", dest),
			"Target directory already exists.",
			fmt.Sprintf("Choose a different --name or remove '%s' first.", dest),
		)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", forkFailure(source, dest, err)
	}
	if err := copySkillDir(source.BaseDir, dest); err != nil {
		return "", forkFailure(source, dest, err)
	}

	forkedSkillMD := filepath.Join(dest, "SKILL.md")
	if _, err := os.Stat(forkedSkillMD); err == nil {
		rewriteNameInSkillMD(forkedSkillMD, newName)
	}
	return dest, nil
}

func installFailure(skillName, gitURL string, err error) error {
	return NewSkillError(
		SkillActivationFailed,
		fmt.Sprintf("Failed to install '%s' from %s", skillName, gitURL),
		err.Error(),
		"Check the URL, your network connection, and git configuration.",
	)
}

func forkFailure(source *ParsedSkill, dest string, err error) error {
	return NewSkillError(
		SkillActivationFailed,
		fmt.Sprintf("Failed to fork skill '%s' to '%s'", source.Name, dest),
		err.Error(),
		"Check file permissions and available disk space.",
	)
}

func asSkillError(err error) *SkillError {
	var skillErr *SkillError
	if errors.As(err, &skillErr) {
		return skillErr
	}
	return nil
}

func registryEntryString(entry map[string]any, key string) string {
	value, ok := entry[key]
	if !ok || value == nil {
		return ""
	}
	return pyStr(value)
}

// gitCloneArgv builds the clone argv. The `--` separator stops a repository
// URL (or a branch value) starting with '-' from being parsed as a git option
// such as --upload-pack, which would execute a command.
func gitCloneArgv(gitURL, target, version string) ([]string, error) {
	if gitURL == "" || strings.HasPrefix(strings.TrimSpace(gitURL), "-") || strings.ContainsRune(gitURL, 0) {
		return nil, fmt.Errorf("invalid git URL %q", gitURL)
	}
	if strings.HasPrefix(strings.TrimSpace(version), "-") || strings.ContainsRune(version, 0) {
		return nil, fmt.Errorf("invalid git ref %q", version)
	}
	args := []string{"clone", "--depth=1"}
	if version != "" {
		args = append(args, "--branch", version)
	}
	args = append(args, "--", gitURL, target)
	return args, nil
}

func gitCloneShallow(gitURL, target, version string) error {
	args, err := gitCloneArgv(gitURL, target, version)
	if err != nil {
		return NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("git clone rejected for %s", gitURL),
			err.Error(),
			"Use a repository URL or path that does not begin with '-'.",
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cloneInstallTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("git clone timed out for %s", gitURL),
			"The clone operation took longer than 60 seconds.",
			"Check your network connection and retry.",
		)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			why := strings.TrimSpace(stderr.String())
			if why == "" {
				why = fmt.Sprintf("git exited with code %d", exitErr.ExitCode())
			}
			return NewSkillError(
				SkillActivationFailed,
				fmt.Sprintf("git clone failed for %s", gitURL),
				why,
				"Check the URL is correct and the repository is publicly accessible.",
			)
		}
		return NewSkillError(
			SkillActivationFailed,
			fmt.Sprintf("Cannot run git for %s", gitURL),
			err.Error(),
			"Ensure git is installed and on PATH.",
		)
	}
	return nil
}

var copyIgnoreNames = map[string]bool{
	".git":         true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	"node_modules": true,
}

func ignoreCopyEntry(name string) bool {
	if copyIgnoreNames[name] {
		return true
	}
	return strings.HasSuffix(name, ".pyc")
}

func copySkillDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", src)
	}
	if err := os.Mkdir(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if ignoreCopyEntry(name) {
			continue
		}
		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)
		entryInfo, err := os.Stat(srcPath)
		if err != nil {
			return err
		}
		if entryInfo.IsDir() {
			if err := copySkillDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type: %s", srcPath)
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, entryInfo.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func rewriteNameInSkillMD(skillMD, newName string) {
	content, err := os.ReadFile(skillMD)
	if err != nil {
		return
	}
	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		return
	}
	frontmatter := map[string]any{}
	if err := yaml.Unmarshal([]byte(strings.TrimSpace(parts[1])), &frontmatter); err != nil {
		return
	}
	if frontmatter == nil {
		return
	}
	frontmatter["name"] = newName
	newYAML, err := yaml.Marshal(frontmatter)
	if err != nil {
		return
	}
	newContent := "---\n" + string(newYAML) + "---\n" + parts[2]
	_ = os.WriteFile(skillMD, []byte(newContent), 0o644)
}

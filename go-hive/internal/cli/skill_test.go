package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/skills"
)

func makeValidSkill(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: A test skill.\nlicense: MIT\n---\n\n## Body\n", name)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

func newSkillRunner(t *testing.T) (*Runner, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	runner.Stdin = strings.NewReader("")
	runner.StdinIsTTY = false
	runner.ShowInstallNotice = func() {}
	return runner, &stdout, &stderr
}

func skillArgs(values map[string]any, provided ...string) *parsedArgs {
	args := newArgs(values)
	for _, key := range provided {
		args.provided[key] = true
	}
	return args
}

type fakeRegistry struct {
	index   map[string]any
	results []map[string]any
	entries map[string]map[string]any
	packs   map[string][]string
}

func (f *fakeRegistry) FetchIndex(bool) map[string]any           { return f.index }
func (f *fakeRegistry) Search(string) []map[string]any           { return f.results }
func (f *fakeRegistry) GetSkillEntry(name string) map[string]any { return f.entries[name] }
func (f *fakeRegistry) GetPack(pack string) []string             { return f.packs[pack] }

type providerCall struct {
	Messages  []map[string]any
	System    string
	MaxTokens int
}

type recordingProvider struct {
	content string
	model   string
	err     error
	calls   []providerCall
}

func (p *recordingProvider) Complete(_ context.Context, messages []map[string]any, opts llm.CompleteOptions) (llm.Completion, error) {
	p.calls = append(p.calls, providerCall{Messages: messages, System: opts.System, MaxTokens: opts.MaxTokens})
	if p.err != nil {
		return llm.Completion{}, p.err
	}
	return llm.Completion{Content: p.content, Model: p.model}, nil
}

type fakeJudge struct {
	passes      bool
	explanation string
	calls       int
}

func (j *fakeJudge) Evaluate(string, string, string, string) map[string]any {
	j.calls++
	return map[string]any{"passes": j.passes, "explanation": j.explanation}
}

func decodeJSONOutput(t *testing.T, text string) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		t.Fatalf("invalid JSON output %q: %v", text, err)
	}
	return data
}

func testLicense(value string) *string { return &value }

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInit::test_creates_skill_md
func TestSkillInitCreatesSkillMD(t *testing.T) {
	tmp := t.TempDir()
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"skill_name": "test-skill", "target_dir": tmp})

	if result := runner.cmdSkillInit(args); result != 0 {
		t.Fatalf("cmdSkillInit = %d, want 0", result)
	}
	if _, err := os.Stat(filepath.Join(tmp, "test-skill", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInit::test_skill_md_contains_name
func TestSkillInitContentContainsName(t *testing.T) {
	tmp := t.TempDir()
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"skill_name": "my-skill", "target_dir": tmp})

	runner.cmdSkillInit(args)

	content, err := os.ReadFile(filepath.Join(tmp, "my-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(content), "name: my-skill") {
		t.Fatalf("SKILL.md missing 'name: my-skill':\n%s", content)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInit::test_error_when_dir_exists
func TestSkillInitErrorWhenDirExists(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, _, stderr := newSkillRunner(t)
	args := skillArgs(map[string]any{"skill_name": "existing", "target_dir": tmp})

	if result := runner.cmdSkillInit(args); result != 1 {
		t.Fatalf("cmdSkillInit = %d, want 1", result)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr = %q, want 'already exists'", stderr.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInit::test_error_when_no_name
func TestSkillInitErrorWhenNoName(t *testing.T) {
	tmp := t.TempDir()
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"skill_name": nil, "target_dir": tmp})

	if result := runner.cmdSkillInit(args); result != 1 {
		t.Fatalf("cmdSkillInit = %d, want 1", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillValidate::test_exits_0_on_valid_skill
func TestSkillValidateExits0OnValidSkill(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": filepath.Join(skillDir, "SKILL.md")})

	if result := runner.cmdSkillValidate(args); result != 0 {
		t.Fatalf("cmdSkillValidate = %d, want 0", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillValidate::test_accepts_directory_path
func TestSkillValidateAcceptsDirectoryPath(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": skillDir})

	if result := runner.cmdSkillValidate(args); result != 0 {
		t.Fatalf("cmdSkillValidate = %d, want 0", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillValidate::test_exits_1_on_invalid_skill
func TestSkillValidateExits1OnInvalidSkill(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "bad-skill")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, stdout, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": filepath.Join(skillDir, "SKILL.md")})

	if result := runner.cmdSkillValidate(args); result != 1 {
		t.Fatalf("cmdSkillValidate = %d, want 1", result)
	}
	if !strings.Contains(stdout.String(), "[ERROR]") {
		t.Fatalf("stdout = %q, want '[ERROR]'", stdout.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillValidate::test_shows_warnings_on_valid_skill_without_license
func TestSkillValidateShowsWarningsWithoutLicense(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "my-skill")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: my-skill\ndescription: No license.\n---\n\n## Body\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, stdout, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": filepath.Join(skillDir, "SKILL.md")})

	if result := runner.cmdSkillValidate(args); result != 0 {
		t.Fatalf("cmdSkillValidate = %d, want 0", result)
	}
	if !strings.Contains(stdout.String(), "[WARN]") {
		t.Fatalf("stdout = %q, want '[WARN]'", stdout.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillDoctor::test_defaults_pass_against_real_framework_skills
func TestSkillDoctorDefaultsPass(t *testing.T) {
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"defaults": true, "name": nil, "project_dir": nil})

	if result := runner.cmdSkillDoctor(args); result != 0 {
		t.Fatalf("cmdSkillDoctor(defaults) = %d, want 0", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillDoctor::test_named_skill_not_found_exits_1
func TestSkillDoctorNamedSkillNotFoundExits1(t *testing.T) {
	tmp := t.TempDir()
	runner, _, stderr := newSkillRunner(t)
	runner.Discover = func(string) []*skills.ParsedSkill { return nil }
	args := skillArgs(map[string]any{"name": "nonexistent-skill", "defaults": false, "project_dir": tmp})

	if result := runner.cmdSkillDoctor(args); result != 1 {
		t.Fatalf("cmdSkillDoctor = %d, want 1", result)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr = %q, want 'not found'", stderr.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillDoctor::test_healthy_skill_exits_0
func TestSkillDoctorHealthySkillExits0(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, _, _ := newSkillRunner(t)
	runner.Discover = func(string) []*skills.ParsedSkill {
		return []*skills.ParsedSkill{{
			Name:        "my-skill",
			Description: "Test.",
			Location:    filepath.Join(skillDir, "SKILL.md"),
			BaseDir:     skillDir,
			SourceScope: "user",
			Body:        "## Body",
		}}
	}
	args := skillArgs(map[string]any{"name": nil, "defaults": false, "project_dir": tmp})

	if result := runner.cmdSkillDoctor(args); result != 0 {
		t.Fatalf("cmdSkillDoctor = %d, want 0", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInstall::test_install_from_url_calls_install_from_git
func TestSkillInstallFromURLCallsInstallFromGit(t *testing.T) {
	tmp := t.TempDir()
	installedPath := filepath.Join(tmp, "skills", "my-skill")
	if err := os.MkdirAll(installedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	runner, _, _ := newSkillRunner(t)
	calls := 0
	var gotURL, gotName string
	runner.InstallFromGit = func(gitURL, skillName, subdirectory, version, targetDir string) (string, error) {
		calls++
		gotURL, gotName = gitURL, skillName
		return installedPath, nil
	}
	args := skillArgs(map[string]any{
		"name_or_url":  nil,
		"from_url":     "https://github.com/org/my-skill.git",
		"pack":         nil,
		"install_name": nil,
		"version":      nil,
	})

	if result := runner.cmdSkillInstall(args); result != 0 {
		t.Fatalf("cmdSkillInstall = %d, want 0", result)
	}
	if calls != 1 {
		t.Fatalf("InstallFromGit calls = %d, want 1", calls)
	}
	if gotURL != "https://github.com/org/my-skill.git" {
		t.Fatalf("git URL = %q", gotURL)
	}
	if gotName != "my-skill" {
		t.Fatalf("derived skill name = %q, want my-skill", gotName)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInstall::test_registry_not_found_exits_1
func TestSkillInstallRegistryNotFoundExits1(t *testing.T) {
	runner, _, stderr := newSkillRunner(t)
	runner.NewRegistry = func() registryClient { return &fakeRegistry{} }
	args := skillArgs(map[string]any{
		"name_or_url":  "nonexistent-skill",
		"from_url":     nil,
		"pack":         nil,
		"install_name": nil,
		"version":      nil,
	})

	if result := runner.cmdSkillInstall(args); result != 1 {
		t.Fatalf("cmdSkillInstall = %d, want 1", result)
	}
	if !strings.Contains(stderr.String(), "not found in registry") {
		t.Fatalf("stderr = %q, want 'not found in registry'", stderr.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInstall::test_no_args_exits_1
func TestSkillInstallNoArgsExits1(t *testing.T) {
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{
		"name_or_url":  nil,
		"from_url":     nil,
		"pack":         nil,
		"install_name": nil,
		"version":      nil,
	})

	if result := runner.cmdSkillInstall(args); result != 1 {
		t.Fatalf("cmdSkillInstall = %d, want 1", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillRemove::test_removes_installed_skill
func TestSkillRemoveRemovesInstalledSkill(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	runner.RemoveSkill = func(name, skillsDir string) (bool, error) { return true, nil }
	args := skillArgs(map[string]any{"name": "my-skill"})

	if result := runner.cmdSkillRemove(args); result != 0 {
		t.Fatalf("cmdSkillRemove = %d, want 0", result)
	}
	if !strings.Contains(stdout.String(), "Removed") {
		t.Fatalf("stdout = %q, want 'Removed'", stdout.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillRemove::test_exits_1_when_not_found
func TestSkillRemoveExits1WhenNotFound(t *testing.T) {
	runner, _, stderr := newSkillRunner(t)
	runner.RemoveSkill = func(name, skillsDir string) (bool, error) { return false, nil }
	args := skillArgs(map[string]any{"name": "missing-skill"})

	if result := runner.cmdSkillRemove(args); result != 1 {
		t.Fatalf("cmdSkillRemove = %d, want 1", result)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr = %q, want 'not found'", stderr.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillSearch::test_exits_1_when_registry_unavailable
func TestSkillSearchExits1WhenRegistryUnavailable(t *testing.T) {
	runner, _, stderr := newSkillRunner(t)
	runner.NewRegistry = func() registryClient { return &fakeRegistry{} }
	args := skillArgs(map[string]any{"query": "research"})

	if result := runner.cmdSkillSearch(args); result != 1 {
		t.Fatalf("cmdSkillSearch = %d, want 1", result)
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "registry unavailable") {
		t.Fatalf("stderr = %q, want 'registry unavailable'", stderr.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillSearch::test_prints_results_when_found
func TestSkillSearchPrintsResultsWhenFound(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	runner.NewRegistry = func() registryClient {
		return &fakeRegistry{
			index: map[string]any{"skills": []any{}},
			results: []map[string]any{{
				"name":        "deep-research",
				"description": "Multi-step research.",
				"tags":        []any{"research"},
				"trust_tier":  "official",
			}},
		}
	}
	args := skillArgs(map[string]any{"query": "research"})

	if result := runner.cmdSkillSearch(args); result != 0 {
		t.Fatalf("cmdSkillSearch = %d, want 0", result)
	}
	if !strings.Contains(stdout.String(), "deep-research") {
		t.Fatalf("stdout = %q, want 'deep-research'", stdout.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillSearch::test_no_results_message
func TestSkillSearchNoResultsMessage(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	runner.NewRegistry = func() registryClient {
		return &fakeRegistry{index: map[string]any{"skills": []any{}}}
	}
	args := skillArgs(map[string]any{"query": "xyzzy-nothing"})

	if result := runner.cmdSkillSearch(args); result != 0 {
		t.Fatalf("cmdSkillSearch = %d, want 0", result)
	}
	if !strings.Contains(stdout.String(), "No skills found") {
		t.Fatalf("stdout = %q, want 'No skills found'", stdout.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInfo::test_shows_locally_installed_skill
func TestSkillInfoShowsLocallyInstalledSkill(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, stdout, _ := newSkillRunner(t)
	runner.Discover = func(string) []*skills.ParsedSkill {
		return []*skills.ParsedSkill{{
			Name:        "my-skill",
			Description: "A test skill.",
			Location:    filepath.Join(skillDir, "SKILL.md"),
			BaseDir:     skillDir,
			SourceScope: "user",
			Body:        "## Body",
			License:     testLicense("MIT"),
		}}
	}
	args := skillArgs(map[string]any{"name": "my-skill", "project_dir": tmp})

	if result := runner.cmdSkillInfo(args); result != 0 {
		t.Fatalf("cmdSkillInfo = %d, want 0", result)
	}
	out := stdout.String()
	if !strings.Contains(out, "my-skill") || !strings.Contains(out, "A test skill.") {
		t.Fatalf("stdout = %q, want name and description", out)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInfo::test_falls_back_to_registry_when_not_installed
func TestSkillInfoFallsBackToRegistry(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	runner.Discover = func(string) []*skills.ParsedSkill { return nil }
	runner.NewRegistry = func() registryClient {
		return &fakeRegistry{entries: map[string]map[string]any{
			"deep-research": {
				"name":        "deep-research",
				"description": "Multi-step research.",
				"version":     "1.0.0",
				"author":      "anthropics",
				"trust_tier":  "official",
			},
		}}
	}
	args := skillArgs(map[string]any{"name": "deep-research", "project_dir": nil})

	if result := runner.cmdSkillInfo(args); result != 0 {
		t.Fatalf("cmdSkillInfo = %d, want 0", result)
	}
	out := stdout.String()
	if !strings.Contains(out, "not installed") || !strings.Contains(out, "deep-research") {
		t.Fatalf("stdout = %q, want 'not installed' and 'deep-research'", out)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillInfo::test_exits_1_when_not_found_anywhere
func TestSkillInfoExits1WhenNotFoundAnywhere(t *testing.T) {
	tmp := t.TempDir()
	runner, _, _ := newSkillRunner(t)
	runner.Discover = func(string) []*skills.ParsedSkill { return nil }
	runner.NewRegistry = func() registryClient { return &fakeRegistry{} }
	args := skillArgs(map[string]any{"name": "ghost-skill", "project_dir": tmp})

	if result := runner.cmdSkillInfo(args); result != 1 {
		t.Fatalf("cmdSkillInfo = %d, want 1", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestJsonFlag::test_list_json_produces_valid_json
func TestSkillListJSONProducesValidJSON(t *testing.T) {
	tmp := t.TempDir()
	runner, stdout, _ := newSkillRunner(t)
	runner.Discover = func(string) []*skills.ParsedSkill { return nil }
	args := skillArgs(map[string]any{"project_dir": tmp, "json": true})

	if result := runner.cmdSkillList(args); result != 0 {
		t.Fatalf("cmdSkillList = %d, want 0", result)
	}
	data := decodeJSONOutput(t, stdout.String())
	if _, ok := data["skills"]; !ok {
		t.Fatalf("missing 'skills': %v", data)
	}
	if _, ok := data["skills"].([]any); !ok {
		t.Fatalf("skills = %T, want array", data["skills"])
	}
}

// Parity: tests/test_skill_cli_commands.py::TestJsonFlag::test_validate_json_valid_skill
func TestSkillValidateJSONValidSkill(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, stdout, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": filepath.Join(skillDir, "SKILL.md"), "json": true})

	if result := runner.cmdSkillValidate(args); result != 0 {
		t.Fatalf("cmdSkillValidate = %d, want 0", result)
	}
	data := decodeJSONOutput(t, stdout.String())
	if data["passed"] != true {
		t.Fatalf("passed = %v, want true", data["passed"])
	}
	errorsList, ok := data["errors"].([]any)
	if !ok || len(errorsList) != 0 {
		t.Fatalf("errors = %v, want []", data["errors"])
	}
	if _, ok := data["warnings"]; !ok {
		t.Fatalf("missing 'warnings': %v", data)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestJsonFlag::test_doctor_defaults_json
func TestSkillDoctorDefaultsJSON(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"defaults": true, "name": nil, "project_dir": nil, "json": true})

	if result := runner.cmdSkillDoctor(args); result != 0 {
		t.Fatalf("cmdSkillDoctor = %d, want 0", result)
	}
	data := decodeJSONOutput(t, stdout.String())
	list, ok := data["skills"].([]any)
	if !ok || len(list) < 1 {
		t.Fatalf("skills = %v, want at least one", data["skills"])
	}
	if data["total_errors"].(float64) != 0 {
		t.Fatalf("total_errors = %v, want 0", data["total_errors"])
	}
}

// Parity: tests/test_skill_cli_commands.py::TestJsonFlag::test_search_json_registry_unavailable_exits_1
func TestSkillSearchJSONRegistryUnavailableExits1(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	runner.NewRegistry = func() registryClient { return &fakeRegistry{} }
	args := skillArgs(map[string]any{"query": "research", "json": true})

	if result := runner.cmdSkillSearch(args); result != 1 {
		t.Fatalf("cmdSkillSearch = %d, want 1", result)
	}
	data := decodeJSONOutput(t, stdout.String())
	if _, ok := data["error"]; !ok {
		t.Fatalf("missing 'error': %v", data)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestJsonFlag::test_remove_json_not_found_exits_1
func TestSkillRemoveJSONNotFoundExits1(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	runner.RemoveSkill = func(name, skillsDir string) (bool, error) { return false, nil }
	args := skillArgs(map[string]any{"name": "ghost-skill", "json": true})

	if result := runner.cmdSkillRemove(args); result != 1 {
		t.Fatalf("cmdSkillRemove = %d, want 1", result)
	}
	data := decodeJSONOutput(t, stdout.String())
	if _, ok := data["error"]; !ok {
		t.Fatalf("missing 'error': %v", data)
	}
}

func writeEvals(t *testing.T, skillDir, body string) {
	t.Helper()
	evalsDir := filepath.Join(skillDir, "evals")
	if err := os.MkdirAll(evalsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evalsDir, "evals.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_structural_only_valid_exits_0
func TestSkillTestStructuralOnlyValidExits0(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, _, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": skillDir, "input_json": nil, "model": nil, "json": false})

	if result := runner.cmdSkillTest(args); result != 0 {
		t.Fatalf("cmdSkillTest = %d, want 0", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_structural_invalid_exits_1
func TestSkillTestStructuralInvalidExits1(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "bad-skill")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner, stdout, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": skillDir, "input_json": nil, "model": nil, "json": false})

	if result := runner.cmdSkillTest(args); result != 1 {
		t.Fatalf("cmdSkillTest = %d, want 1", result)
	}
	if !strings.Contains(stdout.String(), "[ERROR]") {
		t.Fatalf("stdout = %q, want '[ERROR]'", stdout.String())
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_invocation_mode_calls_provider_with_skill_body
func TestSkillTestInvocationCallsProviderWithSkillBody(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, _, _ := newSkillRunner(t)
	provider := &recordingProvider{content: "Hello!", model: "claude-haiku-4-5-20251001"}
	runner.NewProvider = func(string) (completionProvider, error) { return provider, nil }
	args := skillArgs(
		map[string]any{"path": skillDir, "input_json": `{"prompt": "say hello"}`, "model": nil, "json": false},
		"input_json",
	)

	if result := runner.cmdSkillTest(args); result != 0 {
		t.Fatalf("cmdSkillTest = %d, want 0", result)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.calls))
	}
	if provider.calls[0].System != "## Body" {
		t.Fatalf("system = %q, want the skill body", provider.calls[0].System)
	}
	if provider.calls[0].MaxTokens != llm.ResolveAuxMaxTokens(0) {
		t.Fatalf("max_tokens = %d, want %d", provider.calls[0].MaxTokens, llm.ResolveAuxMaxTokens(0))
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_invocation_extracts_prompt_from_json
func TestSkillTestInvocationExtractsPromptFromJSON(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, _, _ := newSkillRunner(t)
	provider := &recordingProvider{content: "response", model: "claude-haiku-4-5-20251001"}
	runner.NewProvider = func(string) (completionProvider, error) { return provider, nil }
	args := skillArgs(
		map[string]any{"path": skillDir, "input_json": `{"prompt": "extracted prompt"}`, "model": nil, "json": false},
		"input_json",
	)

	runner.cmdSkillTest(args)

	if len(provider.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.calls))
	}
	messages := provider.calls[0].Messages
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(messages))
	}
	content, _ := messages[0]["content"].(string)
	if content != "extracted prompt" {
		t.Fatalf("message content = %q, want 'extracted prompt'", content)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_eval_suite_all_pass_exits_0
func TestSkillTestEvalSuiteAllPassExits0(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	writeEvals(t, skillDir, `{"skill_name": "my-skill", "evals": [{"id": 1, "prompt": "Say hi.", "assertions": ["Response is a greeting"]}]}`)
	runner, _, _ := newSkillRunner(t)
	provider := &recordingProvider{content: "Hello!", model: "claude-haiku-4-5-20251001"}
	runner.NewProvider = func(string) (completionProvider, error) { return provider, nil }
	judge := &fakeJudge{passes: true, explanation: "Looks good."}
	runner.NewJudge = func(completionProvider) judgeEvaluator { return judge }
	args := skillArgs(map[string]any{"path": skillDir, "input_json": nil, "model": nil, "json": false})

	if result := runner.cmdSkillTest(args); result != 0 {
		t.Fatalf("cmdSkillTest = %d, want 0", result)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.calls))
	}
	if judge.calls != 1 {
		t.Fatalf("judge calls = %d, want 1", judge.calls)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_eval_any_fail_exits_1
func TestSkillTestEvalAnyFailExits1(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	writeEvals(t, skillDir, `{"skill_name": "my-skill", "evals": [{"id": 1, "prompt": "Say hi.", "assertions": ["Impossible assertion"]}]}`)
	runner, _, _ := newSkillRunner(t)
	provider := &recordingProvider{content: "Hello!", model: "claude-haiku-4-5-20251001"}
	runner.NewProvider = func(string) (completionProvider, error) { return provider, nil }
	judge := &fakeJudge{passes: false, explanation: "Did not satisfy."}
	runner.NewJudge = func(completionProvider) judgeEvaluator { return judge }
	args := skillArgs(map[string]any{"path": skillDir, "input_json": nil, "model": nil, "json": false})

	if result := runner.cmdSkillTest(args); result != 1 {
		t.Fatalf("cmdSkillTest = %d, want 1", result)
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_json_flag_structural_output
func TestSkillTestJSONFlagStructuralOutput(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	runner, stdout, _ := newSkillRunner(t)
	args := skillArgs(map[string]any{"path": skillDir, "input_json": nil, "model": nil, "json": true})

	if result := runner.cmdSkillTest(args); result != 0 {
		t.Fatalf("cmdSkillTest = %d, want 0", result)
	}
	data := decodeJSONOutput(t, stdout.String())
	if _, ok := data["structural"]; !ok {
		t.Fatalf("missing 'structural': %v", data)
	}
	structural, ok := data["structural"].(map[string]any)
	if !ok || structural["passed"] != true {
		t.Fatalf("structural = %v, want passed true", data["structural"])
	}
	if data["skill"] != "my-skill" {
		t.Fatalf("skill = %v, want my-skill", data["skill"])
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_json_flag_eval_results
func TestSkillTestJSONFlagEvalResults(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	writeEvals(t, skillDir, `{"skill_name": "my-skill", "evals": [{"id": 1, "prompt": "Hi.", "assertions": ["Is a greeting"]}]}`)
	runner, stdout, _ := newSkillRunner(t)
	provider := &recordingProvider{content: "Hello!", model: "claude-haiku-4-5-20251001"}
	runner.NewProvider = func(string) (completionProvider, error) { return provider, nil }
	runner.NewJudge = func(completionProvider) judgeEvaluator {
		return &fakeJudge{passes: true, explanation: "Yes."}
	}
	args := skillArgs(map[string]any{"path": skillDir, "input_json": nil, "model": nil, "json": true})

	if result := runner.cmdSkillTest(args); result != 0 {
		t.Fatalf("cmdSkillTest = %d, want 0", result)
	}
	data := decodeJSONOutput(t, stdout.String())
	if _, ok := data["evals"]; !ok {
		t.Fatalf("missing 'evals': %v", data)
	}
	if data["total_passed"].(float64) != 1 {
		t.Fatalf("total_passed = %v, want 1", data["total_passed"])
	}
	if data["total_failed"].(float64) != 0 {
		t.Fatalf("total_failed = %v, want 0", data["total_failed"])
	}
}

// Parity: tests/test_skill_cli_commands.py::TestCmdSkillTest::test_no_api_key_with_evals_degrades_gracefully
func TestSkillTestNoAPIKeyWithEvalsDegradesGracefully(t *testing.T) {
	tmp := t.TempDir()
	skillDir := makeValidSkill(t, tmp, "my-skill")
	writeEvals(t, skillDir, `{"skill_name": "my-skill", "evals": []}`)
	runner, _, stderr := newSkillRunner(t)
	runner.NewProvider = func(string) (completionProvider, error) {
		return nil, errors.New("ANTHROPIC_API_KEY not set")
	}
	args := skillArgs(map[string]any{"path": skillDir, "input_json": nil, "model": nil, "json": false})

	if result := runner.cmdSkillTest(args); result != 0 {
		t.Fatalf("cmdSkillTest = %d, want 0", result)
	}
	if !strings.Contains(stderr.String(), "ANTHROPIC_API_KEY") {
		t.Fatalf("stderr = %q, want 'ANTHROPIC_API_KEY'", stderr.String())
	}
}

// Parity-exempt: _prompt_yes_no has no dedicated Python test; this pins its
func TestPromptYesNoNonInteractive(t *testing.T) {
	runner, stdout, _ := newSkillRunner(t)
	runner.Stdin = strings.NewReader("yes\n")

	if runner.promptYesNo("Fork? [y/N] ") {
		t.Fatal("promptYesNo(non-tty) = true, want false")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no prompt on a non-tty", stdout.String())
	}
}

// Parity-exempt: cmd_skill_fork has no Python test; this pins the abort path
func TestSkillForkNonInteractiveAborts(t *testing.T) {
	tmp := t.TempDir()
	source := makeValidSkill(t, filepath.Join(tmp, "project"), "my-skill")
	runner, stdout, _ := newSkillRunner(t)
	runner.Discover = func(string) []*skills.ParsedSkill {
		return []*skills.ParsedSkill{{Name: "my-skill", Location: filepath.Join(source, "SKILL.md")}}
	}
	target := filepath.Join(tmp, "target")
	args := skillArgs(map[string]any{"name": "my-skill", "new_name": "", "target_dir": target})

	if result := runner.cmdSkillFork(args); result != 0 {
		t.Fatalf("cmdSkillFork = %d, want 0", result)
	}
	if !strings.Contains(stdout.String(), "Aborted.") {
		t.Fatalf("stdout = %q, want 'Aborted.'", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(target, "my-skill-fork")); !os.IsNotExist(err) {
		t.Fatalf("fork directory was created despite abort: %v", err)
	}
}

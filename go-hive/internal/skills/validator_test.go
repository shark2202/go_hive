package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeValidationSkill(t *testing.T, root, dirName, content string) string {
	t.Helper()
	skillDir := filepath.Join(root, dirName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validValidationContent = `---
name: my-skill
description: A test skill for validation.
version: 0.1.0
license: MIT
compatibility:
  - claude-code
  - hive
metadata:
  tags: []
---

## Instructions

Do the thing properly.
`

func hasSubstring(items []string, needle string) bool {
	for _, item := range items {
		if strings.Contains(strings.ToLower(item), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// Parity: tests/test_skill_validator.py::TestHappyPath::test_valid_skill_passes
func TestValidateStrictHappyPath(t *testing.T) {
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", validValidationContent))
	if !result.Passed || len(result.Errors) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestHappyPath::test_namespace_prefix_name_allowed
func TestValidateStrictNamespacePrefixAllowed(t *testing.T) {
	content := "---\nname: hive.my-skill\ndescription: A namespaced skill.\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if !result.Passed {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestHappyPath::test_warning_on_missing_license
func TestValidateStrictMissingLicenseWarns(t *testing.T) {
	content := "---\nname: my-skill\ndescription: No license field.\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if !result.Passed {
		t.Fatalf("result = %+v", result)
	}
	if !hasSubstring(result.Warnings, "license") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck1FileExists::test_error_on_missing_file
func TestValidateStrictMissingFile(t *testing.T) {
	result := ValidateStrict(filepath.Join(t.TempDir(), "nonexistent", "SKILL.md"))
	if result.Passed || !hasSubstring(result.Errors, "not found") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck2FileNotEmpty::test_error_on_empty_file
func TestValidateStrictEmptyFile(t *testing.T) {
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", "   \n"))
	if result.Passed || !hasSubstring(result.Errors, "empty") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck3FrontmatterPresent::test_error_on_missing_delimiters
func TestValidateStrictMissingDelimiters(t *testing.T) {
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", "name: my-skill\ndescription: no delimiters\n"))
	if result.Passed || !(hasSubstring(result.Errors, "frontmatter") || hasSubstring(result.Errors, "---")) {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck4YamlNoFixup::test_error_on_yaml_requiring_fixup
func TestValidateStrictYAMLNeedingFixup(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Use for: research tasks\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed || !(hasSubstring(result.Errors, "YAML") || hasSubstring(result.Errors, "parse")) {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck4YamlNoFixup::test_quoted_colon_passes
func TestValidateStrictQuotedColonPasses(t *testing.T) {
	content := "---\nname: my-skill\ndescription: \"Use for: research tasks\"\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if !result.Passed {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck5Description::test_error_on_missing_description
func TestValidateStrictMissingDescription(t *testing.T) {
	content := "---\nname: my-skill\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed || !hasSubstring(result.Errors, "description") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck5Description::test_error_on_empty_description
func TestValidateStrictEmptyDescription(t *testing.T) {
	content := "---\nname: my-skill\ndescription: \"\"\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck6NamePresent::test_error_on_missing_name
func TestValidateStrictMissingName(t *testing.T) {
	content := "---\ndescription: A skill without a name.\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed || !hasSubstring(result.Errors, "name") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck7NameLength::test_error_on_name_too_long
func TestValidateStrictNameTooLong(t *testing.T) {
	longName := strings.Repeat("a", 65)
	content := "---\nname: " + longName + "\ndescription: Too long.\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), longName, content))
	if result.Passed || !(hasSubstring(result.Errors, "64") || hasSubstring(result.Errors, "characters")) {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck7NameLength::test_exactly_64_chars_passes
func TestValidateStrictNameExactly64(t *testing.T) {
	name := strings.Repeat("a", 64)
	content := "---\nname: " + name + "\ndescription: Exactly 64.\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), name, content))
	if hasSubstring(result.Errors, "64") || hasSubstring(result.Errors, "characters") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck8NameDirectoryMatch::test_error_on_name_dir_mismatch
func TestValidateStrictNameDirMismatch(t *testing.T) {
	content := "---\nname: other-skill\ndescription: Wrong name.\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed || !(hasSubstring(result.Errors, "other-skill") || hasSubstring(result.Errors, "my-skill")) {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck8NameDirectoryMatch::test_exact_match_passes
func TestValidateStrictExactMatch(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Exact match.\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if !result.Passed {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck8NameDirectoryMatch::test_dot_namespace_prefix_passes
func TestValidateStrictDotNamespacePrefix(t *testing.T) {
	content := "---\nname: org.my-skill\ndescription: Namespaced.\nlicense: MIT\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	for _, err := range result.Errors {
		if strings.Contains(err, "my-skill") && strings.Contains(err, "org.my-skill") {
			t.Fatalf("namespace prefix flagged: %v", result.Errors)
		}
	}
}

// Parity: tests/test_skill_validator.py::TestCheck9BodyNotEmpty::test_error_on_empty_body
func TestValidateStrictEmptyBody(t *testing.T) {
	content := "---\nname: my-skill\ndescription: No body.\nlicense: MIT\n---\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed || !(hasSubstring(result.Errors, "body") || hasSubstring(result.Errors, "instructions")) {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck11Scripts::test_error_on_non_executable_script
func TestValidateStrictNonExecutableScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX executable bits")
	}
	path := writeValidationSkill(t, t.TempDir(), "my-skill", validValidationContent)
	scriptsDir := filepath.Join(filepath.Dir(path), "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scriptsDir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := ValidateStrict(path)
	if result.Passed || !hasSubstring(result.Errors, "executable") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck11Scripts::test_passes_with_executable_script
func TestValidateStrictExecutableScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX executable bits")
	}
	path := writeValidationSkill(t, t.TempDir(), "my-skill", validValidationContent)
	scriptsDir := filepath.Join(filepath.Dir(path), "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "run.sh"), []byte("#!/bin/sh\necho hi"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := ValidateStrict(path)
	if !result.Passed {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck12AllowedTools::test_warning_on_malformed_allowed_tools
func TestValidateStrictMalformedAllowedTools(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Skill with bad tools.\nlicense: MIT\nallowed-tools: \"not a list\"\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if !hasSubstring(result.Warnings, "allowed-tools") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck12AllowedTools::test_valid_allowed_tools_no_warning
func TestValidateStrictValidAllowedTools(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Valid tools list.\nlicense: MIT\nallowed-tools:\n  - web_search\n  - file_read\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if hasSubstring(result.Warnings, "allowed-tools") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck13Compatibility::test_error_on_non_list_compatibility
func TestValidateStrictNonListCompatibility(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Bad compat.\nlicense: MIT\ncompatibility: \"claude-code\"\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed || !hasSubstring(result.Errors, "compatibility") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck13Compatibility::test_valid_compatibility_passes
func TestValidateStrictValidCompatibility(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Good compat.\nlicense: MIT\ncompatibility:\n  - claude-code\n  - hive\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if !result.Passed {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck14Metadata::test_error_on_non_dict_metadata
func TestValidateStrictNonDictMetadata(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Bad metadata.\nlicense: MIT\nmetadata: \"not a dict\"\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if result.Passed || !hasSubstring(result.Errors, "metadata") {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_validator.py::TestCheck14Metadata::test_valid_metadata_passes
func TestValidateStrictValidMetadata(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Good metadata.\nlicense: MIT\nmetadata:\n  tags:\n    - research\n---\n\n## Body\n"
	result := ValidateStrict(writeValidationSkill(t, t.TempDir(), "my-skill", content))
	if !result.Passed {
		t.Fatalf("result = %+v", result)
	}
}

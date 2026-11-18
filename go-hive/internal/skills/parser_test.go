package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillMD(t *testing.T, root, dirName, content string) string {
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

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_happy_path
func TestParseSkillMDHappyPath(t *testing.T) {
	content := `---
name: my-skill
description: A test skill for unit testing.
license: MIT
---

## Instructions

Do the thing.
`
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "my-skill", content), "project")
	if result == nil {
		t.Fatal("valid skill rejected")
	}
	if result.Name != "my-skill" {
		t.Fatalf("name = %q", result.Name)
	}
	if result.Description != "A test skill for unit testing." {
		t.Fatalf("description = %q", result.Description)
	}
	if result.License == nil || *result.License != "MIT" {
		t.Fatalf("license = %v", result.License)
	}
	if result.SourceScope != "project" {
		t.Fatalf("source scope = %q", result.SourceScope)
	}
	if !strings.Contains(result.Body, "Do the thing.") {
		t.Fatalf("body = %q", result.Body)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_missing_description_returns_none
func TestParseSkillMDMissingDescription(t *testing.T) {
	content := "---\nname: no-desc\n---\n\nBody here.\n"
	if result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "no-desc", content), "project"); result != nil {
		t.Fatalf("missing description accepted: %+v", result)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_missing_name_uses_directory
func TestParseSkillMDMissingNameUsesDirectory(t *testing.T) {
	content := "---\ndescription: Skill without a name field.\n---\n\nBody.\n"
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "fallback-dir", content), "project")
	if result == nil || result.Name != "fallback-dir" {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_empty_file_returns_none
func TestParseSkillMDEmptyFile(t *testing.T) {
	if result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "empty", ""), "project"); result != nil {
		t.Fatalf("empty file accepted: %+v", result)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_no_frontmatter_delimiters_returns_none
func TestParseSkillMDNoFrontmatter(t *testing.T) {
	content := "Just plain text without YAML frontmatter."
	if result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "no-yaml", content), "project"); result != nil {
		t.Fatalf("plain text accepted: %+v", result)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_unparseable_yaml_returns_none
func TestParseSkillMDUnparseableYAML(t *testing.T) {
	content := "---\nname: [invalid yaml\n  - broken: {{\n---\n\nBody.\n"
	if result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "bad-yaml", content), "project"); result != nil {
		t.Fatalf("broken YAML accepted: %+v", result)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_unquoted_colon_fixup
func TestParseSkillMDUnquotedColonFixup(t *testing.T) {
	content := "---\nname: colon-test\ndescription: Use for: research tasks\n---\n\nBody.\n"
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "colon-test", content), "project")
	if result == nil {
		t.Fatal("fixup path rejected")
	}
	if !strings.Contains(result.Description, "research tasks") {
		t.Fatalf("description = %q", result.Description)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_long_name_warns_but_loads
func TestParseSkillMDLongNameLoads(t *testing.T) {
	longName := strings.Repeat("a", 100)
	content := "---\nname: " + longName + "\ndescription: A skill with an excessively long name.\n---\n\nBody.\n"
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "long-name", content), "project")
	if result == nil || result.Name != longName {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_name_mismatch_warns_but_loads
func TestParseSkillMDNameMismatchLoads(t *testing.T) {
	content := "---\nname: different-name\ndescription: Name doesn't match directory.\n---\n\nBody.\n"
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "actual-dir", content), "project")
	if result == nil || result.Name != "different-name" {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_optional_fields
func TestParseSkillMDOptionalFields(t *testing.T) {
	content := `---
name: full-skill
description: Skill with all optional fields.
license: Apache-2.0
compatibility:
  - claude-code
  - cursor
metadata:
  author: tester
  version: "1.0"
allowed-tools:
  - web_search
  - read_file
---

Instructions here.
`
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "full-skill", content), "project")
	if result == nil {
		t.Fatal("skill rejected")
	}
	if result.License == nil || *result.License != "Apache-2.0" {
		t.Fatalf("license = %v", result.License)
	}
	if strings.Join(result.Compatibility, ",") != "claude-code,cursor" {
		t.Fatalf("compatibility = %v", result.Compatibility)
	}
	if result.Metadata["author"] != "tester" || result.Metadata["version"] != "1.0" {
		t.Fatalf("metadata = %v", result.Metadata)
	}
	if strings.Join(result.AllowedTools, ",") != "web_search,read_file" {
		t.Fatalf("allowed tools = %v", result.AllowedTools)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_body_extraction
func TestParseSkillMDBodyExtraction(t *testing.T) {
	content := `---
name: body-test
description: Test body extraction.
---

## Step 1

Do this first.

## Step 2

Then do this.
`
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "body-test", content), "project")
	if result == nil {
		t.Fatal("skill rejected")
	}
	for _, want := range []string{"## Step 1", "## Step 2", "Do this first."} {
		if !strings.Contains(result.Body, want) {
			t.Fatalf("body %q missing %q", result.Body, want)
		}
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_location_is_absolute
func TestParseSkillMDLocationIsAbsolute(t *testing.T) {
	content := "---\nname: abs-path\ndescription: Check absolute path.\n---\n\nBody.\n"
	path := writeSkillMD(t, t.TempDir(), "abs-path", content)
	result := ParseSkillMD(path, "project")
	if result == nil {
		t.Fatal("skill rejected")
	}
	if !filepath.IsAbs(result.Location) || !filepath.IsAbs(result.BaseDir) {
		t.Fatalf("location = %q, base = %q", result.Location, result.BaseDir)
	}
}

// Parity: tests/test_skill_parser.py::TestParseSkillMd::test_nonexistent_file_returns_none
func TestParseSkillMDNonexistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "SKILL.md")
	if result := ParseSkillMD(path, "project"); result != nil {
		t.Fatalf("nonexistent file accepted: %+v", result)
	}
}

// Parity-exempt: test_skill_parser.py never parses metadata.visibility, so this
func TestParseSkillMDVisibility(t *testing.T) {
	content := "---\nname: vis\ndescription: visibility test\nmetadata:\n  visibility:\n    - planning\n    - building\n---\n\nBody.\n"
	result := ParseSkillMD(writeSkillMD(t, t.TempDir(), "vis", content), "framework")
	if result == nil || strings.Join(result.Visibility, ",") != "planning,building" {
		t.Fatalf("visibility = %+v", result)
	}
}

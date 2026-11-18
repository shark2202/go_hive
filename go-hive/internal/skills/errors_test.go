package skills

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity: tests/test_skill_errors.py::TestSkillErrorCode::test_all_codes_defined
func TestSkillErrorCodesDefined(t *testing.T) {
	codes := []SkillErrorCode{
		SkillNotFound, SkillParseError, SkillActivationFailed, SkillMissingDescription,
		SkillYAMLFixup, SkillNameMismatch, SkillCollision,
	}
	for _, want := range []string{
		"SKILL_NOT_FOUND", "SKILL_PARSE_ERROR", "SKILL_ACTIVATION_FAILED",
		"SKILL_MISSING_DESCRIPTION", "SKILL_YAML_FIXUP", "SKILL_NAME_MISMATCH", "SKILL_COLLISION",
	} {
		found := false
		for _, code := range codes {
			if string(code) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing code %q", want)
		}
	}
}

// Parity: tests/test_skill_errors.py::TestSkillError::test_message_format
func TestSkillErrorMessageFormat(t *testing.T) {
	err := NewSkillError(SkillMissingDescription,
		"Missing description in '/path/SKILL.md'",
		"The description field is absent",
		"Add a description field to the frontmatter")
	want := "[SKILL_MISSING_DESCRIPTION]\n" +
		"What failed: Missing description in '/path/SKILL.md'\n" +
		"Why: The description field is absent\n" +
		"Fix: Add a description field to the frontmatter"
	if err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
}

// Parity: tests/test_skill_errors.py::TestSkillError::test_what_why_fix_attributes
func TestSkillErrorAttributes(t *testing.T) {
	err := NewSkillError(SkillCollision, "Name collision", "Two skills share the same name", "Rename one skill directory")
	if err.Code != SkillCollision || err.What != "Name collision" ||
		err.Why != "Two skills share the same name" || err.Fix != "Rename one skill directory" {
		t.Fatalf("error = %+v", err)
	}
	var asError error = err
	if asError == nil {
		t.Fatal("SkillError does not satisfy error")
	}
}

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	fn()
	return buf.String()
}

// Parity: tests/test_skill_errors.py::TestLogSkillError::test_emits_log
func TestLogSkillErrorEmits(t *testing.T) {
	out := captureLog(t, func() {
		LogSkillError("error", SkillParseError, "Invalid SKILL.md at '/path'", "Empty file", "Add content")
	})
	if !strings.Contains(out, "SKILL_PARSE_ERROR") {
		t.Fatalf("log = %q", out)
	}
}

// Parity: tests/test_skill_errors.py::TestLogSkillError::test_warning_level
func TestLogSkillErrorWarningLevel(t *testing.T) {
	out := captureLog(t, func() {
		LogSkillError("warning", SkillYAMLFixup, "Auto-fixed YAML", "Unquoted colons", "Quote values")
	})
	if !strings.Contains(out, "SKILL_YAML_FIXUP") {
		t.Fatalf("log = %q", out)
	}
}

// Parity: tests/test_skill_errors.py::TestLogSkillError::test_message_contains_all_parts
func TestLogSkillErrorMessageParts(t *testing.T) {
	out := captureLog(t, func() {
		LogSkillError("error", SkillNotFound, "Skill not found", "Not discovered", "Check paths")
	})
	for _, want := range []string{"Skill not found", "Not discovered", "Check paths"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log %q missing %q", out, want)
		}
	}
}

// Parity: tests/test_skill_errors.py::TestSkillErrorInParser::test_yaml_fixup_still_parses
func TestSkillErrorsYAMLFixupStillParses(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "colon-test")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: colon-test\ndescription: Use for: research\n---\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := ParseSkillMD(path, "project")
	if result == nil || !strings.Contains(result.Description, "research") {
		t.Fatalf("result = %+v", result)
	}
}

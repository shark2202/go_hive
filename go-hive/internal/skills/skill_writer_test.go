package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity: tests/test_skill_overrides.py::TestAuthoring::test_write_and_remove
func TestAuthoringWriteAndRemove(t *testing.T) {
	tmp := t.TempDir()
	draft, errMsg := BuildSkillDraft("demo", "A demo skill", "## Steps\n1. Do it.\n", []any{
		map[string]any{"path": "notes.md", "content": "notes"},
	})
	if errMsg != "" {
		t.Fatalf("build draft: %s", errMsg)
	}
	if draft == nil {
		t.Fatal("draft is nil")
	}

	installed, werr, replaced := WriteSkill(draft, tmp, true)
	if werr != "" {
		t.Fatalf("write skill: %s", werr)
	}
	if installed == "" {
		t.Fatal("installed path is empty")
	}
	if replaced {
		t.Fatal("first write should not report replaced")
	}
	if _, err := os.Stat(filepath.Join(installed, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	notes, err := os.ReadFile(filepath.Join(installed, "notes.md"))
	if err != nil {
		t.Fatalf("notes.md missing: %v", err)
	}
	if string(notes) != "notes" {
		t.Fatalf("notes.md = %q", notes)
	}

	removed, rerr := RemoveSkill(tmp, "demo")
	if rerr != "" {
		t.Fatalf("remove skill: %s", rerr)
	}
	if !removed {
		t.Fatal("remove should report removed")
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("skill dir still present: %v", err)
	}
	again, rerr := RemoveSkill(tmp, "demo")
	if rerr != "" || again {
		t.Fatalf("second remove = (%v, %q)", again, rerr)
	}
}

// Parity: tests/test_skill_overrides.py::TestAuthoring::test_reject_absolute_path
func TestAuthoringRejectAbsolutePath(t *testing.T) {
	_, errMsg := BuildSkillDraft("demo", "desc", "body", []any{
		map[string]any{"path": "/etc/passwd", "content": "oops"},
	})
	if errMsg == "" {
		t.Fatal("absolute path should be rejected")
	}
	if !strings.Contains(errMsg, "relative") {
		t.Fatalf("error = %q, want 'relative'", errMsg)
	}
}

// Parity: tests/test_skill_overrides.py::TestAuthoring::test_reject_traversal
func TestAuthoringRejectTraversal(t *testing.T) {
	_, errMsg := BuildSkillDraft("demo", "desc", "body", []any{
		map[string]any{"path": "../escape.sh", "content": "oops"},
	})
	if errMsg == "" {
		t.Fatal("traversal path should be rejected")
	}
}

// Parity: tests/test_skill_overrides.py::TestAuthoring::test_reject_invalid_name
func TestAuthoringRejectInvalidName(t *testing.T) {
	_, errMsg := BuildSkillDraft("Demo_Skill", "desc", "body", nil)
	if errMsg == "" {
		t.Fatal("invalid name should be rejected")
	}
}

// Parity-exempt: exact per-rule skill-name error strings have no pytest; the
func TestValidateSkillNameMessages(t *testing.T) {
	long := strings.Repeat("a", 65)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "   ", "skill_name is required"},
		{"pattern", "Demo_Skill", "skill_name 'Demo_Skill' must match [a-z0-9-] pattern"},
		{"leading hyphen", "-demo", "skill_name '-demo' has leading/trailing/consecutive hyphens"},
		{"trailing hyphen", "demo-", "skill_name 'demo-' has leading/trailing/consecutive hyphens"},
		{"consecutive hyphens", "a--b", "skill_name 'a--b' has leading/trailing/consecutive hyphens"},
		{"too long", long, "skill_name '" + long + "' exceeds 64 chars"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, errMsg := ValidateSkillName(tc.in); errMsg != tc.want {
				t.Fatalf("error = %q, want %q", errMsg, tc.want)
			}
		})
	}

	for _, ok := range []string{"demo", "hive.writing-hive-skills", "a.b-c.d"} {
		name, errMsg := ValidateSkillName(ok)
		if errMsg != "" || name != ok {
			t.Fatalf("ValidateSkillName(%q) = (%q, %q)", ok, name, errMsg)
		}
	}
}

// Parity-exempt: exact description-validation messages have no pytest; the
func TestValidateDescriptionMessages(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "  ", "skill_description is required"},
		{"too long", strings.Repeat("x", 1025), "skill_description must be 1–1024 chars"},
		{"newline", "line one\nline two", "skill_description must be a single line (no newlines)"},
		{"carriage return", "line one\rline two", "skill_description must be a single line (no newlines)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, errMsg := ValidateDescription(tc.in); errMsg != tc.want {
				t.Fatalf("error = %q, want %q", errMsg, tc.want)
			}
		})
	}
	desc, errMsg := ValidateDescription("  A demo skill  ")
	if errMsg != "" || desc != "A demo skill" {
		t.Fatalf("ValidateDescription = (%q, %q)", desc, errMsg)
	}
}

// Parity-exempt: skill_files validation messages and path rules are Go helper
func TestValidateSkillFilesRejects(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"not an array", map[string]any{"path": "a"}, "skill_files must be an array"},
		{"entry not object", []any{"notes.md"},
			"each skill_files entry must be an object with 'path' and 'content'"},
		{"missing path", []any{map[string]any{"content": "x"}},
			"skill_files entry missing non-empty 'path'"},
		{"empty path", []any{map[string]any{"path": "   ", "content": "x"}},
			"skill_files entry missing non-empty 'path'"},
		{"non-string path", []any{map[string]any{"path": 7, "content": "x"}},
			"skill_files entry missing non-empty 'path'"},
		{"missing content", []any{map[string]any{"path": "notes.md"}},
			"skill_files entry 'notes.md' missing string 'content'"},
		{"non-string content", []any{map[string]any{"path": "notes.md", "content": 7}},
			"skill_files entry 'notes.md' missing string 'content'"},
		{"absolute", []any{map[string]any{"path": "/etc/passwd", "content": "x"}},
			"skill_files path '/etc/passwd' must be relative and inside the skill folder"},
		{"windows absolute", []any{map[string]any{"path": "C:\\evil.exe", "content": "x"}},
			"skill_files path 'C:\\evil.exe' must be relative and inside the skill folder"},
		{"traversal", []any{map[string]any{"path": "a/../../b", "content": "x"}},
			"skill_files path 'a/../../b' must be relative and inside the skill folder"},
		{"skill md", []any{map[string]any{"path": "SKILL.md", "content": "x"}},
			"skill_files must not contain SKILL.md — pass skill_body instead"},
		{"dotted skill md", []any{map[string]any{"path": "./SKILL.md", "content": "x"}},
			"skill_files must not contain SKILL.md — pass skill_body instead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, errMsg := ValidateSkillFiles(tc.in); errMsg != tc.want {
				t.Fatalf("error = %q, want %q", errMsg, tc.want)
			}
		})
	}

	if files, errMsg := ValidateSkillFiles(nil); errMsg != "" || len(files) != 0 {
		t.Fatalf("nil files = (%+v, %q)", files, errMsg)
	}
}

// Parity-exempt: Go-only validation of falsy skill-file values; no upstream
// test_skill_writer.py exists in this tree.
func TestValidateSkillFilesTreatsFalsyAsEmpty(t *testing.T) {
	for _, input := range []any{"", 0.0, false, map[string]any{}} {
		files, errMsg := ValidateSkillFiles(input)
		if errMsg != "" || len(files) != 0 {
			t.Fatalf("ValidateSkillFiles(%#v) = (%+v, %q)", input, files, errMsg)
		}
	}
	for _, input := range []any{"notes.md", 7.0, true, map[string]any{"path": "a"}} {
		if _, errMsg := ValidateSkillFiles(input); errMsg != "skill_files must be an array" {
			t.Fatalf("ValidateSkillFiles(%#v) error = %q", input, errMsg)
		}
	}
}

// Parity-exempt: stripping a leading ./ from skill_files paths has no Python
func TestValidateSkillFilesStripsDotSlash(t *testing.T) {
	files, errMsg := ValidateSkillFiles([]any{
		map[string]any{"path": " ./notes.md ", "content": "notes"},
	})
	if errMsg != "" {
		t.Fatalf("error = %q", errMsg)
	}
	if len(files) != 1 || files[0].RelPath != "notes.md" || files[0].Content != "notes" {
		t.Fatalf("files = %+v", files)
	}
}

// Parity-exempt: the empty-skill_body guard lives in the Go draft builder;
func TestBuildSkillDraftRequiresBody(t *testing.T) {
	_, errMsg := BuildSkillDraft("demo", "desc", " \n\t ", nil)
	if !strings.Contains(errMsg, "skill_body is required") {
		t.Fatalf("error = %q", errMsg)
	}
}

// Parity-exempt: the exact rendered SKILL.md text (frontmatter + trailing newline
func TestSkillDraftSkillMDText(t *testing.T) {
	draft, errMsg := BuildSkillDraft("demo", "A demo skill", "## Steps\n1. Do it.\n\n\n  \n", nil)
	if errMsg != "" {
		t.Fatalf("error = %q", errMsg)
	}
	want := "---\nname: demo\ndescription: A demo skill\n---\n\n## Steps\n1. Do it.\n"
	if got := draft.SkillMDText(); got != want {
		t.Fatalf("SkillMDText = %q, want %q", got, want)
	}
}

// Parity-exempt: the "already exists" refusal without replace is a Go writer
func TestWriteSkillRefusesWithoutReplace(t *testing.T) {
	tmp := t.TempDir()
	draft, errMsg := BuildSkillDraft("demo", "desc", "body", nil)
	if errMsg != "" {
		t.Fatalf("error = %q", errMsg)
	}
	if _, werr, _ := WriteSkill(draft, tmp, false); werr != "" {
		t.Fatalf("first write: %s", werr)
	}
	if _, werr, replaced := WriteSkill(draft, tmp, false); werr != "skill 'demo' already exists" || replaced {
		t.Fatalf("second write = (%q, %v)", werr, replaced)
	}
}

// Parity-exempt: removing stale auxiliary files on replace has no Python pytest;
func TestWriteSkillReplaceRemovesStaleFiles(t *testing.T) {
	tmp := t.TempDir()
	first, errMsg := BuildSkillDraft("demo", "desc", "body", []any{
		map[string]any{"path": "notes.md", "content": "notes"},
	})
	if errMsg != "" {
		t.Fatalf("error = %q", errMsg)
	}
	installed, werr, _ := WriteSkill(first, tmp, true)
	if werr != "" {
		t.Fatalf("first write: %s", werr)
	}

	second, errMsg := BuildSkillDraft("demo", "desc", "body", nil)
	if errMsg != "" {
		t.Fatalf("error = %q", errMsg)
	}
	again, werr, replaced := WriteSkill(second, tmp, true)
	if werr != "" {
		t.Fatalf("replace: %s", werr)
	}
	if !replaced {
		t.Fatal("replace should report replaced")
	}
	if again != installed {
		t.Fatalf("path changed: %q -> %q", installed, again)
	}
	if _, err := os.Stat(filepath.Join(installed, "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("stale notes.md survived replacement: %v", err)
	}
}

// Parity-exempt: creating nested parent directories for auxiliary skill files
func TestWriteSkillCreatesAuxParentDirs(t *testing.T) {
	tmp := t.TempDir()
	draft, errMsg := BuildSkillDraft("demo", "desc", "body", []any{
		map[string]any{"path": "assets/data/info.txt", "content": "payload"},
	})
	if errMsg != "" {
		t.Fatalf("error = %q", errMsg)
	}
	installed, werr, _ := WriteSkill(draft, tmp, true)
	if werr != "" {
		t.Fatalf("write: %s", werr)
	}
	content, err := os.ReadFile(filepath.Join(installed, "assets", "data", "info.txt"))
	if err != nil || string(content) != "payload" {
		t.Fatalf("aux file = (%q, %v)", content, err)
	}
}

// Parity-exempt: RemoveSkill's invalid-name guard is a Go helper; the Python
func TestRemoveSkillRejectsInvalidName(t *testing.T) {
	if removed, errMsg := RemoveSkill(t.TempDir(), "../escape"); removed || errMsg == "" {
		t.Fatalf("remove = (%v, %q)", removed, errMsg)
	}
}

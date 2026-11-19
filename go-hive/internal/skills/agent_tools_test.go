package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillTestDirs(t *testing.T) ToolDirs {
	t.Helper()
	return ToolDirs{
		ColonyDir:  t.TempDir(),
		QueenDir:   t.TempDir(),
		QueenID:    "test-queen",
		SessionDir: t.TempDir(),
	}
}

func decodeSkillPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v (%q)", err, content)
	}
	return payload
}

// Parity-exempt: upstream declares the write_skill schema inline in Python;
// this asserts the Go tool kept the same six parameter names and scope enum.
func TestWriteSkillToolParametersMatchUpstream(t *testing.T) {
	def := WriteSkillToolDef()
	if def.Name != "write_skill" {
		t.Fatalf("name = %q", def.Name)
	}
	properties, _ := def.Parameters["properties"].(map[string]any)
	want := []string{"skill_name", "skill_description", "skill_body", "skill_files", "source_path", "scope"}
	if len(properties) != len(want) {
		t.Fatalf("parameters = %v, want %v", properties, want)
	}
	for _, name := range want {
		if _, ok := properties[name]; !ok {
			t.Fatalf("parameter %q missing from %v", name, properties)
		}
	}
	scope, _ := properties["scope"].(map[string]any)
	enum, _ := scope["enum"].([]string)
	if len(enum) != 3 || enum[0] != "queen" || enum[1] != "colony" || enum[2] != "global" {
		t.Fatalf("scope enum = %v", enum)
	}
}

// Parity: tests/test_write_skill_tool.py::test_write_skill_happy_path
func TestWriteSkillToolWritesAndReadsColonySkill(t *testing.T) {
	dirs := skillTestDirs(t)
	content, isError, handled := ExecuteAgentTool(WriteSkillToolName, map[string]any{
		"skill_name":        "house-style",
		"skill_description": "How Ava formats reports.",
		"skill_body":        "# House style\n\nLead with the answer.\n",
		"skill_files": []any{
			map[string]any{"path": "refs/example.txt", "content": "example\n"},
		},
	}, dirs)
	if !handled {
		t.Fatal("write_skill was not handled")
	}
	if isError {
		t.Fatalf("write_skill failed: %s", content)
	}
	payload := decodeSkillPayload(t, content)
	skillPath, _ := payload["skill_path"].(string)
	if !strings.HasSuffix(skillPath, filepath.Join("skills", "house-style")) {
		t.Fatalf("skill_path = %q, want a colony skills/house-style path", skillPath)
	}
	skillMD, err := os.ReadFile(filepath.Join(skillPath, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	for _, want := range []string{"name: house-style", "description: How Ava formats reports.", "# House style", "Lead with the answer."} {
		if !strings.Contains(string(skillMD), want) {
			t.Fatalf("SKILL.md = %q, missing %q", string(skillMD), want)
		}
	}
	if _, err := os.Stat(filepath.Join(skillPath, "refs", "example.txt")); err != nil {
		t.Fatalf("supporting file missing: %v", err)
	}

	view, viewErr, _ := ExecuteAgentTool(SkillViewToolName, map[string]any{"skill_name": "house-style"}, dirs)
	if viewErr {
		t.Fatalf("skill_view failed: %s", view)
	}
	viewPayload := decodeSkillPayload(t, view)
	if !strings.Contains(viewPayload["content"].(string), "Lead with the answer.") {
		t.Fatalf("skill_view content = %q", viewPayload["content"])
	}

	list, listErr, _ := ExecuteAgentTool(ListSkillsToolName, map[string]any{}, dirs)
	if listErr {
		t.Fatalf("list_skills failed: %s", list)
	}
	listPayload := decodeSkillPayload(t, list)
	entries, _ := listPayload["skills"].([]any)
	if len(entries) != 1 {
		t.Fatalf("list_skills returned %d entries, want 1", len(entries))
	}
	entry, _ := entries[0].(map[string]any)
	if entry["name"] != "house-style" || entry["scope"] != "colony" {
		t.Fatalf("list entry = %#v", entry)
	}
}

// Parity: tests/test_write_skill_tool.py::test_write_skill_validation_error_surfaces
func TestWriteSkillToolRejectsInvalidName(t *testing.T) {
	dirs := skillTestDirs(t)
	content, isError, _ := ExecuteAgentTool(WriteSkillToolName, map[string]any{
		"skill_name":        "Bad Name",
		"skill_description": "bad",
		"skill_body":        "body",
	}, dirs)
	if !isError {
		t.Fatalf("invalid name reported success: %s", content)
	}
	payload := decodeSkillPayload(t, content)
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "must match") {
		t.Fatalf("error = %q, want the name-pattern diagnostic", message)
	}
}

// Parity: tests/test_write_skill_tool.py::test_colony_scope_without_a_colony_is_refused
func TestWriteSkillToolColonyScopeWithoutColonyIsRefused(t *testing.T) {
	dirs := ToolDirs{QueenDir: t.TempDir(), QueenID: "default", SessionDir: t.TempDir()}
	content, isError, _ := ExecuteAgentTool(WriteSkillToolName, map[string]any{
		"skill_name":        "house-style",
		"skill_description": "desc",
		"skill_body":        "body",
		"scope":             "colony",
	}, dirs)
	if !isError {
		t.Fatalf("colony scope without a colony reported success: %s", content)
	}
	payload := decodeSkillPayload(t, content)
	if !strings.Contains(payload["error"].(string), "no colony is bound") {
		t.Fatalf("error = %q", payload["error"])
	}
}

// Parity: tests/test_write_skill_tool.py::test_write_skill_copies_from_source_path
func TestWriteSkillToolCopiesSourcePath(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: source-skill\ndescription: copied skill\n---\n\nBody from source.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "refs", "data.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs := skillTestDirs(t)
	content, isError, _ := ExecuteAgentTool(WriteSkillToolName, map[string]any{
		"source_path": source,
		"skill_name":  "renamed-copy",
		"scope":       "queen",
	}, dirs)
	if isError {
		t.Fatalf("source_path copy failed: %s", content)
	}
	payload := decodeSkillPayload(t, content)
	if payload["files_copied"] != float64(1) {
		t.Fatalf("files_copied = %v, want 1", payload["files_copied"])
	}
	target := filepath.Join(dirs.QueenDir, "skills", "renamed-copy")
	if data, err := os.ReadFile(filepath.Join(target, "refs", "data.json")); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("copied aux file = %q, err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "SKILL.md")); err != nil || !strings.Contains(string(data), "Body from source.") {
		t.Fatalf("copied SKILL.md = %q, err=%v", string(data), err)
	}
}

// Parity: tests/test_write_skill_tool.py::test_global_scope_files_a_request_and_installs_nothing
func TestWriteSkillToolGlobalScopeFilesRequest(t *testing.T) {
	dirs := skillTestDirs(t)
	content, isError, _ := ExecuteAgentTool(WriteSkillToolName, map[string]any{
		"skill_name":        "shared-skill",
		"skill_description": "shared",
		"skill_body":        "body",
		"scope":             "global",
	}, dirs)
	if isError {
		t.Fatalf("global scope failed: %s", content)
	}
	payload := decodeSkillPayload(t, content)
	if payload["pending"] != true || payload["request_id"] == nil {
		t.Fatalf("payload = %#v", payload)
	}
	if _, err := os.Stat(filepath.Join(dirs.ColonyDir, "skills", "shared-skill")); !os.IsNotExist(err) {
		t.Fatalf("global scope unexpectedly installed the skill: %v", err)
	}
	pending := PendingRequests(dirs.SessionDir)
	if len(pending) != 1 || pending[0].Name != "shared-skill" {
		t.Fatalf("pending requests = %#v", pending)
	}
}

// Parity: tests/test_write_skill_tool.py::test_queen_scope_never_replaces_a_skill_the_user_made
func TestWriteSkillToolQueenScopeProtectsUserSkill(t *testing.T) {
	dirs := skillTestDirs(t)
	userSkill := filepath.Join(dirs.QueenDir, "skills", "user-made")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userSkill, "SKILL.md"), []byte("---\nname: user-made\ndescription: user\n---\n\nuser body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, isError, _ := ExecuteAgentTool(WriteSkillToolName, map[string]any{
		"skill_name":        "user-made",
		"skill_description": "queen version",
		"skill_body":        "queen body",
		"scope":             "queen",
	}, dirs)
	if !isError {
		t.Fatalf("queen scope replaced the user's skill: %s", content)
	}
	payload := decodeSkillPayload(t, content)
	if !strings.Contains(payload["error"].(string), "the user made the skill") {
		t.Fatalf("error = %q", payload["error"])
	}
}

// Parity-exempt: upstream reads skills through view_file plus <read_skill>
// tags, so there is no Python test for a path-traversal guard in a tool.
func TestSkillViewRejectsTraversal(t *testing.T) {
	dirs := skillTestDirs(t)
	if _, isError, _ := ExecuteAgentTool(WriteSkillToolName, map[string]any{
		"skill_name":        "house-style",
		"skill_description": "desc",
		"skill_body":        "body",
	}, dirs); isError {
		t.Fatal("setup write_skill failed")
	}
	content, isError, _ := ExecuteAgentTool(SkillViewToolName, map[string]any{
		"skill_name": "house-style",
		"file":       "../secret.txt",
	}, dirs)
	if !isError {
		t.Fatalf("traversal read reported success: %s", content)
	}
	if !strings.Contains(content, "relative and inside") {
		t.Fatalf("error = %q", content)
	}
}

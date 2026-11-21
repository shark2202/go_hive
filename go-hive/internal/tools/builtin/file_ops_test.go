package builtin

// Behavior parity for the file tools ported from
// tools/src/aden_tools/file_ops.py. Each test names the upstream test it
// mirrors; Go-only cases (sandbox hardening, fuzzy engine internals) carry an
// explicit Parity-exempt note.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func execTool(t *testing.T, tb *Toolbox, name string, input map[string]any) Result {
	t.Helper()
	res, err := tb.Execute(context.Background(), name, input)
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	return res
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// seedRead performs the read_file call that upstream's file_ops tests bypass
// with an autouse check_fresh fixture. Go has no monkeypatching, so tests that
// edit an existing file read it first exactly like the model must.
func seedRead(t *testing.T, tb *Toolbox, path string) {
	t.Helper()
	res := execTool(t, tb, ToolReadFile, map[string]any{"path": path})
	if res.IsError {
		t.Fatalf("seed read %s: %+v", path, res)
	}
}

// ── search_files ─────────────────────────────────────────────────────────

// Parity: tools/tests/tools/test_file_ops.py::TestSearchFilesPathRelativization::test_python_fallback_relativizes_paths
func TestSearchFilesPathRelativization(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "subdir", "baz.txt"), "find_me\n")
	tb := New(dir)

	res := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern": "find_me",
		"path":    ".",
	})
	if res.IsError {
		t.Fatalf("search error: %q", res.Content)
	}
	if strings.Contains(res.Content, dir) {
		t.Fatalf("output leaked the absolute root: %q", res.Content)
	}
	if !strings.Contains(res.Content, "baz.txt") || !strings.Contains(res.Content, "subdir"+string(filepath.Separator)+"baz.txt") {
		t.Fatalf("output is not root-relative: %q", res.Content)
	}
	if !strings.Contains(res.Content, ":1:find_me") {
		t.Fatalf("missing line-number shape: %q", res.Content)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestSearchFilesBasic::test_search_finds_content
func TestSearchFilesContentFindsMatch(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "hello.txt"), "world\n")
	tb := New(dir)

	res := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern": "world",
		"path":    ".",
	})
	if res.IsError {
		t.Fatalf("search error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "world") || !strings.Contains(res.Content, "hello.txt") {
		t.Fatalf("content search = %q", res.Content)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestSearchFilesBasic::test_search_nonexistent_dir_returns_error
func TestSearchFilesNonexistentPath(t *testing.T) {
	resetSearchTracker()
	tb := New(t.TempDir())
	res := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern": "x",
		"path":    "nonexistent",
	})
	if res.IsError || !strings.Contains(res.Content, "Error") || !strings.Contains(strings.ToLower(res.Content), "not found") {
		t.Fatalf("missing path error = %+v", res)
	}
}

// Parity-exempt: upstream test_file_ops.py has no target='files' case; the
// newest-first ordering and paging hint follow _do_search_files_target.
func TestSearchFilesFilesTargetAndPaging(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.py")
	newPath := filepath.Join(dir, "sub", "new.py")
	mustWrite(t, oldPath, "x = 1\n")
	mustWrite(t, newPath, "y = 2\n")
	mustWrite(t, filepath.Join(dir, "notes.txt"), "plain\n")
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	tb := New(dir)

	res := execTool(t, tb, ToolSearchFiles, map[string]any{
		"target":  "files",
		"pattern": "*.py",
	})
	if res.IsError {
		t.Fatalf("files search error: %q", res.Content)
	}
	lines := strings.Split(res.Content, "\n")
	if len(lines) < 2 {
		t.Fatalf("files search = %q", res.Content)
	}
	if !strings.Contains(lines[0], filepath.Join("sub", "new.py")) {
		t.Fatalf("newest file should be first: %q", res.Content)
	}
	if !strings.Contains(lines[1], "old.py") || strings.Contains(res.Content, "notes.txt") {
		t.Fatalf("glob filter/order = %q", res.Content)
	}

	limited := execTool(t, tb, ToolSearchFiles, map[string]any{
		"target":  "files",
		"pattern": "*.py",
		"limit":   1,
	})
	if !strings.Contains(limited.Content, "showing 1 of 2 files") {
		t.Fatalf("missing files paging hint: %q", limited.Content)
	}

	paged := execTool(t, tb, ToolSearchFiles, map[string]any{
		"target":  "files",
		"pattern": "*.py",
		"offset":  1,
	})
	if strings.Contains(paged.Content, "new.py") || !strings.Contains(paged.Content, "old.py") {
		t.Fatalf("offset page = %q", paged.Content)
	}
}

// Parity-exempt: upstream test_file_ops.py has no output_mode test; the shape
// follows _do_search_content_target in tools/src/aden_tools/file_ops.py.
func TestSearchFilesOutputModes(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "hit\nhit\n")
	mustWrite(t, filepath.Join(dir, "b.txt"), "hit\n")
	tb := New(dir)

	filesOnly := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern":     "hit",
		"output_mode": "files_only",
	})
	if filesOnly.IsError || !strings.Contains(filesOnly.Content, "a.txt") ||
		!strings.Contains(filesOnly.Content, "b.txt") || strings.Contains(filesOnly.Content, ":hit") {
		t.Fatalf("files_only = %q", filesOnly.Content)
	}

	counts := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern":     "hit",
		"output_mode": "count",
	})
	if counts.IsError || !strings.Contains(counts.Content, "a.txt:2") || !strings.Contains(counts.Content, "b.txt:1") {
		t.Fatalf("count = %q", counts.Content)
	}

	if res := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern":     "hit",
		"output_mode": "bogus",
	}); res.IsError || !strings.Contains(res.Content, "invalid output_mode") {
		t.Fatalf("invalid output_mode = %+v", res)
	}
}

// Parity-exempt: upstream test_file_ops.py has no context test; the format
// follows _do_search_content_target's context branch.
func TestSearchFilesContextLines(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "ctx.txt"), "one\nmatch\nthree\n")
	tb := New(dir)

	res := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern": "match",
		"context": 1,
	})
	if res.IsError {
		t.Fatalf("context search error: %q", res.Content)
	}
	for _, want := range []string{"ctx.txt:1-one", "ctx.txt:2:match", "ctx.txt:3-three"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("context output missing %q: %q", want, res.Content)
		}
	}
}

// Parity-exempt: upstream test_file_ops.py has no limit/offset assertion; the
// pagination hint and limit semantics follow _do_search_content_target.
func TestSearchFilesContentLimitAndOffset(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "many.txt"), "hit\nhit\nhit\nhit\nhit\n")
	tb := New(dir)

	first := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern": "hit",
		"limit":   2,
	})
	if first.IsError || strings.Count(first.Content, ":hit") != 2 {
		t.Fatalf("limit page = %q", first.Content)
	}
	if !strings.Contains(first.Content, "showing 2 of 5 matches") {
		t.Fatalf("missing content paging hint: %q", first.Content)
	}

	last := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern": "hit",
		"limit":   2,
		"offset":  4,
	})
	if last.IsError || strings.Count(last.Content, ":hit") != 1 {
		t.Fatalf("offset page = %q", last.Content)
	}
	if strings.Contains(last.Content, "[Hint:") {
		t.Fatalf("last page should not hint: %q", last.Content)
	}

	none := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern": "absent",
	})
	if none.IsError || !strings.Contains(none.Content, "No matches found.") {
		t.Fatalf("empty search = %q", none.Content)
	}
}

// Parity-exempt: upstream test_file_ops.py has no invalid-target/regex case;
// the messages follow the search_files wrapper.
func TestSearchFilesInvalidArguments(t *testing.T) {
	resetSearchTracker()
	tb := New(t.TempDir())

	if res := execTool(t, tb, ToolSearchFiles, map[string]any{"pattern": "x", "target": "bogus"}); res.IsError ||
		!strings.Contains(res.Content, "invalid target") {
		t.Fatalf("invalid target = %+v", res)
	}
	if res := execTool(t, tb, ToolSearchFiles, map[string]any{"pattern": "("}); !res.IsError ||
		!strings.Contains(res.Content, "invalid regex") {
		t.Fatalf("invalid regex = %+v", res)
	}
	if res := execTool(t, tb, ToolSearchFiles, map[string]any{}); !res.IsError ||
		!strings.Contains(res.Content, "pattern is required") {
		t.Fatalf("missing pattern = %+v", res)
	}
}

// Parity-exempt: the legacy target aliases are part of the search_files
// wrapper's accepted input, not of any upstream test.
func TestSearchFilesTargetAliases(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "hello.txt"), "world\n")
	tb := New(dir)

	grep := execTool(t, tb, ToolSearchFiles, map[string]any{"pattern": "world", "target": "grep"})
	if grep.IsError || !strings.Contains(grep.Content, "hello.txt:1:world") {
		t.Fatalf("grep alias = %q", grep.Content)
	}
	find := execTool(t, tb, ToolSearchFiles, map[string]any{"pattern": "*.txt", "target": "find"})
	if find.IsError || !strings.Contains(find.Content, "hello.txt") {
		t.Fatalf("find alias = %q", find.Content)
	}
}

// Parity-exempt: upstream tracks the same consecutive-query guard in
// file_ops.py's SEARCH_TRACKER; test_file_ops.py does not exercise it.
func TestSearchFilesAntiLoopGuard(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "hello.txt"), "world\n")
	tb := New(dir)
	input := map[string]any{"pattern": "world", "path": "hello.txt"}

	for i := 1; i <= 4; i++ {
		res := execTool(t, tb, ToolSearchFiles, input)
		switch i {
		case 1, 2:
			if strings.Contains(res.Content, "[Warning:") || strings.Contains(res.Content, "BLOCKED") {
				t.Fatalf("call %d warned early: %q", i, res.Content)
			}
		case 3:
			if !strings.Contains(res.Content, "[Warning:") {
				t.Fatalf("third call missing warning: %q", res.Content)
			}
		case 4:
			if !res.IsError || !strings.Contains(res.Content, "BLOCKED") {
				t.Fatalf("fourth call was not blocked: %+v", res)
			}
		}
	}

	// A different query resets the counter.
	res := execTool(t, tb, ToolSearchFiles, map[string]any{"pattern": "hello", "path": "hello.txt"})
	if res.IsError || strings.Contains(res.Content, "BLOCKED") {
		t.Fatalf("new query stayed blocked: %+v", res)
	}
}

// Parity-exempt: hashline anchors come from aden_tools/hashline.py; the
// search output shape follows _do_search_content_target.
func TestSearchFilesHashlineAnchors(t *testing.T) {
	resetSearchTracker()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "abc.txt"), "abc\n")
	tb := New(dir)

	res := execTool(t, tb, ToolSearchFiles, map[string]any{
		"pattern":  "abc",
		"hashline": true,
	})
	if res.IsError {
		t.Fatalf("hashline search error: %q", res.Content)
	}
	pattern := regexp.MustCompile(`abc\.txt:1:[0-9a-f]{4}\|abc`)
	if !pattern.MatchString(res.Content) {
		t.Fatalf("hashline shape = %q", res.Content)
	}
}

// ── path policy ──────────────────────────────────────────────────────────

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyHomeAnchoring::test_relative_write_lands_in_home
func TestFilePathPolicyRelativePathsAnchorToRoot(t *testing.T) {
	dir := t.TempDir()
	tb := New(dir)

	write := execTool(t, tb, ToolWriteFile, map[string]any{"path": "notes.md", "content": "hello"})
	if write.IsError {
		t.Fatalf("write = %+v", write)
	}
	if got := mustRead(t, filepath.Join(dir, "notes.md")); got != "hello" {
		t.Fatalf("relative write landed at %q", got)
	}
	read := execTool(t, tb, ToolReadFile, map[string]any{"path": "notes.md"})
	if read.IsError || !strings.Contains(read.Content, "hello") {
		t.Fatalf("relative read = %+v", read)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyHomeAnchoring::test_absolute_path_is_honored_verbatim
func TestFilePathPolicyAbsolutePathHonored(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "abs.txt")
	mustWrite(t, target, "absolute content\n")
	tb := New(dir)

	read := execTool(t, tb, ToolReadFile, map[string]any{"path": target})
	if read.IsError || !strings.Contains(read.Content, "absolute content") {
		t.Fatalf("absolute read = %+v", read)
	}
	write := execTool(t, tb, ToolWriteFile, map[string]any{"path": target, "content": "rewritten"})
	if write.IsError {
		t.Fatalf("absolute write = %+v", write)
	}
	if got := mustRead(t, target); got != "rewritten" {
		t.Fatalf("absolute write content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyDenyLists::test_write_to_system_path_denied
// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyDenyLists::test_write_under_etc_denied
func TestFilePathPolicySystemWriteDenied(t *testing.T) {
	tb := New(t.TempDir())
	for _, target := range []string{"/etc/hosts", "/etc/nginx/nginx.conf"} {
		for _, name := range []string{ToolWriteFile, ToolEditFile} {
			input := map[string]any{"path": target, "content": "x"}
			if name == ToolEditFile {
				input = map[string]any{"mode": "replace", "path": target, "old_string": "a", "new_string": "b"}
			}
			res := execTool(t, tb, name, input)
			if res.IsError || !strings.Contains(res.Content, "escapes the sandbox root") {
				t.Fatalf("%s outside root (%s) = %+v", name, target, res)
			}
		}
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyDenyLists::test_write_to_ssh_denied
func TestFilePathPolicyCredentialPathDenied(t *testing.T) {
	tb := New(t.TempDir())
	write := execTool(t, tb, ToolWriteFile, map[string]any{"path": "~/.ssh/authorized_keys", "content": "x"})
	if write.IsError || !strings.Contains(write.Content, "escapes the sandbox root") {
		t.Fatalf("ssh write = %+v", write)
	}
	read := execTool(t, tb, ToolReadFile, map[string]any{"path": "~/.ssh/id_rsa"})
	// Read refusals are plain text; the credential must not come back.
	if !strings.Contains(read.Content, "escapes the sandbox root") || strings.Contains(read.Content, "PRIVATE KEY") {
		t.Fatalf("ssh read = %+v", read)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyWriteSafeRoot::test_write_outside_safe_root_denied
func TestFilePathPolicyWriteOutsideRootDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	tb := New(root)

	res := execTool(t, tb, ToolWriteFile, map[string]any{
		"path":    filepath.Join(outside, "blocked.txt"),
		"content": "x",
	})
	if res.IsError || !strings.Contains(res.Content, "escapes the sandbox root") {
		t.Fatalf("outside write = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file was written: %v", err)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyWriteSafeRoot::test_write_inside_safe_root_allowed
func TestFilePathPolicyWriteInsideRootAllowed(t *testing.T) {
	root := t.TempDir()
	tb := New(root)
	res := execTool(t, tb, ToolWriteFile, map[string]any{"path": "ok.txt", "content": "x"})
	if res.IsError {
		t.Fatalf("inside write = %+v", res)
	}
	if got := mustRead(t, filepath.Join(root, "ok.txt")); got != "x" {
		t.Fatalf("content = %q", got)
	}
}

// Parity-exempt: upstream allows reads of system config (/etc/hosts); the Go
// toolbox applies the sandbox root to every read as well, which is stricter.
func TestFilePathPolicyReadOutsideRootDenied(t *testing.T) {
	tb := New(t.TempDir())
	res := execTool(t, tb, ToolReadFile, map[string]any{"path": "/etc/hosts"})
	// file_ops.py returns its policy message as plain text; the refusal, not
	// the is_error bit, is what the model reads.
	if !strings.Contains(res.Content, "escapes the sandbox root") || strings.Contains(res.Content, "localhost") {
		t.Fatalf("outside read = %+v", res)
	}
}

// ── edit_file: replace mode ─────────────────────────────────────────────

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolReplaceMode::test_replace_basic
func TestEditFileReplaceBasic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	mustWrite(t, target, "Hello World")
	tb := New(dir)
	seedRead(t, tb, "a.txt")

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "a.txt",
		"old_string": "World",
		"new_string": "Universe",
	})
	if res.IsError {
		t.Fatalf("replace = %+v", res)
	}
	if !strings.Contains(res.Content, "Replaced 1") {
		t.Fatalf("summary = %q", res.Content)
	}
	if !strings.Contains(res.Content, "-Hello World") || !strings.Contains(res.Content, "+Hello Universe") {
		t.Fatalf("diff = %q", res.Content)
	}
	if got := mustRead(t, target); got != "Hello Universe" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolReplaceMode::test_replace_unicode_normalized
func TestEditFileReplaceUnicodeNormalized(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "b.py")
	mustWrite(t, target, "print(\"hi\")\n")
	tb := New(dir)
	seedRead(t, tb, "b.py")

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "b.py",
		"old_string": "print(“hi”)",
		"new_string": "print(\"HELLO\")",
	})
	if res.IsError {
		t.Fatalf("replace = %+v", res)
	}
	if !strings.Contains(res.Content, "matched via unicode-normalized") {
		t.Fatalf("strategy missing: %q", res.Content)
	}
	if got := mustRead(t, target); got != "print(\"HELLO\")\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolReplaceMode::test_replace_escape_normalized
func TestEditFileReplaceEscapeNormalized(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "c.txt")
	mustWrite(t, target, "alpha\nbeta\n")
	tb := New(dir)
	seedRead(t, tb, "c.txt")

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "c.txt",
		"old_string": `alpha\nbeta`,
		"new_string": "X",
	})
	if res.IsError {
		t.Fatalf("replace = %+v", res)
	}
	if got := mustRead(t, target); got != "X\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolReplaceMode::test_replace_missing_file
func TestEditFileReplaceMissingFile(t *testing.T) {
	tb := New(t.TempDir())
	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "nope.txt",
		"old_string": "x",
		"new_string": "y",
	})
	// Upstream returns a plain "Error: File not found: …" string; the loop must
	// not add the ERROR: marker on top of it.
	if res.IsError || !strings.Contains(res.Content, "Error") || !strings.Contains(strings.ToLower(res.Content), "not found") {
		t.Fatalf("missing file = %+v", res)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolReplaceMode::test_replace_no_match
func TestEditFileReplaceNoMatch(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "d.txt"), "alpha\n")
	tb := New(dir)
	seedRead(t, tb, "d.txt")

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "d.txt",
		"old_string": "zzz",
		"new_string": "y",
	})
	if res.IsError || !strings.Contains(res.Content, "Error") {
		t.Fatalf("no match = %+v", res)
	}
	if !strings.Contains(res.Content, "read_file") && !strings.Contains(res.Content, "search_files") {
		t.Fatalf("no hint in %q", res.Content)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolReplaceMode::test_replace_denied_for_system_path
func TestEditFileReplaceSystemPathDenied(t *testing.T) {
	tb := New(t.TempDir())
	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "/etc/hosts",
		"old_string": "a",
		"new_string": "b",
	})
	if res.IsError || !strings.Contains(res.Content, "escapes the sandbox root") {
		t.Fatalf("system path = %+v", res)
	}
}

// Parity-exempt: the fuzzy strategy ladder is ported from
// file_ops.py::_fuzzy_find_candidates; upstream test_file_ops.py only
// exercises the unicode-normalized and escape-normalized rungs.
func TestEditFileReplaceFuzzyStrategies(t *testing.T) {
	cases := []struct {
		name    string
		content string
		old     string
		new     string
		want    string
	}{
		{
			name:    "line-trimmed",
			content: "\tfoo()\n",
			old:     "foo()",
			new:     "bar()",
			want:    "\tbar()\n",
		},
		{
			name:    "whitespace-normalized",
			content: "a  b\n",
			old:     "a b",
			new:     "c",
			want:    "c\n",
		},
		{
			name:    "indentation-flexible",
			content: "    if x:\n        y()\n",
			old:     "if x:\n    y()",
			new:     "z()",
			want:    "z()\n",
		},
		{
			name:    "trimmed-boundary",
			content: "xxneedleyy\n",
			old:     " needle ",
			new:     "NEW",
			want:    "xxNEWyy\n",
		},
		{
			name:    "block-anchor",
			content: "def f():\n    x = 1\n    return x\n",
			old:     "def f():\n    x = 2\n    return x",
			new:     "def f():\n    return 0",
			want:    "def f():\n    return 0\n",
		},
		{
			name:    "context-aware",
			content: "alpha one\nbeta two\n",
			old:     "alpha 0ne\nbeta 2wo",
			new:     "gamma",
			want:    "gamma\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "fuzzy.txt")
			mustWrite(t, target, tc.content)
			tb := New(dir)
			seedRead(t, tb, "fuzzy.txt")
			res := execTool(t, tb, ToolEditFile, map[string]any{
				"mode":       "replace",
				"path":       "fuzzy.txt",
				"old_string": tc.old,
				"new_string": tc.new,
			})
			if res.IsError {
				t.Fatalf("%s: %+v", tc.name, res)
			}
			if got := mustRead(t, target); got != tc.want {
				t.Fatalf("%s: content = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// Parity-exempt: uniqueness and replace_all are defined by
// file_ops.py::_patch_replace; test_file_ops.py has no dedicated case.
func TestEditFileReplaceUniquenessAndReplaceAll(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dup.txt")
	mustWrite(t, target, "x\nx\n")
	tb := New(dir)
	seedRead(t, tb, "dup.txt")

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "dup.txt",
		"old_string": "x",
		"new_string": "y",
	})
	if res.IsError || !strings.Contains(res.Content, "unique match") {
		t.Fatalf("ambiguous replace = %+v", res)
	}

	res = execTool(t, tb, ToolEditFile, map[string]any{
		"mode":        "replace",
		"path":        "dup.txt",
		"old_string":  "x",
		"new_string":  "y",
		"replace_all": true,
	})
	if res.IsError || !strings.Contains(res.Content, "Replaced 2") {
		t.Fatalf("replace_all = %+v", res)
	}
	if got := mustRead(t, target); got != "y\ny\n" {
		t.Fatalf("content = %q", got)
	}

	identical := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "dup.txt",
		"old_string": "y",
		"new_string": "y",
	})
	if identical.IsError || !strings.Contains(identical.Content, "identical") {
		t.Fatalf("identical strings = %+v", identical)
	}
}

// Parity-exempt: CRLF preservation is file_ops.py::_patch_replace behaviour
// with no upstream test.
func TestEditFileReplacePreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "crlf.txt")
	mustWrite(t, target, "a\r\nb\r\n")
	tb := New(dir)
	seedRead(t, tb, "crlf.txt")

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "crlf.txt",
		"old_string": "a\nb",
		"new_string": "A\nB",
	})
	if res.IsError {
		t.Fatalf("crlf replace = %+v", res)
	}
	if got := mustRead(t, target); got != "A\r\nB\r\n" {
		t.Fatalf("content = %q", got)
	}
}

// ── edit_file: patch mode ───────────────────────────────────────────────

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_update_single_file
func TestEditFilePatchUpdatesSingleFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "u.py")
	mustWrite(t, target, "def f():\n    return 1\n")
	tb := New(dir)

	body := "*** Begin Patch\n*** Update File: u.py\n def f():\n-    return 1\n+    return 42\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError {
		t.Fatalf("patch = %+v", res)
	}
	if !strings.Contains(res.Content, "Modified") {
		t.Fatalf("summary = %q", res.Content)
	}
	if got := mustRead(t, target); got != "def f():\n    return 42\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_add_file
func TestEditFilePatchAddFile(t *testing.T) {
	dir := t.TempDir()
	tb := New(dir)

	body := "*** Begin Patch\n*** Add File: new.py\n+# new\n+x = 1\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError || !strings.Contains(res.Content, "Created") {
		t.Fatalf("patch = %+v", res)
	}
	if got := mustRead(t, filepath.Join(dir, "new.py")); got != "# new\nx = 1\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_delete_file
func TestEditFilePatchDeleteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "doomed.py")
	mustWrite(t, target, "bye\n")
	tb := New(dir)

	body := "*** Begin Patch\n*** Delete File: doomed.py\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError || !strings.Contains(res.Content, "Deleted") {
		t.Fatalf("patch = %+v", res)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_move_file
func TestEditFilePatchMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.py")
	mustWrite(t, src, "kept\n")
	tb := New(dir)

	body := "*** Begin Patch\n*** Move File: src.py -> dst.py\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError {
		t.Fatalf("patch = %+v", res)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "dst.py")); got != "kept\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_atomic_failure
func TestEditFilePatchAtomicFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.py")
	mustWrite(t, target, "original\n")
	tb := New(dir)

	body := "*** Begin Patch\n*** Update File: real.py\n-original\n+changed\n*** Update File: missing.py\n-foo\n+bar\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError || !strings.Contains(res.Content, "Patch validation failed (no files were modified)") {
		t.Fatalf("patch = %+v", res)
	}
	if got := mustRead(t, target); got != "original\n" {
		t.Fatalf("first file was modified: %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_lenient_missing_markers
func TestEditFilePatchLenientMissingMarkers(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "loose.py")
	mustWrite(t, target, "a\nb\nc\n")
	tb := New(dir)

	body := "*** Update File: loose.py\n-b\n+B\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError {
		t.Fatalf("patch = %+v", res)
	}
	if got := mustRead(t, target); got != "a\nB\nc\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_empty_body_rejected
func TestEditFilePatchEmptyBodyRejected(t *testing.T) {
	tb := New(t.TempDir())
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": ""})
	if res.IsError || !strings.Contains(res.Content, "Error") {
		t.Fatalf("empty patch = %+v", res)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPatchToolPatchMode::test_patch_unknown_mode
func TestEditFilePatchUnknownMode(t *testing.T) {
	tb := New(t.TempDir())
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "bogus"})
	if res.IsError || !strings.Contains(strings.ToLower(res.Content), "unknown mode") {
		t.Fatalf("unknown mode = %+v", res)
	}
}

// Parity-exempt: context-hint disambiguation and multi-hunk ordering are
// file_ops.py::_apply_hunk behaviour; upstream test_file_ops.py has no case.
func TestEditFilePatchContextHintAndMultipleHunks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hint.py")
	padding := strings.Repeat("# padding\n", 80)
	mustWrite(t, target, "def a():\n    return 1\n"+padding+"def b():\n    return 1\n")
	tb := New(dir)

	body := "*** Begin Patch\n*** Update File: hint.py\n@@ def b() @@\n-    return 1\n+    return 2\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError {
		t.Fatalf("hint patch = %+v", res)
	}
	want := "def a():\n    return 1\n" + padding + "def b():\n    return 2\n"
	if got := mustRead(t, target); got != want {
		t.Fatalf("hint target = %q", got)
	}

	multi := filepath.Join(dir, "multi.py")
	mustWrite(t, multi, "one\ntwo\nthree\nfour\nfive\n")
	body = "*** Begin Patch\n*** Update File: multi.py\n@@\n-one\n+ONE\n@@\n-five\n+FIVE\n*** End Patch\n"
	res = execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError {
		t.Fatalf("multi-hunk patch = %+v", res)
	}
	if got := mustRead(t, multi); got != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("multi-hunk content = %q", got)
	}
}

// Parity-exempt: add-over-existing and path-escape operations must fail
// atomically; upstream only tests atomicity through a missing update target.
func TestEditFilePatchValidationAtomicallyRefuses(t *testing.T) {
	dir := t.TempDir()
	untouched := filepath.Join(dir, "keep.txt")
	mustWrite(t, untouched, "keep\n")
	tb := New(dir)

	body := "*** Begin Patch\n*** Update File: keep.txt\n-keep\n+clobbered\n*** Add File: keep.txt\n+x\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError || !strings.Contains(res.Content, "already exists") {
		t.Fatalf("duplicate add = %+v", res)
	}
	if got := mustRead(t, untouched); got != "keep\n" {
		t.Fatalf("file changed after failed validation: %q", got)
	}

	body = "*** Begin Patch\n*** Update File: ../escape.txt\n-a\n+b\n*** End Patch\n"
	res = execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError || !strings.Contains(res.Content, "escapes the sandbox root") {
		t.Fatalf("escaping patch = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("patch escaped the root: %v", err)
	}
}

// ── assembly: files-tools group ─────────────────────────────────────────

// Parity-exempt: Go assembly guard. Upstream's files-tools harness group is
// built from register_file_tools and holds exactly these four tools.
func TestFilesToolsGroupHasFourTools(t *testing.T) {
	want := []string{ToolReadFile, ToolWriteFile, ToolEditFile, ToolSearchFiles}
	defined := map[string]string{}
	for _, def := range Defs() {
		defined[def.Name] = def.Description
	}
	for _, name := range want {
		description, ok := defined[name]
		if !ok {
			t.Fatalf("files-tools member %q missing from Defs()", name)
		}
		if description == "" {
			t.Fatalf("files-tools member %q has an empty description", name)
		}
	}
	if len(want) != 4 {
		t.Fatalf("files-tools group = %d tools, want 4", len(want))
	}
}

// Parity-exempt: Go schema guard. The edit_file schema mirrors
// file_ops.py's register_tool declaration (every argument defaulted).
func TestEditFileAndSearchFilesSchemas(t *testing.T) {
	defs := map[string]map[string]any{}
	for _, def := range Defs() {
		defs[def.Name] = def.Parameters
	}

	editProps, ok := defs[ToolEditFile]["properties"].(map[string]any)
	if !ok {
		t.Fatalf("edit_file properties = %#v", defs[ToolEditFile]["properties"])
	}
	for _, key := range []string{"mode", "path", "old_string", "new_string", "replace_all", "patch_text"} {
		if _, ok := editProps[key]; !ok {
			t.Fatalf("edit_file schema missing %q: %#v", key, editProps)
		}
	}
	mode := editProps["mode"].(map[string]any)
	if mode["type"] != "string" || !strings.Contains(fmt.Sprint(mode["description"]), "'replace'") {
		t.Fatalf("edit_file mode schema = %#v", mode)
	}
	if _, required := defs[ToolEditFile]["required"]; required {
		t.Fatalf("edit_file should have no required fields: %#v", defs[ToolEditFile]["required"])
	}

	searchProps, ok := defs[ToolSearchFiles]["properties"].(map[string]any)
	if !ok {
		t.Fatalf("search_files properties = %#v", defs[ToolSearchFiles]["properties"])
	}
	for _, key := range []string{"pattern", "target", "path", "file_glob", "limit", "offset", "output_mode", "context", "hashline", "task_id"} {
		if _, ok := searchProps[key]; !ok {
			t.Fatalf("search_files schema missing %q: %#v", key, searchProps)
		}
	}
	target := searchProps["target"].(map[string]any)
	if target["type"] != "string" || !strings.Contains(fmt.Sprint(target["description"]), "'content'") {
		t.Fatalf("search_files target schema = %#v", target)
	}
	required, _ := defs[ToolSearchFiles]["required"].([]string)
	if len(required) != 1 || required[0] != "pattern" {
		t.Fatalf("search_files required = %#v", defs[ToolSearchFiles]["required"])
	}
}

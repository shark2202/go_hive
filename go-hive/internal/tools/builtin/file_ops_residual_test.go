package builtin

// Residual parity for the three file-tool features added after the initial
// port: read_file hashline anchors (aden_tools/hashline.py), the stale-edit
// guard (aden_tools/file_state_cache.py) and write_safe_root. Each test cites
// the upstream test it mirrors; Go-only coverage carries an explicit
// Parity-exempt note.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── read_file: hashline ──────────────────────────────────────────────────

// Parity: tools/tests/tools/test_hashline.py::TestFormatHashlines::test_basic_format
// Parity: tools/tests/tools/test_hashline.py::TestComputeLineHash::test_space_stripping
func TestReadFileHashlineAnchors(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "notes.txt"), "hello\n\nif x:   \n  x\nworld")
	tb := New(dir)

	res := execTool(t, tb, ToolReadFile, map[string]any{"path": "notes.txt", "hashline": true})
	if res.IsError {
		t.Fatalf("hashline read = %+v", res)
	}
	want := strings.Join([]string{
		"1:a686|hello",
		"2:0000|",         // empty line: crc32("") % 65536
		"3:2652|if x:   ", // trailing spaces are hashed away but kept in the output
		"4:425e|  x",      // leading indentation is part of the hash
		"5:1143|world",    // last line, no trailing newline
	}, "\n")
	if res.Content != want {
		t.Fatalf("hashline output = %q, want %q", res.Content, want)
	}
}

// Parity: tools/tests/tools/test_hashline.py::TestComputeLineHash::test_empty_line
func TestReadFileHashlineEmptyLines(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "blank.txt"), "\n")
	tb := New(dir)

	res := execTool(t, tb, ToolReadFile, map[string]any{"path": "blank.txt", "hashline": true})
	if res.IsError {
		t.Fatalf("hashline read = %+v", res)
	}
	if res.Content != "1:0000|" {
		t.Fatalf("blank line = %q, want %q", res.Content, "1:0000|")
	}
}

// Parity: tools/tests/tools/test_hashline.py::TestFormatHashlines::test_offset_and_limit
func TestReadFileHashlineOffsetLimitAndCRLF(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "crlf.txt"), "a\r\nb\r\nc\r\n")
	tb := New(dir)

	res := execTool(t, tb, ToolReadFile, map[string]any{
		"path":     "crlf.txt",
		"offset":   2,
		"limit":    1,
		"hashline": true,
	})
	if res.IsError {
		t.Fatalf("hashline range = %+v", res)
	}
	// CRLF is a line boundary: the \r never reaches the hash or the output,
	// and the truncated range keeps the upstream continuation hint.
	if first, _, _ := strings.Cut(res.Content, "\n"); first != "2:eff9|b" {
		t.Fatalf("range first line = %q, want %q", first, "2:eff9|b")
	}
	if !strings.Contains(res.Content, "(Showing lines 2-2 of 3. Use offset=3 to continue reading.)") {
		t.Fatalf("range hint = %q", res.Content)
	}

	mustWrite(t, filepath.Join(dir, "notail.txt"), "hello\nworld")
	full := execTool(t, tb, ToolReadFile, map[string]any{"path": "notail.txt", "hashline": true})
	if full.IsError {
		t.Fatalf("no-trailing-newline read = %+v", full)
	}
	if want := "1:a686|hello\n2:1143|world"; full.Content != want {
		t.Fatalf("no-trailing-newline output = %q, want %q", full.Content, want)
	}
}

// Parity-exempt: read_file's hashline branch disables line truncation so the
// hash stays valid (file_ops.py::read_file); upstream has no such test.
func TestReadFileHashlineKeepsLongLines(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 2500) // beyond MaxReadLineLength (2000)
	mustWrite(t, filepath.Join(dir, "long.txt"), long+"\n")
	tb := New(dir)

	res := execTool(t, tb, ToolReadFile, map[string]any{"path": "long.txt", "hashline": true})
	if res.IsError {
		t.Fatalf("hashline read = %+v", res)
	}
	if want := "1:41c7|" + long; res.Content != want {
		t.Fatalf("long line = %q... (len %d), want prefix %q and full length %d",
			res.Content[:min(len(res.Content), 40)], len(res.Content), "1:41c7|", len(want))
	}
}

// ── stale-edit guard ─────────────────────────────────────────────────────

// Parity: tools/tests/test_file_state_cache.py::test_edit_file_refuses_without_prior_read
func TestStaleGuardEditRefusesWithoutPriorRead(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "e.py")
	mustWrite(t, target, "print('hello')\n")
	tb := New(dir)

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "e.py",
		"old_string": "hello",
		"new_string": "world",
	})
	if !res.IsError || !strings.Contains(res.Content, "Refusing to edit") || !strings.Contains(res.Content, "read_file") {
		t.Fatalf("guard = %+v", res)
	}
	if got := mustRead(t, target); got != "print('hello')\n" {
		t.Fatalf("file changed under refusal: %q", got)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_edit_file_proceeds_after_read
func TestStaleGuardEditProceedsAfterRead(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.py")
	mustWrite(t, target, "print('hello')\n")
	tb := New(dir)
	seedRead(t, tb, "f.py")

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "f.py",
		"old_string": "hello",
		"new_string": "world",
	})
	if res.IsError || !strings.Contains(res.Content, "Replaced") {
		t.Fatalf("edit = %+v", res)
	}
	if got := mustRead(t, target); got != "print('world')\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_edit_file_refuses_when_file_changed_between_read_and_edit
func TestStaleGuardEditRefusesWhenFileChanged(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "g.py")
	mustWrite(t, target, "print('hello')\n")
	tb := New(dir)
	seedRead(t, tb, "g.py")

	// The user saves the file outside the agent between read and edit.
	mustWrite(t, target, "print('bye')\n")
	bump := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(target, bump, bump); err != nil {
		t.Fatal(err)
	}

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "g.py",
		"old_string": "hello",
		"new_string": "world",
	})
	if !res.IsError || !strings.Contains(res.Content, "Refusing to edit") || !strings.Contains(res.Content, "Re-read") {
		t.Fatalf("stale guard = %+v", res)
	}
	if got := mustRead(t, target); got != "print('bye')\n" {
		t.Fatalf("external content clobbered: %q", got)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_write_file_allowed_for_new_file_without_prior_read
func TestStaleGuardWriteAllowsNewFileWithoutRead(t *testing.T) {
	dir := t.TempDir()
	tb := New(dir)

	res := execTool(t, tb, ToolWriteFile, map[string]any{"path": "brand_new.txt", "content": "first contents\n"})
	if res.IsError {
		t.Fatalf("new file write = %+v", res)
	}
	if got := mustRead(t, filepath.Join(dir, "brand_new.txt")); got != "first contents\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_write_file_refuses_overwrite_without_prior_read
func TestStaleGuardWriteRefusesOverwriteWithoutRead(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	mustWrite(t, target, "do not clobber\n")
	tb := New(dir)

	res := execTool(t, tb, ToolWriteFile, map[string]any{"path": "existing.txt", "content": "clobbered\n"})
	if !res.IsError || !strings.Contains(res.Content, "Refusing to overwrite") || !strings.Contains(res.Content, "read_file") {
		t.Fatalf("write guard = %+v", res)
	}
	if got := mustRead(t, target); got != "do not clobber\n" {
		t.Fatalf("file changed under refusal: %q", got)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_chained_edits_in_same_turn_do_not_self_invalidate
func TestStaleGuardChainedEditsStayFresh(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "chained.py")
	mustWrite(t, target, "print('a')\nprint('b')\n")
	tb := New(dir)
	seedRead(t, tb, "chained.py")

	first := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "chained.py",
		"old_string": "a",
		"new_string": "A",
	})
	if first.IsError || !strings.Contains(first.Content, "Replaced") {
		t.Fatalf("first edit = %+v", first)
	}
	// edit_file re-records the post-write state, so the immediate second edit
	// must not trip the guard.
	second := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "chained.py",
		"old_string": "b",
		"new_string": "B",
	})
	if second.IsError || !strings.Contains(second.Content, "Replaced") {
		t.Fatalf("second edit = %+v", second)
	}
	if got := mustRead(t, target); got != "print('A')\nprint('B')\n" {
		t.Fatalf("content = %q", got)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_agent_scopes_are_isolated
func TestStaleGuardSessionsAreIsolated(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "solo.txt")
	mustWrite(t, target, "only mine\n")
	reader := New(dir)
	other := New(dir)

	seedRead(t, reader, "solo.txt")
	res := execTool(t, other, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "solo.txt",
		"old_string": "mine",
		"new_string": "yours",
	})
	if !res.IsError || !strings.Contains(res.Content, "Refusing to edit") {
		t.Fatalf("second session reused the first session's read state: %+v", res)
	}

	ok := execTool(t, reader, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "solo.txt",
		"old_string": "mine",
		"new_string": "yours",
	})
	if ok.IsError || !strings.Contains(ok.Content, "Replaced") {
		t.Fatalf("owning session edit = %+v", ok)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_check_fresh_returns_unread_when_never_recorded
// Parity: tools/tests/test_file_state_cache.py::test_record_then_check_returns_fresh
// Parity: tools/tests/test_file_state_cache.py::test_external_write_makes_check_return_stale
func TestFileStateCacheFreshnessStates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.txt")
	mustWrite(t, target, "one")
	cache := newFileStateCache()

	if status, _ := cache.checkFresh(target); status != freshnessUnread {
		t.Fatalf("never-recorded status = %v, want unread", status)
	}
	cache.recordRead(target, []byte("one"))
	if status, _ := cache.checkFresh(target); status != freshnessFresh {
		t.Fatalf("recorded status = %v, want fresh", status)
	}

	// Same size, new bytes, bumped mtime: only the sha256 comparison can tell.
	mustWrite(t, target, "two")
	bump := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(target, bump, bump); err != nil {
		t.Fatal(err)
	}
	status, detail := cache.checkFresh(target)
	if status != freshnessStale || !strings.Contains(detail, "sha256 differs") {
		t.Fatalf("changed status = %v (%q), want stale sha256 detail", status, detail)
	}
}

// Parity: tools/tests/test_file_state_cache.py::test_identical_content_rewrite_stays_fresh
func TestFileStateCacheIdenticalRewriteStaysFresh(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "same.txt")
	mustWrite(t, target, "same")
	cache := newFileStateCache()
	cache.recordRead(target, []byte("same"))

	// An editor rewrites identical bytes, moving mtime only.
	bump := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(target, bump, bump); err != nil {
		t.Fatal(err)
	}
	if status, detail := cache.checkFresh(target); status != freshnessFresh {
		t.Fatalf("identical rewrite = %v (%q), want fresh", status, detail)
	}
}

// Parity-exempt: file_state_cache.py::check_fresh maps a vanished file to
// STALE with this detail; upstream has no direct test for the branch.
func TestFileStateCacheDeletedReportsStale(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gone.txt")
	mustWrite(t, target, "x")
	cache := newFileStateCache()
	cache.recordRead(target, []byte("x"))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	status, detail := cache.checkFresh(target)
	if status != freshnessStale || detail != "file has been deleted since it was read" {
		t.Fatalf("deleted = %v (%q)", status, detail)
	}
}

// Parity-exempt: the 256-entry LRU bound comes from
// file_state_cache.py::_MAX_ENTRIES_PER_SCOPE; upstream does not test it.
func TestFileStateCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newFileStateCache()
	record := fileReadRecord{size: 1, mtimeNs: 1, sha256: "aa"}
	for i := 0; i < maxFileStateEntries; i++ {
		cache.remember(fmt.Sprintf("/tmp/tracked-%03d", i), record)
	}
	cache.remember("/tmp/tracked-000", record) // refresh the oldest entry
	cache.remember("/tmp/overflow", record)

	if got := cache.len(); got != maxFileStateEntries {
		t.Fatalf("cache size = %d, want %d", got, maxFileStateEntries)
	}
	if _, ok := cache.entries["/tmp/tracked-000"]; !ok {
		t.Fatal("refreshed entry was evicted")
	}
	if _, ok := cache.entries["/tmp/tracked-001"]; ok {
		t.Fatal("least-recently-used entry survived eviction")
	}
	if _, ok := cache.entries["/tmp/overflow"]; !ok {
		t.Fatal("new entry was not stored")
	}
}

// Parity-exempt: Go-only concurrency hardening; the upstream cache uses a
// threading.Lock but has no parallel-access test.
func TestFileStateCacheConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "concurrent.txt")
	mustWrite(t, target, "body")
	cache := newFileStateCache()

	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				cache.recordRead(target, []byte("body"))
				cache.checkFresh(target)
			}
		}()
	}
	wg.Wait()

	if status, _ := cache.checkFresh(target); status != freshnessFresh {
		t.Fatalf("post-concurrency status = %v, want fresh", status)
	}
}

// Parity-exempt: file_ops.py::_patch_apply records the post-write state but
// applies no check_fresh guard of its own; upstream has no test for it.
func TestStaleGuardPatchModeRecordsWithoutGuarding(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "p.txt")
	mustWrite(t, target, "value = 1\n")
	tb := New(dir)

	body := "*** Begin Patch\n*** Update File: p.txt\n-value = 1\n+value = 2\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if res.IsError || !strings.Contains(res.Content, "Modified") {
		t.Fatalf("patch = %+v", res)
	}
	if got := mustRead(t, target); got != "value = 2\n" {
		t.Fatalf("content = %q", got)
	}

	// Patch mode recorded the new state, so a follow-up replace edit does not
	// need a separate read.
	edit := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "p.txt",
		"old_string": "value = 2",
		"new_string": "value = 3",
	})
	if edit.IsError || !strings.Contains(edit.Content, "Replaced") {
		t.Fatalf("chained edit after patch = %+v", edit)
	}
}

// ── write_safe_root ──────────────────────────────────────────────────────

func mkdirAll(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyWriteSafeRoot::test_write_outside_safe_root_denied
func TestWriteSafeRootDeniesOutsideCeiling(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	allowed := filepath.Join(base, "allowed")
	mkdirAll(t, home, allowed)
	tb := New(home)
	if err := tb.SetWriteSafeRoots(allowed); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(base, "blocked.txt")
	res := execTool(t, tb, ToolWriteFile, map[string]any{"path": outside, "content": "x"})
	if !res.IsError || !strings.Contains(res.Content, "write denied") || !strings.Contains(res.Content, "write_safe_root") {
		t.Fatalf("ceiling = %+v", res)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file was written: %v", err)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyWriteSafeRoot::test_write_safe_root_accepts_list
func TestWriteSafeRootListAllowsAnyConfiguredRoot(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	mkdirAll(t, a, b)
	tb := New(a)
	if err := tb.SetWriteSafeRoots(a, b); err != nil {
		t.Fatal(err)
	}

	// Relative -> inside the sandbox root (a).
	if res := execTool(t, tb, ToolWriteFile, map[string]any{"path": "x.txt", "content": "1"}); res.IsError {
		t.Fatalf("relative write = %+v", res)
	}
	// Absolute under b: outside the sandbox root, but b is a configured root.
	if res := execTool(t, tb, ToolWriteFile, map[string]any{"path": filepath.Join(b, "y.txt"), "content": "2"}); res.IsError {
		t.Fatalf("write under configured root = %+v", res)
	}
	// Absolute outside both roots.
	res := execTool(t, tb, ToolWriteFile, map[string]any{"path": filepath.Join(base, "outside.txt"), "content": "3"})
	if !res.IsError || !strings.Contains(res.Content, "write denied") {
		t.Fatalf("outside both = %+v", res)
	}

	if got := mustRead(t, filepath.Join(a, "x.txt")); got != "1" {
		t.Fatalf("a/x.txt = %q", got)
	}
	if got := mustRead(t, filepath.Join(b, "y.txt")); got != "2" {
		t.Fatalf("b/y.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(base, "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file was written: %v", err)
	}
}

// Parity: tools/tests/tools/test_file_ops.py::TestPathPolicyWriteSafeRoot::test_write_inside_safe_root_allowed
func TestWriteSafeRootInsideSandboxAllowed(t *testing.T) {
	dir := t.TempDir()
	tb := New(dir)
	if err := tb.SetWriteSafeRoots(dir); err != nil {
		t.Fatal(err)
	}

	res := execTool(t, tb, ToolWriteFile, map[string]any{"path": "ok.txt", "content": "x"})
	if res.IsError {
		t.Fatalf("inside write = %+v", res)
	}
	if got := mustRead(t, filepath.Join(dir, "ok.txt")); got != "x" {
		t.Fatalf("content = %q", got)
	}
}

// Parity-exempt: upstream applies write_safe_root only to writes and leaves
// reads unrestricted; Go keeps the sandbox root as the read boundary, so the
// ceiling can never widen reads.
func TestWriteSafeRootDoesNotWidenReads(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	other := filepath.Join(base, "other")
	mkdirAll(t, home, other)
	mustWrite(t, filepath.Join(other, "secret.txt"), "not yours\n")
	tb := New(home)
	if err := tb.SetWriteSafeRoots(home, other); err != nil {
		t.Fatal(err)
	}

	res := execTool(t, tb, ToolReadFile, map[string]any{"path": filepath.Join(other, "secret.txt")})
	// A read refusal is plain text (file_ops.py policy branch), never content.
	if !strings.Contains(res.Content, "escapes the sandbox root") || strings.Contains(res.Content, "not yours") {
		t.Fatalf("read through ceiling = %+v", res)
	}
}

// Parity-exempt: file_ops.py::_patch_replace routes through
// _FilePolicy.write_path, so the ceiling covers edit_file too; the upstream
// write_safe_root tests only exercise write_file.
func TestWriteSafeRootAppliesToEditReplace(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	other := filepath.Join(base, "other")
	mkdirAll(t, home, other)
	target := filepath.Join(other, "code.py")
	mustWrite(t, target, "value = 1\n")
	tb := New(home)
	if err := tb.SetWriteSafeRoots(home); err != nil {
		t.Fatal(err)
	}

	res := execTool(t, tb, ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       target,
		"old_string": "value = 1",
		"new_string": "value = 2",
	})
	if !res.IsError || !strings.Contains(res.Content, "write denied") {
		t.Fatalf("edit outside ceiling = %+v", res)
	}
	if got := mustRead(t, target); got != "value = 1\n" {
		t.Fatalf("blocked edit changed the file: %q", got)
	}
}

// Parity-exempt: file_ops.py::_patch_apply resolves every op through
// _FilePolicy.write_path; the upstream write_safe_root tests have no patch case.
func TestWriteSafeRootAppliesToPatchMode(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	other := filepath.Join(base, "other")
	mkdirAll(t, home, other)
	target := filepath.Join(other, "code.py")
	mustWrite(t, target, "value = 1\n")
	tb := New(home)
	if err := tb.SetWriteSafeRoots(home); err != nil {
		t.Fatal(err)
	}

	body := "*** Begin Patch\n*** Update File: " + target + "\n-value = 1\n+value = 2\n*** End Patch\n"
	res := execTool(t, tb, ToolEditFile, map[string]any{"mode": "patch", "patch_text": body})
	if !res.IsError || !strings.Contains(res.Content, "write denied") || !strings.Contains(res.Content, "no files were modified") {
		t.Fatalf("patch outside ceiling = %+v", res)
	}
	if got := mustRead(t, target); got != "value = 1\n" {
		t.Fatalf("blocked patch changed the file: %q", got)
	}
}

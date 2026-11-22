package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type fakeCompleter struct {
	mu        sync.Mutex
	requests  []llm.CompletionRequest
	responses []llm.Completion
	err       error
}

func (f *fakeCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.err != nil {
		return llm.Completion{}, f.err
	}
	if len(f.responses) == 0 {
		return llm.Completion{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *fakeCompleter) calls() []llm.CompletionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.CompletionRequest{}, f.requests...)
}

func jsonResponse(selected []string) llm.Completion {
	payload, _ := json.Marshal(map[string]any{"selected_memories": selected})
	return llm.Completion{Content: string(payload)}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Parity: core/tests/test_queen_memory.py::test_parse_frontmatter_valid
func TestParseFrontmatterValid(t *testing.T) {
	fm := ParseFrontmatter("---\nname: foo\ntype: profile\ndescription: bar baz\n---\ncontent")
	want := map[string]string{"name": "foo", "type": "profile", "description": "bar baz"}
	if len(fm) != len(want) {
		t.Fatalf("frontmatter = %v", fm)
	}
	for k, v := range want {
		if fm[k] != v {
			t.Fatalf("frontmatter[%q] = %q", k, fm[k])
		}
	}
}

// Parity: core/tests/test_queen_memory.py::test_parse_frontmatter_missing, ::test_parse_frontmatter_empty,
func TestParseFrontmatterInvalid(t *testing.T) {
	for _, text := range []string{"no frontmatter here", "", "---\n: bad\nno colon\n---\n"} {
		if fm := ParseFrontmatter(text); len(fm) != 0 {
			t.Fatalf("ParseFrontmatter(%q) = %v", text, fm)
		}
	}
}

// Parity: core/tests/test_queen_memory.py::test_parse_global_memory_category_valid,
func TestParseGlobalMemoryCategory(t *testing.T) {
	for _, category := range GlobalMemoryCategories {
		got := ParseGlobalMemoryCategory(&category)
		if got == nil || *got != category {
			t.Fatalf("%q rejected", category)
		}
	}
	padded := "  FEEDBACK  "
	if got := ParseGlobalMemoryCategory(&padded); got == nil || *got != "feedback" {
		t.Fatalf("case-insensitive parse = %v", got)
	}
	for _, invalid := range []string{"goal", "unknown"} {
		if got := ParseGlobalMemoryCategory(&invalid); got != nil {
			t.Fatalf("%q accepted as %q", invalid, *got)
		}
	}
	if got := ParseGlobalMemoryCategory(nil); got != nil {
		t.Fatalf("nil category accepted as %q", *got)
	}
}

// Parity: core/tests/test_queen_memory.py::test_memory_file_from_path
func TestMemoryFileFromPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.md")
	writeFile(t, path, "---\nname: test\ntype: profile\ndescription: a test\n---\nbody\n")
	mf := MemoryFileFromPath(path)
	if mf.Filename != "test.md" {
		t.Fatalf("filename = %q", mf.Filename)
	}
	if mf.Name == nil || *mf.Name != "test" {
		t.Fatalf("name = %v", mf.Name)
	}
	if mf.Type == nil || *mf.Type != "profile" {
		t.Fatalf("type = %v", mf.Type)
	}
	if mf.Description == nil || *mf.Description != "a test" {
		t.Fatalf("description = %v", mf.Description)
	}
	if mf.MTime <= 0 {
		t.Fatalf("mtime = %v", mf.MTime)
	}
}

// Parity: core/tests/test_queen_memory.py::test_memory_file_from_path_no_frontmatter
func TestMemoryFileFromPathNoFrontmatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bare.md")
	writeFile(t, path, "just plain text\n")
	mf := MemoryFileFromPath(path)
	if mf.Name != nil || mf.Type != nil || mf.Description != nil {
		t.Fatalf("bare file has frontmatter: %+v", mf)
	}
	if !strings.Contains(strings.Join(mf.HeaderLines, "\n"), "just plain text") {
		t.Fatalf("header lines = %v", mf.HeaderLines)
	}
}

// Parity: core/tests/test_queen_memory.py::test_memory_file_from_path_missing
func TestMemoryFileFromPathMissing(t *testing.T) {
	mf := MemoryFileFromPath(filepath.Join(t.TempDir(), "missing.md"))
	if mf.Filename != "missing.md" || mf.Name != nil {
		t.Fatalf("missing file parsed = %+v", mf)
	}
}

// Parity: core/tests/test_queen_memory.py::test_scan_memory_files
func TestScanMemoryFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "---\nname: a\n---\n")
	time.Sleep(10 * time.Millisecond)
	writeFile(t, filepath.Join(dir, "b.md"), "---\nname: b\n---\n")
	writeFile(t, filepath.Join(dir, ".hidden.md"), "---\nname: hidden\n---\n")
	writeFile(t, filepath.Join(dir, "not-md.txt"), "ignored")

	files := ScanMemoryFiles(dir)
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Filename
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "a.md") || !strings.Contains(joined, "b.md") {
		t.Fatalf("scan = %v", names)
	}
	if strings.Contains(joined, ".hidden.md") || strings.Contains(joined, "not-md.txt") {
		t.Fatalf("scan kept ignored files: %v", names)
	}
	if names[0] != "b.md" {
		t.Fatalf("newest not first: %v", names)
	}
}

// Parity: core/tests/test_queen_memory.py::test_scan_memory_files_cap
func TestScanMemoryFilesCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 210; i++ {
		writeFile(t, filepath.Join(dir, "mem-"+pad4(i)+".md"), "---\nname: m\n---\n")
	}
	if files := ScanMemoryFiles(dir); len(files) != MaxFiles {
		t.Fatalf("scan returned %d files", len(files))
	}
}

func pad4(n int) string {
	digits := []byte{byte('0' + n/1000%10), byte('0' + n/100%10), byte('0' + n/10%10), byte('0' + n%10)}
	return string(digits)
}

// Parity: core/tests/test_queen_memory.py::test_format_memory_manifest
func TestFormatMemoryManifest(t *testing.T) {
	profile, desc := "profile", "desc a"
	manifest := FormatMemoryManifest([]MemoryFile{
		{Filename: "a.md", Path: "a.md", Type: &profile, Description: &desc},
		{Filename: "b.md", Path: "b.md"},
	})
	for _, want := range []string{"[profile] a.md", "desc a", "[unknown] b.md", "(no description)"} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest %q missing %q", manifest, want)
		}
	}
}

// Parity: core/tests/test_queen_memory.py::test_init_memory_dir
func TestInitMemoryDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memories")
	if err := InitMemoryDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("memory dir missing: %v", err)
	}
}

// Parity: core/tests/test_queen_memory.py::test_select_memories_empty_dir
func TestSelectMemoriesEmptyDirSkipsLLM(t *testing.T) {
	fake := &fakeCompleter{}
	if got := SelectMemories(context.Background(), "hello", fake, t.TempDir(), 5, 1000); len(got) != 0 {
		t.Fatalf("selection = %v", got)
	}
	if len(fake.calls()) != 0 {
		t.Fatalf("LLM called for empty dir: %v", fake.calls())
	}
}

// Parity: core/tests/test_queen_memory.py::test_select_memories_with_files
func TestSelectMemoriesWithFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "---\nname: a\ndescription: about A\ntype: profile\n---\nbody")
	writeFile(t, filepath.Join(dir, "b.md"), "---\nname: b\ndescription: about B\ntype: preference\n---\nbody")

	fake := &fakeCompleter{responses: []llm.Completion{jsonResponse([]string{"a.md"})}}
	got := SelectMemories(context.Background(), "tell me about A", fake, dir, 5, 1000)
	if len(got) != 1 || got[0] != "a.md" {
		t.Fatalf("selection = %v", got)
	}
	if len(fake.calls()) != 1 {
		t.Fatalf("LLM calls = %d", len(fake.calls()))
	}
	req := fake.calls()[0]
	if req.System != SelectMemoriesSystemPrompt || !req.JSONMode {
		t.Fatalf("request = %+v", req)
	}
}

// Parity: core/tests/test_queen_memory.py::test_profiles_never_crowd_out_relevant_memories
func TestProfilesNeverCrowdOutRelevantMemories(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, filepath.Join(dir, "character-"+itoa(i)+".md"),
			"---\nname: c\ndescription: character\ntype: profile\n---\nbody")
	}
	writeFile(t, filepath.Join(dir, "deploy-window.md"),
		"---\nname: d\ndescription: Acme deploy window\ntype: preference\n---\nbody")

	fake := &fakeCompleter{responses: []llm.Completion{jsonResponse([]string{"deploy-window.md"})}}
	got := SelectMemories(context.Background(), "when can we deploy to Acme?", fake, dir, 3, 1000)
	if len(got) == 0 || got[0] != "deploy-window.md" {
		t.Fatalf("relevant memory not first: %v", got)
	}
	if len(got) != 3 {
		t.Fatalf("profiles did not back-fill: %v", got)
	}
}

// Parity: core/tests/test_queen_memory.py::test_profiles_fill_in_when_nothing_is_selected
func TestProfilesFillInWhenNothingSelected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "user-profile.md"),
		"---\nname: u\ndescription: the user\ntype: profile\n---\nbody")
	fake := &fakeCompleter{responses: []llm.Completion{jsonResponse([]string{})}}
	got := SelectMemories(context.Background(), "who is my partner?", fake, dir, 5, 1000)
	if len(got) != 1 || got[0] != "user-profile.md" {
		t.Fatalf("selection = %v", got)
	}
}

// Parity: core/tests/test_queen_memory.py::test_select_memories_error_returns_empty
func TestSelectMemoriesErrorReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "---\nname: a\n---\nbody")
	fake := &fakeCompleter{err: os.ErrDeadlineExceeded}
	if got := SelectMemories(context.Background(), "hello", fake, dir, 5, 1000); len(got) != 0 {
		t.Fatalf("selection = %v", got)
	}
}

// Parity: core/tests/test_queen_memory.py::test_format_recall_injection, ::test_format_recall_injection_custom_label
func TestFormatRecallInjection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "---\nname: a\n---\nbody of a")

	got := FormatRecallInjection([]string{"a.md"}, dir, "")
	if !strings.Contains(got, "Global Memories") || !strings.Contains(got, "body of a") {
		t.Fatalf("injection = %q", got)
	}
	custom := FormatRecallInjection([]string{"a.md"}, dir, "Queen Memories: queen_technology")
	if !strings.Contains(custom, "Queen Memories: queen_technology") || !strings.Contains(custom, "body of a") {
		t.Fatalf("custom injection = %q", custom)
	}
}

// Parity: core/tests/test_queen_memory.py::test_format_recall_injection_empty
func TestFormatRecallInjectionEmpty(t *testing.T) {
	if got := FormatRecallInjection(nil, "", "Global Memories"); got != "" {
		t.Fatalf("empty injection = %q", got)
	}
}

// Parity: core/tests/test_queen_memory.py::test_build_scoped_recall_blocks_includes_global_and_queen
func TestBuildScopedRecallBlocksBothScopes(t *testing.T) {
	root := t.TempDir()
	globalDir, queenDir := filepath.Join(root, "global"), filepath.Join(root, "queen")
	mkdirAll(t, globalDir)
	mkdirAll(t, queenDir)
	writeFile(t, filepath.Join(globalDir, "shared.md"), "---\nname: shared\n---\nshared body")
	writeFile(t, filepath.Join(queenDir, "shared.md"), "---\nname: shared\n---\nqueen body")

	fake := &fakeCompleter{responses: []llm.Completion{
		jsonResponse([]string{"shared.md"}),
		jsonResponse([]string{"shared.md"}),
	}}
	globalBlock, queenBlock := BuildScopedRecallBlocks(
		context.Background(), "help me", fake, globalDir, queenDir, "queen_technology", 3, 3, 1000,
	)
	if !strings.Contains(globalBlock, "Global Memories") || !strings.Contains(globalBlock, "shared body") {
		t.Fatalf("global block = %q", globalBlock)
	}
	if !strings.Contains(queenBlock, "Queen Memories: queen_technology") || !strings.Contains(queenBlock, "queen body") {
		t.Fatalf("queen block = %q", queenBlock)
	}
}

// Parity: core/tests/test_queen_memory.py::test_build_scoped_recall_blocks_tolerates_empty_scope
func TestBuildScopedRecallBlocksEmptyScope(t *testing.T) {
	root := t.TempDir()
	globalDir, queenDir := filepath.Join(root, "global"), filepath.Join(root, "queen")
	mkdirAll(t, globalDir)
	mkdirAll(t, queenDir)
	writeFile(t, filepath.Join(globalDir, "a.md"), "---\nname: a\n---\nglobal body")

	fake := &fakeCompleter{responses: []llm.Completion{jsonResponse([]string{"a.md"})}}
	globalBlock, queenBlock := BuildScopedRecallBlocks(
		context.Background(), "help me", fake, globalDir, queenDir, "queen_technology", 3, 3, 1000,
	)
	if !strings.Contains(globalBlock, "Global Memories") {
		t.Fatalf("global block = %q", globalBlock)
	}
	if queenBlock != "" {
		t.Fatalf("queen block = %q", queenBlock)
	}
	if len(fake.calls()) != 1 {
		t.Fatalf("LLM calls = %d", len(fake.calls()))
	}
}

// scopeBarrierCompleter answers a recall pick only after both scopes' picks
// are in flight: a serial caller times out, a concurrent caller is released at
// once. It also answers the global pick last, so a result mapping that followed
// completion order (instead of scope) would put the blocks in the wrong place.
type scopeBarrierCompleter struct {
	mu        sync.Mutex
	arrived   map[string]bool
	released  chan struct{}
	once      sync.Once
	timedOut_ bool
}

func (c *scopeBarrierCompleter) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	text := ""
	for _, message := range req.Messages {
		text += message.Content
	}
	scope, selected := "global", "global-note.md"
	if strings.Contains(text, "queen-note.md") {
		scope, selected = "queen", "queen-note.md"
	}
	c.mu.Lock()
	c.arrived[scope] = true
	both := c.arrived["global"] && c.arrived["queen"]
	c.mu.Unlock()
	if both {
		c.once.Do(func() { close(c.released) })
	}
	select {
	case <-c.released:
	case <-time.After(2 * time.Second):
		c.mu.Lock()
		c.timedOut_ = true
		c.mu.Unlock()
		return llm.Completion{}, errors.New("scope picks did not overlap")
	}
	if scope == "global" {
		time.Sleep(20 * time.Millisecond)
	}
	payload, err := json.Marshal(map[string]any{"selected_memories": []string{selected}})
	if err != nil {
		return llm.Completion{}, err
	}
	return llm.Completion{Content: string(payload), Model: "barrier"}, nil
}

func (c *scopeBarrierCompleter) timedOut() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timedOut_
}

// TestBuildScopedRecallBlocksRunsScopesConcurrently pins the two-scope pick to
// upstream's asyncio.gather shape: neither scope may complete before the other
// starts, and the returned (global, queen) order is fixed by scope, not by
// whichever pick finished first.
//
// Parity: core/framework/agents/queen/recall_selector.py::build_scoped_recall_blocks
func TestBuildScopedRecallBlocksRunsScopesConcurrently(t *testing.T) {
	root := t.TempDir()
	globalDir, queenDir := filepath.Join(root, "global"), filepath.Join(root, "queen")
	mkdirAll(t, globalDir)
	mkdirAll(t, queenDir)
	writeFile(t, filepath.Join(globalDir, "global-note.md"), "---\nname: g\n---\nglobal body")
	writeFile(t, filepath.Join(queenDir, "queen-note.md"), "---\nname: q\n---\nqueen body")

	barrier := &scopeBarrierCompleter{arrived: map[string]bool{}, released: make(chan struct{})}
	globalBlock, queenBlock := BuildScopedRecallBlocks(
		context.Background(), "do both scopes overlap?", barrier, globalDir, queenDir, "queen_x", 3, 3, 1000,
	)
	if barrier.timedOut() {
		t.Fatal("scope picks did not overlap: one pick finished before the other started")
	}
	if !strings.Contains(globalBlock, "--- Global Memories ---") || !strings.Contains(globalBlock, "global body") {
		t.Fatalf("global block = %q, want the global note", globalBlock)
	}
	if !strings.Contains(queenBlock, "--- Queen Memories: queen_x ---") || !strings.Contains(queenBlock, "queen body") {
		t.Fatalf("queen block = %q, want the queen note", queenBlock)
	}
}

// Parity: core/tests/test_queen_memory.py::test_recall_says_how_long_ago_a_memory_was_last_confirmed
func TestRecallSaysHowLongAgoAMemoryWasLastConfirmed(t *testing.T) {
	dir := t.TempDir()
	confirmed := time.Now().AddDate(0, 0, -10)
	if _, err := Write(dir, "tea.md", "---\nname: tea\ntype: preference\ndescription: what Ava drinks\n---\nAva drinks oolong.", WriteOptions{Author: "reflection", Now: confirmed}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := FormatRecallInjection([]string{"tea.md"}, dir, "")
	if !strings.Contains(got, "### tea.md (last confirmed 10 days ago)") {
		t.Fatalf("injection = %q", got)
	}
}

// Parity-exempt: upstream's _format_relative_age falls back to the file's
// mtime for notes written before confirmed days were recorded; this pins that
// fallback and the two-day quiet period.
func TestRecallAgeFallsBackToMtimeAndStaysQuietForTwoDays(t *testing.T) {
	today := time.Now()
	confirmedContent := "---\nname: tea\n---\nbody"
	recent, _ := Write(t.TempDir(), "tea.md", confirmedContent, WriteOptions{Author: "reflection", Now: today.AddDate(0, 0, -2)})
	if age := FormatRelativeAge(recent, float64(today.Unix()), today); age != nil {
		t.Fatalf("two days reported %q", *age)
	}
	mtime := today.AddDate(0, 0, -5)
	age := FormatRelativeAge("---\nname: tea\n---\nbody", float64(mtime.Unix()), today)
	if age == nil || *age != "last confirmed 5 days ago" {
		t.Fatalf("mtime fallback = %v, want last confirmed 5 days ago", age)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

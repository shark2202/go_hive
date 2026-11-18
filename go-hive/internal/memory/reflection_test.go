package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

func toolCall(id, name string, input map[string]any) llm.ToolCall {
	payload, _ := json.Marshal(input)
	return llm.ToolCall{ID: id, Name: name, Arguments: string(payload)}
}

func callsCompletion(calls ...llm.ToolCall) llm.Completion {
	return llm.Completion{ToolCalls: calls}
}

func textCompletion(text string) llm.Completion {
	return llm.Completion{Content: text}
}

func writePart(t *testing.T, sessionDir string, i int, role, content string) {
	t.Helper()
	partsDir := filepath.Join(sessionDir, "conversations", "parts")
	mkdirAll(t, partsDir)
	part, _ := json.Marshal(map[string]any{"role": role, "content": content})
	writeFile(t, filepath.Join(partsDir, pad10(i)+".json"), string(part))
}

func pad10(n int) string {
	digits := make([]byte, 10)
	for i := 9; i >= 0; i-- {
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits)
}

func seedConversation(t *testing.T, sessionDir string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		writePart(t, sessionDir, i, role, "message "+itoa(i))
	}
}

func resetMemoryState(t *testing.T) {
	t.Helper()
	ResetReflectionState()
	t.Cleanup(ResetReflectionState)
}

// Parity: tests/test_queen_memory.py::test_short_reflection
func TestShortReflection(t *testing.T) {
	resetMemoryState(t)
	sessionDir := filepath.Join(t.TempDir(), "session")
	seedConversation(t, sessionDir, 3)
	memDir := t.TempDir()

	fake := &fakeCompleter{responses: []llm.Completion{
		callsCompletion(toolCall("tc_1", "write_memory_file", map[string]any{
			"filename": "user-likes-tests.md",
			"content": "---\nname: user-likes-tests\n" +
				"type: preference\n" +
				"description: User values thorough testing\n" +
				"---\nObserved emphasis on test coverage.",
		})),
		textCompletion("Done reflecting."),
	}}

	result := RunShortReflection(context.Background(), sessionDir, fake, memDir)
	if len(result.ChangedFiles) != 1 || result.ChangedFiles[0] != "global:user-likes-tests.md" {
		t.Fatalf("changed files = %v", result.ChangedFiles)
	}
	written := filepath.Join(memDir, "user-likes-tests.md")
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("memory not written: %v", err)
	}
	if !strings.Contains(string(data), "user-likes-tests") {
		t.Fatalf("content = %q", string(data))
	}
	if len(fake.calls()) != 2 {
		t.Fatalf("LLM calls = %d", len(fake.calls()))
	}
}

// Parity: tests/test_queen_memory.py::test_queen_short_reflection_writes_only_queen_scope
func TestQueenShortReflectionWritesOnlyQueenScope(t *testing.T) {
	resetMemoryState(t)
	sessionDir := filepath.Join(t.TempDir(), "session")
	seedConversation(t, sessionDir, 3)
	globalDir := t.TempDir()
	queenDir := t.TempDir()

	fake := &fakeCompleter{responses: []llm.Completion{
		callsCompletion(toolCall("tc_1", "write_memory_file", map[string]any{
			"filename": "technology-workflow.md",
			"content": "---\nname: technology-workflow\n" +
				"type: preference\n" +
				"description: User prefers implementation-first technical help\n" +
				"---\nFor technical work, the user wants concrete implementation details.",
		})),
		textCompletion("Done reflecting."),
	}}

	RunQueenShortReflection(context.Background(), sessionDir, fake, "queen_technology", queenDir)

	if _, err := os.Stat(filepath.Join(queenDir, "technology-workflow.md")); err != nil {
		t.Fatalf("queen memory missing: %v", err)
	}
	if files := ScanMemoryFiles(globalDir); len(files) != 0 {
		t.Fatalf("global memory touched: %v", files)
	}
}

// Parity: tests/test_queen_memory.py::test_unified_short_reflection_can_write_both_scopes_in_one_loop
func TestUnifiedShortReflectionWritesBothScopes(t *testing.T) {
	resetMemoryState(t)
	sessionDir := filepath.Join(t.TempDir(), "session")
	seedConversation(t, sessionDir, 3)
	globalDir := t.TempDir()
	queenDir := t.TempDir()

	fake := &fakeCompleter{responses: []llm.Completion{
		callsCompletion(
			toolCall("tc_1", "write_memory_file", map[string]any{
				"scope":    "global",
				"filename": "user-profile.md",
				"content":  "---\nname: User Profile\ntype: profile\ndescription: Shared user profile\n---\nShared profile body.",
			}),
			toolCall("tc_2", "write_memory_file", map[string]any{
				"scope":    "queen",
				"filename": "technology-preferences.md",
				"content": "---\nname: technology-preferences\n" +
					"type: preference\n" +
					"description: Technical execution preferences\n" +
					"---\nThe user wants implementation-first technical answers.",
			}),
		),
		textCompletion("Done reflecting."),
	}}

	result := RunUnifiedShortReflection(context.Background(), sessionDir, fake, globalDir, queenDir, "queen_technology")

	if _, err := os.Stat(filepath.Join(globalDir, "user-profile.md")); err != nil {
		t.Fatalf("global memory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(queenDir, "technology-preferences.md")); err != nil {
		t.Fatalf("queen memory missing: %v", err)
	}
	if len(fake.calls()) != 2 {
		t.Fatalf("LLM calls = %d", len(fake.calls()))
	}
	want := "global:user-profile.md,queen:technology-preferences.md"
	if got := strings.Join(result.ChangedFiles, ","); got != want {
		t.Fatalf("changed files = %v", result.ChangedFiles)
	}
}

// Parity: tests/test_queen_memory.py::test_short_reflection_rejects_non_global_types
func TestShortReflectionRejectsNonGlobalTypes(t *testing.T) {
	memDir := t.TempDir()
	result := ExecuteTool("write_memory_file", map[string]any{
		"filename": "bad-type.md",
		"content":  "---\nname: bad\ntype: goal\n---\nbody",
	}, []ScopeDir{{Scope: "global", Dir: memDir}}, "")
	if !strings.Contains(result, "ERROR") {
		t.Fatalf("result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(memDir, "bad-type.md")); err == nil {
		t.Fatal("rejected file was written")
	}
	want := "ERROR: Invalid memory type 'goal'. Allowed types: profile, preference, environment, feedback."
	if result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

// Parity: tests/test_queen_memory.py::test_long_reflection
func TestLongReflectionMergesAndDeletes(t *testing.T) {
	memDir := t.TempDir()
	writeFile(t, filepath.Join(memDir, "dup-a.md"),
		"---\nname: dup-a\ntype: profile\ndescription: profile A\n---\nProfile A details.")
	writeFile(t, filepath.Join(memDir, "dup-b.md"),
		"---\nname: dup-b\ntype: profile\ndescription: profile A dup\n---\nSame profile A.")

	fake := &fakeCompleter{responses: []llm.Completion{
		callsCompletion(toolCall("tc_1", "list_memory_files", map[string]any{})),
		callsCompletion(
			toolCall("tc_2", "write_memory_file", map[string]any{
				"filename": "dup-a.md",
				"content":  "---\nname: dup-a\ntype: profile\ndescription: profile A (merged)\n---\nProfile A details. Also same profile A.",
			}),
			toolCall("tc_3", "delete_memory_file", map[string]any{"filename": "dup-b.md"}),
		),
		textCompletion("Housekeeping complete."),
	}}

	RunLongReflection(context.Background(), fake, memDir, "global")

	if _, err := os.Stat(filepath.Join(memDir, "dup-b.md")); err == nil {
		t.Fatal("dup-b.md still exists")
	}
	data, err := os.ReadFile(filepath.Join(memDir, "dup-a.md"))
	if err != nil {
		t.Fatalf("dup-a.md missing: %v", err)
	}
	if !strings.Contains(string(data), "merged") {
		t.Fatalf("dup-a.md = %q", string(data))
	}
}

// Parity: tests/test_queen_memory.py::test_subscribe_reflection_triggers_runs_housekeeping_for_both_scopes
func TestSubscribeReflectionTriggersRunsHousekeepingForBothScopes(t *testing.T) {
	shortCalls, longCalls := 0, 0
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig {
			return ReflectionConfig{EveryTurns: 2, LongEvery: 2}
		},
		Spawn: func(work func()) { work() },
		Short: func(context.Context) LoopResult { shortCalls++; return LoopResult{Success: true} },
		Long:  func(context.Context) LoopResult { longCalls++; return LoopResult{Success: true} },
	}

	ctx := context.Background()
	for _, reason := range []string{"turn_done", "ask_user", "llm_error", "turn_done", "turn_done", "turn_done"} {
		scheduler.OnTurnDone(ctx, "queen", reason)
	}
	scheduler.OnTurnDone(ctx, "worker", "turn_done")

	if shortCalls != 2 {
		t.Fatalf("short reflections = %d, want 2", shortCalls)
	}
	if longCalls != 1 {
		t.Fatalf("long reflections = %d, want 1", longCalls)
	}
}

// Parity: tests/test_queen_memory.py::test_short_reflection_skips_a_conversation_that_has_not_grown
func TestShortReflectionSkipsConversationThatHasNotGrown(t *testing.T) {
	resetMemoryState(t)
	sessionDir := filepath.Join(t.TempDir(), "session")
	seedConversation(t, sessionDir, 2)
	globalDir := t.TempDir()
	fake := &fakeCompleter{responses: []llm.Completion{textCompletion("Nothing to save.")}}

	reflect := func() {
		RunShutdownReflection(context.Background(), sessionDir, fake, "", globalDir, "", "")
	}
	reflect()
	if got := len(fake.calls()); got != 1 {
		t.Fatalf("after first reflection: calls = %d", got)
	}
	reflect()
	if got := len(fake.calls()); got != 1 {
		t.Fatalf("unchanged session was reflected again: calls = %d", got)
	}
	writePart(t, sessionDir, 2, "user", "message 2")
	reflect()
	if got := len(fake.calls()); got != 2 {
		t.Fatalf("grown session not reflected: calls = %d", got)
	}
}

// Parity: tests/test_queen_memory.py::test_a_restored_session_is_not_reflected_on_until_someone_speaks
func TestRestoredSessionNotReflectedUntilSomeoneSpeaks(t *testing.T) {
	resetMemoryState(t)
	sessionDir := filepath.Join(t.TempDir(), "session")
	seedConversation(t, sessionDir, 2)
	globalDir := t.TempDir()
	fake := &fakeCompleter{responses: []llm.Completion{textCompletion("Nothing to save.")}}

	(&ReflectionScheduler{SessionDir: sessionDir}).InitializeReflectionState()
	RunShutdownReflection(context.Background(), sessionDir, fake, "", globalDir, "", "")
	if got := len(fake.calls()); got != 0 {
		t.Fatalf("restored session reflected: calls = %d", got)
	}

	writePart(t, sessionDir, 2, "user", "message 2")
	RunShutdownReflection(context.Background(), sessionDir, fake, "", globalDir, "", "")
	if got := len(fake.calls()); got != 1 {
		t.Fatalf("new message not reflected: calls = %d", got)
	}
}

// Parity: tests/test_queen_memory.py::test_shutdown_reflection_writes_global_and_queen_scope
func TestShutdownReflectionWritesGlobalAndQueenScope(t *testing.T) {
	resetMemoryState(t)
	sessionDir := filepath.Join(t.TempDir(), "session")
	seedConversation(t, sessionDir, 3)
	globalDir := t.TempDir()
	queenDir := t.TempDir()

	fake := &fakeCompleter{responses: []llm.Completion{
		callsCompletion(
			toolCall("tc_1", "write_memory_file", map[string]any{
				"scope":    "global",
				"filename": "user-profile.md",
				"content":  "---\nname: User Profile\ntype: profile\ndescription: Shared user profile\n---\nShared profile body.",
			}),
			toolCall("tc_2", "write_memory_file", map[string]any{
				"scope":    "queen",
				"filename": "technology-preferences.md",
				"content": "---\nname: technology-preferences\n" +
					"type: preference\n" +
					"description: Technical execution preferences\n" +
					"---\nThe user wants implementation-first technical answers.",
			}),
		),
		textCompletion("Done reflecting."),
	}}

	RunShutdownReflection(context.Background(), sessionDir, fake, "", globalDir, queenDir, "queen_technology")

	if _, err := os.Stat(filepath.Join(globalDir, "user-profile.md")); err != nil {
		t.Fatalf("global memory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(queenDir, "technology-preferences.md")); err != nil {
		t.Fatalf("queen memory missing: %v", err)
	}
}

// Parity: tests/test_queen_memory.py::test_path_traversal_read
func TestPathTraversalRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "safe.md"), "safe content")
	result := ExecuteTool("read_memory_file", map[string]any{"filename": "../../etc/passwd"},
		[]ScopeDir{{Scope: "global", Dir: dir}}, "")
	if !strings.Contains(result, "ERROR") {
		t.Fatalf("result = %q", result)
	}
}

// Parity: tests/test_queen_memory.py::test_path_traversal_write
func TestPathTraversalWrite(t *testing.T) {
	dir := t.TempDir()
	result := ExecuteTool("write_memory_file", map[string]any{
		"filename": "../escape.md",
		"content":  "---\nname: evil\n---\nbad",
	}, []ScopeDir{{Scope: "global", Dir: dir}}, "")
	if !strings.Contains(result, "ERROR") {
		t.Fatalf("result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); err == nil {
		t.Fatal("escape file was written")
	}
}

// Parity: tests/test_queen_memory.py::test_safe_path_accepted
func TestSafePathAccepted(t *testing.T) {
	dir := t.TempDir()
	dirs := []ScopeDir{{Scope: "global", Dir: dir}}

	if result := ExecuteTool("write_memory_file", map[string]any{
		"filename": "good-file.md",
		"content":  "---\nname: good\ntype: profile\n---\ncontent",
	}, dirs, ""); !strings.Contains(result, "Wrote") {
		t.Fatalf("write result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "good-file.md")); err != nil {
		t.Fatalf("file missing: %v", err)
	}
	if result := ExecuteTool("read_memory_file", map[string]any{"filename": "good-file.md"}, dirs, ""); !strings.Contains(result, "content") {
		t.Fatalf("read result = %q", result)
	}
	if result := ExecuteTool("delete_memory_file", map[string]any{"filename": "good-file.md"}, dirs, ""); !strings.Contains(result, "Deleted") {
		t.Fatalf("delete result = %q", result)
	}
}

// Parity: core/tests/test_queen_memory.py::test_unified_short_reflection_can_write_both_scopes_in_one_loop
// Parity: core/tests/test_queen_memory.py::test_queen_short_reflection_writes_only_queen_scope
func TestResolveMemoryScope(t *testing.T) {
	both := []ScopeDir{{Scope: "global", Dir: "/g"}, {Scope: "queen", Dir: "/q"}}
	if scope, err := ResolveMemoryScope(map[string]any{}, both); err != nil || scope != "global" {
		t.Fatalf("default scope = %q, %v", scope, err)
	}
	single := []ScopeDir{{Scope: "queen", Dir: "/q"}}
	if scope, err := ResolveMemoryScope(map[string]any{}, single); err != nil || scope != "queen" {
		t.Fatalf("sole scope = %q, %v", scope, err)
	}
	if scope, err := ResolveMemoryScope(map[string]any{"scope": " QUEEN "}, both); err != nil || scope != "queen" {
		t.Fatalf("normalised scope = %q, %v", scope, err)
	}
	_, err := ResolveMemoryScope(map[string]any{"scope": "colony"}, both)
	want := "Invalid scope 'colony'. Available scopes: global, queen."
	if err == nil || err.Error() != want {
		t.Fatalf("invalid scope error = %v", err)
	}
}

// Parity: core/tests/test_queen_memory.py::test_long_reflection
func TestListMemoryFilesTool(t *testing.T) {
	globalDir, queenDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(globalDir, "shared.md"),
		"---\nname: shared\ntype: profile\ndescription: global fact\n---\nbody")
	writeFile(t, filepath.Join(queenDir, "queen-only.md"),
		"---\nname: queen-only\ntype: preference\ndescription: queen fact\n---\nbody")
	dirs := []ScopeDir{{Scope: "global", Dir: globalDir}, {Scope: "queen", Dir: queenDir}}

	all := ExecuteTool("list_memory_files", map[string]any{}, dirs, "q1")
	for _, want := range []string{"## Scope: global", "## Scope: queen (q1)", "shared.md", "queen-only.md"} {
		if !strings.Contains(all, want) {
			t.Fatalf("unscoped listing %q missing %q", all, want)
		}
	}

	scoped := ExecuteTool("list_memory_files", map[string]any{"scope": "global"}, dirs, "q1")
	if !strings.Contains(scoped, "shared.md") || strings.Contains(scoped, "queen-only.md") {
		t.Fatalf("scoped listing = %q", scoped)
	}

	empty := ExecuteTool("list_memory_files", map[string]any{"scope": "queen"}, []ScopeDir{{Scope: "queen", Dir: t.TempDir()}}, "q1")
	if empty != "(no queen memory files yet)" {
		t.Fatalf("empty listing = %q", empty)
	}

	manifest := FormatMultiScopeManifest([]ScopeDir{{Scope: "global", Dir: t.TempDir()}}, "")
	if !strings.Contains(manifest, "## Scope: global") || !strings.Contains(manifest, "(no memory files yet)") {
		t.Fatalf("empty manifest = %q", manifest)
	}
}

// Parity: core/tests/test_queen_memory.py::test_path_traversal_read
// Parity: core/tests/test_queen_memory.py::test_path_traversal_write
// Parity: core/tests/test_queen_memory.py::test_safe_path_accepted
func TestSafeMemoryPathErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := SafeMemoryPath(" bad.md", dir); err == nil || !strings.Contains(err.Error(), "Invalid filename: ' bad.md'") {
		t.Fatalf("whitespace error = %v", err)
	}
	if _, err := SafeMemoryPath("a/b.md", dir); err == nil ||
		err.Error() != "Invalid filename: path components not allowed: 'a/b.md'" {
		t.Fatalf("path component error = %v", err)
	}
	if _, err := SafeMemoryPath("..hidden.md", dir); err == nil ||
		err.Error() != "Invalid filename: path components not allowed: '..hidden.md'" {
		t.Fatalf("dotdot error = %v", err)
	}
	path, err := SafeMemoryPath("ok.md", dir)
	if err != nil {
		t.Fatalf("valid filename rejected: %v", err)
	}
	if filepath.Dir(path) != mustAbs(t, dir) {
		t.Fatalf("resolved path = %q", path)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		return resolved
	}
	return abs
}

// Parity-exempt: the Python reflection prompt text in reflection_agent.py is
func TestReflectionPrompts(t *testing.T) {
	short := BuildUnifiedShortReflectSystem("queen_technology")
	for _, want := range []string{
		"profile, preference, environment, feedback",
		"- `queen`: durable learnings specific to how queen 'queen_technology' should work with this user",
		"user-profile.md",
		"Do NOT exceed 4096 bytes per file or 200 total files per scope.",
	} {
		if !strings.Contains(short, want) {
			t.Fatalf("short prompt missing %q", want)
		}
	}
	if strings.Contains(BuildUnifiedShortReflectSystem(""), "durable learnings specific") {
		t.Fatal("global-only prompt advertises a queen scope")
	}

	long := BuildUnifiedLongReflectSystem("queen_technology")
	for _, want := range []string{
		"periodic housekeeping pass",
		"max 200 files and 4096 bytes per file in each scope",
		"Do NOT invent new information",
	} {
		if !strings.Contains(long, want) {
			t.Fatalf("long prompt missing %q", want)
		}
	}
	if strings.Contains(BuildUnifiedLongReflectSystem(""), "memories specific to how queen") {
		t.Fatal("global-only long prompt advertises a queen scope")
	}
}

// Parity-exempt: reflection_agent._write_error (reflection_agent.py:960) has no
func TestWriteReflectionError(t *testing.T) {
	dir := t.TempDir()
	WriteReflectionError("unified short reflection", dir, errors.New("reflector exploded"))
	data, err := os.ReadFile(filepath.Join(dir, ".reflection_error.txt"))
	if err != nil {
		t.Fatalf("error file missing: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "context: unified short reflection") || !strings.Contains(text, "reflector exploded") {
		t.Fatalf("error file = %q", text)
	}
	WriteReflectionError("ignored", "", os.ErrClosed)
}

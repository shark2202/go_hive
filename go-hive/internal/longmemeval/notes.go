package longmemeval

// Memory notes for the benchmark's seeded history: the Go counterpart of the
// upstream harness's HIVE_LME_NOTES=1 switch.
//
// In production a reflection reads each finished queen conversation and keeps
// notes about the user; the benchmark's history never ran here, so upstream
// reflects on each seeded session once, oldest first, each seeing the notes
// the earlier ones left (tests/e2e/longmemeval.py::build_notes). The same
// reflection building blocks the runtime uses (memory.ReflectionLoop with the
// unified short-reflect prompt and the reflection tool set) are driven here
// from the question's haystack, exactly as the harness does from its item
// messages, so the seeded home boots with the notes production would have
// left.
//
// Notes are cached per question under
// <cache>/<model>/<prompt version>/<variant>_<question id>, so only the first
// run over a question pays for the reflection calls — the same "cached after"
// contract upstream documents.
//
// Parity: tests/e2e/longmemeval.py::build_notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

const (
	// NotesTranscriptMessageCap keeps the last N messages of a session,
	// mirroring upstream's messages[-50:].
	NotesTranscriptMessageCap = 50
	// NotesTranscriptCharCap truncates one message's transcript line,
	// mirroring upstream's content[:800] + "…".
	NotesTranscriptCharCap = 800
)

// NotesConfig configures one home's note build.
type NotesConfig struct {
	// Writer completes the reflection loops (the notes model).
	Writer llm.Completer
	// CacheDir is the notes cache root (<data dir>/notes_cache).
	CacheDir string
	// Model names the model segment of the cache path.
	Model string
	// Variant is the dataset variant ("s" or "m").
	Variant string
	// QueenID scopes the queen memory directory.
	QueenID string
	// MaxTurns bounds one reflection loop (0 = production default).
	MaxTurns int
	// MaxTokens bounds each reflection call (0 = production default).
	MaxTokens int
}

// NotesResult reports one home's note build.
type NotesResult struct {
	// Count is how many .md notes the home's memories hold.
	Count int
	// Cached reports that the notes came from the cache instead of a build.
	Cached bool
	// Failed counts the sessions whose reflection call failed. Upstream
	// logs and moves on, so one flaky call costs that session's notes, not
	// the question; a build where every reflection failed is an error.
	Failed int
}

// NotesVersion hashes the reflection prompt and tool definitions the way
// upstream names a cached version (sha256(prompt + tools JSON)[:12]): a
// prompt change invalidates the cache instead of mixing note styles.
func NotesVersion(queenID string) string {
	params := make([]any, 0, len(memory.ReflectionTools))
	for _, tool := range memory.ReflectionTools {
		params = append(params, tool.Parameters)
	}
	encoded, _ := json.Marshal(params)
	sum := sha256.Sum256([]byte(memory.BuildUnifiedShortReflectSystem(queenID) + string(encoded)))
	return hex.EncodeToString(sum[:])[:12]
}

// NotesCachePath is one question's cache directory.
func NotesCachePath(cfg NotesConfig, questionID string) string {
	return filepath.Join(cfg.CacheDir, SafeModelName(cfg.Model), NotesVersion(cfg.QueenID), cfg.Variant+"_"+questionID)
}

// BuildNotesForHome equips homeRoot with the item's memory notes: it copies
// the question's cache when one exists and otherwise reflects on each seeded
// session (oldest first) and caches the result. It is the Go side of
// HIVE_LME_NOTES=1 and the build step the runner performs before asking.
func BuildNotesForHome(ctx context.Context, cfg NotesConfig, homeRoot string, item Item) (NotesResult, error) {
	if cfg.Writer == nil {
		return NotesResult{}, fmt.Errorf("notes %s: no notes model configured", item.QuestionID)
	}
	if strings.TrimSpace(homeRoot) == "" {
		return NotesResult{}, fmt.Errorf("notes %s: empty hive home", item.QuestionID)
	}
	memoriesRoot := filepath.Join(homeRoot, "memories")
	globalDir := memory.GlobalMemoryDir(homeRoot)
	queenDir := memory.QueenMemoryDir(homeRoot, cfg.QueenID)
	for _, dir := range []string{globalDir, queenDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return NotesResult{}, fmt.Errorf("notes %s: %w", item.QuestionID, err)
		}
	}

	cached := NotesCachePath(cfg, item.QuestionID)
	if info, err := os.Stat(cached); err == nil && info.IsDir() {
		if err := copyTree(cached, memoriesRoot); err != nil {
			return NotesResult{}, fmt.Errorf("notes %s: copy cache: %w", item.QuestionID, err)
		}
		return NotesResult{Count: countMarkdownFiles(memoriesRoot), Cached: true}, nil
	}

	if err := ValidateItem(&item); err != nil {
		return NotesResult{}, err
	}
	dirs := []memory.ScopeDir{
		{Scope: "global", Dir: globalDir},
		{Scope: "queen", Dir: queenDir},
	}
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = memory.ReflectionMaxTurns
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = memory.ReflectionTokenBudget()
	}
	system := memory.BuildUnifiedShortReflectSystem(cfg.QueenID)
	order := notesSessionOrder(item)
	failed := 0
	for _, index := range order {
		userMsg, ok := notesReflectionMessage(item, index)
		if !ok {
			continue
		}
		result := memory.ReflectionLoop(ctx, cfg.Writer, system, userMsg, dirs, maxTurns, maxTokens, cfg.QueenID)
		if !result.Success {
			// Upstream's reflection returns without raising when one call
			// fails; the session's notes are lost, the question continues.
			failed++
		}
	}
	if err := ctx.Err(); err != nil {
		return NotesResult{}, err
	}
	if len(order) > 0 && failed == len(order) {
		return NotesResult{}, fmt.Errorf("notes %s: every reflection failed (%d sessions)", item.QuestionID, failed)
	}

	count := countMarkdownFiles(memoriesRoot)
	if err := writeNotesCache(memoriesRoot, cached); err != nil {
		return NotesResult{}, fmt.Errorf("notes %s: write cache: %w", item.QuestionID, err)
	}
	return NotesResult{Count: count, Cached: false, Failed: failed}, nil
}

// notesSessionOrder sorts the haystack sessions oldest first, the order a
// session's reflection would have run in history.
func notesSessionOrder(item Item) []int {
	order := make([]int, len(item.HaystackDates))
	for i := range order {
		order[i] = i
	}
	dates := make([]time.Time, len(item.HaystackDates))
	for i, raw := range item.HaystackDates {
		parsed, err := ParseDate(raw)
		if err != nil {
			parsed = time.Time{}
		}
		dates[i] = parsed
	}
	sort.SliceStable(order, func(a, b int) bool { return dates[order[a]].Before(dates[order[b]]) })
	return order
}

// notesReflectionMessage renders one session's reflection user message the way
// upstream's reflection_agent._reflect_on does: the last 50 non-empty
// messages, "[user]: " / "[assistant]: " transcript lines capped at 800
// characters each, headed by the full message count and stamped with the
// session's own date.
func notesReflectionMessage(item Item, index int) (string, bool) {
	turns := item.HaystackSessions[index]
	messages := make([]Turn, 0, len(turns))
	for _, turn := range turns {
		if turn.Content == "" {
			continue
		}
		messages = append(messages, turn)
	}
	if len(messages) == 0 {
		return "", false
	}
	start := 0
	if len(messages) > NotesTranscriptMessageCap {
		start = len(messages) - NotesTranscriptMessageCap
	}
	lines := make([]string, 0, len(messages)-start)
	for _, message := range messages[start:] {
		content := strings.TrimSpace(message.Content)
		if message.Role == "tool" || content == "" {
			continue
		}
		label := "assistant"
		if message.Role == "user" {
			label = "user"
		}
		if runes := []rune(content); len(runes) > NotesTranscriptCharCap {
			content = string(runes[:NotesTranscriptCharCap]) + "…"
		}
		lines = append(lines, "["+label+"]: "+content)
	}
	if len(lines) == 0 {
		return "", false
	}
	stamp := time.Now().Format("2006-01-02T15:04")
	if parsed, err := ParseDate(item.HaystackDates[index]); err == nil {
		stamp = parsed.Format("2006-01-02T15:04")
	}
	userMsg := fmt.Sprintf("## Recent conversation (%d messages total)\n\n%s\n\nTimestamp: %s",
		len(messages), strings.Join(lines, "\n"), stamp)
	return userMsg, true
}

// writeNotesCache stores a built memories tree as the question's cache,
// following upstream's build-part-then-rename contract so a killed build
// never leaves a half-written cache that a later run would copy.
func writeNotesCache(memoriesRoot, cached string) error {
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return err
	}
	part := cached + ".part"
	if err := os.RemoveAll(part); err != nil {
		return err
	}
	if info, err := os.Stat(memoriesRoot); err == nil && info.IsDir() {
		if err := copyTree(memoriesRoot, part); err != nil {
			return err
		}
	} else if err := os.MkdirAll(part, 0o755); err != nil {
		return err
	}
	return os.Rename(part, cached)
}

// copyTree copies a directory tree (files only; the memories trees carry no
// links or special files).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// countMarkdownFiles counts the .md notes under dir, as upstream's
// sum(1 for _ in memories.rglob("*.md")) does.
func countMarkdownFiles(dir string) int {
	count := 0
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			count++
		}
		return nil
	})
	return count
}

// NotesTelemetry summarizes how a question's notes reached the queen: the
// result-row fields upstream's record() adds when notes were built.
//
// Parity: tests/e2e/longmemeval.py::record (the notes block)
func NotesTelemetry(homeRoot string, notes NotesResult, answer Answer) map[string]any {
	extra := map[string]any{
		"notes":          notes.Count,
		"notes_mode":     memory.GetMemoryRecallConfig(homeRoot).Mode,
		"notes_recalled": false,
		"notes_indexed":  false,
		"notes_read":     []string{},
	}
	indexHeader := strings.SplitN(agentloop.MemoryIndexHeader, ":", 2)[0]
	read := map[string]struct{}{}
	for _, part := range answer.Turns {
		content, _ := part["content"].(string)
		if strings.Contains(content, agentloop.MemoryRecallHeader) {
			extra["notes_recalled"] = true
		}
		if strings.Contains(content, indexHeader) {
			extra["notes_indexed"] = true
		}
		for _, call := range notesToolCalls(part) {
			for _, match := range memoryNoteNamePattern.FindAllString(notesToolCallInput(call), -1) {
				read[match] = struct{}{}
			}
		}
	}
	if len(read) > 0 {
		names := make([]string, 0, len(read))
		for name := range read {
			names = append(names, name)
		}
		sort.Strings(names)
		extra["notes_read"] = names
	}
	return extra
}

// memoryNoteNamePattern picks note filenames out of a tool call's arguments,
// as upstream's notes_read re.findall(r"([\w.-]+\.md)") does.
var memoryNoteNamePattern = regexp.MustCompile(`[A-Za-z0-9_.-]+\.md`)

// notesToolCalls lists one transcript part's tool calls (the raw maps the
// runtime persisted; tests may hand in a typed slice).
func notesToolCalls(part map[string]any) []map[string]any {
	if typed, ok := part["tool_calls"].([]map[string]any); ok {
		return typed
	}
	items, _ := part["tool_calls"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

// notesToolCallInput is a tool call's raw argument text.
func notesToolCallInput(call map[string]any) string {
	function, _ := call["function"].(map[string]any)
	if function == nil {
		return ""
	}
	arguments, _ := function["arguments"].(string)
	return arguments
}

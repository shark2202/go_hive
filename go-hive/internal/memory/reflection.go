package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

type ScopeDir struct {
	Scope string
	Dir   string
}

var memoryCategoriesString = strings.Join(GlobalMemoryCategories, ", ")

var ReflectionTools = []llm.ToolDef{
	{
		Name:        "list_memory_files",
		Description: "List memory files with their type, name, and description. When scope is omitted, returns all scopes grouped by scope.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope": map[string]any{
					"type":        "string",
					"description": "Optional scope to inspect: 'global' or 'queen'.",
				},
			},
			"additionalProperties": false,
		},
	},
	{
		Name:        "read_memory_file",
		Description: "Read the full content of a memory file by filename from a scope.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{
					"type":        "string",
					"description": "The filename (e.g. 'user-prefers-dark-mode.md').",
				},
				"scope": map[string]any{
					"type":        "string",
					"description": "Memory scope: 'global' or 'queen'. Defaults to 'global'.",
				},
			},
			"required":             []string{"filename"},
			"additionalProperties": false,
		},
	},
	{
		Name: "write_memory_file",
		Description: fmt.Sprintf(
			"Create or overwrite a memory file.  Content should include YAML frontmatter (name, description, type) followed by the memory body.  "+
				"Max file size: %d bytes.  Max files: %d.", MaxFileSizeBytes, MaxFiles,
		),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{
					"type":        "string",
					"description": "Filename ending in .md (e.g. 'user-prefers-dark-mode.md').",
				},
				"scope": map[string]any{
					"type":        "string",
					"description": "Memory scope: 'global' or 'queen'. Defaults to 'global'.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full file content including frontmatter.",
				},
			},
			"required":             []string{"filename", "content"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "delete_memory_file",
		Description: "Delete a memory file by filename.  Use during long reflection to prune stale or redundant memories.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{
					"type":        "string",
					"description": "The filename to delete.",
				},
				"scope": map[string]any{
					"type":        "string",
					"description": "Memory scope: 'global' or 'queen'. Defaults to 'global'.",
				},
			},
			"required":             []string{"filename"},
			"additionalProperties": false,
		},
	},
}

func ScopeLabel(scope, queenID string) string {
	if scope == "queen" && queenID != "" {
		return "queen (" + queenID + ")"
	}
	return scope
}

func ResolveMemoryScope(args map[string]any, dirs []ScopeDir) (string, error) {
	raw, has := args["scope"]
	var scope string
	if !has || raw == nil {
		if len(dirs) == 1 {
			return dirs[0].Scope, nil
		}
		scope = "global"
	} else {
		scope = strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
		if scope == "" {
			scope = "global"
		}
	}
	for _, d := range dirs {
		if d.Scope == scope {
			return scope, nil
		}
	}
	available := make([]string, 0, len(dirs))
	for _, d := range dirs {
		available = append(available, d.Scope)
	}
	sort.Strings(available)
	return "", fmt.Errorf("Invalid scope '%s'. Available scopes: %s.", scope, strings.Join(available, ", "))
}

func FormatMultiScopeManifest(dirs []ScopeDir, queenID string) string {
	blocks := make([]string, 0, len(dirs))
	for _, d := range dirs {
		files := ScanMemoryFiles(d.Dir)
		body := "(no memory files yet)"
		if len(files) > 0 {
			body = FormatMemoryManifest(files)
		}
		blocks = append(blocks, "## Scope: "+ScopeLabel(d.Scope, queenID)+"\n\n"+body)
	}
	return strings.Join(blocks, "\n\n")
}

func pyRepr(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('\'')
	return b.String()
}

func SafeMemoryPath(filename, dir string) (string, error) {
	if filename == "" || strings.TrimSpace(filename) != filename {
		return "", fmt.Errorf("Invalid filename: %s", pyRepr(filename))
	}
	if strings.ContainsAny(filename, `/\`) || strings.Contains(filename, "..") {
		return "", fmt.Errorf("Invalid filename: path components not allowed: %s", pyRepr(filename))
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		root = dir
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	candidate := filepath.Join(root, filename)
	if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
		candidate = resolved
	}
	rel, relErr := filepath.Rel(root, candidate)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("Path escapes memory directory: %s", pyRepr(filename))
	}
	return candidate, nil
}

func stringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, isString := v.(string); isString {
		return s
	}
	return fmt.Sprint(v)
}

func dirForScope(dirs []ScopeDir, scope string) string {
	for _, d := range dirs {
		if d.Scope == scope {
			return d.Dir
		}
	}
	return ""
}

func ExecuteTool(name string, args map[string]any, dirs []ScopeDir, queenID string) string {
	switch name {
	case "list_memory_files":
		if rawScope, has := args["scope"]; has && rawScope != nil {
			scope, err := ResolveMemoryScope(args, dirs)
			if err != nil {
				return "ERROR: " + err.Error()
			}
			files := ScanMemoryFiles(dirForScope(dirs, scope))
			if len(files) == 0 {
				return "(no " + scope + " memory files yet)"
			}
			return FormatMemoryManifest(files)
		}
		return FormatMultiScopeManifest(dirs, queenID)

	case "read_memory_file":
		filename := stringArg(args, "filename")
		scope, err := ResolveMemoryScope(args, dirs)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		path, err := SafeMemoryPath(filename, dirForScope(dirs, scope))
		if err != nil {
			return "ERROR: " + err.Error()
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			return "ERROR: File not found in " + scope + ": " + filename
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return "ERROR: " + readErr.Error()
		}
		return string(data)

	case "write_memory_file":
		filename := stringArg(args, "filename")
		content := stringArg(args, "content")
		scope, err := ResolveMemoryScope(args, dirs)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		scopeDir := dirForScope(dirs, scope)
		if !strings.HasSuffix(filename, ".md") {
			return "ERROR: Filename must end with .md"
		}
		fm := ParseFrontmatter(content)
		memType := strings.ToLower(strings.TrimSpace(fm["type"]))
		if memType != "" {
			valid := false
			for _, category := range GlobalMemoryCategories {
				if memType == category {
					valid = true
					break
				}
			}
			if !valid {
				return fmt.Sprintf("ERROR: Invalid memory type '%s'. Allowed types: %s.", memType, memoryCategoriesString)
			}
		}
		if len([]byte(content)) > MaxFileSizeBytes {
			return fmt.Sprintf("ERROR: Content exceeds %d byte limit.", MaxFileSizeBytes)
		}
		path, err := SafeMemoryPath(filename, scopeDir)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		if _, statErr := os.Stat(path); statErr != nil {
			if countMarkdownFiles(scopeDir) >= MaxFiles {
				return fmt.Sprintf("ERROR: File cap reached in %s (%d). Delete a file first.", scope, MaxFiles)
			}
		}
		if mkErr := os.MkdirAll(scopeDir, 0o755); mkErr != nil {
			return "ERROR: " + mkErr.Error()
		}
		if writeErr := os.WriteFile(path, []byte(content), 0o644); writeErr != nil {
			return "ERROR: " + writeErr.Error()
		}
		return fmt.Sprintf("Wrote %s:%s (%d chars).", scope, filename, utf8.RuneCountInString(content))

	case "delete_memory_file":
		filename := stringArg(args, "filename")
		scope, err := ResolveMemoryScope(args, dirs)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		path, err := SafeMemoryPath(filename, dirForScope(dirs, scope))
		if err != nil {
			return "ERROR: " + err.Error()
		}
		if _, statErr := os.Stat(path); statErr != nil {
			return "ERROR: File not found in " + scope + ": " + filename
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return "ERROR: " + removeErr.Error()
		}
		return "Deleted " + scope + ":" + filename + "."
	}
	return "ERROR: Unknown tool: " + name
}

func countMarkdownFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			count++
		}
	}
	return count
}

type LoopResult struct {
	Success      bool
	ChangedFiles []string
	LastText     string
}

const ReflectionMaxTurns = 5

var ReflectionTokenBudget = func() int { return 8192 }

func ReflectionLoop(
	ctx context.Context,
	completer llm.Completer,
	system string,
	userMsg string,
	dirs []ScopeDir,
	maxTurns int,
	maxTokens int,
	queenID string,
) LoopResult {
	if maxTurns <= 0 {
		maxTurns = ReflectionMaxTurns
	}
	messages := []llm.ChatMessage{{Role: "user", Content: userMsg}}
	changed := []string{}
	lastText := ""

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := completer.AComplete(ctx, llm.CompletionRequest{
			Messages:  messages,
			System:    system,
			Tools:     ReflectionTools,
			MaxTokens: maxTokens,
		})
		if err != nil {
			return LoopResult{Success: false, ChangedFiles: changed, LastText: lastText}
		}

		type parsedCall struct {
			call  llm.ToolCall
			input map[string]any
		}
		var calls []parsedCall
		for _, tc := range resp.ToolCalls {
			input := map[string]any{}
			if strings.TrimSpace(tc.Arguments) != "" {
				var parsed map[string]any
				if jsonErr := json.Unmarshal([]byte(tc.Arguments), &parsed); jsonErr == nil && parsed != nil {
					input = parsed
				}
			}
			calls = append(calls, parsedCall{call: tc, input: input})
		}

		turnText := resp.Content
		if turnText != "" {
			lastText = turnText
		}
		assistant := llm.ChatMessage{Role: "assistant", Content: turnText}
		for _, pc := range calls {
			assistant.ToolCalls = append(assistant.ToolCalls, pc.call)
		}
		messages = append(messages, assistant)

		if len(calls) == 0 {
			break
		}

		for _, pc := range calls {
			result := ExecuteTool(pc.call.Name, pc.input, dirs, queenID)
			if pc.call.Name == "write_memory_file" || pc.call.Name == "delete_memory_file" {
				filename := stringArg(pc.input, "filename")
				scope, scopeErr := ResolveMemoryScope(pc.input, dirs)
				if scopeErr != nil {
					scope = strings.ToLower(strings.TrimSpace(stringArg(pc.input, "scope")))
					if scope == "" {
						scope = "global"
					}
				}
				if filename != "" && !strings.HasPrefix(result, "ERROR") {
					changed = append(changed, scope+":"+filename)
				}
			}
			messages = append(messages, llm.ChatMessage{
				Role:       "tool",
				ToolCallID: pc.call.ID,
				Content:    result,
			})
		}
	}
	return LoopResult{Success: true, ChangedFiles: changed, LastText: lastText}
}

func BuildUnifiedShortReflectSystem(queenID string) string {
	queenScope := ""
	if queenID != "" {
		queenScope = "- `queen`: durable learnings specific to how queen '" + queenID + "' should work with this user\n"
	}
	return "You are a reflection agent that distills durable knowledge about the USER\n" +
		"into persistent memory files. You run in the background after each\n" +
		"assistant turn.\n" +
		"\n" +
		"Memory categories: " + memoryCategoriesString + "\n" +
		"\n" +
		"Available memory scopes:\n" +
		"- `global`: durable user facts that should help every queen in future sessions\n" +
		queenScope +
		"\n" +
		"Expected format for each memory file:\n" +
		"```markdown\n" +
		"---\n" +
		"name: {{memory name}}\n" +
		"description: {{one-line description — specific and search-friendly}}\n" +
		"type: {{" + memoryCategoriesString + "}}\n" +
		"---\n" +
		"\n" +
		"{{memory content}}\n" +
		"```\n" +
		"\n" +
		"Workflow (aim for 2 turns):\n" +
		"  Turn 1 — call list_memory_files without a scope to inspect all scopes, then\n" +
		"            read_memory_file for any files that might need updating.\n" +
		"  Turn 2 — call write_memory_file / delete_memory_file with an explicit scope.\n" +
		"\n" +
		"Rules:\n" +
		"- Make ONE coordinated storage decision per learning.\n" +
		"- Prefer `global` for broad user facts: identity, general preferences, environment,\n" +
		"  and feedback that should help all queens.\n" +
		"- Prefer `queen` only for stable domain-specific learnings about how this queen\n" +
		"  should reason, prioritize, communicate, or make tradeoffs for this user.\n" +
		"- Avoid storing the same fact in both scopes unless the scoped version adds\n" +
		"  genuinely distinct queen-specific nuance. When in doubt, keep only one copy.\n" +
		"- Update existing files instead of creating duplicates when possible.\n" +
		"- If the same learning already exists in the wrong scope or both scopes,\n" +
		"  you may update one file and delete the redundant one.\n" +
		"- Do NOT store task-specific details, code patterns, file paths, or ephemeral\n" +
		"  session state.\n" +
		"- Keep files concise. Each file should cover ONE topic.\n" +
		"- If there is nothing worth remembering, do nothing (respond with a brief\n" +
		"  reason — no tool calls needed).\n" +
		"- File names should be kebab-case slugs ending in .md.\n" +
		"- For user identity/profile information about the human user (name, role,\n" +
		"  background), ALWAYS use the canonical filename 'user-profile.md' in the\n" +
		"  `global` scope. This is the single source of truth for user profile data,\n" +
		"  shared with the settings UI.\n" +
		"- When updating `global:user-profile.md`, preserve the '## User Identity'\n" +
		"  section — it is managed by the settings UI. Never describe the assistant,\n" +
		"  queen, or agent as the identity in this file. Add/update other sections\n" +
		"  below it.\n" +
		"- Do NOT exceed " + itoa(MaxFileSizeBytes) + " bytes per file or " + itoa(MaxFiles) + " total files per scope.\n"
}

func BuildUnifiedLongReflectSystem(queenID string) string {
	queenScope := ""
	if queenID != "" {
		queenScope = "- `queen`: memories specific to how queen '" + queenID + "' should work with this user\n"
	}
	return "You are a reflection agent performing a periodic housekeeping pass over the\n" +
		"memory system for this user.\n" +
		"\n" +
		"Memory categories: " + memoryCategoriesString + "\n" +
		"\n" +
		"Available memory scopes:\n" +
		"- `global`: facts useful to every queen\n" +
		queenScope +
		"\n" +
		"Workflow:\n" +
		"  1. Call list_memory_files without a scope to inspect all scopes together.\n" +
		"  2. Read files that look redundant, stale, overlapping, or misplaced.\n" +
		"  3. Merge duplicates, move memories to the correct scope, and delete\n" +
		"     redundant copies when appropriate.\n" +
		"  4. Ensure descriptions are specific and search-friendly.\n" +
		"  5. Enforce limits: max " + itoa(MaxFiles) + " files and " + itoa(MaxFileSizeBytes) + " bytes per file in each scope.\n" +
		"\n" +
		"Rules:\n" +
		"- Treat deduplication across scopes as part of the job, not just within a scope.\n" +
		"- Prefer `global` for broad durable user facts and `queen` for queen-specific nuance.\n" +
		"- If two files store materially the same fact, keep the best one and delete or\n" +
		"  rewrite the redundant one.\n" +
		"- Prefer merging over deleting when the memories contain complementary signal.\n" +
		"- Remove memories that are stale, superseded, or misplaced.\n" +
		"- Keep the total collection lean and high-signal.\n" +
		"- Do NOT invent new information — only reorganise what exists.\n"
}

func ReflectionTimestamp() string {
	return time.Now().Format("2006-01-02T15:04")
}

func ReadConversationParts(sessionDir string) []map[string]any {
	partsDir := filepath.Join(sessionDir, "conversations", "parts")
	entries, err := os.ReadDir(partsDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	parts := make([]map[string]any, 0, len(names))
	for _, name := range names {
		data, readErr := os.ReadFile(filepath.Join(partsDir, name))
		if readErr != nil {
			continue
		}
		var part map[string]any
		if jsonErr := json.Unmarshal(data, &part); jsonErr != nil || part == nil {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

var (
	reflectedMu    sync.Mutex
	reflectedParts = map[string]int{}
)

func ReflectedParts(sessionDir string) (int, bool) {
	reflectedMu.Lock()
	defer reflectedMu.Unlock()
	n, ok := reflectedParts[sessionDir]
	return n, ok
}

func SetReflectedParts(sessionDir string, count int) {
	reflectedMu.Lock()
	defer reflectedMu.Unlock()
	reflectedParts[sessionDir] = count
}

func EnsureReflectedParts(sessionDir string) {
	reflectedMu.Lock()
	defer reflectedMu.Unlock()
	if _, exists := reflectedParts[sessionDir]; !exists {
		reflectedParts[sessionDir] = len(ReadConversationParts(sessionDir))
	}
}

func ResetReflectionState() {
	reflectedMu.Lock()
	defer reflectedMu.Unlock()
	reflectedParts = map[string]int{}
}

func defaultGlobalMemoryDir() string {
	home, err := hivehome.Resolve()
	if err != nil {
		return ""
	}
	return GlobalMemoryDir(home.Root)
}

func runShortReflectionWithPrompt(
	ctx context.Context,
	sessionDir string,
	completer llm.Completer,
	dirs []ScopeDir,
	systemPrompt string,
	queenID string,
) LoopResult {
	messages := ReadConversationParts(sessionDir)
	if len(messages) == 0 {
		return LoopResult{}
	}
	if n, ok := ReflectedParts(sessionDir); ok && n == len(messages) {
		return LoopResult{}
	}

	start := 0
	if len(messages) > 50 {
		start = len(messages) - 50
	}
	lines := make([]string, 0, len(messages)-start)
	for _, msg := range messages[start:] {
		role, _ := msg["role"].(string)
		content := ""
		if raw, has := msg["content"]; has && raw != nil {
			if s, isString := raw.(string); isString {
				content = s
			} else {
				content = fmt.Sprint(raw)
			}
		}
		content = strings.TrimSpace(content)
		if role == "tool" || content == "" {
			continue
		}
		label := "assistant"
		if role == "user" {
			label = "user"
		}
		if runes := []rune(content); len(runes) > 800 {
			content = string(runes[:800]) + "…"
		}
		lines = append(lines, "["+label+"]: "+content)
	}
	if len(lines) == 0 {
		return LoopResult{}
	}

	transcript := strings.Join(lines, "\n")
	userMsg := fmt.Sprintf("## Recent conversation (%d messages total)\n\n%s\n\nTimestamp: %s",
		len(messages), transcript, ReflectionTimestamp())
	result := ReflectionLoop(ctx, completer, systemPrompt, userMsg, dirs, ReflectionMaxTurns, ReflectionTokenBudget(), queenID)
	SetReflectedParts(sessionDir, len(messages))
	return result
}

func RunShortReflection(ctx context.Context, sessionDir string, completer llm.Completer, memoryDir string) LoopResult {
	if memoryDir == "" {
		memoryDir = defaultGlobalMemoryDir()
	}
	return runShortReflectionWithPrompt(ctx, sessionDir, completer,
		[]ScopeDir{{Scope: "global", Dir: memoryDir}},
		BuildUnifiedShortReflectSystem(""), "")
}

func RunQueenShortReflection(ctx context.Context, sessionDir string, completer llm.Completer, queenID, memoryDir string) LoopResult {
	return runShortReflectionWithPrompt(ctx, sessionDir, completer,
		[]ScopeDir{{Scope: "queen", Dir: memoryDir}},
		BuildUnifiedShortReflectSystem(queenID), queenID)
}

func RunUnifiedShortReflection(
	ctx context.Context,
	sessionDir string,
	completer llm.Completer,
	globalDir string,
	queenDir string,
	queenID string,
) LoopResult {
	if globalDir == "" {
		globalDir = defaultGlobalMemoryDir()
	}
	dirs := []ScopeDir{{Scope: "global", Dir: globalDir}}
	promptQueenID := ""
	if queenDir != "" && queenID != "" {
		dirs = append(dirs, ScopeDir{Scope: "queen", Dir: queenDir})
		promptQueenID = queenID
	}
	return runShortReflectionWithPrompt(ctx, sessionDir, completer, dirs,
		BuildUnifiedShortReflectSystem(promptQueenID), promptQueenID)
}

func RunLongReflection(ctx context.Context, completer llm.Completer, memoryDir, scopeLabel string) LoopResult {
	if memoryDir == "" {
		memoryDir = defaultGlobalMemoryDir()
	}
	files := ScanMemoryFiles(memoryDir)
	if len(files) == 0 {
		return LoopResult{}
	}
	userMsg := fmt.Sprintf("## Current memory manifest (%d files)\n\n%s\n\nTimestamp: %s",
		len(files), FormatMemoryManifest(files), ReflectionTimestamp())
	return ReflectionLoop(ctx, completer, BuildUnifiedLongReflectSystem(""), userMsg,
		[]ScopeDir{{Scope: "global", Dir: memoryDir}}, ReflectionMaxTurns, ReflectionTokenBudget(), "")
}

func RunUnifiedLongReflection(
	ctx context.Context,
	completer llm.Completer,
	globalDir string,
	queenDir string,
	queenID string,
) LoopResult {
	if globalDir == "" {
		globalDir = defaultGlobalMemoryDir()
	}
	dirs := []ScopeDir{{Scope: "global", Dir: globalDir}}
	promptQueenID := ""
	if queenDir != "" && queenID != "" {
		dirs = append(dirs, ScopeDir{Scope: "queen", Dir: queenDir})
		promptQueenID = queenID
	}
	userMsg := "## Current memory manifest across scopes\n\n" +
		FormatMultiScopeManifest(dirs, promptQueenID) + "\n\nTimestamp: " + ReflectionTimestamp()
	return ReflectionLoop(ctx, completer, BuildUnifiedLongReflectSystem(promptQueenID), userMsg,
		dirs, ReflectionMaxTurns, ReflectionTokenBudget(), promptQueenID)
}

func RunShutdownReflection(
	ctx context.Context,
	sessionDir string,
	completer llm.Completer,
	memoryDir string,
	globalMemoryDirOverride string,
	queenMemoryDir string,
	queenID string,
) LoopResult {
	globalDir := globalMemoryDirOverride
	if globalDir == "" {
		globalDir = memoryDir
	}
	if globalDir == "" {
		globalDir = defaultGlobalMemoryDir()
	}
	return RunUnifiedShortReflection(ctx, sessionDir, completer, globalDir, queenMemoryDir, queenID)
}

func WriteReflectionError(contextName, memoryDir string, cause error) {
	if memoryDir == "" {
		return
	}
	errorPath := filepath.Join(memoryDir, ".reflection_error.txt")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return
	}
	body := fmt.Sprintf("context: %s\ntime: %s\n\n%s", contextName,
		time.Now().Format("2006-01-02T15:04:05.000000"), string(debug.Stack()))
	if cause != nil {
		body += "\n" + cause.Error()
	}
	_ = os.WriteFile(errorPath, []byte(body), 0o644)
}

type ReflectionConfig struct {
	EveryTurns int
	LongEvery  int
	Model      string
}

func DefaultReflectionConfig() ReflectionConfig {
	return ReflectionConfig{EveryTurns: 6, LongEvery: 4, Model: "main"}
}

type ReflectionScheduler struct {
	SessionDir string
	LLM        llm.Completer
	GlobalDir  string
	QueenDir   string
	QueenID    string

	Config func() ReflectionConfig
	Spawn  func(work func())
	Short  func(ctx context.Context) LoopResult
	Long   func(ctx context.Context) LoopResult

	mu          sync.Mutex
	turns       int
	reflections int
	running     bool
}

func (s *ReflectionScheduler) config() ReflectionConfig {
	cfg := DefaultReflectionConfig()
	if s.Config != nil {
		cfg = s.Config()
	}
	if cfg.EveryTurns <= 0 {
		cfg.EveryTurns = 6
	}
	if cfg.LongEvery <= 0 {
		cfg.LongEvery = 4
	}
	return cfg
}

func (s *ReflectionScheduler) spawn(work func()) {
	if s.Spawn != nil {
		s.Spawn(work)
		return
	}
	go work()
}

func (s *ReflectionScheduler) short(ctx context.Context) LoopResult {
	if s.Short != nil {
		return s.Short(ctx)
	}
	return RunUnifiedShortReflection(ctx, s.SessionDir, s.LLM, s.GlobalDir, s.QueenDir, s.QueenID)
}

func (s *ReflectionScheduler) long(ctx context.Context) LoopResult {
	if s.Long != nil {
		return s.Long(ctx)
	}
	return RunUnifiedLongReflection(ctx, s.LLM, s.GlobalDir, s.QueenDir, s.QueenID)
}

func (s *ReflectionScheduler) runWithErrorCapture(ctx context.Context, fn func(context.Context) LoopResult, contextName string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			WriteReflectionError(contextName, s.GlobalDir, fmt.Errorf("%v", recovered))
		}
	}()
	fn(ctx)
}

func (s *ReflectionScheduler) OnTurnDone(ctx context.Context, streamID, parkReason string) {
	if streamID != "queen" {
		return
	}
	if parkReason != "turn_done" && parkReason != "ask_user" {
		return
	}

	s.mu.Lock()
	s.turns++
	turns := s.turns
	cfg := s.config()
	if turns%cfg.EveryTurns != 0 {
		s.mu.Unlock()
		return
	}
	if s.running {
		s.mu.Unlock()
		return
	}
	s.reflections++
	reflections := s.reflections
	isLong := reflections%cfg.LongEvery == 0
	s.running = true
	s.mu.Unlock()

	s.spawn(func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		s.runWithErrorCapture(ctx, s.short, "unified short reflection")
		if isLong {
			s.runWithErrorCapture(ctx, s.long, "unified long reflection")
		}
	})
}

func (s *ReflectionScheduler) OnCompaction(ctx context.Context, streamID string) {
	if streamID != "queen" {
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	s.spawn(func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		s.runWithErrorCapture(ctx, s.long, "unified compaction reflection")
	})
}

func (s *ReflectionScheduler) InitializeReflectionState() {
	EnsureReflectedParts(s.SessionDir)
}

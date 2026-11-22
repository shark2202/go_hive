package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

const SelectMemoriesSystemPrompt = `You are selecting memories that will be useful to the Queen agent as it processes a user's query.

You will be given the user's query and a list of available memory files with their filenames and descriptions.

Return a JSON object with a single key "selected_memories" containing a list of filenames for the memories that will clearly be useful as the Queen processes the user's query (up to 5).

Only include memories that you are certain will be helpful based on their name and description.
- If you are unsure if a memory will be useful in processing the user's query, then do not include it in your list.  Be selective and discerning.
- If there are no memories in the list that would clearly be useful, return an empty list.
`

var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

func SelectMemories(
	ctx context.Context,
	query string,
	completer llm.Completer,
	dir string,
	maxResults int,
	maxTokens int,
) []string {
	if maxResults <= 0 {
		maxResults = 5
	}
	files := ScanMemoryFiles(dir)
	if len(files) == 0 {
		return nil
	}

	var pinned []string
	for _, f := range files {
		t := ""
		if f.Type != nil {
			t = strings.ToLower(*f.Type)
		}
		if t == "profile" {
			pinned = append(pinned, f.Filename)
			if len(pinned) == maxResults {
				break
			}
		}
	}

	manifest := FormatMemoryManifest(files)
	userMsg := "## User query\n\n" + query + "\n\n## Available memories\n\n" + manifest

	resp, err := completer.AComplete(ctx, llm.CompletionRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: userMsg}},
		System:    SelectMemoriesSystemPrompt,
		MaxTokens: maxTokens,
		JSONMode:  true,
	})
	if err != nil {
		return pinned
	}
	raw := strings.TrimSpace(resp.Content)
	if raw == "" {
		return pinned
	}

	data := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		m := jsonObjectRe.FindString(raw)
		if m == "" {
			return pinned
		}
		if err := json.Unmarshal([]byte(m), &data); err != nil {
			return pinned
		}
	}

	selectedRaw, _ := data["selected_memories"].([]any)
	validNames := make(map[string]struct{}, len(files))
	aliases := make(map[string]string, len(files)*2)
	for _, f := range files {
		validNames[f.Filename] = struct{}{}
		aliases[f.Filename] = f.Filename
		stem := strings.TrimSuffix(f.Filename, ".md")
		if _, exists := aliases[stem]; !exists {
			aliases[stem] = f.Filename
		}
	}

	var normalized []string
	for _, rawSelected := range selectedRaw {
		s, ok := rawSelected.(string)
		if !ok {
			continue
		}
		if _, ok := validNames[s]; ok {
			normalized = append(normalized, s)
		} else if filename, ok := aliases[s]; ok {
			normalized = append(normalized, filename)
		}
	}

	combined := make([]string, 0, len(normalized)+len(pinned))
	seen := make(map[string]struct{}, len(normalized)+len(pinned))
	for _, name := range append(append([]string{}, normalized...), pinned...) {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		combined = append(combined, name)
		if len(combined) == maxResults {
			break
		}
	}
	return combined
}

// FormatRelativeAge says how long ago a memory was last confirmed, once that
// is more than two days.
//
// Counted from its “confirmed“ day (memory_log); a file from before those
// were recorded counts from when it was last written.
//
// Parity: framework/agents/queen/recall_selector.py::_format_relative_age
func FormatRelativeAge(content string, mtime float64, now time.Time) *string {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	confirmed := mtimeDay(mtime)
	if day, ok := ConfirmedOn(content); ok {
		if parsed, err := time.Parse("2006-01-02", day); err == nil {
			confirmed = parsed
		}
	}
	days := int(today.Sub(confirmed).Hours() / 24)
	if days <= 2 {
		return nil
	}
	note := "last confirmed " + itoa(days) + " days ago"
	return &note
}

// mtimeDay is the local calendar day a file was last written.
func mtimeDay(mtime float64) time.Time {
	moment := time.Unix(int64(mtime), 0)
	return time.Date(moment.Year(), moment.Month(), moment.Day(), 0, 0, 0, 0, time.UTC)
}

func FormatRecallInjection(filenames []string, dir string, label string) string {
	if len(filenames) == 0 {
		return ""
	}
	if label == "" {
		label = "Global Memories"
	}
	now := time.Now()
	var blocks []string
	for _, fname := range filenames {
		path := filepath.Join(dir, fname)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		header := "### " + fname
		if age := FormatRelativeAge(content, float64(info.ModTime().UnixNano())/1e9, now); age != nil {
			header += " (" + *age + ")"
		}
		blocks = append(blocks, header+"\n\n"+content)
	}
	if len(blocks) == 0 {
		return ""
	}
	body := strings.Join(blocks, "\n\n---\n\n")
	return "--- " + label + " ---\n\n" + body + "\n\n--- End " + label + " ---"
}

func BuildScopedRecallBlocks(
	ctx context.Context,
	query string,
	completer llm.Completer,
	globalDir string,
	queenDir string,
	queenID string,
	globalMaxResults int,
	queenMaxResults int,
	maxTokens int,
) (string, string) {
	if globalMaxResults <= 0 {
		globalMaxResults = 3
	}
	if queenMaxResults <= 0 {
		queenMaxResults = 3
	}

	// Each scope pick is a model call and the caller may be waiting on both
	// (the "wait" recall mode), so run them at once like upstream's
	// asyncio.gather. Each goroutine owns its result, so the returned
	// (global, queen) order is fixed no matter which pick finishes first.
	// At most two goroutines: one per scope. The completer contract already
	// allows concurrent calls (colony workers share one completer).
	//
	// Parity: framework/agents/queen/recall_selector.py::build_scoped_recall_blocks (asyncio.gather)
	var (
		wg             sync.WaitGroup
		globalSelected []string
		queenSelected  []string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		globalSelected = SelectMemories(ctx, query, completer, globalDir, globalMaxResults, maxTokens)
	}()
	hasQueen := queenDir != ""
	if hasQueen {
		wg.Add(1)
		go func() {
			defer wg.Done()
			queenSelected = SelectMemories(ctx, query, completer, queenDir, queenMaxResults, maxTokens)
		}()
	}
	wg.Wait()

	globalBlock := FormatRecallInjection(globalSelected, globalDir, "Global Memories")

	queenBlock := ""
	if hasQueen {
		queenLabel := "Queen Memories"
		if queenID != "" {
			queenLabel = "Queen Memories: " + queenID
		}
		queenBlock = FormatRecallInjection(queenSelected, queenDir, queenLabel)
	}
	return globalBlock, queenBlock
}

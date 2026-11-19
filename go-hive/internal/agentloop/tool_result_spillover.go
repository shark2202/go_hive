package agentloop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const toolResultPreviewCap = 5000

var (
	spillFilenameUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
	spillCounterRe        = regexp.MustCompile(`_(\d+)\.txt$`)
)

// nextSpillFilename returns a short, monotonic filename for a tool result
// spill, shortening the common tool_/mcp_ prefixes.
//
// Parity: core/framework/agent_loop/agent_loop.py::AgentLoop._next_spill_filename
func (l *AgentLoop) nextSpillFilename(toolName string) string {
	l.spillMu.Lock()
	l.spillCounter++
	counter := l.spillCounter
	l.spillMu.Unlock()
	short := strings.TrimPrefix(strings.TrimPrefix(toolName, "tool_"), "mcp_")
	short = spillFilenameUnsafeRe.ReplaceAllString(short, "_")
	if short == "" {
		short = "tool"
	}
	return fmt.Sprintf("%s_%d.txt", short, counter)
}

// restoreSpillCounter scans a spillover directory for existing spill files and
// returns the highest counter, so a resumed conversation cannot overwrite one.
//
// Parity: core/framework/agent_loop/internals/tool_result_handler.py::restore_spill_counter
func restoreSpillCounter(dir string) int {
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	max := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := spillCounterRe.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		if n, err := strconv.Atoi(match[1]); err == nil && n > max {
			max = n
		}
	}
	return max
}

// truncateToolResult persists an oversized non-error tool result to the
// spillover directory. Results at or below maxChars pass through untouched;
// a larger result is replaced with a bounded preview that cites the absolute
// file path, so the recovery reference survives pruning and compaction.
// Errors are untouched as well.
//
// Parity: core/framework/agent_loop/internals/tool_result_handler.py::truncate_tool_result
func (l *AgentLoop) truncateToolResult(result ToolResult, toolName string) ToolResult {
	dir := l.Config.SpilloverDir
	limit := l.Config.MaxToolResultChars
	if dir == "" || result.IsError || limit <= 0 || len(result.Content) <= limit {
		return result
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result
	}
	writeContent := result.Content
	if json.Valid([]byte(result.Content)) {
		var indented bytes.Buffer
		if err := json.Indent(&indented, []byte(result.Content), "", "  "); err == nil {
			writeContent = indented.String()
		}
	}
	filename := l.nextSpillFilename(toolName)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(writeContent), 0o644); err != nil {
		return result
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if limit > 0 && len(result.Content) > limit {
		preview := result.Content
		if runes := []rune(preview); len(runes) > toolResultPreviewCap {
			preview = string(runes[:toolResultPreviewCap]) + "…"
		}
		header := fmt.Sprintf(
			"Tool `%s` returned %s characters (too large for context). Full result at: %s\n"+
				"WARNING: the preview below is a SAMPLE only — do NOT draw counts, totals, or conclusions from it.",
			toolName, formatThousands(len(result.Content)), abs)
		result.Content = header + "\n\nPreview (truncated):\n" + preview
	}
	result.SpilloverPath = abs
	return result
}

// applyToolResultSpillover is the loop's post-execution hook for every tool
// result.
//
// Parity: core/framework/agent_loop/agent_loop.py::AgentLoop._truncate_tool_result
func (l *AgentLoop) applyToolResultSpillover(result ToolResult, toolName string) ToolResult {
	if l == nil {
		return result
	}
	return l.truncateToolResult(result, toolName)
}

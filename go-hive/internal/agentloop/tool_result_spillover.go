package agentloop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	toolResultPreviewCap = 5000

	jsonMetadataMaxDepth      = 3
	jsonMetadataMaxKeys       = 20
	jsonMetadataSampleKeys    = 10
	jsonPreviewArrayThreshold = 10
)

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

// ExtractJSONMetadata returns a concise structural summary of parsed JSON:
// key names, value types, and — crucially — array lengths, so the model knows
// how much data exists beyond the preview. Scalars return "".
//
// Parity: core/framework/agent_loop/internals/tool_result_handler.py::extract_json_metadata
func ExtractJSONMetadata(parsed any) string {
	return extractJSONMetadata(parsed, 0, jsonMetadataMaxDepth)
}

func extractJSONMetadata(parsed any, depth, maxDepth int) string {
	if depth >= maxDepth {
		switch value := parsed.(type) {
		case map[string]any:
			return fmt.Sprintf("dict with %d keys", len(value))
		case []any:
			return fmt.Sprintf("list of %d items", len(value))
		default:
			return pythonTypeName(parsed)
		}
	}
	switch value := parsed.(type) {
	case map[string]any:
		if len(value) == 0 {
			return "empty dict"
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		indent := strings.Repeat("  ", depth+1)
		lines := make([]string, 0, len(keys))
		for i, key := range keys {
			if i >= jsonMetadataMaxKeys {
				break
			}
			item := value[key]
			switch child := item.(type) {
			case []any:
				line := fmt.Sprintf("%s%q: list of %d items", indent, key, len(child))
				if len(child) > 0 {
					switch first := child[0].(type) {
					case map[string]any:
						line += fmt.Sprintf(" (each item: dict with keys %s)", pyStringListRepr(sortedSampleKeys(first)))
					case []any:
						line += fmt.Sprintf(" (each item: list of %d elements)", len(first))
					}
				}
				lines = append(lines, line)
			case map[string]any:
				childSummary := extractJSONMetadata(child, depth+1, maxDepth)
				lines = append(lines, fmt.Sprintf("%s%q: %s", indent, key, childSummary))
			default:
				lines = append(lines, fmt.Sprintf("%s%q: %s", indent, key, pythonTypeName(item)))
			}
		}
		if len(keys) > jsonMetadataMaxKeys {
			lines = append(lines, fmt.Sprintf("%s... and %d more keys", indent, len(keys)-jsonMetadataMaxKeys))
		}
		return strings.Join(lines, "\n")
	case []any:
		if len(value) == 0 {
			return "empty list"
		}
		desc := fmt.Sprintf("list of %d items", len(value))
		switch first := value[0].(type) {
		case map[string]any:
			desc += fmt.Sprintf(" (each item: dict with keys %s)", pyStringListRepr(sortedSampleKeys(first)))
		case []any:
			desc += fmt.Sprintf(" (each item: list of %d elements)", len(first))
		}
		return desc
	}
	return ""
}

// sortedSampleKeys returns the first jsonMetadataSampleKeys object keys in a
// stable order (the Go map has no insertion order).
func sortedSampleKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > jsonMetadataSampleKeys {
		keys = keys[:jsonMetadataSampleKeys]
	}
	return keys
}

// pythonTypeName mirrors type(value).__name__ for JSON values decoded into
// Go's any (encoding/json yields float64 for every number).
func pythonTypeName(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case bool:
		return "bool"
	case float64:
		if typed == float64(int64(typed)) {
			return "int"
		}
		return "float"
	case float32:
		return "float"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// BuildJSONPreview builds a smart preview of parsed JSON, truncating large
// arrays to first 3 + last 1 items with an explicit count marker so the model
// cannot mistake the preview for the full dataset. It returns ("", false) when
// no array was large enough to truncate (the caller then slices the raw text).
//
// Parity: core/framework/agent_loop/internals/tool_result_handler.py::build_json_preview
func BuildJSONPreview(parsed any, maxChars int) (string, bool) {
	preview, changed := truncateJSONArrays(parsed)
	if !changed {
		return "", false
	}
	encoded, err := marshalJSONNoEscape(preview, true)
	if err != nil {
		return "", false
	}
	if maxChars > 0 && utf8.RuneCountInString(encoded) > maxChars {
		// Even 3+1 items are too big: show only the first item per large array.
		minimal := minimalJSONArrays(parsed)
		encoded, err = marshalJSONNoEscape(minimal, true)
		if err != nil {
			return "", false
		}
		if maxChars > 0 && utf8.RuneCountInString(encoded) > maxChars {
			encoded = truncateRunes(encoded, maxChars) + "…"
		}
	}
	return encoded, true
}

// truncateJSONArrays returns a copy with every array longer than
// jsonPreviewArrayThreshold cut to head 3 + marker + tail 1.
func truncateJSONArrays(value any) (any, bool) {
	switch typed := value.(type) {
	case []any:
		if len(typed) > jsonPreviewArrayThreshold {
			head := append([]any(nil), typed[:3]...)
			marker := fmt.Sprintf("... (%d more items omitted, %d total) ...", len(typed)-4, len(typed))
			tail := append([]any(nil), typed[len(typed)-1:]...)
			return append(append(head, marker), tail...), true
		}
		changed := false
		out := make([]any, len(typed))
		for i, item := range typed {
			child, did := truncateJSONArrays(item)
			out[i] = child
			changed = changed || did
		}
		if changed {
			return out, true
		}
		return value, false
	case map[string]any:
		changed := false
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			child, did := truncateJSONArrays(item)
			out[key] = child
			changed = changed || did
		}
		if changed {
			return out, true
		}
		return value, false
	default:
		return value, false
	}
}

// minimalJSONArrays is the last-resort preview: one item per oversized array.
func minimalJSONArrays(value any) any {
	switch typed := value.(type) {
	case []any:
		if len(typed) > jsonPreviewArrayThreshold {
			marker := fmt.Sprintf("... (%d more items omitted, %d total) ...", len(typed)-1, len(typed))
			return append(append([]any(nil), typed[:1]...), marker)
		}
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = minimalJSONArrays(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = minimalJSONArrays(item)
		}
		return out
	default:
		return value
	}
}

// marshalJSONNoEscape renders JSON like Python's json.dumps(indent=2,
// ensure_ascii=False): no HTML escaping and no trailing newline.
func marshalJSONNoEscape(value any, indent bool) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if indent {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

// truncateToolResult persists a non-error tool result to the spillover
// directory and shapes the LLM-visible content to avoid the model mistaking a
// truncated sample for the complete dataset.
//
// Rules (upstream's post-poison-pattern contract):
//   - Errors pass through unchanged.
//   - With a spillover dir, EVERY non-error result is written to disk for
//     debugging. A small result's content passes through unchanged (no
//     "[Saved to ...]" trailer, which frontier models autocomplete); a large
//     result becomes prose header + structural metadata + smart preview.
//   - Without a spillover dir, a large result is truncated in place; a small
//     one passes through.
//
// Parity: core/framework/agent_loop/internals/tool_result_handler.py::truncate_tool_result
func (l *AgentLoop) truncateToolResult(result ToolResult, toolName string) ToolResult {
	if l == nil || result.IsError {
		return result
	}
	dir := l.Config.SpilloverDir
	limit := l.Config.MaxToolResultChars
	if dir == "" {
		return truncateToolResultInPlace(result, toolName, limit)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return truncateToolResultInPlace(result, toolName, limit)
	}
	writeContent, parsed := spillWriteContent(result.Content)
	filename := l.nextSpillFilename(toolName)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(writeContent), 0o644); err != nil {
		return truncateToolResultInPlace(result, toolName, limit)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if limit > 0 && utf8.RuneCountInString(result.Content) > limit {
		metadata := ""
		if parsed != nil {
			metadata = ExtractJSONMetadata(parsed)
		}
		preview := largeToolResultPreview(result.Content, parsed, toolResultPreviewCap)
		header := fmt.Sprintf(
			"Tool `%s` returned %s characters (too large for context). Full result at: %s\n"+
				"For targeted lookup, run `grep -nE \"<pattern>\" \"%s\"` via terminal_exec.\n",
			toolName, formatThousands(utf8.RuneCountInString(result.Content)), abs, abs)
		if metadata != "" {
			header += "\nData structure:\n" + metadata + "\n"
		}
		header += "\nWARNING: the preview below is a SAMPLE only — do NOT draw counts, totals, or conclusions from it."
		result.Content = header + "\n\nPreview (truncated):\n" + preview
	}
	result.SpilloverPath = abs
	return result
}

// spillWriteContent pretty-prints JSON content for line-based terminal cat/grep
// (upstream's write_content), and returns the parsed value for metadata.
func spillWriteContent(content string) (string, any) {
	var parsed any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return content, nil
	}
	encoded, err := marshalJSONNoEscape(parsed, true)
	if err != nil {
		return content, parsed
	}
	return encoded, parsed
}

// largeToolResultPreview prefers the smart JSON preview and falls back to a
// raw character slice with an ellipsis.
func largeToolResultPreview(content string, parsed any, cap int) string {
	if parsed != nil {
		if preview, ok := BuildJSONPreview(parsed, cap); ok {
			return preview
		}
	}
	return truncateRunes(content, cap) + "…"
}

// truncateToolResultInPlace is the no-spillover-dir branch: truncate with the
// upstream budget min(5000, max(limit-500, limit/2)) and the same metadata.
func truncateToolResultInPlace(result ToolResult, toolName string, limit int) ToolResult {
	if limit <= 0 || utf8.RuneCountInString(result.Content) <= limit {
		return result
	}
	cap := limit - 500
	if half := limit / 2; half > cap {
		cap = half
	}
	if cap > toolResultPreviewCap {
		cap = toolResultPreviewCap
	}
	var parsed any
	if err := json.Unmarshal([]byte(result.Content), &parsed); err != nil {
		parsed = nil
	}
	metadata := ""
	if parsed != nil {
		metadata = ExtractJSONMetadata(parsed)
	}
	header := fmt.Sprintf(
		"Tool `%s` returned %s characters (truncated to fit context budget — no spillover dir configured).",
		toolName, formatThousands(utf8.RuneCountInString(result.Content)))
	if metadata != "" {
		header += "\n\nData structure:\n" + metadata
	}
	header += "\n\nWARNING: the preview below is a SAMPLE only — do NOT draw counts, totals, or conclusions from it."
	result.Content = header + "\n\n" + largeToolResultPreview(result.Content, parsed, cap)
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

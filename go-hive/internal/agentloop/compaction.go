package agentloop

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Parity: framework/agent_loop/internals/compaction.py COMPACTABLE_TOOLS.
var CompactableTools = map[string]bool{
	"pdf_read":            true,
	"terminal_exec":       true,
	"terminal_rg":         true,
	"terminal_glob":       true,
	"terminal_output_get": true,
	"terminal_job_logs":   true,
	"web_scrape":          true,
	"search_papers":       true,
	"download_paper":      true,
	"search_wikipedia":    true,
}

const (
	DefaultMicrocompactKeepRecent = 6

	LLMCompactMaxDepth         = 10
	LLMCompactSummaryMaxTokens = 8192

	UserMessageMaxChars  = 100_000
	UserMessageHeadChars = 4_000
	UserMessageTailChars = 4_000

	DefaultMaxVerbatimClientMessages = 40
)

var MicrocompactKeepRecent = envPositiveInt("HIVE_MICROCOMPACT_KEEP_RECENT", DefaultMicrocompactKeepRecent)

var MaxVerbatimClientMessages = envPositiveIntPtr("HIVE_MAX_VERBATIM_CLIENT_MSGS", DefaultMaxVerbatimClientMessages)

func envPositiveInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envPositiveIntPtr(name string, fallback int) *int {
	raw := os.Getenv(name)
	if raw == "" {
		value := fallback
		return &value
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		value := fallback
		return &value
	}
	if value <= 0 {
		return nil
	}
	return &value
}

// Parity: framework/agent_loop/internals/compaction.py::truncate_oversized_user_messages.
func TruncateOversizedUserMessages(c *NodeConversation) int {
	truncated := 0
	for i, msg := range c.messages {
		if msg.Role != RoleUser {
			continue
		}
		if msg.IsSystemReminder || msg.IsSkillContent {
			continue
		}
		if len(msg.Content) <= UserMessageMaxChars {
			continue
		}
		origLen := len(msg.Content)
		head := msg.Content[:UserMessageHeadChars]
		tail := msg.Content[origLen-UserMessageTailChars:]
		cut := origLen - UserMessageHeadChars - UserMessageTailChars
		notice := "\n\n<system-reminder>\n" +
			fmt.Sprintf("[%s chars truncated to keep compaction safe; "+
				"original message was %s chars. If an attachment "+
				"path appears in the tail below, read it selectively via "+
				"pdf_read.]\n", formatThousands(cut), formatThousands(origLen)) +
			"</system-reminder>\n\n"
		updated := msg
		updated.Content = head + notice + tail
		c.messages[i] = updated
		truncated++
	}
	if truncated > 0 {
		c.lastAPIInputTokens = nil
	}
	return truncated
}

// Parity: framework/agent_loop/internals/compaction.py::microcompact (keep_recent argument).
func Microcompact(c *NodeConversation) int {
	return MicrocompactWithKeepRecent(c, MicrocompactKeepRecent)
}

func MicrocompactWithKeepRecent(c *NodeConversation, keepRecent int) int {
	messages := c.messages
	type candidate struct {
		index     int
		spillover string
	}
	compactable := []candidate{}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != RoleTool || msg.IsError || msg.IsSkillContent {
			continue
		}
		if strings.HasPrefix(msg.Content, "Pruned tool result") ||
			strings.HasPrefix(msg.Content, "[Pruned tool result") ||
			strings.HasPrefix(msg.Content, "[Old tool result") {
			continue
		}
		if len(msg.Content) < 100 {
			continue
		}
		toolName := findToolNameForResult(messages, msg)
		if toolName == "" || !CompactableTools[toolName] {
			continue
		}
		var spillover string
		if msg.SpilloverPath != nil {
			spillover = *msg.SpilloverPath
		} else if inline := ExtractSpilloverFilename(msg.Content); inline != nil {
			spillover = *inline
		}
		if spillover == "" {
			continue
		}
		compactable = append(compactable, candidate{index: i, spillover: spillover})
	}

	if keepRecent < 0 {
		keepRecent = 0
	}
	if len(compactable) <= keepRecent {
		return 0
	}

	cleared := 0
	for _, item := range compactable[keepRecent:] {
		msg := messages[item.index]
		origLen := len(msg.Content)
		placeholder := fmt.Sprintf(
			"Old tool result (%s chars) at %s. Use terminal_rg with a pattern against this path to recover specifics.",
			formatThousands(origLen), item.spillover,
		)
		updated := Message{
			Seq:                msg.Seq,
			Role:               msg.Role,
			Content:            placeholder,
			ToolUseID:          msg.ToolUseID,
			ToolCalls:          msg.ToolCalls,
			IsError:            msg.IsError,
			PhaseID:            msg.PhaseID,
			IsTransitionMarker: msg.IsTransitionMarker,
			SpilloverPath:      msg.SpilloverPath,
		}
		c.messages[item.index] = updated
		cleared++
	}
	if cleared > 0 {
		c.lastAPIInputTokens = nil
	}
	return cleared
}

// Parity: framework/agent_loop/internals/compaction.py::_find_tool_name_for_result.
func findToolNameForResult(messages []Message, toolMsg Message) string {
	if toolMsg.ToolUseID == nil {
		return ""
	}
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			if id, _ := tc["id"].(string); id != *toolMsg.ToolUseID {
				continue
			}
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					return name
				}
			}
			return ""
		}
	}
	return ""
}

// Parity: framework/agent_loop/internals/compaction.py::strip_images_from_messages.
func StripImagesFromMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if len(msg.ImageContent) == 0 {
			out = append(out, msg)
			continue
		}
		markers := make([]string, len(msg.ImageContent))
		for i := range markers {
			markers[i] = "[image]"
		}
		marker := strings.Join(markers, " ")
		content := marker
		if msg.Content != "" {
			content = msg.Content + "\n" + marker
		}
		updated := msg
		updated.Content = content
		updated.ImageContent = nil
		out = append(out, updated)
	}
	return out
}

// Parity: framework/agent_loop/internals/compaction.py::format_messages_for_summary.
func FormatMessagesForSummary(messages []Message) string {
	lines := make([]string, 0, len(messages))
	for _, m := range messages {
		switch {
		case m.Role == RoleTool:
			content := m.Content
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			lines = append(lines, "[tool result]: "+content)
		case m.Role == RoleAssistant && len(m.ToolCalls) > 0:
			names := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				name := "?"
				if fn, ok := tc["function"].(map[string]any); ok {
					if n, ok := fn["name"].(string); ok {
						name = n
					}
				}
				names = append(names, name)
			}
			text := m.Content
			if len(text) > 200 {
				text = text[:200]
			}
			lines = append(lines, fmt.Sprintf("[assistant (calls: %s)]: %s", strings.Join(names, ", "), text))
		default:
			lines = append(lines, "["+m.Role+"]: "+m.Content)
		}
	}
	return strings.Join(lines, "\n\n")
}

// Parity: framework/agent_loop/internals/compaction.py::compaction_target_chars.
func CompactionTargetChars(formattedChars, maxContextTokens int) int {
	halfWindow := (maxContextTokens / 2) * 4
	halfInput := formattedChars / 2
	if halfInput < 2_000 {
		halfInput = 2_000
	}
	if halfWindow < halfInput {
		return halfWindow
	}
	return halfInput
}

// Parity: framework/agent_loop/internals/compaction.py::llm_compact_char_limit.
func LLMCompactCharLimit(maxContextTokens int) int {
	limit := (maxContextTokens * 4) / 3
	if limit < 20_000 {
		return 20_000
	}
	return limit
}

// Parity: framework/agent_loop/internals/compaction.py::build_message_inventory.
func BuildMessageInventory(c *NodeConversation) []map[string]any {
	messages := c.messages
	inventory := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		contentChars := len(message.Content)
		toolCallArgsChars := 0
		toolName := ""
		if len(message.ToolCalls) > 0 {
			for _, tc := range message.ToolCalls {
				if fn, ok := tc["function"].(map[string]any); ok {
					switch args := fn["arguments"].(type) {
					case string:
						toolCallArgsChars += len(args)
					default:
						if encoded, err := marshalJSON(args); err == nil {
							toolCallArgsChars += len(encoded)
						}
					}
				}
			}
			names := make([]string, 0, len(message.ToolCalls))
			for _, tc := range message.ToolCalls {
				name := "?"
				if fn, ok := tc["function"].(map[string]any); ok {
					if n, ok := fn["name"].(string); ok {
						name = n
					}
				}
				names = append(names, name)
			}
			toolName = strings.Join(names, ", ")
		} else if message.Role == RoleTool && message.ToolUseID != nil {
			toolName = findToolNameForResult(messages, message)
		}
		entry := map[string]any{
			"seq":           message.Seq,
			"role":          message.Role,
			"content_chars": contentChars,
		}
		if toolCallArgsChars != 0 {
			entry["tool_call_args_chars"] = toolCallArgsChars
		}
		if toolName != "" {
			entry["tool"] = toolName
		}
		if message.IsError {
			entry["is_error"] = true
		}
		if message.PhaseID != nil {
			entry["phase"] = *message.PhaseID
		}
		if contentChars > 2000 {
			entry["preview"] = message.Content[:200] + "\u2026"
		}
		inventory = append(inventory, entry)
	}
	return inventory
}

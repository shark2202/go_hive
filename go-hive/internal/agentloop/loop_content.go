package agentloop

import (
	"fmt"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
)

func chatToolCall(raw map[string]any) llm.ToolCall {
	call := llm.ToolCall{}
	if id, ok := raw["id"].(string); ok {
		call.ID = id
	}
	if function, ok := raw["function"].(map[string]any); ok {
		call.Name, _ = function["name"].(string)
		call.Arguments, _ = function["arguments"].(string)
	}
	return call
}

func contentString(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []map[string]any:
		return joinContentBlocks(value)
	case []any:
		blocks := make([]map[string]any, 0, len(value))
		for _, item := range value {
			if block, ok := item.(map[string]any); ok {
				blocks = append(blocks, block)
			}
		}
		return joinContentBlocks(blocks)
	default:
		return fmt.Sprint(value)
	}
}

func joinContentBlocks(blocks []map[string]any) string {
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text, ok := block["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

package llm

import "strings"

func SupportsImageToolResults(model string) bool {
	if model == "" {
		return true
	}
	return ModelSupportsVision(model)
}

func FilterToolsForModel(tools []ToolDef, model string) ([]ToolDef, []string) {
	filtered := append([]ToolDef(nil), tools...)
	if model == "" || SupportsImageToolResults(model) {
		return filtered, []string{}
	}
	hidden := make([]string, 0)
	for _, tool := range tools {
		if tool.ProducesImage {
			hidden = append(hidden, tool.Name)
		}
	}
	if len(hidden) == 0 {
		return filtered, []string{}
	}
	kept := make([]ToolDef, 0, len(tools)-len(hidden))
	for _, tool := range tools {
		if !tool.ProducesImage {
			kept = append(kept, tool)
		}
	}
	return kept, hidden
}

var anthropicRoutedPrefixes = []string{
	"anthropic/",
	"claude-",
	"kimi/",
	"hive/",
	"openrouter/anthropic/",
}

func SupportsImagesInToolResults(model string) bool {
	if model == "" {
		return true
	}
	lower := strings.ToLower(model)
	for _, prefix := range anthropicRoutedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

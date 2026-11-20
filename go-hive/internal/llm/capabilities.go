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

// SupportsImageBlocks reports whether the model accepts image content blocks.
// The curated catalog decides; unknown models default to capable, mirroring
// upstream's model_supports_vision default so hosted frontier models work
// without a catalog entry.
func SupportsImageBlocks(model string) bool {
	return ModelSupportsVision(model)
}

var documentModelPrefixes = []string{
	// Anthropic Messages API: the file part is remapped to a `document` block.
	"anthropic/",
	"claude-",
	"kimi/",
	"hive/",
	// OpenAI Chat Completions and OpenRouter: native `file` content parts
	// (PDF only on Chat Completions; OpenRouter parses for every model).
	"openai/",
	"gpt-",
	"o1-",
	"o3-",
	"o4-",
	"openrouter/",
}

// SupportsDocumentBlocks reports whether the model's endpoint accepts a PDF
// `file` content part (OpenAI/LiteLLM shape, remapped to Anthropic `document`
// by the transport). Unknown prefixes return false: the message assembly then
// replaces the block with an explicit "not delivered" note instead of sending
// a part the endpoint may reject or silently ignore.
func SupportsDocumentBlocks(model string) bool {
	if model == "" {
		return true
	}
	lower := strings.ToLower(model)
	for _, prefix := range documentModelPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

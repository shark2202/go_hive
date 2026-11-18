package tools

import "strings"

type visionModel struct {
	ID             string
	SupportsVision bool
}

type visionProvider struct {
	ID     string
	Models []visionModel
}

var visionCatalogProviders = []visionProvider{
	{
		ID: "anthropic",
		Models: []visionModel{
			{ID: "claude-haiku-4-5-20251001", SupportsVision: true},
			{ID: "claude-sonnet-4-5-20250929", SupportsVision: true},
			{ID: "claude-opus-4-6", SupportsVision: true},
		},
	},
	{
		ID: "openai",
		Models: []visionModel{
			{ID: "gpt-5.5", SupportsVision: true},
			{ID: "gpt-5.4", SupportsVision: true},
			{ID: "gpt-5.4-mini", SupportsVision: true},
			{ID: "gpt-5.4-nano", SupportsVision: true},
		},
	},
	{
		ID: "gemini",
		Models: []visionModel{
			{ID: "gemini-3-flash-preview", SupportsVision: true},
			{ID: "gemini-3.5-flash", SupportsVision: true},
			{ID: "gemini-3.1-pro-preview-customtools", SupportsVision: true},
		},
	},
	{
		ID: "groq",
		Models: []visionModel{
			{ID: "openai/gpt-oss-120b", SupportsVision: false},
			{ID: "openai/gpt-oss-20b", SupportsVision: false},
			{ID: "llama-3.3-70b-versatile", SupportsVision: false},
			{ID: "llama-3.1-8b-instant", SupportsVision: false},
		},
	},
	{
		ID: "cerebras",
		Models: []visionModel{
			{ID: "gpt-oss-120b", SupportsVision: false},
			{ID: "zai-glm-4.7", SupportsVision: false},
			{ID: "qwen-3-235b-a22b-instruct-2507", SupportsVision: false},
		},
	},
	{
		ID: "minimax",
		Models: []visionModel{
			{ID: "MiniMax-M2.7", SupportsVision: false},
			{ID: "MiniMax-M2.5", SupportsVision: false},
		},
	},
	{
		ID: "mistral",
		Models: []visionModel{
			{ID: "mistral-large-2512", SupportsVision: true},
			{ID: "mistral-medium-2508", SupportsVision: true},
			{ID: "mistral-small-2603", SupportsVision: true},
			{ID: "codestral-2508", SupportsVision: false},
		},
	},
	{
		ID: "together",
		Models: []visionModel{
			{ID: "deepseek-ai/DeepSeek-V3.1", SupportsVision: false},
			{ID: "Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8", SupportsVision: false},
			{ID: "openai/gpt-oss-120b", SupportsVision: false},
			{ID: "meta-llama/Llama-3.3-70B-Instruct-Turbo", SupportsVision: false},
		},
	},
	{
		ID: "deepseek",
		Models: []visionModel{
			{ID: "deepseek-v4-pro", SupportsVision: false},
			{ID: "deepseek-v4-flash", SupportsVision: false},
			{ID: "deepseek-reasoner", SupportsVision: false},
		},
	},
	{
		ID: "kimi",
		Models: []visionModel{
			{ID: "kimi-k2.6", SupportsVision: true},
			{ID: "kimi-k2.5", SupportsVision: true},
		},
	},
	{
		ID: "hive",
		Models: []visionModel{
			{ID: "queen", SupportsVision: true},
			{ID: "glm-5.1", SupportsVision: false},
			{ID: "kimi-k2.6", SupportsVision: false},
			{ID: "kimi-k2.5", SupportsVision: true},
			{ID: "mimo-v2.5", SupportsVision: false},
			{ID: "mimo-v2.5-pro", SupportsVision: false},
			{ID: "hive-2.1", SupportsVision: false},
			{ID: "hive-swarm", SupportsVision: true},
			{ID: "hive-vision", SupportsVision: true},
			{ID: "gemini-3.5-flash", SupportsVision: true},
			{ID: "claude-fable-5", SupportsVision: true},
			{ID: "gpt-5.5", SupportsVision: true},
			{ID: "gemini-3-ultra", SupportsVision: true},
		},
	},
	{
		ID: "openrouter",
		Models: []visionModel{
			{ID: "openai/gpt-5.4", SupportsVision: true},
			{ID: "anthropic/claude-sonnet-4.6", SupportsVision: true},
			{ID: "anthropic/claude-opus-4.6", SupportsVision: true},
			{ID: "google/gemini-3.1-pro-preview-customtools", SupportsVision: true},
			{ID: "qwen/qwen3.6-plus", SupportsVision: false},
			{ID: "z-ai/glm-5v-turbo", SupportsVision: true},
			{ID: "z-ai/glm-5.1", SupportsVision: false},
			{ID: "minimax/minimax-m2.7", SupportsVision: false},
			{ID: "xiaomi/mimo-v2-pro", SupportsVision: true},
		},
	},
}

var anthropicRoutedPrefixes = []string{
	"anthropic/",
	"claude-",
	"kimi/",
	"hive/",
	"openrouter/anthropic/",
}

func ModelSupportsVision(modelID string) bool {
	if modelID == "" {
		return true
	}
	candidates := []string{modelID}
	if idx := strings.Index(modelID, "/"); idx >= 0 {
		candidates = append(candidates, modelID[idx+1:])
	}
	for _, candidate := range candidates {
		for _, provider := range visionCatalogProviders {
			for _, model := range provider.Models {
				if model.ID == candidate {
					return model.SupportsVision
				}
			}
		}
	}
	return true
}

func SupportsImageToolResults(model string) bool {
	if model == "" {
		return true
	}
	return ModelSupportsVision(model)
}

func FilterToolsForModel(toolList []Tool, model string) ([]Tool, []string) {
	if model == "" || SupportsImageToolResults(model) {
		return append([]Tool(nil), toolList...), []string{}
	}
	hidden := []string{}
	for _, t := range toolList {
		if t.ProducesImage {
			hidden = append(hidden, t.Name)
		}
	}
	if len(hidden) == 0 {
		return append([]Tool(nil), toolList...), []string{}
	}
	kept := make([]Tool, 0, len(toolList)-len(hidden))
	for _, t := range toolList {
		if !t.ProducesImage {
			kept = append(kept, t)
		}
	}
	return kept, hidden
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

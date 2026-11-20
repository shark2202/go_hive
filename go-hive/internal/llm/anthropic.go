package llm

import (
	"context"
	"errors"
	"os"
	"strings"
)

const DefaultAnthropicModel = "claude-haiku-4-5-20251001"

const AnthropicAPIKeyRequiredMessage = "Anthropic API key required. Set ANTHROPIC_API_KEY env var or pass api_key."

type AnthropicCredentialLookup func() (string, bool)

var anthropicCredentialLookup AnthropicCredentialLookup

func SetAnthropicCredentialLookup(lookup AnthropicCredentialLookup) {
	anthropicCredentialLookup = lookup
}

func resolveAnthropicAPIKey(apiKey string) string {
	if apiKey != "" {
		return apiKey
	}
	if anthropicCredentialLookup != nil {
		if key, ok := anthropicCredentialLookup(); ok && key != "" {
			return key
		}
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

type AnthropicProvider struct {
	APIKey string
	Model  string

	Provider *Provider
}

func NewAnthropicProvider(apiKey, model string) (*AnthropicProvider, error) {
	return NewAnthropicProviderWithBase(apiKey, model, "")
}

// NewAnthropicProviderWithBase is NewAnthropicProvider with an explicit API
// base for Anthropic-compatible proxies. An empty apiBase falls back to the
// ANTHROPIC_API_BASE / ANTHROPIC_BASE_URL environment variables, then to the
// public Anthropic endpoint.
//
// Parity: core/framework/llm/anthropic.py::AnthropicProvider.__init__
func NewAnthropicProviderWithBase(apiKey, model, apiBase string) (*AnthropicProvider, error) {
	key := resolveAnthropicAPIKey(apiKey)
	if key == "" {
		return nil, errors.New(AnthropicAPIKeyRequiredMessage)
	}
	if model == "" {
		model = DefaultAnthropicModel
	}
	if apiBase == "" {
		apiBase = strings.TrimSpace(os.Getenv("ANTHROPIC_API_BASE"))
	}
	if apiBase == "" {
		apiBase = strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL"))
	}
	return &AnthropicProvider{
		APIKey:   key,
		Model:    model,
		Provider: NewHTTPProvider(ProviderOptions{Model: model, APIKey: key, APIBase: apiBase}),
	}, nil
}

func (a *AnthropicProvider) Complete(ctx context.Context, messages []map[string]any, opts CompleteOptions) (Completion, error) {
	if a == nil || a.Provider == nil {
		return Completion{}, errors.New("llm: anthropic provider is not initialised")
	}
	return a.Provider.Complete(ctx, messages, opts)
}

func (a *AnthropicProvider) AComplete(ctx context.Context, req CompletionRequest) (Completion, error) {
	if a == nil || a.Provider == nil {
		return Completion{}, errors.New("llm: anthropic provider is not initialised")
	}
	system := MergedSystemPrompt(req.System, req.SystemDynamicSuffix)
	return a.Provider.Complete(ctx, ChatMessagesToMaps(req.Messages), CompleteOptions{
		System:    system,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		JSONMode:  req.JSONMode,
	})
}

func ChatMessagesToMaps(messages []ChatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		entry := map[string]any{"role": message.Role}
		if len(message.ContentParts) > 0 {
			parts := make([]any, 0, len(message.ContentParts))
			for _, part := range message.ContentParts {
				parts = append(parts, part)
			}
			entry["content"] = parts
		} else {
			entry["content"] = message.Content
		}
		if message.ToolCallID != "" {
			entry["tool_call_id"] = message.ToolCallID
		}
		if len(message.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				calls = append(calls, map[string]any{
					"id":   call.ID,
					"type": "function",
					"function": map[string]any{
						"name":      call.Name,
						"arguments": call.Arguments,
					},
				})
			}
			entry["tool_calls"] = calls
			if _, blocks := entry["content"].([]any); !blocks && message.Content == "" {
				entry["content"] = nil
			}
		}
		out = append(out, entry)
	}
	return out
}

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// OpenRouter routes each model to a sub-provider and some routed providers do
// not support native function calling at all (they answer 404 "No endpoints
// found that support tool use"). The compatibility path prompts for one JSON
// object carrying the assistant text and any tool calls, then converts that
// JSON back into the runtime's event shapes. A successful fallback is cached
// for a bounded window so later turns skip the failing native-tools probe.
//
// Parity: core/framework/llm/litellm.py::_stream_via_openrouter_tool_compat,
// ::_should_use_openrouter_tool_compat, ::_is_openrouter_tool_compat_cached
const (
	openRouterToolCompatCacheTTL = time.Hour
)

var openRouterToolCompatErrorSnippets = []string{
	"no endpoints found that support tool use",
	"no endpoints available that support tool use",
	"provider routing",
}

var openRouterToolCompatCache = struct {
	sync.Mutex
	expiresAt map[string]time.Time
}{expiresAt: map[string]time.Time{}}

// isOpenRouterModel reports whether the configured model targets OpenRouter,
// either by model prefix or by API base.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider._is_openrouter_model
func isOpenRouterModel(model, apiBase string) bool {
	if strings.HasPrefix(strings.ToLower(model), "openrouter/") {
		return true
	}
	return strings.Contains(strings.ToLower(apiBase), "openrouter.ai/api/v1")
}

// shouldUseOpenRouterToolCompat reports whether an OpenRouter failure means
// the routed provider cannot do native tool use.
func shouldUseOpenRouterToolCompat(model, apiBase string, err error) bool {
	if err == nil || !isOpenRouterModel(model, apiBase) {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, snippet := range openRouterToolCompatErrorSnippets {
		if strings.Contains(text, snippet) {
			return true
		}
	}
	return false
}

// rememberOpenRouterToolCompatModel caches the fallback decision.
func rememberOpenRouterToolCompatModel(model string) {
	if model == "" {
		return
	}
	openRouterToolCompatCache.Lock()
	openRouterToolCompatCache.expiresAt[model] = time.Now().Add(openRouterToolCompatCacheTTL)
	openRouterToolCompatCache.Unlock()
}

// isOpenRouterToolCompatCached reports whether the model's compatibility
// decision is still fresh. Expired entries are evicted lazily.
func isOpenRouterToolCompatCached(model string) bool {
	openRouterToolCompatCache.Lock()
	defer openRouterToolCompatCache.Unlock()
	expires, ok := openRouterToolCompatCache.expiresAt[model]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(openRouterToolCompatCache.expiresAt, model)
		return false
	}
	return true
}

// openRouterCompatToolCall is one call parsed out of the compatibility JSON.
type openRouterCompatToolCall struct {
	ToolCall
	Input map[string]any
}

// openRouterToolCompatCompletion runs the JSON-emulated tool-calling path and
// publishes the same StreamEvent sequence a normal non-stream turn produces.
func (p *Provider) openRouterToolCompatCompletion(
	ctx context.Context,
	req CompletionRequest,
	model, apiBase, apiKey string,
	emit func(StreamEvent),
	cause error,
) (Completion, error) {
	if emit == nil {
		emit = func(StreamEvent) {}
	}
	messages := BuildOpenRouterToolCompatMessages(
		ChatMessagesToMaps(req.Messages), req.System, req.Tools, req.SystemDynamicSuffix, model)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = ResolveAuxMaxTokens(0)
	}
	kwargs := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}
	for key, value := range p.ExtraKwargs {
		kwargs[key] = value
	}
	if apiKey != "" {
		kwargs["api_key"] = apiKey
	}
	if apiBase != "" {
		kwargs["api_base"] = apiBase
	}

	response, err := p.completionWithRetry(ctx, kwargs, nil)
	if err != nil {
		p.emitStreamError(emit, err)
		if cause != nil {
			return Completion{}, fmt.Errorf("llm: openrouter tool compatibility failed (%v): %w", cause, err)
		}
		return Completion{}, err
	}

	content, calls := parseOpenRouterToolCompatResponse(response.Content, req.Tools)
	completion := p.completionFromResponse(response)
	completion.Content = content
	completion.ToolCalls = make([]ToolCall, 0, len(calls))
	toolInputs := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		completion.ToolCalls = append(completion.ToolCalls, call.ToolCall)
		toolInputs = append(toolInputs, call.Input)
	}
	switch {
	case len(completion.ToolCalls) > 0:
		completion.StopReason = "tool_calls"
	case response.FinishReason != "":
		completion.StopReason = response.FinishReason
	default:
		completion.StopReason = "stop"
	}

	if completion.Content != "" {
		emit(TextDeltaEvent{Content: completion.Content, Snapshot: completion.Content})
		emit(TextEndEvent{FullText: completion.Content})
	}
	for i, call := range completion.ToolCalls {
		emit(ToolCallEvent{ToolUseID: call.ID, ToolName: call.Name, ToolInput: toolInputs[i]})
	}
	emit(FinishEvent{
		StopReason:          completion.StopReason,
		InputTokens:         completion.InputTokens,
		OutputTokens:        completion.OutputTokens,
		CachedTokens:        completion.CachedTokens,
		CacheCreationTokens: completion.CacheCreationTokens,
		CostUSD:             completion.CostUSD,
		Credits:             completion.Credits,
		Model:               completion.Model,
		ThinkingBlocks:      completion.ThinkingBlocks,
		ReasoningContent:    completion.ReasoningContent,
	})
	return completion, nil
}

// parseOpenRouterToolCompatResponse turns the compatibility JSON into
// assistant text plus tool calls. Non-JSON answers are treated as plain text
// (the model ignored the schema but still answered).
//
// Parity: core/framework/llm/litellm.py::_parse_openrouter_tool_compat_response
func parseOpenRouterToolCompatResponse(content string, tools []ToolDef) (string, []openRouterCompatToolCall) {
	payload := extractJSONObject(content)
	if payload == nil {
		return strings.TrimSpace(content), nil
	}

	assistantText := ""
	for _, key := range []string{"assistant_response", "content", "response"} {
		if text, ok := payload[key].(string); ok {
			assistantText = text
			break
		}
	}

	rawCalls, _ := payload["tool_calls"].([]any)
	if len(rawCalls) == 0 {
		if _, hasName := payload["name"]; hasName {
			if _, hasArguments := payload["arguments"]; hasArguments {
				rawCalls = []any{payload}
			}
		} else if call, ok := payload["tool_call"].(map[string]any); ok {
			rawCalls = []any{call}
		}
	}

	allowed := make(map[string]bool, len(tools))
	for _, tool := range tools {
		allowed[tool.Name] = true
	}
	prefix := fmt.Sprintf("openrouter_compat_%d", time.Now().UnixNano())
	calls := make([]openRouterCompatToolCall, 0, len(rawCalls))
	for index, raw := range rawCalls {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		function, _ := entry["function"].(map[string]any)
		name := firstString(entry["name"], entry["tool_name"])
		if name == "" && function != nil {
			name, _ = function["name"].(string)
		}
		if name == "" || !allowed[name] {
			continue
		}
		var rawArguments any
		switch {
		case entry["arguments"] != nil:
			rawArguments = entry["arguments"]
		case entry["tool_input"] != nil:
			rawArguments = entry["tool_input"]
		case entry["input"] != nil:
			rawArguments = entry["input"]
		case function != nil && function["arguments"] != nil:
			rawArguments = function["arguments"]
		default:
			rawArguments = map[string]any{}
		}
		input := normalizeToolArguments(rawArguments)
		arguments, err := json.Marshal(input)
		if err != nil {
			continue
		}
		calls = append(calls, openRouterCompatToolCall{
			ToolCall: ToolCall{
				ID:        fmt.Sprintf("%s_%d", prefix, index),
				Name:      name,
				Arguments: string(arguments),
			},
			Input: input,
		})
	}
	return strings.TrimSpace(assistantText), calls
}

// normalizeToolArguments accepts the shapes a compatibility payload may use
// for arguments: an object, a JSON string, or a scalar.
func normalizeToolArguments(raw any) map[string]any {
	switch value := raw.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		return value
	case string:
		parsed := map[string]any{}
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			return map[string]any{"_raw": value}
		}
		return parsed
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return map[string]any{}
		}
		parsed := map[string]any{}
		if err := json.Unmarshal(encoded, &parsed); err != nil {
			return map[string]any{"value": value}
		}
		return parsed
	}
}

// extractJSONObject returns the first JSON object in text, tolerating a code
// fence or leading/trailing prose.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider._extract_json_object
func extractJSONObject(text string) map[string]any {
	candidates := []string{strings.TrimSpace(text)}
	stripped := strings.TrimSpace(text)
	if strings.HasPrefix(stripped, "```") {
		lines := strings.Split(stripped, "\n")
		if len(lines) >= 3 {
			candidates = append(candidates, strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n")))
		}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(candidate))
		var parsed map[string]any
		if err := decoder.Decode(&parsed); err == nil {
			return parsed
		}
		for offset := 0; offset < len(candidate); offset++ {
			if candidate[offset] != '{' {
				continue
			}
			decoder = json.NewDecoder(strings.NewReader(candidate[offset:]))
			parsed = map[string]any{}
			if err := decoder.Decode(&parsed); err == nil {
				return parsed
			}
		}
	}
	return nil
}

func firstString(values ...any) string {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

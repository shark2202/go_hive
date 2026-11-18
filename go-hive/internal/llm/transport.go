package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Parity: core/framework/llm/litellm.py::LiteLLMProvider._default_api_base_for_model, core/framework/llm/litellm.py::rewrite_proxy_model
const (
	OpenAIAPIBase    = "https://api.openai.com/v1"
	AnthropicAPIBase = "https://api.anthropic.com"
	OllamaAPIBase    = "http://127.0.0.1:11434"

	AnthropicVersion = "2023-06-01"

	DefaultHTTPTimeout     = 600 * time.Second
	maxCompletionBodyBytes = 32 << 20

	anthropicOAuthPrefix = "sk-ant-oat"
)

// httpCompletionClient is shared by every HTTP transport. Timeouts are applied
// per request through the context so long tool-calling turns are not cut off.
var httpCompletionClient = &http.Client{}

// NewHTTPProvider builds a provider with the same defaults as NewProvider and
// attaches the real HTTP completion transport.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.__init__
func NewHTTPProvider(opts ProviderOptions) *Provider {
	provider := NewProvider(opts)
	provider.Call = provider.httpCompletion
	return provider
}

// AComplete lets *Provider satisfy Completer so the agent loop can drive any
// HTTP-configured provider directly.
//
// Parity: core/framework/llm/anthropic.py::AnthropicProvider.acomplete
func (p *Provider) AComplete(ctx context.Context, req CompletionRequest) (Completion, error) {
	if p == nil {
		return Completion{}, errors.New("llm: provider is not initialised")
	}
	if p.Call == nil {
		return Completion{}, errors.New("llm: provider has no completion transport")
	}
	system := req.System
	if req.SystemDynamicSuffix != "" {
		if system == "" {
			system = req.SystemDynamicSuffix
		} else {
			system = system + "\n\n" + req.SystemDynamicSuffix
		}
	}
	return p.Complete(ctx, ChatMessagesToMaps(req.Messages), CompleteOptions{
		System:    system,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		JSONMode:  req.JSONMode,
	})
}

// httpCompletion dispatches a completion over HTTP, choosing the wire protocol
// from the model family the same way litellm routes provider prefixes.
func (p *Provider) httpCompletion(ctx context.Context, kwargs map[string]any) (CompletionResponse, error) {
	model := transportString(kwargs["model"])
	if model == "" {
		model = p.Model
	}
	apiBase := transportString(kwargs["api_base"])
	if apiBase == "" {
		apiBase = p.APIBase
	}
	apiKey := transportString(kwargs["api_key"])
	if apiKey == "" {
		apiKey = p.APIKey
	}

	if usesAnthropicMessagesAPI(model) {
		return p.anthropicMessagesCompletion(ctx, kwargs, model, apiBase, apiKey)
	}
	return p.openAIChatCompletion(ctx, kwargs, model, apiBase, apiKey)
}

// usesAnthropicMessagesAPI reports whether the model is served by the Anthropic
// Messages API rather than an OpenAI-compatible chat completions endpoint.
//
// Parity: core/framework/llm/litellm.py::rewrite_proxy_model (kimi/ and hive/
// are rewritten to anthropic/)
func usesAnthropicMessagesAPI(model string) bool {
	lower := strings.ToLower(model)
	if strings.HasPrefix(lower, "openrouter/") {
		return false
	}
	return IsAnthropicModel(lower) ||
		strings.HasPrefix(lower, "kimi/") ||
		strings.HasPrefix(lower, "hive/")
}

// wireModelName strips routing prefixes that are transport selectors rather
// than part of the upstream model id.
//
// Parity: core/framework/llm/litellm.py::rewrite_proxy_model
func wireModelName(model string) string {
	index := strings.Index(model, "/")
	if index < 0 {
		return model
	}
	switch strings.ToLower(model[:index]) {
	case "openai", "openrouter", "anthropic", "ollama", "ollama_chat",
		"vllm", "lm_studio", "llamacpp", "minimax", "kimi", "hive",
		"groq", "deepseek", "mistral", "xai", "perplexity", "together",
		"cerebras", "gemini":
		return model[index+1:]
	}
	return model
}

func transportString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func transportInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	}
	return 0
}

// resolveCompletionAPIBase resolves the effective base URL for a completion.
func resolveCompletionAPIBase(model, configured string) string {
	if base := strings.TrimRight(strings.TrimSpace(configured), "/"); base != "" {
		return base
	}
	if base, ok := DefaultAPIBaseForModel(model); ok {
		return strings.TrimRight(base, "/")
	}
	lower := strings.ToLower(model)
	switch {
	case IsAnthropicModel(lower):
		return AnthropicAPIBase
	case IsOllamaModel(lower):
		if host := strings.TrimRight(strings.TrimSpace(os.Getenv("OLLAMA_HOST")), "/"); host != "" {
			return host
		}
		return OllamaAPIBase
	}
	return OpenAIAPIBase
}

// openAIChatEndpoint appends /chat/completions to a base URL, tolerating bases
// that already end in /v1 or the full endpoint path.
func openAIChatEndpoint(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// anthropicMessagesEndpoint appends /v1/messages to a base URL, tolerating
// bases that already end in /v1 or the full endpoint path.
func anthropicMessagesEndpoint(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

// apiBaseNeedsBearerAuth mirrors the Python helper: Anthropic-compatible
// proxies mint JWTs and expect Authorization: Bearer rather than x-api-key.
//
// Parity: core/framework/llm/litellm.py::_api_base_needs_bearer_auth
func apiBaseNeedsBearerAuth(apiBase string) bool {
	if apiBase == "" {
		return false
	}
	lowered := strings.ToLower(apiBase)
	for _, host := range []string{"adenhq.com", "open-hive.com", "127.0.0.1:8890", "localhost:8890"} {
		if strings.Contains(lowered, host) {
			return true
		}
	}
	override := strings.ToLower(strings.TrimSpace(os.Getenv("HIVE_LLM_BASE_URL")))
	return override != "" && strings.Contains(lowered, override)
}

// postJSON sends a JSON body and decodes a successful response into target.
// Non-2xx responses are mapped to *ProviderError so rate-limit retries work.
func postJSON(ctx context.Context, label, endpoint string, headers map[string]string, payload any, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("llm: encode %s request: %w", label, err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, DefaultHTTPTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("llm: build %s request: %w", label, err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := httpCompletionClient.Do(request)
	if err != nil {
		return fmt.Errorf("llm: %s request failed: %w", label, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxCompletionBodyBytes))
	if err != nil {
		return fmt.Errorf("llm: read %s response: %w", label, err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newProviderHTTPError(label, response, body)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("llm: decode %s response: %w", label, err)
	}
	return nil
}

func newProviderHTTPError(label string, response *http.Response, body []byte) *ProviderError {
	headers := make(map[string]string, len(response.Header))
	for key, values := range response.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	detail := strings.TrimSpace(extractErrorMessage(body))
	if detail == "" {
		detail = strings.TrimSpace(string(body))
		if len(detail) > 400 {
			detail = detail[:400]
		}
	}
	message := fmt.Sprintf("llm: %s returned status %d", label, response.StatusCode)
	if detail != "" {
		message += ": " + detail
	}
	return &ProviderError{Message: message, StatusCode: response.StatusCode, Headers: headers}
}

func extraHeadersFromKwargs(kwargs map[string]any) map[string]string {
	headers := map[string]string{}
	raw, ok := kwargs["extra_headers"].(map[string]any)
	if !ok {
		if stringMap, ok := kwargs["extra_headers"].(map[string]string); ok {
			for key, value := range stringMap {
				headers[key] = value
			}
		}
		return headers
	}
	for key, value := range raw {
		if text, ok := value.(string); ok {
			headers[key] = text
		}
	}
	return headers
}

// ---------------------------------------------------------------------------
// OpenAI-compatible chat completions
// ---------------------------------------------------------------------------

type openAIFunctionPayload struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type openAIToolCallPayload struct {
	ID       string                `json:"id"`
	Type     string                `json:"type"`
	Function openAIFunctionPayload `json:"function"`
}

type openAIUsagePayload struct {
	PromptTokens             int     `json:"prompt_tokens"`
	CompletionTokens         int     `json:"completion_tokens"`
	TotalTokens              int     `json:"total_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	Cost                     float64 `json:"cost"`
	PromptTokensDetails      *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_creation_tokens"`
	} `json:"prompt_tokens_details"`
}

type openAIChoicePayload struct {
	FinishReason string `json:"finish_reason"`
	Message      struct {
		Content      json.RawMessage         `json:"content"`
		ToolCalls    []openAIToolCallPayload `json:"tool_calls"`
		FunctionCall *openAIFunctionPayload  `json:"function_call"`
	} `json:"message"`
}

type openAICompletionPayload struct {
	Model   string                `json:"model"`
	Choices []openAIChoicePayload `json:"choices"`
	Usage   *openAIUsagePayload   `json:"usage"`
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete
func (p *Provider) openAIChatCompletion(ctx context.Context, kwargs map[string]any, model, apiBase, apiKey string) (CompletionResponse, error) {
	body := openAIRequestBody(kwargs, model)
	base := resolveCompletionAPIBase(model, apiBase)

	headers := extraHeadersFromKwargs(kwargs)
	if apiKey != "" {
		if _, ok := headers["Authorization"]; !ok {
			headers["Authorization"] = "Bearer " + apiKey
		}
	}

	var payload openAICompletionPayload
	if err := postJSON(ctx, "openai", openAIChatEndpoint(base), headers, body, &payload); err != nil {
		return CompletionResponse{}, err
	}
	return payload.toCompletionResponse(model), nil
}

func openAIRequestBody(kwargs map[string]any, model string) map[string]any {
	body := make(map[string]any, len(kwargs)+2)
	for key, value := range kwargs {
		switch key {
		case "api_key", "api_base", "extra_headers", "extra_body",
			"allowed_openai_params", "timeout", "stream", "stream_options":
			continue
		}
		body[key] = value
	}
	body["model"] = wireModelName(model)
	if _, ok := body["messages"]; !ok {
		body["messages"] = []any{}
	}
	if maxTokens := transportInt(body["max_tokens"]); maxTokens <= 0 {
		body["max_tokens"] = ResolveAuxMaxTokens(0)
	}
	if extra, ok := kwargs["extra_body"].(map[string]any); ok {
		for key, value := range extra {
			body[key] = value
		}
	}
	return body
}

func (payload openAICompletionPayload) toCompletionResponse(requestedModel string) CompletionResponse {
	response := CompletionResponse{
		Model:        payload.Model,
		HiddenParams: map[string]any{},
	}
	if response.Model == "" {
		response.Model = requestedModel
	}
	if len(payload.Choices) > 0 {
		choice := payload.Choices[0]
		response.Content = decodeMessageContent(choice.Message.Content)
		response.FinishReason = choice.FinishReason
		response.ToolCalls = openAIToolCalls(choice.Message.ToolCalls)
		if len(choice.Message.ToolCalls) == 0 && choice.Message.FunctionCall != nil {
			response.ToolCalls = openAIToolCalls([]openAIToolCallPayload{{
				ID:       "call_0",
				Type:     "function",
				Function: *choice.Message.FunctionCall,
			}})
		}
	}
	if usage := payload.Usage; usage != nil {
		response.Usage = usage.toUsage()
		if usage.Cost > 0 {
			response.HiddenParams["response_cost"] = usage.Cost
		}
	}
	return response
}

func openAIToolCalls(calls []openAIToolCallPayload) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: rawArgumentsString(call.Function.Arguments),
		})
	}
	return out
}

func (usage openAIUsagePayload) toUsage() *Usage {
	converted := &Usage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		Cost:             usage.Cost,
	}
	cachedRead := usage.CacheReadInputTokens
	cacheCreation := usage.CacheCreationInputTokens
	if usage.PromptTokensDetails != nil {
		if usage.PromptTokensDetails.CachedTokens > 0 {
			cachedRead = usage.PromptTokensDetails.CachedTokens
		}
		if usage.PromptTokensDetails.CacheWriteTokens > 0 {
			cacheCreation = usage.PromptTokensDetails.CacheWriteTokens
		}
	}
	if cachedRead > 0 || cacheCreation > 0 {
		converted.PromptTokensDetails = &PromptTokensDetails{
			CachedTokens:     cachedRead,
			CacheWriteTokens: cacheCreation,
		}
	}
	return converted
}

// rawArgumentsString renders JSON tool arguments as the string form the
// OpenAI wire format uses, tolerating providers that send an object.
func rawArgumentsString(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return trimmed
}

// decodeMessageContent accepts both the string form and the content-parts
// array some OpenAI-compatible providers return.
func decodeMessageContent(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		if value, ok := part["text"].(string); ok {
			builder.WriteString(value)
			continue
		}
		if value, ok := part["refusal"].(string); ok {
			builder.WriteString(value)
		}
	}
	return builder.String()
}

// ---------------------------------------------------------------------------
// Anthropic Messages API
// ---------------------------------------------------------------------------

type anthropicContentBlockPayload struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicUsagePayload struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicMessagePayload struct {
	Model      string                         `json:"model"`
	Content    []anthropicContentBlockPayload `json:"content"`
	StopReason string                         `json:"stop_reason"`
	Usage      *anthropicUsagePayload         `json:"usage"`
}

// Parity: core/framework/llm/anthropic.py::AnthropicProvider, core/framework/llm/litellm.py::rewrite_proxy_model
func (p *Provider) anthropicMessagesCompletion(ctx context.Context, kwargs map[string]any, model, apiBase, apiKey string) (CompletionResponse, error) {
	body, err := anthropicRequestBody(kwargs, model)
	if err != nil {
		return CompletionResponse{}, err
	}
	base := resolveCompletionAPIBase(model, apiBase)

	headers := extraHeadersFromKwargs(kwargs)
	headers["anthropic-version"] = AnthropicVersion
	if apiKey != "" {
		oauth := strings.HasPrefix(apiKey, anthropicOAuthPrefix)
		bearer := oauth || p.HiveProxyAuth || apiBaseNeedsBearerAuth(base)
		if bearer {
			if _, ok := headers["Authorization"]; !ok {
				headers["Authorization"] = "Bearer " + apiKey
			}
		} else if _, ok := headers["x-api-key"]; !ok {
			headers["x-api-key"] = apiKey
		}
	}

	var payload anthropicMessagePayload
	if err := postJSON(ctx, "anthropic", anthropicMessagesEndpoint(base), headers, body, &payload); err != nil {
		return CompletionResponse{}, err
	}
	return payload.toCompletionResponse(model), nil
}

func anthropicRequestBody(kwargs map[string]any, model string) (map[string]any, error) {
	messages, err := anthropicMessages(kwargs["messages"])
	if err != nil {
		return nil, err
	}

	maxTokens := transportInt(kwargs["max_tokens"])
	if maxTokens <= 0 {
		maxTokens = DefaultAuxMaxTokens
	}

	body := map[string]any{
		"model":      wireModelName(model),
		"max_tokens": maxTokens,
		"messages":   messages,
	}
	if system := anthropicSystem(kwargs["messages"]); system != nil {
		body["system"] = system
	}
	if tools := anthropicTools(kwargs["tools"]); len(tools) > 0 {
		body["tools"] = tools
		if choice := anthropicToolChoice(kwargs["tool_choice"]); choice != nil {
			body["tool_choice"] = choice
		}
	}
	for _, key := range []string{"temperature", "top_p", "top_k", "metadata", "thinking"} {
		if value, ok := kwargs[key]; ok && value != nil {
			body[key] = value
		}
	}
	if stop := anthropicStopSequences(kwargs["stop"]); len(stop) > 0 {
		body["stop_sequences"] = stop
	}
	return body, nil
}

func anthropicStopSequences(value any) []any {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []any{typed}
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		out := make([]any, 0, len(typed))
		for _, text := range typed {
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

// anthropicSystem folds OpenAI system/developer messages into the Anthropic
// top-level system prompt, preserving cache_control breakpoints.
func anthropicSystem(raw any) any {
	messages, ok := asMapSlice(raw)
	if !ok {
		return nil
	}
	var texts []string
	blocks := make([]any, 0, len(messages))
	needsBlocks := false
	for _, message := range messages {
		role, _ := message["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		switch content := message["content"].(type) {
		case string:
			if strings.TrimSpace(content) == "" {
				continue
			}
			texts = append(texts, content)
			blocks = append(blocks, map[string]any{"type": "text", "text": content})
		default:
			parts, ok := asAnySlice(content)
			if !ok {
				continue
			}
			for _, part := range parts {
				block, ok := part.(map[string]any)
				if !ok {
					continue
				}
				blocks = append(blocks, copyBlock(block))
				needsBlocks = true
				if text, ok := block["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		if cacheControl, ok := message["cache_control"].(map[string]any); ok && len(blocks) > 0 {
			if last, ok := blocks[len(blocks)-1].(map[string]any); ok {
				last["cache_control"] = cacheControl
			}
			needsBlocks = true
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	if !needsBlocks {
		return strings.Join(texts, "\n\n")
	}
	return blocks
}

func anthropicMessages(raw any) ([]any, error) {
	messages, ok := asMapSlice(raw)
	if !ok {
		return nil, errors.New("llm: anthropic messages must be a slice of message objects")
	}
	out := make([]any, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer":
			continue
		case "assistant":
			blocks, err := anthropicAssistantBlocks(message)
			if err != nil {
				return nil, err
			}
			blocks = applyAnthropicCacheControl(blocks, message)
			if len(blocks) == 0 {
				continue
			}
			out = appendAnthropicMessage(out, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			callID, _ := message["tool_call_id"].(string)
			if callID == "" {
				continue
			}
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": callID,
				"content":     anthropicToolResultContent(message["content"]),
			}
			blocks := applyAnthropicCacheControl([]any{block}, message)
			out = appendAnthropicMessage(out, map[string]any{"role": "user", "content": blocks})
		default:
			blocks := anthropicUserBlocks(message["content"])
			blocks = applyAnthropicCacheControl(blocks, message)
			if len(blocks) == 0 {
				continue
			}
			out = appendAnthropicMessage(out, map[string]any{"role": "user", "content": blocks})
		}
	}
	return out, nil
}

// appendAnthropicMessage merges adjacent same-role messages because the
// Anthropic API expects alternating roles.
func appendAnthropicMessage(out []any, message map[string]any) []any {
	if len(out) > 0 {
		if previous, ok := out[len(out)-1].(map[string]any); ok && previous["role"] == message["role"] {
			previousBlocks, _ := previous["content"].([]any)
			newBlocks, _ := message["content"].([]any)
			previous["content"] = append(previousBlocks, newBlocks...)
			return out
		}
	}
	return append(out, message)
}

func applyAnthropicCacheControl(blocks []any, message map[string]any) []any {
	cacheControl, ok := message["cache_control"].(map[string]any)
	if !ok || len(blocks) == 0 {
		return blocks
	}
	if block, ok := blocks[len(blocks)-1].(map[string]any); ok {
		block["cache_control"] = cacheControl
	}
	return blocks
}

func anthropicAssistantBlocks(message map[string]any) ([]any, error) {
	blocks := make([]any, 0, 4)
	if thinking, ok := asAnySlice(message["thinking_blocks"]); ok {
		for _, block := range thinking {
			if typed, ok := block.(map[string]any); ok {
				blocks = append(blocks, copyBlock(typed))
			}
		}
	}
	blocks = append(blocks, anthropicTextBlocks(message["content"])...)

	toolCalls, _ := asMapSlice(message["tool_calls"])
	for _, call := range toolCalls {
		function, _ := call["function"].(map[string]any)
		name := transportString(function["name"])
		if name == "" {
			continue
		}
		arguments, err := ParseToolCallArguments(rawArgumentsString(rawMessage(function["arguments"])), name)
		if err != nil {
			return nil, fmt.Errorf("llm: anthropic tool call %q has invalid arguments: %w", name, err)
		}
		block := map[string]any{
			"type":  "tool_use",
			"id":    transportString(call["id"]),
			"name":  name,
			"input": arguments,
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

// rawMessage normalises a tool-call arguments value to json.RawMessage.
func rawMessage(value any) json.RawMessage {
	switch typed := value.(type) {
	case json.RawMessage:
		return typed
	case string:
		return json.RawMessage(typed)
	case nil:
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func anthropicUserBlocks(content any) []any {
	switch typed := content.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": typed}}
	default:
		parts, ok := asAnySlice(content)
		if !ok {
			text := fmt.Sprint(content)
			return []any{map[string]any{"type": "text", "text": text}}
		}
		blocks := make([]any, 0, len(parts))
		for _, part := range parts {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch transportString(block["type"]) {
			case "text":
				text, _ := block["text"].(string)
				if strings.TrimSpace(text) == "" {
					continue
				}
				blocks = append(blocks, copyBlock(block))
			case "image_url":
				if image := anthropicImageBlock(block); image != nil {
					blocks = append(blocks, image)
				}
			default:
				blocks = append(blocks, copyBlock(block))
			}
		}
		return blocks
	}
}

func anthropicTextBlocks(content any) []any {
	switch typed := content.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": typed}}
	default:
		parts, ok := asAnySlice(content)
		if !ok {
			return nil
		}
		blocks := make([]any, 0, len(parts))
		for _, part := range parts {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch transportString(block["type"]) {
			case "text":
				text, _ := block["text"].(string)
				if strings.TrimSpace(text) == "" {
					continue
				}
				blocks = append(blocks, copyBlock(block))
			default:
				blocks = append(blocks, copyBlock(block))
			}
		}
		return blocks
	}
}

func anthropicImageBlock(block map[string]any) map[string]any {
	var url string
	switch value := block["image_url"].(type) {
	case string:
		url = value
	case map[string]any:
		url = transportString(value["url"])
	}
	if url == "" {
		return nil
	}
	converted := map[string]any{"type": "image"}
	if strings.HasPrefix(url, "data:") {
		mediaType, data, ok := parseDataURL(url)
		if !ok {
			return nil
		}
		converted["source"] = map[string]any{
			"type":       "base64",
			"media_type": mediaType,
			"data":       data,
		}
	} else {
		converted["source"] = map[string]any{"type": "url", "url": url}
	}
	if cacheControl, ok := block["cache_control"].(map[string]any); ok {
		converted["cache_control"] = cacheControl
	}
	return converted
}

func parseDataURL(url string) (mediaType, data string, ok bool) {
	remainder := strings.TrimPrefix(url, "data:")
	comma := strings.Index(remainder, ",")
	if comma < 0 {
		return "", "", false
	}
	header := remainder[:comma]
	payload := remainder[comma+1:]
	mediaType = strings.TrimSuffix(header, ";base64")
	if mediaType == "" || !strings.Contains(header, "base64") {
		return "", "", false
	}
	return mediaType, payload, true
}

func anthropicToolResultContent(content any) any {
	switch typed := content.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		blocks := anthropicUserBlocks(typed)
		if len(blocks) == 0 {
			return ""
		}
		return blocks
	default:
		return fmt.Sprint(content)
	}
}

// anthropicTools maps OpenAI-style tool specs to Anthropic tool definitions.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete (tools passed
// as OpenAI function specs and rewritten by litellm for Anthropic)
func anthropicTools(raw any) []any {
	items, ok := asAnySlice(raw)
	if !ok {
		return nil
	}
	tools := make([]any, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		spec := entry
		if function, ok := entry["function"].(map[string]any); ok {
			spec = function
		}
		name := transportString(spec["name"])
		if name == "" {
			continue
		}
		parameters := spec["parameters"]
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tool := map[string]any{
			"name":         name,
			"input_schema": parameters,
		}
		if description := transportString(spec["description"]); description != "" {
			tool["description"] = description
		}
		if cacheControl, ok := entry["cache_control"].(map[string]any); ok {
			tool["cache_control"] = cacheControl
		}
		tools = append(tools, tool)
	}
	return tools
}

func anthropicToolChoice(raw any) any {
	switch typed := raw.(type) {
	case string:
		switch typed {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required", "any":
			return map[string]any{"type": "any"}
		case "none":
			return map[string]any{"type": "none"}
		}
	case map[string]any:
		if function, ok := typed["function"].(map[string]any); ok {
			if name := transportString(function["name"]); name != "" {
				return map[string]any{"type": "tool", "name": name}
			}
		}
		if name := transportString(typed["name"]); name != "" {
			return map[string]any{"type": "tool", "name": name}
		}
		switch transportString(typed["type"]) {
		case "auto", "any", "none":
			return map[string]any{"type": typed["type"]}
		}
	}
	return nil
}

func (payload anthropicMessagePayload) toCompletionResponse(requestedModel string) CompletionResponse {
	response := CompletionResponse{
		Model:        payload.Model,
		FinishReason: anthropicFinishReason(payload.StopReason),
	}
	if response.Model == "" {
		response.Model = requestedModel
	}
	var text strings.Builder
	for _, block := range payload.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			arguments := strings.TrimSpace(string(block.Input))
			if arguments == "" || arguments == "null" {
				arguments = "{}"
			}
			response.ToolCalls = append(response.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: arguments,
			})
		}
	}
	response.Content = text.String()
	if usage := payload.Usage; usage != nil {
		response.Usage = usage.toUsage()
	}
	return response
}

// anthropicFinishReason maps Anthropic stop reasons to the OpenAI vocabulary
// the rest of the runtime expects.
//
// Parity: litellm.litellm_core_utils.core_helpers.map_finish_reason
func anthropicFinishReason(stopReason string) string {
	switch stopReason {
	case "":
		return ""
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens", "compaction":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

func (usage anthropicUsagePayload) toUsage() *Usage {
	// Anthropic reports cache tokens separately from input_tokens; litellm
	// folds them into prompt_tokens so cost accounting sees the full prompt.
	promptTokens := usage.InputTokens
	cacheRead := usage.CacheReadInputTokens
	cacheCreation := usage.CacheCreationInputTokens
	converted := &Usage{
		PromptTokens:             promptTokens + cacheRead + cacheCreation,
		CompletionTokens:         usage.OutputTokens,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: cacheCreation,
	}
	if cacheRead > 0 || cacheCreation > 0 {
		converted.PromptTokensDetails = &PromptTokensDetails{
			CachedTokens:     cacheRead,
			CacheWriteTokens: cacheCreation,
		}
	}
	return converted
}

func copyBlock(block map[string]any) map[string]any {
	copied := make(map[string]any, len(block)+1)
	for key, value := range block {
		copied[key] = value
	}
	return copied
}

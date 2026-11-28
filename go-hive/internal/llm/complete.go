package llm

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

const (
	DefaultAuxMaxTokens = 8192

	// DefaultMaxTokens is upstream's DEFAULT_MAX_TOKENS: the output budget used
	// when neither configuration.json nor the model catalog pins one.
	//
	// Parity: framework/config.py::DEFAULT_MAX_TOKENS
	DefaultMaxTokens = 8192

	RateLimitMaxRetries = 10

	EmptyResponseMaxRetries = 3
)

type CompletionResponse struct {
	Content        string
	Model          string
	FinishReason   string
	ToolCalls      []ToolCall
	Usage          *Usage
	HiddenParams   map[string]any
	ThinkingBlocks []map[string]any
}

type CompletionCall func(ctx context.Context, kwargs map[string]any) (CompletionResponse, error)

type CompleteOptions struct {
	System string
	// SystemDynamicSuffix is the per-turn tail of the system prompt. When a
	// provider honors cache_control it travels as a second, uncached content
	// block so the static prefix stays warm across turns.
	//
	// Parity: core/framework/llm/litellm.py::_build_system_message
	SystemDynamicSuffix string
	Tools               []ToolDef
	MaxTokens           int
	ResponseFormat      map[string]any
	JSONMode            bool
	MaxRetries          *int
}

func ResolveAuxMaxTokens(configured int) int {
	if configured <= 0 || configured > DefaultAuxMaxTokens {
		return DefaultAuxMaxTokens
	}
	return configured
}

// auxMaxTokensOverride carries the configured llm.aux_max_tokens for the
// process. Zero means "not configured", which keeps the built-in cap.
var auxMaxTokensOverride atomic.Int64

// SetAuxMaxTokens installs the configured aux budget (llm.aux_max_tokens) for
// every small utility call in the process. A non-positive value clears the
// override, so an unconfigured home keeps the built-in cap.
//
// Parity: framework/config.py::get_aux_max_tokens (the reader itself lives in
// internal/server.AuxMaxTokens; this is its process-wide carrier).
func SetAuxMaxTokens(tokens int) {
	if tokens <= 0 {
		auxMaxTokensOverride.Store(0)
		return
	}
	auxMaxTokensOverride.Store(int64(tokens))
}

// AuxMaxTokens is the effective output budget for a small utility call: the
// configured llm.aux_max_tokens when set, else the built-in cap. Unlike
// ResolveAuxMaxTokens it does not clamp a configured value — a user who pins
// 16k for a thinking model means it.
//
// Parity: framework/config.py::get_aux_max_tokens
func AuxMaxTokens() int {
	if configured := auxMaxTokensOverride.Load(); configured > 0 {
		return int(configured)
	}
	return ResolveAuxMaxTokens(0)
}

func IsRateLimitError(err error) bool {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.StatusCode == 429
	}
	return false
}

func (p *Provider) Complete(ctx context.Context, messages []map[string]any, opts CompleteOptions) (Completion, error) {
	if p.Call == nil {
		return Completion{}, errors.New("llm: provider has no completion transport")
	}

	kwargs := p.buildCompletionKwargs(messages, opts)
	response, err := p.completionWithRetry(ctx, kwargs, opts.MaxRetries)
	if err != nil {
		// A rejected hive streamToken is refreshed once and the call replayed
		// with the new credential before the failure is surfaced.
		//
		// Parity: core/framework/llm/litellm.py (hive_stream_token_invalid ->
		// try_refresh_now -> retry the same call once)
		tokenRefreshed := false
		if token, ok := retryAfterStreamTokenRefresh(ctx, err, &tokenRefreshed); ok {
			kwargs["api_key"] = token
			response, err = p.completionWithRetry(ctx, kwargs, opts.MaxRetries)
		}
		if err != nil {
			return Completion{}, err
		}
	}
	return p.completionFromResponse(response), nil
}

// buildCompletionKwargs assembles the OpenAI-compatible request payload shared
// by the non-streaming and streaming transports, so both send the same model,
// messages, tools and sampling parameters.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete
func (p *Provider) buildCompletionKwargs(messages []map[string]any, opts CompleteOptions) map[string]any {
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = ResolveAuxMaxTokens(0)
	}

	full := make([]map[string]any, 0, len(messages)+1)
	if systemMessage := BuildSystemMessage(opts.System, opts.SystemDynamicSuffix, p.Model); systemMessage != nil {
		full = append(full, systemMessage)
	}
	for _, message := range messages {
		copied := make(map[string]any, len(message)+1)
		for key, value := range message {
			copied[key] = value
		}
		full = append(full, copied)
	}
	ApplyHistoryCacheBreakpoint(full, p.Model)

	if opts.JSONMode {
		AppendJSONInstruction(&full)
	}

	kwargs := map[string]any{
		"model":      p.Model,
		"messages":   full,
		"max_tokens": maxTokens,
	}
	for key, value := range p.ExtraKwargs {
		kwargs[key] = value
	}
	if p.APIKey != "" {
		kwargs["api_key"] = p.APIKey
	}
	if p.APIBase != "" {
		kwargs["api_base"] = p.APIBase
	}

	if len(opts.Tools) > 0 {
		tools := make([]any, 0, len(opts.Tools))
		for _, tool := range opts.Tools {
			tools = append(tools, ToolToOpenAIFormat(tool))
		}
		kwargs["tools"] = tools
		switch {
		case IsOllamaModel(p.Model):
			if _, ok := kwargs["tool_choice"]; !ok {
				kwargs["tool_choice"] = "auto"
			}
		case p.HiveProxyAuth:
			if _, ok := kwargs["tool_choice"]; !ok {
				kwargs["tool_choice"] = "required"
			}
		}
	}

	if len(opts.ResponseFormat) > 0 {
		kwargs["response_format"] = opts.ResponseFormat
	}
	return kwargs
}

// completionRequestKwargs renders a CompletionRequest into the same payload
// Complete sends. The streaming transport calls it directly instead of going
// through Complete, which owns the retry loop.
func (p *Provider) completionRequestKwargs(req CompletionRequest) map[string]any {
	return p.buildCompletionKwargs(ChatMessagesToMaps(req.Messages), CompleteOptions{
		System:              req.System,
		SystemDynamicSuffix: req.SystemDynamicSuffix,
		Tools:               req.Tools,
		MaxTokens:           req.MaxTokens,
		JSONMode:            req.JSONMode,
	})
}

// completionFromResponse folds a decoded upstream response into the Completion
// the agent loop consumes. Both transports return the same shape.
func (p *Provider) completionFromResponse(response CompletionResponse) Completion {
	cachedTokens, cacheCreationTokens := ExtractCacheTokens(response.Usage)
	completion := Completion{
		Content:             response.Content,
		Model:               response.Model,
		StopReason:          response.FinishReason,
		ToolCalls:           response.ToolCalls,
		CostUSD:             ExtractCost(response.Usage, response.HiddenParams, p.Model),
		CachedTokens:        cachedTokens,
		CacheCreationTokens: cacheCreationTokens,
		ThinkingBlocks:      response.ThinkingBlocks,
	}
	if response.Usage != nil {
		completion.InputTokens = response.Usage.PromptTokens
		completion.OutputTokens = response.Usage.CompletionTokens
		completion.Credits = response.Usage.Credits
	}
	if completion.Model == "" {
		completion.Model = p.Model
	}
	return completion
}

func (p *Provider) completionWithRetry(ctx context.Context, kwargs map[string]any, maxRetries *int) (CompletionResponse, error) {
	retries := RateLimitMaxRetries
	if maxRetries != nil {
		retries = *maxRetries
	}
	if retries < 0 {
		retries = 0
	}
	emptyCap := retries
	if EmptyResponseMaxRetries < emptyCap {
		emptyCap = EmptyResponseMaxRetries
	}

	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return CompletionResponse{}, err
		}
		response, err := p.Call(ctx, kwargs)
		if err != nil {
			if !IsRateLimitError(err) {
				return CompletionResponse{}, err
			}
			if attempt == retries {
				return CompletionResponse{}, err
			}
			// A cancelled caller must not sit through the retry backoff; the
			// sleep is not interruptible and can last RateLimitMaxDelay.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return CompletionResponse{}, ctxErr
			}
			p.sleepFor(ComputeRetryDelay(attempt, err, RetryPolicy{Jitter: true}))
			continue
		}

		if response.Content != "" || len(response.ToolCalls) > 0 {
			return response, nil
		}
		if lastConversationRole(kwargs["messages"]) == "assistant" {
			return response, nil
		}
		if response.FinishReason == "length" {
			return response, nil
		}
		if attempt >= emptyCap {
			return response, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return CompletionResponse{}, ctxErr
		}
		p.sleepFor(ComputeRetryDelay(attempt, nil, RetryPolicy{}))
	}
	return CompletionResponse{}, fmt.Errorf("llm: exhausted rate limit retries")
}

func lastConversationRole(value any) string {
	messages, ok := asMapSlice(value)
	if !ok {
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role != "system" {
			return role
		}
	}
	return ""
}

func (p *Provider) sleepFor(seconds float64) {
	duration := time.Duration(seconds * float64(time.Second))
	if p.SleepFunc != nil {
		p.SleepFunc(duration)
		return
	}
	time.Sleep(duration)
}

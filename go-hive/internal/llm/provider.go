package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const DefaultModel = "gpt-4o-mini"

const (
	RateLimitBackoffBase = 2
	RateLimitMaxDelay    = 120

	MinimaxAPIBase    = "https://api.minimax.io/v1"
	OpenRouterAPIBase = "https://openrouter.ai/api/v1"
	KimiAPIBase       = "https://api.kimi.com/coding"
	HiveAPIBase       = HiveLLMEndpoint

	BackpressureRetryFloor = 1.0

	ShortRetryAfterThreshold = 5.0
)

type ProviderOptions struct {
	Model   string
	APIKey  string
	APIBase string
}

type Provider struct {
	Model   string
	APIKey  string
	APIBase string

	Call CompletionCall

	// Gate paces one outbound HTTP attempt against the process's configured
	// call limits (rate_limits.llm.* in hive.config.json). Nil means
	// unlimited; NewHTTPProvider installs ratelimit.AcquireLLMRequest, which
	// is a no-op until an llm bucket is configured. A provider driven through
	// a scripted Call (tests, embeddings) is never paced.
	Gate func(ctx context.Context) error

	ExtraKwargs map[string]any

	// KeyPool rotates through llm.api_keys when the configured pool has two
	// or more keys (upstream's KeyPool). Nil keeps APIKey as the single key.
	KeyPool *KeyPool

	HiveProxyAuth bool

	// PaymentRequiredSink, when installed by the session runtime, receives the
	// dedicated billing-gate event for a classified 402. PaymentRequiredStreamID
	// and PaymentRequiredExecutionID identify the SSE stream/execution the event
	// belongs to (nil execution is allowed).
	//
	// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
	// (StreamErrorEvent error_type=="payment_required" -> emit_payment_required)
	PaymentRequiredSink        PaymentRequiredEmitter
	PaymentRequiredStreamID    string
	PaymentRequiredExecutionID *string

	SleepFunc func(time.Duration)

	refusal parallelToolsRefusalLatch
}

func NewProvider(opts ProviderOptions) *Provider {
	model := opts.Model
	if model == "" {
		model = DefaultModel
	}
	model = EnsureOllamaChatPrefix(model)

	base := opts.APIBase
	if base == "" {
		if def, ok := DefaultAPIBaseForModel(model); ok {
			base = def
		}
	}
	return &Provider{Model: model, APIKey: opts.APIKey, APIBase: base}
}

func (p *Provider) ModelName() string { return p.Model }

func DefaultAPIBaseForModel(model string) (string, bool) {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "minimax/"), strings.HasPrefix(m, "minimax-"):
		return MinimaxAPIBase, true
	case strings.HasPrefix(m, "openrouter/"):
		return OpenRouterAPIBase, true
	case strings.HasPrefix(m, "kimi/"):
		return KimiAPIBase, true
	case strings.HasPrefix(m, "hive/"):
		return HiveAPIBase, true
	}
	return "", false
}

func IsMinimaxModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "minimax/") || strings.HasPrefix(m, "minimax-")
}

var localModelPrefixes = []string{
	"ollama/",
	"ollama_chat/",
	"vllm/",
	"lm_studio/",
	"llamacpp/",
}

func IsLocalModel(model string) bool {
	m := strings.ToLower(model)
	for _, prefix := range localModelPrefixes {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

func IsOllamaModel(model string) bool {
	return strings.HasPrefix(model, "ollama/") || strings.HasPrefix(model, "ollama_chat/")
}

func EnsureOllamaChatPrefix(model string) string {
	if strings.HasPrefix(model, "ollama/") {
		return "ollama_chat/" + strings.TrimPrefix(model, "ollama/")
	}
	return model
}

var cacheControlPrefixes = []string{
	"anthropic/",
	"claude-",
	"minimax/",
	"minimax-",
	"MiniMax-",
	"zai-glm",
	"glm-",
}

var openRouterCacheControlPrefixes = []string{
	"openrouter/anthropic/",
	"openrouter/google/gemini-",
	"openrouter/z-ai/glm",
	"openrouter/minimax/",
}

func ModelSupportsCacheControl(model string) bool {
	for _, prefix := range cacheControlPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	for _, prefix := range openRouterCacheControlPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// MergedSystemPrompt folds the per-turn dynamic suffix into the static system
// prompt. Both the streaming and the non-streaming completion paths use it so
// the two transports send byte-identical system text.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
// (system + system_dynamic_suffix)
func MergedSystemPrompt(system, dynamicSuffix string) string {
	if dynamicSuffix == "" {
		return system
	}
	if system == "" {
		return dynamicSuffix
	}
	return system + "\n\n" + dynamicSuffix
}

func BuildSystemMessage(system, dynamicSuffix, model string) map[string]any {
	if system == "" && dynamicSuffix == "" {
		return nil
	}
	if dynamicSuffix != "" && ModelSupportsCacheControl(model) {
		blocks := make([]any, 0, 2)
		if system != "" {
			blocks = append(blocks, map[string]any{
				"type":          "text",
				"text":          system,
				"cache_control": map[string]any{"type": "ephemeral"},
			})
		}
		blocks = append(blocks, map[string]any{"type": "text", "text": dynamicSuffix})
		return map[string]any{"role": "system", "content": blocks}
	}

	combined := system
	if dynamicSuffix != "" {
		if system != "" {
			combined = system + "\n\n" + dynamicSuffix
		} else {
			combined = dynamicSuffix
		}
	}
	msg := map[string]any{"role": "system", "content": combined}
	if ModelSupportsCacheControl(model) {
		msg["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	return msg
}

const jsonInstruction = "\n\nPlease respond with a valid JSON object."

func AppendJSONInstruction(messages *[]map[string]any) {
	if messages == nil {
		return
	}
	msgs := *messages
	if len(msgs) > 0 && msgs[0]["role"] == "system" {
		switch content := msgs[0]["content"].(type) {
		case string:
			msgs[0]["content"] = content + jsonInstruction
		case []any:
			msgs[0]["content"] = append(content, map[string]any{
				"type": "text",
				"text": strings.TrimSpace(jsonInstruction),
			})
		case []map[string]any:
			blocks := make([]any, len(content), len(content)+1)
			for i, block := range content {
				blocks[i] = block
			}
			msgs[0]["content"] = append(blocks, map[string]any{
				"type": "text",
				"text": strings.TrimSpace(jsonInstruction),
			})
		}
		return
	}
	inserted := map[string]any{
		"role":    "system",
		"content": strings.TrimSpace(jsonInstruction),
	}
	*messages = append([]map[string]any{inserted}, msgs...)
}

func HistoryCacheBreakpointIndex(messages []map[string]any, model string) int {
	if !ModelSupportsCacheControl(model) {
		return -1
	}
	anchor := len(messages) - 1
	if anchor < 0 {
		return -1
	}
	if messages[anchor]["role"] == "system" {
		return -1
	}
	return anchor
}

func ApplyHistoryCacheBreakpoint(messages []map[string]any, model string) {
	anchor := HistoryCacheBreakpointIndex(messages, model)
	if anchor < 0 {
		return
	}
	messages[anchor]["cache_control"] = map[string]any{"type": "ephemeral"}
}

type RetryPolicy struct {
	BackoffBase float64
	MaxDelay    float64
	Jitter      bool
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{BackoffBase: RateLimitBackoffBase, MaxDelay: RateLimitMaxDelay}
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.BackoffBase <= 0 {
		p.BackoffBase = RateLimitBackoffBase
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = RateLimitMaxDelay
	}
	return p
}

type ProviderError struct {
	Message    string
	StatusCode int
	Headers    map[string]string
}

func (e *ProviderError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("provider error (status %d)", e.StatusCode)
	}
	return "provider error"
}

func (e *ProviderError) RetryHeaders() map[string]string { return e.Headers }

type retryHeaderer interface {
	RetryHeaders() map[string]string
}

func headersFromError(err error) map[string]string {
	var carrier retryHeaderer
	if errors.As(err, &carrier) {
		return carrier.RetryHeaders()
	}
	return nil
}

func headerLookup(headers map[string]string, name string) (string, bool) {
	if value, ok := headers[name]; ok {
		return value, true
	}
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

func clampFloat(value, low, high float64) float64 {
	return math.Min(math.Max(value, low), high)
}

func ParseRetryAfter(headers map[string]string, maxDelay float64) (float64, bool) {
	if raw, ok := headerLookup(headers, "retry-after-ms"); ok {
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			return clampFloat(value/1000.0, 0, maxDelay), true
		}
	}
	if raw, ok := headerLookup(headers, "retry-after"); ok {
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			return clampFloat(value, 0, maxDelay), true
		}
		if when, err := http.ParseTime(strings.TrimSpace(raw)); err == nil {
			return clampFloat(time.Until(when).Seconds(), 0, maxDelay), true
		}
	}
	return 0, false
}

func ComputeRetryDelay(attempt int, err error, policy RetryPolicy) float64 {
	policy = policy.normalized()

	if err != nil {
		if headers := headersFromError(err); headers != nil {
			if delay, ok := ParseRetryAfter(headers, policy.MaxDelay); ok {
				if policy.Jitter && delay < ShortRetryAfterThreshold {
					delay = delay*(1.0+rand.Float64()*0.5) + rand.Float64()*0.5
					delay = math.Min(delay, policy.MaxDelay)
				}
				return delay
			}
		}
		if strings.Contains(strings.ToLower(err.Error()), "backpressure") {
			delay := BackpressureRetryFloor
			if policy.Jitter {
				delay = delay*(1.0+rand.Float64()*0.5) + rand.Float64()*0.5
			}
			return math.Min(delay, policy.MaxDelay)
		}
	}

	delay := policy.BackoffBase * math.Pow(2, float64(attempt))
	delay = math.Min(delay, policy.MaxDelay)
	if policy.Jitter {
		delay *= 0.75 + rand.Float64()*0.5
	}
	return delay
}

type PromptTokensDetails struct {
	CachedTokens     int
	CacheWriteTokens int
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	// Credits is the Hive proxy's cumulative per-request credit charge.
	// Nil means absent (direct provider models), not zero.
	//
	// Parity: core/framework/llm/litellm.py::_credits_from_usage
	Credits *float64

	PromptTokensDetails *PromptTokensDetails

	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

func ExtractCacheTokens(usage *Usage) (int, int) {
	if usage == nil {
		return 0, 0
	}
	var cacheRead, cacheCreation int
	if details := usage.PromptTokensDetails; details != nil {
		cacheRead = details.CachedTokens
		cacheCreation = details.CacheWriteTokens
	} else {
		cacheRead = usage.CacheReadInputTokens
	}
	if cacheCreation == 0 {
		cacheCreation = usage.CacheCreationInputTokens
	}
	return cacheRead, cacheCreation
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func ExtractCost(usage *Usage, hiddenParams map[string]any, model string) float64 {
	if usage == nil {
		return 0
	}
	if usage.Cost > 0 {
		return usage.Cost
	}
	if hiddenParams != nil {
		if value, ok := toFloat(hiddenParams["response_cost"]); ok && value > 0 {
			return value
		}
	}
	cacheRead, cacheCreation := ExtractCacheTokens(usage)
	return CostFromCatalogPricing(
		model,
		usage.PromptTokens,
		usage.CompletionTokens,
		cacheRead,
		cacheCreation,
	)
}

func CostFromCatalogPricing(model string, inputTokens, outputTokens, cachedTokens, cacheCreationTokens int) float64 {
	if model == "" || (inputTokens == 0 && outputTokens == 0) {
		return 0
	}
	pricing, ok := GetModelPricing(model)
	if !ok && strings.Contains(model, "/") {
		pricing, ok = GetModelPricing(model[strings.Index(model, "/")+1:])
	}
	if !ok {
		return 0
	}

	perMTokIn := pricing["input"]
	perMTokOut := pricing["output"]
	perMTokCacheRead, hasRead := pricing["cache_read"]
	if !hasRead {
		perMTokCacheRead = perMTokIn
	}
	perMTokCacheWrite, hasWrite := pricing["cache_creation"]
	if !hasWrite {
		perMTokCacheWrite = perMTokIn
	}

	plainInput := inputTokens - cachedTokens - cacheCreationTokens
	if plainInput < 0 {
		plainInput = 0
	}
	total := (float64(plainInput)*perMTokIn +
		float64(cachedTokens)*perMTokCacheRead +
		float64(cacheCreationTokens)*perMTokCacheWrite +
		float64(outputTokens)*perMTokOut) / 1_000_000
	if total > 0 {
		return total
	}
	return 0
}

func CostFromTokens(model string, inputTokens, outputTokens, cachedTokens, cacheCreationTokens int) float64 {
	if model == "" || (inputTokens == 0 && outputTokens == 0) {
		return 0
	}
	return CostFromCatalogPricing(model, inputTokens, outputTokens, cachedTokens, cacheCreationTokens)
}

func SummarizeMessageContent(content any) map[string]any {
	switch value := content.(type) {
	case nil:
		return map[string]any{"content_kind": "none"}
	case string:
		return map[string]any{
			"content_kind": "string",
			"text_chars":   len(value),
		}
	}

	blocks, ok := asAnySlice(content)
	if !ok {
		return map[string]any{"content_kind": fmt.Sprintf("%T", content)}
	}

	blockTypes := make([]string, 0, len(blocks))
	textChars := 0
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			blockTypes = append(blockTypes, fmt.Sprintf("%T", raw))
			continue
		}
		blockType := "unknown"
		if value, ok := block["type"]; ok && value != nil {
			blockType = fmt.Sprint(value)
		}
		blockTypes = append(blockTypes, blockType)
		switch blockType {
		case "text":
			textChars += len(fmt.Sprint(block["text"]))
		case "tool_result":
			switch blockContent := block["content"].(type) {
			case string:
				textChars += len(blockContent)
			case []any:
				for _, inner := range blockContent {
					if innerMap, ok := inner.(map[string]any); ok && innerMap["type"] == "text" {
						textChars += len(fmt.Sprint(innerMap["text"]))
					}
				}
			}
		}
	}
	return map[string]any{
		"content_kind": "list",
		"blocks":       len(blocks),
		"block_types":  blockTypes,
		"text_chars":   textChars,
	}
}

func SummarizeMessagesForLog(messages []map[string]any) []map[string]any {
	summary := make([]map[string]any, 0, len(messages))
	for idx, message := range messages {
		keys := make([]string, 0, len(message))
		for key := range message {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		item := map[string]any{
			"idx":  idx,
			"role": message["role"],
			"keys": keys,
		}
		for key, value := range SummarizeMessageContent(message["content"]) {
			item[key] = value
		}
		if toolCalls, ok := asAnySlice(message["tool_calls"]); ok {
			item["tool_calls"] = len(toolCalls)
			names := make([]string, 0, len(toolCalls))
			for _, raw := range toolCalls {
				toolCall, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if fn, ok := toolCall["function"].(map[string]any); ok {
					if name, ok := fn["name"]; ok && fmt.Sprint(name) != "" {
						names = append(names, fmt.Sprint(name))
					}
				}
			}
			if len(names) > 0 {
				item["tool_call_names"] = names
			}
		}
		if truthy(message["cache_control"]) {
			item["cache_control"] = true
		}
		if toolCallID := message["tool_call_id"]; truthy(toolCallID) {
			item["tool_call_id"] = fmt.Sprint(toolCallID)
		}
		summary = append(summary, item)
	}
	return summary
}

func SummarizeRequestForLog(kwargs map[string]any) map[string]any {
	tools, _ := asAnySlice(kwargs["tools"])
	toolNames := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := fn["name"]; ok && truthy(name) {
			toolNames = append(toolNames, fmt.Sprint(name))
		}
	}

	messages, _ := asMapSlice(kwargs["messages"])
	nonSystemRoles := make([]string, 0, len(messages))
	for _, message := range messages {
		role := fmt.Sprint(message["role"])
		if role != "system" {
			nonSystemRoles = append(nonSystemRoles, role)
		}
	}

	var firstRole, lastRole any
	if len(nonSystemRoles) > 0 {
		firstRole = nonSystemRoles[0]
		lastRole = nonSystemRoles[len(nonSystemRoles)-1]
	}

	return map[string]any{
		"model":                    kwargs["model"],
		"api_base":                 kwargs["api_base"],
		"stream":                   kwargs["stream"],
		"max_tokens":               kwargs["max_tokens"],
		"tool_count":               len(tools),
		"tool_names":               toolNames,
		"tool_choice":              kwargs["tool_choice"],
		"response_format":          truthy(kwargs["response_format"]),
		"message_count":            len(messages),
		"non_system_message_count": len(nonSystemRoles),
		"first_non_system_role":    firstRole,
		"last_non_system_role":     lastRole,
		"system_only":              len(messages) > 0 && len(nonSystemRoles) == 0,
		"messages":                 SummarizeMessagesForLog(messages),
	}
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case []map[string]any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func asAnySlice(value any) ([]any, bool) {
	switch v := value.(type) {
	case []any:
		return v, true
	case []map[string]any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = item
		}
		return out, true
	}
	return nil, false
}

func asMapSlice(value any) ([]map[string]any, bool) {
	switch v := value.(type) {
	case []map[string]any:
		return v, true
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if message, ok := item.(map[string]any); ok {
				out = append(out, message)
			}
		}
		return out, true
	}
	return nil, false
}

// ToolToOpenAIFormat wraps a tool definition in the OpenAI function schema.
//
// `required` is carried over exactly as the definition declares it: emitted when
// the schema has the key (even as an empty list), absent otherwise. The Python
// runtime's request shape keeps that distinction — pydantic omits the key when
// nothing is required, while the MCP tool schemas (write_skill) publish
// `required: []` — and the tool-contract recheck compares the request
// byte-for-byte, so a synthesised empty list would be visible drift.
func ToolToOpenAIFormat(tool ToolDef) map[string]any {
	var properties any = map[string]any{}
	parameters := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if tool.Parameters != nil {
		if value, ok := tool.Parameters["properties"]; ok && value != nil {
			parameters["properties"] = value
		}
		if value, ok := tool.Parameters["required"]; ok && value != nil {
			parameters["required"] = value
		}
	}
	return toolDefinitionForRequest(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  parameters,
		},
	})
}

// toolDefinitionForRequest applies the Python LLM-boundary path rewrite to a
// projected tool definition: prompt text says ~/.hive, but the runtime's hive
// root can live elsewhere (the desktop ships it under Application Support), so
// the model must see the real on-disk location. When HIVE_HOME cannot be
// resolved the definition is returned unchanged rather than rewritten to an
// empty string.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format
// (resolve_hive_paths_deep over the whole function dict).
func toolDefinitionForRequest(tool map[string]any) map[string]any {
	home, ok := toolDescriptionHiveHome()
	if !ok {
		return tool
	}
	rewritten, ok := resolveHivePathsDeep(tool, home).(map[string]any)
	if !ok {
		return tool
	}
	return rewritten
}

func CloseTruncatedJSONFragment(fragment string) string {
	stack := make([]byte, 0, 8)
	inString := false
	escaped := false

	normalized := strings.TrimRightFunc(fragment, unicode.IsSpace)
	for len(normalized) > 0 {
		switch normalized[len(normalized)-1] {
		case ',', ':', '{', '[':
			normalized = strings.TrimRightFunc(normalized[:len(normalized)-1], unicode.IsSpace)
			continue
		}
		break
	}

	for i := 0; i < len(normalized); i++ {
		char := normalized[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, char)
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
			}
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == '[' {
				stack = stack[:len(stack)-1]
			}
		}
	}

	if inString {
		if escaped {
			normalized = normalized[:len(normalized)-1]
		}
		normalized += `"`
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			normalized += "}"
		} else {
			normalized += "]"
		}
	}
	return normalized
}

func RepairTruncatedToolArguments(rawArguments string) (map[string]any, bool) {
	stripped := strings.TrimSpace(rawArguments)
	if stripped == "" || stripped[0] != '{' {
		return nil, false
	}

	maxTrim := len(stripped)
	if maxTrim > 256 {
		maxTrim = 256
	}
	for trim := 0; trim <= maxTrim; trim++ {
		candidate := strings.TrimRightFunc(stripped[:len(stripped)-trim], unicode.IsSpace)
		if candidate == "" {
			break
		}
		candidate = CloseTruncatedJSONFragment(candidate)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
			continue
		}
		if parsed != nil {
			return parsed, true
		}
	}
	return nil, false
}

func ParseToolCallArguments(rawArguments, toolName string) (map[string]any, error) {
	if rawArguments != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(rawArguments), &parsed); err == nil && parsed != nil {
			return parsed, nil
		}
	} else {
		return map[string]any{}, nil
	}

	if repaired, ok := RepairTruncatedToolArguments(rawArguments); ok {
		return repaired, nil
	}
	return nil, fmt.Errorf("Failed to parse tool call arguments for '%s' (likely truncated JSON).", toolName)
}

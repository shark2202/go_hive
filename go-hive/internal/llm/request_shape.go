package llm

import (
	"encoding/json"
	"strings"
	"sync"
)

func IsAnthropicModel(model string) bool {
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "anthropic/") || strings.HasPrefix(model, "claude-")
}

type parallelToolsRefusalLatch struct {
	mu      sync.Mutex
	refused bool
}

func (p *Provider) RequestParallelToolCalls(hasTools bool) bool {
	if !hasTools || IsAnthropicModel(p.Model) {
		return false
	}
	p.refusal.mu.Lock()
	defer p.refusal.mu.Unlock()
	return !p.refusal.refused
}

func (p *Provider) NoteError(err error) bool {
	if err == nil {
		return false
	}
	if !strings.Contains(err.Error(), "parallel_tool_calls") {
		return false
	}
	p.refusal.mu.Lock()
	defer p.refusal.mu.Unlock()
	if p.refusal.refused {
		return false
	}
	p.refusal.refused = true
	return true
}

func (p *Provider) ParallelToolCallsRefused() bool {
	p.refusal.mu.Lock()
	defer p.refusal.mu.Unlock()
	return p.refusal.refused
}

type toolCompatSpec struct {
	name        string
	description string
	parameters  any
}

func marshalToolSpecs(tools []ToolDef) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, tool := range tools {
		if i > 0 {
			b.WriteString(", ")
		}
		params := tool.Parameters
		if params == nil {
			params = map[string]any{}
		}
		name, _ := json.Marshal(tool.Name)
		description, _ := json.Marshal(tool.Description)
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			paramsJSON = []byte("{}")
		}
		b.WriteString(`{"name": `)
		b.Write(name)
		b.WriteString(`, "description": `)
		b.Write(description)
		b.WriteString(`, "parameters": `)
		b.Write(paramsJSON)
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.String()
}

const toolCompatInstructionPrefix = "Tool compatibility mode is active because this OpenRouter model does not support " +
	"native function calling on the routed provider.\n" +
	"Return exactly one JSON object and nothing else.\n" +
	"Schema: {\"assistant_response\": string, " +
	"\"tool_calls\": [{\"name\": string, \"arguments\": object}]}\n" +
	"Rules:\n" +
	"- If a tool is required, put one or more entries in tool_calls " +
	"and do not invent tool results.\n" +
	"- If no tool is required, set tool_calls to [] and put the full " +
	"answer in assistant_response.\n" +
	"- Only use tool names from the allowed tool list.\n" +
	"- arguments must always be valid JSON objects.\n" +
	"Allowed tools:\n"

func BuildOpenRouterToolCompatMessages(messages []map[string]any, system string, tools []ToolDef, systemDynamicSuffix, model string) []map[string]any {
	instruction := toolCompatInstructionPrefix + marshalToolSpecs(tools)
	compatSystem := instruction
	if system != "" {
		compatSystem = system + "\n\n" + instruction
	}

	full := make([]map[string]any, 0, len(messages)+1)
	if systemMessage := BuildSystemMessage(compatSystem, systemDynamicSuffix, model); systemMessage != nil {
		full = append(full, systemMessage)
	}
	for _, message := range messages {
		copied := make(map[string]any, len(message)+1)
		for key, value := range message {
			copied[key] = value
		}
		full = append(full, copied)
	}
	ApplyHistoryCacheBreakpoint(full, model)

	filtered := make([]map[string]any, 0, len(full))
	for _, message := range full {
		if message["role"] == "assistant" && isEmptyContent(message["content"]) && !hasToolCalls(message) {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func isEmptyContent(content any) bool {
	switch value := content.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	case []map[string]any:
		return len(value) == 0
	}
	return false
}

func hasToolCalls(message map[string]any) bool {
	switch value := message["tool_calls"].(type) {
	case nil:
		return false
	case []any:
		return len(value) > 0
	case []ToolCall:
		return len(value) > 0
	case []map[string]any:
		return len(value) > 0
	}
	return true
}

func ExtraLLMKWargs(llmSection map[string]any, apiKey, codexAccountID string) map[string]any {
	if truthy(llmSection["use_claude_code_subscription"]) && apiKey != "" {
		return map[string]any{
			"extra_headers": map[string]any{"authorization": "Bearer " + apiKey},
		}
	}
	if truthy(llmSection["use_codex_subscription"]) && apiKey != "" {
		headers := map[string]any{
			"Authorization": "Bearer " + apiKey,
			"User-Agent":    "CodexBar",
		}
		if codexAccountID != "" {
			headers["ChatGPT-Account-Id"] = codexAccountID
		}
		return map[string]any{
			"extra_headers":         headers,
			"store":                 false,
			"allowed_openai_params": []any{"store"},
		}
	}
	if provider, _ := llmSection["provider"].(string); provider == "ollama" {
		return map[string]any{"num_ctx": numCtxOr(llmSection["num_ctx"], 16384)}
	}
	if extraBody, ok := llmSection["extra_body"].(map[string]any); ok && len(extraBody) > 0 {
		copied := make(map[string]any, len(extraBody))
		for key, value := range extraBody {
			copied[key] = value
		}
		return map[string]any{"extra_body": copied}
	}
	return map[string]any{}
}

func numCtxOr(value any, fallback int) int {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed > 0 {
			return int(parsed)
		}
	case int:
		if typed > 0 {
			return typed
		}
	case int64:
		if typed > 0 {
			return int(typed)
		}
	case float64:
		if typed > 0 {
			return int(typed)
		}
	}
	return fallback
}

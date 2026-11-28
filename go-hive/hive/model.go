package hive

import "github.com/shark2202/go_hive/go-hive/internal/llm"

// Model is one completion transport. The runtime calls AComplete once per
// agent-loop iteration.
//
// The alias is the public provider contract: a derivative project can
// implement it (the request and response value types are exported below) or
// build one with NewProvider, NewAnthropicProvider or NewMockProvider.
//
// Parity: framework/llm/provider.py::LLMProvider
type Model = llm.Completer

// Completion is one model answer: text, tool calls and token accounting.
//
// Parity: framework/llm/provider.py::LLMResponse
type Completion = llm.Completion

// CompletionRequest is what the agent loop sends to a Model.
//
// Parity: framework/llm/provider.py::LLMProvider.complete
type CompletionRequest = llm.CompletionRequest

// ChatMessage is one message in a provider request.
//
// Parity: framework/agent_loop/conversation.py::Message.to_llm_dict
type ChatMessage = llm.ChatMessage

// ToolCall is the provider's wire-form tool call: arguments are a JSON string.
//
// Parity-exempt: Go wire-level type (llm.ToolCall); Python carries the same
// data on its stream events instead of a named provider class.
type ToolCall = llm.ToolCall

// ProviderOptions configures the OpenAI-compatible HTTP transport.
//
// APIKey is a caller-supplied credential value, not a value the SDK reads
// back out of a Hive home.
//
// Parity: framework/llm/litellm.py::LiteLLMProvider
type ProviderOptions = llm.ProviderOptions

// NewProvider builds an OpenAI-compatible completion transport.
//
// Parity: framework/llm/litellm.py::LiteLLMProvider
func NewProvider(opts ProviderOptions) Model {
	return llm.NewHTTPProvider(opts)
}

// NewAnthropicProvider builds the Anthropic Messages transport.
//
// Parity: framework/llm/anthropic.py::AnthropicProvider
func NewAnthropicProvider(apiKey, model string) (Model, error) {
	return llm.NewAnthropicProvider(apiKey, model)
}

// NewMockProvider builds the deterministic mock transport used in tests and
// offline demos. The model name defaults to "mock".
//
// Parity: framework/llm/mock.py::MockLLMProvider
func NewMockProvider(model ...string) Model {
	return llm.NewMockProvider(model...)
}

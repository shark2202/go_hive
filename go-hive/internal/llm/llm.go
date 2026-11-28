package llm

import "context"

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ToolDef struct {
	Name            string
	Description     string
	Parameters      map[string]any
	ProducesImage   bool
	ConcurrencySafe bool
}

type ChatMessage struct {
	Role string
	// Content is the text projection of the message. It is what text-only
	// transports send and what logs/telemetry read.
	Content string
	// ContentParts, when non-empty, is the OpenAI-style content block list
	// (text / image_url / file). Transports emit it as the message's content
	// array instead of Content, so attachments surfaced as images or PDFs
	// reach the model as real multimodal parts.
	ContentParts []map[string]any
	ToolCalls    []ToolCall
	ToolCallID   string
	// ThinkingBlocks is the assistant turn's reasoning/`thinking` content
	// blocks, carrying each block's opaque signature verbatim. Anthropic
	// rejects the next request when a signed thinking block is missing, so
	// the blocks must be echoed back on follow-up requests.
	//
	// Parity: core/framework/agent_loop/conversation.py::Message.thinking_blocks
	ThinkingBlocks []map[string]any
	// ReasoningContent is the thinking an assistant turn streamed as the
	// OpenAI-compatible ``reasoning_content`` string (DeepSeek, Qwen, GLM).
	// The agent loop echoes it back for the steps of the current turn, where
	// these models' chat templates read it — dropping it makes every earlier
	// step of the turn read as unthought.
	//
	// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict
	ReasoningContent string
}

type CompletionRequest struct {
	Messages            []ChatMessage
	System              string
	Tools               []ToolDef
	MaxTokens           int
	JSONMode            bool
	SystemDynamicSuffix string
}

type Completion struct {
	Content             string
	Model               string
	StopReason          string
	ToolCalls           []ToolCall
	CostUSD             float64
	InputTokens         int
	OutputTokens        int
	CachedTokens        int
	CacheCreationTokens int
	// Credits is the Hive proxy's cumulative per-request credit charge
	// (``usage.credits``). Nil means "no estimate" per the proxy spec, not
	// zero; direct provider models never carry it.
	//
	// Parity: core/framework/llm/litellm.py::_credits_from_usage
	Credits *float64
	// ReasoningContent is the reasoning an OpenAI-compatible model streamed
	// (``delta.reasoning_content``) or returned on the message. The agent loop
	// stores it on the assistant turn and echoes it back for the current
	// turn's later steps.
	//
	// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream (reasoning_acc)
	ReasoningContent string
	// ThinkingBlocks is the signed reasoning/`thinking` content blocks an
	// Anthropic Messages stream produced, reassembled from thinking_delta /
	// signature_delta frames. It mirrors FinishEvent.ThinkingBlocks and feeds
	// the assistant turn's persisted blocks so the next request can echo
	// them back (unsigned blocks are dropped).
	//
	// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
	// (thinking_acc -> FinishEvent.thinking_blocks)
	ThinkingBlocks []map[string]any
}

type Completer interface {
	AComplete(ctx context.Context, req CompletionRequest) (Completion, error)
}

type ModelNamer interface {
	ModelName() string
}

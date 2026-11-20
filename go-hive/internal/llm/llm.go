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
}

type Completer interface {
	AComplete(ctx context.Context, req CompletionRequest) (Completion, error)
}

type ModelNamer interface {
	ModelName() string
}

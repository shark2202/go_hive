package llm

import "fmt"

type StreamEvent interface {
	StreamEventType() string
}

type TextDeltaEvent struct {
	Content  string
	Snapshot string
}

func (TextDeltaEvent) StreamEventType() string { return "text_delta" }

type TextEndEvent struct {
	FullText string
}

func (TextEndEvent) StreamEventType() string { return "text_end" }

type ToolCallEvent struct {
	ToolUseID string
	ToolName  string
	ToolInput map[string]any
}

func (ToolCallEvent) StreamEventType() string { return "tool_call" }

type ToolResultEvent struct {
	ToolUseID string
	Content   string
	IsError   bool
}

func (ToolResultEvent) StreamEventType() string { return "tool_result" }

type ReasoningStartEvent struct{}

func (ReasoningStartEvent) StreamEventType() string { return "reasoning_start" }

type ReasoningDeltaEvent struct {
	Content string
}

func (ReasoningDeltaEvent) StreamEventType() string { return "reasoning_delta" }

type FinishEvent struct {
	StopReason          string
	InputTokens         int
	OutputTokens        int
	CachedTokens        int
	CacheCreationTokens int
	CostUSD             float64
	Credits             *float64
	Model               string
	ThinkingBlocks      []map[string]any
	ReasoningContent    string
}

func (FinishEvent) StreamEventType() string { return "finish" }

type StreamErrorEvent struct {
	Error          string
	Recoverable    bool
	ErrorType      *string
	UpstreamStatus *int
}

func (StreamErrorEvent) StreamEventType() string { return "error" }

func StreamEventToMap(event StreamEvent) (map[string]any, error) {
	switch e := event.(type) {
	case TextDeltaEvent:
		return map[string]any{
			"type":     e.StreamEventType(),
			"content":  e.Content,
			"snapshot": e.Snapshot,
		}, nil
	case TextEndEvent:
		return map[string]any{
			"type":      e.StreamEventType(),
			"full_text": e.FullText,
		}, nil
	case ToolCallEvent:
		input := e.ToolInput
		if input == nil {
			input = map[string]any{}
		}
		return map[string]any{
			"type":        e.StreamEventType(),
			"tool_use_id": e.ToolUseID,
			"tool_name":   e.ToolName,
			"tool_input":  input,
		}, nil
	case ToolResultEvent:
		return map[string]any{
			"type":        e.StreamEventType(),
			"tool_use_id": e.ToolUseID,
			"content":     e.Content,
			"is_error":    e.IsError,
		}, nil
	case ReasoningStartEvent:
		return map[string]any{"type": e.StreamEventType()}, nil
	case ReasoningDeltaEvent:
		return map[string]any{
			"type":    e.StreamEventType(),
			"content": e.Content,
		}, nil
	case FinishEvent:
		blocks := make([]any, 0, len(e.ThinkingBlocks))
		for _, block := range e.ThinkingBlocks {
			blocks = append(blocks, block)
		}
		var credits any
		if e.Credits != nil {
			credits = *e.Credits
		}
		return map[string]any{
			"type":                  e.StreamEventType(),
			"stop_reason":           e.StopReason,
			"input_tokens":          e.InputTokens,
			"output_tokens":         e.OutputTokens,
			"cached_tokens":         e.CachedTokens,
			"cache_creation_tokens": e.CacheCreationTokens,
			"cost_usd":              e.CostUSD,
			"credits":               credits,
			"model":                 e.Model,
			"thinking_blocks":       blocks,
			"reasoning_content":     e.ReasoningContent,
		}, nil
	case StreamErrorEvent:
		var errorType any
		if e.ErrorType != nil {
			errorType = *e.ErrorType
		}
		var upstreamStatus any
		if e.UpstreamStatus != nil {
			upstreamStatus = *e.UpstreamStatus
		}
		return map[string]any{
			"type":            e.StreamEventType(),
			"error":           e.Error,
			"recoverable":     e.Recoverable,
			"error_type":      errorType,
			"upstream_status": upstreamStatus,
		}, nil
	}
	return nil, fmt.Errorf("unknown stream event %T", event)
}

func AllStreamEventTypes() []string {
	return []string{
		"text_delta",
		"text_end",
		"tool_call",
		"tool_result",
		"reasoning_start",
		"reasoning_delta",
		"finish",
		"error",
	}
}

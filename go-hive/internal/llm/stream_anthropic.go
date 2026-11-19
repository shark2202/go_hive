package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Anthropic Messages streaming. The wire format is server-sent events
// (`message_start`, `content_block_delta`, `message_delta`, `message_stop`)
// rather than the OpenAI chunk shape, so it cannot share the OpenAI parser.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
const anthropicStreamLabel = "anthropic stream"

var errEmptyAnthropicStream = errors.New("llm: anthropic stream returned no content")

// anthropicRequestHeaders builds the auth and version headers shared by the
// streaming and non-streaming Anthropic transports, so both authenticate the
// same way against Anthropic and its compatible proxies.
//
// Parity: core/framework/llm/litellm.py::_api_base_needs_bearer_auth
func anthropicRequestHeaders(p *Provider, kwargs map[string]any, apiBase, apiKey string) map[string]string {
	headers := extraHeadersFromKwargs(kwargs)
	headers["anthropic-version"] = AnthropicVersion
	if apiKey == "" {
		return headers
	}
	oauth := strings.HasPrefix(apiKey, anthropicOAuthPrefix)
	bearer := oauth || p.HiveProxyAuth || apiBaseNeedsBearerAuth(apiBase)
	if bearer {
		if _, ok := headers["Authorization"]; !ok {
			headers["Authorization"] = "Bearer " + apiKey
		}
	} else if _, ok := headers["x-api-key"]; !ok {
		headers["x-api-key"] = apiKey
	}
	return headers
}

// anthropicMessagesStream streams a Messages API completion. It emits each
// text_delta as a TextDeltaEvent as the upstream produces it, assembles
// tool_use blocks from input_json_delta fragments, and returns the same
// Completion shape as the non-streaming transport.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
func (p *Provider) anthropicMessagesStream(
	ctx context.Context,
	original CompletionRequest,
	kwargs map[string]any,
	model, apiBase, apiKey string,
	emit func(StreamEvent),
) (Completion, error) {
	body, err := anthropicRequestBody(kwargs, model)
	if err != nil {
		return Completion{}, err
	}
	body["stream"] = true

	encoded, err := json.Marshal(body)
	if err != nil {
		return Completion{}, fmt.Errorf("llm: encode %s request: %w", anthropicStreamLabel, err)
	}

	endpoint := anthropicMessagesEndpoint(resolveCompletionAPIBase(model, apiBase))
	headers := anthropicRequestHeaders(p, kwargs, apiBase, apiKey)

	attempts := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Completion{}, ctxErr
		}
		if err := p.paceRequest(ctx); err != nil {
			return Completion{}, err
		}
		response, err := p.doStreamRequest(ctx, anthropicStreamLabel, endpoint, headers, encoded)
		if err != nil {
			return Completion{}, err
		}

		if response.StatusCode < 200 || response.StatusCode >= 300 {
			detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			providerErr := newProviderHTTPError(anthropicStreamLabel, response, detail)
			if response.StatusCode == http.StatusTooManyRequests && attempts < RateLimitMaxRetries {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Completion{}, ctxErr
				}
				p.sleepFor(ComputeRetryDelay(attempts, providerErr, RetryPolicy{Jitter: true}))
				attempts++
				continue
			}
			if streamingFallbackStatus(response.StatusCode) {
				return p.streamingNonStreamFallback(ctx, original, emit, providerErr)
			}
			return Completion{}, providerErr
		}

		result, err := p.consumeAnthropicStreamResponse(ctx, response, model, emit)
		if errors.Is(err, errEmptyAnthropicStream) {
			if attempts >= EmptyResponseMaxRetries {
				return result.completion, nil
			}
			if ctxErr := p.sleepStreamRetry(ctx); ctxErr != nil {
				return Completion{}, ctxErr
			}
			attempts++
			continue
		}
		if err != nil {
			emit(StreamErrorEvent{Error: err.Error()})
			return Completion{}, err
		}
		emitStreamTail(result, emit)
		return result.completion, nil
	}
}

// consumeAnthropicStreamResponse accepts either a real SSE body or a plain
// Messages JSON body from a proxy that ignored `stream:true`.
func (p *Provider) consumeAnthropicStreamResponse(ctx context.Context, response *http.Response, requestedModel string, emit func(StreamEvent)) (streamResult, error) {
	defer response.Body.Close()

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return p.parseAnthropicStream(ctx, response.Body, requestedModel, emit)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCompletionBodyBytes))
	if err != nil {
		return streamResult{}, fmt.Errorf("llm: read %s response: %w", anthropicStreamLabel, err)
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("data:")) {
		return p.parseAnthropicStream(ctx, bytes.NewReader(raw), requestedModel, emit)
	}

	var payload anthropicMessagePayload
	if err := json.Unmarshal(raw, &payload); err != nil || (len(payload.Content) == 0 && payload.StopReason == "") {
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return streamResult{}, fmt.Errorf(
			"llm: %s response is not server-sent events (content-type %q, body %q)",
			anthropicStreamLabel, response.Header.Get("Content-Type"), detail)
	}
	completion := p.completionFromResponse(payload.toCompletionResponse(requestedModel))
	result := streamResult{completion: completion}
	// The proxy ignored streaming: forward the whole answer as one honest
	// delta instead of slicing it into fake chunks.
	if completion.Content != "" {
		emit(TextDeltaEvent{Content: completion.Content, Snapshot: completion.Content})
	}
	result.toolInputs = make([]map[string]any, 0, len(completion.ToolCalls))
	for _, call := range completion.ToolCalls {
		input, parseErr := ParseToolCallArguments(call.Arguments, call.Name)
		if parseErr != nil {
			input = map[string]any{}
		}
		result.toolInputs = append(result.toolInputs, input)
	}
	return result, nil
}

// anthropicStreamEvent is one decoded SSE `data:` frame.
type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   *int   `json:"index"`
	Message *struct {
		Model string                 `json:"model"`
		Usage *anthropicUsagePayload `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsagePayload `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicStreamBlock accumulates one content block across deltas.
type anthropicStreamBlock struct {
	kind      string
	id        string
	name      string
	arguments strings.Builder
	initial   string
}

// anthropicStreamState is the accumulator a single SSE read fills.
type anthropicStreamState struct {
	text          strings.Builder
	model         string
	stopReason    string
	inputTokens   int
	outputTokens  int
	cacheRead     int
	cacheCreation int
	blocks        map[int]*anthropicStreamBlock
	hasToolUse    bool
}

func newAnthropicStreamState(requestedModel string) *anthropicStreamState {
	return &anthropicStreamState{
		model:  requestedModel,
		blocks: map[int]*anthropicStreamBlock{},
	}
}

// apply folds one event into the stream state, emitting text as it arrives.
func (s *anthropicStreamState) apply(event *anthropicStreamEvent, emit func(StreamEvent)) error {
	switch event.Type {
	case "message_start":
		if event.Message == nil {
			return nil
		}
		if event.Message.Model != "" {
			s.model = event.Message.Model
		}
		if usage := event.Message.Usage; usage != nil {
			s.inputTokens = usage.InputTokens
			s.cacheRead = usage.CacheReadInputTokens
			s.cacheCreation = usage.CacheCreationInputTokens
			if usage.OutputTokens > 0 {
				s.outputTokens = usage.OutputTokens
			}
		}
	case "content_block_start":
		index := eventIndex(event.Index)
		block := &anthropicStreamBlock{}
		if event.ContentBlock != nil {
			block.kind = event.ContentBlock.Type
			block.id = event.ContentBlock.ID
			block.name = event.ContentBlock.Name
			block.initial = strings.TrimSpace(string(event.ContentBlock.Input))
			if event.ContentBlock.Type == "tool_use" {
				s.hasToolUse = true
			}
			if event.ContentBlock.Type == "text" && event.ContentBlock.Text != "" {
				s.text.WriteString(event.ContentBlock.Text)
				emit(TextDeltaEvent{Content: event.ContentBlock.Text, Snapshot: s.text.String()})
			}
		}
		s.blocks[index] = block
	case "content_block_delta":
		index := eventIndex(event.Index)
		block := s.blocks[index]
		if block == nil {
			block = &anthropicStreamBlock{kind: "text"}
			s.blocks[index] = block
		}
		if event.Delta == nil {
			return nil
		}
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				s.text.WriteString(event.Delta.Text)
				emit(TextDeltaEvent{Content: event.Delta.Text, Snapshot: s.text.String()})
			}
		case "input_json_delta":
			block.arguments.WriteString(event.Delta.PartialJSON)
		case "thinking_delta", "signature_delta":
			// Extended thinking is not part of the answer text; the
			// non-streaming transport drops it too.
		}
	case "content_block_stop":
		// Blocks are finalised after the stream ends; nothing to do.
	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			s.stopReason = anthropicStreamStopReason(event.Delta.StopReason, s.hasToolUse)
		}
		if event.Usage != nil {
			s.outputTokens = event.Usage.OutputTokens
			// Some proxies only report prompt tokens here.
			if event.Usage.InputTokens > 0 {
				s.inputTokens = event.Usage.InputTokens
				s.cacheRead = event.Usage.CacheReadInputTokens
				s.cacheCreation = event.Usage.CacheCreationInputTokens
			}
		}
	case "message_stop":
		// Terminal frame; the scanner ends right after it.
	case "error":
		message := "upstream stream error"
		if event.Error != nil && event.Error.Message != "" {
			message = event.Error.Message
		}
		return fmt.Errorf("llm: %s error: %s", anthropicStreamLabel, message)
	case "ping":
		// Keep-alive.
	default:
		// Unknown event types are forward-compatible noise.
	}
	return nil
}

// completion assembles the final Completion from accumulated blocks.
func (s *anthropicStreamState) completion() Completion {
	completion := Completion{
		Content:    s.text.String(),
		Model:      s.model,
		StopReason: s.stopReason,
	}
	usage := &Usage{
		PromptTokens:             s.inputTokens + s.cacheRead + s.cacheCreation,
		CompletionTokens:         s.outputTokens,
		CacheReadInputTokens:     s.cacheRead,
		CacheCreationInputTokens: s.cacheCreation,
	}
	if s.cacheRead > 0 || s.cacheCreation > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{
			CachedTokens:     s.cacheRead,
			CacheWriteTokens: s.cacheCreation,
		}
	}
	completion.InputTokens = usage.PromptTokens
	completion.OutputTokens = usage.CompletionTokens
	completion.CachedTokens = s.cacheRead
	completion.CacheCreationTokens = s.cacheCreation
	completion.CostUSD = ExtractCost(usage, nil, s.model)
	return completion
}

// parseAnthropicStream reads Messages SSE frames. `event:` lines are
// descriptive only; the JSON `type` field is authoritative. Comments
// (keep-alives) and `ping` frames are ignored.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
func (p *Provider) parseAnthropicStream(ctx context.Context, reader io.Reader, requestedModel string, emit func(StreamEvent)) (streamResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEEventBytes)
	state := newAnthropicStreamState(requestedModel)
	result := streamResult{}

	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		trimmed := strings.TrimSpace(payload)
		if trimmed == "" || trimmed == "[DONE]" {
			return nil
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("llm: decode %s chunk: %w", anthropicStreamLabel, err)
		}
		return state.apply(&event, emit)
	}

	for scanner.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return result, err
			}
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			dataLines = append(dataLines, payload)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("llm: read %s response: %w", anthropicStreamLabel, err)
	}
	if err := flush(); err != nil {
		return result, err
	}

	completion := state.completion()
	indices := make([]int, 0, len(state.blocks))
	for index := range state.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		block := state.blocks[index]
		if block.kind != "tool_use" || block.name == "" {
			continue
		}
		arguments := strings.TrimSpace(block.arguments.String())
		if arguments == "" {
			if block.initial != "" && block.initial != "null" && block.initial != "{}" {
				arguments = block.initial
			} else {
				arguments = "{}"
			}
		}
		completion.ToolCalls = append(completion.ToolCalls, ToolCall{
			ID:        block.id,
			Name:      block.name,
			Arguments: arguments,
		})
		input, err := ParseToolCallArguments(arguments, block.name)
		if err != nil {
			input = map[string]any{}
		}
		result.toolInputs = append(result.toolInputs, input)
	}

	result.completion = completion
	if result.empty() {
		return result, errEmptyAnthropicStream
	}
	return result, nil
}

func eventIndex(index *int) int {
	if index == nil {
		return 0
	}
	return *index
}

// anthropicStreamStopReason maps an Anthropic stop_reason to the runtime's
// vocabulary, repairing a paused turn the same way the OpenAI stream does.
//
// Parity: litellm.litellm_core_utils.core_helpers.map_finish_reason
func anthropicStreamStopReason(raw string, hasToolCalls bool) string {
	if raw == "pause_turn" {
		return normalizeStreamFinishReason(raw, hasToolCalls)
	}
	return anthropicFinishReason(raw)
}

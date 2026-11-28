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

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
// (OpenAI-compatible stream=True + stream_options include_usage)
const (
	openAIStreamLabel = "openai stream"

	// maxSSEEventBytes bounds one buffered SSE event (a full tool-call
	// argument object can be large, but a single frame should never be MBs).
	maxSSEEventBytes = 8 << 20

	// streamEmptyRetryDelay mirrors the fixed short delay upstream uses for
	// empty streams instead of the rate-limit backoff.
	streamEmptyRetryDelaySeconds = 1.0
)

var errEmptyOpenAIStream = errors.New("llm: openai stream returned no content")

// ACompleteStream streams an OpenAI-compatible chat completion. It emits text
// deltas as they arrive, aggregates tool-call fragments by index (reattaching
// argument deltas that arrive without an id), and returns the assembled
// Completion at the end.
//
// Non-streaming fallbacks: a gateway that rejects the stream parameter, or one
// that ignores `stream:true` and answers with a plain completion JSON, is
// served through the non-streaming transport and surfaces its whole text as a
// single delta. An Anthropic Messages or MiniMax model returns
// ErrStreamingUnsupported so the caller can fall back knowingly.
func (p *Provider) ACompleteStream(ctx context.Context, req CompletionRequest, emit func(StreamEvent)) (Completion, error) {
	if p == nil {
		return Completion{}, errors.New("llm: provider is not initialised")
	}
	if emit == nil {
		emit = func(StreamEvent) {}
	}

	kwargs := p.completionRequestKwargs(req)
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
	if p.KeyPool != nil {
		if pooled := p.KeyPool.GetKey(); pooled != "" {
			apiKey = pooled
		}
	}
	// Codex ChatGPT subscriptions only expose the Responses API. The bridge
	// emits the same StreamEvents as the chat-completions transport so the
	// agent loop does not need a second code path.
	//
	// Parity: core/framework/llm/litellm.py::LiteLLMProvider._codex_backend
	if isCodexResponsesBase(resolveCompletionAPIBase(model, apiBase)) {
		attempts := 0
		for {
			result, err := p.codexResponsesStream(ctx, kwargs, model, apiBase, apiKey, emit)
			if err != nil {
				p.noteKeyOutcome(apiKey, err)
				return Completion{}, err
			}
			if result.empty() && attempts < EmptyResponseMaxRetries {
				if ctxErr := p.sleepStreamRetry(ctx); ctxErr != nil {
					return Completion{}, ctxErr
				}
				attempts++
				continue
			}
			p.noteKeyOutcome(apiKey, nil)
			emitStreamTail(result, emit)
			return result.completion, nil
		}
	}
	// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream — Anthropic
	// (and the Anthropic-compatible kimi/hive proxies) speaks the Messages SSE
	// protocol, which has its own parser. MiniMax chunks are known to break
	// the upstream stream parser, so it stays on the non-streaming path there.
	if usesAnthropicMessagesAPI(model) {
		completion, err := p.anthropicMessagesStream(ctx, req, kwargs, model, apiBase, apiKey, emit)
		p.noteKeyOutcome(apiKey, err)
		return completion, err
	}
	if IsMinimaxModel(model) {
		return Completion{}, fmt.Errorf("%w: minimax uses the non-streaming transport (%s)", ErrStreamingUnsupported, model)
	}

	completion, err := p.openAIChatCompletionStream(ctx, req, kwargs, model, apiBase, apiKey, emit)
	p.noteKeyOutcome(apiKey, err)
	return completion, err
}

func (p *Provider) openAIChatCompletionStream(
	ctx context.Context,
	original CompletionRequest,
	kwargs map[string]any,
	model, apiBase, apiKey string,
	emit func(StreamEvent),
) (Completion, error) {
	// A model that previously rejected native tool use skips the probe: the
	// compatibility prompt is used directly until the cache entry expires.
	//
	// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
	// (_is_openrouter_tool_compat_cached)
	if len(original.Tools) > 0 && isOpenRouterModel(model, apiBase) && isOpenRouterToolCompatCached(model) {
		return p.openRouterToolCompatCompletion(ctx, original, model, apiBase, apiKey, emit, nil)
	}

	body := openAIRequestBody(kwargs, model)
	body["stream"] = true
	// Anthropic is routed elsewhere; every remaining OpenAI-compatible
	// provider tolerates the OpenAI usage field, and it is the only way to
	// report tokens for a streamed turn.
	body["stream_options"] = map[string]any{"include_usage": true}
	// Ask for parallel tool calls so one response can batch several calls
	// (the reference does this on every non-Anthropic streaming request with
	// tools). A provider that rejects the parameter latches it off below and
	// the request is retried without it.
	//
	// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
	if _, hasTools := body["tools"]; hasTools && !IsAnthropicModel(model) && p.RequestParallelToolCalls(true) {
		if _, ok := body["parallel_tool_calls"]; !ok {
			body["parallel_tool_calls"] = true
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return Completion{}, fmt.Errorf("llm: encode %s request: %w", openAIStreamLabel, err)
	}

	endpoint := openAIChatEndpoint(resolveCompletionAPIBase(model, apiBase))
	headers := extraHeadersFromKwargs(kwargs)
	if apiKey != "" {
		if _, ok := headers["Authorization"]; !ok {
			headers["Authorization"] = "Bearer " + apiKey
		}
	}

	attempts := 0
	transientAttempts := 0
	tokenRefreshed := false
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Completion{}, ctxErr
		}
		if err := p.paceRequest(ctx); err != nil {
			return Completion{}, err
		}
		response, err := p.doStreamRequest(ctx, openAIStreamLabel, endpoint, headers, encoded)
		if err != nil {
			// Network blips and timeouts are retried a bounded number of
			// times, exactly like upstream's transient-stream set.
			//
			// Parity: core/framework/llm/litellm.py::_is_stream_transient_error
			if isStreamTransientError(err) && transientAttempts < StreamTransientMaxRetries {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Completion{}, ctxErr
				}
				p.sleepFor(ComputeRetryDelay(transientAttempts, err, RetryPolicy{Jitter: true}))
				transientAttempts++
				continue
			}
			p.emitStreamError(emit, err)
			return Completion{}, err
		}

		if response.StatusCode < 200 || response.StatusCode >= 300 {
			detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			providerErr := newProviderHTTPError(openAIStreamLabel, response, detail)
			// A gateway that rejects the parallel_tool_calls parameter is
			// retried once without it (the latch keeps later requests clean).
			//
			// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
			if p.NoteError(providerErr) {
				delete(body, "parallel_tool_calls")
				encoded, err = json.Marshal(body)
				if err != nil {
					return Completion{}, fmt.Errorf("llm: encode %s request: %w", openAIStreamLabel, err)
				}
				continue
			}
			if response.StatusCode == http.StatusTooManyRequests && attempts < RateLimitMaxRetries {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Completion{}, ctxErr
				}
				p.sleepFor(ComputeRetryDelay(attempts, providerErr, RetryPolicy{Jitter: true}))
				attempts++
				continue
			}
			// Gateway/server unavailability is transient: retry before
			// surfacing a recoverable terminal error.
			if isStreamTransientStatus(response.StatusCode) && transientAttempts < StreamTransientMaxRetries {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Completion{}, ctxErr
				}
				p.sleepFor(ComputeRetryDelay(transientAttempts, providerErr, RetryPolicy{Jitter: true}))
				transientAttempts++
				continue
			}
			// A rejected hive streamToken is refreshed once and the request
			// replayed with the new credential — the defensive path for clock
			// skew or revocation that beat the rotation loop.
			//
			// Parity: core/framework/llm/litellm.py (hive_stream_token_invalid
			// -> try_refresh_now -> retry the same call once)
			if refreshed, ok := retryAfterStreamTokenRefresh(ctx, providerErr, &tokenRefreshed); ok {
				headers["Authorization"] = "Bearer " + refreshed
				continue
			}
			// OpenRouter can reject native tool use for the routed model; the
			// compatibility path emulates tools through a JSON prompt and is
			// cached so later turns skip the probe.
			//
			// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
			// (_should_use_openrouter_tool_compat)
			if len(original.Tools) > 0 && shouldUseOpenRouterToolCompat(model, apiBase, providerErr) {
				rememberOpenRouterToolCompatModel(model)
				return p.openRouterToolCompatCompletion(ctx, original, model, apiBase, apiKey, emit, providerErr)
			}
			if streamingFallbackStatus(response.StatusCode) {
				return p.streamingNonStreamFallback(ctx, original, emit, providerErr)
			}
			// Terminal non-2xx: classify before failing so the SSE consumer
			// sees error_type/upstream_status (402 -> payment_required).
			//
			// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
			p.emitStreamError(emit, providerErr)
			return Completion{}, providerErr
		}

		// Track whether this attempt surfaced anything: a mid-stream
		// transient failure may only be retried when nothing was emitted,
		// otherwise the consumer would see duplicated deltas.
		emitted := 0
		attemptEmit := func(event StreamEvent) {
			emitted++
			emit(event)
		}
		result, err := p.consumeOpenAIStreamResponse(ctx, response, model, attemptEmit)
		if errors.Is(err, errEmptyOpenAIStream) {
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
			if emitted == 0 && isStreamTransientError(err) && transientAttempts < StreamTransientMaxRetries {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Completion{}, ctxErr
				}
				p.sleepFor(ComputeRetryDelay(transientAttempts, err, RetryPolicy{Jitter: true}))
				transientAttempts++
				continue
			}
			p.emitStreamError(emit, err)
			return Completion{}, err
		}
		emitStreamTail(result, emit)
		return result.completion, nil
	}
}

// doStreamRequest sends a streaming request. The timeout context is released
// when the caller closes the body, so a long generation is bounded by
// DefaultHTTPTimeout rather than by the time to first byte.
func (p *Provider) doStreamRequest(ctx context.Context, label, endpoint string, headers map[string]string, encoded []byte) (*http.Response, error) {
	requestCtx, cancel := context.WithTimeout(ctx, DefaultHTTPTimeout)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("llm: build %s request: %w", label, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := httpCompletionClient.Do(request)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("llm: %s request failed: %w", label, err)
	}
	response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

// cancelOnClose ties a request's timeout context to the response body lifetime.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// streamingFallbackStatus reports whether an upstream status means "this
// endpoint cannot stream" rather than a real request/auth failure. Those are
// retried once without the stream parameter.
func streamingFallbackStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed,
		http.StatusNotAcceptable, http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return true
	}
	return false
}

func (p *Provider) sleepStreamRetry(ctx context.Context) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	p.sleepFor(streamEmptyRetryDelaySeconds)
	return ctx.Err()
}

// streamingNonStreamFallback serves a provider that rejected streaming through
// the ordinary completion transport. The whole answer is published as one
// delta (not sliced into fake chunks), followed by tool calls, text end and
// finish, so a streaming consumer sees the same terminal sequence.
func (p *Provider) streamingNonStreamFallback(ctx context.Context, req CompletionRequest, emit func(StreamEvent), cause error) (Completion, error) {
	completion, err := p.AComplete(ctx, req)
	if err != nil {
		// The stream status was rejected and the non-streaming fallback also
		// failed: classify the terminal response before the turn fails.
		p.emitStreamError(emit, err)
		return Completion{}, fmt.Errorf("llm: streaming request rejected (%v); non-streaming fallback failed: %w", cause, err)
	}
	if completion.Content != "" {
		emit(TextDeltaEvent{Content: completion.Content, Snapshot: completion.Content})
	}
	toolInputs := make([]map[string]any, len(completion.ToolCalls))
	for i, call := range completion.ToolCalls {
		input, parseErr := ParseToolCallArguments(call.Arguments, call.Name)
		if parseErr != nil {
			input = map[string]any{}
		}
		toolInputs[i] = input
		emit(ToolCallEvent{ToolUseID: call.ID, ToolName: call.Name, ToolInput: input})
	}
	if completion.Content != "" {
		emit(TextEndEvent{FullText: completion.Content})
	}
	stopReason := completion.StopReason
	if stopReason == "" {
		// The non-streaming fallback always publishes a terminal stop, like
		// upstream's `response.stop_reason or "stop"`.
		stopReason = "stop"
	}
	emit(FinishEvent{
		StopReason:          stopReason,
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

// streamResult is the parsed stream: the assembled completion plus the
// per-call tool inputs (needed by ToolCallEvent).
type streamResult struct {
	completion Completion
	toolInputs []map[string]any
	// suppressFinish is set when the stream ended without a finish_reason.
	// Upstream emits no FinishEvent for a disconnected stream; synthesizing
	// Finish(stop="") makes a truncated turn look complete.
	//
	// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
	// (FinishEvent is only appended inside `if choice.finish_reason`)
	suppressFinish bool
}

// empty reports a stream that produced neither text, tool calls nor a
// non-length stop reason — the shape upstream retries.
func (r streamResult) empty() bool {
	return r.completion.Content == "" &&
		len(r.completion.ToolCalls) == 0 &&
		r.completion.StopReason != "length"
}

func emitStreamTail(result streamResult, emit func(StreamEvent)) {
	if result.suppressFinish {
		// A stream that never reported a finish_reason was cut off: upstream
		// produces no tail events for it, and a synthetic stop would mark the
		// turn complete.
		//
		// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
		return
	}
	for i, call := range result.completion.ToolCalls {
		input := map[string]any{}
		if i < len(result.toolInputs) && result.toolInputs[i] != nil {
			input = result.toolInputs[i]
		}
		emit(ToolCallEvent{ToolUseID: call.ID, ToolName: call.Name, ToolInput: input})
	}
	if result.completion.Content != "" {
		emit(TextEndEvent{FullText: result.completion.Content})
	}
	emit(FinishEvent{
		StopReason:          result.completion.StopReason,
		InputTokens:         result.completion.InputTokens,
		OutputTokens:        result.completion.OutputTokens,
		CachedTokens:        result.completion.CachedTokens,
		CacheCreationTokens: result.completion.CacheCreationTokens,
		CostUSD:             result.completion.CostUSD,
		Credits:             result.completion.Credits,
		Model:               result.completion.Model,
		ThinkingBlocks:      result.completion.ThinkingBlocks,
		ReasoningContent:    result.completion.ReasoningContent,
	})
}

// consumeOpenAIStreamResponse accepts either a real SSE body or a plain
// completion JSON from a gateway that ignored `stream:true`.
func (p *Provider) consumeOpenAIStreamResponse(ctx context.Context, response *http.Response, requestedModel string, emit func(StreamEvent)) (streamResult, error) {
	defer response.Body.Close()

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return p.parseOpenAIStream(ctx, response.Body, requestedModel, emit)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCompletionBodyBytes))
	if err != nil {
		return streamResult{}, fmt.Errorf("llm: read %s response: %w", openAIStreamLabel, err)
	}
	// Some proxies label the SSE body application/octet-stream; trust the
	// payload shape over the header.
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("data:")) {
		return p.parseOpenAIStream(ctx, bytes.NewReader(raw), requestedModel, emit)
	}

	var payload openAICompletionPayload
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Choices) == 0 {
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return streamResult{}, fmt.Errorf(
			"llm: %s response is not server-sent events (content-type %q, body %q)",
			openAIStreamLabel, response.Header.Get("Content-Type"), detail)
	}
	completion := p.completionFromResponse(payload.toCompletionResponse(requestedModel))
	result := streamResult{completion: completion}
	// The gateway ignored streaming: there are no increments to forward, so the
	// whole answer is one honest delta.
	if completion.Content != "" {
		emit(TextDeltaEvent{Content: completion.Content, Snapshot: completion.Content})
	}
	toolInputs := make([]map[string]any, len(completion.ToolCalls))
	for i, call := range completion.ToolCalls {
		input, parseErr := ParseToolCallArguments(call.Arguments, call.Name)
		if parseErr != nil {
			input = map[string]any{}
		}
		toolInputs[i] = input
	}
	result.toolInputs = toolInputs
	return result, nil
}

// parseOpenAIStream reads SSE frames. Only `data:` fields are meaningful for
// the OpenAI wire format; comments (keep-alives), `event:` and `id:` are
// ignored. Multi-line data fields are joined as the SSE spec requires.
func (p *Provider) parseOpenAIStream(ctx context.Context, reader io.Reader, requestedModel string, emit func(StreamEvent)) (streamResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEEventBytes)

	var (
		dataLines   []string
		text        strings.Builder
		reasoning   strings.Builder
		model       = requestedModel
		stopReason  string
		finishSeen  bool
		usage       *Usage
		tools       = map[int]*openAIStreamToolAccumulator{}
		lastToolIdx = -1
		result      = streamResult{}
	)

	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if trimmed := strings.TrimSpace(payload); trimmed == "" || trimmed == "[DONE]" {
			return nil
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("llm: decode %s chunk: %w", openAIStreamLabel, err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("llm: %s error: %s", openAIStreamLabel, chunk.Error.Message)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			// Cumulative usage rides a trailing choices-less chunk; last wins.
			usage = chunk.Usage.toUsage()
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		if content := decodeMessageContent(choice.Delta.Content); content != "" {
			text.WriteString(content)
			emit(TextDeltaEvent{Content: content, Snapshot: text.String()})
		}
		// Reasoning models (DeepSeek, Qwen, GLM) stream their thinking as the
		// OpenAI-compatible `delta.reasoning_content` string. Surface it so
		// the UI gets reasoning_delta events and the agent loop can echo it
		// back on this turn's later steps, exactly like the reference.
		//
		// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
		if delta := choice.Delta.ReasoningContent; delta != "" {
			reasoning.WriteString(delta)
			emit(ReasoningDeltaEvent{Content: delta})
		}
		for _, call := range choice.Delta.ToolCalls {
			idx := 0
			if call.Index != nil {
				idx = *call.Index
			}
			// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream —
			// some bridges hardcode index=0 for parallel calls, so a new id
			// opens a new slot and argument fragments without an id attach to
			// the last opened one.
			if call.ID != "" {
				if existing, ok := streamToolIndexByID(tools, call.ID); ok {
					idx = existing
				} else if slot, ok := tools[idx]; ok && slot.id != "" && slot.id != call.ID {
					idx = streamMaxToolIndex(tools) + 1
				}
				lastToolIdx = idx
			} else if lastToolIdx >= 0 {
				idx = lastToolIdx
			}
			slot := tools[idx]
			if slot == nil {
				slot = &openAIStreamToolAccumulator{}
				tools[idx] = slot
			}
			if call.ID != "" {
				slot.id = call.ID
			}
			if call.Function.Name != "" {
				slot.name = call.Function.Name
			}
			if call.Function.Arguments != "" {
				slot.arguments.WriteString(call.Function.Arguments)
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			stopReason = normalizeStreamFinishReason(*choice.FinishReason, len(tools) > 0)
			finishSeen = true
		}
		return nil
	}

scan:
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
			// The [DONE] sentinel terminates the stream by SSE contract. Do
			// not wait for the transport to close: OpenAI-compatible proxies
			// (and simple local stubs) keep the connection alive after it,
			// and the Python client stops reading here too.
			if strings.TrimSpace(payload) == "[DONE]" {
				break scan
			}
			dataLines = append(dataLines, payload)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("llm: read %s response: %w", openAIStreamLabel, err)
	}
	// A stream that closes without the final blank line still carries one last
	// event; process it rather than dropping the answer.
	if err := flush(); err != nil {
		return result, err
	}

	// Gemini (and similar models) sometimes emit tool calls as
	// `<tool_code>{"name": {...args}}</tool_code>` text instead of structured
	// function calls. Extract them as real tool calls and strip the blocks
	// from the visible answer.
	//
	// Parity: core/framework/llm/litellm.py::_extract_text_tool_calls
	content := text.String()
	extracted, cleaned := extractTextToolCalls(content)
	if len(extracted) > 0 {
		content = cleaned
		// Overwrite the consumer's accumulated snapshot with the cleaned text.
		emit(TextDeltaEvent{Content: "", Snapshot: cleaned})
	}

	completion := Completion{
		Content:          content,
		Model:            model,
		StopReason:       stopReason,
		ReasoningContent: reasoning.String(),
	}
	if usage != nil {
		completion.InputTokens = usage.PromptTokens
		completion.OutputTokens = usage.CompletionTokens
		completion.CostUSD = ExtractCost(usage, nil, model)
		completion.CachedTokens, completion.CacheCreationTokens = ExtractCacheTokens(usage)
		completion.Credits = usage.Credits
	}
	indices := make([]int, 0, len(tools))
	for idx := range tools {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		slot := tools[idx]
		arguments := strings.TrimSpace(slot.arguments.String())
		completion.ToolCalls = append(completion.ToolCalls, ToolCall{
			ID:        slot.id,
			Name:      slot.name,
			Arguments: arguments,
		})
		input, err := ParseToolCallArguments(arguments, slot.name)
		if err != nil {
			input = map[string]any{}
		}
		result.toolInputs = append(result.toolInputs, input)
	}
	for _, call := range extracted {
		completion.ToolCalls = append(completion.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
		result.toolInputs = append(result.toolInputs, call.Input)
	}

	result.completion = completion
	// A stream that closes without any finish_reason was truncated; upstream
	// yields no FinishEvent for it.
	result.suppressFinish = !finishSeen
	if result.empty() {
		return result, errEmptyOpenAIStream
	}
	return result, nil
}

// normalizeStreamFinishReason mirrors the upstream pause_turn repair: a paused
// turn with accumulated tool calls is a tool-calling stop.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
func normalizeStreamFinishReason(reason string, hasToolCalls bool) string {
	if reason == "pause_turn" {
		if hasToolCalls {
			return "tool_calls"
		}
		return "stop"
	}
	return reason
}

type openAIStreamToolAccumulator struct {
	id        string
	name      string
	arguments strings.Builder
}

func streamToolIndexByID(tools map[int]*openAIStreamToolAccumulator, id string) (int, bool) {
	for idx, slot := range tools {
		if slot.id == id {
			return idx, true
		}
	}
	return 0, false
}

func streamMaxToolIndex(tools map[int]*openAIStreamToolAccumulator) int {
	max := -1
	for idx := range tools {
		if idx > max {
			max = idx
		}
	}
	return max
}

// openAIStreamChunk is one `data:` frame of an OpenAI-compatible SSE stream.
type openAIStreamChunk struct {
	ID      string               `json:"id"`
	Model   string               `json:"model"`
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *openAIUsagePayload  `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type openAIStreamChoice struct {
	Index        int               `json:"index"`
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type openAIStreamDelta struct {
	Content          json.RawMessage             `json:"content"`
	ToolCalls        []openAIStreamToolCallDelta `json:"tool_calls"`
	ReasoningContent string                      `json:"reasoning_content"`
}

type openAIStreamToolCallDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

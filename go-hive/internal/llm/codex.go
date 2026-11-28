package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Codex ChatGPT subscriptions speak the OpenAI Responses API, not chat
// completions. Upstream reaches it through LiteLLM's responses_api_bridge;
// this file is the equivalent bridge for the Go transport: convert the
// chat-completions request the agent loop builds into a Responses request and
// fold the Responses SSE events back into the same StreamEvent/Completion
// shapes every other provider produces.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider._codex_backend and
// LiteLLM's completion_extras/litellm_responses_transformation bridge.
const (
	codexStreamLabel = "codex responses"

	// The ChatGPT backend requires instructions and always streams. These are
	// the only request fields the upstream ChatGPT transformation forwards.
	codexDefaultInstructions = "You are a helpful assistant."
)

// isCodexResponsesBase reports whether a base URL is the Codex ChatGPT
// backend. Upstream detects it by the same "chatgpt.com/backend-api/codex"
// substring; the path suffix also matches local test/relay endpoints.
func isCodexResponsesBase(base string) bool {
	base = strings.ToLower(strings.TrimRight(strings.TrimSpace(base), "/"))
	if base == "" {
		return false
	}
	return strings.Contains(base, "chatgpt.com/backend-api/codex") ||
		strings.HasSuffix(base, "/backend-api/codex")
}

// codexResponsesEndpoint appends /responses, tolerating a base that already
// carries the path.
func codexResponsesEndpoint(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	return base + "/responses"
}

// codexResponsesRequestBody converts the OpenAI chat-completions payload into
// a Responses API request.
//
// Parity: LiteLLM completion_extras/litellm_responses_transformation
// (transform_request) and llms/chatgpt/responses/transformation.py
// (store=false, stream=true, include reasoning.encrypted_content).
func codexResponsesRequestBody(kwargs map[string]any, model string) map[string]any {
	messages, _ := asMapSlice(kwargs["messages"])
	input, instructions := codexInputItems(messages)
	if strings.TrimSpace(instructions) == "" {
		instructions = codexDefaultInstructions
	}

	body := map[string]any{
		"model":        wireModelName(model),
		"instructions": instructions,
		"input":        input,
		"stream":       true,
		"store":        false,
		"include":      []any{"reasoning.encrypted_content"},
	}
	if tools := codexTools(kwargs["tools"]); len(tools) > 0 {
		body["tools"] = tools
		if choice := codexToolChoice(kwargs["tool_choice"]); choice != nil {
			body["tool_choice"] = choice
		}
	}
	if value, ok := kwargs["parallel_tool_calls"]; ok && value != nil {
		body["parallel_tool_calls"] = value
	}
	for _, key := range []string{"reasoning", "truncation", "previous_response_id"} {
		if value, ok := kwargs[key]; ok && value != nil {
			body[key] = value
		}
	}
	// extra_body may carry Responses-only knobs (reasoning effort/summary)
	// configured for the Codex slot.
	if extra, ok := kwargs["extra_body"].(map[string]any); ok {
		for _, key := range []string{"reasoning", "truncation", "previous_response_id", "parallel_tool_calls"} {
			if value, ok := extra[key]; ok && value != nil {
				body[key] = value
			}
		}
	}
	return body
}

// codexInputItems converts chat-completions history into Responses input
// items. System/developer messages become instructions; assistant text and
// tool calls become output items; tool results become function_call_output.
func codexInputItems(messages []map[string]any) ([]any, string) {
	input := make([]any, 0, len(messages))
	var instructions []string
	for _, message := range messages {
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer":
			if text := codexMessageText(message["content"]); text != "" {
				instructions = append(instructions, text)
			}
		case "user":
			input = append(input, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": codexContentParts(message["content"], "user"),
			})
		case "assistant":
			// Replay encrypted reasoning items before the turn's text/tool
			// calls: the stateless Responses API (store=false) needs them to
			// continue a reasoning conversation without a 400.
			for _, reasoning := range codexReasoningInputItems(message["thinking_blocks"]) {
				input = append(input, reasoning)
			}
			// Ghost empty assistant turns are dropped upstream: they confuse
			// Codex models and can cause cascading empty streams.
			if text := codexMessageText(message["content"]); text != "" {
				input = append(input, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				})
			}
			for _, call := range codexToolCalls(message["tool_calls"]) {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Name,
					"arguments": call.Arguments,
				})
			}
		case "tool":
			callID, _ := message["tool_call_id"].(string)
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  codexContentParts(message["content"], "user"),
			})
		}
	}
	return input, strings.Join(instructions, " ")
}

// codexContentParts renders a chat message's content as Responses content
// parts. Text-bearing parts use input_text for input roles and output_text for
// assistant history; images/files keep their Responses type.
func codexContentParts(raw any, role string) []any {
	textType := "input_text"
	if role == "assistant" {
		textType = "output_text"
	}
	switch content := raw.(type) {
	case string:
		return []any{map[string]any{"type": textType, "text": content}}
	case []any:
		return codexContentPartList(content, textType)
	case []map[string]any:
		items := make([]any, 0, len(content))
		for _, item := range content {
			items = append(items, item)
		}
		return codexContentPartList(items, textType)
	}
	return []any{map[string]any{"type": textType, "text": ""}}
}

func codexContentPartList(content []any, textType string) []any {
	parts := make([]any, 0, len(content))
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			if text, ok := item.(string); ok {
				parts = append(parts, map[string]any{"type": textType, "text": text})
			}
			continue
		}
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			parts = append(parts, map[string]any{"type": textType, "text": text})
		case "input_text", "output_text":
			parts = append(parts, block)
		case "image_url":
			parts = append(parts, codexImagePart(block["image_url"]))
		case "image":
			converted := map[string]any{"type": "input_image"}
			for key, value := range block {
				if key != "type" {
					converted[key] = value
				}
			}
			parts = append(parts, converted)
		case "file":
			parts = append(parts, codexFilePart(block["file"]))
		default:
			if text := codexMessageText(item); text != "" {
				parts = append(parts, map[string]any{"type": textType, "text": text})
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"type": textType, "text": ""})
	}
	return parts
}

func codexImagePart(raw any) map[string]any {
	part := map[string]any{"type": "input_image"}
	switch image := raw.(type) {
	case string:
		part["image_url"] = image
	case map[string]any:
		if url, ok := image["url"].(string); ok {
			part["image_url"] = url
		}
		if detail, ok := image["detail"].(string); ok && detail != "" {
			part["detail"] = detail
		}
	default:
		part["image_url"] = raw
	}
	return part
}

func codexFilePart(raw any) map[string]any {
	part := map[string]any{"type": "input_file"}
	if file, ok := raw.(map[string]any); ok {
		for _, key := range []string{"file_id", "file_data", "filename"} {
			if value, ok := file[key]; ok {
				part[key] = value
			}
		}
	}
	return part
}

// codexReasoningInputItems projects persisted reasoning blocks back into
// Responses input items. Only blocks that carry encrypted_content are valid
// for stateless replay; Anthropic-style thinking blocks are ignored.
//
// Parity: LiteLLM completion_extras/litellm_responses_transformation
// (_reasoning_item_to_response_input)
func codexReasoningInputItems(raw any) []any {
	blocks, ok := asAnySlice(raw)
	if !ok {
		return nil
	}
	items := make([]any, 0, len(blocks))
	for _, entry := range blocks {
		block, ok := entry.(map[string]any)
		if !ok || block["type"] != "reasoning" {
			continue
		}
		encrypted, _ := block["encrypted_content"].(string)
		if encrypted == "" {
			continue
		}
		item := map[string]any{
			"type":              "reasoning",
			"summary":           codexSummaryInput(block["summary"]),
			"encrypted_content": encrypted,
		}
		if id, _ := block["id"].(string); id != "" {
			item["id"] = id
		} else {
			sum := sha256.Sum256([]byte(encrypted))
			item["id"] = "rs_" + hex.EncodeToString(sum[:])[:12]
		}
		items = append(items, item)
	}
	return items
}

// codexSummaryInput normalizes a persisted summary field to the []any shape
// the request encoder expects. The Responses API requires summary to be
// present even when empty.
func codexSummaryInput(raw any) []any {
	switch summary := raw.(type) {
	case []any:
		return summary
	case []map[string]any:
		items := make([]any, 0, len(summary))
		for _, entry := range summary {
			items = append(items, entry)
		}
		return items
	}
	return []any{}
}

// codexMessageText flattens a message's text blocks without losing order.
func codexMessageText(raw any) string {
	switch content := raw.(type) {
	case string:
		return content
	case []any:
		var text strings.Builder
		for _, item := range content {
			text.WriteString(codexMessageText(item))
		}
		return text.String()
	case []map[string]any:
		var text strings.Builder
		for _, item := range content {
			text.WriteString(codexMessageText(item))
		}
		return text.String()
	case map[string]any:
		if value, ok := content["text"].(string); ok {
			return value
		}
	}
	return ""
}

// codexToolCalls normalizes the three shapes tool_calls can arrive in: the Go
// ChatMessage projection, generic maps from a caller, or []ToolCall.
func codexToolCalls(raw any) []ToolCall {
	switch calls := raw.(type) {
	case []ToolCall:
		return calls
	case []map[string]any:
		items := make([]any, 0, len(calls))
		for _, call := range calls {
			items = append(items, call)
		}
		return codexToolCallList(items)
	case []any:
		return codexToolCallList(calls)
	}
	return nil
}

func codexToolCallList(items []any) []ToolCall {
	calls := make([]ToolCall, 0, len(items))
	for _, item := range items {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := call["id"].(string)
		function, _ := call["function"].(map[string]any)
		name, _ := function["name"].(string)
		arguments := codexArguments(function["arguments"])
		if name == "" {
			continue
		}
		calls = append(calls, ToolCall{ID: id, Name: name, Arguments: arguments})
	}
	return calls
}

// codexTools converts OpenAI function tools into Responses function tools.
// Non-function tools pass through unchanged.
func codexTools(raw any) []any {
	items, ok := asAnySlice(raw)
	if !ok {
		return nil
	}
	tools := make([]any, 0, len(items))
	for _, item := range items {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if tool["type"] != "function" {
			tools = append(tools, tool)
			continue
		}
		function, _ := tool["function"].(map[string]any)
		converted := map[string]any{"type": "function"}
		for _, key := range []string{"name", "description", "parameters", "strict"} {
			if value, ok := function[key]; ok && value != nil {
				converted[key] = value
			}
		}
		tools = append(tools, converted)
	}
	return tools
}

func codexToolChoice(raw any) any {
	switch choice := raw.(type) {
	case string:
		if choice != "" {
			return choice
		}
	case map[string]any:
		if choice["type"] != "function" {
			return choice
		}
		converted := map[string]any{"type": "function"}
		if function, ok := choice["function"].(map[string]any); ok {
			if name, ok := function["name"]; ok {
				converted["name"] = name
			}
		} else if name, ok := choice["name"]; ok {
			converted["name"] = name
		}
		return converted
	}
	return nil
}

// codexResponsesStream sends one Responses request and returns the assembled
// completion. The Responses backend always streams, so the non-streaming
// transport delegates here too (upstream complete() -> acomplete() for Codex).
func (p *Provider) codexResponsesStream(
	ctx context.Context,
	kwargs map[string]any,
	model, apiBase, apiKey string,
	emit func(StreamEvent),
) (streamResult, error) {
	if emit == nil {
		emit = func(StreamEvent) {}
	}
	body := codexResponsesRequestBody(kwargs, model)
	if _, hasTools := body["tools"]; hasTools && p.RequestParallelToolCalls(true) {
		if _, ok := body["parallel_tool_calls"]; !ok {
			body["parallel_tool_calls"] = true
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return streamResult{}, fmt.Errorf("llm: encode %s request: %w", codexStreamLabel, err)
	}

	endpoint := codexResponsesEndpoint(resolveCompletionAPIBase(model, apiBase))
	headers := extraHeadersFromKwargs(kwargs)
	if apiKey != "" {
		if _, ok := headerLookup(headers, "Authorization"); !ok {
			headers["Authorization"] = "Bearer " + apiKey
		}
	}

	attempts := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return streamResult{}, ctxErr
		}
		if err := p.paceRequest(ctx); err != nil {
			return streamResult{}, err
		}
		response, err := p.doStreamRequest(ctx, codexStreamLabel, endpoint, headers, encoded)
		if err != nil {
			return streamResult{}, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			providerErr := newProviderHTTPError(codexStreamLabel, response, detail)
			if response.StatusCode == http.StatusTooManyRequests && attempts < RateLimitMaxRetries {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return streamResult{}, ctxErr
				}
				p.sleepFor(ComputeRetryDelay(attempts, providerErr, RetryPolicy{Jitter: true}))
				attempts++
				continue
			}
			// Codex requires streaming; a non-stream fallback would loop back
			// into this transport, so classify and fail the turn here.
			p.emitStreamError(emit, providerErr)
			return streamResult{}, providerErr
		}
		result, err := p.consumeCodexStreamResponse(ctx, response, model, emit)
		if err != nil {
			p.emitStreamError(emit, err)
			return streamResult{}, err
		}
		return result, nil
	}
}

// consumeCodexStreamResponse accepts SSE or a plain completed-response body
// (some relays answer the streaming request with one JSON object).
func (p *Provider) consumeCodexStreamResponse(ctx context.Context, response *http.Response, requestedModel string, emit func(StreamEvent)) (streamResult, error) {
	defer response.Body.Close()

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return p.parseCodexStream(ctx, response.Body, requestedModel, emit)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCompletionBodyBytes))
	if err != nil {
		return streamResult{}, fmt.Errorf("llm: read %s response: %w", codexStreamLabel, err)
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return p.parseCodexStream(ctx, bytes.NewReader(raw), requestedModel, emit)
	}
	var payload codexResponse
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return streamResult{}, fmt.Errorf("llm: decode %s response: %w", codexStreamLabel, err)
	}
	state := newCodexStreamState(requestedModel)
	state.absorbResponse(&payload)
	if payload.Error != nil && payload.Error.Message != "" {
		return streamResult{}, fmt.Errorf("llm: %s error: %s", codexStreamLabel, payload.Error.Message)
	}
	state.emitMissingDeltas(emit)
	return state.result(), nil
}

func (p *Provider) parseCodexStream(ctx context.Context, reader io.Reader, requestedModel string, emit func(StreamEvent)) (streamResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEEventBytes)
	state := newCodexStreamState(requestedModel)

	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if trimmed := strings.TrimSpace(payload); trimmed == "" || trimmed == "[DONE]" {
			return nil
		}
		var event codexStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("llm: decode %s event: %w", codexStreamLabel, err)
		}
		return state.apply(&event, emit)
	}

scan:
	for scanner.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return streamResult{}, ctxErr
		}
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return streamResult{}, err
			}
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			if strings.TrimSpace(payload) == "[DONE]" {
				break scan
			}
			dataLines = append(dataLines, payload)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return streamResult{}, ctxErr
		}
		return streamResult{}, fmt.Errorf("llm: read %s response: %w", codexStreamLabel, err)
	}
	if err := flush(); err != nil {
		return streamResult{}, err
	}
	state.emitMissingDeltas(emit)
	return state.result(), nil
}

// codexStreamEvent is one Responses SSE frame. Fields not used by an event
// type are simply left zero.
type codexStreamEvent struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta"`
	Text        string          `json:"text"`
	ItemID      string          `json:"item_id"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Arguments   json.RawMessage `json:"arguments"`
	Response    *codexResponse  `json:"response"`
	Error       *codexAPIError  `json:"error"`
	Message     string          `json:"message"`
}

type codexAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type codexResponse struct {
	ID                string            `json:"id"`
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Output            []codexOutputItem `json:"output"`
	Usage             *codexUsage       `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *codexAPIError `json:"error"`
}

type codexOutputItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Role             string          `json:"role"`
	Arguments        json.RawMessage `json:"arguments"`
	Content          []codexContent  `json:"content"`
	Summary          []codexContent  `json:"summary"`
	EncryptedContent string          `json:"encrypted_content"`
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexUsage struct {
	InputTokens        int      `json:"input_tokens"`
	OutputTokens       int      `json:"output_tokens"`
	TotalTokens        int      `json:"total_tokens"`
	Credits            *float64 `json:"credits"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type codexStreamTool struct {
	id        string
	name      string
	arguments strings.Builder
}

type codexStreamState struct {
	model       string
	text        strings.Builder
	reasoning   strings.Builder
	tools       map[string]*codexStreamTool
	order       []string
	byIndex     map[int]string
	lastToolKey string
	usage       *Usage
	status      string
	incomplete  string

	// completed is set once the backend sent a terminal response.completed /
	// response.incomplete frame. A stream that dies before one was truncated,
	// and must not synthesize a Finish.
	completed bool

	// thinking holds replayable reasoning items (encrypted_content included)
	// in output order. They ride the assistant turn's thinking_blocks so the
	// next request can echo them back through the stateless Responses API.
	thinkingOrder []string
	thinkingByKey map[string]map[string]any

	textEmitted      bool
	reasoningEmitted bool
}

func newCodexStreamState(requestedModel string) *codexStreamState {
	return &codexStreamState{
		model:         requestedModel,
		tools:         map[string]*codexStreamTool{},
		byIndex:       map[int]string{},
		thinkingByKey: map[string]map[string]any{},
	}
}

func (s *codexStreamState) apply(event *codexStreamEvent, emit func(StreamEvent)) error {
	switch event.Type {
	case "", "response.created", "response.in_progress", "response.content_part.added",
		"response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		// Lifecycle/no-op frames.
	case "response.output_text.delta":
		if event.Delta != "" {
			s.text.WriteString(event.Delta)
			s.textEmitted = true
			emit(TextDeltaEvent{Content: event.Delta, Snapshot: s.text.String()})
		}
	case "response.output_text.done":
		if s.text.Len() == 0 && event.Text != "" {
			s.text.WriteString(event.Text)
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.reasoning.WriteString(event.Delta)
			s.reasoningEmitted = true
			emit(ReasoningDeltaEvent{Content: event.Delta})
		}
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		if s.reasoning.Len() == 0 && event.Text != "" {
			s.reasoning.WriteString(event.Text)
		}
	case "response.output_item.added":
		s.absorbItem(event.Item, event.OutputIndex)
	case "response.function_call_arguments.delta":
		if tool := s.toolForKey(event.ItemID, event.OutputIndex); tool != nil && event.Delta != "" {
			tool.arguments.WriteString(event.Delta)
		}
	case "response.function_call_arguments.done":
		if tool := s.toolForKey(event.ItemID, event.OutputIndex); tool != nil {
			if arguments := codexArguments(event.Arguments); arguments != "" && tool.arguments.Len() == 0 {
				tool.arguments.WriteString(arguments)
			}
		}
	case "response.output_item.done":
		s.absorbItem(event.Item, event.OutputIndex)
	case "response.completed":
		s.completed = true
		if event.Response != nil {
			s.absorbResponse(event.Response)
		}
	case "response.incomplete":
		s.completed = true
		s.status = "incomplete"
		if event.Response != nil {
			s.absorbResponse(event.Response)
			if event.Response.IncompleteDetails != nil {
				s.incomplete = event.Response.IncompleteDetails.Reason
			}
		}
	case "response.failed":
		message := "request failed"
		if event.Response != nil && event.Response.Error != nil && event.Response.Error.Message != "" {
			message = event.Response.Error.Message
		} else if event.Message != "" {
			message = event.Message
		}
		return fmt.Errorf("llm: %s error: %s", codexStreamLabel, message)
	case "error":
		message := event.Message
		if event.Error != nil && event.Error.Message != "" {
			message = event.Error.Message
		}
		if message == "" {
			message = "upstream stream error"
		}
		return fmt.Errorf("llm: %s error: %s", codexStreamLabel, message)
	}
	return nil
}

// absorbItem folds one output item (from output_item.added/done or a completed
// response) into the state. Added function calls register the accumulator;
// done/completed items carry the full arguments and win over deltas.
func (s *codexStreamState) absorbItem(raw json.RawMessage, outputIndex int) {
	if len(raw) == 0 {
		return
	}
	var item codexOutputItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return
	}
	switch item.Type {
	case "function_call":
		tool := s.ensureTool(&item, outputIndex)
		if item.Name != "" {
			tool.name = item.Name
		}
		if arguments := codexArguments(item.Arguments); arguments != "" {
			tool.arguments.Reset()
			tool.arguments.WriteString(arguments)
		}
	case "message":
		s.absorbMessage(&item)
	case "reasoning":
		s.absorbReasoning(&item)
	}
}

func (s *codexStreamState) ensureTool(item *codexOutputItem, outputIndex int) *codexStreamTool {
	canonical := item.ID
	if canonical == "" {
		canonical = item.CallID
	}
	if canonical == "" {
		canonical = fmt.Sprintf("index-%d", outputIndex)
	}
	// An added event may only carry call_id while the done/completed event
	// carries the item id (or vice versa): follow either alias to the same
	// accumulator instead of creating a duplicate tool call.
	tool := s.tools[canonical]
	if tool == nil && item.CallID != "" {
		tool = s.tools[item.CallID]
	}
	if tool == nil && item.ID != "" {
		tool = s.tools[item.ID]
	}
	if tool == nil {
		tool = &codexStreamTool{}
		s.order = append(s.order, canonical)
	}
	s.tools[canonical] = tool
	if item.ID != "" {
		s.tools[item.ID] = tool
	}
	if item.CallID != "" {
		s.tools[item.CallID] = tool
	}
	if tool.id == "" {
		tool.id = item.CallID
		if tool.id == "" {
			tool.id = item.ID
		}
	}
	if outputIndex >= 0 {
		s.byIndex[outputIndex] = canonical
	}
	s.lastToolKey = canonical
	return tool
}

func (s *codexStreamState) toolForKey(itemID string, outputIndex int) *codexStreamTool {
	if itemID != "" {
		if tool := s.tools[itemID]; tool != nil {
			return tool
		}
	}
	if canonical := s.byIndex[outputIndex]; canonical != "" {
		return s.tools[canonical]
	}
	if s.lastToolKey != "" {
		return s.tools[s.lastToolKey]
	}
	return nil
}

func (s *codexStreamState) absorbMessage(item *codexOutputItem) {
	if s.text.Len() > 0 {
		return
	}
	for _, content := range item.Content {
		if content.Type == "output_text" && content.Text != "" {
			s.text.WriteString(content.Text)
		}
	}
}

func (s *codexStreamState) absorbReasoning(item *codexOutputItem) {
	// The visible reasoning summary feeds reasoning_content deltas; the item
	// itself (encrypted_content included) is kept for replay.
	if s.reasoning.Len() == 0 {
		for _, summary := range item.Summary {
			if summary.Text != "" {
				s.reasoning.WriteString(summary.Text)
			}
		}
	}
	s.captureReasoningItem(item)
}

// captureReasoningItem stores one encrypted reasoning item for replay. Items
// without encrypted_content cannot be sent back through the stateless
// Responses API (store=false), so only the encrypted form is persisted.
//
// Parity: LiteLLM completion_extras/litellm_responses_transformation
// (_build_reasoning_item / _reasoning_item_to_response_input)
func (s *codexStreamState) captureReasoningItem(item *codexOutputItem) {
	if item.EncryptedContent == "" {
		return
	}
	key := item.ID
	if key == "" {
		key = item.EncryptedContent
	}
	block := map[string]any{
		"type":              "reasoning",
		"summary":           codexSummaryList(item.Summary),
		"encrypted_content": item.EncryptedContent,
	}
	if item.ID != "" {
		block["id"] = item.ID
	}
	if _, seen := s.thinkingByKey[key]; !seen {
		s.thinkingOrder = append(s.thinkingOrder, key)
	}
	// Later frames (done/completed) may carry the complete summary; keep the
	// newest copy without duplicating the item.
	s.thinkingByKey[key] = block
}

func codexSummaryList(summary []codexContent) []any {
	blocks := make([]any, 0, len(summary))
	for _, entry := range summary {
		kind := entry.Type
		if kind == "" {
			kind = "summary_text"
		}
		blocks = append(blocks, map[string]any{"type": kind, "text": entry.Text})
	}
	return blocks
}

func (s *codexStreamState) thinkingBlocks() []map[string]any {
	if len(s.thinkingOrder) == 0 {
		return nil
	}
	blocks := make([]map[string]any, 0, len(s.thinkingOrder))
	for _, key := range s.thinkingOrder {
		if block := s.thinkingByKey[key]; block != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func (s *codexStreamState) absorbResponse(response *codexResponse) {
	if response.Model != "" {
		s.model = response.Model
	}
	if response.Status != "" {
		s.status = response.Status
	}
	if response.Usage != nil {
		usage := &Usage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
		}
		if response.Usage.InputTokensDetails != nil && response.Usage.InputTokensDetails.CachedTokens > 0 {
			usage.PromptTokensDetails = &PromptTokensDetails{
				CachedTokens: response.Usage.InputTokensDetails.CachedTokens,
			}
		}
		usage.Credits = response.Usage.Credits
		s.usage = usage
	}
	for index := range response.Output {
		item := &response.Output[index]
		switch item.Type {
		case "function_call":
			tool := s.ensureTool(item, index)
			if item.Name != "" {
				tool.name = item.Name
			}
			if arguments := codexArguments(item.Arguments); arguments != "" {
				tool.arguments.Reset()
				tool.arguments.WriteString(arguments)
			}
		case "message":
			s.absorbMessage(item)
		case "reasoning":
			s.absorbReasoning(item)
		}
	}
	if response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "" {
		s.incomplete = response.IncompleteDetails.Reason
	}
}

// emitMissingDeltas covers a plain JSON / deltas-less completed response: the
// whole text/reasoning is surfaced honestly as one delta so a streaming
// consumer still sees the answer.
func (s *codexStreamState) emitMissingDeltas(emit func(StreamEvent)) {
	if s.text.Len() > 0 && !s.textEmitted {
		emit(TextDeltaEvent{Content: s.text.String(), Snapshot: s.text.String()})
	}
	if s.reasoning.Len() > 0 && !s.reasoningEmitted {
		emit(ReasoningDeltaEvent{Content: s.reasoning.String()})
	}
}

func (s *codexStreamState) result() streamResult {
	model := s.model
	if model == "" {
		model = DefaultModel
	}
	completion := Completion{
		Content:          s.text.String(),
		Model:            model,
		ReasoningContent: s.reasoning.String(),
		ThinkingBlocks:   s.thinkingBlocks(),
	}
	if s.usage != nil {
		completion.InputTokens = s.usage.PromptTokens
		completion.OutputTokens = s.usage.CompletionTokens
		completion.CachedTokens, completion.CacheCreationTokens = ExtractCacheTokens(s.usage)
		completion.CostUSD = ExtractCost(s.usage, nil, model)
		completion.Credits = s.usage.Credits
	}
	for _, key := range s.order {
		tool := s.tools[key]
		if tool == nil || (tool.name == "" && tool.arguments.Len() == 0) {
			continue
		}
		arguments := strings.TrimSpace(tool.arguments.String())
		completion.ToolCalls = append(completion.ToolCalls, ToolCall{
			ID:        tool.id,
			Name:      tool.name,
			Arguments: arguments,
		})
	}
	switch {
	case len(completion.ToolCalls) > 0:
		completion.StopReason = "tool_calls"
	case s.incomplete == "max_output_tokens" || s.incomplete == "length":
		completion.StopReason = "length"
	case s.status == "incomplete":
		completion.StopReason = "length"
	default:
		completion.StopReason = "stop"
	}

	result := streamResult{completion: completion, suppressFinish: !s.completed}
	for _, call := range completion.ToolCalls {
		input, err := ParseToolCallArguments(call.Arguments, call.Name)
		if err != nil {
			input = map[string]any{}
		}
		result.toolInputs = append(result.toolInputs, input)
	}
	return result
}

// codexCompletionResponse adapts a bridged Completion to the non-streaming
// CompletionResponse shape Complete() consumes.
func codexCompletionResponse(completion Completion) CompletionResponse {
	response := CompletionResponse{
		Content:      completion.Content,
		Model:        completion.Model,
		FinishReason: completion.StopReason,
		ToolCalls:    completion.ToolCalls,
	}
	if completion.InputTokens != 0 || completion.OutputTokens != 0 || completion.CachedTokens != 0 {
		usage := &Usage{
			PromptTokens:     completion.InputTokens,
			CompletionTokens: completion.OutputTokens,
			Credits:          completion.Credits,
		}
		if completion.CachedTokens > 0 {
			usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: completion.CachedTokens}
		}
		response.Usage = usage
	}
	return response
}

// codexArguments renders a Responses arguments field, which is normally a JSON
// string but some relays send as an object.
func codexArguments(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.RawMessage:
		if len(value) == 0 || string(value) == "null" {
			return ""
		}
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			return text
		}
		return string(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

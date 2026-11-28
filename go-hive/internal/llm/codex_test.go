package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func codexRequestInput(t *testing.T, body map[string]any) []any {
	t.Helper()
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v, want a list", body["input"])
	}
	return input
}

func codexRequestMap(t *testing.T, raw any) map[string]any {
	t.Helper()
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want an object", raw)
	}
	return value
}

// Parity: LiteLLM completion_extras/litellm_responses_transformation
// (chat completions -> Responses request conversion)
func TestCodexResponsesRequestBodyConvertsHistoryAndTools(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "You are a careful engineer."},
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "look"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/shot.png", "detail": "high"}},
		}},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": "shell", "arguments": `{"command":"ls"}`},
		}}},
		{"role": "tool", "tool_call_id": "call_1", "content": "file.txt"},
		{"role": "assistant", "content": "done"},
	}
	body := codexResponsesRequestBody(map[string]any{
		"messages":            messages,
		"max_tokens":          1000,
		"temperature":         0.2,
		"parallel_tool_calls": true,
		"tools":               []any{map[string]any{"type": "function", "function": map[string]any{"name": "shell", "description": "run", "parameters": map[string]any{"type": "object"}}}},
		"tool_choice":         map[string]any{"type": "function", "function": map[string]any{"name": "shell"}},
	}, "openai/gpt-5.3-codex")

	if body["model"] != "gpt-5.3-codex" {
		t.Fatalf("model = %#v, want the wire name", body["model"])
	}
	if body["instructions"] != "You are a careful engineer." {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	if body["stream"] != true || body["store"] != false {
		t.Fatalf("stream/store = %#v/%#v, want true/false", body["stream"], body["store"])
	}
	for _, key := range []string{"messages", "max_tokens", "temperature"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s leaked into the Responses request", key)
		}
	}
	include, _ := body["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want reasoning.encrypted_content", body["include"])
	}

	input := codexRequestInput(t, body)
	if len(input) != 4 {
		t.Fatalf("input items = %d (%#v), want 4 (empty assistant turn skipped)", len(input), input)
	}
	user := codexRequestMap(t, input[0])
	if user["type"] != "message" || user["role"] != "user" {
		t.Fatalf("user item = %#v", user)
	}
	parts, _ := user["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("user parts = %#v, want text + image", parts)
	}
	if textPart := codexRequestMap(t, parts[0]); textPart["type"] != "input_text" || textPart["text"] != "look" {
		t.Fatalf("text part = %#v", textPart)
	}
	imagePart := codexRequestMap(t, parts[1])
	if imagePart["type"] != "input_image" || imagePart["image_url"] != "https://example.test/shot.png" || imagePart["detail"] != "high" {
		t.Fatalf("image part = %#v", imagePart)
	}

	call := codexRequestMap(t, input[1])
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "shell" || call["arguments"] != `{"command":"ls"}` {
		t.Fatalf("function_call item = %#v", call)
	}
	output := codexRequestMap(t, input[2])
	if output["type"] != "function_call_output" || output["call_id"] != "call_1" {
		t.Fatalf("function_call_output item = %#v", output)
	}
	outputParts, _ := output["output"].([]any)
	if len(outputParts) != 1 || codexRequestMap(t, outputParts[0])["text"] != "file.txt" {
		t.Fatalf("function_call_output parts = %#v", output)
	}
	assistant := codexRequestMap(t, input[3])
	assistantParts, _ := assistant["content"].([]any)
	if assistant["role"] != "assistant" || len(assistantParts) != 1 || codexRequestMap(t, assistantParts[0])["type"] != "output_text" {
		t.Fatalf("assistant item = %#v", assistant)
	}

	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool := codexRequestMap(t, tools[0])
	if tool["type"] != "function" || tool["name"] != "shell" || tool["description"] != "run" {
		t.Fatalf("tool = %#v", tool)
	}
	if _, ok := tool["function"]; ok {
		t.Fatalf("tool was not flattened for Responses: %#v", tool)
	}
	choice := codexRequestMap(t, body["tool_choice"])
	if choice["type"] != "function" || choice["name"] != "shell" {
		t.Fatalf("tool_choice = %#v", choice)
	}
	if body["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v", body["parallel_tool_calls"])
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream (stream=True
// for the Codex backend) and LiteLLM's Responses -> chat stream bridge.
func TestCodexResponsesStreamTranslatesTextReasoningAndToolCalls(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("path = %q, want /backend-api/codex/responses", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("ChatGPT-Account-Id"); got != "acct-1" {
			t.Errorf("ChatGPT-Account-Id = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`)
		writeSSEFrame(t, writer, `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`)
		writeSSEFrame(t, writer, `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":1,"delta":"Think "}`)
		writeSSEFrame(t, writer, `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hel"}`)
		writeSSEFrame(t, writer, `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"lo"}`)
		writeSSEFrame(t, writer, `data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"call_1","name":"shell","arguments":""}}`)
		writeSSEFrame(t, writer, `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"command\":"}`)
		writeSSEFrame(t, writer, `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"\"ls\"}"}`)
		writeSSEFrame(t, writer, `data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":2,"arguments":"{\"command\":\"ls\"}"}`)
		writeSSEFrame(t, writer, `data: {"type":"response.output_item.done","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"ls\"}"}}`)
		writeSSEFrame(t, writer, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"ls\"}"}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":3}}}}`)
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "openai/gpt-5.3-codex",
		APIKey:  "token",
		APIBase: server.URL + "/backend-api/codex",
	})
	provider.ExtraKwargs = map[string]any{
		"extra_headers": map[string]any{"ChatGPT-Account-Id": "acct-1"},
	}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		Tools:    []ToolDef{{Name: "shell", Description: "run", Parameters: map[string]any{"type": "object"}}},
	}, collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	if captured["stream"] != true || captured["store"] != false {
		t.Fatalf("request stream/store = %#v/%#v", captured["stream"], captured["store"])
	}
	if captured["instructions"] != codexDefaultInstructions {
		t.Fatalf("instructions = %#v, want the default", captured["instructions"])
	}
	input := codexRequestInput(t, captured)
	if len(input) != 1 || codexRequestMap(t, input[0])["role"] != "user" {
		t.Fatalf("input = %#v", input)
	}

	var textDeltas, reasoningDeltas []string
	for _, event := range events {
		switch typed := event.(type) {
		case TextDeltaEvent:
			textDeltas = append(textDeltas, typed.Content)
		case ReasoningDeltaEvent:
			reasoningDeltas = append(reasoningDeltas, typed.Content)
		}
	}
	if strings.Join(textDeltas, "|") != "Hel|lo" {
		t.Fatalf("text deltas = %#v", textDeltas)
	}
	if strings.Join(reasoningDeltas, "|") != "Think " {
		t.Fatalf("reasoning deltas = %#v", reasoningDeltas)
	}

	if completion.Content != "Hello" || completion.Model != "gpt-5.3-codex" {
		t.Fatalf("completion = %#v", completion)
	}
	if completion.ReasoningContent != "Think " {
		t.Fatalf("reasoning_content = %q", completion.ReasoningContent)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].ID != "call_1" ||
		completion.ToolCalls[0].Name != "shell" || completion.ToolCalls[0].Arguments != `{"command":"ls"}` {
		t.Fatalf("tool calls = %#v", completion.ToolCalls)
	}
	if completion.InputTokens != 11 || completion.OutputTokens != 7 || completion.CachedTokens != 3 {
		t.Fatalf("usage = %d/%d cached %d", completion.InputTokens, completion.OutputTokens, completion.CachedTokens)
	}
	if completion.StopReason != "tool_calls" {
		t.Fatalf("stop reason = %q", completion.StopReason)
	}

	finish, ok := events[len(events)-1].(FinishEvent)
	if !ok {
		t.Fatalf("last event = %T, want FinishEvent", events[len(events)-1])
	}
	if finish.StopReason != "tool_calls" || finish.InputTokens != 11 || finish.OutputTokens != 7 {
		t.Fatalf("finish = %#v", finish)
	}
	var toolEvent *ToolCallEvent
	for i := range events {
		if call, ok := events[i].(ToolCallEvent); ok {
			toolEvent = &call
		}
	}
	if toolEvent == nil || toolEvent.ToolName != "shell" || toolEvent.ToolInput["command"] != "ls" {
		t.Fatalf("tool call event = %#v", toolEvent)
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete (Codex
// routes non-streaming calls through the streaming bridge).
func TestCodexResponsesCompletionUsesStreamingBridge(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"response.completed","response":{"id":"resp_2","model":"gpt-5.3-codex","status":"completed","output":[{"id":"msg_2","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`)
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "openai/gpt-5.3-codex",
		APIKey:  "token",
		APIBase: server.URL + "/backend-api/codex",
	})
	completion, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("AComplete: %v", err)
	}
	if captured["stream"] != true {
		t.Fatalf("request stream = %#v, want true", captured["stream"])
	}
	if completion.Content != "Hello" || completion.StopReason != "stop" {
		t.Fatalf("completion = %#v", completion)
	}
	if completion.InputTokens != 5 || completion.OutputTokens != 2 {
		t.Fatalf("usage = %d/%d", completion.InputTokens, completion.OutputTokens)
	}
}

// Parity: core/framework/llm/litellm.py::_classify_llm_error via the Codex
// transport (P0-12's classification must cover the Responses path too).
func TestCodexResponsesStreamClassifiesPaymentRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusPaymentRequired)
		_, _ = writer.Write([]byte(`{"error":{"message":"codex plan required"}}`))
	}))
	defer server.Close()

	emitter := &recordingPaymentEmitter{}
	executionID := "exec-codex"
	provider := NewHTTPProvider(ProviderOptions{
		Model:   "openai/gpt-5.3-codex",
		APIKey:  "token",
		APIBase: server.URL + "/backend-api/codex",
	})
	provider.PaymentRequiredSink = emitter
	provider.PaymentRequiredStreamID = "chat"
	provider.PaymentRequiredExecutionID = &executionID

	var events []StreamEvent
	_, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err == nil {
		t.Fatal("ACompleteStream succeeded on a 402 response")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("error = %v, want a 402 ProviderError", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want only the stream error", events)
	}
	event, ok := events[0].(StreamErrorEvent)
	if !ok {
		t.Fatalf("event = %T, want StreamErrorEvent", events[0])
	}
	if event.ErrorType == nil || *event.ErrorType != LLMErrorTypePaymentRequired {
		t.Fatalf("error_type = %v", event.ErrorType)
	}
	if event.UpstreamStatus == nil || *event.UpstreamStatus != http.StatusPaymentRequired {
		t.Fatalf("upstream_status = %v", event.UpstreamStatus)
	}
	calls := emitter.snapshot()
	if len(calls) != 1 || calls[0].streamID != "chat" {
		t.Fatalf("payment calls = %#v", calls)
	}
}

// The endpoint helper must not double-append /responses.
// Parity-exempt: Go-only Codex Responses endpoint normalization; upstream
// builds this URL inside LiteLLM rather than in a testable helper.
func TestCodexResponsesEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://chatgpt.com/backend-api/codex":             "https://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/codex/":            "https://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/codex/responses":   "https://chatgpt.com/backend-api/codex/responses",
		"http://127.0.0.1:9999/backend-api/codex/responses": "http://127.0.0.1:9999/backend-api/codex/responses",
	}
	keys := make([]string, 0, len(cases))
	for key := range cases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, base := range keys {
		if got := codexResponsesEndpoint(base); got != cases[base] {
			t.Errorf("codexResponsesEndpoint(%q) = %q, want %q", base, got, cases[base])
		}
	}
}

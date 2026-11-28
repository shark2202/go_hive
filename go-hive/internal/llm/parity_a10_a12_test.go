package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A10: the streaming request shape splits the static system prefix from the
// dynamic turn suffix for cache-control providers, and stays concatenated for
// everyone else.
//
// Parity: core/framework/llm/litellm.py::_build_system_message
func TestCompletionRequestKwargsSplitsCacheableSystemPrefix(t *testing.T) {
	cacheable := NewProvider(ProviderOptions{Model: "openrouter/anthropic/claude-opus-4.5"})
	kwargs := cacheable.completionRequestKwargs(CompletionRequest{
		System:              "static prefix",
		SystemDynamicSuffix: "Current time: now",
		MaxTokens:           64,
	})
	messages, _ := kwargs["messages"].([]map[string]any)
	if len(messages) != 1 || messages[0]["role"] != "system" {
		t.Fatalf("messages = %#v", kwargs["messages"])
	}
	blocks, ok := messages[0]["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("system content = %#v, want two blocks", messages[0]["content"])
	}
	static, _ := blocks[0].(map[string]any)
	if static["text"] != "static prefix" {
		t.Fatalf("static block = %#v", static)
	}
	if control, _ := static["cache_control"].(map[string]any); control["type"] != "ephemeral" {
		t.Fatalf("static cache_control = %#v", static["cache_control"])
	}
	dynamic, _ := blocks[1].(map[string]any)
	if dynamic["text"] != "Current time: now" {
		t.Fatalf("dynamic block = %#v", dynamic)
	}
	if _, present := dynamic["cache_control"]; present {
		t.Fatalf("dynamic block must stay uncached: %#v", dynamic)
	}

	plain := NewProvider(ProviderOptions{Model: "openai/gpt-4o-mini"})
	kwargs = plain.completionRequestKwargs(CompletionRequest{
		System:              "static prefix",
		SystemDynamicSuffix: "Current time: now",
		MaxTokens:           64,
	})
	messages, _ = kwargs["messages"].([]map[string]any)
	if messages[0]["content"] != "static prefix\n\nCurrent time: now" {
		t.Fatalf("non-cache system content = %#v", messages[0]["content"])
	}
}

// A11: a disconnected stream carries no finish_reason, and upstream emits no
// tail events for it. The Go transport must not synthesize Finish(stop="").
// Parity-exempt: Go-only stream-tail suppression regression guard.
func TestStreamWithoutFinishReasonSuppressesTail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "k", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if completion.Content != "partial" || completion.StopReason != "" {
		t.Fatalf("completion = %#v, want partial content and no stop reason", completion)
	}
	for _, event := range events {
		switch event.(type) {
		case FinishEvent, TextEndEvent, ToolCallEvent:
			t.Fatalf("truncated stream emitted a tail event: %#v", event)
		}
	}
}

// A11: gateway/server failures are retried up to the transient cap instead of
// failing the turn on the first 5xx.
// Parity-exempt: Go-only transient gateway retry around streaming HTTP.
func TestStreamRetriesTransientGatewayFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"message":"upstream unavailable"}}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "k", APIBase: server.URL})
	provider.SleepFunc = func(time.Duration) {}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want one 503 then one retry", calls.Load())
	}
	if completion.Content != "recovered" || completion.StopReason != "stop" {
		t.Fatalf("completion = %#v", completion)
	}
	var finishes, errorsSeen int
	for _, event := range events {
		switch event.(type) {
		case FinishEvent:
			finishes++
		case StreamErrorEvent:
			errorsSeen++
		}
	}
	if finishes != 1 || errorsSeen != 0 {
		t.Fatalf("finishes = %d, stream errors = %d", finishes, errorsSeen)
	}
}

// A12: usage.credits (Hive proxy) reaches both the Completion and the
// FinishEvent; nil stays nil for providers that do not report it.
// Parity: core/framework/llm/provider.py::LLMResponse
func TestStreamCarriesUsageCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, `data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"credits":0.25}}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "k", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if completion.Credits == nil || *completion.Credits != 0.25 {
		t.Fatalf("completion credits = %v", completion.Credits)
	}
	var finish *FinishEvent
	for i := range events {
		if typed, ok := events[i].(FinishEvent); ok {
			finish = &typed
		}
	}
	if finish == nil || finish.Credits == nil || *finish.Credits != 0.25 {
		t.Fatalf("finish credits = %#v", finish)
	}
}

// A12: an OpenRouter model without native tool support falls back to the JSON
// compatibility prompt, parses the synthetic calls, and caches the decision.
//
// Parity: core/tests/test_litellm_provider.py
// ::test_stream_tool_compat_parses_json_tool_calls_and_uses_cache
func TestOpenRouterToolCompatFallbackAndCache(t *testing.T) {
	model := "openrouter/liquid/lfm-2.5-1.2b-thinking:free"
	defer func() {
		openRouterToolCompatCache.Lock()
		delete(openRouterToolCompatCache.expiresAt, model)
		openRouterToolCompatCache.Unlock()
	}()

	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if _, hasTools := body["tools"]; hasTools {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"error":{"message":"No endpoints found that support tool use."}}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model":"liquid/lfm-2.5","choices":[{"index":0,"message":{"role":"assistant","content":"{\"assistant_response\":\"searching\",\"tool_calls\":[{\"name\":\"web_search\",\"arguments\":{\"query\":\"hive\"}}]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":18,"completion_tokens":9}}`)
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: model, APIKey: "k", APIBase: server.URL})
	request := CompletionRequest{
		System:   "Use tools when needed.",
		Tools:    []ToolDef{{Name: "web_search", Description: "Search the web", Parameters: map[string]any{"type": "object"}}},
		Messages: []ChatMessage{{Role: "user", Content: "search hive"}},
	}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(), request, collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the failed probe plus the compat answer", len(bodies))
	}
	if _, hasTools := bodies[0]["tools"]; !hasTools {
		t.Fatal("first request did not ask with native tools")
	}
	if _, hasTools := bodies[1]["tools"]; hasTools {
		t.Fatal("compat request must not send tools")
	}
	messages, _ := bodies[1]["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("compat messages = %#v", bodies[1]["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if text, _ := first["content"].(string); !strings.Contains(text, "Tool compatibility mode is active") {
		t.Fatalf("compat system prompt = %#v", first["content"])
	}
	if completion.Content != "searching" || len(completion.ToolCalls) != 1 {
		t.Fatalf("completion = %#v", completion)
	}
	if completion.ToolCalls[0].Name != "web_search" || !strings.Contains(completion.ToolCalls[0].Arguments, `"hive"`) {
		t.Fatalf("tool call = %#v", completion.ToolCalls[0])
	}
	if completion.StopReason != "tool_calls" {
		t.Fatalf("stop reason = %q", completion.StopReason)
	}
	var toolEvents, finishes int
	for _, event := range events {
		switch typed := event.(type) {
		case ToolCallEvent:
			toolEvents++
			if typed.ToolName != "web_search" || typed.ToolInput["query"] != "hive" {
				t.Fatalf("tool event = %#v", typed)
			}
		case FinishEvent:
			finishes++
			if typed.StopReason != "tool_calls" {
				t.Fatalf("finish = %#v", typed)
			}
		}
	}
	if toolEvents != 1 || finishes != 1 {
		t.Fatalf("tool events = %d, finishes = %d", toolEvents, finishes)
	}
	if !isOpenRouterToolCompatCached(model) {
		t.Fatal("compatibility decision was not cached")
	}

	// The cached decision skips the native-tools probe entirely.
	if _, err := provider.ACompleteStream(context.Background(), request, nil); err != nil {
		t.Fatalf("cached ACompleteStream: %v", err)
	}
	mu.Lock()
	cachedCalls := len(bodies)
	mu.Unlock()
	if cachedCalls != 3 {
		t.Fatalf("requests after cached turn = %d, want one compat call only", cachedCalls)
	}
}

// A12: Gemini-style `<tool_code>` text is converted into real tool calls and
// stripped from the visible answer.
//
// Parity: core/framework/llm/litellm.py::_extract_text_tool_calls
func TestStreamExtractsGeminiToolCodeText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"content":"Let me check. <tool_code>{\"web_search\": {\"query\": \"go\"}}</tool_code>"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gemini/gemini-2.0-flash", APIKey: "k", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "search"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if completion.Content != "Let me check." {
		t.Fatalf("content = %q, want the cleaned text", completion.Content)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "web_search" {
		t.Fatalf("tool calls = %#v", completion.ToolCalls)
	}
	if !strings.HasPrefix(completion.ToolCalls[0].ID, "synth_") {
		t.Fatalf("synthetic id = %q", completion.ToolCalls[0].ID)
	}
	var toolEvent *ToolCallEvent
	var corrected bool
	for i := range events {
		switch typed := events[i].(type) {
		case ToolCallEvent:
			toolEvent = &typed
		case TextDeltaEvent:
			if typed.Content == "" && typed.Snapshot == "Let me check." {
				corrected = true
			}
		}
	}
	if toolEvent == nil || toolEvent.ToolInput["query"] != "go" {
		t.Fatalf("tool event = %#v", toolEvent)
	}
	if !corrected {
		t.Fatal("missing the corrected snapshot delta")
	}
}

// P0-13 residual: encrypted Codex reasoning items are captured on the
// Completion and replayed as Responses reasoning input items on the next turn.
// Parity: core/framework/llm/litellm.py::LiteLLMProvider
func TestCodexReasoningItemPersistedAndReplayed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Think"}],"encrypted_content":"enc-payload"},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":4,"output_tokens":2}}}`)
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "openai/gpt-5.3-codex",
		APIKey:  "k",
		APIBase: server.URL + "/backend-api/codex",
	})
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if len(completion.ThinkingBlocks) != 1 {
		t.Fatalf("thinking blocks = %#v", completion.ThinkingBlocks)
	}
	block := completion.ThinkingBlocks[0]
	if block["type"] != "reasoning" || block["encrypted_content"] != "enc-payload" || block["id"] != "rs_1" {
		t.Fatalf("reasoning block = %#v", block)
	}

	body := codexResponsesRequestBody(map[string]any{
		"messages": ChatMessagesToMaps([]ChatMessage{{
			Role:           "assistant",
			ThinkingBlocks: completion.ThinkingBlocks,
			ToolCalls:      []ToolCall{{ID: "call_1", Name: "shell", Arguments: "{}"}},
		}}),
	}, "openai/gpt-5.3-codex")
	input := codexRequestInput(t, body)
	if len(input) != 2 {
		t.Fatalf("input = %#v, want reasoning + function_call", input)
	}
	reasoning, _ := input[0].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["encrypted_content"] != "enc-payload" || reasoning["id"] != "rs_1" {
		t.Fatalf("replayed reasoning item = %#v", reasoning)
	}
	summary, _ := reasoning["summary"].([]any)
	if len(summary) != 1 || summary[0].(map[string]any)["text"] != "Think" {
		t.Fatalf("replayed summary = %#v", reasoning["summary"])
	}
}

// Anthropic thinking blocks (no encrypted_content) must not leak into a Codex
// request as malformed reasoning items.
// Parity: core/framework/llm/litellm.py::LiteLLMProvider
func TestCodexReasoningReplayIgnoresAnthropicBlocks(t *testing.T) {
	body := codexResponsesRequestBody(map[string]any{
		"messages": []map[string]any{{
			"role":            "assistant",
			"content":         "answer",
			"thinking_blocks": []map[string]any{{"type": "thinking", "thinking": "x", "signature": "sig"}},
		}},
	}, "openai/gpt-5.3-codex")
	input := codexRequestInput(t, body)
	if len(input) != 1 || input[0].(map[string]any)["type"] != "message" {
		t.Fatalf("input = %#v", input)
	}
}

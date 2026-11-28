package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func expectedClaudeCodeBillingLine(t *testing.T, firstUserText string) string {
	t.Helper()
	sampled := claudeCodeSampleCharacters(firstUserText)
	if sampled != "ehu" {
		t.Fatalf("test fixture sampled characters = %q, want ehu", sampled)
	}
	sum := sha256.Sum256([]byte(claudeCodeBillingSalt + sampled + ClaudeCodeVersion))
	versionHash := hex.EncodeToString(sum[:])
	return "x-anthropic-billing-header: cc_version=" + ClaudeCodeVersion + "." + versionHash[:3] +
		"; cc_entrypoint=cli; cch=00000;"
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.__init__ (user-agent),
// the litellm Anthropic OAuth header patch (anthropic-beta), and
// ::LiteLLMProvider.stream (billing system block)
// flattenSystemText renders an Anthropic top-level system field (string or
// cached text-block list) as plain text for assertions.
func flattenSystemText(t *testing.T, raw any) string {
	t.Helper()
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		var texts []string
		for _, entry := range value {
			if block, ok := entry.(map[string]any); ok {
				if text, ok := block["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, "\n\n")
	}
	t.Fatalf("system = %#v, want a string or text-block list", raw)
	return ""
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider
func TestClaudeCodeSubscriptionStreamRequestShape(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	capture := &streamCapture{}
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers = request.Header.Clone()
		capture.record(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":9}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_stop"}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("sk-ant-oat01-test", "claude-sonnet-4", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	if _, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		System:   "You are Hive.",
		Messages: []ChatMessage{{Role: "user", Content: "abcdefghijklmnopqrstuvwxyz"}},
	}, nil); err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	if got := headers.Get("Authorization"); got != "Bearer sk-ant-oat01-test" {
		t.Fatalf("Authorization = %q, want the OAuth bearer", got)
	}
	if got := headers.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want it omitted for an OAuth token", got)
	}
	if got := headers.Get("User-Agent"); got != ClaudeCodeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, ClaudeCodeUserAgent)
	}
	if got := headers.Get("anthropic-beta"); !strings.Contains(got, ClaudeCodeOAuthBetaHeader) {
		t.Fatalf("anthropic-beta = %q, want it to contain %q", got, ClaudeCodeOAuthBetaHeader)
	}

	body := capture.snapshot()[0]
	system := flattenSystemText(t, body["system"])
	if !strings.Contains(system, expectedClaudeCodeBillingLine(t, "abcdefghijklmnopqrstuvwxyz")) {
		t.Fatalf("system prompt missing the Claude Code billing block: %q", system)
	}
	if !strings.Contains(system, "You are Hive.") {
		t.Fatalf("system prompt lost the caller prompt: %q", system)
	}
	if strings.Contains(system, "role") {
		t.Fatalf("billing system message leaked into a non-system slot: %q", system)
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.acomplete (the
// non-streaming path prepends the same billing system block)
func TestClaudeCodeSubscriptionNonStreamRequestShape(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	capture := &streamCapture{}
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers = request.Header.Clone()
		capture.record(t, request)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"model":"claude-sonnet-4",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":9,"output_tokens":2}
		}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("sk-ant-oat01-test", "claude-sonnet-4", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "abcdefghijklmnopqrstuvwxyz"},
	}, CompleteOptions{System: "You are Hive.", MaxTokens: 32}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got := headers.Get("User-Agent"); got != ClaudeCodeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, ClaudeCodeUserAgent)
	}
	if got := headers.Get("anthropic-beta"); !strings.Contains(got, ClaudeCodeOAuthBetaHeader) {
		t.Fatalf("anthropic-beta = %q, want the OAuth beta", got)
	}
	body := capture.snapshot()[0]
	system := flattenSystemText(t, body["system"])
	if !strings.Contains(system, expectedClaudeCodeBillingLine(t, "abcdefghijklmnopqrstuvwxyz")) {
		t.Fatalf("non-stream system prompt missing the billing block: %q", system)
	}
}

// Parity: core/framework/llm/litellm.py Anthropic OAuth header patch (merge
// with caller-supplied anthropic-beta rather than replacing it)
func TestClaudeCodeHeadersMergeExistingBeta(t *testing.T) {
	provider := NewProvider(ProviderOptions{Model: "claude-sonnet-4", APIKey: "sk-ant-oat01-test"})
	headers := anthropicRequestHeaders(provider, map[string]any{
		"extra_headers": map[string]any{
			"anthropic-beta": "custom-beta",
			"User-Agent":     "caller-agent",
		},
	}, "https://api.anthropic.com", "sk-ant-oat01-test")

	if got := headers["anthropic-beta"]; got != "custom-beta,"+ClaudeCodeOAuthBetaHeader {
		t.Fatalf("anthropic-beta = %q, want the merged value", got)
	}
	if got := headers["User-Agent"]; got != "caller-agent" {
		t.Fatalf("User-Agent = %q, want the caller value preserved", got)
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.__init__ (plain API
// keys never get the Claude Code headers or billing block)
func TestClaudeCodeHeadersAbsentForPlainAPIKey(t *testing.T) {
	provider := NewProvider(ProviderOptions{Model: "claude-sonnet-4", APIKey: "sk-ant-api03-test"})
	headers := anthropicRequestHeaders(provider, nil, "https://api.anthropic.com", "sk-ant-api03-test")
	if _, present := headers["User-Agent"]; present {
		t.Fatalf("User-Agent leaked onto a plain API key: %#v", headers)
	}
	if _, present := headers["anthropic-beta"]; present {
		t.Fatalf("anthropic-beta leaked onto a plain API key: %#v", headers)
	}
	body, err := anthropicRequestBody(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "abcdefghijklmnopqrstuvwxyz"}},
	}, "claude-sonnet-4", "sk-ant-api03-test")
	if err != nil {
		t.Fatalf("anthropicRequestBody: %v", err)
	}
	if _, present := body["system"]; present {
		t.Fatalf("billing block leaked onto a plain API key: %#v", body["system"])
	}
}

// Parity: core/framework/llm/litellm.py Anthropic request transform (the
// assembled body stays JSON-encodable after the billing block is prepended)
func TestAnthropicRequestBodyIsJSONEncodable(t *testing.T) {
	body, err := anthropicRequestBody(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, "claude-sonnet-4", "")
	if err != nil {
		t.Fatalf("anthropicRequestBody: %v", err)
	}
	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal body: %v", err)
	}
}

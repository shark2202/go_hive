package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureCompletionServer replies with a minimal completion and records the
// decoded request body each wire test asserts against.
func captureCompletionServer(t *testing.T, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	captured := &map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		*captured = decoded
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(reply))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// Parity-exempt: ChatMessage.ContentParts is the Go transport seam for the
// blocks upstream hands to LiteLLM; there is no Python-side test target.
func TestChatMessagesToMapsSerializesContentParts(t *testing.T) {
	imageBlock := map[string]any{
		"type":      "image_url",
		"image_url": map[string]any{"url": "data:image/png;base64,AAAA"},
	}
	messages := ChatMessagesToMaps([]ChatMessage{
		{Role: "user", Content: "text projection", ContentParts: []map[string]any{
			{"type": "text", "text": "look"},
			imageBlock,
		}},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "attach_file", Arguments: "{}"}}},
	})
	parts, ok := messages[0]["content"].([]any)
	if !ok {
		t.Fatalf("user content = %#v, want a parts array", messages[0]["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %#v", parts)
	}
	if part, _ := parts[1].(map[string]any); part["type"] != "image_url" {
		t.Fatalf("parts[1] = %#v", parts[1])
	}
	if messages[1]["content"] != nil {
		t.Fatalf("assistant content = %#v, want nil for a tool-call turn", messages[1]["content"])
	}

	plain := ChatMessagesToMaps([]ChatMessage{{Role: "user", Content: "just text"}})
	if plain[0]["content"] != "just text" {
		t.Fatalf("plain content = %#v, want the string", plain[0]["content"])
	}
}

// Parity-exempt: this pins the wire shape the OpenAI-compatible transport
// sends; upstream produces it inside LiteLLM rather than in Hive code.
func TestOpenAIWireCarriesImageAndFileBlocks(t *testing.T) {
	server, captured := captureCompletionServer(t,
		`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-5.4", APIKey: "sk-test", APIBase: server.URL})
	_, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{
			Role: "user",
			ContentParts: []map[string]any{
				{"type": "text", "text": "describe"},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
				{"type": "file", "file": map[string]any{
					"file_data": "data:application/pdf;base64,JVBERi0=",
					"filename":  "doc.pdf",
				}},
			},
		}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("AComplete: %v", err)
	}

	contentParts := firstMessageParts(t, captured)
	if len(contentParts) != 3 {
		t.Fatalf("content parts = %#v", contentParts)
	}
	if partsType(contentParts[1]) != "image_url" || partsType(contentParts[2]) != "file" {
		t.Fatalf("block types = %s / %s", partsType(contentParts[1]), partsType(contentParts[2]))
	}
	file, _ := contentParts[2].(map[string]any)["file"].(map[string]any)
	if file["file_data"] != "data:application/pdf;base64,JVBERi0=" || file["filename"] != "doc.pdf" {
		t.Fatalf("file block = %#v", file)
	}
}

// Parity-exempt: pins the Anthropic request transform (file -> document)
// that LiteLLM performs upstream without a Hive-side test target.
func TestAnthropicWireMapsFileToDocument(t *testing.T) {
	server, captured := captureCompletionServer(t,
		`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)

	provider := NewHTTPProvider(ProviderOptions{Model: "anthropic/claude-opus-4-6", APIKey: "sk-test", APIBase: server.URL})
	_, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "attached"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "attach_file", Arguments: "{}"}}},
			{Role: "tool", ToolCallID: "c1", Content: `{"attached":[]}`, ContentParts: []map[string]any{
				{"type": "text", "text": `{"attached":[]}`},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
				{"type": "file", "file": map[string]any{
					"file_data": "data:application/pdf;base64,JVBERi0=",
					"filename":  "doc.pdf",
				}},
			}},
		},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("AComplete: %v", err)
	}

	messages, ok := (*captured)["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v", (*captured)["messages"])
	}
	toolResult := findToolResultParts(t, messages)
	if len(toolResult) != 3 {
		t.Fatalf("tool_result parts = %#v", toolResult)
	}
	if partsType(toolResult[1]) != "image" {
		t.Fatalf("parts[1] = %#v, want an image block", toolResult[1])
	}
	document, _ := toolResult[2].(map[string]any)
	if partsType(toolResult[2]) != "document" {
		t.Fatalf("parts[2] = %#v, want a document block", toolResult[2])
	}
	source, _ := document["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != "JVBERi0=" {
		t.Fatalf("document source = %#v", source)
	}
	if _, leaked := document["file_data"]; leaked {
		t.Fatalf("openai file block leaked into the anthropic body: %#v", document)
	}
}

func firstMessageParts(t *testing.T, captured *map[string]any) []any {
	t.Helper()
	messages, ok := (*captured)["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v", (*captured)["messages"])
	}
	for _, raw := range messages {
		target, _ := raw.(map[string]any)
		if target["role"] == "system" || target["role"] == "developer" {
			continue
		}
		parts, ok := target["content"].([]any)
		if !ok {
			t.Fatalf("content = %#v, want a parts array", target["content"])
		}
		return parts
	}
	t.Fatal("no non-system message in the openai body")
	return nil
}

func findToolResultParts(t *testing.T, messages []any) []any {
	t.Helper()
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if block["type"] != "tool_result" {
				continue
			}
			parts, ok := block["content"].([]any)
			if !ok {
				t.Fatalf("tool_result content = %#v", block["content"])
			}
			return parts
		}
	}
	t.Fatal("no tool_result block in the anthropic body")
	return nil
}

func partsType(part any) string {
	block, _ := part.(map[string]any)
	blockType, _ := block["type"].(string)
	return blockType
}

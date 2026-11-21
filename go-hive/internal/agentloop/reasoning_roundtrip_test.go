package agentloop

// Reasoning round-trip parity for the request path.
//
// The real-LLM crosscheck (docs/llm-real-crosscheck.md) found that the Go
// port's request projection dropped an assistant step's reasoning_content, so
// reasoning models (DeepSeek, Qwen, GLM) read every earlier step of a turn as
// unthought. The reference keeps it for the steps after the last user message
// (reasoningForThisTurn) and sends it back.
//
// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict

import "testing"

// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict
func TestToChatMessagesCarriesReasoningContent(t *testing.T) {
	msgs := []map[string]any{
		{"role": RoleUser, "content": "What time is it?"},
		{
			"role":              RoleAssistant,
			"content":           "",
			"reasoning_content": "Call the time tool.",
			"tool_calls": []map[string]any{
				{
					"id":   "call_1",
					"type": "function",
					"function": map[string]any{
						"name":      "get_current_time",
						"arguments": `{"timezone": "UTC"}`,
					},
				},
			},
		},
		{"role": RoleTool, "tool_call_id": "call_1", "content": `{"time": "00:00"}`},
	}
	chats := toChatMessages(msgs)
	if len(chats) != 3 {
		t.Fatalf("chat message count = %d, want 3", len(chats))
	}
	if chats[1].ReasoningContent != "Call the time tool." {
		t.Fatalf("assistant reasoning_content = %q, want %q", chats[1].ReasoningContent, "Call the time tool.")
	}
	if chats[0].ReasoningContent != "" || chats[2].ReasoningContent != "" {
		t.Fatalf("reasoning leaked onto non-assistant messages: %#v / %#v", chats[0].ReasoningContent, chats[2].ReasoningContent)
	}
	if len(chats[1].ToolCalls) != 1 || chats[1].ToolCalls[0].Arguments != `{"timezone": "UTC"}` {
		t.Fatalf("tool call projection drifted: %#v", chats[1].ToolCalls)
	}
}

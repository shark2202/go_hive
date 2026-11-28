package agentloop

// Thinking-block round trip parity for the agent loop.
//
// The LLM layer reassembles signed Anthropic thinking blocks onto Completion
// and FinishEvent, but the blocks only help if the loop persists them on the
// assistant turn and projects them back into the next request: Anthropic
// rejects a follow-up that drops a signed block (the "signature required" 400).
// These tests pin the two halves the w94/llm lane left unwired — the assistant
// message options and the toChatMessages projection — including the on-disk
// round trip a resumed session takes.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
// (FinishEvent.thinking_blocks -> Message(thinking_blocks=...))
// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func signedThinkingBlocks() []map[string]any {
	return []map[string]any{{
		"type":      "thinking",
		"thinking":  "Weigh the two options before answering.",
		"signature": "SIG-ABC",
	}}
}

// thinkingRoundTripCompleter answers the first call with a tool call carrying
// signed thinking blocks and every later call with plain text, recording each
// request so the projection can be asserted.
type thinkingRoundTripCompleter struct {
	blocks []map[string]any

	mu    sync.Mutex
	calls []llm.CompletionRequest
}

func (c *thinkingRoundTripCompleter) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	index := len(c.calls)
	c.mu.Unlock()
	if index == 1 {
		return llm.Completion{
			ToolCalls:      []llm.ToolCall{{ID: "call_1", Name: "lookup", Arguments: "{}"}},
			ThinkingBlocks: c.blocks,
		}, nil
	}
	return llm.Completion{Content: "done"}, nil
}

func (c *thinkingRoundTripCompleter) request(i int) llm.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[i]
}

// assistantStepWithToolCalls returns the projected assistant message that
// produced tool calls, i.e. the step upstream attaches thinking blocks to.
func assistantStepWithToolCalls(t *testing.T, messages []llm.ChatMessage) llm.ChatMessage {
	t.Helper()
	for _, message := range messages {
		if message.Role == RoleAssistant && len(message.ToolCalls) > 0 {
			return message
		}
	}
	t.Fatalf("no assistant tool-call step in %#v", messages)
	return llm.ChatMessage{}
}

// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict
func TestToChatMessagesCarriesThinkingBlocks(t *testing.T) {
	blocks := signedThinkingBlocks()
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("look it up", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if _, err := conv.AddAssistantMessage("on it", nil, AssistantMessageOptions{ThinkingBlocks: blocks}); err != nil {
		t.Fatalf("AddAssistantMessage: %v", err)
	}

	chats := toChatMessages(conv.ToLLMMessages("claude-sonnet-4"))
	if len(chats) != 2 {
		t.Fatalf("chat message count = %d, want 2", len(chats))
	}
	if !reflect.DeepEqual(chats[1].ThinkingBlocks, blocks) {
		t.Fatalf("assistant thinking_blocks = %#v, want the signed block verbatim", chats[1].ThinkingBlocks)
	}
	if len(chats[0].ThinkingBlocks) != 0 {
		t.Fatalf("thinking blocks leaked onto the user step: %#v", chats[0].ThinkingBlocks)
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (the turn's
// signed blocks are persisted on the assistant message and re-sent on the next
// request, including after a conversation restore).
func TestLoopPersistsAndEchoesThinkingBlocks(t *testing.T) {
	blocks := signedThinkingBlocks()
	completer := &thinkingRoundTripCompleter{blocks: blocks}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3}, func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "looked up " + req.Name}, nil
	})
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt: "You are a test assistant.",
		Store:        store,
	})
	ctx := midturnAgentContext(completer, "lookup")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	var stored []map[string]any
	for _, message := range conv.Messages() {
		if message.Role == RoleAssistant && len(message.ToolCalls) > 0 {
			stored = message.ThinkingBlocks
		}
	}
	if !reflect.DeepEqual(stored, blocks) {
		t.Fatalf("assistant message thinking_blocks = %#v, want the completion's signed block", stored)
	}

	// The follow-up request echoes the block on the assistant step, in the
	// shape the Anthropic transport projects into thinking content blocks.
	second := completer.request(1)
	assistant := assistantStepWithToolCalls(t, second.Messages)
	if !reflect.DeepEqual(assistant.ThinkingBlocks, blocks) {
		t.Fatalf("follow-up request thinking_blocks = %#v, want the signed block verbatim", assistant.ThinkingBlocks)
	}

	// A resumed session rebuilds the message from storage; the block must
	// survive that round trip or the next request after a restart loses the
	// signature.
	restored, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("RestoreConversation returned no conversation")
	}
	restoredChats := toChatMessages(restored.ToLLMMessages("claude-sonnet-4"))
	restoredAssistant := assistantStepWithToolCalls(t, restoredChats)
	if !reflect.DeepEqual(restoredAssistant.ThinkingBlocks, blocks) {
		t.Fatalf("restored request thinking_blocks = %#v, want the persisted signed block", restoredAssistant.ThinkingBlocks)
	}
}

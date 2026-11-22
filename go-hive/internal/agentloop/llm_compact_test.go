package agentloop

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity-exempt: Go-only unit tests for llm_compact.go, which mirrors

type compactFake struct {
	respond func(prompt string, index int) (string, error)

	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (f *compactFake) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[0].Content
	}
	content, err := f.respond(prompt, index)
	if err != nil {
		return llm.Completion{}, err
	}
	return llm.Completion{Content: content}, nil
}

func (f *compactFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *compactFake) request(i int) llm.CompletionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

func userMessage(content string) Message {
	return Message{Role: RoleUser, Content: content}
}

// Parity-exempt: llm_compact.go has no dedicated Python test module.
func TestLlmCompactPromptCarriesAgentContextAndOutputs(t *testing.T) {
	criteria := "do it well"
	spec := &AgentSpec{
		ID:              "agent",
		Name:            "Agent",
		Description:     "d",
		SuccessCriteria: &criteria,
		OutputKeys:      []string{"a", "b", "c"},
	}
	accumulator := map[string]any{"a": "done", "b": nil, "c": nil}
	fake := &compactFake{respond: func(string, int) (string, error) { return "summary", nil }}

	messages := []Message{userMessage("hello"), userMessage("world")}
	summary, err := LlmCompact(context.Background(), fake, messages, LlmCompactOptions{
		Spec:        spec,
		Accumulator: accumulator,
	})
	if err != nil {
		t.Fatalf("LlmCompact: %v", err)
	}
	if summary != "summary" {
		t.Fatalf("summary = %q, want %q", summary, "summary")
	}
	if fake.callCount() != 1 {
		t.Fatalf("%d completion calls, want 1", fake.callCount())
	}

	request := fake.request(0)
	if request.System != llmCompactSystemMessage(false) {
		t.Fatalf("system prompt = %q", request.System)
	}
	prompt := request.Messages[0].Content
	for _, want := range []string{
		"NODE: Agent (id=agent)",
		"PURPOSE: d",
		"SUCCESS CRITERIA: do it well",
		"OUTPUTS ALREADY SET:\n  a: done",
		"OUTPUTS STILL NEEDED: b, c",
		"CONVERSATION MESSAGES:\n[user]: hello\n\n[user]: world",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// Parity-exempt: llm_compact.go has no dedicated Python test module.
func TestLlmCompactSummaryBudgetScalesWithContext(t *testing.T) {
	big := strings.Repeat("x", 40_000)
	messages := []Message{userMessage(big)}
	formattedChars := len(FormatMessagesForSummary(messages))
	targetTokens := CompactionTargetChars(formattedChars, 128_000) / 4

	fake := &compactFake{respond: func(string, int) (string, error) { return "summary", nil }}
	if _, err := LlmCompact(context.Background(), fake, messages, LlmCompactOptions{}); err != nil {
		t.Fatalf("LlmCompact: %v", err)
	}
	if got := fake.request(0).MaxTokens; got != LLMCompactSummaryMaxTokens {
		t.Fatalf("normal budget = %d, want %d", got, LLMCompactSummaryMaxTokens)
	}

	fake = &compactFake{respond: func(string, int) (string, error) { return "summary", nil }}
	if _, err := LlmCompact(context.Background(), fake, messages, LlmCompactOptions{PreserveUserMessages: true}); err != nil {
		t.Fatalf("LlmCompact preserve: %v", err)
	}
	wantPreserve := maxInt(1024, targetTokens*2)
	if got := fake.request(0).MaxTokens; got != wantPreserve {
		t.Fatalf("preserve budget = %d, want %d", got, wantPreserve)
	}
}

// Parity-exempt: llm_compact.go has no dedicated Python test module.
func TestLlmCompactPreserveUserMessagesAmplifiesPrompt(t *testing.T) {
	fake := &compactFake{respond: func(string, int) (string, error) { return "summary", nil }}
	if _, err := LlmCompact(context.Background(), fake,
		[]Message{userMessage("hi")}, LlmCompactOptions{PreserveUserMessages: true}); err != nil {
		t.Fatalf("LlmCompact: %v", err)
	}
	request := fake.request(0)
	if !strings.Contains(request.Messages[0].Content, "Reproduce EVERY user message verbatim") {
		t.Fatalf("prompt lacks the verbatim instruction:\n%s", request.Messages[0].Content)
	}
	if !strings.Contains(request.System, "reproduce every user message verbatim") {
		t.Fatalf("system prompt lacks the verbatim instruction: %q", request.System)
	}
}

// Parity-exempt: llm_compact.go has no dedicated Python test module.
func TestLlmCompactProactiveSplitOnOversizedInput(t *testing.T) {
	fake := &compactFake{respond: func(prompt string, _ int) (string, error) {
		switch {
		case strings.Contains(prompt, "FIRST"):
			return "LEFT", nil
		case strings.Contains(prompt, "SECOND"):
			return "RIGHT", nil
		default:
			return "COMBINED", nil
		}
	}}
	limit := 10
	summary, err := LlmCompact(context.Background(), fake,
		[]Message{userMessage("FIRST body"), userMessage("SECOND body")},
		LlmCompactOptions{CharLimit: &limit})
	if err != nil {
		t.Fatalf("LlmCompact: %v", err)
	}
	if fake.callCount() != 2 {
		t.Fatalf("%d completion calls, want 2 (one per half)", fake.callCount())
	}
	if summary != "LEFT\n\nRIGHT" {
		t.Fatalf("summary = %q, want %q", summary, "LEFT\n\nRIGHT")
	}
}

// Parity-exempt: llm_compact.go has no dedicated Python test module.
func TestLlmCompactFallsBackToSplitOnContextTooLarge(t *testing.T) {
	fake := &compactFake{respond: func(prompt string, _ int) (string, error) {
		left := strings.Contains(prompt, "FIRST")
		right := strings.Contains(prompt, "SECOND")
		switch {
		case left && right:
			return "", errors.New("maximum context length exceeded")
		case left:
			return "LEFT", nil
		case right:
			return "RIGHT", nil
		default:
			return "COMBINED", nil
		}
	}}
	summary, err := LlmCompact(context.Background(), fake,
		[]Message{userMessage("FIRST body"), userMessage("SECOND body")}, LlmCompactOptions{})
	if err != nil {
		t.Fatalf("LlmCompact: %v", err)
	}
	if fake.callCount() != 3 {
		t.Fatalf("%d completion calls, want 3 (combined + two halves)", fake.callCount())
	}
	if summary != "LEFT\n\nRIGHT" {
		t.Fatalf("summary = %q, want %q", summary, "LEFT\n\nRIGHT")
	}
}

// Parity-exempt: llm_compact.go has no dedicated Python test module.
func TestLlmCompactDepthGuard(t *testing.T) {
	fake := &compactFake{respond: func(string, int) (string, error) {
		t.Fatalf("no completion call should be made past the depth guard")
		return "", nil
	}}
	limit := 1
	messages := []Message{
		userMessage("m1"),
		userMessage("m2"),
		userMessage("m3"),
		userMessage("m4"),
	}
	_, err := LlmCompact(context.Background(), fake, messages, LlmCompactOptions{
		CharLimit: &limit,
		MaxDepth:  1,
	})
	if err == nil {
		t.Fatalf("expected a recursion-limit error")
	}
	if err.Error() != "LLM compaction recursion limit (1)" {
		t.Fatalf("error = %q", err.Error())
	}
}

// Parity-exempt: llm_compact.go has no dedicated Python test module.
func TestLlmCompactAppendsToolCallHistory(t *testing.T) {
	fake := &compactFake{respond: func(string, int) (string, error) { return "summary", nil }}
	messages := []Message{
		userMessage("find something"),
		{
			Role: RoleAssistant,
			ToolCalls: []map[string]any{{
				"id":   "c1",
				"type": "function",
				"function": map[string]any{
					"name":      "web_search",
					"arguments": `{"query": "hive"}`,
				},
			}},
		},
		{Role: RoleTool, Content: "found it", ToolUseID: stringPtr("c1")},
	}
	summary, err := LlmCompact(context.Background(), fake, messages, LlmCompactOptions{})
	if err != nil {
		t.Fatalf("LlmCompact: %v", err)
	}
	if !strings.Contains(summary, "TOOLS ALREADY CALLED:") || !strings.Contains(summary, "web_search (1x)") {
		t.Fatalf("summary lacks the tool history: %q", summary)
	}
}

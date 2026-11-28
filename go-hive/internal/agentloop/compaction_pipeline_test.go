package agentloop

// Deterministic coverage for the compaction fallback chain. Upstream's
// compact() runs truncate -> microcompact -> check -> prune -> check -> LLM
// summary, and only reaches the summarizer when the free stages cannot bring
// the conversation back under budget. Each test below isolates one stage and
// pins both the reclaimed transcript and the emitted events.
//
// Parity: framework/agent_loop/internals/compaction.py::compact

import (
	"context"
	"strings"
	"testing"
)

// seedCompactionToolResult appends an assistant tool call plus its stored
// result. `spillover` may be empty for a non-recoverable result.
func seedCompactionToolResult(t *testing.T, conv *NodeConversation, id, toolName, content, spillover string) {
	t.Helper()
	if _, err := conv.AddAssistantMessage("", []map[string]any{{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      toolName,
			"arguments": `{}`,
		},
	}}, AssistantMessageOptions{}); err != nil {
		t.Fatalf("AddAssistantMessage(%s): %v", id, err)
	}
	opts := ToolResultOptions{}
	if spillover != "" {
		path := spillover
		opts.SpilloverPath = &path
	}
	if _, err := conv.AddToolResult(id, content, opts); err != nil {
		t.Fatalf("AddToolResult(%s): %v", id, err)
	}
}

// renameStoredToolCall rewrites the function name of an already stored call.
// It lets a test build a result through the public path under a non-compactable
// name (so AddToolResult's proactive microcompaction stays out of the way) and
// then present it to the compact pass as a compactable tool.
func renameStoredToolCall(t *testing.T, conv *NodeConversation, id, name string) {
	t.Helper()
	for _, message := range conv.Messages() {
		for _, call := range message.ToolCalls {
			callID, _ := call["id"].(string)
			if callID != id {
				continue
			}
			fn, _ := call["function"].(map[string]any)
			if fn == nil {
				t.Fatalf("tool call %s has no function block", id)
			}
			fn["name"] = name
			return
		}
	}
	t.Fatalf("tool call %s not found", id)
}

func findCompactionToolMessage(conv *NodeConversation, id string) *Message {
	for _, message := range conv.Messages() {
		if message.Role == RoleTool && message.ToolUseID != nil && *message.ToolUseID == id {
			stored := message
			return &stored
		}
	}
	return nil
}

// Parity: framework/agent_loop/internals/compaction.py::compact (step 0)
//
// TestCompactionMicrocompactStageShortCircuitsLLM: step 0 clears compactable
// tool results and the budget is restored, so the summarizer is never called.
func TestCompactionMicrocompactStageShortCircuitsLLM(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{MaxContextTokens: 1000}, successToolExecutor)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    1000,
		CompactionThreshold: 0.5,
	})
	if _, err := conv.AddUserMessage("hello", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	seedCompactionToolResult(t, conv, "t1", "read_file", strings.Repeat("x", 2400), "/tmp/spill.txt")
	renameStoredToolCall(t, conv, "t1", "terminal_exec")
	if !conv.NeedsCompaction() {
		t.Fatal("test setup: conversation does not need compaction")
	}
	ratioBefore := conv.UsageRatio()
	messageCount := conv.MessageCount()
	// The pass must clear the only compactable result; the default keep-recent
	// window is 6, so a one-result fixture needs an explicit zero.
	previousKeepRecent := MicrocompactKeepRecent
	MicrocompactKeepRecent = 0
	t.Cleanup(func() { MicrocompactKeepRecent = previousKeepRecent })

	summarizer := &compactFake{respond: func(string, int) (string, error) {
		t.Fatal("summarizer called after microcompaction restored the budget")
		return "summary", nil
	}}
	var order []string
	var started []ContextCompactionStarted
	var compacted []ContextCompacted
	loop.OnContextCompactionStarted = func(payload ContextCompactionStarted) {
		order = append(order, "started")
		started = append(started, payload)
	}
	loop.OnContextCompacted = func(payload ContextCompacted) {
		order = append(order, "compacted")
		compacted = append(compacted, payload)
	}

	loop.compactConversationIfNeeded(context.Background(), judgeTestContext(summarizer, nil), conv, nil)

	if got := summarizer.callCount(); got != 0 {
		t.Fatalf("summarizer calls = %d, want 0", got)
	}
	if got := loop.Counter(compactionMicrocompactedCounter); got != 1 {
		t.Fatalf("stats[%s] = %d, want 1", compactionMicrocompactedCounter, got)
	}
	if got := loop.Counter(compactionPrunedCounter); got != 0 {
		t.Fatalf("stats[%s] = %d, want 0 (microcompaction short-circuits prune)", compactionPrunedCounter, got)
	}
	toolMessage := findCompactionToolMessage(conv, "t1")
	if toolMessage == nil || !strings.HasPrefix(toolMessage.Content, "Old tool result") {
		t.Fatalf("tool result = %#v, want the microcompact placeholder", toolMessage)
	}
	if len(started) != 1 || len(compacted) != 1 {
		t.Fatalf("events started/compacted = %d/%d, want 1/1", len(started), len(compacted))
	}
	if started[0].UsageBefore != compactionPercent(ratioBefore) {
		t.Fatalf("usage_before = %d, want %d", started[0].UsageBefore, compactionPercent(ratioBefore))
	}
	if started[0].MessageCount != messageCount {
		t.Fatalf("message_count = %d, want %d", started[0].MessageCount, messageCount)
	}
	if started[0].LLMCompactionSkipped {
		t.Fatal("llm_compaction_skipped = true, want false with a closed breaker")
	}
	if compacted[0].Level != CompactionLevel(ratioBefore, conv.UsageRatio()) {
		t.Fatalf("level = %q, want %q", compacted[0].Level, CompactionLevel(ratioBefore, conv.UsageRatio()))
	}
	if strings.Join(order, ",") != "started,compacted" {
		t.Fatalf("event order = %v, want started then compacted", order)
	}
}

// Parity: framework/agent_loop/internals/compaction.py::compact (step 1)
//
// TestCompactionPruneStageShortCircuitsLLM: microcompaction cannot touch a
// non-compactable result, so step 1 prunes the oldest recoverable results and
// the budget is restored before the summarizer.
func TestCompactionPruneStageShortCircuitsLLM(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{MaxContextTokens: 24000}, successToolExecutor)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    24000,
		CompactionThreshold: 0.2,
	})
	if _, err := conv.AddUserMessage("hello", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	// Two equally sized recoverable results: the newest absorbs the protection
	// budget, the older one is pruneable.
	seedCompactionToolResult(t, conv, "old", "web_search", strings.Repeat("o", 12000), "/tmp/old.txt")
	seedCompactionToolResult(t, conv, "new", "web_search", strings.Repeat("n", 12000), "/tmp/new.txt")
	if !conv.NeedsCompaction() {
		t.Fatal("test setup: conversation does not need compaction")
	}

	summarizer := &compactFake{respond: func(string, int) (string, error) {
		t.Fatal("summarizer called after pruning restored the budget")
		return "summary", nil
	}}
	started := 0
	compacted := 0
	loop.OnContextCompactionStarted = func(ContextCompactionStarted) { started++ }
	loop.OnContextCompacted = func(ContextCompacted) { compacted++ }

	loop.compactConversationIfNeeded(context.Background(), judgeTestContext(summarizer, nil), conv, nil)

	if got := summarizer.callCount(); got != 0 {
		t.Fatalf("summarizer calls = %d, want 0", got)
	}
	if got := loop.Counter(compactionMicrocompactedCounter); got != 0 {
		t.Fatalf("stats[%s] = %d, want 0 (web_search is not compactable)", compactionMicrocompactedCounter, got)
	}
	if got := loop.Counter(compactionPrunedCounter); got != 1 {
		t.Fatalf("stats[%s] = %d, want 1", compactionPrunedCounter, got)
	}
	oldMessage := findCompactionToolMessage(conv, "old")
	if oldMessage == nil || !strings.HasPrefix(oldMessage.Content, "Pruned tool result") {
		t.Fatalf("old result = %#v, want the prune placeholder", oldMessage)
	}
	newMessage := findCompactionToolMessage(conv, "new")
	if newMessage == nil || !strings.Contains(newMessage.Content, strings.Repeat("n", 12000)) {
		t.Fatalf("new result was altered: %#v", newMessage)
	}
	if started != 1 || compacted != 1 {
		t.Fatalf("events started/compacted = %d/%d, want 1/1", started, compacted)
	}
}

// Parity: framework/agent_loop/internals/compaction.py::compact (step 2)
//
// TestCompactionFallsThroughToLLMWhenFreeStagesCannotReclaim: a non-recoverable
// user message is invisible to microcompaction and pruning, so the pass reaches
// the summarizer — the fallback the port used to enter directly.
func TestCompactionFallsThroughToLLMWhenFreeStagesCannotReclaim(t *testing.T) {
	loop := compactionTestLoop()
	conv := compactionTestConversation(t)
	summarizer := &compactFake{respond: func(string, int) (string, error) {
		return "the summary", nil
	}}
	started := 0
	compacted := 0
	loop.OnContextCompactionStarted = func(ContextCompactionStarted) { started++ }
	loop.OnContextCompacted = func(ContextCompacted) { compacted++ }

	loop.compactConversationIfNeeded(context.Background(), judgeTestContext(summarizer, nil), conv, nil)

	if got := summarizer.callCount(); got != 1 {
		t.Fatalf("summarizer calls = %d, want 1", got)
	}
	if got := loop.Counter(compactionMicrocompactedCounter); got != 0 {
		t.Fatalf("stats[%s] = %d, want 0", compactionMicrocompactedCounter, got)
	}
	if got := loop.Counter(compactionPrunedCounter); got != 0 {
		t.Fatalf("stats[%s] = %d, want 0", compactionPrunedCounter, got)
	}
	messages := conv.Messages()
	if len(messages) != 1 || messages[0].Content != "the summary" {
		t.Fatalf("messages = %#v, want the single summary", messages)
	}
	if started != 1 || compacted != 1 {
		t.Fatalf("events started/compacted = %d/%d, want 1/1", started, compacted)
	}
}

// Parity: framework/agent_loop/internals/compaction.py::compact (circuit breaker)
//
// TestCompactionStartedReportsOpenCircuit: after repeated failures the breaker
// suppresses only the LLM stage; the pass still emits both events and finishes
// with the conversation left intact.
func TestCompactionStartedReportsOpenCircuit(t *testing.T) {
	loop := compactionTestLoop()
	conv := compactionTestConversation(t)
	failing := &compactFake{respond: func(string, int) (string, error) {
		return "", context.DeadlineExceeded
	}}
	agentCtx := judgeTestContext(failing, nil)
	var started []ContextCompactionStarted
	compacted := 0
	loop.OnContextCompactionStarted = func(payload ContextCompactionStarted) {
		started = append(started, payload)
	}
	loop.OnContextCompacted = func(ContextCompacted) { compacted++ }

	for i := 0; i < DefaultMaxConsecutiveCompactionFailures; i++ {
		loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))
	}
	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, NewReminderAgentContext(agentCtx))

	if got := failing.callCount(); got != DefaultMaxConsecutiveCompactionFailures {
		t.Fatalf("summarizer calls = %d, want %d (breaker must suppress the 4th)", got, DefaultMaxConsecutiveCompactionFailures)
	}
	if len(started) != DefaultMaxConsecutiveCompactionFailures+1 {
		t.Fatalf("started events = %d, want %d", len(started), DefaultMaxConsecutiveCompactionFailures+1)
	}
	for i, payload := range started[:DefaultMaxConsecutiveCompactionFailures] {
		if payload.LLMCompactionSkipped {
			t.Fatalf("started[%d].LLMCompactionSkipped = true before the breaker opened", i)
		}
	}
	if !started[len(started)-1].LLMCompactionSkipped {
		t.Fatal("started event did not report the open breaker")
	}
	if compacted != DefaultMaxConsecutiveCompactionFailures+1 {
		t.Fatalf("compacted events = %d, want %d", compacted, DefaultMaxConsecutiveCompactionFailures+1)
	}
	if got := loop.Counter(compactionCircuitOpenCounter); got != 1 {
		t.Fatalf("stats[%s] = %d, want 1", compactionCircuitOpenCounter, got)
	}
}

// Package hive_test is the external consumer of the public SDK. It imports
// the SDK by its public path only; if the facade ever needs an internal
// import to be usable, this file stops compiling.
package hive_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/hive"
)

// scriptedModel plays one scripted completion per call and remembers the
// requests it saw.
type scriptedModel struct {
	turns    []hive.Completion
	requests []hive.CompletionRequest
}

func (m *scriptedModel) AComplete(_ context.Context, req hive.CompletionRequest) (hive.Completion, error) {
	m.requests = append(m.requests, req)
	index := len(m.requests) - 1
	if index >= len(m.turns) {
		index = len(m.turns) - 1
	}
	return m.turns[index], nil
}

// Parity: core/tests/test_public_api.py::test_on_report_receives_the_report_without_a_worker_stream
func TestConsumerRunsAMinimalLoop(t *testing.T) {
	model := &scriptedModel{turns: []hive.Completion{{Content: "It is raining.", StopReason: "stop"}}}
	loop := hive.NewLoop(hive.DefaultLoopConfig(), nil)
	conv := hive.NewConversation("You are a test assistant.")
	ctx := &hive.AgentContext{
		AgentID:   "scout",
		AgentSpec: hive.NewAgentSpec("scout", "Scout", "Looks around"),
		LLM:       model,
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("result.Success = false, want true (%+v)", result)
	}
	if result.FinalText != "It is raining." {
		t.Fatalf("FinalText = %q, want %q", result.FinalText, "It is raining.")
	}
	if result.InnerTurns != 1 {
		t.Fatalf("InnerTurns = %d, want 1", result.InnerTurns)
	}
	if conv.MessageCount() < 2 {
		t.Fatalf("conversation has %d messages, want the task frame and the answer", conv.MessageCount())
	}
	if len(model.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.requests))
	}
}

// Parity: core/tests/test_event_loop_node.py::test_tool_execution_feedback
func TestConsumerExecutesAToolThenAnswers(t *testing.T) {
	model := &scriptedModel{turns: []hive.Completion{
		{ToolCalls: []hive.ToolCall{{ID: "call-1", Name: "weather", Arguments: "{}"}}, StopReason: "tool_calls"},
		{Content: "It is raining.", StopReason: "stop"},
	}}
	var executed []string
	loop := hive.NewLoop(hive.LoopConfig{MaxIterations: 4}, func(use hive.ToolUse) (hive.ToolResult, error) {
		executed = append(executed, use.Name)
		return hive.ToolResult{ToolUseID: use.ID, Content: "rain"}, nil
	})
	conv := hive.NewConversation("Use the weather tool.")
	ctx := &hive.AgentContext{
		AgentID:   "scout",
		AgentSpec: hive.NewAgentSpec("scout", "Scout", "Checks weather"),
		LLM:       model,
		Tools:     []hive.Tool{hive.NewTool("weather", "Current weather")},
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(executed) != 1 || executed[0] != "weather" {
		t.Fatalf("executed tools = %v, want [weather]", executed)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].ToolName != "weather" {
		t.Fatalf("ToolResults = %+v, want one weather record", result.ToolResults)
	}
	if result.FinalText != "It is raining." {
		t.Fatalf("FinalText = %q, want the answer after the tool batch", result.FinalText)
	}
}

// Parity: core/tests/test_event_bus.py::TestSubscriptionManagement
func TestConsumerSubscribesToEvents(t *testing.T) {
	bus := hive.NewEventBus()
	received := make(chan *hive.Event, 1)
	id := bus.Subscribe([]hive.EventType{hive.EventToolCallStarted}, func(event *hive.Event) {
		received <- event
	}, hive.EventFilter{StreamID: "stream-1"})
	if id == "" {
		t.Fatal("Subscribe returned an empty subscription id")
	}
	event := hive.NewEvent(hive.EventToolCallStarted, "stream-1")
	event.Data["tool_name"] = "weather"
	bus.Publish(event)
	select {
	case got := <-received:
		if got.Data["tool_name"] != "weather" {
			t.Fatalf("event data = %#v", got.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called")
	}
	if !bus.Unsubscribe(id) {
		t.Fatal("Unsubscribe reported a missing subscription")
	}
}

// Parity: core/tests/test_tool_registry.py::test_unknown_tool_error_returns_proper_result
func TestConsumerUsesTheToolRegistry(t *testing.T) {
	registry := hive.NewToolRegistry()
	registry.Register(hive.Tool{Name: "echo", Description: "Echoes the input"}, func(inputs map[string]any) (any, error) {
		return inputs["text"], nil
	})
	if _, ok := registry.Get("echo"); !ok {
		t.Fatal("registered tool missing from the registry")
	}
	result, err := registry.Execute(context.Background(), hive.ToolUse{ID: "u1", Name: "echo", Input: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != "hi" {
		t.Fatalf("tool result = %q, want hi", result.Content)
	}
	missing, err := registry.Execute(context.Background(), hive.ToolUse{ID: "u2", Name: "nope"})
	if err != nil {
		t.Fatalf("Execute(unknown): %v", err)
	}
	if !missing.IsError {
		t.Fatalf("unknown tool result = %+v, want an error result", missing)
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports
func TestConsumerConstructsAColony(t *testing.T) {
	home, err := hive.HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	colony, err := hive.NewColony(hive.ColonyOptions{
		Home:      home,
		ID:        "sdk-colony",
		AgentSpec: hive.NewAgentSpec("colony", "Colony", "Test colony"),
		LLM:       hive.NewMockProvider(),
	})
	if err != nil {
		t.Fatalf("NewColony: %v", err)
	}
	defer colony.Stop()
	if colony.ID() != "sdk-colony" {
		t.Fatalf("colony id = %q", colony.ID())
	}
	if colony.Events() == nil {
		t.Fatal("colony event bus is nil")
	}
	if workers := colony.Workers(); len(workers) != 0 {
		t.Fatalf("workers = %+v, want none before a spawn", workers)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "sdk-colony", "metadata.json")); err != nil {
		t.Fatalf("colony metadata not materialised: %v", err)
	}
}

// Parity: core/tests/test_public_api.py::test_every_primitive_resolves
func TestConsumerReachesEveryDocumentedPrimitive(t *testing.T) {
	if hive.Version == "" || hive.UpstreamVersion == "" {
		t.Fatal("version constants are empty")
	}
	if hive.DefaultLoopConfig().MaxIterations <= 0 {
		t.Fatal("DefaultLoopConfig has no iteration budget")
	}
	home, err := hive.HomeAt(t.TempDir())
	if err != nil {
		t.Fatalf("HomeAt: %v", err)
	}
	if hive.PreferredModel(home) == "" {
		t.Fatal("PreferredModel returned an empty model")
	}
	if hive.ListQueens(hive.Home{}) != nil {
		t.Fatal("ListQueens on a zero Home should be nil")
	}
	if hive.NewEventBus().SubscriptionCount() != 0 {
		t.Fatal("a fresh bus has subscriptions")
	}
	if hive.NewToolRegistry().Names() != nil {
		t.Fatal("a fresh registry has tools")
	}
	if hive.NewSkillCatalog(nil).Count() != 0 {
		t.Fatal("an empty catalog has skills")
	}
}

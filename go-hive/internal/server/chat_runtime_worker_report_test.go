package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity-exempt: the colony-phase gating of run_worker is a tool-surface
// assertion this port makes on the chat runtime (upstream gates through the
// phase registry, which the Go chat session does not model).
func TestChatRuntimeHidesRunWorkerWithoutColony(t *testing.T) {
	runtime, _ := newTestChatRuntime(t, t.TempDir())
	runtime.tools = append(runtime.tools, llm.ToolDef{Name: colony.RunWorkerToolName})

	if queenToolNameSet(runtime.toolsForTurn())[colony.RunWorkerToolName] {
		t.Fatal("run_worker advertised to a session with no colony")
	}

	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		EventBus: eventbus.NewEventBus(),
		StreamID: "colony_alpha",
	})
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})
	if !queenToolNameSet(runtime.toolsForTurn())[colony.RunWorkerToolName] {
		t.Fatal("run_worker missing after the colony bound")
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_tool_returns_immediately_and_emits_reports
func TestChatRuntimeInjectsWorkerReportAsQueenTurn(t *testing.T) {
	runtime, _ := newTestChatRuntime(t, t.TempDir())
	bus := eventbus.NewEventBus()
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{EventBus: bus, StreamID: "colony_alpha"})
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})
	t.Cleanup(func() { runtime.BindColony(nil) })

	event := eventbus.NewAgentEvent(eventbus.EventSubagentReport, "worker:1")
	event.Data = map[string]any{
		"worker_id":  "worker:1",
		"batch_id":   "batch_1",
		"batch_size": 1,
		"status":     "success",
		"summary":    "wrote two rows",
		"task":       "fill rows",
	}
	bus.Publish(event)

	content := waitForTranscriptContent(t, runtime, "[WORKER_REPORT]", 5*time.Second)
	if !strings.Contains(content, "[External event]") {
		t.Fatalf("worker report was not delivered as an external event turn:\n%s", content)
	}
	if !strings.Contains(content, "<summary>wrote two rows</summary>") {
		t.Fatalf("report summary missing:\n%s", content)
	}
	if !strings.Contains(content, "<batch_remaining>0</batch_remaining>") {
		t.Fatalf("batch metadata missing:\n%s", content)
	}
}

// Parity-exempt: internal wiring assertion. The executor context carries the
// concrete colony runtime so the catalogue's run_worker call reaches
// SpawnBatchWithOptions even though the catalogue was built before binding.
func TestChatRuntimeRunWorkerExecutorReceivesColony(t *testing.T) {
	var seen colony.RunWorkerSpawner
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &fakeChatLLM{},
		Tools: []llm.ToolDef{{
			Name:        colony.RunWorkerToolName,
			Description: "dispatch",
			Parameters:  map[string]any{"type": "object"},
		}},
		Execute: func(ctx context.Context, name string, input map[string]any) (string, bool, error) {
			seen = colony.RunWorkerSpawnerFromContext(ctx)
			return "ok", false, nil
		},
		Root: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		EventBus: eventbus.NewEventBus(),
		StreamID: "colony_alpha",
	})
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})

	if _, err := runtime.executeTool(agentloop.ToolCallRequest{Name: colony.RunWorkerToolName}); err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if seen == nil {
		t.Fatal("run_worker executor context carried no colony runtime")
	}

	runtime.BindColony(nil)
	if _, err := runtime.executeTool(agentloop.ToolCallRequest{Name: colony.RunWorkerToolName}); err != nil {
		t.Fatalf("executeTool unbind: %v", err)
	}
	if seen != nil {
		t.Fatal("run_worker context still carried a colony after unbind")
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_tool_returns_immediately_and_emits_reports
func TestChatRuntimeRunWorkerEndToEndDispatchAndReport(t *testing.T) {
	bus := eventbus.NewEventBus()
	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.AgentSpec{ID: "worker", Name: "Worker", AgentType: "event_loop"},
		LLM:         reportingWorkerLLM{},
		EventBus:    bus,
		StreamID:    "colony_alpha",
		StoragePath: t.TempDir(),
	})
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &fakeChatLLM{},
		Tools: []llm.ToolDef{
			colony.RunWorkerToolDef(),
			{Name: "read_file", Description: "read"},
		},
		Execute: func(ctx context.Context, name string, input map[string]any) (string, bool, error) {
			if name == colony.RunWorkerToolName {
				content, isError := colony.RunWorker(ctx, colony.RunWorkerSpawnerFromContext(ctx), input)
				return content, isError, nil
			}
			return "ok", false, nil
		},
		Root: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})

	result, err := runtime.executeTool(agentloop.ToolCallRequest{
		Name: colony.RunWorkerToolName,
		Input: map[string]any{
			"tasks": []any{map[string]any{"task": "write one row"}},
		},
	})
	if err != nil {
		t.Fatalf("run_worker: %v", err)
	}
	if result.IsError {
		t.Fatalf("run_worker failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"status":"started"`) {
		t.Fatalf("run_worker result = %s", result.Content)
	}

	content := waitForTranscriptContent(t, runtime, "[WORKER_REPORT]", 5*time.Second)
	if !strings.Contains(content, "<status>success</status>") {
		t.Fatalf("worker did not report success:\n%s", content)
	}
	if !strings.Contains(content, "<batch_remaining>0</batch_remaining>") {
		t.Fatalf("batch_remaining missing:\n%s", content)
	}
}

// colonyHostStub mirrors the CLI colony host seam: the server sees an opaque
// ColonyRuntime, and the concrete runtime is reachable through Runtime().
type colonyHostStub struct {
	runtime *colony.ColonyRuntime
}

func (s colonyHostStub) StreamID() string { return s.runtime.StreamID() }

func (s colonyHostStub) SubscribeToEvents(eventTypes []eventbus.EventType, handler eventbus.EventHandler, opts ...eventbus.SubscribeOption) (string, error) {
	return s.runtime.SubscribeToEvents(eventTypes, handler, opts...)
}

func (s colonyHostStub) InjectInput(workerID, content string) error {
	return s.runtime.InjectInput(workerID, content)
}

func (s colonyHostStub) Runtime() *colony.ColonyRuntime { return s.runtime }

func queenToolNameSet(defs []llm.ToolDef) map[string]bool {
	names := make(map[string]bool, len(defs))
	for _, def := range defs {
		names[def.Name] = true
	}
	return names
}

// reportingWorkerLLM scripts the colony worker's model: a worker ends by
// reporting, so a text-only answer would now be auto-failed as a stall.
type reportingWorkerLLM struct{}

func (reportingWorkerLLM) ModelName() string { return "reporting-worker" }

func (reportingWorkerLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{
		ToolCalls: []llm.ToolCall{{
			ID:        "report_1",
			Name:      "report_to_parent",
			Arguments: `{"status":"success","summary":"wrote one row"}`,
		}},
		StopReason: "tool_calls",
	}, nil
}

func waitForTranscriptContent(t *testing.T, runtime *ChatRuntime, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var joined string
	for time.Now().Before(deadline) {
		parts, err := runtime.Transcript()
		if err != nil {
			t.Fatalf("Transcript: %v", err)
		}
		var builder strings.Builder
		for _, part := range parts {
			builder.WriteString(contentOf(part))
			builder.WriteString("\n")
		}
		joined = builder.String()
		if strings.Contains(joined, needle) {
			return joined
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; transcript = %s", needle, joined)
	return ""
}

func contentOf(part map[string]any) string {
	content, _ := part["content"].(string)
	return content
}

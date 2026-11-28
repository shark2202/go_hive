package colony

// P0-1: a worker that exits its turn-loop cleanly without ever calling
// report_to_parent is reported as `partial` (data.no_explicit_report), never
// as a plain success.
//
// Parity: framework/host/worker.py::Worker._build_result (outcome 2)
// Parity: framework/host/worker.py::Worker._emit_terminal_events

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// askUserOnlyLLM calls ask_user and never reports. A successful ask_user
// stops the turn (upstream's user_input_requested early return), so the loop
// exits cleanly without any report_to_parent call — the P0-1 backstop.
type askUserOnlyLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *askUserOnlyLLM) ModelName() string { return "mock-ask-user-only" }

func (m *askUserOnlyLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return llm.Completion{
		ToolCalls: []llm.ToolCall{{
			ID:        "ask_1",
			Name:      agentloop.AskUserToolName,
			Arguments: `{"question":"proceed?"}`,
		}},
		StopReason: "tool_calls",
	}, nil
}

func (m *askUserOnlyLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// Parity: framework/host/worker.py::Worker._build_result (outcome 2)
func TestWorkerWithoutExplicitReportSynthesizesPartial(t *testing.T) {
	provider := &askUserOnlyLLM{}
	bus := eventbus.NewEventBus()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{{Name: agentloop.AskUserToolName, Description: "ask_user", Parameters: map[string]any{"type": "object"}}},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus:             bus,
		StreamID:             "p0_1_colony",
		MaxConcurrentWorkers: 2,
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	reports := collectEvents(bus, eventbus.EventSubagentReport)

	const task = "summarize the ledger without reporting"
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: task}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil")
	}
	if !eventually(t, 10*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatalf("worker never terminated; model calls = %d", provider.callCount())
	}

	// The loop exits cleanly (iteration budget), so the runtime status stays
	// COMPLETED; the report is what says partial.
	if worker.Status() != WorkerCompleted {
		errText := "<nil>"
		if err := worker.Result().Error; err != nil {
			errText = *err
		}
		t.Fatalf("worker status = %s, want completed (result error: %s)", worker.Status(), errText)
	}
	result := worker.Result()
	if result.ReportStatus != "partial" {
		t.Fatalf("result report status = %q, want partial", result.ReportStatus)
	}
	wantSummary := "Worker terminated without calling report_to_parent. Task: '" + task +
		"'. Iteration / grace budget exhausted, or the agent yielded an empty turn."
	if result.Summary != wantSummary {
		t.Fatalf("result summary = %q, want %q", result.Summary, wantSummary)
	}
	if result.Data["no_explicit_report"] != true {
		t.Fatalf("result data = %#v, want no_explicit_report=true", result.Data)
	}

	if !eventually(t, 5*time.Second, func() bool { return reports.count() == 1 }) {
		t.Fatalf("SUBAGENT_REPORT events = %d, want 1", reports.count())
	}
	payload := reports.snapshot()[0].Data
	if payload["status"] != "partial" {
		t.Fatalf("report status = %v, want partial", payload["status"])
	}
	if payload["summary"] != wantSummary {
		t.Fatalf("report summary = %q, want %q", payload["summary"], wantSummary)
	}
	data, _ := payload["data"].(map[string]any)
	if data["no_explicit_report"] != true {
		t.Fatalf("report data = %#v, want no_explicit_report=true", payload["data"])
	}
	if _, hasText := data["text"]; !hasText {
		t.Fatalf("report data = %#v, want the worker output under text", payload["data"])
	}

	// The wait path (queen batch collection / playbook) reads the terminal
	// report too: it must expose the synthesized partial, not the payload's
	// "success" default.
	waited, err := runtime.WaitForWorkerReports(context.Background(), ids, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForWorkerReports: %v", err)
	}
	if len(waited) != 1 {
		t.Fatalf("WaitForWorkerReports returned %d reports, want 1", len(waited))
	}
	if waited[0]["status"] != "partial" {
		t.Fatalf("waited status = %v, want partial", waited[0]["status"])
	}
	if waited[0]["summary"] != wantSummary {
		t.Fatalf("waited summary = %q, want %q", waited[0]["summary"], wantSummary)
	}
	waitedData, _ := waited[0]["data"].(map[string]any)
	if waitedData["no_explicit_report"] != true {
		t.Fatalf("waited data = %#v, want no_explicit_report=true", waited[0]["data"])
	}

	// A long task is truncated to upstream's task[:80] in the summary.
	longTask := strings.Repeat("x", 100)
	runtime2 := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		LLM:         &askUserOnlyLLM{},
		Tools:       []llm.ToolDef{{Name: agentloop.AskUserToolName}},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: bus,
		StreamID: "p0_1_colony_long",
	})
	if err := runtime2.Start(context.Background()); err != nil {
		t.Fatalf("Start(long): %v", err)
	}
	t.Cleanup(runtime2.Stop)
	longIDs, err := runtime2.SpawnBatch(context.Background(), []BatchTask{{Task: longTask}})
	if err != nil {
		t.Fatalf("SpawnBatch(long): %v", err)
	}
	longWorker := runtime2.GetWorker(longIDs[0])
	if !eventually(t, 10*time.Second, func() bool { return longWorker != nil && !longWorker.IsActive() }) {
		t.Fatal("long-task worker never terminated")
	}
	if !strings.Contains(longWorker.Result().Summary, "'"+longTask[:80]+"'") {
		t.Fatalf("long-task summary = %q, want the 80-char task slice", longWorker.Result().Summary)
	}
}

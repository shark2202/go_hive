package colony

// Parallel worker text-only stall auto-fail, colony half.
//
// A worker that produces text-only turns past the grace bound is auto-failed
// with a synthesized report_to_parent(status='failed') — it never emits
// ESCALATION_REQUESTED and never burns its whole iteration budget retrying.
//
// Parity: core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (6h worker stall)

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// textOnlyWorkerLLM never calls a tool: every worker turn is plain text.
// Mirrors the upstream _AlwaysTextOnlyLLM mock.
type textOnlyWorkerLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *textOnlyWorkerLLM) ModelName() string { return "mock-text-only" }

func (m *textOnlyWorkerLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return llm.Completion{Content: "Hmm, let me think...", StopReason: "stop"}, nil
}

func (m *textOnlyWorkerLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// Parity: core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report
func TestParallelWorkerStallAutoFailsWithSyntheticReport(t *testing.T) {
	provider := &textOnlyWorkerLLM{}
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)
	reports := collectEvents(bus, eventbus.EventSubagentReport)
	escalations := collectEvents(bus, eventbus.EventEscalationRequested)

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "do something"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("SpawnBatch returned %d ids, want 1", len(ids))
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}
	if !eventually(t, 10*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatalf("worker never terminated; model calls = %d", provider.callCount())
	}

	// Bounded: two grace turns tolerated, the third auto-fails. The worker
	// must not keep re-calling the model until its iteration budget runs out.
	if got := provider.callCount(); got != 3 {
		t.Fatalf("model calls = %d, want 3 (grace 2 + the failing turn)", got)
	}

	// The worker terminated cleanly (success=True) — its REPORT says failed.
	if worker.Status() != WorkerCompleted {
		t.Fatalf("worker status = %s, want completed (the report carries the failure)", worker.Status())
	}
	report := worker.ExplicitReport()
	if report == nil || report["status"] != "failed" {
		t.Fatalf("worker report = %#v, want status failed", report)
	}
	summary, _ := report["summary"].(string)
	if !strings.Contains(strings.ToLower(summary), "auto-failed") || !strings.Contains(summary, "text-only") {
		t.Fatalf("report summary = %q, want the auto-fail stall reason", summary)
	}
	data, _ := report["data"].(map[string]any)
	if data["auto_fail_reason"] != "stall_text_only" {
		t.Fatalf("report data = %#v, want auto_fail_reason=stall_text_only", report["data"])
	}

	// The queen sees exactly one [WORKER_REPORT] with the failed status.
	if !eventually(t, 5*time.Second, func() bool { return reports.count() == 1 }) {
		t.Fatalf("SUBAGENT_REPORT events = %d, want 1", reports.count())
	}
	payload := reports.snapshot()[0].Data
	if payload["status"] != "failed" {
		t.Fatalf("report event status = %v, want failed", payload["status"])
	}
	eventData, _ := payload["data"].(map[string]any)
	if eventData["auto_fail_reason"] != "stall_text_only" {
		t.Fatalf("report event data = %#v, want the auto_fail_reason", payload["data"])
	}

	// Critical BRD invariant: a stalled parallel worker must NOT escalate.
	if got := escalations.count(); got != 0 {
		t.Fatalf("ESCALATION_REQUESTED events = %d, want 0 for a stalled parallel worker", got)
	}

	if loop := worker.AgentLoop(); loop != nil {
		if got := loop.Counter("worker_stall_auto_failed"); got != 1 {
			t.Fatalf("worker_stall_auto_failed = %d, want 1", got)
		}
		if got := loop.Counter("worker_stall_escalated"); got != 0 {
			t.Fatalf("worker_stall_escalated = %d, want 0", got)
		}
	}
}

// Parity-exempt: the negative half — a worker that keeps calling tools is
// making progress and must never be auto-failed.
func TestWorkerWithToolCallsIsNotAutoFailed(t *testing.T) {
	provider := newScriptedLLM(
		toolResponse("t1", "task_list", `{}`),
		reportResponse("success", "kept working", nil),
	)
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)
	escalations := collectEvents(bus, eventbus.EventEscalationRequested)

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "keep working"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if report := worker.ExplicitReport(); report == nil || report["status"] != "success" {
		t.Fatalf("worker report = %#v, want the model's success report", report)
	}
	if got := provider.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (work turn + report)", got)
	}
	if got := escalations.count(); got != 0 {
		t.Fatalf("escalations = %d, want none", got)
	}
	if loop := worker.AgentLoop(); loop != nil && loop.Counter("worker_stall_auto_failed") != 0 {
		t.Fatalf("worker_stall_auto_failed = %d, want 0", loop.Counter("worker_stall_auto_failed"))
	}
}

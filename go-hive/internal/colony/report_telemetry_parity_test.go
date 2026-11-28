package colony

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// A21: every terminal report — success, failed, stopped — carries the same
// telemetry block Python's Worker._emit_terminal_events publishes
// (tool_calls_used, budget_limited, budget_pinned, tool_call_lifetime_budget,
// tokens_used, duration_seconds, output_file). Before this, the failed and
// stopped maps dropped all of them except duration.
//
// Parity: framework/host/worker.py::Worker._emit_terminal_events (SUBAGENT_REPORT)
func TestTerminalReportsCarryTelemetryOnEveryOutcome(t *testing.T) {
	required := []string{
		"tool_calls_used",
		"budget_limited",
		"budget_pinned",
		"tool_call_lifetime_budget",
		"tokens_used",
		"duration_seconds",
		"output_file",
	}
	assertTelemetry := func(t *testing.T, report map[string]any, status string) {
		t.Helper()
		if report["status"] != status {
			t.Fatalf("report status = %v, want %s", report["status"], status)
		}
		for _, key := range required {
			if _, ok := report[key]; !ok {
				t.Errorf("%s report is missing %q: %#v", status, key, report)
			}
		}
	}

	t.Run("failed", func(t *testing.T) {
		bus := eventbus.NewEventBus()
		runtime := makeTestColony(t, &crashingLLM{}, bus)
		events := collectEvents(bus, eventbus.EventSubagentReport)
		ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "crash please"}})
		if err != nil {
			t.Fatalf("SpawnBatch: %v", err)
		}
		worker := runtime.GetWorker(ids[0])
		if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
			t.Fatalf("worker status = %s, want terminal", worker.Status())
		}
		if !eventually(t, 2*time.Second, func() bool { return len(events.snapshot()) > 0 }) {
			t.Fatal("no SUBAGENT_REPORT")
		}
		assertTelemetry(t, events.snapshot()[0].Data, "failed")
		if message, _ := events.snapshot()[0].Data["error"].(string); !strings.Contains(message, "boom") {
			t.Fatalf("failed report error = %v, want the crash text", events.snapshot()[0].Data["error"])
		}
	})

	t.Run("stopped", func(t *testing.T) {
		provider := newGateLLM("slow unit")
		bus := eventbus.NewEventBus()
		runtime := makeTestColony(t, provider, bus)
		events := collectEvents(bus, eventbus.EventSubagentReport)
		ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "slow unit"}})
		if err != nil {
			t.Fatalf("SpawnBatch: %v", err)
		}
		worker := runtime.GetWorker(ids[0])
		if !eventually(t, 5*time.Second, func() bool { return provider.totalCalls() > 0 }) {
			t.Fatal("worker never reached the model")
		}
		if !runtime.StopWorker(ids[0]) {
			t.Fatal("StopWorker refused a live worker")
		}
		if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
			t.Fatalf("worker status = %s, want terminal", worker.Status())
		}
		if !eventually(t, 2*time.Second, func() bool { return len(events.snapshot()) > 0 }) {
			t.Fatal("no SUBAGENT_REPORT")
		}
		assertTelemetry(t, events.snapshot()[0].Data, "stopped")
	})

	t.Run("success", func(t *testing.T) {
		bus := eventbus.NewEventBus()
		runtime := makeTestColony(t, newScriptedLLM(reportResponse("success", "done", nil)), bus)
		events := collectEvents(bus, eventbus.EventSubagentReport)
		ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "finish"}})
		if err != nil {
			t.Fatalf("SpawnBatch: %v", err)
		}
		worker := runtime.GetWorker(ids[0])
		if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
			t.Fatalf("worker status = %s, want terminal", worker.Status())
		}
		if !eventually(t, 2*time.Second, func() bool { return len(events.snapshot()) > 0 }) {
			t.Fatal("no SUBAGENT_REPORT")
		}
		assertTelemetry(t, events.snapshot()[0].Data, "success")
	})
}

// A22: the wait path force-stops a worker whose deadline expires and returns
// that worker's own stopped result, instead of the old synthetic
// failed/timeout entry that left the worker running.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.wait_for_worker_reports
// (timeout branch: stop_worker -> worker._result -> last_assistant_excerpt)
func TestWaitForWorkerReportsTimeoutsStopTheWorker(t *testing.T) {
	provider := newGateLLM("hanging worker")
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "hanging worker"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if !eventually(t, 5*time.Second, func() bool { return provider.totalCalls() > 0 }) {
		t.Fatal("worker never reached the model")
	}
	worker := runtime.GetWorker(ids[0])
	reports, err := runtime.WaitForWorkerReports(context.Background(), ids, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForWorkerReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	report := reports[0]
	if report["status"] != "stopped" {
		t.Fatalf("timeout report status = %v, want the worker's own stopped status (%#v)", report["status"], report)
	}
	if report["error"] == nil {
		t.Fatalf("timeout report error = nil, want the worker's cancel error (%#v)", report)
	}
	if _, ok := report["tokens_used"]; !ok {
		t.Fatalf("timeout report is missing tokens_used: %#v", report)
	}
	if !worker.Status().Done() || worker.IsActive() {
		t.Fatalf("worker status = %s after the timeout wait, want a terminal stop", worker.Status())
	}
}

// A22 (enrichment): a timed-out worker that had produced assistant text gets
// its last non-empty assistant turn attached, capped at 500 code points.
//
// Parity: framework/host/colony_runtime.py::_tail_last_assistant_message
func TestLastAssistantExcerptCapsAndSkipsEmptyTurns(t *testing.T) {
	conv := agentloop.NewNodeConversation("system")
	if _, err := conv.AddUserMessage("do the thing", agentloop.UserMessageOptions{}); err != nil {
		t.Fatalf("add user: %v", err)
	}
	if _, err := conv.AddAssistantMessage("I was working on the ledger", nil, agentloop.AssistantMessageOptions{}); err != nil {
		t.Fatalf("add assistant: %v", err)
	}
	worker := NewWorker("worker_tail", "tail", "worker:worker_tail")
	worker.conversation = conv
	if got := lastAssistantExcerpt(worker); got != "I was working on the ledger" {
		t.Fatalf("excerpt = %q", got)
	}

	long := strings.Repeat("x", stopTailMaxChars+50)
	conv2 := agentloop.NewNodeConversation("system")
	if _, err := conv2.AddAssistantMessage(long, nil, agentloop.AssistantMessageOptions{}); err != nil {
		t.Fatalf("add long assistant: %v", err)
	}
	worker.conversation = conv2
	got := lastAssistantExcerpt(worker)
	if len([]rune(got)) != stopTailMaxChars+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long excerpt = %d runes, want %d + ellipsis", len([]rune(got)), stopTailMaxChars)
	}
}

// A23: a transcript the retention janitor tombstoned cannot be resumed; the
// tombstone lands before the delete, so it is the guard that closes the race.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
// (result.json._janitor guard)
func TestResumeWorkersRefusesJanitorTombstone(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(reportResponse("success", "first pass", nil)), eventbus.NewEventBus())
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "finish the ledger"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
		t.Fatalf("worker status = %s, want terminal", worker.Status())
	}
	resultPath := filepath.Join(runtime.workerStorage(ids[0]), workerResultFile)
	// A worker writes result.json asynchronously; seeing the file exist is not
	// enough because a concurrent write can leave it empty or partial. Poll
	// until the file parses as the result object.
	var result map[string]any
	if !eventually(t, 2*time.Second, func() bool {
		data, err := os.ReadFile(resultPath)
		if err != nil {
			return false
		}
		var candidate map[string]any
		if err := json.Unmarshal(data, &candidate); err != nil {
			return false
		}
		result = candidate
		return true
	}) {
		t.Fatalf("result.json missing or unreadable at %s", resultPath)
	}
	result["_janitor"] = map[string]any{"pruned_at": "2026-01-01T00:00:00Z"}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(resultPath, raw, 0o644); err != nil {
		t.Fatalf("write tombstone: %v", err)
	}

	resumed, failures := runtime.ResumeWorkers(context.Background(), ids, nil, "", nil, "")
	if len(resumed) != 0 {
		t.Fatalf("resumed = %v, want none for a pruned worker", resumed)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want one entry", failures)
	}
	message, _ := failures[0]["error"].(string)
	if !strings.Contains(message, "retention janitor") {
		t.Fatalf("failure = %q, want the janitor reason", message)
	}
}

// A23: run_worker resume carries the queen's tool scope into the resumed
// worker (upstream tools_override), while keeping the terminal report channel.
//
// Parity: framework/tools/queen_lifecycle_tools.py::run_worker +
// framework/host/colony_runtime.py::ColonyRuntime.resume_worker
func TestResumeWorkersAppliesToolScopeOverride(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "first pass", nil), reportResponse("success", "second pass", nil))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "finish the ledger"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
		t.Fatalf("worker status = %s, want terminal", worker.Status())
	}

	scope := []llm.ToolDef{{Name: "scope_only", Description: "queen-scoped tool", Parameters: map[string]any{"type": "object"}}}
	resumed, failures := runtime.ResumeWorkers(context.Background(), ids, scope, "", nil, "")
	if len(failures) != 0 || len(resumed) != 1 {
		t.Fatalf("resume = (%v, %v)", resumed, failures)
	}
	resumedWorker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return resumedWorker.Status().Done() }) {
		t.Fatalf("resumed worker status = %s, want terminal", resumedWorker.Status())
	}
	call, ok := lastScriptedCall(provider)
	if !ok {
		t.Fatal("no resumed LLM call")
	}
	names := toolNames(call.Tools)
	if !toolScopeContains(names, "scope_only") {
		t.Fatalf("resumed tools = %v, want the override tool", names)
	}
	if !toolScopeContains(names, "report_to_parent") {
		t.Fatalf("resumed tools = %v, want the terminal report channel", names)
	}
	if toolScopeContains(names, "run_playbook") {
		t.Fatalf("resumed tools = %v, must not expose the queen-only playbook driver", names)
	}
}

func toolScopeContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

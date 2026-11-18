package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

func hostTestSpec() agentloop.AgentSpec {
	systemPrompt := "You are a host test agent."
	spec := agentloop.NewAgentSpec("host_test_worker", "Host Test Worker", "Worker used by host tests.")
	spec.SystemPrompt = &systemPrompt
	spec.ToolAccessPolicy = "all"
	return spec
}

func firstUserMessage(messages []llm.ChatMessage) string {
	for _, message := range messages {
		if message.Role == "user" {
			return message.Content
		}
	}
	return ""
}

// reportLLM reports success for every task, optionally failing named tasks.
type reportLLM struct {
	failTasks map[string]bool

	mu    sync.Mutex
	calls int
}

func (r *reportLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()

	task := firstUserMessage(req.Messages)
	if r.failTasks != nil {
		for key := range r.failTasks {
			if strings.Contains(task, key) {
				return llm.Completion{}, errors.New("simulated worker failure")
			}
		}
	}
	arguments, _ := json.Marshal(map[string]any{
		"status":  "success",
		"summary": task + " done",
		"data":    map[string]any{"task": task},
	})
	return llm.Completion{
		ToolCalls:  []llm.ToolCall{{ID: "report_1", Name: "report_to_parent", Arguments: string(arguments)}},
		StopReason: "tool_calls",
	}, nil
}

func (r *reportLLM) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// blockingLLM holds every worker until its context is cancelled.
type blockingLLM struct {
	mu      sync.Mutex
	running int
}

func (b *blockingLLM) AComplete(ctx context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	b.mu.Lock()
	b.running++
	b.mu.Unlock()
	<-ctx.Done()
	return llm.Completion{}, ctx.Err()
}

func (b *blockingLLM) runningCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

func newTestHost(t *testing.T, provider llm.Completer, maxConcurrent int) *Host {
	t.Helper()
	h, err := New(Config{
		ColonyID:             "host_test_colony",
		AgentSpec:            hostTestSpec(),
		StoragePath:          t.TempDir(),
		LLM:                  provider,
		Tools:                []llm.ToolDef{},
		EventBus:             eventbus.NewEventBus(),
		MaxConcurrentWorkers: maxConcurrent,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(h.Stop)
	return h
}

func eventuallyHost(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return condition()
}

// Parity-exempt: new Go assembly facade (internal/host); no Python counterpart.
func TestHostRunAggregatesColonyResults(t *testing.T) {
	h := newTestHost(t, &reportLLM{}, 2)

	result, err := h.Run(context.Background(), []colony.BatchTask{
		{Task: "alpha"},
		{Task: "beta"},
	}, colony.RunSpawnOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != colony.RunCompleted {
		t.Fatalf("status = %s, want completed", result.Status)
	}
	if result.Succeeded != 2 || len(result.WorkerResults) != 2 {
		t.Fatalf("succeeded=%d workers=%d, want 2/2", result.Succeeded, len(result.WorkerResults))
	}
	if result.WorkerResults[0].Task != "alpha" || result.WorkerResults[1].Task != "beta" {
		t.Fatalf("worker order = %q/%q", result.WorkerResults[0].Task, result.WorkerResults[1].Task)
	}

	stats := h.Stats()
	if stats["total_workers"] != 2 {
		t.Fatalf("runtime total_workers = %v, want 2", stats["total_workers"])
	}
	hostStats, ok := stats["host"].(map[string]any)
	if !ok {
		t.Fatalf("host stats missing: %v", stats)
	}
	if hostStats["runs_total"] != 1 || hostStats["runs_active"] != 0 {
		t.Fatalf("host stats = %v, want 1 total / 0 active runs", hostStats)
	}

	runStats, ok := h.RunStatus(result.RunID)
	if !ok {
		t.Fatalf("RunStatus(%q) not found", result.RunID)
	}
	if runStats["status"] != string(colony.RunCompleted) {
		t.Fatalf("run status = %v, want completed", runStats["status"])
	}
	if len(h.Runs()) != 1 {
		t.Fatalf("Runs() = %d, want 1", len(h.Runs()))
	}
}

// Parity-exempt: new Go assembly facade (internal/host); no Python counterpart.
func TestHostRunSurfacesWorkerFailure(t *testing.T) {
	h := newTestHost(t, &reportLLM{failTasks: map[string]bool{"boom": true}}, 2)

	result, err := h.Run(context.Background(), []colony.BatchTask{
		{Task: "good task"},
		{Task: "boom task"},
	}, colony.RunSpawnOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != colony.RunPartial {
		t.Fatalf("status = %s, want partial", result.Status)
	}
	if result.Succeeded != 1 || result.Failed != 1 {
		t.Fatalf("succeeded=%d failed=%d, want 1/1", result.Succeeded, result.Failed)
	}
	if result.Error == nil {
		t.Fatal("result.Error = nil, want the failed worker's error")
	}
}

// Parity-exempt: new Go assembly facade (internal/host); no Python counterpart.
func TestHostStopStopsActiveRun(t *testing.T) {
	provider := &blockingLLM{}
	h := newTestHost(t, provider, 2)

	run, err := h.Spawn(context.Background(), []colony.BatchTask{
		{Task: "blocked one"},
		{Task: "blocked two"},
	}, colony.RunSpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !eventuallyHost(t, 2*time.Second, func() bool { return provider.runningCount() == 2 }) {
		t.Fatalf("running workers = %d, want 2", provider.runningCount())
	}

	h.Stop()

	result, err := run.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait after Stop: %v", err)
	}
	if result.Status != colony.RunStopped {
		t.Fatalf("status = %s, want stopped", result.Status)
	}
	if result.Stopped != 2 {
		t.Fatalf("stopped = %d, want 2", result.Stopped)
	}
	if h.Started() {
		t.Fatal("Started() = true after Stop")
	}
	if _, err := h.Spawn(context.Background(), []colony.BatchTask{{Task: "late"}}, colony.RunSpawnOptions{}); err == nil {
		t.Fatal("Spawn after Stop = nil error, want refusal")
	}
}

// Parity-exempt: new Go assembly facade (internal/host); no Python counterpart.
func TestHostRejectsInvalidConfig(t *testing.T) {
	if _, err := New(Config{AgentSpec: hostTestSpec()}); err == nil {
		t.Fatal("New without LLM = nil error, want an error")
	}
	provider := &reportLLM{}
	if _, err := New(Config{LLM: provider}); err == nil {
		t.Fatal("New without agent spec = nil error, want an error")
	}
}

// Parity-exempt: new Go assembly facade (internal/host); no Python counterpart.
func TestHostSubscribeDeliversColonyFilteredWorkerReports(t *testing.T) {
	h := newTestHost(t, &reportLLM{}, 1)

	var mu sync.Mutex
	var reports []map[string]any
	h.Subscribe(
		[]eventbus.EventType{eventbus.EventSubagentReport},
		func(event *eventbus.AgentEvent) {
			mu.Lock()
			reports = append(reports, event.Data)
			mu.Unlock()
		},
		eventbus.WithFilterColony("host_test_colony"),
	)

	if _, err := h.Run(context.Background(), []colony.BatchTask{{Task: "streamed"}}, colony.RunSpawnOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	if reports[0]["status"] != "success" {
		t.Fatalf("report status = %v, want success", reports[0]["status"])
	}
	if reports[0]["colony_id"] != "host_test_colony" {
		t.Fatalf("report colony_id = %v, want host_test_colony", reports[0]["colony_id"])
	}
}

// Parity-exempt: new Go assembly facade (internal/host); no Python counterpart.
func TestHostSpawnRejectsEmptyTasks(t *testing.T) {
	h := newTestHost(t, &reportLLM{}, 1)
	if _, err := h.Spawn(context.Background(), nil, colony.RunSpawnOptions{}); err == nil {
		t.Fatal("Spawn(nil) = nil error, want an error")
	}
}

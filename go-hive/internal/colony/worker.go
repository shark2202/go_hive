package colony

// Parity: framework/host/worker.py

import (
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/tracker"
)

func (s WorkerStatus) Done() bool {
	switch s {
	case WorkerCompleted, WorkerFailed, WorkerStopped:
		return true
	}
	return false
}

func (s WorkerStatus) String() string { return string(s) }

// Parity: framework/host/worker.py::WorkerResult
type WorkerResult struct {
	WorkerID      string
	Status        WorkerStatus
	Output        map[string]any
	Error         *string
	ToolCallsUsed int
	BudgetLimited bool
}

// Parity: framework/host/worker.py::WorkerInfo
type WorkerInfo struct {
	WorkerID  string
	Task      string
	StreamID  string
	BatchID   string
	Index     int
	Size      int
	StartedAt float64
}

// Parity: framework/host/worker.py::Worker (the report/status surface)
type Worker struct {
	ID         string
	Task       string
	StreamID   string
	Persistent bool

	Context *agentloop.AgentContext

	conversation *agentloop.NodeConversation

	BatchID    string
	BatchIndex int
	BatchSize  int

	loopOverrides map[string]any
	loopConfig    *agentloop.LoopConfig

	// Parity: framework/host/worker.py::Worker.budget_pinned
	BudgetPinned bool

	mu        sync.Mutex
	status    WorkerStatus
	report    map[string]any
	result    WorkerResult
	startedAt time.Time
	agentLoop *agentloop.AgentLoop
	done      chan struct{}
	doneOnce  sync.Once
}

func NewWorker(id, task, streamID string) *Worker {
	return &Worker{
		ID:       id,
		Task:     task,
		StreamID: streamID,
		status:   WorkerQueued,
		done:     make(chan struct{}),
	}
}

func (w *Worker) Status() WorkerStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *Worker) SetStatus(status WorkerStatus) {
	w.mu.Lock()
	w.status = status
	w.mu.Unlock()
}

func (w *Worker) MarkStarted(at time.Time) {
	w.mu.Lock()
	w.startedAt = at
	w.mu.Unlock()
}

func (w *Worker) StartedAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startedAt
}

// Parity: framework/host/worker.py::Worker.is_active
func (w *Worker) IsActive() bool {
	return w.Status().IsActive()
}

func (w *Worker) IsPersistent() bool { return w.Persistent }

// Parity: framework/host/worker.py::Worker._explicit_report / record_explicit_report
func (w *Worker) RecordExplicitReport(input map[string]any) {
	report := map[string]any{}
	for _, key := range []string{"status", "summary", "data"} {
		if value, ok := input[key]; ok {
			report[key] = value
		}
	}
	if _, ok := report["data"]; !ok {
		report["data"] = map[string]any{}
	}
	w.mu.Lock()
	w.report = report
	w.mu.Unlock()
}

func (w *Worker) ExplicitReport() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.report == nil {
		return nil
	}
	out := make(map[string]any, len(w.report))
	for key, value := range w.report {
		out[key] = value
	}
	return out
}

func (w *Worker) Result() WorkerResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.result
}

func (w *Worker) Finish(status WorkerStatus, result WorkerResult) {
	w.mu.Lock()
	w.status = status
	result.WorkerID = w.ID
	result.Status = status
	w.result = result
	w.mu.Unlock()
	w.doneOnce.Do(func() { close(w.done) })
}

func (w *Worker) Done() <-chan struct{} { return w.done }

func (w *Worker) Wait() { <-w.done }

func (w *Worker) Conversation() *agentloop.NodeConversation { return w.conversation }

// Parity: framework/host/worker.py::Worker._agent_loop
func (w *Worker) SetAgentLoop(loop *agentloop.AgentLoop) {
	w.mu.Lock()
	w.agentLoop = loop
	w.mu.Unlock()
}

func (w *Worker) AgentLoop() *agentloop.AgentLoop {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.agentLoop
}

// Parity: framework/host/worker.py::Worker._loop_tool_calls_used
func (w *Worker) LoopToolCallsUsed() int {
	if loop := w.AgentLoop(); loop != nil {
		return loop.ToolCallsUsed()
	}
	return 0
}

// Parity: framework/host/worker.py::Worker._loop_budget_limited
func (w *Worker) LoopBudgetLimited() bool {
	if loop := w.AgentLoop(); loop != nil {
		return loop.BudgetLimited()
	}
	return false
}

// Parity: framework/host/worker.py::_config["tool_call_lifetime_budget"]
func (w *Worker) EffectiveBudget() int {
	if loop := w.AgentLoop(); loop != nil {
		return loop.LifetimeBudget()
	}
	return 0
}

// Parity: framework/host/worker.py::Worker.run (binding propagation)
func (w *Worker) TrackerResolver() tracker.Resolver {
	return func() (*tracker.Binding, error) {
		if w.Context == nil {
			return nil, nil
		}
		return tracker.BindingFromInputData(w.Context.InputData), nil
	}
}

// Parity: framework/host/worker.py::Worker._publish_report (SUBAGENT_REPORT
func (w *Worker) reportPayload() map[string]any {
	payload := map[string]any{
		"worker_id": w.ID,
		"status":    "success",
		"summary":   "",
		"data":      map[string]any{},
	}
	result := w.Result()
	payload["tool_calls_used"] = result.ToolCallsUsed
	payload["budget_limited"] = result.BudgetLimited
	payload["budget_pinned"] = w.BudgetPinned
	payload["tool_call_lifetime_budget"] = w.EffectiveBudget()
	if report := w.ExplicitReport(); report != nil {
		if status, ok := report["status"].(string); ok && status != "" {
			payload["status"] = status
		}
		if summary, ok := report["summary"].(string); ok {
			payload["summary"] = summary
		}
		if data, ok := report["data"].(map[string]any); ok {
			payload["data"] = data
		}
		return payload
	}
	if result.Status == WorkerFailed {
		payload["status"] = "failed"
		if result.Error != nil {
			payload["error"] = *result.Error
			payload["summary"] = *result.Error
		}
	}
	return payload
}

func (w *Worker) Info() WorkerInfo {
	w.mu.Lock()
	startedAt := w.startedAt
	w.mu.Unlock()
	startedSeconds := 0.0
	if !startedAt.IsZero() {
		startedSeconds = float64(startedAt.UnixNano()) / 1e9
	}
	return WorkerInfo{
		WorkerID:  w.ID,
		Task:      w.Task,
		StreamID:  w.StreamID,
		BatchID:   w.BatchID,
		Index:     w.BatchIndex,
		Size:      w.BatchSize,
		StartedAt: startedSeconds,
	}
}

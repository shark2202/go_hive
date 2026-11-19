package colony

// Parity: framework/host/worker.py

import (
	"context"
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

	// Summary/Data/ReportStatus/DurationSeconds mirror the remaining
	// framework/host/worker.py::WorkerResult fields. They are filled when the
	// worker reaches a terminal state so the HTTP read surface
	// (GET /workers, /workers/{id}) renders the same payload the Python result
	// carries; ReportStatus keeps the report vocabulary ("success", "failed",
	// "stopped", ...) separate from the runtime WorkerStatus.
	Summary         string
	Data            map[string]any
	ReportStatus    string
	DurationSeconds float64
}

// Parity: framework/host/worker.py::WorkerInfo
type WorkerInfo struct {
	WorkerID  string
	Task      string
	Goal      string
	Status    WorkerStatus
	StreamID  string
	BatchID   string
	Index     int
	Size      int
	StartedAt float64
	// ProfileName is the colony worker profile this worker was spawned from;
	// empty for legacy / single-template colonies.
	ProfileName string
	// WorkerSeq is the run-scoped dispatch ordinal for playbook workers.
	WorkerSeq int
	// Result is nil until the worker reaches a terminal state.
	Result *WorkerResult
}

// Parity: framework/host/worker.py::Worker (the report/status surface)
type Worker struct {
	ID         string
	Task       string
	StreamID   string
	Persistent bool
	// Goal is the plain-language title the dashboard shows for this worker.
	// Metadata only: it is never part of the worker's prompt.
	Goal string

	Context *agentloop.AgentContext

	conversation *agentloop.NodeConversation

	BatchID    string
	BatchIndex int
	BatchSize  int

	// ProfileName is the colony worker profile this worker was spawned from
	// (empty for legacy / single-template colonies); WorkerSeq is the
	// run-scoped dispatch ordinal for playbook workers. Both are copied onto
	// WorkerInfo by Info().
	ProfileName string
	WorkerSeq   int

	loopOverrides map[string]any
	loopConfig    *agentloop.LoopConfig

	// Parity: framework/host/worker.py::Worker.budget_pinned
	BudgetPinned bool

	mu            sync.Mutex
	status        WorkerStatus
	report        map[string]any
	result        WorkerResult
	startedAt     time.Time
	finishedAt    time.Time
	finished      bool
	stopRequested bool
	cancel        context.CancelFunc
	agentLoop     *agentloop.AgentLoop
	done          chan struct{}
	doneOnce      sync.Once
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

func (w *Worker) FinishedAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.finishedAt
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
	w.finishOnce(status, result, nil)
}

// finishOnce records the terminal result exactly once. A worker can be
// stopped from outside (StopWorker) and also unwind from its own loop; the
// first terminal transition wins so status and the done channel cannot
// disagree. after runs once the result is visible but before done closes, so
// terminal reports are published before waiters observe completion.
func (w *Worker) finishOnce(status WorkerStatus, result WorkerResult, after func()) bool {
	w.mu.Lock()
	if w.finished {
		w.mu.Unlock()
		return false
	}
	w.finished = true
	w.status = status
	w.finishedAt = time.Now()
	result.WorkerID = w.ID
	result.Status = status
	result = w.completeResultLocked(result, status)
	w.result = result
	w.mu.Unlock()
	if after != nil {
		after()
	}
	w.doneOnce.Do(func() { close(w.done) })
	return true
}

// completeResultLocked fills the telemetry fields the read surface renders
// (framework/host/worker.py::WorkerResult) from state the runtime already
// holds: the explicit report, the wall-clock duration, and the report-status
// vocabulary. Callers may pass any of them pre-filled; values win over
// derivations.
func (w *Worker) completeResultLocked(result WorkerResult, status WorkerStatus) WorkerResult {
	if result.DurationSeconds == 0 && !w.startedAt.IsZero() {
		result.DurationSeconds = w.finishedAt.Sub(w.startedAt).Seconds()
	}
	if result.Summary == "" && w.report != nil {
		if summary, ok := w.report["summary"].(string); ok {
			result.Summary = summary
		}
	}
	if result.Summary == "" && result.Error != nil {
		result.Summary = *result.Error
	}
	if result.Data == nil && w.report != nil {
		if data, ok := w.report["data"].(map[string]any); ok {
			result.Data = data
		}
	}
	if result.ReportStatus == "" && w.report != nil {
		if reported, ok := w.report["status"].(string); ok {
			result.ReportStatus = reported
		}
	}
	if result.ReportStatus == "" {
		result.ReportStatus = status.reportStatus()
	}
	return result
}

// reportStatus maps the runtime status onto the report-status vocabulary
// framework/host/worker.py::WorkerResult uses.
func (s WorkerStatus) reportStatus() string {
	switch s {
	case WorkerCompleted:
		return "success"
	case WorkerFailed:
		return "failed"
	case WorkerStopped:
		return "stopped"
	}
	return ""
}

// Inject queues a client message for the worker's running loop, mirroring
// framework/host/worker.py::Worker.inject: the text lands as a user turn at
// the loop's next boundary. Returns false when the worker is not active or has
// no agent loop yet.
func (w *Worker) Inject(content string) bool {
	if content == "" || !w.IsActive() {
		return false
	}
	loop := w.AgentLoop()
	if loop == nil {
		return false
	}
	loop.InjectEvent(content, true, nil)
	return true
}

// setResultSummary fills the terminal result summary once the published
// report text is known (publishReport runs after finishOnce).
func (w *Worker) setResultSummary(summary string) {
	if summary == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.result.Summary == "" {
		w.result.Summary = summary
	}
}

// beginRun promotes a queued worker to running. It refuses when the worker
// was stopped while waiting for a concurrency slot, or when the run context
// is already cancelled.
func (w *Worker) beginRun(ctx context.Context) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished || w.stopRequested {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	w.status = WorkerRunning
	w.startedAt = time.Now()
	return true
}

func (w *Worker) setCancel(cancel context.CancelFunc) {
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()
}

// requestStop flips the stop flag and hands back the worker's cancel func.
// The gated run goroutine owns the terminal transition and report.
func (w *Worker) requestStop() (context.CancelFunc, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		return nil, false
	}
	w.stopRequested = true
	return w.cancel, true
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

// Parity: framework/agent_loop/internals/types.py::LoopConfig.tool_call_lifetime_budget
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

// Parity: framework/host/worker.py::Worker._emit_terminal_events (SUBAGENT_REPORT
// payload; see also Worker.record_explicit_report)
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
	finished := w.finished
	status := w.status
	profileName := w.ProfileName
	workerSeq := w.WorkerSeq
	result := w.result
	w.mu.Unlock()
	startedSeconds := 0.0
	if !startedAt.IsZero() {
		startedSeconds = float64(startedAt.UnixNano()) / 1e9
	}
	info := WorkerInfo{
		WorkerID:    w.ID,
		Task:        w.Task,
		Goal:        w.Goal,
		Status:      status,
		StreamID:    w.StreamID,
		BatchID:     w.BatchID,
		Index:       w.BatchIndex,
		Size:        w.BatchSize,
		StartedAt:   startedSeconds,
		ProfileName: profileName,
		WorkerSeq:   workerSeq,
	}
	if finished {
		copied := result
		info.Result = &copied
	}
	return info
}

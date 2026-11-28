package colony

// Parity: framework/host/worker.py

import (
	"context"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
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
	// TokensUsed is the prompt+completion token total the worker's loop
	// recorded, the port's counterpart of upstream WorkerResult.tokens_used.
	TokensUsed int

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
	// GoalEvaluation is the executed verdict for the colony goal's weighted
	// success criteria (nil when the colony declares none). It rides the
	// terminal result so callers reading WorkerResult see the same outcome the
	// queen received in the [WORKER_REPORT].
	GoalEvaluation *agentloop.GoalEvaluation
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
	// ReportSchema is the playbook receipt contract this worker was spawned
	// with (worker(schema=...)). Empty means the colony's own report schema.
	ReportSchema map[string]any

	// StoragePath is the worker's on-disk home (<colony>/workers/<id>).
	// OutputFile is the transcript path a report points the queen at.
	StoragePath string
	OutputFile  string

	Context *agentloop.AgentContext

	conversation *agentloop.NodeConversation
	// resumed marks a worker rebuilt over a saved transcript: its loop
	// continues the persisted conversation instead of rendering the fresh
	// task message.
	resumed bool
	// resumeGuidance is the queen's steering turn for a resumed worker. It is
	// injected as client input once the loop exists (runWorker) instead of
	// being written into the saved transcript.
	resumeGuidance string
	// resumeTools is the tool scope the resume caller handed down (upstream
	// run_worker tools_override). Empty keeps the runtime's live worker
	// surface; non-empty replaces it for this run.
	resumeTools []llm.ToolDef
	// wake carries "new input arrived" to the persistent (overseer) run loop.
	// Non-persistent workers still write it; nobody reads their channel.
	wake chan struct{}
	// pendingInput holds text injected into a persistent worker between turns;
	// the run loop renders each item as the next user message.
	pendingInput []string

	BatchID    string
	BatchIndex int
	BatchSize  int

	// ProfileName is the colony worker profile this worker was spawned from
	// (empty for legacy / single-template colonies); WorkerSeq is the
	// run-scoped dispatch ordinal for playbook workers. Both are copied onto
	// WorkerInfo by Info().
	ProfileName string
	WorkerSeq   int

	// Integrations is the resolved worker-profile credential binding: a
	// provider id (e.g. "slack", "google") -> connected account alias. The
	// runtime applies it as the default `account` argument for the worker's
	// provider-mapped tool calls; an explicit account on the call still wins.
	// Empty for unbound/legacy workers and a no-op.
	//
	// Parity: framework/host/worker.py::Worker._integrations / account_overrides
	Integrations map[string]string
	// BrowserProfile is the Chrome profile label this worker's browser tools
	// target ("default" when unbound). The spawn pre-flight fails fast when a
	// specific profile's extension is not connected; the browser reaper uses
	// the same label for the worker's tab group.
	//
	// Parity: framework/host/worker.py::Worker._browser_profile
	BrowserProfile string

	loopOverrides map[string]any
	loopConfig    *agentloop.LoopConfig

	// Parity: framework/host/worker.py::Worker.budget_pinned
	BudgetPinned bool

	mu       sync.Mutex
	status   WorkerStatus
	report   map[string]any
	result   WorkerResult
	goalEval *agentloop.GoalEvaluation
	// improvementSignals are the missed-criterion/judge-retry records this
	// worker produced; the terminal report carries them.
	improvementSignals []ImprovementSignal
	startedAt          time.Time
	finishedAt         time.Time
	finished           bool
	stopRequested      bool
	cancel             context.CancelFunc
	agentLoop          *agentloop.AgentLoop
	done               chan struct{}
	doneOnce           sync.Once
}

func NewWorker(id, task, streamID string) *Worker {
	return &Worker{
		ID:       id,
		Task:     task,
		StreamID: streamID,
		status:   WorkerQueued,
		done:     make(chan struct{}),
		wake:     make(chan struct{}, 1),
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

// SetGoalEvaluation records the executed goal-criteria verdict for this
// worker. It is called before the terminal result is built so the outcome
// rides result.json, GET /workers, and the [WORKER_REPORT] payload.
func (w *Worker) SetGoalEvaluation(evaluation *agentloop.GoalEvaluation) {
	w.mu.Lock()
	w.goalEval = evaluation
	w.mu.Unlock()
}

// GoalEvaluation returns the recorded goal-criteria verdict, or nil when the
// colony declares no success criteria (or the worker never finished a turn).
func (w *Worker) GoalEvaluation() *agentloop.GoalEvaluation {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.goalEval
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
	if result.GoalEvaluation == nil {
		result.GoalEvaluation = w.goalEval
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
	// A persistent worker (the overseer) is between turns most of the time,
	// and AgentLoop drains injected input only after a tool batch. Buffer the
	// text for the run loop instead, which renders it as the next user turn.
	if w.Persistent {
		w.mu.Lock()
		w.pendingInput = append(w.pendingInput, content)
		w.mu.Unlock()
		w.signalWake()
		return true
	}
	loop := w.AgentLoop()
	if loop == nil {
		return false
	}
	loop.InjectEvent(content, true, nil)
	w.signalWake()
	return true
}

// takePendingInput drains text injected while the persistent run loop was
// between turns. The caller renders each item as a user message.
func (w *Worker) takePendingInput() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pendingInput) == 0 {
		return nil
	}
	out := w.pendingInput
	w.pendingInput = nil
	return out
}

func (w *Worker) signalWake() {
	select {
	case w.wakeCh() <- struct{}{}:
	default:
	}
}

// wakeCh is the run loop's "new input" signal. A zero Worker built by hand in
// a test still gets a channel rather than a nil receive.
func (w *Worker) wakeCh() chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wake == nil {
		w.wake = make(chan struct{}, 1)
	}
	return w.wake
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

// takeResumeGuidance drains the queen's steering turn queued by a resume. The
// run path injects it into the loop as client input before the first model
// call, matching upstream's inject_event-at-resume shape.
func (w *Worker) takeResumeGuidance() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	text := w.resumeGuidance
	w.resumeGuidance = ""
	return text
}

// resumeToolScope returns the resume caller's tool scope, or nil when the
// resume kept the runtime's live worker surface.
func (w *Worker) resumeToolScope() []llm.ToolDef {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.resumeTools) == 0 {
		return nil
	}
	out := make([]llm.ToolDef, len(w.resumeTools))
	copy(out, w.resumeTools)
	return out
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

// LoopTokensUsed mirrors WorkerResult.tokens_used: the loop's cumulative
// prompt+completion tokens at the moment the terminal report is built.
//
// Parity: framework/host/worker.py::WorkerResult.tokens_used
func (w *Worker) LoopTokensUsed() int {
	if loop := w.AgentLoop(); loop != nil {
		return loop.TokensUsed()
	}
	return 0
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
	// tokens_used rides every terminal report (upstream WorkerResult carries
	// it on success, failed and stopped alike) so the queen-side formatter can
	// render cost without a second lookup.
	payload["tokens_used"] = result.TokensUsed
	// duration_seconds is the upstream report field the queen's formatter
	// renders; it rides every terminal report (success, failed, stopped).
	payload["duration_seconds"] = result.DurationSeconds
	if w.OutputFile != "" {
		payload["output_file"] = w.OutputFile
	}
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
	// No explicit report: the terminal result still carries the report
	// vocabulary. finishWorkerWithoutReport sets ReportStatus=partial for a
	// clean exit without report_to_parent (P0-1), and a stopped worker maps to
	// "stopped" — the wait/report readers must see that, not the payload's
	// "success" default.
	if result.ReportStatus != "" {
		payload["status"] = result.ReportStatus
	}
	if result.Summary != "" {
		payload["summary"] = result.Summary
	}
	if len(result.Data) > 0 {
		payload["data"] = result.Data
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

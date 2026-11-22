package colony

// Colony-level run facade: spawn a batch of workers, let them execute under
// the colony concurrency cap, then aggregate their reports into one terminal
// result. The runtime already emitted per-worker SUBAGENT_REPORT events; this
// layer adds a colony-stream lifecycle (execution_started ->
// execution_completed/execution_failed) so external status/SSE consumers can
// follow the whole run without polling individual workers.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

type RunPhase string

const (
	RunQueued    RunPhase = "queued"
	RunRunning   RunPhase = "running"
	RunCompleted RunPhase = "completed"
	RunPartial   RunPhase = "partial"
	RunFailed    RunPhase = "failed"
	RunStopped   RunPhase = "stopped"
)

// RunSpawnOptions controls one colony run.
type RunSpawnOptions struct {
	// LoopConfigOverrides is forwarded to every worker in the run.
	LoopConfigOverrides map[string]any
	// Timeout bounds the whole run. When it expires the run context is
	// cancelled, queued workers are stopped before starting, and running
	// workers unwind through their own context. <=0 means no deadline.
	Timeout time.Duration
}

// WorkerOutcome is one worker's terminal contribution to a colony run.
type WorkerOutcome struct {
	WorkerID string
	Task     string
	StreamID string

	// Status is the runtime worker state (completed/failed/stopped).
	Status WorkerStatus
	// Outcome is the reporting category: success, partial, failed, stopped.
	Outcome string
	// ReportStatus is the explicit report_to_parent status when supplied,
	// otherwise synthesised from Status.
	ReportStatus string

	Summary       string
	Data          map[string]any
	Error         *string
	ToolCallsUsed int
	BudgetLimited bool
	Duration      time.Duration
}

// ColonyResult is the aggregated outcome of one colony run.
type ColonyResult struct {
	RunID    string
	BatchID  string
	ColonyID string
	Status   RunPhase

	WorkerResults []WorkerOutcome
	Summaries     []string

	Succeeded int
	Partial   int
	Failed    int
	Stopped   int

	// Data merges every worker's report data under its worker id, so
	// multi-worker output survives aggregation without key collisions.
	Data map[string]any

	Error *string

	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

// ColonyRun is a live handle on an executing colony run. Spawn is
// non-blocking; Wait returns the aggregated result once every worker is
// terminal.
type ColonyRun struct {
	runtime *ColonyRuntime
	id      string
	batchID string
	tasks   []BatchTask

	workerIDs []string

	ctx    context.Context
	cancel context.CancelFunc

	timeout   time.Duration
	startedAt time.Time

	done      chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	phase  RunPhase
	result *ColonyResult
}

func (run *ColonyRun) ID() string      { return run.id }
func (run *ColonyRun) BatchID() string { return run.batchID }
func (run *ColonyRun) WorkerIDs() []string {
	return append([]string(nil), run.workerIDs...)
}

func (run *ColonyRun) Phase() RunPhase {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.phase
}

// Done closes once the run reached a terminal phase and its result is ready.
func (run *ColonyRun) Done() <-chan struct{} { return run.done }

// Result returns nil until the run has finished.
func (run *ColonyRun) Result() *ColonyResult {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.result
}

// Stats is the externally visible snapshot of a run: phase plus per-status
// worker counts. Safe to call while the run is still executing.
func (run *ColonyRun) Stats() map[string]any {
	run.mu.Lock()
	phase := run.phase
	result := run.result
	run.mu.Unlock()

	active := 0
	queued := 0
	for _, id := range run.workerIDs {
		worker := run.runtime.GetWorker(id)
		if worker == nil {
			continue
		}
		if worker.IsActive() {
			active++
		}
		if worker.Status() == WorkerQueued {
			queued++
		}
	}

	finished := result != nil
	duration := time.Since(run.startedAt)
	if finished {
		duration = result.Duration
	}
	stats := map[string]any{
		"run_id":           run.id,
		"batch_id":         run.batchID,
		"colony_id":        run.runtime.StreamID(),
		"status":           string(phase),
		"worker_count":     len(run.workerIDs),
		"active_workers":   active,
		"queued_workers":   queued,
		"finished":         finished,
		"started_at":       run.startedAt.UTC().Format(time.RFC3339Nano),
		"duration_seconds": duration.Seconds(),
		"worker_ids":       append([]string(nil), run.workerIDs...),
	}
	if result != nil {
		stats["succeeded_workers"] = result.Succeeded
		stats["partial_workers"] = result.Partial
		stats["failed_workers"] = result.Failed
		stats["stopped_workers"] = result.Stopped
	}
	return stats
}

// Stop cancels the run. Queued workers never start; running workers unwind.
func (run *ColonyRun) Stop() {
	if run == nil {
		return
	}
	run.cancel()
}

// Wait blocks for the run's terminal result. Cancelling ctx stops the run and
// returns the aggregated result together with ctx.Err().
func (run *ColonyRun) Wait(ctx context.Context) (*ColonyResult, error) {
	if run == nil {
		return nil, fmt.Errorf("colony: nil run")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-run.done:
		return run.Result(), nil
	case <-ctx.Done():
		select {
		case <-run.done:
			return run.Result(), nil
		default:
		}
		run.Stop()
		<-run.done
		return run.Result(), ctx.Err()
	}
}

// SpawnRun starts a colony run and returns immediately. The caller can follow
// worker events on the event bus, poll Stats, or block on Wait for the
// aggregated result.
func (r *ColonyRuntime) SpawnRun(ctx context.Context, tasks []BatchTask, opts RunSpawnOptions) (*ColonyRun, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("colony: run requires at least one task")
	}
	r.UnblockDispatch()
	baseCtx := r.runtimeContext(ctx)
	runCtx, cancel := context.WithCancel(baseCtx)
	if ctx != nil {
		// Honor the caller's context in addition to the runtime's lifetime.
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-runCtx.Done():
			}
		}()
	}

	runID := r.newID("run")
	batchID := r.newID("batch")
	ids, err := r.spawnBatchOn(runCtx, tasks, SpawnBatchOptions{
		LoopConfigOverrides: opts.LoopConfigOverrides,
	}, batchID)
	if err != nil {
		cancel()
		return nil, err
	}

	run := &ColonyRun{
		runtime:   r,
		id:        runID,
		batchID:   batchID,
		tasks:     append([]BatchTask(nil), tasks...),
		workerIDs: ids,
		ctx:       runCtx,
		cancel:    cancel,
		timeout:   opts.Timeout,
		startedAt: time.Now(),
		done:      make(chan struct{}),
		phase:     RunRunning,
	}
	r.publishRunStarted(run)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.watchRun(run)
	}()
	return run, nil
}

// RunBatch is the blocking one-call closed loop: spawn, execute, aggregate.
func (r *ColonyRuntime) RunBatch(ctx context.Context, tasks []BatchTask, opts RunSpawnOptions) (*ColonyResult, error) {
	run, err := r.SpawnRun(ctx, tasks, opts)
	if err != nil {
		return nil, err
	}
	return run.Wait(ctx)
}

func (r *ColonyRuntime) watchRun(run *ColonyRun) {
	defer run.cancel()

	var timer *time.Timer
	if run.timeout > 0 {
		timer = time.AfterFunc(run.timeout, run.cancel)
		defer timer.Stop()
	}

	for _, id := range run.workerIDs {
		worker := r.GetWorker(id)
		if worker != nil {
			worker.Wait()
		}
	}

	result := r.aggregateRun(run)

	run.mu.Lock()
	run.result = result
	run.phase = result.Status
	run.mu.Unlock()

	r.publishRunFinished(run, result)
	run.closeOnce.Do(func() { close(run.done) })
}

func (r *ColonyRuntime) aggregateRun(run *ColonyRun) *ColonyResult {
	result := &ColonyResult{
		RunID:         run.id,
		BatchID:       run.batchID,
		ColonyID:      r.streamID,
		WorkerResults: make([]WorkerOutcome, 0, len(run.workerIDs)),
		Data:          map[string]any{},
		StartedAt:     run.startedAt,
	}

	for _, id := range run.workerIDs {
		worker := r.GetWorker(id)
		if worker == nil {
			errorText := "no_such_worker"
			outcome := WorkerOutcome{
				WorkerID:     id,
				Outcome:      "failed",
				ReportStatus: "failed",
				Data:         map[string]any{},
				Error:        &errorText,
			}
			result.WorkerResults = append(result.WorkerResults, outcome)
			result.Summaries = append(result.Summaries, errorText)
			result.Failed++
			continue
		}
		outcome := workerOutcome(worker)
		result.WorkerResults = append(result.WorkerResults, outcome)
		result.Summaries = append(result.Summaries, outcome.Summary)
		switch outcome.Outcome {
		case "success":
			result.Succeeded++
		case "partial":
			result.Partial++
		case "stopped":
			result.Stopped++
		default:
			result.Failed++
		}
		if outcome.Error != nil && result.Error == nil {
			result.Error = outcome.Error
		}
		if len(outcome.Data) > 0 {
			result.Data[worker.ID] = outcome.Data
		}
	}

	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	result.Status = classifyRun(result)
	if result.Error == nil && (result.Status == RunFailed || result.Status == RunStopped) {
		errorText := string(result.Status)
		if result.Status == RunStopped {
			errorText = "colony run stopped before all workers completed"
		}
		result.Error = &errorText
	}
	return result
}

func workerOutcome(worker *Worker) WorkerOutcome {
	status := worker.Status()
	workerResult := worker.Result()
	report := worker.ExplicitReport()

	outcome := WorkerOutcome{
		WorkerID:      worker.ID,
		Task:          worker.Task,
		StreamID:      worker.StreamID,
		Status:        status,
		Data:          map[string]any{},
		Error:         workerResult.Error,
		ToolCallsUsed: workerResult.ToolCallsUsed,
		BudgetLimited: workerResult.BudgetLimited,
	}

	if report != nil {
		if value, ok := report["status"].(string); ok {
			outcome.ReportStatus = value
		}
		if value, ok := report["summary"].(string); ok {
			outcome.Summary = value
		}
		if value, ok := report["data"].(map[string]any); ok {
			outcome.Data = value
		}
	}
	if outcome.ReportStatus == "" {
		switch status {
		case WorkerCompleted:
			outcome.ReportStatus = "success"
		case WorkerFailed:
			outcome.ReportStatus = "failed"
		case WorkerStopped:
			outcome.ReportStatus = "stopped"
		default:
			outcome.ReportStatus = string(status)
		}
	}
	if outcome.Summary == "" {
		if text, ok := workerResult.Output["text"].(string); ok {
			outcome.Summary = text
		}
	}
	if outcome.Summary == "" && outcome.Error != nil {
		outcome.Summary = *outcome.Error
	}

	startedAt := worker.StartedAt()
	finishedAt := worker.FinishedAt()
	if !startedAt.IsZero() && !finishedAt.IsZero() {
		outcome.Duration = finishedAt.Sub(startedAt)
	}

	switch status {
	case WorkerFailed:
		outcome.Outcome = "failed"
	case WorkerStopped:
		outcome.Outcome = "stopped"
	case WorkerCompleted:
		switch outcome.ReportStatus {
		case "failed":
			outcome.Outcome = "failed"
		case "partial":
			outcome.Outcome = "partial"
		case "stopped", "timeout":
			outcome.Outcome = "stopped"
		default:
			outcome.Outcome = "success"
		}
	default:
		outcome.Outcome = "failed"
	}
	return outcome
}

func classifyRun(result *ColonyResult) RunPhase {
	switch {
	case result.Failed == 0 && result.Stopped == 0 && result.Partial == 0:
		return RunCompleted
	case result.Succeeded == 0 && result.Partial == 0 && result.Failed == 0 && result.Stopped > 0:
		return RunStopped
	case result.Succeeded == 0 && result.Partial == 0:
		return RunFailed
	default:
		return RunPartial
	}
}

func (r *ColonyRuntime) publishRunStarted(run *ColonyRun) {
	if r.bus == nil {
		return
	}
	colonyID := r.streamID
	event := eventbus.NewAgentEvent(eventbus.EventExecutionStarted, r.streamID)
	event.NodeID = eventbus.StrPtr(run.id)
	event.ExecutionID = eventbus.StrPtr(run.id)
	event.ColonyID = &colonyID
	event.Data = map[string]any{
		"run_id":     run.id,
		"batch_id":   run.batchID,
		"colony_id":  r.streamID,
		"worker_ids": append([]string(nil), run.workerIDs...),
		"task_count": len(run.tasks),
		"status":     string(RunRunning),
	}
	r.bus.Publish(event)
}

func (r *ColonyRuntime) publishRunFinished(run *ColonyRun, result *ColonyResult) {
	if r.bus == nil {
		return
	}
	eventType := eventbus.EventExecutionCompleted
	if result.Status == RunFailed || result.Status == RunStopped {
		eventType = eventbus.EventExecutionFailed
	}
	colonyID := r.streamID
	event := eventbus.NewAgentEvent(eventType, r.streamID)
	event.NodeID = eventbus.StrPtr(run.id)
	event.ExecutionID = eventbus.StrPtr(run.id)
	event.ColonyID = &colonyID
	event.Data = map[string]any{
		"run_id":           run.id,
		"batch_id":         run.batchID,
		"colony_id":        r.streamID,
		"status":           string(result.Status),
		"worker_ids":       append([]string(nil), run.workerIDs...),
		"worker_count":     len(result.WorkerResults),
		"succeeded":        result.Succeeded,
		"partial":          result.Partial,
		"failed":           result.Failed,
		"stopped":          result.Stopped,
		"summaries":        append([]string(nil), result.Summaries...),
		"duration_seconds": result.Duration.Seconds(),
	}
	if result.Error != nil {
		event.Data["error"] = *result.Error
	}
	r.bus.Publish(event)
}

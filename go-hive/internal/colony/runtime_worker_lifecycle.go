package colony

// Worker lifecycle and terminal-report helpers: batch spawn registration, the
// worker run entry point, and every finish path it can end on. Split out of
// runtime.go to keep the runtime file under its size budget; the bodies are
// unchanged.

import (
	"context"
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// spawnBatchOn registers and starts one goroutine per task. Every worker gets
// its own context so a single run/worker can be stopped without touching its
// siblings. Admission is gated by workerSlots: workers beyond the colony cap
// stay QUEUED until a running peer releases its slot.
func (r *ColonyRuntime) spawnBatchOn(runCtx context.Context, tasks []BatchTask, opts SpawnBatchOptions, batchID string) ([]string, error) {
	cfg, err := BuildWorkerLoopConfig(opts.LoopConfigOverrides)
	if err != nil {
		return nil, err
	}
	pinned := false
	if _, ok := opts.LoopConfigOverrides["tool_call_lifetime_budget"]; ok {
		pinned = true
	}

	ids := make([]string, 0, len(tasks))
	r.mu.Lock()
	for range tasks {
		ids = append(ids, r.newIDLocked("worker"))
	}
	r.mu.Unlock()

	workers := make([]*Worker, 0, len(tasks))
	for index, task := range tasks {
		profileName := task.ProfileName
		if profileName == "" {
			profileName = opts.ProfileName
		}
		resolvedName, integrations, browserProfile := r.resolveWorkerProfileBinding(profileName)
		worker := r.BuildWorker(WorkerBuildOptions{
			WorkerID:       ids[index],
			Task:           task.Task,
			StreamID:       task.StreamID,
			InputData:      r.workerInputData(task.Data, ids[index], task.Task),
			ProfileName:    resolvedName,
			Integrations:   integrations,
			BrowserProfile: browserProfile,
			PreloadTools:   task.PreloadTools,
			ReportSchema:   task.ReportSchema,
		})
		worker.Goal = task.Goal
		worker.BatchID = batchID
		// Upstream's spawn_batch numbers a fan-out from 1 (enumerate(..., start=1));
		// 0 is reserved for "not part of a batch", which is what the report
		// formatter and the dashboard read. A solo spawn() carries no batch at
		// all, so it keeps the zero coordinates.
		if batchID != "" {
			worker.BatchIndex = index + 1
			worker.BatchSize = len(tasks)
		}
		worker.WorkerSeq = task.WorkerSeq
		worker.loopOverrides = opts.LoopConfigOverrides
		workerConfig := cfg
		worker.loopConfig = &workerConfig
		worker.BudgetPinned = pinned
		// Lineage breadcrumb: resume needs the original task/profile, and the
		// colony id is the cross-colony guard a later resume checks.
		r.writeWorkerMeta(worker)
		workers = append(workers, worker)
	}

	r.mu.Lock()
	for _, worker := range workers {
		r.workers[worker.ID] = worker
	}
	r.mu.Unlock()

	for _, worker := range workers {
		r.startWorker(runCtx, worker)
	}
	return ids, nil
}

// startWorker launches one worker under the colony's admission gate. Every
// worker gets its own context so a single run/worker can be stopped without
// touching its siblings; resumed workers enter through the same path.
func (r *ColonyRuntime) startWorker(runCtx context.Context, worker *Worker) {
	// Every worker enters the run-state record here, including a resumed one
	// whose meta predates the record: the restarted colony must see it as
	// in flight until it reaches a terminal status.
	r.writeWorkerRunState(worker, string(WorkerQueued))
	workerCtx, cancel := context.WithCancel(runCtx)
	worker.setCancel(cancel)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		r.runGatedWorker(workerCtx, worker)
	}()
}

func (r *ColonyRuntime) finishWorkerStopped(worker *Worker, summary string) {
	// A report that landed just before cancellation is more informative than
	// the stop marker — preserve it instead of masking a delivered result.
	if worker.ExplicitReport() != nil {
		worker.finishOnce(WorkerCompleted, r.workerTerminalResult(worker, map[string]any{}, nil), func() {
			r.publishReport(worker, summary)
		})
		return
	}
	errorText := summary
	// A21: a stopped worker reports the live tool-call/budget telemetry
	// upstream's CancelledError branch fills from the loop. Without it a
	// worker stopped mid-run looked like it never called a tool.
	result := WorkerResult{
		Output:        map[string]any{},
		Error:         &errorText,
		ToolCallsUsed: worker.LoopToolCallsUsed(),
		BudgetLimited: worker.LoopBudgetLimited(),
	}
	worker.finishOnce(WorkerStopped, result, func() {
		r.publishStopped(worker, summary)
	})
}

// StopWorker stops one live or queued worker. Running workers unwind through
// their own context; queued workers wake from the admission gate and publish a
// stopped report. Returns false when the worker is unknown or already terminal.
//
// The stopped worker's browser tab group is reaped here, after the bounded
// cancel, exactly as upstream's stop_worker -> stop_workers -> _reap_worker_browsers
// path does. The runtime's reaped set makes a colony stop overlapping this one
// harmless.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop_worker
// Parity: framework/host/colony_runtime.py::ColonyRuntime._reap_worker_browsers
func (r *ColonyRuntime) StopWorker(id string) bool {
	worker, cancelled := r.cancelWorker(id)
	if !cancelled {
		return false
	}
	r.reapWorkerBrowsers([]*Worker{worker})
	return true
}

// cancelWorker is the cancel half of a worker stop: it flips the stop flag and
// cancels the worker's own context. The stop sweep uses it directly so it can
// reap the whole set once, concurrently, after its bounded wait instead of
// paying the reaper's budget per worker.
func (r *ColonyRuntime) cancelWorker(id string) (*Worker, bool) {
	worker := r.GetWorker(id)
	if worker == nil {
		return nil, false
	}
	cancel, active := worker.requestStop()
	if !active {
		return worker, false
	}
	if cancel != nil {
		cancel()
	}
	return worker, true
}

func (r *ColonyRuntime) runWorker(ctx context.Context, worker *Worker) {
	if err := ctx.Err(); err != nil {
		r.finishWorkerStopped(worker, "Worker was stopped before it started.")
		return
	}
	// Parity: framework/host/worker.py::Worker.run (browser-profile
	// pre-flight). A worker bound to a specific Chrome profile whose
	// extension is not connected fails before its first model call. "default"
	// and unbound workers are untouched, and an unreachable probe is unknown
	// (proceed), never a false failure.
	if unavailable, errorText, summary := r.workerBrowserProfileUnavailable(worker); unavailable {
		r.finishWorkerBrowserUnavailable(worker, errorText, summary)
		return
	}

	spec := r.specForWorker(worker)
	if worker.Context == nil {
		worker.Context = r.buildWorkerContext(worker, spec, r.workerToolDefs(), r.workerStorage(worker.ID), nil)
	}
	// The worker's transcript is persisted before the first LLM call, so a
	// worker stopped mid-run can be resumed from exactly where it stopped.
	// A fresh worker owns an empty store: stray parts from a previous process
	// that reused this id must not leak into its conversation.
	conv := worker.conversation
	if conv == nil {
		store := r.workerConversationStore(worker.ID)
		_ = store.Destroy()
		conv = agentloop.NewNodeConversationWithOptions(agentloop.ConversationOptions{
			SystemPrompt: r.workerSystemPrompt(worker, spec),
			Store:        store,
		})
		// Upstream seeds no user message here: the loop's
		// _build_initial_message composes the task frame plus the goal block
		// on the empty transcript. The Go worker owns its task text, so the
		// same first turn is materialised here — goal context rides it, not
		// the system prompt.
		_, _ = conv.AddUserMessage(r.workerSeedUserMessage(worker), agentloop.UserMessageOptions{})
	}
	worker.conversation = conv

	cfg := worker.loopConfig
	if cfg == nil {
		built, err := BuildWorkerLoopConfig(worker.loopOverrides)
		if err != nil {
			errorText := err.Error()
			worker.finishOnce(WorkerFailed, WorkerResult{Error: &errorText, Output: map[string]any{}}, func() {
				r.publishFailed(worker, errorText)
			})
			return
		}
		cfg = &built
	}
	tools := r.workerTools(worker)
	// A23: a resume may carry an explicit tool scope (upstream run_worker's
	// tools_override — the queen's current scope at resume time). It replaces
	// the live surface for this run; report_to_parent is the worker's terminal
	// channel and is re-added, and escalate stays off every colony worker.
	if scope := worker.resumeToolScope(); len(scope) > 0 {
		tools = appendMissingToolDefs(scope, agentloop.BuildReportToParentTool(r.reportSchemaFor(worker)))
		tools = withoutTool(tools, agentloop.EscalateToolName)
	}
	loop, stopHub := r.newWorkerAgentLoop(worker, *cfg, tools)
	defer stopHub()
	// The deterministic doom-loop detection is published as
	// node_tool_doom_loop at the threshold that also raises the escalation, so
	// the session event log and a watching SSE client see the repetition.
	loop.DoomLoopSink = r.workerDoomLoopSink(worker)
	loop.NodeStalledSink = r.workerNodeStalledSink(worker)
	// Hive 1.0.15 builds the colony worker loop with judge=None. The port does
	// not arm a loop judge here: the worker's text-only stall/report policy owns
	// termination, and report_to_parent stays its supervision channel (the
	// explicit report path stops the batch before the judge step).
	worker.SetAgentLoop(loop)
	// A23: resume guidance enters as client input, exactly where upstream's
	// resume_worker inject_event leaves it: queued for the first resumed
	// iteration, not appended to the saved transcript.
	if guidance := worker.takeResumeGuidance(); guidance != "" {
		loop.InjectEvent(guidance, true, nil)
	}
	r.ApplyAdaptiveBudget(worker)

	// Publish the worker's live turn progress on the colony bus. The session
	// bridge re-stamps text/tool/token events onto the session stream with
	// relayed_from=<worker id>, so the worker is visible while it runs.
	previousObserver := worker.Context.Observer
	if r.bus != nil {
		executionID := worker.Context.ExecutionID
		if executionID == "" {
			executionID = worker.ID
		}
		worker.Context.Observer = newWorkerEmitter(r.bus, worker.StreamID, executionID)
	}

	turn, err := loop.ExecuteTurn(ctx, worker.Context, conv, tools)
	worker.Context.Observer = previousObserver

	if err != nil {
		if ctx.Err() != nil {
			r.finishWorkerStopped(worker, "Worker was cancelled before completion.")
			return
		}
		errorText := err.Error()
		worker.finishOnce(WorkerFailed, r.workerTerminalResult(worker, map[string]any{}, &errorText), func() {
			r.publishFailed(worker, errorText)
		})
		return
	}

	// Execute the goal's weighted success criteria against the worker's final
	// output and turn misses into improvement signals. No declared criteria
	// means no evaluation and no signal (nil is "nothing to execute").
	evaluation := r.evaluateWorkerGoal(ctx, worker, loop, turn)
	worker.SetGoalEvaluation(evaluation)
	r.recordGoalImprovementSignals(worker, evaluation)

	output := map[string]any{"text": turn.FinalText}
	if evaluation != nil {
		output["goal_evaluation"] = evaluation.Map()
	}
	// Parity: framework/host/worker.py::Worker._build_result. A worker that
	// ran the loop to a clean exit without ever calling report_to_parent gets
	// a synthesised `partial` report (data.no_explicit_report=true) instead of
	// a success, so the queen never reads a silent no-op as an accepted task.
	if worker.ExplicitReport() == nil {
		r.finishWorkerWithoutReport(worker, output)
		return
	}
	worker.finishOnce(WorkerCompleted, r.workerTerminalResult(worker, output, nil), func() {
		r.publishReport(worker, turn.FinalText)
	})
}

// finishWorkerWithoutReport mirrors Worker._build_result's clean-exit
// backstop: no explicit report + a successful turn-loop synthesises a
// `partial` report carrying the Python summary and data.no_explicit_report.
// The worker's runtime status stays COMPLETED — it terminated cleanly; the
// REPORT is what says partial.
//
// Parity: framework/host/worker.py::Worker._build_result (outcome 2)
func (r *ColonyRuntime) finishWorkerWithoutReport(worker *Worker, output map[string]any) {
	data := make(map[string]any, len(output)+1)
	for key, value := range output {
		data[key] = value
	}
	data["no_explicit_report"] = true
	summary := fmt.Sprintf(
		"Worker terminated without calling report_to_parent. Task: '%s'. "+
			"Iteration / grace budget exhausted, or the agent yielded an empty turn.",
		truncateTaskForReport(worker.Task),
	)
	result := r.workerTerminalResult(worker, output, nil)
	result.Summary = summary
	result.Data = data
	result.ReportStatus = "partial"
	worker.finishOnce(WorkerCompleted, result, func() {
		r.publishSynthesizedPartial(worker, summary, data)
	})
}

// finishWorkerBrowserUnavailable fails a worker at the browser-profile
// pre-flight: status FAILED, no lifecycle executed, one failed report with
// upstream's "Not attempted" summary.
//
// Parity: framework/host/worker.py::Worker.run (browser-profile pre-flight)
func (r *ColonyRuntime) finishWorkerBrowserUnavailable(worker *Worker, errorText, summary string) {
	result := r.workerTerminalResult(worker, map[string]any{}, &errorText)
	result.Summary = summary
	result.ReportStatus = "failed"
	worker.finishOnce(WorkerFailed, result, func() {
		r.publishBrowserPreflightFailure(worker, errorText, summary)
	})
}

// truncateTaskForReport mirrors Python's task[:80] slice (code points, not
// bytes).
func truncateTaskForReport(task string) string {
	runes := []rune(task)
	if len(runes) <= 80 {
		return task
	}
	return string(runes[:80])
}

// Parity: framework/host/worker.py::Worker._build_result (telemetry)
func (r *ColonyRuntime) workerTerminalResult(worker *Worker, output map[string]any, errorText *string) WorkerResult {
	return WorkerResult{
		Output:        output,
		Error:         errorText,
		ToolCallsUsed: worker.LoopToolCallsUsed(),
		BudgetLimited: worker.LoopBudgetLimited(),
		TokensUsed:    worker.LoopTokensUsed(),
	}
}

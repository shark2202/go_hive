package colony

// Queen-facing worker fan-out: the run_worker tool, the [WORKER_REPORT]
// formatter and the SUBAGENT_REPORT subscription the queen session uses to
// see worker results as fresh user turns.
//
// Parity: framework/tools/queen_lifecycle_tools.py::run_worker
// Parity: framework/server/queen_orchestrator.py::_on_worker_report
// Parity: framework/host/colony_runtime.py::ColonyRuntime.watch_batch_timeouts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// RunWorkerToolName is the tool name the queen session advertises while a
// colony is bound. It stays reserved even without a colony: the session
// filters the def out until the binding exists.
const RunWorkerToolName = "run_worker"

const (
	// RunWorkerDefaultSoftTimeout is the upstream soft deadline (10 minutes).
	RunWorkerDefaultSoftTimeout = 600 * time.Second
	// runWorkerHardTimeoutCap bounds the derived hard cutoff (1 hour).
	runWorkerHardTimeoutCap = 3600 * time.Second
)

// RunWorkerSpawner is the slice of ColonyRuntime run_worker drives. The
// interface keeps the tool testable with a fake spawner and documents exactly
// which runtime calls the tool owns.
type RunWorkerSpawner interface {
	SpawnBatchWithOptions(ctx context.Context, tasks []BatchTask, opts SpawnBatchOptions) ([]string, error)
	GetWorker(id string) *Worker
	MaxConcurrentWorkers() int
}

// runWorkerContextKey carries the session's colony runtime from the queen
// runtime down to the tool executor. The chat catalogue is built before the
// colony attaches, so the binding is resolved per call instead of captured at
// construction.
type runWorkerContextKey struct{}

// WithRunWorkerSpawner binds spawner to ctx for the run_worker executor.
func WithRunWorkerSpawner(ctx context.Context, spawner RunWorkerSpawner) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runWorkerContextKey{}, spawner)
}

// RunWorkerSpawnerFromContext returns the colony bound to ctx, or nil.
func RunWorkerSpawnerFromContext(ctx context.Context) RunWorkerSpawner {
	if ctx == nil {
		return nil
	}
	spawner, _ := ctx.Value(runWorkerContextKey{}).(RunWorkerSpawner)
	return spawner
}

// RunWorkerToolDef returns the run_worker tool definition. The parameter
// schema mirrors the upstream Tool(...) declaration: tasks (spawn mode) and
// resume_worker_ids (resume mode, currently unsupported by this port) are
// mutually exclusive at execution time, so neither is marked required.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_run_parallel_tool
func RunWorkerToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        RunWorkerToolName,
		Description: runWorkerDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"description": "List of task specs to fan out. Each spec is " +
						"'{\"task\": \"<description>\", \"data\": {<optional structured input>}}'. " +
						"The 'task' string becomes the worker's initial user message — keep it tight " +
						"and per-worker UNIQUE (don't duplicate the shared protocol). 'data' is " +
						"merged into the worker's AgentContext.input_data so structured fields are " +
						"available to the worker's first turn.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"task": map[string]any{
								"type":        "string",
								"description": "Task description for the worker.",
							},
							"data": map[string]any{
								"type":        "object",
								"description": "Optional structured input fields.",
							},
							"goal": map[string]any{
								"type": "string",
								"description": "One sentence, in plain end-user language, describing what " +
									"this worker is doing (e.g. 'Checking 20 Instagram profiles to see " +
									"who accepts DMs'). Shown in the UI as the worker's title — ALWAYS " +
									"provide it; a non-technical user should understand it. Not shown to " +
									"the worker.",
							},
							"preload_tools": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
								"description": "Exact tool names to pre-load into this worker's eager " +
									"toolset at spawn (skips its search_tools round-trip). Use when you " +
									"KNOW the task needs tools that are searchable for workers. Unknown " +
									"names are ignored.",
							},
						},
						"required": []string{"task"},
					},
					"minItems": 1,
				},
				"max_iterations": map[string]any{
					"type":    "integer",
					"minimum": 1,
					"maximum": 1000,
					"description": "Cap on each worker's AgentLoop iterations of useful work. After " +
						"this is exhausted the framework grants an additional grace iteration where " +
						"dispatch is restricted to report_to_parent / tracker_upsert / task_update so " +
						"the worker can wrap up. Worker default: 3 (work) + 1 (grace) = 4 total.",
				},
				"timeout": map[string]any{
					"type": "number",
					"description": "SOFT deadline in seconds. Workers still running at this point " +
						"are messaged to call report_to_parent. Default 600 (10 minutes).",
				},
				"tool_call_lifetime_budget": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": 2000,
					"description": "Cap on each worker's CUMULATIVE tool calls across ALL its turns " +
						"(distinct from max_iterations, which counts turns). When a worker reaches " +
						"this total it is forced into the grace wind-down so it reports back instead " +
						"of running unbounded. Worker default: 150. OMIT this to let the colony's " +
						"adaptive budget manage the fan-out.",
				},
				"resume_worker_ids": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "RESUME mode (mutually exclusive with 'tasks'). worker_ids of " +
						"workers that stopped before reporting. Each is reloaded from its saved " +
						"conversation and continues from where it left off. An id with no saved " +
						"transcript is reported back per id instead of being re-spawned.",
					"minItems": 1,
				},
				"guidance": map[string]any{
					"type": "string",
					"description": "RESUME mode only: a single steering instruction injected as a " +
						"user turn into every resumed worker before it continues. Ignored when " +
						"spawning fresh workers via 'tasks'.",
				},
			},
		},
	}
}

const runWorkerDescription = "Fan out a batch of tasks to parallel workers and RETURN IMMEDIATELY. " +
	"Workers run in the background; each one reports back to you as a [WORKER_REPORT] user " +
	"turn when it finishes, so you stay unblocked and can chat with the user, kick off more " +
	"work, or do anything else in the meantime.\n\n" +
	"BATCH SIMILAR UNITS — 5-10 PER WORKER. Every worker pays a fixed orientation tax before " +
	"its first useful action: fresh context, system prompt, skill reads. One-small-unit-per-" +
	"worker multiplies that tax by N for nothing. Give each worker a slice of 5-10 similar " +
	"units and a timeout that covers the whole slice.\n\n" +
	"Per-task strings must be self-contained for the *differences*: include row keys, IDs, " +
	"URLs, anything unique to that worker's slice. Workers cannot ask the user follow-up " +
	"questions and cannot see your chat history.\n\n" +
	"NON-OVERLAPPING SLICES. Workers run concurrently, cannot coordinate, and cannot see each " +
	"other. Partition the work cleanly — each row/file/record should be owned by exactly one " +
	"worker.\n\n" +
	"The tool returns a JSON object with status='started' and the list of worker_ids you just " +
	"spawned. Each worker's completion arrives later as a [WORKER_REPORT] message containing " +
	"worker_id, status (success|partial|failed|timeout|stopped), summary, data, error, " +
	"duration. Read those messages as they arrive and respond to the user naturally.\n\n" +
	"TIMEOUT — 'timeout' is a SOFT deadline (default 600s). When it expires, every still-" +
	"active worker that hasn't reported gets a [SOFT TIMEOUT] message telling it to call " +
	"report_to_parent now. The hard cutoff (when stragglers are force-stopped) is derived " +
	"from 'timeout' automatically and is not agent-tunable.\n\n" +
	"Don't poll: do NOT poll worker status just to fill silence. Wait for [WORKER_REPORT]. " +
	"Don't fabricate: never predict, summarise, or guess worker results before the report " +
	"arrives."

// RunWorker executes one run_worker call. It returns the JSON tool payload and
// whether that payload is a tool error, matching the catalogue's
// (content, isError) contract.
//
// Two modes, mutually exclusive: tasks (spawn a fresh batch) and
// resume_worker_ids (rebuild stopped workers over their saved transcripts).
// A spawner that cannot resume still reports resume as unsupported instead of
// silently spawning fresh workers.
//
// Differences from upstream, stated explicitly instead of silently dropped:
//   - the tracker-registry preflight is not enforced (the Go port's workers do
//     not depend on a shared tracker DB for coordination).
func RunWorker(ctx context.Context, spawner RunWorkerSpawner, input map[string]any) (string, bool) {
	if spawner == nil {
		return jsonPayload(map[string]any{
			"error": "No colony is bound to this session. run_worker needs a live colony " +
				"runtime — create a colony (or load one) before dispatching workers.",
		}, true)
	}

	// Dispatch limits apply to both modes: a resume is a run_worker call too,
	// with the same soft/hard deadline watch and loop overrides.
	soft := RunWorkerDefaultSoftTimeout
	if seconds, ok := floatInput(input["timeout"]); ok && seconds > 0 {
		soft = time.Duration(seconds * float64(time.Second))
	}
	hard := computeHardTimeout(soft)
	overrides := map[string]any{}
	if value, ok := intInput(input["max_iterations"]); ok {
		overrides["max_iterations"] = value
	}
	if value, ok := intInput(input["tool_call_lifetime_budget"]); ok {
		overrides["tool_call_lifetime_budget"] = value
	}
	var loopOverrides map[string]any
	if len(overrides) > 0 {
		loopOverrides = overrides
	}

	resumeIDs, hasResume := stringSliceInput(input["resume_worker_ids"])
	if hasResume {
		if len(taskSpecs(input["tasks"])) > 0 {
			return jsonPayload(map[string]any{
				"error": "Pass either 'tasks' (spawn) or 'resume_worker_ids' (resume), not both.",
			}, true)
		}
		if len(resumeIDs) == 0 {
			return jsonPayload(map[string]any{
				"error": "resume_worker_ids must be a non-empty list of worker_id strings",
			}, true)
		}
		resumer, ok := spawner.(WorkerResumer)
		if !ok {
			return jsonPayload(map[string]any{
				"error": "run_worker resume mode is not supported by this colony runtime: it has " +
					"no saved worker conversations. Re-dispatch the unfinished units as new tasks instead.",
				"requested_worker_ids": resumeIDs,
			}, true)
		}
		batchID := newRunWorkerBatchID()
		resumed, failures := resumer.ResumeWorkers(ctx, resumeIDs,
			stringValue(input["guidance"]), loopOverrides, batchID)
		if len(resumed) == 0 {
			return jsonPayload(map[string]any{
				"error": "none of the requested workers could be resumed from disk — resume is " +
					"unsupported for a worker with no saved transcript, or one that is still " +
					"running; re-dispatch them as new tasks",
				"failures": failures,
			}, true)
		}
		if watcher, ok := spawner.(interface {
			WatchBatchTimeouts(workerIDs []string, soft, hard time.Duration)
		}); ok {
			watcher.WatchBatchTimeouts(resumed, soft, hard)
		}
		return jsonPayload(map[string]any{
			"status":               "resumed",
			"batch_id":             batchID,
			"worker_count":         len(resumed),
			"worker_ids":           resumed,
			"resumed_count":        len(resumed),
			"failures":             failures,
			"soft_timeout_seconds": soft.Seconds(),
			"hard_timeout_seconds": hard.Seconds(),
			"message": fmt.Sprintf(
				"Resumed %d worker(s) from their saved conversation; each continues where it "+
					"stopped and emits one [WORKER_REPORT] when it terminates.", len(resumed)),
		}, false)
	}

	specs := taskSpecs(input["tasks"])
	if items, ok := input["tasks"].([]any); ok {
		for index, item := range items {
			if _, isDict := item.(map[string]any); !isDict {
				return jsonPayload(map[string]any{
					"error": fmt.Sprintf("tasks[%d] is not a dict: %T", index, item),
				}, true)
			}
		}
	}
	if len(specs) == 0 {
		return jsonPayload(map[string]any{
			"error": "tasks must be a non-empty list of {task, data?} dicts",
		}, true)
	}

	tasks := make([]BatchTask, 0, len(specs))
	for i, spec := range specs {
		taskText := strings.TrimSpace(stringValue(spec["task"]))
		if taskText == "" {
			return jsonPayload(map[string]any{
				"error": fmt.Sprintf("tasks[%d].task is empty", i),
			}, true)
		}
		data := map[string]any{}
		for key, value := range mapValue(spec["data"]) {
			data[key] = value
		}
		// Keep the task text reachable from the worker's input_data, as
		// upstream does once a per-task data payload is present.
		if _, exists := data["task"]; !exists {
			data["task"] = taskText
		}
		task := BatchTask{Task: taskText, Data: data}
		if goal := strings.TrimSpace(stringValue(spec["goal"])); goal != "" {
			task.Goal = goal
		}
		if preload, ok := stringSliceInput(spec["preload_tools"]); ok {
			task.PreloadTools = preload
		}
		tasks = append(tasks, task)
	}

	batchID := newRunWorkerBatchID()
	workerIDs, err := spawner.SpawnBatchWithOptions(ctx, tasks, SpawnBatchOptions{
		LoopConfigOverrides: loopOverrides,
		BatchID:             batchID,
	})
	if err != nil {
		return jsonPayload(map[string]any{"error": "spawn_batch failed: " + err.Error()}, true)
	}

	if watcher, ok := spawner.(interface {
		WatchBatchTimeouts(workerIDs []string, soft, hard time.Duration)
	}); ok {
		watcher.WatchBatchTimeouts(workerIDs, soft, hard)
	}

	breadcrumbs := make([]map[string]any, 0, len(workerIDs))
	runningNow, queued := 0, 0
	for index, workerID := range workerIDs {
		taskText := ""
		if index < len(tasks) {
			taskText = tasks[index].Task
		}
		preview := previewText(taskText)
		initialStatus := "running"
		if worker := spawner.GetWorker(workerID); worker != nil {
			if worker.Status() == WorkerQueued {
				initialStatus = "queued"
				queued++
			} else {
				runningNow++
			}
		} else {
			runningNow++
		}
		breadcrumbs = append(breadcrumbs, map[string]any{
			"worker_id":      workerID,
			"task_index":     index + 1,
			"task_preview":   preview,
			"initial_status": initialStatus,
		})
	}

	return jsonPayload(map[string]any{
		"status":                 "started",
		"batch_id":               batchID,
		"worker_count":           len(workerIDs),
		"worker_ids":             workerIDs,
		"workers":                breadcrumbs,
		"running_now":            runningNow,
		"queued":                 queued,
		"max_concurrent_workers": spawner.MaxConcurrentWorkers(),
		"soft_timeout_seconds":   soft.Seconds(),
		"hard_timeout_seconds":   hard.Seconds(),
		"message": fmt.Sprintf(
			"Dispatched %d workers — %d running now, %d queued (colony cap: %d). "+
				"Each emits one structured [WORKER_REPORT] user turn when it terminates, "+
				"including queued workers; reports carry <batch_remaining>N</batch_remaining> "+
				"covering BOTH still-running AND still-queued peers in this batch. "+
				"Validate the work only AFTER you see <batch_remaining>0</batch_remaining> in "+
				"a report — until then more results are still coming. Don't poll, don't "+
				"fabricate, and don't peek at a worker's in-progress output unless the user "+
				"explicitly asks for live progress.",
			len(workerIDs), runningNow, queued, spawner.MaxConcurrentWorkers(),
		),
	}, false)
}

// ComputeHardTimeout is the exported derivation used by the tool and by tests:
// max(4× soft, soft + 600), capped at one hour.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_compute_hard_timeout
func ComputeHardTimeout(soft time.Duration) time.Duration { return computeHardTimeout(soft) }

func computeHardTimeout(soft time.Duration) time.Duration {
	hard := 4 * soft
	if candidate := soft + 600*time.Second; candidate > hard {
		hard = candidate
	}
	if hard > runWorkerHardTimeoutCap {
		hard = runWorkerHardTimeoutCap
	}
	if hard <= soft {
		hard = soft + 60*time.Second
	}
	return hard
}

func newRunWorkerBatchID() string {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		suffix = []byte{0, 0, 0, 0}
	}
	return "rpw_" + time.Now().UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(suffix)
}

// FormatWorkerReport renders one worker's terminal report as the structured
// block the queen reads. Field order and tags mirror the upstream
// _on_worker_report builder so a prompt written against Python Hive keeps
// working against this port.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report
func FormatWorkerReport(data map[string]any, batchRemaining int) string {
	workerID := stringValue(data["worker_id"])
	lines := []string{"[WORKER_REPORT]", "<worker_id>" + workerID + "</worker_id>"}
	if batchID := stringValue(data["batch_id"]); batchID != "" {
		lines = append(lines, "<batch_id>"+batchID+"</batch_id>")
		if size, ok := intInput(data["batch_size"]); ok && size > 0 {
			if index, ok := intInput(data["batch_index"]); ok {
				lines = append(lines, fmt.Sprintf("<task_index>%d</task_index>", index))
			}
			lines = append(lines, fmt.Sprintf("<task_count>%d</task_count>", size))
		}
		lines = append(lines, fmt.Sprintf("<batch_remaining>%d</batch_remaining>", batchRemaining))
	}
	if task := stringValue(data["task"]); task != "" {
		lines = append(lines, "<original_task>"+previewText(task)+"</original_task>")
	}
	if outputFile := stringValue(data["output_file"]); outputFile != "" {
		lines = append(lines, "<output_file>"+outputFile+"</output_file>")
	}
	status := stringValue(data["status"])
	if status == "" {
		status = "unknown"
	}
	lines = append(lines, "<status>"+status+"</status>")
	if duration, ok := floatInput(data["duration_seconds"]); ok {
		lines = append(lines, fmt.Sprintf("<duration_seconds>%.1f</duration_seconds>", duration))
	}
	used, haveUsed := intInput(data["tool_calls_used"])
	if haveUsed && used > 0 {
		if budget, ok := intInput(data["tool_call_lifetime_budget"]); ok && budget > 0 {
			lines = append(lines, fmt.Sprintf("<tool_calls>%d/%d</tool_calls>", used, budget))
		} else {
			lines = append(lines, fmt.Sprintf("<tool_calls>%d</tool_calls>", used))
		}
	}
	if limited, _ := data["budget_limited"].(bool); limited {
		lines = append(lines, "<budget_limited>true — the framework wound this worker down at "+
			"its tool-call budget before it finished on its own; re-dispatch the remaining "+
			"units if the task warrants more effort</budget_limited>")
	}
	summary := stringValue(data["summary"])
	if summary == "" {
		summary = "(no summary)"
	}
	lines = append(lines, "<summary>"+summary+"</summary>")
	if message := stringValue(data["error"]); message != "" {
		lines = append(lines, "<error>"+message+"</error>")
	}
	if payload, ok := data["data"].(map[string]any); ok && len(payload) > 0 {
		if encoded, err := json.Marshal(payload); err == nil {
			lines = append(lines, "<data>"+string(encoded)+"</data>")
		} else {
			lines = append(lines, fmt.Sprintf("<data>%v</data>", payload))
		}
	}
	return strings.Join(lines, "\n")
}

// SubscribeQueenReports forwards every worker terminal report on the colony
// bus to sink as a formatted [WORKER_REPORT] block. It returns an unsubscribe
// function; a nil runtime or sink yields a no-op. The queen's own stream is
// skipped, matching the upstream handler; the persistent overseer's report is
// NOT skipped (its stream id is "overseer", and upstream only filters
// stream_id == "queen").
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (subscribe)
func SubscribeQueenReports(runtime *ColonyRuntime, sink func(string)) (func(), error) {
	if sink == nil {
		return SubscribeQueenReportsWithWorker(runtime, nil)
	}
	return SubscribeQueenReportsWithWorker(runtime, func(_ string, report string) {
		sink(report)
	})
}

// SubscribeQueenReportsWithWorker is SubscribeQueenReports with the reporting
// worker's id, so a caller can route the same report to the persistent
// overseer without echoing the overseer's own report back to itself.
func SubscribeQueenReportsWithWorker(runtime *ColonyRuntime, sink func(workerID, report string)) (func(), error) {
	noop := func() {}
	if runtime == nil || sink == nil {
		return noop, nil
	}
	subscriptionID, err := runtime.SubscribeToEvents(
		[]eventbus.EventType{eventbus.EventSubagentReport},
		func(event *eventbus.AgentEvent) {
			if event == nil || event.StreamID == "queen" {
				return
			}
			data := event.Data
			if data == nil {
				return
			}
			workerID := stringValue(data["worker_id"])
			if workerID == "" && event.NodeID != nil {
				workerID = *event.NodeID
			}
			if workerID == "" {
				return
			}
			remaining := runtime.BatchRemaining(stringValue(data["batch_id"]), workerID)
			sink(workerID, FormatWorkerReport(data, remaining))
		},
	)
	if err != nil {
		return noop, err
	}
	return func() { runtime.Unsubscribe(subscriptionID) }, nil
}

// Unsubscribe removes a bus subscription created through SubscribeToEvents.
// False means the runtime had no bus or the id was unknown.
func (r *ColonyRuntime) Unsubscribe(subscriptionID string) bool {
	if r == nil || r.bus == nil || subscriptionID == "" {
		return false
	}
	return r.bus.Unsubscribe(subscriptionID)
}

// BatchRemaining counts the peers of workerID in batchID that are still
// active. Queued workers count: the queen must not validate a batch while a
// queued peer has yet to report.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (batch_remaining)
func (r *ColonyRuntime) BatchRemaining(batchID, excludeWorkerID string) int {
	if r == nil || batchID == "" {
		return 0
	}
	r.mu.Lock()
	ids := make([]string, 0, len(r.workers))
	for id := range r.workers {
		ids = append(ids, id)
	}
	r.mu.Unlock()

	remaining := 0
	for _, id := range ids {
		if id == excludeWorkerID {
			continue
		}
		worker := r.GetWorker(id)
		if worker == nil || worker.BatchID != batchID {
			continue
		}
		if worker.IsActive() {
			remaining++
		}
	}
	return remaining
}

// WatchBatchTimeouts enforces the soft and hard deadlines of one run_worker
// batch: at soft, still-active workers without an explicit report are told to
// report now; at hard, they are force-stopped (which still publishes their
// terminal report).
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.watch_batch_timeouts
func (r *ColonyRuntime) WatchBatchTimeouts(workerIDs []string, soft, hard time.Duration) {
	if r == nil || len(workerIDs) == 0 || soft <= 0 {
		return
	}
	if hard <= soft {
		hard = soft + 60*time.Second
	}
	grace := hard - soft
	warning := fmt.Sprintf(
		"[SOFT TIMEOUT] You've been running for %.0fs. Wrap up now: call report_to_parent "+
			"with whatever partial results you have. You have ~%.0fs more before a hard stop "+
			"— anything not reported by then will be lost.",
		soft.Seconds(), grace.Seconds(),
	)

	r.mu.Lock()
	runCtx := r.ctx
	r.mu.Unlock()
	if runCtx == nil {
		runCtx = context.Background()
	}

	go func() {
		select {
		case <-runCtx.Done():
			return
		case <-time.After(soft):
		}
		for _, id := range workerIDs {
			worker := r.GetWorker(id)
			if worker == nil || !worker.IsActive() || worker.ExplicitReport() != nil {
				continue
			}
			worker.Inject(warning)
		}

		select {
		case <-runCtx.Done():
			return
		case <-time.After(grace):
		}
		for _, id := range workerIDs {
			worker := r.GetWorker(id)
			if worker == nil || !worker.IsActive() {
				continue
			}
			r.StopWorker(id)
		}
	}()
}

func jsonPayload(payload map[string]any, isError bool) (string, bool) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), true
	}
	return string(encoded), isError
}

func taskSpecs(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]map[string]any); ok {
			return typed
		}
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if spec, ok := item.(map[string]any); ok {
			out = append(out, spec)
		}
	}
	return out
}

func mapValue(raw any) map[string]any {
	if value, ok := raw.(map[string]any); ok {
		return value
	}
	return nil
}

func stringValue(raw any) string {
	switch typed := raw.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func stringSliceInput(raw any) ([]string, bool) {
	switch typed := raw.(type) {
	case nil:
		return nil, false
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(stringValue(item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out, true
	default:
		return nil, true
	}
}

func intInput(raw any) (int, bool) {
	switch typed := raw.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(value), true
	case string:
		value, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

func floatInput(raw any) (float64, bool) {
	switch typed := raw.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		value, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return value, true
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

// previewText compacts a task string for the report/breadcrumb preview,
// capping to 200 characters (runes, so multi-byte text is not cut mid-symbol).
func previewText(text string) string {
	preview := strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	runes := []rune(preview)
	if len(runes) > 200 {
		return string(runes[:200]) + "…"
	}
	return preview
}

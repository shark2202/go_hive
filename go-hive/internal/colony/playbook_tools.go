package colony

// Colony wiring for the playbook runner. The pure convergence spine lives in
// internal/playbook; this file resolves the colony binding, turns tracker rows
// into worker dispatches, and records every finish into the colony's
// bookkeeping DB (colony.db).
//
// IMPORTANT — Python playbooks: upstream playbooks are Python scripts
// (`.play.py`) executed in the host process. The Go port keeps its pure-Go
// main binary and runs them on an external interpreter through
// internal/playbook's stdio JSON-RPC bridge (python.go + the embedded
// bootstrap): the same hooks and result envelope as upstream, with
// dispatch_one/query_rows crossing into this colony runtime. A host without
// Python 3.8+ gets an explicit `status=unsupported` and zero dispatches —
// never a fake success.
//
// The other supported form is a native JSON playbook — the Go runner
// equivalent of the Python script's converge() call:
//
//	{
//	  "name": "fix-ci-failures",
//	  "description": "one worker per failed CI job",
//	  "concurrency": 4,
//	  "pending_sql": "SELECT id, job FROM ci_failures WHERE status='open'",
//	  "task": "Reproduce and fix CI job {job} (id {id})",
//	  "goal": "Fixing CI job {job}",
//	  "data_columns": ["id", "job"],
//	  "retries": 1,
//	  "backoff": "exp",
//	  "timeout_seconds": 600,
//	  "max_rounds": 3
//	}
//
// Inline native playbooks are saved to
// `colonies/<id>/playbooks/<sanitized-name>.play.json`; re-running by
// playbook_name resumes by construction because every round re-queries the
// tracker and rows already marked done drop out.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/playbook"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// RunPlaybookToolName is the queen-facing tool for deterministic row batches.
const RunPlaybookToolName = "run_playbook"

const (
	playbookPendingRowCap  = 100000
	playbookDefaultTimeout = 600 * time.Second
)

// playbookRuntime is the slice of the colony runtime a playbook run drives:
// worker fan-out, terminal report wait, and the tracker binding that locates
// both the ledger and the colony's playbooks/ library. The anonymous assertion
// in RunPlaybook keeps RunWorkerSpawner unchanged for existing fakes.
type playbookRuntime interface {
	RunWorkerSpawner
	WaitForWorkerReports(ctx context.Context, ids []string, timeout time.Duration) ([]map[string]any, error)
	TrackerBinding() *tracker.Binding
}

// RunPlaybookToolDef is the upstream tool schema (playbook / playbook_path /
// playbook_name / args), with the execution model stated honestly: this build
// cannot execute Python `.play.py` scripts.
//
// Parity: core/framework/tools/playbook_tools.py::register_playbook_tools
func RunPlaybookToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: RunPlaybookToolName,
		Description: "Run a colony playbook — a deterministic converge loop over a tracker table: query the " +
			"undone rows, dispatch workers, re-query, repeat. Use it when the goal has row shape; use " +
			"run_worker for one-off heterogeneous tasks. Re-running is resume: rows already marked done drop " +
			"out of the pending query, so a second run only does the gap.\n\n" +
			"PYTHON PLAYBOOKS: a `.play.py` script (inline, by path, or saved in the colony library) runs on " +
			"an external Python 3.8+ interpreter found on the host PATH (`python3`, then `python`, or " +
			"HIVE_PYTHON). The script sees the upstream hooks — converge / worker / tracker_query / " +
			"tracker_count / lane / deadletter / log / phase — and returns the upstream envelope " +
			"(meta/result/logs/deadletter/dispatched). If no interpreter is available the call returns " +
			"status='unsupported' with zero workers dispatched; it never fakes a run. Scripts are trusted " +
			"author code, as upstream. The run is synchronous in this port: the call returns when the " +
			"convergence loop finishes or is stopped (no [PLAYBOOK_COMPLETE] callback).\n\n" +
			"SUPPORTED FORM — native JSON playbook:\n" +
			"{\"name\": \"...\", \"description\": \"...\", \"concurrency\": 4,\n" +
			" \"pending_sql\": \"SELECT id, job FROM t WHERE done_at IS NULL\",\n" +
			" \"task\": \"Fix {job}\", \"goal\": \"Fixing {job}\",\n" +
			" \"data_columns\": [\"id\", \"job\"],\n" +
			" \"retries\": 1, \"backoff\": \"exp\", \"timeout_seconds\": 600,\n" +
			" \"max_rounds\": 3}\n\n" +
			"Supply exactly one of `playbook` (inline JSON, saved to " +
			"playbooks/<name>.play.json), `playbook_path` (a JSON/`.play.py` file on disk), or " +
			"`playbook_name` (a saved playbook by meta name — the resume path).\n\n" +
			"REQUIRED FIELDS: name, pending_sql, task. `pending_sql` MUST select the undone ROWS (not " +
			"COUNT(*)); `task`/`goal` are {column} templates. Optional: concurrency (default 8, max 32; " +
			"also bounded by the colony's max_concurrent_workers), data_columns (defaults to the whole row), " +
			"profile, skill, retries, backoff (exp|linear), timeout_seconds, max_rounds (default 1), " +
			"chunk, circuit_breaker.\n\n" +
			"The Go port runs the loop synchronously and returns the final result (no background " +
			"[PLAYBOOK_COMPLETE] notification). Run logs go to the colony's colony.db playbook_runs table.\n\n" +
			"CONTROL: `action` (default 'run') selects what the call does. action='status' lists the runs " +
			"executing right now in this colony plus the recent history from colony.db — use it before " +
			"re-running something that may still be in flight. action='stop' cancels one run by `run_id`: " +
			"no further rows are dispatched, rows already done stay done, and re-running the playbook " +
			"resumes the gap.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"playbook": map[string]any{
					"type":        "string",
					"description": "Inline playbook: a Python script (saved as playbooks/<meta-name>.play.py) or native JSON (saved as .play.json). Pass exactly one of playbook / playbook_name / playbook_path.",
				},
				"playbook_name": map[string]any{
					"type":        "string",
					"description": "Re-run a SAVED playbook by its meta name (playbooks/<name>.play.json, falling back to .play.py) — the normal way to resume or re-run without re-sending the script.",
				},
				"playbook_path": map[string]any{
					"type":        "string",
					"description": "Path to a playbook file on disk (a Python `.play.py` script or native JSON).",
				},
				"args": map[string]any{
					"type":        "object",
					"description": "Optional JSON object passed to the native playbook entrypoint.",
				},
				"action": map[string]any{
					"type": "string",
					"enum": []string{"run", "status", "stop"},
					"description": "'run' (default) executes a playbook. 'status' lists in-flight runs of this colony " +
						"and the recent history from colony.db. 'stop' cancels the run named by `run_id`.",
				},
				"run_id": map[string]any{
					"type":        "string",
					"description": "The run to stop (only with action='stop'), as returned by an earlier run_playbook or listed by action='status'.",
				},
			},
		},
	}
}

// RunPlaybook executes one run_playbook call. It returns the JSON payload and
// whether that payload is a tool error.
func RunPlaybook(ctx context.Context, spawner RunWorkerSpawner, input map[string]any) (string, bool) {
	if spawner == nil {
		return jsonPayload(map[string]any{
			"error": "run_playbook: no colony is bound to this session. Playbooks reconcile a tracker table, " +
				"which needs a colony — create or load a colony before running one.",
		}, true)
	}
	runtime, ok := spawner.(playbookRuntime)
	if !ok {
		return jsonPayload(map[string]any{
			"error": "run_playbook: the bound colony runtime does not expose worker report waiting, so a " +
				"playbook cannot converge. This is a wiring error, not a playbook error.",
		}, true)
	}
	binding := runtime.TrackerBinding()
	if binding == nil || strings.TrimSpace(binding.Dir) == "" || strings.TrimSpace(binding.TrackerDB) == "" {
		return jsonPayload(map[string]any{
			"error": "run_playbook: the bound colony has no tracker binding yet. Start the colony (which " +
				"provisions tracker.db) before running a playbook.",
		}, true)
	}

	// Control actions act on the binding, so they resolve after it and before
	// any playbook script is read.
	switch action := strings.ToLower(strings.TrimSpace(stringValue(input["action"]))); action {
	case "", "run":
		// fall through to the run path below
	case "status":
		return jsonPayload(playbookStatusPayload(binding, intFromAny(input["limit"])), false)
	case "stop":
		payload, isError := playbookStopPayload(binding, stringValue(input["run_id"]))
		return jsonPayload(payload, isError)
	default:
		return jsonPayload(map[string]any{
			"status": "failed",
			"error":  "run_playbook: action=" + action + " is not supported; use 'run', 'status' or 'stop'.",
		}, true)
	}

	scriptText, sourcePath, err := resolvePlaybookScript(
		stringValue(input["playbook"]),
		stringValue(input["playbook_path"]),
		stringValue(input["playbook_name"]),
		filepath.Join(binding.Dir, "playbooks"),
	)
	if err != nil {
		return jsonPayload(map[string]any{"error": "run_playbook: " + err.Error()}, true)
	}

	if isPythonPlaybook(sourcePath, scriptText) {
		return runPythonPlaybook(ctx, runtime, binding, input, scriptText, sourcePath)
	}

	spec, parseErr := parseNativePlaybook(scriptText)
	if parseErr != nil {
		return jsonPayload(map[string]any{
			"status": "failed",
			"error":  "run_playbook: native playbook JSON is invalid: " + parseErr.Error(),
		}, true)
	}

	runID := newPlaybookRunID()
	native, err := buildNativeScript(spec)
	if err != nil {
		return jsonPayload(map[string]any{"status": "failed", "run_id": runID, "error": "run_playbook: " + err.Error()}, true)
	}

	// Persist an inline native playbook so a later run_playbook(playbook_name=…)
	// resumes from the tracker without re-sending the script (upstream parity).
	savedPath := sourcePath
	if strings.TrimSpace(stringValue(input["playbook"])) != "" {
		if path, ok := persistNativePlaybook(binding.Dir, spec.Name, scriptText); ok {
			savedPath = path
		}
	}

	executor := &workerDispatchExecutor{
		runtime: runtime,
		runID:   runID,
	}
	run := playbook.NewPlaybookRun(playbook.RunOptions{
		DispatchOne:    executor.dispatchOne,
		QueryRows:      queryRowsForBinding(binding),
		RunID:          runID,
		ConcurrencyCap: intPointer(runtime.MaxConcurrentWorkers()),
	})
	// The run is reachable by run_id for action="status"/"stop" while it
	// executes, and its context is what stop cancels.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	registerPlaybookRun(&playbookRunHandle{
		runID:     runID,
		name:      spec.Name,
		colonyDir: binding.Dir,
		started:   time.Now(),
		cancel:    cancel,
	})
	defer unregisterPlaybookRun(runID)

	out, runErr := run.Execute(runCtx, native, input["args"])
	stopped := playbookRunWasStopped(runID)
	if stopped && runErr == nil {
		// The cancel may land between two ctx checks, so a run can finish
		// without error; the record still has to say it was stopped.
		runErr = errPlaybookStopped
	}
	logDB := recordPlaybookRun(binding, runID, spec.Name, out, runErr)
	if stopped {
		// A stop is an outcome, not a failure: the queen asked for it.
		payload := playbookRunInfo(runID, spec.Name, savedPath, dispatchedCount(out))
		payload["log_db"] = logDB
		payload["playbook_path"] = savedPath
		return jsonPayload(payload, false)
	}
	if runErr != nil {
		concise, full := playbook.ErrorDetail(runErr)
		return jsonPayload(map[string]any{
			"status":        "failed",
			"run_id":        runID,
			"error":         "playbook failed — " + concise,
			"traceback":     full,
			"log_db":        logDB,
			"playbook_name": spec.Name,
			"playbook_path": savedPath,
			"dispatched":    dispatchedCount(out),
		}, true)
	}

	dead := deadLetterCount(out)
	payload := map[string]any{
		"status":        "completed",
		"run_id":        runID,
		"concurrency":   spec.resolvedConcurrency(),
		"dispatched":    dispatchedCount(out),
		"dead_lettered": dead,
		"result":        out["result"],
		"log_db":        logDB,
		"playbook_name": spec.Name,
		"playbook_path": savedPath,
	}
	if dispatchedCount(out) == 0 {
		payload["hint"] = "dispatched 0 workers. If the table is already converged this is correct; to " +
			"continue later, re-run with run_playbook(playbook_name=…) — done rows are skipped."
	}
	return jsonPayload(payload, false)
}

// isPythonPlaybook decides which runner owns a resolved script. An explicit
// extension wins; an inline body is Python unless it is a JSON object (the
// native form). The JSON-object heuristic matches the previous native parse
// split, so a malformed native playbook still reports a JSON error instead of
// being shipped to the interpreter.
func isPythonPlaybook(sourcePath, scriptText string) bool {
	lower := strings.ToLower(strings.TrimSpace(sourcePath))
	if strings.HasSuffix(lower, ".play.py") {
		return true
	}
	if strings.HasSuffix(lower, ".play.json") {
		return false
	}
	return !looksLikeJSONObject(scriptText)
}

// runPythonPlaybook executes the `.play.py` form: resolve the interpreter,
// load the script synchronously (compile + exec + meta validation), persist an
// inline script to the colony library, then drive the bridge's RPC hooks. The
// payload mirrors the native path and upstream's fast-path result shape.
func runPythonPlaybook(ctx context.Context, runtime playbookRuntime, binding *tracker.Binding, input map[string]any, scriptText, sourcePath string) (string, bool) {
	runID := newPlaybookRunID()
	executor := &workerDispatchExecutor{runtime: runtime, runID: runID}
	handle, err := playbook.StartPythonPlaybook(ctx, scriptText, playbook.PythonOptions{
		DispatchOne: executor.dispatchOne,
		QueryRows:   queryRowsForBinding(binding),
		RunID:       runID,
	})
	if err != nil {
		if playbook.IsPythonUnavailable(err) {
			return unsupportedPythonInterpreterPayload(err, input, sourcePath)
		}
		concise, full := playbook.ErrorDetail(err)
		var scriptErr *playbook.PythonScriptError
		if errors.As(err, &scriptErr) && scriptErr.Stage == "load" {
			// Upstream: "playbook did not start — <root cause>" before any
			// worker exists.
			return jsonPayload(map[string]any{
				"status":        "failed",
				"run_id":        runID,
				"error":         "run_playbook: playbook did not start — " + concise,
				"traceback":     full,
				"playbook_path": sourcePath,
			}, true)
		}
		return jsonPayload(map[string]any{
			"status":    "failed",
			"run_id":    runID,
			"error":     "run_playbook: " + concise,
			"traceback": full,
		}, true)
	}
	defer handle.Close()

	meta := handle.Meta
	name := strings.TrimSpace(stringValue(meta["name"]))
	concurrency, concurrencyErr := playbook.ResolveConcurrency(meta)
	if concurrencyErr != nil {
		return jsonPayload(map[string]any{
			"status": "failed",
			"run_id": runID,
			"error":  "run_playbook: " + concurrencyErr.Error(),
		}, true)
	}

	// Persist an inline script so a later run_playbook(playbook_name=…)
	// resumes from the tracker without re-sending it (upstream saves
	// playbooks/<meta-name>.play.py; running from a path/name already lives
	// there).
	savedPath := sourcePath
	if strings.TrimSpace(stringValue(input["playbook"])) != "" {
		if path, ok := playbook.PersistScript(binding.Dir, name, scriptText); ok {
			savedPath = path
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	registerPlaybookRun(&playbookRunHandle{
		runID:     runID,
		name:      name,
		colonyDir: binding.Dir,
		started:   time.Now(),
		cancel:    cancel,
	})
	defer unregisterPlaybookRun(runID)

	out, runErr := handle.Execute(runCtx, input["args"], concurrency)
	stopped := playbookRunWasStopped(runID)
	if stopped {
		// Upstream records a stopped run as "stopped by stop_playbook"; the
		// underlying context.Canceled is the mechanism, not the outcome.
		runErr = errPlaybookStopped
	}
	logDB := recordPlaybookRun(binding, runID, name, out, runErr)
	if stopped {
		// A stop is an outcome, not a failure: the queen asked for it.
		payload := playbookRunInfo(runID, name, savedPath, dispatchedCount(out))
		payload["log_db"] = logDB
		payload["playbook_path"] = savedPath
		return jsonPayload(payload, false)
	}
	if runErr != nil {
		concise, full := playbook.ErrorDetail(runErr)
		return jsonPayload(map[string]any{
			"status":        "failed",
			"run_id":        runID,
			"error":         "playbook failed — " + concise,
			"traceback":     full,
			"log_db":        logDB,
			"playbook_name": name,
			"playbook_path": savedPath,
			"dispatched":    dispatchedCount(out),
		}, true)
	}

	dead := deadLetterCount(out)
	payload := map[string]any{
		"status":        "completed",
		"run_id":        runID,
		"concurrency":   concurrency,
		"dispatched":    dispatchedCount(out),
		"dead_lettered": dead,
		"result":        out["result"],
		"log_db":        logDB,
		"playbook_name": name,
		"playbook_path": savedPath,
	}
	if dispatchedCount(out) == 0 {
		payload["hint"] = "dispatched 0 workers. If the table is already converged this is correct; to " +
			"continue later, re-run with run_playbook(playbook_name=…) — done rows are skipped."
	}
	return jsonPayload(payload, false)
}

// unsupportedPythonInterpreterPayload is the honest terminal answer when the
// environment has no usable Python: the script is identified, nothing was
// dispatched, and the reason names the lookup that failed.
func unsupportedPythonInterpreterPayload(cause error, input map[string]any, sourcePath string) (string, bool) {
	name := strings.TrimSpace(stringValue(input["playbook_name"]))
	payload := map[string]any{
		"status": "unsupported",
		"error": "run_playbook: Python playbooks need an external Python 3.8+ interpreter, but none is " +
			"available. Install Python 3 or set HIVE_PYTHON to the interpreter path; no workers were " +
			"dispatched and no success is being reported. Detail: " + cause.Error(),
		"playbook_name": name,
		"playbook_path": sourcePath,
		"dispatched":    0,
	}
	if sourcePath == "" && strings.TrimSpace(stringValue(input["playbook"])) != "" {
		payload["playbook_path"] = "(inline script, not saved)"
	}
	return jsonPayload(payload, true)
}

// nativePlaybookSpec is the JSON contract described in RunPlaybookToolDef.
type nativePlaybookSpec struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Concurrency    *int     `json:"concurrency"`
	PendingSQL     string   `json:"pending_sql"`
	Task           string   `json:"task"`
	Goal           string   `json:"goal"`
	Profile        string   `json:"profile"`
	Skill          string   `json:"skill"`
	DataColumns    []string `json:"data_columns"`
	Retries        int      `json:"retries"`
	Backoff        string   `json:"backoff"`
	TimeoutSeconds *float64 `json:"timeout_seconds"`
	MaxRounds      *int     `json:"max_rounds"`
	Chunk          *int     `json:"chunk"`
	CircuitBreaker *float64 `json:"circuit_breaker"`
}

func (s *nativePlaybookSpec) resolvedConcurrency() int {
	meta := map[string]any{}
	if s.Concurrency != nil {
		meta["concurrency"] = *s.Concurrency
	}
	n, err := playbook.ResolveConcurrency(meta)
	if err != nil {
		return playbook.DefaultPlaybookConcurrency
	}
	return n
}

func parseNativePlaybook(script string) (*nativePlaybookSpec, error) {
	trimmed := strings.TrimSpace(script)
	if trimmed == "" {
		return nil, fmt.Errorf("playbook is empty")
	}
	if !looksLikeJSONObject(trimmed) {
		return nil, fmt.Errorf("not a native JSON playbook")
	}
	var spec nativePlaybookSpec
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, fmt.Errorf("native playbook requires a non-empty 'name'")
	}
	if strings.TrimSpace(spec.PendingSQL) == "" {
		return nil, fmt.Errorf("native playbook requires 'pending_sql' (the undone ROWS, not COUNT(*))")
	}
	if strings.TrimSpace(spec.Task) == "" {
		return nil, fmt.Errorf("native playbook requires a 'task' template")
	}
	if !spec.backoffValid() {
		return nil, fmt.Errorf("native playbook 'backoff' must be 'exp' or 'linear' (got %q)", spec.Backoff)
	}
	if spec.TimeoutSeconds != nil && *spec.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("native playbook 'timeout_seconds' must be > 0")
	}
	if spec.MaxRounds != nil && *spec.MaxRounds < 1 {
		return nil, fmt.Errorf("native playbook 'max_rounds' must be >= 1")
	}
	if spec.Chunk != nil && *spec.Chunk < 1 {
		return nil, fmt.Errorf("native playbook 'chunk' must be >= 1")
	}
	if spec.Concurrency != nil && *spec.Concurrency > playbook.MaxPlaybookConcurrency {
		return nil, fmt.Errorf("native playbook 'concurrency'=%d exceeds the maximum %d", *spec.Concurrency, playbook.MaxPlaybookConcurrency)
	}
	return &spec, nil
}

// backoffValid keeps the backoff enum check in one place; an empty value means
// no backoff and is valid.
func (s *nativePlaybookSpec) backoffValid() bool {
	switch strings.ToLower(strings.TrimSpace(s.Backoff)) {
	case "", "exp", "linear":
		return true
	default:
		return false
	}
}

func looksLikeJSONObject(script string) bool {
	return strings.HasPrefix(strings.TrimSpace(script), "{")
}

// buildNativeScript turns the declarative spec into the Go script the pure
// runner executes. converge() owns the parallelism, retry and round loop; the
// tracker remains the source of truth, so a re-run is resume by construction.
func buildNativeScript(spec *nativePlaybookSpec) (*playbook.Script, error) {
	if spec == nil {
		return nil, fmt.Errorf("native playbook is nil")
	}
	concurrency := spec.resolvedConcurrency()
	meta := map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"concurrency": concurrency,
	}
	maxRounds := 1
	if spec.MaxRounds != nil {
		maxRounds = *spec.MaxRounds
	}
	chunk := concurrency
	if spec.Chunk != nil && *spec.Chunk < chunk {
		chunk = *spec.Chunk
	}
	specCopy := *spec

	return &playbook.Script{
		Meta: meta,
		Run: func(ctx context.Context, args any, h *playbook.Hooks) (any, error) {
			var convergeOpts playbook.ConvergeOptions
			convergeOpts.Pending = func() []map[string]any {
				return h.TrackerQuery(specCopy.PendingSQL)
			}
			convergeOpts.MaxRounds = maxRounds
			convergeOpts.Chunk = &chunk
			if specCopy.CircuitBreaker != nil {
				breaker := *specCopy.CircuitBreaker
				convergeOpts.CircuitBreaker = &breaker
			}
			convergeOpts.Dispatch = func(ctx context.Context, row map[string]any, index int) (any, error) {
				opts := playbook.WorkerOptions{
					Profile: specCopy.Profile,
					Skill:   specCopy.Skill,
					Retries: specCopy.Retries,
					Backoff: specCopy.Backoff,
					Data:    playbookData(specCopy.DataColumns, row),
				}
				if goal := renderPlaybookTemplate(specCopy.Goal, row); goal != "" {
					opts.Goal = &goal
				}
				if specCopy.TimeoutSeconds != nil {
					timeout := time.Duration(*specCopy.TimeoutSeconds * float64(time.Second))
					opts.Timeout = &timeout
				}
				return h.Worker(ctx, renderPlaybookTemplate(specCopy.Task, row), opts)
			}
			result := h.Converge(ctx, convergeOpts)
			remaining := h.TrackerCount(specCopy.PendingSQL)
			return map[string]any{
				"rounds":        result.Rounds,
				"converged":     result.Converged,
				"remaining":     remaining,
				"dead_lettered": result.DeadLettered,
				"args":          args,
			}, nil
		},
	}, nil
}

func playbookData(columns []string, row map[string]any) map[string]any {
	if len(columns) == 0 {
		out := make(map[string]any, len(row))
		for key, value := range row {
			out[key] = value
		}
		return out
	}
	out := make(map[string]any, len(columns))
	for _, column := range columns {
		if value, ok := row[column]; ok {
			out[column] = value
		}
	}
	return out
}

func renderPlaybookTemplate(template string, row map[string]any) string {
	out := template
	for key, value := range row {
		out = strings.ReplaceAll(out, "{"+key+"}", fmt.Sprint(value))
		out = strings.ReplaceAll(out, "{{"+key+"}}", fmt.Sprint(value))
	}
	return out
}

type workerDispatchExecutor struct {
	runtime playbookRuntime
	runID   string
}

// dispatchOne spawns one size-1 batch and awaits its terminal report, exactly
// like upstream's colony dispatch closure. It serves both runners: the native
// JSON path calls it through Hooks.Worker, and the Python bridge calls it
// through the `dispatch_one` RPC (where the shim owns retry/backoff/lanes).
// Schema forwarding is absent because the Go BatchTask has no report_schema
// field; the worker still receives its native report_to_parent surface.
func (e *workerDispatchExecutor) dispatchOne(ctx context.Context, task string, opts playbook.DispatchOptions) (map[string]any, error) {
	goal := ""
	if opts.Goal != nil {
		goal = *opts.Goal
	}
	ids, err := e.runtime.SpawnBatchWithOptions(ctx, []BatchTask{{
		Task:        task,
		Data:        opts.Data,
		ProfileName: opts.Profile,
		Goal:        goal,
	}}, SpawnBatchOptions{BatchID: e.runID})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return map[string]any{"status": "failed", "error": "no worker was spawned", "data": map[string]any{}}, nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = playbookDefaultTimeout
	}
	reports, err := e.runtime.WaitForWorkerReports(ctx, ids, timeout)
	if err != nil {
		return nil, err
	}
	if len(reports) == 0 || reports[0] == nil {
		return map[string]any{"status": "failed", "error": "no report from worker", "data": map[string]any{}}, nil
	}
	return reports[0], nil
}

// queryRowsForBinding executes one pending SELECT against the colony's tracker
// and returns row dicts. A query error yields no rows (the runner then treats
// the table as converged); the tracker's own validation and logging are the
// diagnostic surface.
func queryRowsForBinding(binding *tracker.Binding) playbook.QueryRows {
	return func(sql string) []map[string]any {
		result, err := tracker.ExecuteSQL(binding.TrackerDB, sql, playbookPendingRowCap)
		if err != nil {
			return nil
		}
		rowsResult := playbook.ExtractRowsResult(result)
		if rowsResult == nil {
			return nil
		}
		columns := resultColumns(rowsResult["columns"])
		rawRows, _ := rowsResult["rows"].([]any)
		out := make([]map[string]any, 0, len(rawRows))
		for _, rawRow := range rawRows {
			values, ok := rawRow.([]any)
			if !ok {
				continue
			}
			row := make(map[string]any, len(columns))
			for i, column := range columns {
				if i < len(values) {
					row[column] = values[i]
				}
			}
			out = append(out, row)
		}
		return out
	}
}

func resultColumns(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}

// resolvePlaybookScript accepts exactly one of inline / path / saved name.
// Saved names prefer the native .play.json extension and fall back to
// .play.py so a Python-era library still resolves (and now executes on the
// external-interpreter path).
func resolvePlaybookScript(inline, path, name, playbooksDir string) (script, sourcePath string, err error) {
	given := 0
	for _, value := range []string{inline, path, name} {
		if strings.TrimSpace(value) != "" {
			given++
		}
	}
	if given > 1 {
		return "", "", fmt.Errorf("pass exactly ONE of `playbook` (inline), `playbook_path`, or `playbook_name`")
	}
	if strings.TrimSpace(inline) != "" {
		return inline, "", nil
	}
	if strings.TrimSpace(path) != "" {
		expanded := expandPlaybookHome(path)
		info, statErr := os.Stat(expanded)
		if statErr != nil || info.IsDir() {
			return "", "", fmt.Errorf("playbook_path '%s' does not exist or is not a file", path)
		}
		data, readErr := os.ReadFile(expanded)
		if readErr != nil {
			return "", "", fmt.Errorf("failed to read playbook_path: %v", readErr)
		}
		return string(data), expanded, nil
	}
	if strings.TrimSpace(name) != "" {
		base := filepath.Join(playbooksDir, playbook.SanitizeName(name))
		for _, candidate := range []string{base + ".play.json", base + ".play.py"} {
			info, statErr := os.Stat(candidate)
			if statErr != nil || info.IsDir() {
				continue
			}
			data, readErr := os.ReadFile(candidate)
			if readErr != nil {
				return "", "", fmt.Errorf("failed to read saved playbook: %v", readErr)
			}
			return string(data), candidate, nil
		}
		saved := playbook.SavedPlaybookNames(playbooksDir)
		list := "(none yet)"
		if len(saved) > 0 {
			list = fmt.Sprint(saved)
		}
		return "", "", fmt.Errorf("no saved playbook named '%s'. Saved playbooks in this colony: %s", name, list)
	}
	return "", "", fmt.Errorf("provide `playbook` (inline), `playbook_path`, or `playbook_name`")
}

func persistNativePlaybook(colonyDir, name, script string) (string, bool) {
	target := filepath.Join(colonyDir, "playbooks", playbook.SanitizeName(name)+".play.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", false
	}
	if err := os.WriteFile(target, []byte(script), 0o644); err != nil {
		return "", false
	}
	return target, true
}

// recordPlaybookRun persists the run narration and result in the colony's
// bookkeeping DB, `colonies/<id>/colony.db` (table playbook_runs, columns
// status/dispatched/dead_lettered/error/result_json/logs_json). This is the
// upstream colony.db shape, chosen over a separate JSONL so one store answers
// "what ran" and "what did it log".
func recordPlaybookRun(binding *tracker.Binding, runID, name string, out map[string]any, runErr error) string {
	dbPath := filepath.Join(binding.Dir, "colony.db")
	status := "done"
	var errorText *string
	if runErr != nil {
		status = "error"
		_, full := playbook.ErrorDetail(runErr)
		errorText = &full
	}
	nameCopy := name
	in := PlaybookRunInput{
		RunID:        runID,
		Name:         &nameCopy,
		Status:       status,
		Dispatched:   dispatchedCount(out),
		DeadLettered: deadLetterCount(out),
		Error:        errorText,
		Logs:         playbookLogs(out),
	}
	if out != nil {
		in.Result = out["result"]
	}
	if err := RecordPlaybookRun(dbPath, in); err != nil {
		return ""
	}
	return dbPath
}

func dispatchedCount(out map[string]any) int {
	if out == nil {
		return 0
	}
	switch value := out["dispatched"].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func deadLetterCount(out map[string]any) int {
	if out == nil {
		return 0
	}
	switch value := out["deadletter"].(type) {
	case []map[string]any:
		return len(value)
	case []any:
		return len(value)
	default:
		return 0
	}
}

func playbookLogs(out map[string]any) []string {
	if out == nil {
		return nil
	}
	switch value := out["logs"].(type) {
	case []string:
		return value
	case []any:
		lines := make([]string, 0, len(value))
		for _, line := range value {
			lines = append(lines, fmt.Sprint(line))
		}
		return lines
	default:
		return nil
	}
}

func newPlaybookRunID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "pb_" + hex.EncodeToString(buf[:])
	}
	return "pb_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func intPointer(value int) *int { return &value }

func expandPlaybookHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

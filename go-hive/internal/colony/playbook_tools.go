package colony

// Colony wiring for the playbook runner. The pure convergence spine lives in
// internal/playbook; this file resolves the colony binding, turns tracker rows
// into worker dispatches, and records every finish into the colony's
// bookkeeping DB (colony.db).
//
// IMPORTANT — Python playbooks: upstream playbooks are Python scripts
// (`.play.py`) executed in the host process. The Go port has no Python
// interpreter and its runner takes a Go *playbook.Script, so a Python script
// is loaded (or at least resolved) and then reported as UNSUPPORTED. It never
// claims to have dispatched workers it did not dispatch.
//
// The supported form is a native JSON playbook — the Go runner equivalent of
// the Python script's converge() call:
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
			"PYTHON IS NOT EXECUTABLE IN THIS BUILD. A `.play.py` script (inline, by path, or saved in the " +
			"colony library) is resolved and then returned with status='unsupported' and ZERO workers " +
			"dispatched. Do not retry it expecting a different result.\n\n" +
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
			"[PLAYBOOK_COMPLETE] notification). Run logs go to the colony's colony.db playbook_runs table.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"playbook": map[string]any{
					"type":        "string",
					"description": "Inline playbook. This build supports native JSON; a Python script is returned as unsupported. Pass exactly one of playbook / playbook_name / playbook_path.",
				},
				"playbook_name": map[string]any{
					"type":        "string",
					"description": "Re-run a SAVED playbook by its meta name (playbooks/<name>.play.json, falling back to .play.py) — the normal way to resume or re-run without re-sending the script.",
				},
				"playbook_path": map[string]any{
					"type":        "string",
					"description": "Path to a playbook file on disk (native JSON or .play.py).",
				},
				"args": map[string]any{
					"type":        "object",
					"description": "Optional JSON object passed to the native playbook entrypoint.",
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

	scriptText, sourcePath, err := resolvePlaybookScript(
		stringValue(input["playbook"]),
		stringValue(input["playbook_path"]),
		stringValue(input["playbook_name"]),
		filepath.Join(binding.Dir, "playbooks"),
	)
	if err != nil {
		return jsonPayload(map[string]any{"error": "run_playbook: " + err.Error()}, true)
	}

	spec, parseErr := parseNativePlaybook(scriptText)
	if parseErr != nil {
		if looksLikeJSONObject(scriptText) {
			return jsonPayload(map[string]any{
				"status": "failed",
				"error":  "run_playbook: native playbook JSON is invalid: " + parseErr.Error(),
			}, true)
		}
		return unsupportedPythonPayload(sourcePath, input, err)
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

	executor := &nativePlaybookExecutor{
		runtime: runtime,
		runID:   runID,
	}
	run := playbook.NewPlaybookRun(playbook.RunOptions{
		DispatchOne:    executor.dispatchOne,
		QueryRows:      queryRowsForBinding(binding),
		RunID:          runID,
		ConcurrencyCap: intPointer(runtime.MaxConcurrentWorkers()),
	})
	out, runErr := run.Execute(ctx, native, input["args"])
	logDB := recordPlaybookRun(binding, runID, spec.Name, out, runErr)
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

// unsupportedPythonPayload is the honest terminal answer for a Python script:
// the file was resolved (the payload names it), but this build cannot execute
// it and dispatched nothing.
func unsupportedPythonPayload(sourcePath string, input map[string]any, resolveErr error) (string, bool) {
	name := strings.TrimSpace(stringValue(input["playbook_name"]))
	payload := map[string]any{
		"status": "unsupported",
		"error": "run_playbook: this Go build cannot execute Python playbooks. The runner is Go-native and " +
			"there is no Python interpreter in the process, so no workers were dispatched and no success " +
			"is being reported. Use the native JSON playbook form (see the tool description) or run the " +
			"script on a Python Hive host.",
		"playbook_name": name,
		"playbook_path": sourcePath,
		"dispatched":    0,
	}
	if sourcePath == "" && strings.TrimSpace(stringValue(input["playbook"])) != "" {
		payload["playbook_path"] = "(inline script, not saved)"
	}
	if resolveErr != nil {
		payload["error"] = "run_playbook: " + resolveErr.Error() + ". " + payload["error"].(string)
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

type nativePlaybookExecutor struct {
	runtime playbookRuntime
	runID   string
}

// dispatchOne spawns one size-1 batch and awaits its terminal report, exactly
// like upstream's colony dispatch closure. Schema forwarding is absent because
// the Go BatchTask has no report_schema field; the worker still receives its
// native report_to_parent surface.
func (e *nativePlaybookExecutor) dispatchOne(ctx context.Context, task string, opts playbook.DispatchOptions) (map[string]any, error) {
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
// .play.py so a Python-era library is still discoverable (and reported as
// unsupported, never silently ignored).
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

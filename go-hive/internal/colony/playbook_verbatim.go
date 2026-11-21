package colony

// Upstream-verbatim payload builders for the `.play.py` playbook path.
//
// Every string and every field name here is copied from
// core/framework/tools/playbook_tools.py::run_playbook / _zero_dispatch_hint /
// _notify_complete. The point is FIELD-LEVEL PARITY: a queen (or a test) that
// knows the upstream JSON/notice shape must not be able to tell which
// implementation produced it. Keep this file side-by-side with that source when
// reviewing — deviations belong in docs/playbook-python.md §8, not here.

import (
	"fmt"
	"strings"
)

// playbookCapRuntime is the runtime seam a playbook run uses to raise the
// colony's worker admission cap for its duration (upstream mutates
// colony_config.max_concurrent_workers). A runtime double without the method
// keeps its cap: the raise is an optimization for admitting meta['concurrency'],
// never a correctness requirement.
type playbookCapRuntime interface {
	SetMaxConcurrentWorkers(capacity int) int
}

// playbookCompleteNoticeForDone builds the notice data for a successful
// background run: upstream `_notify_complete(out=...)`.
func playbookCompleteNoticeForDone(runID, name, logDB string, out map[string]any) map[string]any {
	return playbookCompleteNoticeFields(
		runID, name, "done", logDB, "", playbookZeroDispatchHint(out),
		playbookResultJSON(out["result"]),
		dispatchedCount(out), deadLetterCount(out), playbookLogTail(out))
}

// playbookCompleteNoticeForError builds the notice data for a failed or
// stopped background run: upstream `_notify_complete(error=...)`.
func playbookCompleteNoticeForError(runID, name, logDB, errorText string) map[string]any {
	return playbookCompleteNoticeFields(runID, name, "error", logDB, errorText, "", "", 0, 0, nil)
}

// playbookZeroDispatchHint is upstream `_zero_dispatch_hint` verbatim.
func playbookZeroDispatchHint(out map[string]any) string {
	if dispatchedCount(out) != 0 {
		return ""
	}
	return "dispatched 0 workers. If the table is already converged this is correct. " +
		"If you expected work: (1) `pending` must SELECT the undone ROWS (e.g. " +
		"`SELECT id, url FROM t WHERE done_at IS NULL`), NOT `SELECT COUNT(*)` " +
		"(which returns one row); (2) dispatch via `await converge(pending=..., " +
		"dispatch=lambda row, i: worker(...))` — worker() is async and converge " +
		"awaits it; calling worker() in a bare loop without await runs nothing; " +
		"(3) tracker_query returns a list of row dicts — index a row's column " +
		"(row['url']), not the list."
}

// playbookPathValue renders a saved path the way a Python f-string would:
// None when the inline persist failed, else the path text.
func playbookPathValue(path *string) any {
	if path == nil {
		return nil
	}
	return *path
}

func playbookPathText(path *string) string {
	if path == nil {
		return "None"
	}
	return *path
}

// playbookPathString is the empty-string variant used by the folded control
// payloads, where "None" (a Python repr) would be noise.
func playbookPathString(path *string) string {
	if path == nil {
		return ""
	}
	return *path
}

// playbookFailedStartPayload is upstream's synchronous load-failure payload
// (playbook_tools.py: after `run.load(script)` raises PlaybookScriptError).
func playbookFailedStartPayload(runID, concise, traceback string) map[string]any {
	return map[string]any{
		"status":    "failed",
		"run_id":    runID,
		"error":     "playbook did not start — " + concise,
		"traceback": traceback,
	}
}

// playbookConcurrencyErrorPayload is upstream's concurrency-validation failure
// payload (the cerr branch).
func playbookConcurrencyErrorPayload(runID, message string) map[string]any {
	return map[string]any{
		"status": "failed",
		"run_id": runID,
		"error":  message,
	}
}

// playbookFailedPayload is upstream's fast-path failure payload.
func playbookFailedPayload(runID, concise, traceback, logDB string) map[string]any {
	return map[string]any{
		"status":    "failed",
		"run_id":    runID,
		"error":     "playbook failed — " + concise,
		"traceback": traceback,
		"log_db":    logDB,
	}
}

// playbookCompletedPayload is upstream's fast-path success payload. `path` is
// nil when an inline script could not be persisted (upstream emits null).
func playbookCompletedPayload(runID string, concurrency, dispatched, deadLettered int, result any, logDB, name string, path *string) map[string]any {
	payload := map[string]any{
		"status":        "completed",
		"run_id":        runID,
		"concurrency":   concurrency,
		"dispatched":    dispatched,
		"dead_lettered": deadLettered,
		"result":        result,
		"log_db":        logDB,
		"playbook_name": name,
		"playbook_path": playbookPathValue(path),
	}
	return payload
}

// playbookCompletedPayloadWithHint adds the zero-dispatch hint exactly where
// upstream does (only when dispatched == 0).
func playbookCompletedPayloadWithHint(runID string, concurrency, dispatched, deadLettered int, result any, logDB, name string, path *string, out map[string]any) map[string]any {
	payload := playbookCompletedPayload(runID, concurrency, dispatched, deadLettered, result, logDB, name, path)
	if hint := playbookZeroDispatchHint(out); hint != "" {
		payload["hint"] = hint
	}
	return payload
}

// playbookStartedMessage is upstream's started `message` verbatim. The
// get_playbook_status / stop_playbook names it mentions are answerable in this
// port: the flat tool surface routes both names onto run_playbook's actions.
func playbookStartedMessage(runID string, concurrency int, name string, path *string) string {
	return fmt.Sprintf(
		"Playbook is dispatching workers in the background, %d at a time (your meta['concurrency']). "+
			"Routine outcomes (retry, dead-letter) are handled by the convergence loop; you'll receive a "+
			"[PLAYBOOK_COMPLETE] notification when it finishes (status=done or error). Check progress "+
			"anytime with get_playbook_status(run_id='%s') or kill it with stop_playbook(run_id='%s'). "+
			"Saved to the colony library as '%s' — TO RESUME (after a stop, or after editing %s), call "+
			"run_playbook(playbook_name='%s'); done rows are skipped.",
		concurrency, runID, runID, name, playbookPathText(path), name)
}

// playbookStartedPayload is upstream's async acknowledgement. `out` is unused
// (upstream does not know the outcome yet) but kept for symmetry with the
// completed builder in call sites.
func playbookStartedPayload(runID string, concurrency int, logDB, name string, path *string) map[string]any {
	return map[string]any{
		"status":        "started",
		"run_id":        runID,
		"concurrency":   concurrency,
		"log_db":        logDB,
		"playbook_name": name,
		"playbook_path": playbookPathValue(path),
		"message":       playbookStartedMessage(runID, concurrency, name, path),
	}
}

// playbookCompleteNoticeFields is the data one [PLAYBOOK_COMPLETE] event
// carries. Upstream builds the message text in `_notify_complete`; the Go port
// renders the same text when the notice is published, and the queen's report
// subscription passes it through unformatted (see FormatPlaybookComplete).
func playbookCompleteNoticeFields(runID, name, status, logDB, errText, hint, resultJSON string, dispatched, deadLettered int, logTail []string) map[string]any {
	note := map[string]any{
		"worker_id":       "playbook:" + runID,
		"status":          status,
		"playbook_run_id": runID,
		"playbook_name":   name,
	}
	if status == "done" {
		note["dispatched"] = dispatched
		note["dead_lettered"] = deadLettered
		if resultJSON != "" {
			note["result_json"] = resultJSON
		}
		if hint != "" {
			note["hint"] = hint
		}
		if logTail != nil {
			note["log_tail"] = logTail
		}
	}
	if logDB != "" {
		note["log_db"] = logDB
	}
	if errText != "" {
		note["error"] = errText
	}
	note["summary"] = FormatPlaybookCompleteNotice(note)
	return note
}

// FormatPlaybookCompleteNotice renders the exact upstream `_notify_complete`
// message for a notice data map.
func FormatPlaybookCompleteNotice(data map[string]any) string {
	runID := stringValue(data["playbook_run_id"])
	if runID == "" {
		runID = strings.TrimPrefix(stringValue(data["worker_id"]), "playbook:")
	}
	status := stringValue(data["status"])
	logDB := stringValue(data["log_db"])
	logLine := ""
	if logDB != "" {
		logLine = "<log_db>" + logDB + "</log_db>\n"
	}
	if status != "done" {
		return "[PLAYBOOK_COMPLETE]\n" +
			"<run_id>" + runID + "</run_id>\n" +
			"<status>error</status>\n" +
			logLine +
			"<error>" + stringValue(data["error"]) + "</error>"
	}
	resultJSON := stringValue(data["result_json"])
	if resultJSON == "" {
		resultJSON = "null"
	}
	hintLine := ""
	if hint := stringValue(data["hint"]); hint != "" {
		hintLine = "<hint>" + hint + "</hint>\n"
	}
	logTail := playbookStringSlice(data["log_tail"])
	return "[PLAYBOOK_COMPLETE]\n" +
		"<run_id>" + runID + "</run_id>\n" +
		"<status>done</status>\n" +
		logLine +
		fmt.Sprintf("<dispatched>%d</dispatched>\n", intValue(data["dispatched"])) +
		fmt.Sprintf("<dead_lettered>%d</dead_lettered>\n", intValue(data["dead_lettered"])) +
		"<result>" + resultJSON + "</result>\n" +
		hintLine +
		"<log_tail>\n" + strings.Join(logTail, "\n") + "\n</log_tail>"
}

// playbookResultJSON matches upstream `json.dumps(result, default=str)` for
// the values a JSON-RPC playbook can return. Go's map iteration order is not
// Python's insertion order; keys are sorted (field-equivalent, see
// docs/playbook-python.md §8).
func playbookResultJSON(result any) string {
	return pyJSONDumps(result)
}

func playbookLogTail(out map[string]any) []string {
	logs := playbookLogs(out)
	if len(logs) > 10 {
		logs = logs[len(logs)-10:]
	}
	return logs
}

func playbookStringSlice(value any) []string {
	switch typed := value.(type) {
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

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

package colony

// Playbook run control: `run_playbook(action="status")` and
// `run_playbook(action="stop")`.
//
// Upstream exposes two separate tools (get_playbook_status / stop_playbook in
// core/framework/tools/playbook_tools.py) over a long-lived playbook task: a
// run is started in the background, tracked by run_id, and can be peeked at or
// killed. The Go runner executes synchronously inside the tool call, so there
// is nothing to peek at from another turn — but there IS something to stop:
// the run's context. This file keeps a colony-scoped registry of the runs that
// are executing right now, so the queen can list them and cancel one by id.
//
// History comes from the colony's own bookkeeping DB (playbook_runs in
// colony.db), the same store recordPlaybookRun writes. A run that is stopped
// is still recorded, with error="stopped by stop_playbook", so history never
// shows a run that simply vanished.
//
// Names: the action rides on the existing run_playbook tool (one tool, one
// schema) rather than adding two new tool names the session catalogue would
// have to advertise.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// playbookStatusHistoryLimit caps the history rows a status call returns.
const playbookStatusHistoryLimit = 20

// playbookRunHandle is one in-flight run.
type playbookRunHandle struct {
	runID     string
	name      string
	colonyDir string
	started   time.Time
	cancel    context.CancelFunc
	stopped   bool
}

var playbookRunRegistry = struct {
	mu   sync.Mutex
	runs map[string]*playbookRunHandle
}{runs: map[string]*playbookRunHandle{}}

func registerPlaybookRun(handle *playbookRunHandle) {
	if handle == nil || handle.runID == "" {
		return
	}
	playbookRunRegistry.mu.Lock()
	playbookRunRegistry.runs[handle.runID] = handle
	playbookRunRegistry.mu.Unlock()
}

func unregisterPlaybookRun(runID string) {
	playbookRunRegistry.mu.Lock()
	delete(playbookRunRegistry.runs, runID)
	playbookRunRegistry.mu.Unlock()
}

// markPlaybookRunStopped flags a run and cancels its context. ok is false when
// the run is unknown or already finished.
func markPlaybookRunStopped(colonyDir, runID string) (handle *playbookRunHandle, ok bool) {
	playbookRunRegistry.mu.Lock()
	defer playbookRunRegistry.mu.Unlock()
	handle, ok = playbookRunRegistry.runs[runID]
	if !ok || handle.stopped {
		return nil, false
	}
	if colonyDir != "" && handle.colonyDir != colonyDir {
		// Scoped by colony identity: one session's stop never reaches another
		// colony's run (upstream stop_playbooks_for_colony).
		return nil, false
	}
	handle.stopped = true
	return handle, true
}

// playbookRunWasStopped reports whether stop cancelled this run.
func playbookRunWasStopped(runID string) bool {
	playbookRunRegistry.mu.Lock()
	defer playbookRunRegistry.mu.Unlock()
	handle, ok := playbookRunRegistry.runs[runID]
	return ok && handle.stopped
}

// runningPlaybookRows lists the in-flight runs of one colony, oldest first.
func runningPlaybookRows(colonyDir string, now time.Time) []map[string]any {
	playbookRunRegistry.mu.Lock()
	defer playbookRunRegistry.mu.Unlock()
	rows := make([]map[string]any, 0, len(playbookRunRegistry.runs))
	for _, handle := range playbookRunRegistry.runs {
		if colonyDir != "" && handle.colonyDir != colonyDir {
			continue
		}
		rows = append(rows, map[string]any{
			"run_id":          handle.runID,
			"playbook_name":   handle.name,
			"status":          "running",
			"started_at":      handle.started.Format(time.RFC3339),
			"elapsed_seconds": int(now.Sub(handle.started).Seconds()),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i]["started_at"].(string) < rows[j]["started_at"].(string)
	})
	return rows
}

// playbookStatusPayload answers action="status": what is running now plus the
// colony's recorded history.
func playbookStatusPayload(binding *tracker.Binding, limit int) map[string]any {
	if limit <= 0 || limit > playbookStatusHistoryLimit {
		limit = playbookStatusHistoryLimit
	}
	dbPath := filepath.Join(binding.Dir, "colony.db")
	history := []map[string]any{}
	if runs, err := ListPlaybookRuns(dbPath, limit); err == nil {
		for _, run := range runs {
			row := map[string]any{
				"run_id":        run.RunID,
				"status":        run.Status,
				"dispatched":    run.Dispatched,
				"dead_lettered": run.DeadLettered,
				"created_at":    run.CreatedAt,
			}
			if run.Name != nil {
				row["playbook_name"] = *run.Name
			}
			if run.Error != nil {
				row["error"] = *run.Error
			}
			history = append(history, row)
		}
	}
	running := runningPlaybookRows(binding.Dir, time.Now())
	return map[string]any{
		"status":        "ok",
		"running":       running,
		"runs":          history,
		"running_count": len(running),
		"log_db":        dbPath,
		"note": "This build runs a playbook inside the tool call, so `running` only ever holds a run started by " +
			"another turn of this session that has not returned yet. Finished runs come from colony.db.",
	}
}

// playbookStopPayload answers action="stop": cancel one run by id.
func playbookStopPayload(binding *tracker.Binding, runID string) (map[string]any, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return map[string]any{
			"status": "failed",
			"error":  "run_playbook(action=\"stop\") needs `run_id`. Use action=\"status\" to list the runs of this colony.",
		}, true
	}
	handle, ok := markPlaybookRunStopped(binding.Dir, runID)
	if !ok {
		return map[string]any{
			"status":  "not_running",
			"run_id":  runID,
			"message": "No running playbook with that run_id in this colony (already finished?).",
		}, false
	}
	if handle.cancel != nil {
		handle.cancel()
	}
	return map[string]any{
		"status":  "stopped",
		"run_id":  runID,
		"message": "Cancelled the run's context: no further rows will be dispatched. Rows already done stay done, so re-running the playbook resumes the gap.",
	}, false
}

// playbookRunInfo is the payload a stopped run returns instead of a failure.
func playbookRunInfo(runID, name, path string, dispatched int) map[string]any {
	return map[string]any{
		"status":        "stopped",
		"run_id":        runID,
		"playbook_name": name,
		"playbook_path": path,
		"dispatched":    dispatched,
	}
}

// errPlaybookStopped is recorded in colony.db for a run cancelled by
// action="stop", mirroring upstream's "stopped by stop_playbook".
var errPlaybookStopped = errors.New("stopped by stop_playbook")

// intFromAny coerces a JSON number or numeric string parameter.
func intFromAny(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

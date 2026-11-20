package agentloop

// Colony worker-fleet snapshot for the queen at tool-budget checkpoints.
//
// Sister source to TrackerSnapshotReminderSource: fires at the same
// TOOL_BUDGET_CHECKPOINT lifecycle point, but is queen-only and shows the live
// worker fleet (active list + active/total counts) so a queen grinding through
// her own tool calls is reminded what the rest of the colony is doing.
//
// Distinct from ActiveWorkersReminderSource, which fires at USER_PROMPT_SUBMIT:
// this one fires mid-turn when the queen has been the one burning through
// tools, not the workers.
//
// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py

import (
	"strconv"
	"strings"
)

const (
	// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::_MAX_LISTED
	colonyWorkerSnapshotMaxListed = 8
	// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::_TASK_BODY_MAX
	colonyWorkerSnapshotTaskMax = 80
)

// ColonyWorkerSnapshotReminderSource is the queen-only, in-colony-only fleet
// snapshot at tool-budget checkpoints. The double gate (queen stream AND a
// binding provider) keeps it silent for workers, pre-fork queens, and
// independent-mode queens.
//
// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::ColonyWorkerSnapshotReminderSource
type ColonyWorkerSnapshotReminderSource struct{}

// NewColonyWorkerSnapshotReminderSource builds the source (stateless; the
// budget checkpoint itself provides the cadence).
func NewColonyWorkerSnapshotReminderSource() *ColonyWorkerSnapshotReminderSource {
	return &ColonyWorkerSnapshotReminderSource{}
}

// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::ColonyWorkerSnapshotReminderSource.name
func (s *ColonyWorkerSnapshotReminderSource) Name() string { return "colony_worker_snapshot" }

// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::ColonyWorkerSnapshotReminderSource.points
func (s *ColonyWorkerSnapshotReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{TOOL_BUDGET_CHECKPOINT}
}

// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::ColonyWorkerSnapshotReminderSource.applies_to
func (s *ColonyWorkerSnapshotReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	if agentCtx == nil || !agentCtx.IsQueenStream {
		return false
	}
	// Provider presence, not its current return value: applies_to runs once at
	// bind time, but a queen forks mid-session. Render re-resolves the binding.
	return agentCtx.ColonyBindingProvider != nil
}

// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::ColonyWorkerSnapshotReminderSource.render
func (s *ColonyWorkerSnapshotReminderSource) Render(rctx ReminderContext) (string, bool) {
	if rctx.Point != TOOL_BUDGET_CHECKPOINT {
		return "", false
	}
	if resolveTrackerBinding(rctx.AgentCtx) == nil {
		return "", false
	}
	stats := safeColonyStats(rctx.AgentCtx)
	workers := safeActiveWorkers(rctx.AgentCtx)
	active := stats.Active
	if active == 0 && len(workers) > 0 {
		active = len(workers)
	}
	total := stats.Total
	if total == 0 {
		total = active
	}
	if active == 0 && total == 0 {
		return "", false
	}
	return renderColonyWorkerSnapshotBody(workers, active, total), true
}

func safeColonyStats(agentCtx *ReminderAgentContext) (stats ColonyStats) {
	if agentCtx == nil || agentCtx.ColonyStatsProvider == nil {
		return ColonyStats{}
	}
	defer func() {
		if recover() != nil {
			stats = ColonyStats{}
		}
	}()
	return agentCtx.ColonyStatsProvider()
}

func safeActiveWorkers(agentCtx *ReminderAgentContext) (workers []ActiveWorker) {
	if agentCtx == nil || agentCtx.ActiveWorkersProvider == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			workers = nil
		}
	}()
	return agentCtx.ActiveWorkersProvider()
}

// Parity: framework/agent_loop/colony_worker_snapshot_reminder.py::_render_body
func renderColonyWorkerSnapshotBody(workers []ActiveWorker, active, total int) string {
	lines := []string{"Colony fleet: " + strconv.Itoa(active) + " active worker(s), " + strconv.Itoa(total) + " total this session."}
	listed := workers
	if len(listed) > colonyWorkerSnapshotMaxListed {
		listed = listed[:colonyWorkerSnapshotMaxListed]
	}
	if len(listed) > 0 {
		lines = append(lines, "")
		for _, worker := range listed {
			id := worker.ID
			if id == "" {
				id = "?"
			}
			status := worker.Status
			if status == "" {
				status = "?"
			}
			task := strings.TrimSpace(worker.Task)
			if len(task) > colonyWorkerSnapshotTaskMax {
				task = strings.TrimSpace(task[:colonyWorkerSnapshotTaskMax-1]) + "…"
			}
			if task != "" {
				lines = append(lines, "  - "+id+" ["+status+"]: "+task)
			} else {
				lines = append(lines, "  - "+id+" ["+status+"]")
			}
		}
		if len(workers) > colonyWorkerSnapshotMaxListed {
			lines = append(lines, "  ... and "+strconv.Itoa(len(workers)-colonyWorkerSnapshotMaxListed)+" more")
		}
	}
	lines = append(lines, "")
	lines = append(lines,
		"Don't re-dispatch in-flight tasks. If you're tool-busy because "+
			"workers are pending, prefer waiting on their WORKER_REPORTs over "+
			"running their work yourself.")
	return strings.Join(lines, "\n")
}

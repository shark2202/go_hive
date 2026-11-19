package agentloop

import (
	"fmt"
	"strings"
)

// Parity: framework/agent_loop/active_workers_reminder.py ACTIVE_WORKERS_SNAPSHOT_MAX
var ActiveWorkersSnapshotMax = envInt("HIVE_ACTIVE_WORKERS_SNAPSHOT_MAX", 8)

// Parity: framework/agent_loop/active_workers_reminder.py TASK_BODY_MAX
var ActiveWorkersTaskMax = envInt("HIVE_ACTIVE_WORKERS_TASK_MAX", 80)

// ActiveWorkersReminderSource re-surfaces in-flight parallel work to the queen
// on every user turn while a batch is still draining. Stateless: the presence
// of live workers is the only gate, so it goes quiet on its own.
//
// Parity: framework/agent_loop/active_workers_reminder.py::ActiveWorkersReminderSource
type ActiveWorkersReminderSource struct{}

// Parity: framework/agent_loop/active_workers_reminder.py::ActiveWorkersReminderSource
func NewActiveWorkersReminderSource() *ActiveWorkersReminderSource {
	return &ActiveWorkersReminderSource{}
}

// Parity: framework/agent_loop/active_workers_reminder.py::ActiveWorkersReminderSource.name
func (s *ActiveWorkersReminderSource) Name() string { return "active_workers" }

// Parity: framework/agent_loop/active_workers_reminder.py::ActiveWorkersReminderSource.points
func (s *ActiveWorkersReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{USER_PROMPT_SUBMIT}
}

// Parity: framework/agent_loop/active_workers_reminder.py::ActiveWorkersReminderSource.applies_to
func (s *ActiveWorkersReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	return agentCtx != nil && agentCtx.IsQueenStream
}

// Parity: framework/agent_loop/active_workers_reminder.py::ActiveWorkersReminderSource.render
func (s *ActiveWorkersReminderSource) Render(rctx ReminderContext) (string, bool) {
	workers := activeWorkersFor(rctx.AgentCtx)
	if len(workers) == 0 {
		return "", false
	}
	return renderActiveWorkersBody(workers), true
}

// Parity: framework/agent_loop/active_workers_reminder.py::_list_active_workers
func activeWorkersFor(agentCtx *ReminderAgentContext) (workers []ActiveWorker) {
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

// Parity: framework/agent_loop/active_workers_reminder.py::_render_body
func renderActiveWorkersBody(workers []ActiveWorker) string {
	count := len(workers)
	plural := "s"
	if count == 1 {
		plural = ""
	}
	lines := []string{
		fmt.Sprintf("You have %d parallel worker%s still running from earlier "+
			"fan-out (run_playbook or run_worker). Their reports arrive as "+
			"WORKER_REPORT messages as each finishes — don't re-dispatch the "+
			"same tasks or summarize the batch yet.", count, plural),
		"",
	}
	shown := workers
	if ActiveWorkersSnapshotMax > 0 && len(shown) > ActiveWorkersSnapshotMax {
		shown = shown[:ActiveWorkersSnapshotMax]
	}
	for _, worker := range shown {
		id := worker.ID
		if id == "" {
			id = "?"
		}
		status := worker.Status
		if status == "" {
			status = "?"
		}
		task := strings.TrimSpace(worker.Task)
		if ActiveWorkersTaskMax > 0 {
			if runes := []rune(task); len(runes) > ActiveWorkersTaskMax {
				task = strings.TrimSpace(string(runes[:ActiveWorkersTaskMax-1])) + "…"
			}
		}
		if task != "" {
			lines = append(lines, fmt.Sprintf("  - %s [%s]: %s", id, status, task))
		} else {
			lines = append(lines, fmt.Sprintf("  - %s [%s]", id, status))
		}
	}
	if rest := count - len(shown); rest > 0 {
		lines = append(lines, fmt.Sprintf("  ... and %d more", rest))
	}
	lines = append(lines, "")
	lines = append(lines, "If the user's message asks about progress, prefer get_worker_status over re-running tools yourself.")
	return strings.Join(lines, "\n")
}

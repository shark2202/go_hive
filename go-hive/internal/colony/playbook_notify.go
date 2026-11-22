package colony

// Background playbook completion: the [PLAYBOOK_COMPLETE] notice.
//
// Parity: framework/tools/playbook_tools.py::_notify_complete
//
// Upstream injects one `[PLAYBOOK_COMPLETE]` turn into the queen's session when
// a background run finishes. The Go port publishes the same notice on the
// colony eventbus as a subagent report; SubscribeQueenReportsWithWorker passes
// a playbook notice through verbatim (the text is rendered by
// FormatPlaybookCompleteNotice to the upstream template), while worker reports
// keep their [WORKER_REPORT] formatting. A runtime with no bus records the run
// only — the notification is degraded, never faked.

import (
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// playbookRunNotifier is the runtime seam a background run uses to announce
// its completion. ColonyRuntime implements it; a test double may not, and then
// the run is still recorded (the notification is simply skipped).
type playbookRunNotifier interface {
	NotifyPlaybookComplete(notice map[string]any)
}

// NotifyPlaybookComplete publishes one completion notice for a background run.
// `notice` is the data built by playbookCompleteNoticeFields: it already
// carries the upstream-verbatim `summary` text. It never blocks on a listener
// failure: Publish is the bus's own contract.
func (r *ColonyRuntime) NotifyPlaybookComplete(notice map[string]any) {
	if r == nil || r.bus == nil || notice == nil {
		return
	}
	payload := make(map[string]any, len(notice)+1)
	for key, value := range notice {
		payload[key] = value
	}
	status := stringValue(payload["status"])
	if status == "" {
		status = "done"
	}
	payload["status"] = status
	event := eventbus.NewAgentEvent(eventbus.EventSubagentReport, r.streamID)
	event.Data = payload
	colonyID := r.streamID
	event.ColonyID = &colonyID
	event.Timestamp = time.Now()
	r.bus.Publish(event)
}

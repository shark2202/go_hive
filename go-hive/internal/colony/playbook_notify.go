package colony

// Background playbook completion: the [PLAYBOOK_COMPLETE] notice.
//
// Parity: framework/tools/playbook_tools.py::_notify_complete
//
// Upstream injects a [PLAYBOOK_COMPLETE] turn into the queen's session once a
// background run finishes. The Go runner publishes the same notice on the
// colony event bus as a subagent report, which is the port's one live channel
// from a colony to the queen: SubscribeQueenReports turns every such report
// into a formatted turn the queen reads, and the session mirror keeps it
// visible in the dashboard. A runtime with no bus records the run only — the
// notification is degraded, never faked.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

// playbookRunNotifier is the runtime seam a background run uses to announce
// its completion. ColonyRuntime implements it; a test double may not, and then
// the run is still recorded (the notification is simply skipped).
type playbookRunNotifier interface {
	NotifyPlaybookComplete(runID, name, resultJSON string)
}

// NotifyPlaybookComplete publishes one completion notice for a background run.
// It never blocks on a listener failure: Publish is the bus's own contract.
func (r *ColonyRuntime) NotifyPlaybookComplete(runID, name, resultJSON string) {
	if r == nil || r.bus == nil {
		return
	}
	status := "done"
	var parsed map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err == nil {
		if value, ok := parsed["status"].(string); ok && value != "" {
			status = value
		}
	}
	summary := "[PLAYBOOK_COMPLETE] playbook '" + name + "' (" + runID + ") finished with status=" + status
	if parsed != nil {
		if dispatched, ok := parsed["dispatched"]; ok {
			summary += fmt.Sprintf(", dispatched=%v", dispatched)
		}
		if message, ok := parsed["error"].(string); ok && strings.TrimSpace(message) != "" {
			summary += " — " + firstLine(message)
		}
		if hint, ok := parsed["hint"].(string); ok && strings.TrimSpace(hint) != "" {
			summary += " (" + hint + ")"
		}
	}
	payload := map[string]any{
		// The colony report channel is keyed by a worker id; a playbook run is
		// not a worker, so the id is namespaced instead of borrowing one.
		"worker_id":       "playbook:" + runID,
		"status":          status,
		"summary":         summary,
		"playbook_run_id": runID,
		"playbook_name":   name,
		"playbook_result": parsed,
	}
	event := eventbus.NewAgentEvent(eventbus.EventSubagentReport, r.streamID)
	event.Data = payload
	colonyID := r.streamID
	event.ColonyID = &colonyID
	event.Timestamp = time.Now()
	r.bus.Publish(event)
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

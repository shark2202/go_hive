package eventbus

import "strings"

var WorkerMetaTypes = map[string]struct{}{
	"execution_started":   {},
	"node_loop_started":   {},
	"task_created":        {},
	"task_updated":        {},
	"task_deleted":        {},
	"subagent_report":     {},
	"execution_completed": {},
	"execution_failed":    {},
	"worker_completed":    {},
	"worker_failed":       {},
	"payment_required":    {},
}

var WorkerFaninTypes = WorkerMetaTypes

func IsWorkerStream(streamID string) bool {
	return strings.HasPrefix(streamID, "worker")
}

func IsFanoutWorkerStream(streamID string) bool {
	return strings.HasPrefix(streamID, "worker:")
}

func IsWorkerLocal(streamID string, eventType string) bool {
	if !IsFanoutWorkerStream(streamID) {
		return false
	}
	_, isMeta := WorkerMetaTypes[eventType]
	return !isMeta
}

// Parity: framework/server/routes_events.py::_parse_watch
func ParseWatch(raw string) (watchAll bool, watched map[string]struct{}) {
	raw = strings.TrimSpace(raw)
	watched = map[string]struct{}{}
	if raw == "*" {
		return true, watched
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "*" {
			continue
		}
		watched[part] = struct{}{}
	}
	return false, watched
}

// Parity: framework/server/routes_events.py::is_suppressed_for_client
func IsSuppressedForClient(evt map[string]any, watchAll bool, watched map[string]struct{}) bool {
	if watchAll {
		return false
	}
	streamID, _ := evt["stream_id"].(string)
	eventType, _ := evt["type"].(string)
	if !IsWorkerLocal(streamID, eventType) {
		return false
	}
	_, ok := watched[streamID]
	return !ok
}

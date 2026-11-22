package server

// Parity: framework/server/routes_events.py

import "github.com/shark2202/go_hive/go-hive/internal/eventbus"

var WorkerEventAllowlist = eventbus.WorkerFaninTypes

// Parity: framework/server/routes_events.py::_is_worker_noise
func IsWorkerNoise(streamID *string, eventType eventbus.EventType) bool {
	if streamID == nil {
		return false
	}
	return eventbus.IsWorkerLocal(*streamID, eventType.String())
}

package runtimehealth

// Parity: framework/host/runtime_health.py

import (
	"strings"
	"sync"
	"time"
)

var networkErrorMarkers = []string{
	"name resolution",
	"name or service not known",
	"cannot connect to host",
	"connection refused",
	"connection reset",
	"network is unreachable",
	"no route to host",
	"ssl handshake",
	"ssl: handshake_failure",
	"remote disconnected",
	"connect timeout",
	"connectionerror",
}

// Parity: framework/host/runtime_health.py::RuntimeNetworkSnapshot
type Snapshot struct {
	Degraded       bool     `json:"degraded"`
	Reason         *string  `json:"reason"`
	SinceEpoch     *float64 `json:"since_epoch"`
	LastEventEpoch *float64 `json:"last_event_epoch"`
}

const maxReasonChars = 240

var (
	mu    sync.Mutex
	state = Snapshot{Degraded: false}
)

// Parity: framework/host/runtime_health.py::is_upstream_network_error
func IsUpstreamNetworkError(text string) bool {
	lowered := strings.ToLower(text)
	for _, marker := range networkErrorMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// Parity: framework/host/runtime_health.py::mark_upstream_degraded
func MarkUpstreamDegraded(reason string) {
	now := epochNow()
	mu.Lock()
	defer mu.Unlock()
	if !state.Degraded {
		state.SinceEpoch = &now
	}
	state.Degraded = true
	trimmed := truncateChars(reason, maxReasonChars)
	state.Reason = &trimmed
	state.LastEventEpoch = &now
}

// Parity: framework/host/runtime_health.py::mark_upstream_healthy
func MarkUpstreamHealthy() {
	mu.Lock()
	defer mu.Unlock()
	if !state.Degraded {
		return
	}
	now := epochNow()
	state.Degraded = false
	state.Reason = nil
	state.SinceEpoch = nil
	state.LastEventEpoch = &now
}

// Parity: framework/host/runtime_health.py::get_runtime_network
func Current() Snapshot {
	mu.Lock()
	defer mu.Unlock()
	return Snapshot{
		Degraded:       state.Degraded,
		Reason:         copyString(state.Reason),
		SinceEpoch:     copyFloat(state.SinceEpoch),
		LastEventEpoch: copyFloat(state.LastEventEpoch),
	}
}

func Reset() {
	mu.Lock()
	defer mu.Unlock()
	state = Snapshot{Degraded: false}
}

func epochNow() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func truncateChars(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

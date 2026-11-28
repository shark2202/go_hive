package colony

// Worker lifecycle vocabulary shared by the runtime, the worker registry and
// the HTTP read surface.
//
// Parity: framework/host/worker.py::WorkerStatus
//
// A25: the port briefly carried a second, in-process WorkerScheduler with its
// own pending queue (the pre-runtime admission model). It had no production
// caller — the runtime's own admission gate in runtime.go is the single queue —
// so keeping it only offered a second place for queue semantics to drift. The
// scheduler is gone; the status vocabulary it housed lives here because the
// runtime, the worker registry and the server read surface all use it.

import (
	"os"
	"strconv"
	"strings"
)

// DefaultMaxConcurrentWorkers is the admission cap when a colony declares
// none: HIVE_MAX_CONCURRENT_WORKERS, else four.
//
// Parity: framework/host/colony_runtime.py::_DEFAULT_MAX_CONCURRENT_WORKERS
var DefaultMaxConcurrentWorkers = colonyEnvInt("HIVE_MAX_CONCURRENT_WORKERS", 4)

func colonyEnvInt(name string, def int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func colonyEnvBool(name string, def bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

type WorkerStatus string

const (
	WorkerQueued    WorkerStatus = "queued"
	WorkerPending   WorkerStatus = "pending"
	WorkerRunning   WorkerStatus = "running"
	WorkerCompleted WorkerStatus = "completed"
	WorkerFailed    WorkerStatus = "failed"
	WorkerStopped   WorkerStatus = "stopped"
)

func (s WorkerStatus) IsActive() bool {
	return s == WorkerQueued || s == WorkerPending || s == WorkerRunning
}

func (s WorkerStatus) IsRunningWorker() bool {
	return s == WorkerPending || s == WorkerRunning
}

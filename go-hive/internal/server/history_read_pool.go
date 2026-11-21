package server

// Parity: framework/server/routes_sessions.py::_history_read_executor
//
// Upstream keeps a dedicated, bounded thread pool for latency-sensitive UI
// disk reads (events/history tails plus the cold-session listings) so a
// session switch never queues behind tool spillover or a busy LLM turn. The
// pool size is `max(4, int(os.environ.get("HIVE_HISTORY_READ_WORKERS", "8")))`;
// Go has goroutines rather than a thread pool, so the port keeps the same
// contract as a bounded semaphore shared by exactly those read paths.
//
// Upstream does not guard the int() call: a non-numeric value raises
// ValueError out of the handler. The port mirrors that by surfacing an error
// the route turns into a 500 instead of silently substituting a size.

import (
	"sync"

	"github.com/aden-hive/hive-go/internal/pyenv"
)

const historyReadMinWorkers = 4

// historyReadPool is a shared semaphore sized once per process.
type historyReadPool struct {
	workers int
	slots   chan struct{}
}

var historyReadPoolState struct {
	mu   sync.Mutex
	pool *historyReadPool
	err  error
}

// envIntOrError is the package-local spelling of the upstream read that has no
// exception handler (`int(os.environ.get(name, "8"))`): unset is the default, a
// present-but-unparsable value is an error.
func envIntOrError(name string, def int) (int, bool, error) {
	return pyenv.IntOrError(name, def)
}

// historyReadWorkers resolves the env override with upstream's semantics:
// unset -> 8, otherwise int() (raise on garbage), floored at 4.
func historyReadWorkers() (int, error) {
	parsed, _, err := envIntOrError("HIVE_HISTORY_READ_WORKERS", 8)
	if err != nil {
		return 0, err
	}
	if parsed < historyReadMinWorkers {
		parsed = historyReadMinWorkers
	}
	return parsed, nil
}

// historyReadExecutor returns the process-wide pool, building it on first use.
func historyReadExecutor() (*historyReadPool, error) {
	historyReadPoolState.mu.Lock()
	defer historyReadPoolState.mu.Unlock()
	if historyReadPoolState.pool != nil {
		return historyReadPoolState.pool, nil
	}
	if historyReadPoolState.err != nil {
		return nil, historyReadPoolState.err
	}
	workers, err := historyReadWorkers()
	if err != nil {
		historyReadPoolState.err = err
		return nil, err
	}
	historyReadPoolState.pool = &historyReadPool{workers: workers, slots: make(chan struct{}, workers)}
	return historyReadPoolState.pool, nil
}

// HistoryReadWorkers reports the resolved pool size (diagnostics and tests).
func HistoryReadWorkers() (int, error) {
	pool, err := historyReadExecutor()
	if err != nil {
		return 0, err
	}
	return pool.workers, nil
}

// acquire blocks until a slot is free. The returned release is idempotent.
func (p *historyReadPool) acquire() func() {
	p.slots <- struct{}{}
	var once sync.Once
	return func() { once.Do(func() { <-p.slots }) }
}

// withHistoryReadSlot runs fn inside a pool slot. It returns the pool error
// unchanged so the caller can answer the same failure upstream's exception
// would produce.
func withHistoryReadSlot(fn func()) error {
	pool, err := historyReadExecutor()
	if err != nil {
		return err
	}
	release := pool.acquire()
	defer release()
	fn()
	return nil
}

// resetHistoryReadPoolForTest drops the memoised pool so a test can change
// HIVE_HISTORY_READ_WORKERS between cases.
func resetHistoryReadPoolForTest() {
	historyReadPoolState.mu.Lock()
	historyReadPoolState.pool = nil
	historyReadPoolState.err = nil
	historyReadPoolState.mu.Unlock()
}

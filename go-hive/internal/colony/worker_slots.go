package colony

// workerSlotGate is the colony's worker admission gate with a MUTABLE
// capacity.
//
// Upstream raises the colony's `max_concurrent_workers` for the duration of a
// playbook run — `max(original, meta['concurrency'])` — so the declared
// concurrency is actually admitted by the scheduler, then restores it when the
// run finishes (framework/tools/playbook_tools.py: the `_restore_cap` closure
// set right before `asyncio.create_task(run.run_loaded(args))` and called on
// both the fast path and the background finisher). The Go port's admission
// gate used to be a fixed-size buffered channel created at runtime
// construction, which cannot be resized; this gate keeps the same
// block-until-admitted semantics but lets the cap move while workers wait.
//
// Invariants:
//   - inFlight never exceeds capacity at admission time; lowering the capacity
//     below inFlight lets the excess drain to a terminal state (the same slack
//     upstream's replacing scheduler has).
//   - a capacity change wakes every waiter so a raise is observed
//     immediately.

import (
	"context"
	"sync"
)

type workerSlotGate struct {
	mu       sync.Mutex
	capacity int
	inFlight int
	wake     chan struct{}
}

func newWorkerSlotGate(capacity int) *workerSlotGate {
	if capacity < 1 {
		capacity = 1
	}
	return &workerSlotGate{capacity: capacity, wake: make(chan struct{})}
}

// acquire blocks until a slot is free or ctx is done. true means the caller
// holds a slot and must release it.
func (g *workerSlotGate) acquire(ctx context.Context) bool {
	if g == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		if g.inFlight < g.capacity {
			g.inFlight++
			g.mu.Unlock()
			return true
		}
		wake := g.wake
		g.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return false
		}
	}
}

func (g *workerSlotGate) release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.inFlight > 0 {
		g.inFlight--
	}
	g.broadcastLocked()
	g.mu.Unlock()
}

func (g *workerSlotGate) setCapacity(capacity int) {
	if g == nil {
		return
	}
	if capacity < 1 {
		capacity = 1
	}
	g.mu.Lock()
	if g.capacity != capacity {
		g.capacity = capacity
		g.broadcastLocked()
	}
	g.mu.Unlock()
}

func (g *workerSlotGate) currentCapacity() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capacity
}

// broadcastLocked wakes every waiter. The caller holds mu.
func (g *workerSlotGate) broadcastLocked() {
	close(g.wake)
	g.wake = make(chan struct{})
}

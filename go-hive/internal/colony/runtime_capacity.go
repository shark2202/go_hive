package colony

// MaxConcurrentWorkers reports the colony's worker admission cap. run_worker
// echoes it so the queen can tell running-now from queued peers.
func (r *ColonyRuntime) MaxConcurrentWorkers() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxConcurrent
}

// SetMaxConcurrentWorkers moves the colony's admission cap and returns the
// previous value. A playbook run raises the cap to
// max(current, meta['concurrency']) while it executes and restores it when the
// run finishes — upstream's temporary raise around
// `run.run_loaded(args)` (framework/tools/playbook_tools.py).
func (r *ColonyRuntime) SetMaxConcurrentWorkers(capacity int) int {
	if r == nil {
		return 0
	}
	if capacity < 1 {
		capacity = 1
	}
	r.mu.Lock()
	previous := r.maxConcurrent
	r.maxConcurrent = capacity
	gate := r.workerSlots
	r.mu.Unlock()
	gate.setCapacity(capacity)
	return previous
}

package colony

// Runtime read/control surface consumed by the HTTP route seams.
//
// The route layer declares its own view structs (WorkerInfoView, ColonyTool,
// LiveWorkerSnapshot) because it must not import host internals into its
// handlers. The methods here expose the same state in colony types so the
// assembly layer can map it once instead of standing up fake runtimes.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime (status & query)
// Parity: framework/server/routes_colony_workers.py::_resolve_colony_runtime
// Parity: framework/server/routes_workers.py::handle_stop_live_worker

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// DefaultStopWorkersTimeout mirrors framework/host/worker.py::STOP_TIMEOUT_SEC:
// one wedged worker must not hang a stop sweep.
const DefaultStopWorkersTimeout = 10 * time.Second

// ColonyTool is the name/description pair the colony tools route renders.
type ColonyTool struct {
	Name        string
	Description string
}

// LiveWorkerState is one row of the live worker registry
// (framework/server/routes_workers.py::_build_live_workers_payload).
//
// StartedAt is wall-clock epoch seconds, matching WorkerInfo.StartedAt and the
// route's default Now(); zero means "never started". ResultStatus and
// ResultSummary are nil until the worker reaches a terminal state.
type LiveWorkerState struct {
	ID             string
	Task           string
	Status         string
	IsActive       bool
	StartedAt      float64
	ExplicitReport map[string]any
	ResultStatus   *string
	ResultSummary  *string
}

// ListWorkers returns a snapshot of every worker the runtime still tracks,
// terminal ones included (framework/host/colony_runtime.py::list_workers).
// Python dict order is insertion order; Go maps are unordered, so the rows are
// sorted by worker ID to keep responses stable.
func (r *ColonyRuntime) ListWorkers() []WorkerInfo {
	out := []WorkerInfo{}
	if r == nil {
		return out
	}
	for _, worker := range r.workerList() {
		if worker == nil {
			continue
		}
		out = append(out, worker.Info())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out
}

// Worker returns one worker's info. The bool reports registry membership
// (framework/host/colony_runtime.py::get_worker, info view).
func (r *ColonyRuntime) Worker(workerID string) (WorkerInfo, bool) {
	if r == nil {
		return WorkerInfo{}, false
	}
	worker := r.GetWorker(workerID)
	if worker == nil {
		return WorkerInfo{}, false
	}
	return worker.Info(), true
}

// LiveWorkers returns the registry rows the workers stream/stop routes read.
func (r *ColonyRuntime) LiveWorkers() []LiveWorkerState {
	out := []LiveWorkerState{}
	if r == nil {
		return out
	}
	for _, worker := range r.workerList() {
		if worker == nil {
			continue
		}
		out = append(out, worker.liveState())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LiveWorker returns one registry row, mirroring routes_workers.py's
// colony._workers[worker_id] lookup.
func (r *ColonyRuntime) LiveWorker(workerID string) (LiveWorkerState, bool) {
	if r == nil {
		return LiveWorkerState{}, false
	}
	worker := r.GetWorker(workerID)
	if worker == nil {
		return LiveWorkerState{}, false
	}
	return worker.liveState(), true
}

func (w *Worker) liveState() LiveWorkerState {
	info := w.Info()
	state := LiveWorkerState{
		ID:             info.WorkerID,
		Task:           info.Task,
		Status:         string(info.Status),
		IsActive:       info.Status.IsActive(),
		StartedAt:      info.StartedAt,
		ExplicitReport: w.ExplicitReport(),
	}
	if info.Result != nil {
		status := info.Result.ReportStatus
		summary := info.Result.Summary
		state.ResultStatus = &status
		state.ResultSummary = &summary
	}
	return state
}

// StopLiveWorker stops one worker through the same cascade as StopWorkers and
// returns its summary. Already-terminal workers stop nothing and report an
// empty sweep, matching ColonyRuntime.stop_worker.
func (r *ColonyRuntime) StopLiveWorker(workerID string) (map[string]any, error) {
	if r == nil {
		return nil, errors.New("colony: nil colony runtime")
	}
	worker := r.GetWorker(workerID)
	if worker == nil {
		return nil, fmt.Errorf("colony: unknown worker %q", workerID)
	}
	previous := r.setDispatchBlocked(true)
	defer r.setDispatchBlocked(previous)
	return r.stopWorkerSet([]*Worker{worker}), nil
}

// StopAllLiveWorkers is the stop-all route's cascade
// (routes_workers.py::handle_stop_all_live_workers -> colony.stop_workers()).
func (r *ColonyRuntime) StopAllLiveWorkers() map[string]any {
	summary, err := r.StopWorkers(false)
	if err != nil || summary == nil {
		return map[string]any{}
	}
	return summary
}

// StopWorkers is THE stop cascade (ColonyRuntime.stop_workers): queued workers
// are cancelled before they start, live workers are cancelled and awaited
// concurrently under one timeout, and the summary matches the Python keys.
//
// Dispatch stays blocked for the duration of the sweep so a concurrent queen
// turn cannot start a worker that survives the stop. When blockDispatch is
// true the block is held afterwards until UnblockDispatch (the next user turn);
// a spawn never re-opens it — SpawnBatch refuses while the gate is closed,
// matching Python's _dispatch_blocked admission check.
func (r *ColonyRuntime) StopWorkers(blockDispatch bool) (map[string]any, error) {
	if r == nil {
		return nil, errors.New("colony: nil colony runtime")
	}
	previous := r.setDispatchBlocked(true)
	if !blockDispatch {
		defer r.setDispatchBlocked(previous)
	}
	return r.stopWorkerSet(r.workerList()), nil
}

func (r *ColonyRuntime) stopWorkerSet(targets []*Worker) map[string]any {
	stopped := []string{}
	timedOut := []string{}
	queuedCancelled := 0
	waiting := make([]*Worker, 0, len(targets))

	for _, worker := range targets {
		if worker == nil || !worker.IsActive() {
			continue
		}
		wasQueued := worker.Status() == WorkerQueued
		stoppedWorker, cancelled := r.cancelWorker(worker.ID)
		if !cancelled {
			continue
		}
		if wasQueued {
			queuedCancelled++
		}
		stopped = append(stopped, worker.ID)
		waiting = append(waiting, stoppedWorker)
	}

	timeout := r.effectiveStopTimeout()
	deadline := time.Now().Add(timeout)
	for _, worker := range waiting {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			timedOut = append(timedOut, worker.ID)
			r.forceStopWorker(worker, timeout)
			continue
		}
		timer := time.NewTimer(remaining)
		select {
		case <-worker.Done():
		case <-timer.C:
			// A worker that swallowed the cancel is force-marked terminal,
			// upstream's Worker.stop timeout branch: anything left
			// non-terminal is an orphan the scheduler can still promote.
			timedOut = append(timedOut, worker.ID)
			r.forceStopWorker(worker, timeout)
		}
		timer.Stop()
	}

	// Release the stopped workers' browser tab groups once, concurrently,
	// after the bounded wait — upstream's _reap_worker_browsers step. Doing it
	// here (not per StopWorker) keeps an unresponsive bridge from multiplying
	// its 5 s budget by the batch size.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime._reap_worker_browsers
	r.reapWorkerBrowsers(waiting)

	sort.Strings(stopped)
	sort.Strings(timedOut)
	return map[string]any{
		"stopped":          stopped,
		"stopped_count":    len(stopped),
		"queued_cancelled": queuedCancelled,
		"timed_out":        timedOut,
		"errors":           nil,
	}
}

// ColonyID is the colony's identifier (framework/host/colony_runtime.py's
// colony_id property, which delegates to the stream id).
func (r *ColonyRuntime) ColonyID() string {
	if r == nil {
		return ""
	}
	return r.streamID
}

// StoragePath is the root the runtime writes worker directories under. The
// worker-dir resolver falls back to it when the colony-layout directory is
// absent (routes_colony_workers.py::_resolve_worker_dir).
func (r *ColonyRuntime) StoragePath() string {
	if r == nil {
		return ""
	}
	return r.storagePath
}

// Skills returns the colony's skill catalog. The bool reports whether a
// catalog is available at all; a live runtime always has one (upstream's
// SkillsManager is always constructed), while nil reports unavailable.
func (r *ColonyRuntime) Skills() ([]*skills.ParsedSkill, bool) {
	if r == nil || r.skillsProvider == nil {
		return nil, false
	}
	return r.skillsProvider()
}

// Tools returns the colony's default tool set plus the name->provider map the
// tools route attributes rows with (ColonyRuntime._tools /
// _tool_provider_map).
func (r *ColonyRuntime) Tools() ([]ColonyTool, map[string]string) {
	if r == nil {
		return []ColonyTool{}, map[string]string{}
	}
	out := make([]ColonyTool, 0, len(r.tools))
	for _, def := range r.tools {
		out = append(out, ColonyTool{Name: def.Name, Description: def.Description})
	}
	providers := make(map[string]string, len(r.toolProviderMap))
	for name, provider := range r.toolProviderMap {
		providers[name] = provider
	}
	return out, providers
}

// WorkerLLM exposes the completer workers use so the model route can swap its
// model in place (routes_models.py: session.Colony.WorkerLLM()). Falls back to
// the colony completer when no separate worker model is configured.
func (r *ColonyRuntime) WorkerLLM() any {
	if r == nil {
		return nil
	}
	if r.workerLLM != nil {
		return r.workerLLM
	}
	return r.llm
}

// LLM exposes the colony's own completer (the overseer's transport, shared
// with workers when no worker_llm is configured). The queen-model route uses
// it to follow a queen_llm write for her colony-bound sessions: upstream's
// colony holds the session's provider by reference when worker_llm is unset,
// so a queen reconfigure reaches it; this seam is the Go equivalent.
//
// Parity: framework/server/queen_models.py::apply (s.llm reconfigure reaches
// the colony through the shared provider object)
func (r *ColonyRuntime) LLM() any {
	if r == nil {
		return nil
	}
	return r.llm
}

// BoundWorkersForProfile lists the live workers dispatched from a worker
// profile. routes_colonies_profiles.go duck-types this seam to refuse profile
// deletion while workers still use it.
func (r *ColonyRuntime) BoundWorkersForProfile(profileName string) []string {
	out := []string{}
	if r == nil || profileName == "" {
		return out
	}
	for _, worker := range r.workerList() {
		if worker == nil || worker.ID == "" {
			continue
		}
		if worker.ProfileName == profileName {
			out = append(out, worker.ID)
		}
	}
	sort.Strings(out)
	return out
}

// SubscribeToEvents wires a handler onto the colony bus so the session layer
// can treat the runtime as its event source (server.ColonyRuntime seam).
func (r *ColonyRuntime) SubscribeToEvents(eventTypes []eventbus.EventType, handler eventbus.EventHandler, opts ...eventbus.SubscribeOption) (string, error) {
	if r == nil || r.bus == nil {
		return "", errors.New("colony: no event bus on this runtime")
	}
	return r.bus.Subscribe(eventTypes, handler, opts...), nil
}

// InjectInput queues a client message for a worker's running loop, mirroring
// ColonyRuntime.send_to_worker. Unknown or inactive workers report an error so
// the caller (escalation queue-full reply) can log a real failure.
func (r *ColonyRuntime) InjectInput(workerID, content string) error {
	if r == nil {
		return errors.New("colony: nil colony runtime")
	}
	worker := r.GetWorker(workerID)
	if worker == nil {
		return fmt.Errorf("colony: unknown worker %q", workerID)
	}
	if !worker.Inject(content) {
		return fmt.Errorf("colony: worker %q is not accepting input", workerID)
	}
	return nil
}

// BlockDispatch stops queued workers from starting. The stop cascade uses the
// same gate so a worker admitted mid-sweep cannot survive it.
func (r *ColonyRuntime) BlockDispatch() { r.setDispatchBlocked(true) }

// UnblockDispatch re-opens worker admission.
func (r *ColonyRuntime) UnblockDispatch() { r.setDispatchBlocked(false) }

// DispatchBlocked reports whether worker admission is currently gated.
func (r *ColonyRuntime) DispatchBlocked() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dispatchBlocked
}

// setDispatchBlocked sets the gate and returns its previous value so callers
// can restore it.
func (r *ColonyRuntime) setDispatchBlocked(blocked bool) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.dispatchBlocked
	r.dispatchBlocked = blocked
	return previous
}

// WorkerList returns the runtime's worker handles (live and terminal). It is
// the pointer-level companion of ListWorkers for callers that need worker
// methods (IsActive / IsPersistent / ExplicitReport) rather than the info view.
func (r *ColonyRuntime) WorkerList() []*Worker { return r.workerList() }

// workerList copies the registry so callers never hold r.mu while touching
// individual workers.
func (r *ColonyRuntime) workerList() []*Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Worker, 0, len(r.workers))
	for _, worker := range r.workers {
		out = append(out, worker)
	}
	return out
}

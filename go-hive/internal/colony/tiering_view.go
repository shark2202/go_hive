package colony

// Parity: framework/server/routes_sessions.py::handle_session_live_tools (worker tool-tiering view)

import (
	"sort"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// WorkerToolTiering is one worker's eager/searchable split for the debug
// panel's live-tools view. Enabled is false when the worker carries no
// ToolTierState (no keep-set configured), meaning it holds its full spawn
// toolset eagerly.
type WorkerToolTiering struct {
	WorkerID        string
	Enabled         bool
	EagerTotal      int
	SearchableTotal int
	LoadedViaSearch []string
}

// WorkerToolTiering reports the per-worker tool tiering view.
//
// Python iterates ColonyRuntime._workers (a dict, insertion-ordered); Go maps
// are unordered, so the workers are sorted by ID to keep the response stable.
func (r *ColonyRuntime) WorkerToolTiering() []WorkerToolTiering {
	out := []WorkerToolTiering{}
	if r == nil {
		return out
	}
	r.mu.Lock()
	workers := make(map[string]*Worker, len(r.workers))
	ids := make([]string, 0, len(r.workers))
	for id, worker := range r.workers {
		workers[id] = worker
		ids = append(ids, id)
	}
	r.mu.Unlock()
	sort.Strings(ids)

	for _, id := range ids {
		worker := workers[id]
		var ctxAvailable int
		var tier *tools.ToolTierState
		if worker != nil && worker.Context != nil {
			ctxAvailable = len(worker.Context.AvailableTools)
			tier, _ = worker.Context.ToolTierState.(*tools.ToolTierState)
		}
		if tier == nil {
			out = append(out, WorkerToolTiering{
				WorkerID:   id,
				Enabled:    false,
				EagerTotal: ctxAvailable,
			})
			continue
		}
		out = append(out, WorkerToolTiering{
			WorkerID:        id,
			Enabled:         true,
			EagerTotal:      len(tier.GetCurrentTools()),
			SearchableTotal: len(tier.GetSearchableTools()),
			LoadedViaSearch: append([]string{}, tier.LoadedToolNames...),
		})
	}
	return out
}

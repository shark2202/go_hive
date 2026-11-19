package colony

// Tracker wiring for a live colony.
//
// A colony owns exactly one tracker.db — the shared ledger the queen designs
// (tracker_sql), whitelists for writers (tracker_register_writable), and the
// workers fill in (tracker_upsert / tracker_query). Three seams make that real:
//
//  1. Start provisions the DB (upstream: the sibling-colony fork calls
//     ensure_tracker_db before the colony's worker.json is written).
//  2. Every spawned worker carries the binding in its InputData (upstream:
//     run_worker stamps {"binding": binding.to_dict()} onto each task spec).
//  3. The worker tool table advertises role="worker" tracker tools and the
//     executor dispatches them against the worker's binding.
//
// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
// Parity: framework/tools/queen_lifecycle_tools.py::run_worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// colonyMetadataFile marks an on-disk colony directory. Worker transcripts and
// plain DM roots don't carry it, so the runtime only provisions a tracker for
// a real colony.
const colonyMetadataFile = "metadata.json"

// isColonyDir reports whether path is a materialised colony (metadata.json is
// written by the colony create/fork flow before any worker runs).
func isColonyDir(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(path, colonyMetadataFile))
	return err == nil && info.Mode().IsRegular()
}

// TrackerBinding returns the binding of the tracker the colony's tools target,
// or nil when this runtime is not a colony (DM overhead) or Start has not
// provisioned it yet.
func (r *ColonyRuntime) TrackerBinding() *tracker.Binding {
	if r == nil {
		return nil
	}
	r.trackerMu.Lock()
	defer r.trackerMu.Unlock()
	return r.trackerBinding
}

// provisionTracker creates tracker.db on first use and records the binding.
// It is idempotent and safe to call from every lifecycle entry point; a
// storage path that is not a colony is left alone so a DM session never grows
// a stray tracker.
func (r *ColonyRuntime) provisionTracker() error {
	if r == nil {
		return nil
	}
	r.trackerMu.Lock()
	defer r.trackerMu.Unlock()
	if r.trackerBinding != nil {
		return nil
	}
	if !isColonyDir(r.storagePath) {
		return nil
	}
	binding, err := tracker.BindingForColonyDir(r.storagePath, r.streamID)
	if err != nil {
		return err
	}
	r.trackerBinding = binding
	return nil
}

// trackerToolDefs converts the role-filtered tracker table into model-facing
// definitions. Workers get tracker_upsert + tracker_query; queen-only tools
// (tracker_sql, tracker_register_writable) never appear in the worker table.
func trackerToolDefs(role string) []llm.ToolDef {
	registry := tracker.NewRegistry(nil)
	if err := tracker.RegisterTrackerTools(registry, role); err != nil {
		return nil
	}
	defs := make([]llm.ToolDef, 0, len(registry.Names()))
	for _, name := range registry.Names() {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		defs = append(defs, llm.ToolDef{
			Name:            tool.Name,
			Description:     tool.Description,
			Parameters:      tool.Parameters,
			ConcurrencySafe: tool.ConcurrencySafe,
		})
	}
	return defs
}

// workerToolDefs is the colony's tool surface plus the tracker tools every
// worker needs. The queen's tool list (r.tools) is shared by the overseer, so
// the append happens here rather than mutating r.tools; the base surface goes
// through the declared-tool merge because this is a worker-only list.
func (r *ColonyRuntime) workerToolDefs() []llm.ToolDef {
	return appendMissingToolDefs(r.baseWorkerTools(), trackerToolDefs("worker")...)
}

// workerTrackerRegistry binds the worker role tools to the worker's own
// binding. Workers receive it through InputData at spawn; the runtime binding
// is the fallback for a worker built by hand.
func (r *ColonyRuntime) workerTrackerRegistry(worker *Worker) *tracker.Registry {
	resolve := func() (*tracker.Binding, error) {
		if worker != nil && worker.Context != nil {
			if binding := tracker.BindingFromInputData(worker.Context.InputData); binding != nil {
				return binding, nil
			}
		}
		return r.TrackerBinding(), nil
	}
	registry := tracker.NewRegistry(resolve)
	if err := tracker.RegisterTrackerTools(registry, "worker"); err != nil {
		return nil
	}
	return registry
}

// workerInputData mirrors upstream run_worker's spec stamping: the task text
// survives, the colony binding is authoritative (it comes after the caller's
// data), and the worker's identity rides along for its first turn. Batch ids
// stay off input_data — Worker already carries them for report correlation and
// they would only add prompt noise.
func (r *ColonyRuntime) workerInputData(data map[string]any, workerID, task string) map[string]any {
	out := make(map[string]any, len(data)+4)
	for key, value := range data {
		out[key] = value
	}
	if _, ok := out["task"]; !ok {
		out["task"] = task
	}
	if workerID != "" {
		out["worker_id"] = workerID
	}
	if r.streamID != "" {
		out["colony_id"] = r.streamID
	}
	if binding := r.TrackerBinding(); binding != nil {
		out["binding"] = map[string]string{
			"name":       binding.Name,
			"dir":        binding.Dir,
			"tracker_db": binding.TrackerDB,
		}
	}
	return out
}

// executeTrackerTool runs one tracker call for a worker and renders the tool
// result the agent loop expects. ok is false for names the tracker table does
// not own, letting the caller fall through to the colony executor.
func (r *ColonyRuntime) executeTrackerTool(registry *tracker.Registry, req agentloop.ToolCallRequest) (agentloop.ToolResult, bool) {
	result, ok := registry.Execute(req.Name, req.Input)
	if !ok {
		return agentloop.ToolResult{}, false
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return agentloop.ToolResult{Content: "tracker tool result could not be encoded: " + err.Error(), IsError: true}, true
	}
	success, _ := result["success"].(bool)
	return agentloop.ToolResult{Content: string(payload), IsError: !success}, true
}

// TrackerBindingFromContext returns the binding the session's colony runtime
// carries, or nil when no colony is attached. It is what the queen's tracker
// tools use to reach the same tracker.db her workers write.
func TrackerBindingFromContext(ctx context.Context) *tracker.Binding {
	if ctx == nil {
		return nil
	}
	spawner := RunWorkerSpawnerFromContext(ctx)
	if spawner == nil {
		return nil
	}
	carrier, ok := spawner.(interface{ TrackerBinding() *tracker.Binding })
	if !ok {
		return nil
	}
	return carrier.TrackerBinding()
}

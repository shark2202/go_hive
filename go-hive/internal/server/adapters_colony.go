package server

// Adapters projecting *colony.ColonyRuntime onto the read surfaces the wave-1
// route tables expect (ColonyWorkerReadSurface, LiveWorkerRuntime).
//
// internal/colony exports neither a worker list, nor the runtime storage path,
// nor a colony-level tool catalog. Everything below is assembled from what the
// runtime does export — WorkerToolTiering (the worker ids), GetWorker,
// Worker.Info/Result/ExplicitReport and AgentContext.AvailableTools — so the
// routes report live state instead of invented rows. A value the runtime cannot
// answer stays unknown (nil/""), and the handlers keep their explicit
// 404/503/404-not-known paths rather than a fabricated success.

import (
	"sort"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/skills"
)

// colonyWorkerSurface adapts one live colony runtime to the colony-worker read
// surface. A nil runtime yields empty reads; callers treat that as "no live
// colony on this session" and fall back to the on-disk worker walk.
type colonyWorkerSurface struct {
	runtime *colony.ColonyRuntime
}

// colonyRuntimeSource lets a session's colony wrapper expose the underlying
// runtime without the server package reaching into colony internals.
type colonyRuntimeSource interface {
	ColonyRuntime() *colony.ColonyRuntime
}

// sessionColonyRuntime returns the session's live colony runtime, or nil when
// the session has none loaded (the routes then fall back to disk state).
// *colony.ColonyRuntime does not satisfy the server's ColonyRuntime interface
// (it has no InjectInput), so sessions either expose it through the
// ColonyRuntime() accessor or the app supplies it through AppDeps.
func sessionColonyRuntime(session *Session) *colony.ColonyRuntime {
	if session == nil || session.Colony == nil {
		return nil
	}
	if source, ok := session.Colony.(colonyRuntimeSource); ok {
		if runtime := source.ColonyRuntime(); runtime != nil {
			return runtime
		}
	}
	runtime, _ := any(session.Colony).(*colony.ColonyRuntime)
	return runtime
}

func colonyWorkerReadSurfaceFor(runtime *colony.ColonyRuntime) ColonyWorkerReadSurface {
	if runtime == nil {
		return nil
	}
	return colonyWorkerSurface{runtime: runtime}
}

func liveWorkerRuntimeFor(runtime *colony.ColonyRuntime) LiveWorkerRuntime {
	if runtime == nil {
		return nil
	}
	return colonyLiveWorkers{runtime: runtime}
}

func (s colonyWorkerSurface) workerIDs() []string {
	if s.runtime == nil {
		return nil
	}
	// WorkerToolTiering walks the runtime's private worker registry and is the
	// only exported enumeration of worker ids.
	tiering := s.runtime.WorkerToolTiering()
	ids := make([]string, 0, len(tiering))
	for _, entry := range tiering {
		if entry.WorkerID != "" {
			ids = append(ids, entry.WorkerID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s colonyWorkerSurface) ListWorkers() []WorkerInfoView {
	views := make([]WorkerInfoView, 0)
	for _, id := range s.workerIDs() {
		worker := s.runtime.GetWorker(id)
		if worker == nil {
			continue
		}
		views = append(views, colonyWorkerInfoView(worker))
	}
	return views
}

func (s colonyWorkerSurface) Worker(workerID string) (WorkerInfoView, WorkerBatchView, bool) {
	if s.runtime == nil {
		return WorkerInfoView{}, WorkerBatchView{}, false
	}
	worker := s.runtime.GetWorker(workerID)
	if worker == nil {
		return WorkerInfoView{}, WorkerBatchView{}, false
	}
	view := colonyWorkerInfoView(worker)
	return view, view.Batch, true
}

func (s colonyWorkerSurface) ColonyID() string {
	if s.runtime == nil {
		return ""
	}
	info := s.runtime.GetStats()
	if colonyID, ok := info["colony_id"].(string); ok {
		return colonyID
	}
	return ""
}

// StoragePath is unknown: the runtime keeps its storage root private, so the
// routes fall back to the session's queen directory.
func (s colonyWorkerSurface) StoragePath() string { return "" }

// Skills is unknown: the runtime exposes no parsed-skill catalog and no storage
// path to scan, so the route answers with the skills it knows (none) rather
// than a guess.
func (s colonyWorkerSurface) Skills() ([]*skills.ParsedSkill, bool) { return nil, false }

// Tools projects the live worker tool sets. Before the first worker starts the
// colony holds no loaded tool list, so the answer is empty — matching what the
// runtime has actually materialized.
func (s colonyWorkerSurface) Tools() ([]ColonyTool, map[string]string) {
	if s.runtime == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	names := make([]string, 0)
	descriptions := map[string]string{}
	for _, id := range s.workerIDs() {
		worker := s.runtime.GetWorker(id)
		if worker == nil || worker.Context == nil {
			continue
		}
		for _, def := range worker.Context.AvailableTools {
			if def.Name == "" {
				continue
			}
			descriptions[def.Name] = def.Description
			if _, present := seen[def.Name]; present {
				continue
			}
			seen[def.Name] = struct{}{}
			names = append(names, def.Name)
		}
	}
	sort.Strings(names)
	tools := make([]ColonyTool, 0, len(names))
	for _, name := range names {
		tools = append(tools, ColonyTool{Name: name, Description: descriptions[name]})
	}
	return tools, nil
}

func colonyWorkerInfoView(worker *colony.Worker) WorkerInfoView {
	info := worker.Info()
	view := WorkerInfoView{
		ID:        info.WorkerID,
		Task:      info.Task,
		Status:    worker.Status(),
		StartedAt: info.StartedAt,
		Batch: WorkerBatchView{
			BatchID:    info.BatchID,
			BatchIndex: info.Index,
			BatchSize:  info.Size,
		},
	}
	if worker.Context != nil {
		view.ProfileName = worker.Context.AgentSpec.Name
	}
	if view.Status.Done() {
		view.Result = colonyWorkerResultView(worker)
	}
	return view
}

func colonyWorkerResultView(worker *colony.Worker) *WorkerResultView {
	result := worker.Result()
	view := &WorkerResultView{
		Status:        string(result.Status),
		Error:         result.Error,
		ToolCallsUsed: result.ToolCallsUsed,
		BudgetLimited: result.BudgetLimited,
	}
	if summary, ok := result.Output["summary"].(string); ok {
		view.Summary = summary
	}
	if tokens, ok := numberFromAny(result.Output["tokens_used"]); ok {
		view.TokensUsed = int(tokens)
	}
	started, finished := worker.StartedAt(), worker.FinishedAt()
	if !started.IsZero() && !finished.IsZero() {
		view.DurationSeconds = finished.Sub(started).Seconds()
	}
	return view
}

// colonyLiveWorkers adapts one live colony runtime to the SSE worker registry.
type colonyLiveWorkers struct {
	runtime *colony.ColonyRuntime
}

func (s colonyLiveWorkers) LiveWorkers() []LiveWorkerSnapshot {
	snapshots := make([]LiveWorkerSnapshot, 0)
	ids := colonyWorkerSurface{runtime: s.runtime}.workerIDs()
	for _, id := range ids {
		worker := s.runtime.GetWorker(id)
		if worker == nil {
			continue
		}
		snapshots = append(snapshots, colonyLiveWorkerSnapshot(worker))
	}
	return snapshots
}

func (s colonyLiveWorkers) LiveWorker(workerID string) (LiveWorkerSnapshot, bool) {
	if s.runtime == nil {
		return LiveWorkerSnapshot{}, false
	}
	worker := s.runtime.GetWorker(workerID)
	if worker == nil {
		return LiveWorkerSnapshot{}, false
	}
	return colonyLiveWorkerSnapshot(worker), true
}

func (s colonyLiveWorkers) StopLiveWorker(workerID string) (map[string]any, error) {
	if s.runtime == nil {
		return nil, nil
	}
	worker := s.runtime.GetWorker(workerID)
	if worker == nil {
		return map[string]any{"reason": "Worker not found"}, nil
	}
	status := worker.Status()
	if !s.runtime.StopWorker(workerID) {
		return map[string]any{
			"reason":    "Worker already terminated",
			"worker_id": workerID,
			"status":    string(status),
		}, nil
	}
	return map[string]any{"worker_id": workerID, "status": string(status)}, nil
}

func (s colonyLiveWorkers) StopAllLiveWorkers() map[string]any {
	stopped := []string{}
	queuedCancelled := []string{}
	timedOut := []string{}
	if s.runtime != nil {
		ids := colonyWorkerSurface{runtime: s.runtime}.workerIDs()
		for _, id := range ids {
			worker := s.runtime.GetWorker(id)
			if worker == nil || !worker.IsActive() {
				continue
			}
			if !s.runtime.StopWorker(id) {
				timedOut = append(timedOut, id)
				continue
			}
			if worker.Status() == colony.WorkerQueued {
				queuedCancelled = append(queuedCancelled, id)
				continue
			}
			stopped = append(stopped, id)
		}
	}
	return map[string]any{
		"stopped":          stopped,
		"stopped_count":    len(stopped),
		"queued_cancelled": queuedCancelled,
		"timed_out":        timedOut,
		"errors":           nil,
	}
}

func colonyLiveWorkerSnapshot(worker *colony.Worker) LiveWorkerSnapshot {
	snapshot := LiveWorkerSnapshot{
		ID:             worker.ID,
		Task:           worker.Task,
		Status:         string(worker.Status()),
		IsActive:       worker.IsActive(),
		ExplicitReport: worker.ExplicitReport(),
	}
	if started := worker.StartedAt(); !started.IsZero() {
		snapshot.StartedAt = float64(started.UnixNano()) / 1e9
	}
	if status := worker.Status(); status.Done() {
		result := worker.Result()
		statusText := string(result.Status)
		snapshot.ResultStatus = &statusText
		if summary, ok := result.Output["summary"].(string); ok {
			snapshot.ResultSummary = &summary
		}
	}
	return snapshot
}

// numberFromAny converts the JSON-ish numbers a worker report carries.
func numberFromAny(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}

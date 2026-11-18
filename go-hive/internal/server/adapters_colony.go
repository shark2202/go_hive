package server

// Adapters projecting *colony.ColonyRuntime onto the two read surfaces the
// wave-1 route tables consume (ColonyWorkerReadSurface, LiveWorkerRuntime).
//
// colony cannot return the server package's view structs (the dependency runs
// server→colony only), so the five methods whose signatures differ — ListWorkers,
// Worker, Tools and the two live-worker reads — copy fields here. Everything
// else (ColonyID, StoragePath, Skills, StopLiveWorker, StopAllLiveWorkers)
// matches exactly and is forwarded.

import (
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/skills"
)

// colonyWorkerSurface adapts one live colony runtime to the colony-worker read
// surface. A nil runtime yields empty reads; callers treat that as "no live
// colony on this session" and fall back to the on-disk worker walk.
type colonyWorkerSurface struct {
	runtime *colony.ColonyRuntime
}

// sessionColonyRuntime returns the session's live colony runtime, or nil when
// the session has none loaded (the routes then fall back to disk state).
// *colony.ColonyRuntime satisfies the server's ColonyRuntime interface, so a
// loaded colony can be held directly.
func sessionColonyRuntime(session *Session) *colony.ColonyRuntime {
	if session == nil || session.Colony == nil {
		return nil
	}
	runtime, _ := session.Colony.(*colony.ColonyRuntime)
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

func (s colonyWorkerSurface) ListWorkers() []WorkerInfoView {
	listed := s.runtime.ListWorkers()
	views := make([]WorkerInfoView, 0, len(listed))
	for _, info := range listed {
		views = append(views, colonyWorkerInfoView(info))
	}
	return views
}

func (s colonyWorkerSurface) Worker(workerID string) (WorkerInfoView, WorkerBatchView, bool) {
	info, ok := s.runtime.Worker(workerID)
	if !ok {
		return WorkerInfoView{}, WorkerBatchView{}, false
	}
	view := colonyWorkerInfoView(info)
	return view, view.Batch, true
}

func (s colonyWorkerSurface) ColonyID() string { return s.runtime.ColonyID() }

func (s colonyWorkerSurface) StoragePath() string { return s.runtime.StoragePath() }

func (s colonyWorkerSurface) Skills() ([]*skills.ParsedSkill, bool) { return s.runtime.Skills() }

func (s colonyWorkerSurface) Tools() ([]ColonyTool, map[string]string) {
	listed, providers := s.runtime.Tools()
	tools := make([]ColonyTool, 0, len(listed))
	for _, tool := range listed {
		tools = append(tools, ColonyTool{Name: tool.Name, Description: tool.Description})
	}
	return tools, providers
}

// colonyWorkerInfoView copies a colony.WorkerInfo into the route's view type.
func colonyWorkerInfoView(info colony.WorkerInfo) WorkerInfoView {
	view := WorkerInfoView{
		ID:          info.WorkerID,
		Task:        info.Task,
		Status:      info.Status,
		StartedAt:   info.StartedAt,
		ProfileName: info.ProfileName,
		Batch: WorkerBatchView{
			BatchID:    info.BatchID,
			BatchIndex: info.Index,
			BatchSize:  info.Size,
			WorkerSeq:  info.WorkerSeq,
		},
	}
	if result := info.Result; result != nil {
		view.Result = &WorkerResultView{
			Status:          result.ReportStatus,
			Summary:         result.Summary,
			Error:           result.Error,
			TokensUsed:      tokensUsedFromData(result.Data),
			DurationSeconds: result.DurationSeconds,
			ToolCallsUsed:   result.ToolCallsUsed,
			BudgetLimited:   result.BudgetLimited,
		}
	}
	return view
}

// tokensUsedFromData reads the token count worker reports carry in their data
// payload; the colony result has no dedicated field for it.
func tokensUsedFromData(data map[string]any) int {
	if tokens, ok := numberFromAny(data["tokens_used"]); ok {
		return int(tokens)
	}
	return 0
}

// colonyLiveWorkers adapts one live colony runtime to the SSE worker registry.
type colonyLiveWorkers struct {
	runtime *colony.ColonyRuntime
}

func (s colonyLiveWorkers) LiveWorkers() []LiveWorkerSnapshot {
	listed := s.runtime.LiveWorkers()
	snapshots := make([]LiveWorkerSnapshot, 0, len(listed))
	for _, state := range listed {
		snapshots = append(snapshots, colonyLiveWorkerSnapshot(state))
	}
	return snapshots
}

func (s colonyLiveWorkers) LiveWorker(workerID string) (LiveWorkerSnapshot, bool) {
	state, ok := s.runtime.LiveWorker(workerID)
	if !ok {
		return LiveWorkerSnapshot{}, false
	}
	return colonyLiveWorkerSnapshot(state), true
}

func (s colonyLiveWorkers) StopLiveWorker(workerID string) (map[string]any, error) {
	return s.runtime.StopLiveWorker(workerID)
}

func (s colonyLiveWorkers) StopAllLiveWorkers() map[string]any {
	return s.runtime.StopAllLiveWorkers()
}

// colonyLiveWorkerSnapshot copies a colony.LiveWorkerState into the route's view.
func colonyLiveWorkerSnapshot(state colony.LiveWorkerState) LiveWorkerSnapshot {
	return LiveWorkerSnapshot{
		ID:             state.ID,
		Task:           state.Task,
		Status:         state.Status,
		IsActive:       state.IsActive,
		StartedAt:      state.StartedAt,
		ExplicitReport: state.ExplicitReport,
		ResultStatus:   state.ResultStatus,
		ResultSummary:  state.ResultSummary,
	}
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

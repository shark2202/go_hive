package server

import "github.com/aden-hive/hive-go/internal/colony"

type WorkerResultView struct {
	Status          string
	Summary         string
	Error           *string
	TokensUsed      int
	DurationSeconds float64
	ToolCallsUsed   int
	BudgetLimited   bool
}

type WorkerBatchView struct {
	BatchID    string
	BatchIndex int
	BatchSize  int
	WorkerSeq  int
}

type WorkerInfoView struct {
	ID          string
	Task        string
	Status      colony.WorkerStatus
	StartedAt   float64
	Result      *WorkerResultView
	ProfileName string
	Batch       WorkerBatchView
}

// Parity: framework/server/routes_colony_workers.py::_worker_info_to_dict
func WorkerInfoToDict(info WorkerInfoView) map[string]any {
	var result any
	if info.Result != nil {
		result = map[string]any{
			"status":           info.Result.Status,
			"summary":          info.Result.Summary,
			"error":            info.Result.Error,
			"tokens_used":      info.Result.TokensUsed,
			"duration_seconds": info.Result.DurationSeconds,
			"tool_calls_used":  info.Result.ToolCallsUsed,
			"budget_limited":   info.Result.BudgetLimited,
		}
	}
	return map[string]any{
		"worker_id":    info.ID,
		"task":         info.Task,
		"status":       string(info.Status),
		"started_at":   info.StartedAt,
		"result":       result,
		"profile_name": info.ProfileName,
		"batch": map[string]any{
			"batch_id":    info.Batch.BatchID,
			"batch_index": info.Batch.BatchIndex,
			"batch_size":  info.Batch.BatchSize,
			"worker_seq":  info.Batch.WorkerSeq,
		},
	}
}

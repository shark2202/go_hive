package colony

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

func (r *ColonyRuntime) publishReport(worker *Worker, finalText string) {
	payload := worker.reportPayload()
	payload["colony_id"] = r.streamID
	summary := reportSummary(worker, finalText)
	payload["summary"] = summary
	payload["task"] = worker.Task
	worker.setResultSummary(summary)
	// The executed goal verdict and the improvement signals it produced ride
	// every terminal report, including the explicit-report path: a worker that
	// reports success while a weighted criterion missed must show both.
	if evaluation := worker.GoalEvaluation(); evaluation != nil {
		payload["goal_evaluation"] = evaluation.Map()
	}
	if signals := worker.improvementSignalMaps(); len(signals) > 0 {
		payload["improvement_signals"] = signals
	}
	if payload["goal_evaluation"] != nil || payload["improvement_signals"] != nil {
		payload["data"] = mergeReportData(payload["data"], payload["goal_evaluation"], payload["improvement_signals"])
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.maybeAdaptColonyBudget(payload)
	r.recordWorkerTerminal(worker, payload)
	r.publishEvent(worker, payload)

	if r.bus != nil {
		executionID := ""
		if worker.Context != nil {
			executionID = worker.Context.ExecutionID
		}
		r.bus.EmitExecutionCompleted(worker.StreamID, executionID, map[string]any{"status": payload["status"]}, nil, nil)
	}
}

func (r *ColonyRuntime) publishFailed(worker *Worker, errorText string) {
	payload := map[string]any{
		"worker_id":        worker.ID,
		"colony_id":        r.streamID,
		"task":             worker.Task,
		"status":           "failed",
		"summary":          "Worker crashed: " + errorText,
		"data":             map[string]any{},
		"error":            errorText,
		"duration_seconds": worker.Result().DurationSeconds,
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.recordWorkerTerminal(worker, payload)
	r.publishEvent(worker, payload)

	if r.bus != nil {
		executionID := ""
		if worker.Context != nil {
			executionID = worker.Context.ExecutionID
		}
		r.bus.EmitExecutionFailed(worker.StreamID, executionID, errorText, nil, nil)
	}
}

func (r *ColonyRuntime) publishStopped(worker *Worker, summary string) {
	payload := map[string]any{
		"worker_id":        worker.ID,
		"colony_id":        r.streamID,
		"task":             worker.Task,
		"status":           "stopped",
		"summary":          summary,
		"data":             map[string]any{},
		"error":            nil,
		"duration_seconds": worker.Result().DurationSeconds,
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.recordWorkerTerminal(worker, payload)
	r.publishEvent(worker, payload)
}

// recordWorkerTerminal persists the terminal report beside the transcript
// (result.json + one events.jsonl line). Every terminal path — success,
// failure, stop — goes through here, so a later resume or audit finds the same
// payload the queen received.
func (r *ColonyRuntime) recordWorkerTerminal(worker *Worker, payload map[string]any) {
	// Terminal first, so a restart between the report and the artefact writes
	// never sees the worker as in flight and resumes finished work.
	r.writeWorkerRunState(worker, workerStateFromReport(stringValue(payload["status"])))
	r.persistWorkerResult(worker, payload)
	r.appendWorkerEvent(worker, payload)
}

func (r *ColonyRuntime) publishEvent(worker *Worker, payload map[string]any) {
	if r.bus == nil {
		return
	}
	event := eventbus.NewAgentEvent(eventbus.EventSubagentReport, worker.StreamID)
	event.Data = payload
	colonyID := r.streamID
	event.ColonyID = &colonyID
	r.bus.Publish(event)
}

func reportSummary(worker *Worker, finalText string) string {
	if report := worker.ExplicitReport(); report != nil {
		if summary, ok := report["summary"].(string); ok {
			return summary
		}
	}
	return finalText
}

func writeSeedParts(storagePath, workerID string, seed []SeedMessage) error {
	if len(seed) == 0 {
		return nil
	}
	dir := filepath.Join(storagePath, "workers", workerID, "parts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for index, message := range seed {
		part := map[string]any{
			"seq":     message.Seq,
			"role":    message.Role,
			"content": message.Content,
		}
		data, err := json.Marshal(part)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%d.json", index)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

package colony

// Per-worker transcript and terminal artefacts on disk.
//
// A worker owns one directory, <colony>/workers/<worker_id>/, the same layout
// upstream's ColonyRuntime._build_worker creates. Inside it:
//
//	conversations/parts/*.json  the persisted AgentLoop transcript (resume)
//	meta.json                   spawn lineage (task / profile / colony)
//	result.json                 terminal result snapshot
//	events.jsonl               terminal report payloads, one per line
//
// Persisting here is what makes resume_worker_ids real: a worker that stopped
// before reporting keeps its transcript on disk, so a later resume continues
// the same AgentLoop instead of starting over.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
// Parity: framework/storage/conversation_store.py::FileConversationStore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

const (
	workerConversationsDir = "conversations"
	workerMetaFile         = "meta.json"
	workerResultFile       = "result.json"
	workerEventsFile       = "events.jsonl"
)

// workerConversationStore is the on-disk store a worker's AgentLoop persists
// into. The base is <worker>/conversations, matching upstream's
// FileConversationStore(worker_storage / "conversations").
func (r *ColonyRuntime) workerConversationStore(workerID string) agentloop.ConversationStore {
	return agentloop.NewFileConversationStore(filepath.Join(r.workerStorage(workerID), workerConversationsDir))
}

// restoreWorkerConversation reloads a saved worker transcript. ok is false
// when the worker has no persisted parts, which is the "nothing to resume"
// signal resume_worker_ids reports per id.
func restoreWorkerConversation(store agentloop.ConversationStore) (*agentloop.NodeConversation, bool, error) {
	parts, err := store.ReadParts()
	if err != nil {
		return nil, false, err
	}
	if len(parts) == 0 {
		return nil, false, nil
	}
	conv, err := agentloop.RestoreConversation(store, nil, nil)
	if err != nil {
		return nil, false, err
	}
	if conv == nil || len(conv.Messages()) == 0 {
		return nil, false, nil
	}
	return conv, true, nil
}

// writeWorkerMeta records the spawn breadcrumb resume needs: the original task
// and profile are not otherwise recoverable from parts alone, and the colony
// id is the cross-colony guard.
func (r *ColonyRuntime) writeWorkerMeta(worker *Worker) {
	if r == nil || worker == nil || worker.ID == "" {
		return
	}
	meta := map[string]any{
		"worker_id":    worker.ID,
		"colony_id":    r.streamID,
		"task":         worker.Task,
		"profile_name": worker.ProfileName,
		"goal":         worker.Goal,
		"batch_id":     worker.BatchID,
		"batch_index":  worker.BatchIndex,
		"batch_size":   worker.BatchSize,
	}
	path := filepath.Join(r.workerStorage(worker.ID), workerMetaFile)
	writeJSONFile(path, meta)
}

// readWorkerMeta returns the spawn breadcrumb, or an empty map when it is
// missing or unreadable (a resumed worker from an older build has no meta).
func readWorkerMeta(workerDir string) map[string]any {
	data, err := os.ReadFile(filepath.Join(workerDir, workerMetaFile))
	if err != nil {
		return map[string]any{}
	}
	meta := map[string]any{}
	if json.Unmarshal(data, &meta) != nil {
		return map[string]any{}
	}
	return meta
}

// persistWorkerResult writes the terminal result snapshot next to the
// transcript. Failures are ignored: a missing artefact must never fail a
// worker that already finished.
func (r *ColonyRuntime) persistWorkerResult(worker *Worker, payload map[string]any) {
	if r == nil || worker == nil || worker.ID == "" {
		return
	}
	result := worker.Result()
	snapshot := map[string]any{
		"worker_id":        worker.ID,
		"colony_id":        r.streamID,
		"status":           payload["status"],
		"summary":          payload["summary"],
		"data":             payload["data"],
		"error":            payload["error"],
		"duration_seconds": result.DurationSeconds,
		"tool_calls_used":  result.ToolCallsUsed,
		"budget_limited":   result.BudgetLimited,
	}
	writeJSONFile(filepath.Join(r.workerStorage(worker.ID), workerResultFile), snapshot)
}

// appendWorkerEvent appends one terminal report payload to the worker's
// events log, so the colony keeps a per-worker event trail beside the
// transcript (upstream streams these on the event bus alone).
func (r *ColonyRuntime) appendWorkerEvent(worker *Worker, payload map[string]any) {
	if r == nil || worker == nil || worker.ID == "" {
		return
	}
	dir := r.workerStorage(worker.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, workerEventsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
}

func writeJSONFile(path string, value map[string]any) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o644)
}

// workerResumeError renders the per-id failure resume_worker_ids returns; the
// message names the missing transcript so the queen can re-dispatch instead of
// guessing.
func workerResumeError(workerID, reason string) map[string]any {
	return map[string]any{
		"worker_id": workerID,
		"error":     fmt.Sprintf("worker %s cannot be resumed: %s", workerID, reason),
	}
}

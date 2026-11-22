package colony

// Worker run state: the durable record a restarted colony reads to find the
// workers that were still queued or running when the process stopped, and to
// rebuild them from their saved transcripts (ResumeInFlightWorkers).
//
// The record is written in two places so either scan works:
//
//	<colony>/runs/<worker_id>.json          colony-level run index (scaffolded)
//	<colony>/workers/<worker_id>/state.json per-worker copy beside the transcript
//
// Only queued/running records are auto-resumed. A worker that reached a
// terminal state — including the "stopped" written by a graceful colony stop —
// is finished: restarting it would re-run work the queen already saw reported.
// A non-terminal record whose transcript is gone is marked unrecoverable and
// skipped, so every restart does not retry the same broken id forever.
//
// Parity: framework/host/worker.py::WorkerStatus
// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const (
	workerStateFile   = "state.json"
	workerRunsDirName = "runs"

	workerResumeStatusResumed       = "resumed"
	workerResumeStatusUnrecoverable = "unrecoverable"
)

// workerStateFromReport maps a terminal report status ("success"/"failed"/
// "stopped") onto the run-state vocabulary. Unknown values keep the raw text
// so a custom explicit report never silently looks resumable.
func workerStateFromReport(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success", "completed":
		return string(WorkerCompleted)
	case "failed", "error":
		return string(WorkerFailed)
	case "stopped":
		return string(WorkerStopped)
	}
	return strings.ToLower(strings.TrimSpace(status))
}

func workerStateTerminal(state string) bool {
	switch WorkerStatus(state) {
	case WorkerCompleted, WorkerFailed, WorkerStopped:
		return true
	}
	return false
}

func (r *ColonyRuntime) workerRunStatePath(workerID string) string {
	return filepath.Join(r.storagePath, workerRunsDirName, workerID+".json")
}

func (r *ColonyRuntime) workerStatePath(workerID string) string {
	return filepath.Join(r.workerStorage(workerID), workerStateFile)
}

// writeWorkerRunState snapshots a worker's lifecycle state to both record
// locations. Existing resume bookkeeping (resume_status / resume_count) is
// preserved: a resumed worker that finishes must still say it was resumed.
// Best effort: a worker must not fail because its bookkeeping file could not
// be written.
func (r *ColonyRuntime) writeWorkerRunState(worker *Worker, state string) {
	if r == nil || worker == nil || worker.ID == "" {
		return
	}
	record := readWorkerRunStateFile(r.workerRunStatePath(worker.ID))
	if record == nil {
		record = readWorkerRunStateFile(r.workerStatePath(worker.ID))
	}
	if record == nil {
		record = map[string]any{}
	}
	for key, value := range r.workerRunStateRecord(worker) {
		record[key] = value
	}
	record["status"] = state
	record["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	r.persistWorkerRunState(worker.ID, record)
}

func (r *ColonyRuntime) workerRunStateRecord(worker *Worker) map[string]any {
	return map[string]any{
		"worker_id":    worker.ID,
		"colony_id":    r.streamID,
		"task":         worker.Task,
		"goal":         worker.Goal,
		"profile_name": worker.ProfileName,
		"batch_id":     worker.BatchID,
		"batch_index":  worker.BatchIndex,
		"batch_size":   worker.BatchSize,
	}
}

// resetWorkerRunState writes a fresh record for a newly spawned worker. A
// restarted process reuses worker ids from zero, so spawn must start a clean
// record instead of inheriting the previous run's resume bookkeeping.
func (r *ColonyRuntime) resetWorkerRunState(worker *Worker, state string) {
	if r == nil || worker == nil || worker.ID == "" {
		return
	}
	record := r.workerRunStateRecord(worker)
	record["status"] = state
	record["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	r.persistWorkerRunState(worker.ID, record)
}

// updateWorkerRunState merges fields into the existing record (creating one
// when absent) and rewrites both copies. Resume bookkeeping — resume_status,
// resume_count, resume_error — rides here so the record stays a single source
// of truth across restarts.
func (r *ColonyRuntime) updateWorkerRunState(workerID string, updates map[string]any) {
	if r == nil || workerID == "" {
		return
	}
	record := readWorkerRunStateFile(r.workerRunStatePath(workerID))
	if record == nil {
		record = readWorkerRunStateFile(r.workerStatePath(workerID))
	}
	if record == nil {
		record = map[string]any{"worker_id": workerID, "colony_id": r.streamID}
	}
	for key, value := range updates {
		record[key] = value
	}
	record["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	r.persistWorkerRunState(workerID, record)
}

func (r *ColonyRuntime) persistWorkerRunState(workerID string, record map[string]any) {
	_ = atomicfs.WriteJSON(r.workerRunStatePath(workerID), record, 0o644)
	_ = atomicfs.WriteJSON(r.workerStatePath(workerID), record, 0o644)
}

func readWorkerRunStateFile(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	record := map[string]any{}
	if json.Unmarshal(data, &record) != nil {
		return nil
	}
	return record
}

// inFlightWorkerRunStates lists the colony's worker run records in worker-id
// order. The runs/ index is authoritative; a colony written before runs/
// existed (or a worker directory copied alone) falls back to the per-worker
// state.json copies.
func (r *ColonyRuntime) inFlightWorkerRunStates() []map[string]any {
	if r == nil {
		return nil
	}
	records := readWorkerRunStateDir(filepath.Join(r.storagePath, workerRunsDirName))
	if len(records) > 0 {
		return records
	}
	workersDir := filepath.Join(r.storagePath, "workers")
	entries, err := os.ReadDir(workersDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if record := readWorkerRunStateFile(filepath.Join(workersDir, entry.Name(), workerStateFile)); record != nil {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return stringValue(records[i]["worker_id"]) < stringValue(records[j]["worker_id"])
	})
	return records
}

func readWorkerRunStateDir(dir string) []map[string]any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	records := make([]map[string]any, 0, len(names))
	for _, name := range names {
		if record := readWorkerRunStateFile(filepath.Join(dir, name)); record != nil {
			records = append(records, record)
		}
	}
	return records
}

// ResumeInFlightWorkers is the restart-recovery half of the durability story:
// every worker record still queued/running when the previous process stopped is
// rebuilt over its saved transcript and started again. Records that cannot be
// recovered (no transcript, foreign colony, corrupt meta) are marked
// unrecoverable in place and skipped, so the colony boots without them and a
// later boot does not retry the same id.
//
// It is called from Start, so a colony that comes back up after a crash keeps
// working its in-flight assignments instead of losing them with the process.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
func (r *ColonyRuntime) ResumeInFlightWorkers(ctx context.Context) ([]string, []map[string]any) {
	resumed := make([]string, 0)
	skipped := make([]map[string]any, 0)
	if r == nil || r.llm == nil {
		// Without the colony completer a worker's loop cannot run; leave the
		// records untouched for a host that can actually resume them.
		return resumed, skipped
	}
	if ctx == nil {
		ctx = r.runtimeContext(nil)
	}
	for _, record := range r.inFlightWorkerRunStates() {
		workerID := strings.TrimSpace(stringValue(record["worker_id"]))
		if workerID == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(stringValue(record["status"])))
		if workerStateTerminal(status) {
			continue
		}
		if strings.EqualFold(stringValue(record["resume_status"]), workerResumeStatusResumed) {
			// Already recovered in this process; the live worker owns it now.
			continue
		}
		if strings.EqualFold(stringValue(record["resume_status"]), workerResumeStatusUnrecoverable) {
			// A previous boot already proved this record cannot be rebuilt;
			// retrying it on every restart would only rewrite the same failure.
			continue
		}
		if existing := r.GetWorker(workerID); existing != nil && existing.IsActive() {
			continue
		}
		worker, err := r.resumeWorkerOverTranscript(
			workerID,
			"",
			nil,
			stringValue(record["batch_id"]),
			stateInt(record["batch_index"]),
			stateInt(record["batch_size"]),
		)
		if err != nil {
			r.markWorkerUnrecoverable(workerID, err)
			skipped = append(skipped, map[string]any{"worker_id": workerID, "error": err.Error()})
			continue
		}
		if worker.Goal == "" {
			worker.Goal = stringValue(record["goal"])
		}
		r.markWorkerResumed(worker, stateInt(record["resume_count"])+1)
		r.startWorker(ctx, worker)
		resumed = append(resumed, workerID)
	}
	return resumed, skipped
}

func (r *ColonyRuntime) markWorkerResumed(worker *Worker, resumeCount int) {
	updates := r.workerRunStateRecord(worker)
	updates["status"] = string(WorkerRunning)
	updates["resume_status"] = workerResumeStatusResumed
	updates["resume_count"] = resumeCount
	updates["resumed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	delete(updates, "resume_error")
	r.updateWorkerRunState(worker.ID, updates)
}

func (r *ColonyRuntime) markWorkerUnrecoverable(workerID string, cause error) {
	r.updateWorkerRunState(workerID, map[string]any{
		"resume_status": workerResumeStatusUnrecoverable,
		"resume_error":  cause.Error(),
	})
}

// stateInt reads an integer out of a decoded JSON record (float64) or a live
// in-memory value, defaulting to 0.
func stateInt(raw any) int {
	switch typed := raw.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return n
		}
	}
	return 0
}

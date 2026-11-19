package colony

// Worker resume: rebuild a stopped worker over its saved transcript and let the
// same AgentLoop continue from where it stopped.
//
// Upstream's run_worker resume mode calls ColonyRuntime.resume_worker once per
// id; the tool reports per-id failures instead of aborting the batch. This file
// is that path for the Go runtime: the transcript lives under
// <colony>/workers/<id>/conversations, so a worker that stopped before
// reporting can be continued rather than re-dispatched from scratch.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

// WorkerResumer is the resume half of the colony runtime run_worker drives. It
// stays a separate interface from RunWorkerSpawner so a spawner fake without
// disk transcripts still compiles (and still reports resume unsupported).
type WorkerResumer interface {
	ResumeWorkers(ctx context.Context, ids []string, guidance string, loopOverrides map[string]any, batchID string) ([]string, []map[string]any)
}

// ResumeWorkers rebuilds each requested worker from its on-disk transcript and
// starts it under the colony's admission gate. It returns the ids it started
// plus one failure entry per id that could not be resumed, so the caller can
// report a mixed batch (upstream raises per id and the tool records it).
//
// Refused, matching upstream:
//   - a worker that is still active (two loops on one transcript corrupt it);
//   - an id with no saved conversation;
//   - an id whose meta.json names a different colony.
func (r *ColonyRuntime) ResumeWorkers(ctx context.Context, ids []string, guidance string, loopOverrides map[string]any, batchID string) ([]string, []map[string]any) {
	resumed := make([]string, 0, len(ids))
	failures := make([]map[string]any, 0)
	if r == nil {
		return resumed, []map[string]any{{"error": "no colony runtime"}}
	}
	for _, raw := range ids {
		workerID := strings.TrimSpace(raw)
		if workerID == "" {
			continue
		}
		worker, err := r.resumeWorkerOverTranscript(workerID, guidance, loopOverrides, batchID, 0, len(ids))
		if err != nil {
			failures = append(failures, workerResumeError(workerID, err.Error()))
			continue
		}
		r.startWorker(r.runtimeContext(ctx), worker)
		resumed = append(resumed, workerID)
	}
	return resumed, failures
}

// resumeWorkerOverTranscript rebuilds one worker over its saved transcript and
// registers it, but does not start it: the caller owns the run context (a
// queen-requested resume starts it immediately; a restart recovery starts it
// on the colony's own context). An error means "this id cannot be resumed" and
// names the reason.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
func (r *ColonyRuntime) resumeWorkerOverTranscript(workerID, guidance string, loopOverrides map[string]any, batchID string, batchIndex, batchSize int) (*Worker, error) {
	if r == nil {
		return nil, fmt.Errorf("no colony runtime")
	}
	if existing := r.GetWorker(workerID); existing != nil && existing.IsActive() {
		return nil, fmt.Errorf("it is still active (%s); a running worker cannot be resumed", existing.Status())
	}
	workerDir := r.workerStorage(workerID)
	store := r.workerConversationStore(workerID)
	conv, ok, err := restoreWorkerConversation(store)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("it has no saved conversation to resume")
	}
	meta := readWorkerMeta(workerDir)
	if colonyID := stringValue(meta["colony_id"]); colonyID != "" && colonyID != r.streamID {
		return nil, fmt.Errorf("it belongs to colony %q, not %q", colonyID, r.streamID)
	}

	task := stringValue(meta["task"])
	worker := r.BuildWorker(WorkerBuildOptions{
		WorkerID:    workerID,
		Task:        task,
		InputData:   r.workerInputData(nil, workerID, task),
		ProfileName: stringValue(meta["profile_name"]),
	})
	worker.conversation = conv
	worker.resumed = true
	// A resumed worker is exempt from adaptive clamping: its cursor restores
	// the cumulative tool-call count, so a colony-norm clamp below that would
	// flip it straight into grace. Upstream pins it.
	worker.BudgetPinned = true
	worker.BatchID = batchID
	worker.BatchIndex = batchIndex
	worker.BatchSize = batchSize
	if worker.Goal = stringValue(meta["goal"]); worker.Goal == "" {
		worker.Goal = task
	}
	cfg, cfgErr := BuildWorkerLoopConfig(loopOverrides)
	if cfgErr != nil {
		return nil, cfgErr
	}
	worker.loopConfig = &cfg
	worker.loopOverrides = loopOverrides

	// Optional steering turn from the queen, read as the worker's latest user
	// message when the loop re-enters (upstream injects the same
	// "[Resume guidance from the queen]" framing).
	if text := strings.TrimSpace(guidance); text != "" {
		_, _ = conv.AddUserMessage("[Resume guidance from the queen]: "+text, agentloop.UserMessageOptions{})
	}

	r.mu.Lock()
	r.workers[workerID] = worker
	r.mu.Unlock()
	return worker, nil
}

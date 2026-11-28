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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// WorkerResumer is the resume half of the colony runtime run_worker drives. It
// stays a separate interface from RunWorkerSpawner so a spawner fake without
// disk transcripts still compiles (and still reports resume unsupported).
type WorkerResumer interface {
	ResumeWorkers(ctx context.Context, ids []string, toolsOverride []llm.ToolDef, guidance string, loopOverrides map[string]any, batchID string) ([]string, []map[string]any)
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
func (r *ColonyRuntime) ResumeWorkers(ctx context.Context, ids []string, toolsOverride []llm.ToolDef, guidance string, loopOverrides map[string]any, batchID string) ([]string, []map[string]any) {
	resumed := make([]string, 0, len(ids))
	failures := make([]map[string]any, 0)
	if r == nil {
		return resumed, []map[string]any{{"error": "no colony runtime"}}
	}
	for index, raw := range ids {
		workerID := strings.TrimSpace(raw)
		if workerID == "" {
			continue
		}
		// Batch coordinates are positional, as upstream's run_worker resume
		// loop stamps them: batch_index = idx + 1 over the REQUESTED ids, so a
		// failed id still consumes its ordinal, and batch_size is the request
		// size for every resume in the pass.
		worker, err := r.resumeWorkerOverTranscript(workerID, toolsOverride, guidance, loopOverrides, batchID, index+1, len(ids))
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
func (r *ColonyRuntime) resumeWorkerOverTranscript(workerID string, toolsOverride []llm.ToolDef, guidance string, loopOverrides map[string]any, batchID string, batchIndex, batchSize int) (*Worker, error) {
	if r == nil {
		return nil, fmt.Errorf("no colony runtime")
	}
	if existing := r.GetWorker(workerID); existing != nil && existing.IsActive() {
		return nil, fmt.Errorf("it is still active (%s); a running worker cannot be resumed", existing.Status())
	}
	workerDir := r.workerStorage(workerID)
	// A23: the retention janitor tombstones a worker in result.json BEFORE it
	// deletes the transcript, so a resume that raced it would run on a
	// half-removed store. Refuse the tombstone outright.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
	// (result.json._janitor guard)
	if workerPrunedByJanitor(workerDir) {
		return nil, fmt.Errorf("it was pruned by the retention janitor; its transcript is archived and cannot be resumed")
	}
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
	profileName, integrations, browserProfile := r.resolveWorkerProfileBinding(stringValue(meta["profile_name"]))
	worker := r.BuildWorker(WorkerBuildOptions{
		WorkerID:       workerID,
		Task:           task,
		InputData:      r.workerInputData(nil, workerID, task),
		ProfileName:    profileName,
		Integrations:   integrations,
		BrowserProfile: browserProfile,
		// A23: apply the caller's tool scope (run_worker passes the queen's
		// live worker surface; recovery leaves it nil). Nil keeps the spawn
		// default, so a restart-recovered worker runs exactly like one
		// spawned now.
		SpawnTools: toolsOverride,
	})
	worker.conversation = conv
	worker.resumed = true
	worker.resumeTools = append([]llm.ToolDef(nil), toolsOverride...)
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

	// A23: the queen's steering turn is loop input, not a persisted user
	// message. Upstream resumes through agent_loop.inject_event(
	// is_client_input=True) so the guidance is drained at the first resumed
	// iteration boundary; adding it to the transcript here would rewrite the
	// saved conversation and replay on every later resume. The loop is built
	// in runWorker, so the text parks on the worker and the run path injects
	// it before ExecuteTurn.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
	// (inject_event("[Resume guidance from the queen]: ...", is_client_input=True))
	if text := strings.TrimSpace(guidance); text != "" {
		worker.resumeGuidance = "[Resume guidance from the queen]: " + text
	}

	r.mu.Lock()
	r.workers[workerID] = worker
	r.mu.Unlock()
	return worker, nil
}

// workerPrunedByJanitor reports whether the retention janitor has already
// tombstoned this worker's result.json. The tombstone lands before the
// transcript is deleted, so it is the only reliable "do not resume" marker
// while a prune is in flight.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
// (result.json._janitor guard)
func workerPrunedByJanitor(workerDir string) bool {
	raw, err := os.ReadFile(filepath.Join(workerDir, workerResultFile))
	if err != nil {
		return false
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return false
	}
	_, pruned := result["_janitor"]
	return pruned
}

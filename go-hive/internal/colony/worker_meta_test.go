package colony

// Worker spawn metadata: the lineage breadcrumb written once at spawn
// (queen_session_id / queen_name / spawned_at / worker_seq) plus the batch
// coordinates a fan-out carries (1-based batch_index, minted rpw_ batch_id).
//
// These are the fields the server's colony worker walk reads after the runtime
// is gone, so a mismatch here makes a worker invisible (or mis-numbered) on the
// workers surface even though it ran fine.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const workerMetaTestQueen = "queen_growth"

func makeWorkerMetaTestColony(t *testing.T) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         newScriptedLLM(textResponse("done")),
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus:  eventbus.NewEventBus(),
		StreamID:  "session_20250101_110000_beefbeef",
		QueenName: workerMetaTestQueen,
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// readWorkerMetaFile decodes <colony>/workers/<id>/meta.json.
func readWorkerMetaFile(t *testing.T, runtime *ColonyRuntime, workerID string) map[string]any {
	t.Helper()
	path := filepath.Join(runtime.workerStorage(workerID), workerMetaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	meta := map[string]any{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return meta
}

// Parity: core/framework/host/colony_runtime.py::ColonyRuntime.spawn
// (the meta.json lineage breadcrumb written before the worker starts)
func TestWorkerMetaRecordsSpawnLineage(t *testing.T) {
	runtime := makeWorkerMetaTestColony(t)

	before := float64(time.Now().UnixNano()) / 1e9
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "scrape page 1"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("SpawnBatch returned %d ids, want 1", len(ids))
	}
	after := float64(time.Now().UnixNano()) / 1e9

	meta := readWorkerMetaFile(t, runtime, ids[0])

	if got := meta["queen_session_id"]; got != runtime.StreamID() {
		t.Fatalf("meta.queen_session_id = %v, want %q", got, runtime.StreamID())
	}
	if got := meta["queen_name"]; got != workerMetaTestQueen {
		t.Fatalf("meta.queen_name = %v, want %q", got, workerMetaTestQueen)
	}
	spawnedAt, ok := meta["spawned_at"].(float64)
	if !ok {
		t.Fatalf("meta.spawned_at = %T, want a number (epoch seconds)", meta["spawned_at"])
	}
	if spawnedAt < before || spawnedAt > after {
		t.Fatalf("meta.spawned_at = %v, want within [%v, %v]", spawnedAt, before, after)
	}
	if got, present := meta["worker_seq"]; !present || got != float64(0) {
		t.Fatalf("meta.worker_seq = %v (present=%v), want 0", got, present)
	}
	if got := meta["colony_id"]; got != runtime.StreamID() {
		t.Fatalf("meta.colony_id = %v, want %q", got, runtime.StreamID())
	}
}

// Parity: core/framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
// (batch_id minted per call, batch_index 1-based via enumerate(start=1))
// Parity: core/tests/test_run_worker_tool.py::test_subagent_report_carries_batch_metadata
func TestSpawnBatchCoordinatesMatchUpstream(t *testing.T) {
	runtime := makeWorkerMetaTestColony(t)

	ids, err := runtime.SpawnBatchWithOptions(context.Background(), []BatchTask{
		{Task: "task-A"},
		{Task: "task-B"},
		{Task: "task-C", WorkerSeq: 7},
	}, SpawnBatchOptions{})
	if err != nil {
		t.Fatalf("SpawnBatchWithOptions: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("spawned %d workers, want 3", len(ids))
	}

	batchIDs := map[string]struct{}{}
	for index, id := range ids {
		meta := readWorkerMetaFile(t, runtime, id)
		batchID, _ := meta["batch_id"].(string)
		if !strings.HasPrefix(batchID, spawnBatchPrefix) {
			t.Fatalf("worker %d batch_id = %q, want an %q prefix", index, batchID, spawnBatchPrefix)
		}
		if len(batchID) != len(spawnBatchPrefix)+len("20060102T150405Z")+1+8 {
			t.Fatalf("worker %d batch_id = %q, want <ts>_<8 hex> after the prefix", index, batchID)
		}
		batchIDs[batchID] = struct{}{}
		if got := meta["batch_index"]; got != float64(index+1) {
			t.Fatalf("worker %d batch_index = %v, want %d (1-based)", index, got, index+1)
		}
		if got := meta["batch_size"]; got != float64(3) {
			t.Fatalf("worker %d batch_size = %v, want 3", index, got)
		}
		wantSeq := float64(0)
		if index == 2 {
			wantSeq = 7
		}
		if got := meta["worker_seq"]; got != wantSeq {
			t.Fatalf("worker %d worker_seq = %v, want %v", index, got, wantSeq)
		}
	}
	if len(batchIDs) != 1 {
		t.Fatalf("batch ids = %v, want one shared id for the whole fan-out", batchIDs)
	}

	// The runtime view the workers surface serializes must agree with meta.
	if info := runtime.GetWorker(ids[0]).Info(); info.Index != 1 || info.Size != 3 {
		t.Fatalf("Info() batch = %d/%d, want 1/3", info.Index, info.Size)
	}
}

// Parity: core/framework/host/colony_runtime.py::ColonyRuntime.spawn
// (spawn(count=n) carries no batch_id — only spawn_batch mints one)
//
// Negative regression: the minting rule must not leak into solo spawns. A
// one-off worker rendered as "batch 1 of 1" is a visible dashboard regression,
// so batch coordinates stay zero and the batch id stays empty.
func TestSoloSpawnWorkerHasNoBatchCoordinates(t *testing.T) {
	runtime := makeWorkerMetaTestColony(t)

	ids, err := runtime.Spawn(context.Background(), "one-off task", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	meta := readWorkerMetaFile(t, runtime, ids[0])
	if got, present := meta["batch_id"]; present && got != nil {
		t.Fatalf("meta.batch_id = %v, want null for a solo spawn", got)
	}
	if got := meta["batch_index"]; got != float64(0) {
		t.Fatalf("meta.batch_index = %v, want 0 for a solo spawn", got)
	}
	if got := meta["batch_size"]; got != float64(0) {
		t.Fatalf("meta.batch_size = %v, want 0 for a solo spawn", got)
	}
	if got := runtime.GetWorker(ids[0]).Info(); got.BatchID != "" || got.Index != 0 {
		t.Fatalf("Info() batch = %q/%d, want no batch", got.BatchID, got.Index)
	}
}

// Parity: core/tests/test_run_worker_tool.py::test_run_worker_resume_mode_calls_resume_worker
// (batch_index = idx + 1 over the requested ids, batch_size = len(ids))
func TestResumeWorkersNumberBatchFromOne(t *testing.T) {
	runtime := makeWorkerMetaTestColony(t)
	ctx := context.Background()

	ids, err := runtime.SpawnBatch(ctx, []BatchTask{{Task: "resume me"}, {Task: "resume me too"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	for _, id := range ids {
		if worker := runtime.GetWorker(id); worker != nil {
			worker.Wait()
		}
	}

	resumed, failures := runtime.ResumeWorkers(ctx, ids, nil, "", nil, newResumeBatchID())
	if len(failures) != 0 {
		t.Fatalf("ResumeWorkers failures = %v, want none", failures)
	}
	if len(resumed) != 2 {
		t.Fatalf("resumed %d workers, want 2", len(resumed))
	}
	for index, id := range resumed {
		info := runtime.GetWorker(id).Info()
		if info.Index != index+1 {
			t.Fatalf("resumed worker %d Index = %d, want %d", index, info.Index, index+1)
		}
		if info.Size != 2 {
			t.Fatalf("resumed worker %d Size = %d, want 2", index, info.Size)
		}
		if !strings.HasPrefix(info.BatchID, resumeBatchPrefix) {
			t.Fatalf("resumed worker %d BatchID = %q, want an %q prefix", index, info.BatchID, resumeBatchPrefix)
		}
	}
}

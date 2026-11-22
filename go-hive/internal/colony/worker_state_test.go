package colony

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// durableRuntimeOptions builds a colony runtime over a caller-owned storage
// directory, so a test can restart a runtime over the same colony tree.
func durableRuntimeOptions(storage string, provider llm.Completer) RuntimeOptions {
	return RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: storage,
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "durable_colony",
	}
}

// waitForRunStateStatus waits until the run-state record reports the given
// status. The terminal status is written by the worker's report hook after the
// status flips, so polling Status().Done() alone can observe it too early.
func waitForRunStateStatus(runtime *ColonyRuntime, workerID, want string) map[string]any {
	deadline := time.Now().Add(5 * time.Second)
	var record map[string]any
	for time.Now().Before(deadline) {
		record = readWorkerRunStateFile(runtime.workerRunStatePath(workerID))
		if record != nil && record["status"] == want {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	return record
}

// Parity: framework/host/worker.py::WorkerStatus
func TestWorkerRunStateTracksLifecycle(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(reportResponse("success", "done", nil)), eventbus.NewEventBus())
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "record my state"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
		t.Fatalf("worker status = %s, want a terminal state", worker.Status())
	}
	record := waitForRunStateStatus(runtime, ids[0], string(WorkerCompleted))
	if record == nil {
		t.Fatalf("runs/%s.json was not written", ids[0])
	}
	if record["status"] != string(WorkerCompleted) {
		t.Fatalf("run state status = %#v, want completed", record["status"])
	}
	if record["colony_id"] != "test_colony" || record["task"] != "record my state" {
		t.Fatalf("run state record = %#v", record)
	}
	workerCopy := waitForRunStateFileStatus(runtime.workerStatePath(ids[0]), string(WorkerCompleted))
	if workerCopy == nil || workerCopy["status"] != string(WorkerCompleted) {
		t.Fatalf("worker state.json = %#v, want completed", workerCopy)
	}
}

// waitForRunStateFileStatus waits until one run-state file reports the given
// status (the two copies are written back-to-back, so a reader can catch the
// window between them).
func waitForRunStateFileStatus(path, want string) map[string]any {
	deadline := time.Now().Add(5 * time.Second)
	var record map[string]any
	for time.Now().Before(deadline) {
		record = readWorkerRunStateFile(path)
		if record != nil && record["status"] == want {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	return record
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
func TestStartResumesInFlightWorkerAfterRestart(t *testing.T) {
	storage := t.TempDir()
	firstProvider := newScriptedLLM(reportResponse("success", "first pass", nil))
	first := NewColonyRuntime(durableRuntimeOptions(storage, firstProvider))
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	ids, err := first.SpawnBatch(context.Background(), []BatchTask{{Task: "finish the ledger"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	workerID := ids[0]
	worker := first.GetWorker(workerID)
	if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
		t.Fatalf("worker status = %s, want a terminal state", worker.Status())
	}
	if record := waitForRunStateStatus(first, workerID, string(WorkerCompleted)); record == nil || record["status"] != string(WorkerCompleted) {
		t.Fatalf("first run never recorded a terminal state: %#v", record)
	}
	first.Stop()
	if firstProvider.callCount() == 0 {
		t.Fatal("first run never called the model")
	}

	// Simulate a process that died mid-worker: the transcript is on disk, the
	// terminal result was never written, and the run record still says running.
	for _, path := range []string{first.workerRunStatePath(workerID), first.workerStatePath(workerID)} {
		record := readWorkerRunStateFile(path)
		if record == nil {
			t.Fatalf("missing run state at %s", path)
		}
		record["status"] = string(WorkerRunning)
		if err := atomicfs.WriteJSON(path, record, 0o644); err != nil {
			t.Fatalf("rewrite run state: %v", err)
		}
	}
	if err := os.Remove(filepath.Join(first.workerStorage(workerID), workerResultFile)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove terminal result: %v", err)
	}

	secondProvider := newScriptedLLM(reportResponse("success", "second pass", nil))
	second := NewColonyRuntime(durableRuntimeOptions(storage, secondProvider))
	defer second.Stop()
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	resumedWorker := second.GetWorker(workerID)
	if resumedWorker == nil {
		t.Fatalf("Start did not rebuild in-flight worker %s", workerID)
	}
	if !eventually(t, 5*time.Second, func() bool { return resumedWorker.Status().Done() }) {
		t.Fatalf("resumed worker status = %s, want a terminal state", resumedWorker.Status())
	}
	if secondProvider.callCount() == 0 {
		t.Fatal("resumed worker never called the model")
	}
	last, ok := lastScriptedCall(secondProvider)
	if !ok {
		t.Fatal("no recorded LLM call")
	}
	if userText := firstUserText(last.Messages); !strings.Contains(userText, "finish the ledger") {
		t.Fatalf("resumed turn lost the original task: %q", userText)
	}

	record := waitForRunStateStatus(second, workerID, string(WorkerCompleted))
	if record == nil {
		t.Fatalf("run state record disappeared")
	}
	if record["status"] != string(WorkerCompleted) {
		t.Fatalf("run state status after resume = %#v, want completed", record["status"])
	}
	if record["resume_status"] != workerResumeStatusResumed {
		t.Fatalf("resume_status = %#v, want resumed", record["resume_status"])
	}
	if stateInt(record["resume_count"]) != 1 {
		t.Fatalf("resume_count = %#v, want 1", record["resume_count"])
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
func TestStartMarksUnrecoverableInFlightWorkerAndSkipsIt(t *testing.T) {
	storage := t.TempDir()
	runtime := NewColonyRuntime(durableRuntimeOptions(storage, newScriptedLLM(textResponse("ok"))))
	defer runtime.Stop()

	// A run record with no transcript is the "cannot be rebuilt" case: a
	// worker directory that was pruned or never flushed.
	ghost := "worker_ghost"
	record := map[string]any{
		"worker_id": ghost,
		"colony_id": "durable_colony",
		"task":      "lost work",
		"status":    string(WorkerRunning),
	}
	if err := atomicfs.WriteJSON(runtime.workerRunStatePath(ghost), record, 0o644); err != nil {
		t.Fatalf("seed run state: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if runtime.GetWorker(ghost) != nil {
		t.Fatal("unrecoverable worker was rebuilt")
	}
	updated := readWorkerRunStateFile(runtime.workerRunStatePath(ghost))
	if updated == nil {
		t.Fatalf("unrecoverable record disappeared")
	}
	if updated["resume_status"] != workerResumeStatusUnrecoverable {
		t.Fatalf("resume_status = %#v, want unrecoverable", updated["resume_status"])
	}
	if message, _ := updated["resume_error"].(string); !strings.Contains(message, "no saved conversation") {
		t.Fatalf("resume_error = %#v, want the missing-transcript reason", updated["resume_error"])
	}
}

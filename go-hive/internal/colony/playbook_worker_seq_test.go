package colony

// Playbook dispatch ordinals. A playbook run spawns every worker as its own
// size-1 batch sharing one batch_id, so batch_index is always 1 and cannot
// disambiguate peers — the run-scoped worker_seq is what the dashboard badge
// and the cloud-usage suffix read instead.

import (
	"context"
	"fmt"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/playbook"
)

// Parity: core/framework/tools/playbook_tools.py::run_playbook (_dispatch_seq
// = itertools.count(1), stamped onto each spawn spec as worker_seq)
func TestPlaybookDispatchStampsRunScopedWorkerSeq(t *testing.T) {
	fake := &fakePlaybookRuntime{}
	executor := &workerDispatchExecutor{runtime: fake, runID: newPlaybookRunID()}

	for index := 0; index < 3; index++ {
		if _, err := executor.dispatchOne(context.Background(), fmt.Sprintf("row %d", index), playbook.DispatchOptions{}); err != nil {
			t.Fatalf("dispatch %d: %v", index, err)
		}
	}

	fake.mu.Lock()
	spawned := append([]BatchTask(nil), fake.spawned...)
	runIDs := append([]string(nil), fake.runIDs...)
	fake.mu.Unlock()

	if len(spawned) != 3 {
		t.Fatalf("dispatched %d workers, want 3", len(spawned))
	}
	seen := map[int]struct{}{}
	for index, task := range spawned {
		if task.WorkerSeq != index+1 {
			t.Fatalf("dispatch %d worker_seq = %d, want %d (1-based, run-scoped)", index, task.WorkerSeq, index+1)
		}
		if _, duplicate := seen[task.WorkerSeq]; duplicate {
			t.Fatalf("worker_seq %d was handed out twice in one run", task.WorkerSeq)
		}
		seen[task.WorkerSeq] = struct{}{}
		// Every dispatch shares the run's batch id; only worker_seq separates them.
		if runIDs[index] != runIDs[0] || runIDs[index] == "" {
			t.Fatalf("dispatch %d batch_id = %q, want the shared run id %q", index, runIDs[index], runIDs[0])
		}
	}
}

// Parity-exempt: the Go dispatch counter is an atomic because concurrent
// dispatches share one executor; upstream's itertools.count runs on a single
// asyncio loop, so there is no Python concurrency test to mirror.
func TestPlaybookDispatchWorkerSeqUniqueUnderConcurrency(t *testing.T) {
	fake := &fakePlaybookRuntime{}
	executor := &workerDispatchExecutor{runtime: fake, runID: newPlaybookRunID()}

	const dispatches = 8
	done := make(chan error, dispatches)
	for index := 0; index < dispatches; index++ {
		go func(index int) {
			_, err := executor.dispatchOne(context.Background(), fmt.Sprintf("row %d", index), playbook.DispatchOptions{})
			done <- err
		}(index)
	}
	for index := 0; index < dispatches; index++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent dispatch: %v", err)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.spawned) != dispatches {
		t.Fatalf("dispatched %d workers, want %d", len(fake.spawned), dispatches)
	}
	seen := map[int]struct{}{}
	for _, task := range fake.spawned {
		if task.WorkerSeq <= 0 {
			t.Fatalf("worker_seq = %d, want a positive ordinal", task.WorkerSeq)
		}
		if _, duplicate := seen[task.WorkerSeq]; duplicate {
			t.Fatalf("worker_seq %d was handed out twice", task.WorkerSeq)
		}
		seen[task.WorkerSeq] = struct{}{}
	}
}

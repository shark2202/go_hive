package colony

import (
	"context"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// Parity: core/tests/test_worker_started_at.py::test_info_started_at_is_wall_clock
func TestWorkerInfoStartedAtIsWallClock(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(textResponse("say hello")), eventbus.NewEventBus())

	before := time.Now()
	ids, err := runtime.Spawn(context.Background(), "say hello", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("Spawn returned %d ids, want 1", len(ids))
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}

	worker.Wait()
	after := time.Now()

	started := worker.Info().StartedAt
	beforeSeconds := float64(before.UnixNano()) / 1e9
	afterSeconds := float64(after.UnixNano()) / 1e9
	if started < beforeSeconds || started > afterSeconds {
		t.Fatalf("Info().StartedAt = %v, want within [%v, %v]", started, beforeSeconds, afterSeconds)
	}
	if started < 1e9 {
		t.Fatalf("Info().StartedAt = %v, want a wall-clock epoch timestamp, not monotonic", started)
	}
	if delta := worker.StartedAt().Sub(time.Unix(0, int64(started*1e9))); delta > time.Microsecond || delta < -time.Microsecond {
		t.Errorf("StartedAt() = %v does not match Info().StartedAt = %v", worker.StartedAt(), started)
	}
}

// Parity: framework/host/worker.py::WorkerInfo
func TestWorkerInfoStartedAtDefaultsToZero(t *testing.T) {
	worker := NewWorker("w1", "t", "s1")
	if got := worker.Info().StartedAt; got != 0 {
		t.Fatalf("queued worker Info().StartedAt = %v, want 0", got)
	}
	if !worker.StartedAt().IsZero() {
		t.Fatalf("queued worker StartedAt() = %v, want the zero time", worker.StartedAt())
	}
}

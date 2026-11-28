package memory

// Shutdown is the bounded teardown path: it waits for an in-flight reflection
// only as long as its budget allows, cancels what is still running when the
// budget is spent, and still runs the final short reflection when there is room
// left. Upstream spends the same budget once, at server exit
// (loader/cli.py::_SHUTDOWN_GRACE_S = 6.0 →
// session_manager.finish_background_tasks(6)).

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Parity: core/framework/server/session_manager.py::finish_background_tasks
func TestReflectionSchedulerShutdownRunsFinalPassWithinBudget(t *testing.T) {
	var calls int32
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig { return ReflectionConfig{EveryTurns: 1, LongEvery: 1000} },
		Short: func(context.Context) LoopResult {
			atomic.AddInt32(&calls, 1)
			return LoopResult{Success: true}
		},
	}
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if !scheduler.Shutdown(ctx) {
		t.Fatal("Shutdown reported undrained with nothing in flight")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("short passes = %d, want the one teardown pass", got)
	}
	// Calling it again (a route and the runtime both tearing down) neither
	// repeats the final pass nor reports a second result.
	if !scheduler.Shutdown(ctx) {
		t.Fatal("second Shutdown reported undrained")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("second Shutdown ran another pass: %d calls", got)
	}
}

// Parity: core/framework/server/session_manager.py::finish_background_tasks
func TestReflectionSchedulerShutdownCancelsOverBudgetPass(t *testing.T) {
	var calls, sawCancelled int32
	started := make(chan struct{})
	exited := make(chan struct{})
	short := func(ctx context.Context) LoopResult {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-ctx.Done()
			atomic.StoreInt32(&sawCancelled, 1)
			close(exited)
		}
		return LoopResult{Success: true}
	}
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig { return ReflectionConfig{EveryTurns: 1, LongEvery: 1000} },
		Short:  short,
	}
	scheduler.OnTurnDone(context.Background(), "queen", "turn_done")
	<-started

	ctx, stop := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer stop()
	begin := time.Now()
	drained := scheduler.Shutdown(ctx)
	elapsed := time.Since(begin)
	if !drained {
		t.Fatal("Shutdown reported a pass still running although the pass exited on cancellation")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %v, want about the 120ms budget", elapsed)
	}
	if atomic.LoadInt32(&sawCancelled) != 1 {
		t.Fatal("the in-flight pass never observed its cancellation")
	}
	// The goroutine is gone before Shutdown returns, and the final pass was
	// skipped: the budget it would have run on was the one just spent.
	select {
	case <-exited:
	default:
		t.Fatal("the cancelled pass had not exited when Shutdown returned")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("a final pass ran after the budget was spent: %d calls", got)
	}
}

// Parity: core/framework/server/session_manager.py::finish_background_tasks
// Two-sided counterpart of the cancel test: a pass that finishes inside the
// budget is never cancelled, and the teardown pass still runs afterwards.
func TestReflectionSchedulerShutdownDrainsFastPassThenReflects(t *testing.T) {
	var calls, sawCancelled int32
	started := make(chan struct{})
	release := make(chan struct{})
	short := func(ctx context.Context) LoopResult {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-release
			if ctx.Err() != nil {
				atomic.StoreInt32(&sawCancelled, 1)
			}
		}
		return LoopResult{Success: true}
	}
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig { return ReflectionConfig{EveryTurns: 1, LongEvery: 1000} },
		Short:  short,
	}
	scheduler.OnTurnDone(context.Background(), "queen", "turn_done")
	<-started

	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	closed := make(chan bool, 1)
	go func() { closed <- scheduler.Shutdown(ctx) }()
	select {
	case <-closed:
		t.Fatal("Shutdown returned while the pass was still inside its budget")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case drained := <-closed:
		if !drained {
			t.Fatal("Shutdown reported undrained although the pass finished in budget")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return after the pass finished")
	}
	if atomic.LoadInt32(&sawCancelled) != 0 {
		t.Fatal("a pass that fit the budget was cancelled")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("short passes = %d, want the triggered pass plus the teardown pass", got)
	}
}

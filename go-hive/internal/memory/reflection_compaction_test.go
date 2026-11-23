package memory

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Parity: core/framework/agents/queen/reflection_agent.py::_on_compaction
func TestReflectionSchedulerOnCompactionRunsLongPass(t *testing.T) {
	var shortCalls, longCalls int
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig { return ReflectionConfig{EveryTurns: 6, LongEvery: 4} },
		Spawn:  func(work func()) { work() },
		Short: func(context.Context) LoopResult {
			shortCalls++
			return LoopResult{Success: true}
		},
		Long: func(context.Context) LoopResult {
			longCalls++
			return LoopResult{Success: true}
		},
	}

	scheduler.OnCompaction(context.Background(), "queen")

	if shortCalls != 0 || longCalls != 1 {
		t.Fatalf("short/long calls = %d/%d, want 0/1 (compaction runs the long pass only)", shortCalls, longCalls)
	}
}

// Parity: core/framework/agents/queen/reflection_agent.py::_on_compaction
func TestReflectionSchedulerOnCompactionIgnoresOtherStreams(t *testing.T) {
	var longCalls int32
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig { return ReflectionConfig{EveryTurns: 6, LongEvery: 4} },
		Spawn:  func(work func()) { work() },
		Long: func(context.Context) LoopResult {
			atomic.AddInt32(&longCalls, 1)
			return LoopResult{Success: true}
		},
	}

	scheduler.OnCompaction(context.Background(), "worker-1")

	if got := atomic.LoadInt32(&longCalls); got != 0 {
		t.Fatalf("long reflections = %d, want 0 for a non-queen stream", got)
	}
}

// Parity: core/framework/agents/queen/reflection_agent.py::_on_compaction
func TestReflectionSchedulerOnCompactionIgnoredAfterClose(t *testing.T) {
	var longCalls int32
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig { return ReflectionConfig{EveryTurns: 6, LongEvery: 4} },
		Spawn:  func(work func()) { work() },
		Short:  func(context.Context) LoopResult { return LoopResult{Success: true} },
		Long: func(context.Context) LoopResult {
			atomic.AddInt32(&longCalls, 1)
			return LoopResult{Success: true}
		},
	}
	scheduler.Close(time.Second)

	scheduler.OnCompaction(context.Background(), "queen")

	if got := atomic.LoadInt32(&longCalls); got != 0 {
		t.Fatalf("long reflections = %d, want 0 after Close", got)
	}
}

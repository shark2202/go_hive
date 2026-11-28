package server

// Close spends one shutdown budget on everything stopping a session starts — the
// final short reflection and the timeline flush — instead of the old 2s
// reflection grace plus a 30s timeline flush. Upstream's stop_session starts both
// fire-and-forget and the server spends one 6s grace on them at exit
// (loader/cli.py::_SHUTDOWN_GRACE_S, session_manager.finish_background_tasks).

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

// shutdownProbeLLM is the session's model. A timeline flush calls it once per
// pending session; blocking mode holds the call until its context is cancelled,
// so a teardown's budget is observable.
type shutdownProbeLLM struct {
	block bool

	mu    sync.Mutex
	calls int
}

func (m *shutdownProbeLLM) ModelName() string { return "shutdown-probe" }

func (m *shutdownProbeLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.block {
		<-ctx.Done()
		return llm.Completion{}, ctx.Err()
	}
	return llm.Completion{Content: "ok"}, nil
}

func (m *shutdownProbeLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// shutdownTestRuntime builds a runtime whose session directory names itself the
// way the timeline parser expects and carries one user message, so the timeline
// flush has pending work and really reaches the model.
func shutdownTestRuntime(t *testing.T, model llm.Completer) (*ChatRuntime, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "session_20260101_120000_shut")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	events := `{"type":"client_input_received","timestamp":"2026-01-01T12:00:05+00:00","data":{"content":"hello there"}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      root,
		HomeRoot:  t.TempDir(),
		QueenName: "queen_shutdown",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	if runtime.reflection == nil {
		_ = runtime.Close()
		t.Fatal("runtime built no reflection scheduler")
	}
	// The timeline feature flag is read from configuration at construction; the
	// test turns the writer on directly, like a home with memory_timeline set.
	runtime.timelineEnabled = true
	runtime.shutdownGrace = 150 * time.Millisecond
	runtime.reflection.Config = func() memory.ReflectionConfig {
		return memory.ReflectionConfig{EveryTurns: 1, LongEvery: 1000}
	}
	return runtime, root
}

// Parity: core/framework/server/session_manager.py::finish_background_tasks
func TestChatRuntimeCloseBoundsShutdownWorkToItsBudget(t *testing.T) {
	model := &shutdownProbeLLM{block: true}
	runtime, _ := shutdownTestRuntime(t, model)

	var passes int32
	started := make(chan struct{})
	exited := make(chan struct{})
	runtime.reflection.Short = func(ctx context.Context) memory.LoopResult {
		if atomic.AddInt32(&passes, 1) == 1 {
			close(started)
			<-ctx.Done()
			close(exited)
		}
		return memory.LoopResult{Success: true}
	}
	runtime.reflection.OnTurnDone(context.Background(), "queen", "turn_done")
	<-started

	begin := time.Now()
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(begin)
	// The old teardown was 2s (reflection grace) and up to 30s (timeline flush);
	// both legs now share the 150ms budget the test installed.
	if elapsed > 2*time.Second {
		t.Fatalf("Close took %v; both shutdown paths should share the 150ms budget", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Close returned after %v, before the budget was spent", elapsed)
	}
	if model.callCount() == 0 {
		t.Fatal("the timeline flush never reached the model")
	}
	// Nothing is left writing: the cancelled reflection goroutine has exited and
	// the final pass was skipped because its budget was the one just spent.
	select {
	case <-exited:
	default:
		t.Fatal("the reflection goroutine had not exited when Close returned")
	}
	if got := atomic.LoadInt32(&passes); got != 1 {
		t.Fatalf("reflection passes = %d, want only the cancelled in-flight one", got)
	}
}

// Parity: core/framework/server/session_manager.py::finish_background_tasks
// Two-sided counterpart: when the budget has room, Close still does the work —
// the final reflection runs and the timeline flush completes — rather than only
// cancelling.
func TestChatRuntimeCloseStillRunsShutdownWorkWhenIdle(t *testing.T) {
	model := &shutdownProbeLLM{}
	runtime, _ := shutdownTestRuntime(t, model)

	var passes int32
	runtime.reflection.Short = func(context.Context) memory.LoopResult {
		atomic.AddInt32(&passes, 1)
		return memory.LoopResult{Success: true}
	}

	begin := time.Now()
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(begin)
	if elapsed > 2*time.Second {
		t.Fatalf("an idle teardown took %v, want well under the installed budget", elapsed)
	}
	if got := atomic.LoadInt32(&passes); got != 1 {
		t.Fatalf("reflection passes = %d, want the one teardown pass", got)
	}
	if model.callCount() == 0 {
		t.Fatal("the timeline flush never reached the model")
	}
}

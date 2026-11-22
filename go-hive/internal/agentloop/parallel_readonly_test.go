package agentloop

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// orderObserver records tool events in emission order.
type orderObserver struct {
	mu        sync.Mutex
	started   []string
	completed []string
}

func (o *orderObserver) IterationStarted(int)                {}
func (o *orderObserver) LLMTurnComplete(llm.Completion, int) {}
func (o *orderObserver) AssistantText(string, string, int)   {}
func (o *orderObserver) ToolCallStarted(call ToolCallRequest) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = append(o.started, call.Name)
}
func (o *orderObserver) ToolCallCompleted(call ToolCallRequest, _ ToolResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.completed = append(o.completed, call.Name)
}

// overlapProbe tracks the maximum number of executor calls in flight. The
// first two calls rendezvous on release, so a sequential run blocks until the
// timeout and can never reach 2.
type overlapProbe struct {
	inFlight int32
	max      int32
	release  chan struct{}
	once     sync.Once
}

func newOverlapProbe() *overlapProbe { return &overlapProbe{release: make(chan struct{})} }

func (p *overlapProbe) enter() {
	n := atomic.AddInt32(&p.inFlight, 1)
	for {
		max := atomic.LoadInt32(&p.max)
		if n <= max || atomic.CompareAndSwapInt32(&p.max, max, n) {
			break
		}
	}
	if n >= 2 {
		p.once.Do(func() { close(p.release) })
	}
	select {
	case <-p.release:
	case <-time.After(300 * time.Millisecond):
	}
}

func (p *overlapProbe) exit() { atomic.AddInt32(&p.inFlight, -1) }

func readOnlyBatchCompleter() *scriptedCompleter {
	return &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "c1", Name: "read_slow", Arguments: `{"path":"a"}`},
			{ID: "c2", Name: "read_fast", Arguments: `{"path":"b"}`},
		}},
		{content: "done"},
	}}
}

func runReadOnlyBatch(t *testing.T, parallel bool, markSafe bool) (*AgentLoop, *TurnResult, *orderObserver) {
	t.Helper()
	probe := newOverlapProbe()
	executor := func(req ToolCallRequest) (ToolResult, error) {
		probe.enter()
		if req.Name == "read_slow" {
			time.Sleep(20 * time.Millisecond)
		}
		probe.exit()
		return ToolResult{Content: "result-" + req.Name}, nil
	}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3, ParallelReadOnlyTools: parallel}, executor)
	observer := &orderObserver{}
	ctx := midturnAgentContext(readOnlyBatchCompleter())
	ctx.Observer = observer
	conv := NewNodeConversation("You are a test assistant.")
	tools := []llm.ToolDef{
		{Name: "read_slow", ConcurrencySafe: markSafe},
		{Name: "read_fast", ConcurrencySafe: markSafe},
	}
	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, tools)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if got := atomic.LoadInt32(&probe.max); got != 2 && parallel && markSafe {
		t.Fatalf("max calls in flight = %d, want 2 (batch did not run concurrently)", got)
	}
	if got := atomic.LoadInt32(&probe.max); got != 1 && (!parallel || !markSafe) {
		t.Fatalf("max calls in flight = %d, want 1 (batch must stay sequential)", got)
	}
	return loop, &result, observer
}

// Parity-exempt: upstream partitions a batch into a concurrency-safe half and
// a serial half, both sharing an asyncio.Semaphore(_PARALLEL_CAP = 10); this
// port keeps the batch together and requires every call to be marked safe.
// The test pins overlap, per-index result order, and event order.
func TestParallelReadOnlyBatchRunsConcurrentlyAndKeepsOrder(t *testing.T) {
	_, result, observer := runReadOnlyBatch(t, true, true)
	if len(result.ToolResults) != 2 {
		t.Fatalf("tool results = %d, want 2", len(result.ToolResults))
	}
	if result.ToolResults[0].ToolName != "read_slow" || result.ToolResults[1].ToolName != "read_fast" {
		t.Fatalf("tool result order = [%s %s], want [read_slow read_fast]",
			result.ToolResults[0].ToolName, result.ToolResults[1].ToolName)
	}
	if result.ToolResults[0].Content != "result-read_slow" || result.ToolResults[1].Content != "result-read_fast" {
		t.Fatalf("results crossed: %q / %q", result.ToolResults[0].Content, result.ToolResults[1].Content)
	}
	want := []string{"read_slow", "read_fast"}
	for i, name := range want {
		if observer.started[i] != name || observer.completed[i] != name {
			t.Fatalf("event order = started %v completed %v, want %v", observer.started, observer.completed, want)
		}
	}
}

// Parity-exempt: default-off and unmarked-tool batches must stay sequential.
func TestParallelReadOnlyBatchStaysSequentialUnlessExplicitlyOptedIn(t *testing.T) {
	t.Run("flag off", func(t *testing.T) {
		runReadOnlyBatch(t, false, true)
	})
	t.Run("tool not marked safe", func(t *testing.T) {
		runReadOnlyBatch(t, true, false)
	})
}

// Parity-exempt: upstream caps its concurrency-safe batch with
// asyncio.Semaphore(_PARALLEL_CAP = 10) but has no dedicated test for the
// ceiling; this pins the port's matching in-flight bound (and proves the
// concurrent path is still reached).
func TestParallelReadOnlyBatchCapsInflightAtTen(t *testing.T) {
	const calls = 25
	batch := make([]llm.ToolCall, 0, calls)
	for i := 0; i < calls; i++ {
		batch = append(batch, llm.ToolCall{
			ID:        fmt.Sprintf("c%d", i),
			Name:      "read_safe",
			Arguments: fmt.Sprintf(`{"i":%d}`, i),
		})
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: batch},
		{content: "done"},
	}}
	var inFlight, peak int32
	executor := func(req ToolCallRequest) (ToolResult, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			observed := atomic.LoadInt32(&peak)
			if n <= observed || atomic.CompareAndSwapInt32(&peak, observed, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return ToolResult{Content: "ok"}, nil
	}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3, ParallelReadOnlyTools: true}, executor)
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")
	tools := []llm.ToolDef{{Name: "read_safe", ConcurrencySafe: true}}
	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, tools)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(result.ToolResults) != calls {
		t.Fatalf("tool results = %d, want %d", len(result.ToolResults), calls)
	}
	if got := atomic.LoadInt32(&peak); got > parallelReadOnlyCap {
		t.Fatalf("max calls in flight = %d, want <= %d", got, parallelReadOnlyCap)
	}
	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Fatalf("max calls in flight = %d, want the concurrent path (>= 2)", got)
	}
}

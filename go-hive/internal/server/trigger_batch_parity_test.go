package server

// Trigger batching parity: every trigger queued at one wake must land as ONE
// user message (shared [stamp] header, blocks joined by a blank line,
// is_trigger=true), the way the upstream node loop's drain_trigger_queue
// merges the queue before the next iteration. Each test names the upstream
// anchor it pins.

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func triggerTurn(kind, id string, payload map[string]any) chatTurnRequest {
	return chatTurnRequest{triggers: []agentloop.TriggerEvent{{
		TriggerType: kind,
		SourceID:    id,
		Payload:     payload,
	}}}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func TestDrainTriggerQueueBatchesContiguousTriggers(t *testing.T) {
	queue := []chatTurnRequest{
		triggerTurn("timer", "alpha", map[string]any{"task": "check inbox"}),
		triggerTurn("webhook", "beta", nil),
	}

	turn, rest := coalesceQueuedTurn(queue)
	if len(rest) != 0 {
		t.Fatalf("coalesce left %d queued turn(s) behind, want none", len(rest))
	}
	if len(turn.triggers) != 2 {
		t.Fatalf("batched turn carries %d trigger(s), want 2", len(turn.triggers))
	}
	if turn.triggers[0].SourceID != "alpha" || turn.triggers[1].SourceID != "beta" {
		t.Fatalf("batch order = %q,%q, want alpha,beta (FIFO)",
			turn.triggers[0].SourceID, turn.triggers[1].SourceID)
	}
	if turn.clientInput {
		t.Fatal("a trigger batch must not count as client input")
	}
	if got := formatTriggerBatch(turn.triggers, "2026-01-02 03:04 UTC"); strings.Count(got, "[TRIGGER:") != 2 {
		t.Fatalf("batched message carries %d block(s), want 2:\n%s", strings.Count(got, "[TRIGGER:"), got)
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_injection_queue
// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func TestDrainTriggerQueueKeepsClientTurnsInFifoOrder(t *testing.T) {
	queue := []chatTurnRequest{
		{message: "client one", clientInput: true},
		triggerTurn("timer", "alpha", nil),
		triggerTurn("timer", "beta", nil),
		{message: "client two", clientInput: true},
		triggerTurn("webhook", "gamma", nil),
	}

	var popped []chatTurnRequest
	for len(queue) > 0 {
		var turn chatTurnRequest
		turn, queue = coalesceQueuedTurn(queue)
		popped = append(popped, turn)
	}
	if len(popped) != 4 {
		t.Fatalf("drained %d turn(s), want 4 (client, batch of 2, client, batch of 1)", len(popped))
	}
	if popped[0].message != "client one" || !popped[0].clientInput {
		t.Fatalf("turn 0 = %#v, want the leading client message on its own turn", popped[0])
	}
	if len(popped[1].triggers) != 2 || popped[1].triggers[0].SourceID != "alpha" || popped[1].triggers[1].SourceID != "beta" {
		t.Fatalf("turn 1 = %#v, want the alpha+beta trigger batch", popped[1].triggers)
	}
	if popped[2].message != "client two" || !popped[2].clientInput {
		t.Fatalf("turn 2 = %#v, want the second client message after the trigger batch", popped[2])
	}
	if len(popped[3].triggers) != 1 || popped[3].triggers[0].SourceID != "gamma" {
		t.Fatalf("turn 3 = %#v, want another client turn ahead of the gamma batch", popped[3].triggers)
	}
	if len(popped[1].triggers)+len(popped[3].triggers) != 3 {
		t.Fatal("not every trigger event survived the drain")
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func TestTriggerBatchTextUsesOneStampAndBlankLineJoin(t *testing.T) {
	stamp := "2026-01-02 03:04 UTC"
	batch := formatTriggerBatch([]agentloop.TriggerEvent{
		{TriggerType: "timer", SourceID: "alpha", Payload: map[string]any{"task": "check inbox"}},
		{TriggerType: "webhook", SourceID: "beta"},
	}, stamp)

	want := "[2026-01-02 03:04 UTC]\n" +
		"[TRIGGER: timer/alpha]\nTask: check inbox\n{\"task\":\"check inbox\"}" +
		"\n\n" +
		"[TRIGGER: webhook/beta]\n{}"
	if batch != want {
		t.Fatalf("batch text mismatch\n got: %q\nwant: %q", batch, want)
	}
	if strings.Count(batch, "[2026-01-02 03:04 UTC]") != 1 {
		t.Fatalf("batch must carry exactly one stamp header:\n%s", batch)
	}
	if strings.Count(batch, "\n\n") != 1 {
		t.Fatalf("batch blocks must be joined by exactly one blank line:\n%s", batch)
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func TestSingleTriggerTurnKeepsPreBatchText(t *testing.T) {
	// The pre-batch runtime rendered one event per turn; a one-event drain is
	// the same bytes, so a single trigger's user message must not change.
	stamp := "2026-01-02 03:04 UTC"
	event := agentloop.TriggerEvent{
		TriggerType: "timer",
		SourceID:    "solo",
		Payload:     map[string]any{"task": "check inbox"},
	}
	want := "[2026-01-02 03:04 UTC]\n[TRIGGER: timer/solo]\nTask: check inbox\n{\"task\":\"check inbox\"}"
	if got := formatTriggerTurn(event, stamp); got != want {
		t.Fatalf("single-trigger text changed\n got: %q\nwant: %q", got, want)
	}
	if got := formatTriggerBatch([]agentloop.TriggerEvent{event}, stamp); got != want {
		t.Fatalf("one-event batch text differs from the per-event shape\n got: %q\nwant: %q", got, want)
	}
	// A payload with no task keeps the `{}` fallback on the payload line only.
	empty := agentloop.TriggerEvent{TriggerType: "webhook", SourceID: "hook"}
	if got, want := formatTriggerTurn(empty, stamp), "[2026-01-02 03:04 UTC]\n[TRIGGER: webhook/hook]\n{}"; got != want {
		t.Fatalf("empty-payload trigger text\n got: %q\nwant: %q", got, want)
	}
}

// triggerBatchGateLLM blocks the model call whose user message carries gateOn
// (or every call when gateOn is empty) until the test releases it, so queued
// triggers can accumulate behind a live turn.
type triggerBatchGateLLM struct {
	gateOn   string
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func newTriggerBatchGateLLM(gateOn string) *triggerBatchGateLLM {
	return &triggerBatchGateLLM{
		gateOn:  gateOn,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (g *triggerBatchGateLLM) ModelName() string { return "trigger-batch-gate-model" }

func (g *triggerBatchGateLLM) unblock() {
	select {
	case <-g.release:
	default:
		close(g.release)
	}
}

func (g *triggerBatchGateLLM) requestCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

// userTexts returns the user messages of every request seen so far, in order.
func (g *triggerBatchGateLLM) userTexts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var texts []string
	for _, request := range g.requests {
		for _, message := range request.Messages {
			if message.Role == "user" {
				texts = append(texts, message.Content)
			}
		}
	}
	return texts
}

func (g *triggerBatchGateLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	last := ""
	for _, message := range req.Messages {
		if message.Role == "user" {
			last = message.Content
		}
	}
	g.mu.Lock()
	g.requests = append(g.requests, req)
	g.mu.Unlock()

	if g.gateOn == "" || strings.Contains(last, g.gateOn) {
		g.once.Do(func() { close(g.started) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return llm.Completion{}, ctx.Err()
		}
	}
	return llm.Completion{
		Content:     "echo " + strings.TrimSpace(last),
		Model:       "trigger-batch-gate-model",
		StopReason:  "end_turn",
		InputTokens: 1,
	}, nil
}

func newTriggerBatchGateRuntime(t *testing.T, gateOn string) (*ChatRuntime, *triggerBatchGateLLM) {
	t.Helper()
	model := newTriggerBatchGateLLM(gateOn)
	runtime, err := NewChatRuntime(ChatRuntimeDeps{LLM: model, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(model.unblock)
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, model
}

func waitForGateStart(t *testing.T, model *triggerBatchGateLLM) {
	t.Helper()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the gated model call never started")
	}
}

// waitForTriggerBatch waits until exactly want trigger-tagged user parts are
// persisted and returns every transcript part.
func waitForTriggerBatch(t *testing.T, runtime *ChatRuntime, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var parts []map[string]any
	for time.Now().Before(deadline) {
		read, err := runtime.Transcript()
		if err != nil {
			t.Fatalf("Transcript: %v", err)
		}
		parts = read
		tagged := 0
		for _, part := range parts {
			if isTrigger, _ := part["is_trigger"].(bool); isTrigger {
				tagged++
			}
		}
		if tagged >= want {
			return parts
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d trigger message(s); transcript = %v", want, parts)
	return nil
}

func triggerPartsForTest(t *testing.T, parts []map[string]any) []map[string]any {
	t.Helper()
	var tagged []map[string]any
	for _, part := range parts {
		if isTrigger, _ := part["is_trigger"].(bool); isTrigger {
			tagged = append(tagged, part)
		}
	}
	return tagged
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func TestTriggerBatchLandsAsOneUserMessage(t *testing.T) {
	runtime, model := newTriggerBatchGateRuntime(t, "hold the turn")

	if err := runtime.Deliver("hold the turn"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForGateStart(t, model)

	// Both triggers queue while the worker is inside the client turn, so they
	// are pending together when the next turn is popped.
	alpha := agentloop.TriggerEvent{TriggerType: "timer", SourceID: "alpha", Payload: map[string]any{"task": "check inbox"}}
	beta := agentloop.TriggerEvent{TriggerType: "webhook", SourceID: "beta"}
	if err := runtime.InjectTrigger(alpha); err != nil {
		t.Fatalf("InjectTrigger alpha: %v", err)
	}
	if err := runtime.InjectTrigger(beta); err != nil {
		t.Fatalf("InjectTrigger beta: %v", err)
	}
	model.unblock()

	parts := waitForTriggerBatch(t, runtime, 1)
	tagged := triggerPartsForTest(t, parts)
	if len(tagged) != 1 {
		t.Fatalf("transcript carries %d trigger message(s), want exactly 1 (the batch): %v", len(tagged), parts)
	}
	content, _ := tagged[0]["content"].(string)
	header, rest, found := strings.Cut(content, "\n")
	if !found {
		t.Fatalf("batched message has no stamp header line:\n%q", content)
	}
	if !regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2} \S+\]$`).MatchString(header) {
		t.Fatalf("batched message header %q is not a [stamp] line", header)
	}
	wantBlocks := formatTriggerBlock(alpha) + "\n\n" + formatTriggerBlock(beta)
	if rest != wantBlocks {
		t.Fatalf("batched message blocks mismatch\n got: %q\nwant: %q", rest, wantBlocks)
	}
	if strings.Count(content, "[TRIGGER:") != 2 {
		t.Fatalf("batched message carries %d block(s), want 2:\n%s", strings.Count(content, "[TRIGGER:"), content)
	}
	if isClient, _ := tagged[0]["is_client_input"].(bool); isClient {
		t.Fatal("a trigger batch must not be tagged is_client_input")
	}

	// The model saw the batch as a single user message (never a trigger alone),
	// after the client turn.
	if !waitForModelRequests2(t, model, 2) {
		t.Fatalf("model saw %d request(s), want at least 2", model.requestCount())
	}
	merged := false
	for _, text := range model.userTexts() {
		if !strings.Contains(text, "[TRIGGER:") {
			continue
		}
		if strings.Count(text, "[TRIGGER:") != 2 ||
			!strings.Contains(text, "[TRIGGER: timer/alpha]") ||
			!strings.Contains(text, "[TRIGGER: webhook/beta]") {
			t.Fatalf("a model request carried a partial trigger batch:\n%s", text)
		}
		merged = true
	}
	if !merged {
		t.Fatalf("no request carried the merged batch: %q", model.userTexts())
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_injection_queue
// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func TestTriggerBatchWaitsBehindQueuedClientTurns(t *testing.T) {
	runtime, model := newTriggerBatchGateRuntime(t, "hold the turn")

	if err := runtime.Deliver("hold the turn"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForGateStart(t, model)

	trigger := agentloop.TriggerEvent{TriggerType: "timer", SourceID: "later"}
	if err := runtime.InjectTrigger(trigger); err != nil {
		t.Fatalf("InjectTrigger: %v", err)
	}
	// A client message queued after the trigger keeps its own turn and keeps
	// its place: the trigger batch is drained ahead of it, never merged in.
	if err := runtime.Deliver("second message"); err != nil {
		t.Fatalf("Deliver second: %v", err)
	}
	model.unblock()

	parts := waitForTriggerBatch(t, runtime, 1)
	waitForTranscriptContent(t, runtime, "second message", 5*time.Second)
	if read, err := runtime.Transcript(); err == nil {
		parts = read
	}
	if !waitForModelRequests2(t, model, 3) {
		t.Fatalf("model saw %d request(s), want 3 (client, trigger, client)", model.requestCount())
	}
	var ordered []string
	for _, part := range parts {
		if part["role"] != "user" {
			continue
		}
		content, _ := part["content"].(string)
		if strings.Contains(content, "hold the turn") || strings.Contains(content, "second message") ||
			strings.Contains(content, "[TRIGGER:") {
			ordered = append(ordered, content)
		}
	}
	if len(ordered) != 3 {
		t.Fatalf("user turns = %q, want the client, the trigger batch and the second client", ordered)
	}
	if !strings.Contains(ordered[0], "hold the turn") {
		t.Fatalf("turn 0 = %q, want the gated client message", ordered[0])
	}
	if !strings.Contains(ordered[1], "[TRIGGER: timer/later]") {
		t.Fatalf("turn 1 = %q, want the single-trigger batch before the second client message", ordered[1])
	}
	if !strings.Contains(ordered[2], "second message") {
		t.Fatalf("turn 2 = %q, want the second client message last", ordered[2])
	}
}

func waitForModelRequests2(t *testing.T, model *triggerBatchGateLLM, count int) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if model.requestCount() >= count {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

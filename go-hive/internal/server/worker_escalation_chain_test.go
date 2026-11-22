package server

// Production worker-escalation chain: a real colony worker trips the tool
// doom-loop detector, the session's colony-scoped routing records the question,
// the queen sees it, list_worker_questions reads the real inbox, and
// reply_to_worker either resumes the worker (still alive) or reports it as
// inactive (already failed fast).
//
// This is the production wiring the audit found missing, and it exercises the
// only worker escalation producer upstream keeps for parallel workers (the
// escalate tool is gated off worker:<id> streams).
//
// Parity: core/framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_escalation_requested)
// Parity: tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// doomLoopChainLLM scripts a worker into the deterministic doom-loop park:
// every call re-issues the same task_list. The detector escalates at the third
// call and the loop parks at the fifth, so the worker fails fast after telling
// the queen once.
type doomLoopChainLLM struct {
	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (l *doomLoopChainLLM) ModelName() string { return "doom-loop-chain" }

func (l *doomLoopChainLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	l.mu.Lock()
	l.requests = append(l.requests, req)
	l.mu.Unlock()
	return llm.Completion{
		StopReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID:        "repeat_task_list",
			Name:      "task_list",
			Arguments: `{}`,
		}},
	}, nil
}

func (l *doomLoopChainLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.requests)
}

// resumableDoomLoopLLM scripts the same detector trip, but blocks on its fourth
// call until the test releases it. That gives the test a stable window to reply
// while the worker is still alive; the injected guidance then arrives before
// the next model call and the worker reports success.
type resumableDoomLoopLLM struct {
	mu       sync.Mutex
	calls    int
	requests []llm.CompletionRequest
	gate     chan struct{}
}

func newResumableDoomLoopLLM() *resumableDoomLoopLLM {
	return &resumableDoomLoopLLM{gate: make(chan struct{})}
}

func (l *resumableDoomLoopLLM) ModelName() string { return "resumable-doom-loop" }

func (l *resumableDoomLoopLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	l.mu.Lock()
	l.calls++
	call := l.calls
	l.requests = append(l.requests, req)
	l.mu.Unlock()

	if messagesContainString(req.Messages, "[QUEEN_REPLY]") {
		return llm.Completion{
			StopReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{
				ID:        "report_after_reply",
				Name:      "report_to_parent",
				Arguments: `{"status":"success","summary":"Recovered with the queen's guidance.","data":{}}`,
			}},
		}, nil
	}
	if call <= 3 {
		return llm.Completion{
			StopReason: "tool_calls",
			ToolCalls:  []llm.ToolCall{{ID: "repeat_task_list", Name: "task_list", Arguments: `{}`}},
		}, nil
	}
	select {
	case <-l.gate:
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
	}
	return llm.Completion{
		StopReason: "tool_calls",
		ToolCalls:  []llm.ToolCall{{ID: "after_release", Name: "task_list", Arguments: `{"status":"pending"}`}},
	}, nil
}

func (l *resumableDoomLoopLLM) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.gate:
	default:
		close(l.gate)
	}
}

func (l *resumableDoomLoopLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

func (l *resumableDoomLoopLLM) messagesContain(needle string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, req := range l.requests {
		if messagesContainString(req.Messages, needle) {
			return true
		}
	}
	return false
}

func messagesContainString(messages []llm.ChatMessage, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

func newDoomLoopSession(t *testing.T, workerLLM llm.Completer) (*Session, *colony.ColonyRuntime, *eventbus.EventBus, *fakeQueen) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   agentloop.NewAgentSpec("worker", "Worker", "a colony worker"),
		StoragePath: t.TempDir(),
		LLM:         workerLLM,
		Tools:       []llm.ToolDef{},
		EventBus:    bus,
		StreamID:    "colony_escalation",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(runtime.Stop)

	session := NewSession("session_escalation", eventbus.NewEventBus())
	session.Colony = runtime
	queen := attachQueen(session)
	session.BindChatColony()
	if session.WorkerHandoffSub == "" {
		t.Fatal("BindChatColony did not install the worker escalation routing")
	}
	return session, runtime, bus, queen
}

func spawnDoomLoopWorker(t *testing.T, runtime *colony.ColonyRuntime) string {
	t.Helper()
	ids, err := runtime.SpawnBatchWithOptions(context.Background(),
		[]colony.BatchTask{{Task: "query the ledger"}},
		colony.SpawnBatchOptions{LoopConfigOverrides: map[string]any{"max_iterations": 10}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return ids[0]
}

func waitForQueenHandoff(t *testing.T, queen *fakeQueen) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for queen.callCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("queen never received the [WORKER_ESCALATION] handoff")
		}
		time.Sleep(10 * time.Millisecond)
	}
	injected, isClientInput := queen.lastCall(t)
	if isClientInput {
		t.Fatal("escalation handoff was injected as client input")
	}
	if !strings.Contains(injected, "[WORKER_ESCALATION]") || !strings.Contains(injected, "reason: Tool doom loop detected") {
		t.Fatalf("handoff does not carry the doom-loop escalation:\n%s", injected)
	}
	return injected
}

func listPendingRequestID(t *testing.T, binding *QueenLifecycleBinding) string {
	t.Helper()
	content, isError := binding.Execute(context.Background(), QueenListWorkerQuestionsToolName, nil)
	if isError {
		t.Fatalf("list_worker_questions returned an error: %s", content)
	}
	var listed struct {
		Count   int `json:"count"`
		Pending []struct {
			RequestID string `json:"request_id"`
			Reason    string `json:"reason"`
		} `json:"pending"`
	}
	if err := json.Unmarshal([]byte(content), &listed); err != nil {
		t.Fatalf("list_worker_questions payload %s: %v", content, err)
	}
	if listed.Count != 1 || len(listed.Pending) != 1 {
		t.Fatalf("pending questions = %s, want exactly one", content)
	}
	if listed.Pending[0].Reason != "Tool doom loop detected" {
		t.Fatalf("pending reason = %q, want the doom-loop reason", listed.Pending[0].Reason)
	}
	return listed.Pending[0].RequestID
}

// Parity: core/framework/agent_loop/agent_loop.py::_execute_impl (the doom loop
// emits exactly one escalation; the deterministic park then fails the worker
// fast, and a late reply is reported as worker_not_active).
func TestProductionDoomLoopEscalationReachesQueenInbox(t *testing.T) {
	workerLLM := &doomLoopChainLLM{}
	session, runtime, _, queen := newDoomLoopSession(t, workerLLM)
	binding := NewQueenLifecycleBinding(session, hivehome.Home{Root: t.TempDir()})

	workerID := spawnDoomLoopWorker(t, runtime)

	injected := waitForQueenHandoff(t, queen)
	if !strings.Contains(injected, "worker_id: "+workerID) {
		t.Fatalf("handoff does not name the worker %s:\n%s", workerID, injected)
	}
	requestID := listPendingRequestID(t, binding)
	if !strings.Contains(injected, "request_id: "+requestID) {
		t.Fatalf("handoff request_id does not match the inbox entry:\n%s", injected)
	}

	// The worker repeated past the park threshold and failed fast; the question
	// stays addressable so the queen can act (re-dispatch / take over).
	worker := runtime.GetWorker(workerID)
	if !eventuallyFinished(worker, 5*time.Second) {
		t.Fatal("worker did not terminate on the doom-loop park")
	}
	if worker.Status() != colony.WorkerFailed {
		t.Fatalf("worker status = %q, want failed (fail-fast, no queen wait)", worker.Status())
	}
	if got := workerLLM.callCount(); got != 5 {
		t.Fatalf("worker model calls = %d, want 5 (escalate at 3, park at 5)", got)
	}

	replyContent, replyError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{
		"request_id": requestID,
		"reply":      "Abandon the repeated query and report your blocker.",
	})
	if replyError {
		t.Fatalf("reply_to_worker returned an error: %s", replyContent)
	}
	var reply struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(replyContent), &reply); err != nil || reply.Status != "worker_not_active" {
		t.Fatalf("reply_to_worker payload = %s (err=%v), want worker_not_active", replyContent, err)
	}
	afterContent, _ := binding.Execute(context.Background(), QueenListWorkerQuestionsToolName, nil)
	if !strings.Contains(afterContent, `"count":0`) {
		t.Fatalf("inbox after reply = %s, want empty", afterContent)
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_execute_impl (the doom-loop
// escalation is an intervention channel: a reply delivered while the worker is
// still alive resumes it).
func TestProductionDoomLoopGuidanceResumesTheWorker(t *testing.T) {
	workerLLM := newResumableDoomLoopLLM()
	session, runtime, _, queen := newDoomLoopSession(t, workerLLM)
	binding := NewQueenLifecycleBinding(session, hivehome.Home{Root: t.TempDir()})

	workerID := spawnDoomLoopWorker(t, runtime)

	injected := waitForQueenHandoff(t, queen)
	requestID := listPendingRequestID(t, binding)
	if !strings.Contains(injected, "request_id: "+requestID) {
		t.Fatalf("handoff request_id does not match the inbox entry:\n%s", injected)
	}
	worker := runtime.GetWorker(workerID)
	if !worker.IsActive() {
		t.Fatal("worker stopped before the queen could answer")
	}

	replyContent, replyError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{
		"request_id": requestID,
		"reply":      "Try a different query.",
	})
	if replyError {
		t.Fatalf("reply_to_worker returned an error: %s", replyContent)
	}
	var reply struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(replyContent), &reply); err != nil || reply.Status != "delivered" {
		t.Fatalf("reply_to_worker payload = %s (err=%v), want delivered", replyContent, err)
	}

	workerLLM.release()
	if !eventuallyFinished(worker, 5*time.Second) {
		t.Fatal("worker did not terminate after the queen's reply")
	}
	report := worker.ExplicitReport()
	if report == nil || report["status"] != "success" {
		t.Fatalf("worker report = %#v, want the resumed success report", report)
	}
	if !workerLLM.messagesContain("[QUEEN_REPLY] request_id=" + requestID) {
		t.Fatal("worker never saw the queen's reply in its conversation")
	}
	if got := workerLLM.callCount(); got != 5 {
		t.Fatalf("worker model calls = %d, want 5 (3 repeats, 1 gated, 1 resumed)", got)
	}
}

// Parity: core/tests/test_session_manager_worker_handoff.py::test_stop_session_unsubscribes_worker_handoff
func TestProductionEscalationRoutingDropsWithTheColony(t *testing.T) {
	workerLLM := &doomLoopChainLLM{}
	session, _, bus, queen := newDoomLoopSession(t, workerLLM)

	// A probe proves the event still reaches the colony bus after the session
	// unloads: the routing removal, not a dead bus, is what keeps the queen
	// quiet.
	probe := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventEscalationRequested}, func(event *eventbus.AgentEvent) {
		select {
		case probe <- event:
		default:
		}
	})

	StopColonyRuntime(session.Colony)
	session.Colony = nil
	session.BindChatColony()
	if session.WorkerHandoffSub != "" {
		t.Fatalf("WorkerHandoffSub = %q after the colony was unloaded, want empty", session.WorkerHandoffSub)
	}

	before := queen.callCount()
	colonyID := "colony_escalation"
	event := eventbus.NewAgentEvent(eventbus.EventEscalationRequested, "worker:late")
	event.NodeID = eventbus.StrPtr("worker:late")
	event.ColonyID = &colonyID
	event.Data = map[string]any{"request_id": "req-late", "reason": "late", "context": ""}
	bus.Publish(event)

	select {
	case <-probe:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never saw the escalation on the colony bus")
	}
	time.Sleep(100 * time.Millisecond)
	if got := queen.callCount(); got != before {
		t.Fatalf("queen received %d handoffs after the colony was unloaded, want %d", got, before)
	}
	if _, present := session.PendingEscalations["req-late"]; present {
		t.Fatal("a detached escalation still landed in the session inbox")
	}
}

func eventuallyFinished(worker *colony.Worker, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if worker != nil && !worker.IsActive() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return worker != nil && !worker.IsActive()
}

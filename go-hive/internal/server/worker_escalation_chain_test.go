package server

// Production worker-escalation chain: a real colony worker escalates, the
// session's colony-scoped routing records the question, list_worker_questions
// reads it, reply_to_worker hands the guidance back, and the worker resumes and
// reports. This is the production wiring the audit found missing (the tools and
// the handler existed, but nothing installed the handler or produced the event).
//
// Parity: tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// escalationChainLLM scripts the worker: escalate once, then report after the
// queen's guidance arrives.
type escalationChainLLM struct {
	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (l *escalationChainLLM) ModelName() string { return "escalation-chain" }

func (l *escalationChainLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	l.mu.Lock()
	l.requests = append(l.requests, req)
	index := len(l.requests) - 1
	l.mu.Unlock()
	if index == 0 {
		return llm.Completion{
			Model:      "escalation-chain",
			StopReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{
				ID:        "escalate_1",
				Name:      "escalate",
				Arguments: `{"reason":"Credential wall","context":"DATABASE_URL is unset"}`,
			}},
		}, nil
	}
	return llm.Completion{
		Model:      "escalation-chain",
		StopReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID:        "report_1",
			Name:      "report_to_parent",
			Arguments: `{"status":"success","summary":"Finished with the staging database.","data":{}}`,
		}},
	}, nil
}

func (l *escalationChainLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.requests)
}

func (l *escalationChainLLM) messagesContain(needle string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, req := range l.requests {
		for _, message := range req.Messages {
			if strings.Contains(message.Content, needle) {
				return true
			}
		}
	}
	return false
}

func newEscalationChainSession(t *testing.T, workerLLM llm.Completer) (*Session, *colony.ColonyRuntime, *eventbus.EventBus, *fakeQueen) {
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

// Parity: tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen
func TestProductionWorkerEscalationReachesQueenInboxAndResumes(t *testing.T) {
	workerLLM := &escalationChainLLM{}
	session, runtime, _, queen := newEscalationChainSession(t, workerLLM)
	binding := NewQueenLifecycleBinding(session, hivehome.Home{Root: t.TempDir()})

	ids, err := runtime.Spawn(context.Background(), "query the ledger", colony.SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	workerID := ids[0]

	// 1. The worker's escalate call reaches the queen as an addressed handoff.
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
	for _, want := range []string{"[WORKER_ESCALATION]", "worker_id: " + workerID, "reason: Credential wall", "DATABASE_URL is unset"} {
		if !strings.Contains(injected, want) {
			t.Fatalf("handoff missing %q:\n%s", want, injected)
		}
	}

	// 2. list_worker_questions reads the real inbox (not a half in memory).
	content, isError := binding.Execute(context.Background(), QueenListWorkerQuestionsToolName, nil)
	if isError {
		t.Fatalf("list_worker_questions returned an error: %s", content)
	}
	var listed struct {
		Count   int `json:"count"`
		Pending []struct {
			RequestID string `json:"request_id"`
			WorkerID  string `json:"worker_id"`
			Reason    string `json:"reason"`
		} `json:"pending"`
	}
	if err := json.Unmarshal([]byte(content), &listed); err != nil {
		t.Fatalf("list_worker_questions payload %s: %v", content, err)
	}
	if listed.Count != 1 || len(listed.Pending) != 1 {
		t.Fatalf("pending questions = %s, want one", content)
	}
	if listed.Pending[0].WorkerID != workerID || listed.Pending[0].Reason != "Credential wall" {
		t.Fatalf("pending entry = %#v, want the worker's question", listed.Pending[0])
	}
	requestID := listed.Pending[0].RequestID
	if requestID == "" {
		t.Fatal("pending entry has no request_id")
	}
	if !strings.Contains(injected, "request_id: "+requestID) {
		t.Fatalf("handoff request_id does not match the inbox entry:\n%s", injected)
	}

	// 3. reply_to_worker delivers the guidance and drains the inbox.
	replyContent, replyError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{
		"request_id": requestID,
		"reply":      "Use the staging database.",
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
	afterContent, _ := binding.Execute(context.Background(), QueenListWorkerQuestionsToolName, nil)
	if !strings.Contains(afterContent, `"count":0`) {
		t.Fatalf("inbox after reply = %s, want empty", afterContent)
	}

	// 4. The worker received the reply and continued to its terminal report.
	deadline = time.Now().Add(5 * time.Second)
	for {
		worker := runtime.GetWorker(workerID)
		if worker != nil && !worker.IsActive() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker stayed parked after the queen's reply")
		}
		time.Sleep(10 * time.Millisecond)
	}
	worker := runtime.GetWorker(workerID)
	report := worker.ExplicitReport()
	if report == nil || report["status"] != "success" {
		t.Fatalf("worker report = %#v, want the resumed success report", report)
	}
	if !workerLLM.messagesContain("[QUEEN_REPLY] request_id=" + requestID) {
		t.Fatal("worker never saw the queen's reply in its conversation")
	}
	if got := workerLLM.callCount(); got != 2 {
		t.Fatalf("worker model calls = %d, want 2 (escalate, then resume)", got)
	}
}

// Parity: core/tests/test_session_manager_worker_handoff.py::test_stop_session_unsubscribes_worker_handoff
func TestProductionEscalationRoutingDropsWithTheColony(t *testing.T) {
	workerLLM := &escalationChainLLM{}
	session, _, bus, queen := newEscalationChainSession(t, workerLLM)

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

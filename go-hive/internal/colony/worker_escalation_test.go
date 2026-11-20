package colony

// Worker escalation channel, colony half.
//
// Parallel workers have no escalate tool (upstream gates it to the legacy
// `worker` stream): they report_to_parent and fail fast. The one worker
// escalation producer upstream leaves open is the automatic tool doom-loop
// detector, which publishes ESCALATION_REQUESTED with the colony id and a fresh
// request_id; injected guidance resumes the worker while it is still alive.
//
// Parity: core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report
// Parity: core/framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_escalation_requested)

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (tool surface gate:
// escalate is not offered to parallel workers; report_to_parent is).
func TestWorkerToolsExcludeEscalate(t *testing.T) {
	provider := newScriptedLLM(textResponse("done"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w1", Task: "investigate"})
	names := toolNames(runtime.workerTools(worker))
	if hasTool(names, "escalate") {
		t.Fatalf("worker tools = %v, want no escalate (parallel workers fail fast)", names)
	}
	if !hasTool(names, "report_to_parent") {
		t.Fatalf("worker tools = %v, want report_to_parent", names)
	}

	overseer := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "overseer:test_colony", Task: ""})
	overseer.Persistent = true
	overseerNames := toolNames(runtime.workerTools(overseer))
	if hasTool(overseerNames, "escalate") || hasTool(overseerNames, "report_to_parent") {
		t.Fatalf("overseer tools = %v, want neither escalate nor report_to_parent", overseerNames)
	}
}

// Parity-exempt: upstream drives the emit through the AgentLoop's event bus; this
// asserts the port's colony half directly (colony stamp + request_id shape).
func TestWorkerEscalationEventCarriesColonyAndRequestID(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, newScriptedLLM(textResponse("done")), bus)

	received := make(chan *eventbus.AgentEvent, 1)
	bus.Subscribe([]eventbus.EventType{eventbus.EventEscalationRequested}, func(event *eventbus.AgentEvent) {
		received <- event
	})

	requestID, err := runtime.requestWorkerEscalation(agentEscalationRequest())
	if err != nil {
		t.Fatalf("requestWorkerEscalation: %v", err)
	}
	if requestID == "" {
		t.Fatal("requestWorkerEscalation returned no request_id")
	}

	select {
	case event := <-received:
		if event.StreamID != "worker:w1" {
			t.Fatalf("stream_id = %q, want worker:w1", event.StreamID)
		}
		if event.ColonyID == nil || *event.ColonyID != "test_colony" {
			t.Fatalf("colony_id = %v, want test_colony (colony-scoped subscription depends on it)", event.ColonyID)
		}
		if event.Data["request_id"] != requestID {
			t.Fatalf("event request_id = %v, want %q", event.Data["request_id"], requestID)
		}
		if event.Data["reason"] != "Credential wall" || event.Data["context"] != "HTTP 401" {
			t.Fatalf("event payload = %#v, want the tool's reason/context", event.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ESCALATION_REQUESTED event on the colony bus")
	}
}

// doomLoopWorkerLLM scripts one worker through a deterministic doom-loop
// escalation: three identical calls trip the detector, then the fourth call
// blocks until the test releases it. Blocking there gives the test a stable
// window to inject the queen's guidance while the worker is still alive (the
// deterministic park at five repeats would otherwise race the reply).
type doomLoopWorkerLLM struct {
	mu          sync.Mutex
	calls       int
	requests    []llm.CompletionRequest
	releaseGate chan struct{}
}

func newDoomLoopWorkerLLM() *doomLoopWorkerLLM {
	return &doomLoopWorkerLLM{releaseGate: make(chan struct{})}
}

func (m *doomLoopWorkerLLM) ModelName() string { return "doom-loop-worker" }

func (m *doomLoopWorkerLLM) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.requests = append(m.requests, req)
	m.mu.Unlock()

	if messagesContain(req.Messages, "[QUEEN_REPLY]") {
		return toolCompletion("report_after_reply", "report_to_parent",
			`{"status":"success","summary":"Recovered with the queen's guidance.","data":{}}`), nil
	}
	if call <= 3 {
		// Identical tool name and arguments: the deterministic detector trips
		// on the third call, exactly where upstream escalates.
		return toolCompletion("repeat_"+strconv.Itoa(call), "task_list", `{}`), nil
	}
	select {
	case <-m.releaseGate:
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
	}
	// A different call breaks the streak; the injected reply drains into the
	// conversation before the next model call.
	return toolCompletion("after_release", "task_list", `{"status":"pending"}`), nil
}

func (m *doomLoopWorkerLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *doomLoopWorkerLLM) lastRequest() llm.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return llm.CompletionRequest{}
	}
	return m.requests[len(m.requests)-1]
}

func (m *doomLoopWorkerLLM) released() bool {
	select {
	case <-m.releaseGate:
		return true
	default:
		return false
	}
}

func (m *doomLoopWorkerLLM) release() {
	if !m.released() {
		close(m.releaseGate)
	}
}

func toolCompletion(id, name, arguments string) llm.Completion {
	return llm.Completion{
		ToolCalls:  []llm.ToolCall{{ID: id, Name: name, Arguments: arguments}},
		StopReason: "tool_calls",
	}
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop emits ESCALATION_REQUESTED once; the worker keeps running and an
// injected reply resumes it).
func TestWorkerDoomLoopEscalatesOnceAndResumesWithGuidance(t *testing.T) {
	provider := newDoomLoopWorkerLLM()
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)

	escalations := collectEvents(bus, eventbus.EventEscalationRequested)

	// The default worker profile is 3 iterations + 1 grace: too short to trip
	// the detector (3 calls) and then resume on a later call. Raise the budget
	// for this scripted worker so the resume is observable.
	ids, err := runtime.SpawnBatchWithOptions(context.Background(),
		[]BatchTask{{Task: "query the ledger"}},
		SpawnBatchOptions{LoopConfigOverrides: map[string]any{"max_iterations": 10}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	workerID := ids[0]
	worker := runtime.GetWorker(workerID)
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}

	// The escalation fires at the third identical call, while the worker is
	// still alive; the fourth call then blocks on the test's gate.
	if !eventually(t, 5*time.Second, func() bool { return escalations.count() > 0 }) {
		t.Fatalf("worker never escalated; model calls = %d", provider.callCount())
	}
	if got := escalations.count(); got != 1 {
		t.Fatalf("escalations = %d, want exactly 1 for the streak", got)
	}
	event := escalations.snapshot()[0]
	if event.ColonyID == nil || *event.ColonyID != "test_colony" {
		t.Fatalf("escalation colony_id = %v, want test_colony", event.ColonyID)
	}
	if event.Data["reason"] != "Tool doom loop detected" {
		t.Fatalf("escalation reason = %v, want the upstream doom-loop reason", event.Data["reason"])
	}
	if !strings.Contains(stringValue(event.Data["context"]), "task_list") {
		t.Fatalf("escalation context = %v, want the repeating call named", event.Data["context"])
	}
	requestID, _ := event.Data["request_id"].(string)
	if requestID == "" {
		t.Fatal("escalation event carries no request_id")
	}
	if !worker.IsActive() {
		t.Fatal("worker stopped instead of continuing after the doom-loop escalation")
	}

	// Still one event after the worker replays several more turns.
	if err := runtime.InjectInput(workerID, "[QUEEN_REPLY] request_id="+requestID+"\nTry a different query."); err != nil {
		t.Fatalf("InjectInput: %v", err)
	}
	provider.release()

	if !eventually(t, 5*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatal("worker did not terminate after the queen's reply")
	}
	report := worker.ExplicitReport()
	if report == nil || report["status"] != "success" {
		t.Fatalf("worker report = %#v (status=%v, summary=%q), want the resumed success report",
			report, worker.Status(), worker.Result().Summary)
	}
	last := provider.lastRequest()
	if !messagesContain(last.Messages, "[QUEEN_REPLY] request_id="+requestID) {
		t.Fatalf("queen reply missing from the resumed worker call: %#v", last.Messages)
	}
	if got := escalations.count(); got != 1 {
		t.Fatalf("escalations after resume = %d, want still 1 (no storm)", got)
	}
}

// Parity-exempt: the negative half — a worker whose calls keep changing has no
// doom loop and must publish nothing.
func TestWorkerWithoutRepeatedCallsPublishesNoEscalation(t *testing.T) {
	provider := newScriptedLLM(
		scriptedResponse{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "task_list", Arguments: `{}`}}, StopReason: "tool_calls"},
		scriptedResponse{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "task_list", Arguments: `{"status":"pending"}`}}, StopReason: "tool_calls"},
		scriptedResponse{ToolCalls: []llm.ToolCall{{ID: "c3", Name: "task_list", Arguments: `{"status":"done"}`}}, StopReason: "tool_calls"},
		reportResponse("success", "Varied work, nothing repeated.", nil),
	)
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)
	escalations := collectEvents(bus, eventbus.EventEscalationRequested)

	ids, err := runtime.Spawn(context.Background(), "varied work", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatal("worker did not terminate")
	}
	if got := escalations.count(); got != 0 {
		t.Fatalf("escalations = %d, want none without a repeated call: %#v", got, escalations.snapshot())
	}
	if report := worker.ExplicitReport(); report == nil || report["status"] != "success" {
		t.Fatalf("worker report = %#v, want the scripted success report", report)
	}
}

func agentEscalationRequest() agentloop.EscalationRequest {
	return agentloop.EscalationRequest{
		StreamID:    "worker:w1",
		NodeID:      "worker:w1",
		ExecutionID: "exec_w1",
		Reason:      "Credential wall",
		Context:     "HTTP 401",
	}
}

func messagesContain(messages []llm.ChatMessage, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

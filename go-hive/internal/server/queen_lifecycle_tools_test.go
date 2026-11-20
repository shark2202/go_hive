package server

// Tests for the session-bound queen lifecycle tools: tool schemas, worker
// monitoring/steering/stopping, and the escalation inbox loop.
//
// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
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
	"github.com/aden-hive/hive-go/internal/schemas"
)

// lifecycleTestSession builds one queen chat session plus its lifecycle table.
func lifecycleTestSession(t *testing.T) (*Session, *QueenLifecycleBinding, hivehome.Home) {
	t.Helper()
	session := NewSession("session_lifecycle", eventbus.NewEventBus())
	home := hivehome.Home{Root: t.TempDir()}
	return session, NewQueenLifecycleBinding(session, home), home
}

// lifecycleGateLLM blocks every call until the gate opens (or the worker is
// cancelled), so the test can hold a worker in the running state.
type lifecycleGateLLM struct {
	gate chan struct{}
	mu   sync.Mutex
	uses int
}

func newLifecycleGateLLM() *lifecycleGateLLM {
	return &lifecycleGateLLM{gate: make(chan struct{})}
}

func (g *lifecycleGateLLM) release() {
	select {
	case <-g.gate:
	default:
		close(g.gate)
	}
}

func (g *lifecycleGateLLM) AComplete(ctx context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	g.mu.Lock()
	g.uses++
	g.mu.Unlock()
	select {
	case <-g.gate:
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
	}
	return llm.Completion{Content: "done", StopReason: "stop"}, nil
}

// lifecycleTestColony starts a real colony runtime the lifecycle tools can
// drive. Workers are spawned on demand and held by the gate LLM.
func lifecycleTestColony(t *testing.T, bus *eventbus.EventBus, provider llm.Completer) *colony.ColonyRuntime {
	t.Helper()
	spec := agentloop.NewAgentSpec("lifecycle_agent", "Lifecycle Agent", "colony runtime for lifecycle tool tests")
	systemPrompt := "You are a test worker."
	spec.SystemPrompt = &systemPrompt
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:   spec,
		Goal:        &schemas.Goal{ID: "goal-1", Name: "Lifecycle goal"},
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus:             bus,
		StreamID:             "colony_lifecycle",
		MaxConcurrentWorkers: 4,
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("colony Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// lifecycleClock is a controllable monotonic+wall clock so the status tiers'
// cooldowns can be exercised without sleeping.
type lifecycleClock struct {
	mu   sync.Mutex
	mono float64
	wall float64
}

func newLifecycleClock() *lifecycleClock {
	return &lifecycleClock{mono: 1000, wall: 1700000000}
}

func (c *lifecycleClock) NowMonotonic() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

func (c *lifecycleClock) NowWall() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *lifecycleClock) advance(seconds float64) {
	c.mu.Lock()
	c.mono += seconds
	c.wall += seconds
	c.mu.Unlock()
}

// spawnGatedWorker spawns one worker and waits until it is running with its
// agent loop armed (status Running alone precedes loop construction).
func spawnGatedWorker(t *testing.T, runtime *colony.ColonyRuntime, task string) string {
	t.Helper()
	ids, err := runtime.SpawnBatchWithOptions(context.Background(), []colony.BatchTask{{Task: task}}, colony.SpawnBatchOptions{BatchID: "batch_lifecycle"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("spawned %d workers, want 1", len(ids))
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		worker := runtime.GetWorker(ids[0])
		if worker != nil && worker.Status() == colony.WorkerRunning && worker.AgentLoop() != nil {
			return ids[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("worker %s never reached running with an armed loop", ids[0])
	return ""
}

func decodeToolPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("tool payload is not JSON: %v\n%s", err, content)
	}
	return payload
}

// Parity: tests/test_session_manager_worker_handoff.py::test_worker_handoff_ignores_queen_stream
func TestEmptyInboxAndTriggerList(t *testing.T) {
	session, binding, _ := lifecycleTestSession(t)

	// A fresh session has an empty (non-nil) inbox.
	list := decodeToolPayload(t, binding.listWorkerQuestions())
	if list["count"] != json.Number("0") {
		t.Fatalf("fresh inbox count = %v", list["count"])
	}
	if pending := list["pending"].([]any); len(pending) != 0 {
		t.Fatalf("fresh inbox pending = %#v", pending)
	}

	// A session whose inbox map is nil must still answer an empty list rather
	// than panicking.
	session.PendingEscalations = nil
	if list = decodeToolPayload(t, binding.listWorkerQuestions()); list["count"] != json.Number("0") {
		t.Fatalf("nil inbox count = %v", list["count"])
	}

	// No triggers configured: an empty array, never null.
	triggersList := decodeToolPayload(t, binding.listTriggers())
	rows, ok := triggersList["triggers"].([]any)
	if !ok || len(rows) != 0 {
		t.Fatalf("empty trigger list = %#v", triggersList)
	}
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestQueenLifecycleToolDefs(t *testing.T) {
	defs := QueenLifecycleToolDefs()
	byName := map[string]llm.ToolDef{}
	for _, def := range defs {
		byName[def.Name] = def
	}
	for _, name := range QueenLifecycleToolNames() {
		if _, ok := byName[name]; !ok {
			t.Fatalf("definition missing for %q", name)
		}
	}
	required := map[string][]string{
		QueenStopWorkerToolName:          {},
		QueenGetWorkerStatusToolName:     {},
		QueenInjectMessageToolName:       {"content"},
		QueenListWorkerQuestionsToolName: {},
		QueenReplyToWorkerToolName:       {"request_id", "reply"},
		QueenSetTriggerToolName:          {"trigger_id"},
		QueenRemoveTriggerToolName:       {"trigger_id"},
		QueenListTriggersToolName:        {},
		QueenUpdateWorkerProfileToolName: {"colony_id", "profile_name"},
		QueenListBrowserProfilesToolName: {},
	}
	for name, fields := range required {
		def, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing", name)
		}
		parameters := def.Parameters
		if parameters["type"] != "object" {
			t.Fatalf("%s parameters.type = %v, want object", name, parameters["type"])
		}
		declared, _ := parameters["required"].([]any)
		if len(declared) != len(fields) {
			t.Fatalf("%s required = %v, want %v", name, declared, fields)
		}
	}
	focus := byName[QueenGetWorkerStatusToolName].Parameters["properties"].(map[string]any)["focus"].(map[string]any)
	if got := len(focus["enum"].([]any)); got != 6 {
		t.Fatalf("get_worker_status focus enum length = %d, want 6", got)
	}
	if !colonyBoundQueenTool(QueenStopWorkerToolName) || !colonyBoundQueenTool(QueenGetWorkerStatusToolName) || !colonyBoundQueenTool(QueenInjectMessageToolName) {
		t.Fatal("colony-runtime lifecycle tools must be colony-gated")
	}
	if colonyBoundQueenTool(QueenListTriggersToolName) {
		t.Fatal("list_triggers must stay advertised without a colony")
	}
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestGetWorkerStatusNoColony(t *testing.T) {
	_, binding, _ := lifecycleTestSession(t)
	if got := binding.getWorkerStatus(map[string]any{}); got != "No colony running." {
		t.Fatalf("status = %q, want the no-colony message", got)
	}
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestGetWorkerStatusFoci(t *testing.T) {
	bus := eventbus.NewEventBus()
	provider := newLifecycleGateLLM()
	runtime := lifecycleTestColony(t, bus, provider)
	session, binding, _ := lifecycleTestSession(t)
	clock := newLifecycleClock()
	session.TriggerClock = clock
	session.Colony = colonyHostStub{runtime: runtime}
	session.EventBus = bus

	if got := binding.getWorkerStatus(map[string]any{}); got != "No workers loaded." {
		t.Fatalf("empty colony status = %q, want No workers loaded.", got)
	}

	workerID := spawnGatedWorker(t, runtime, "inspect the ledger")

	summary := binding.getWorkerStatus(map[string]any{})
	if !strings.Contains(summary, "Worker is running") {
		t.Fatalf("summary = %q, want a running-worker sentence", summary)
	}
	if !strings.Contains(summary, "No issues detected") {
		t.Fatalf("summary = %q, want the no-issues note", summary)
	}

	activity := binding.getWorkerStatus(map[string]any{"focus": "activity"})
	if !strings.Contains(activity, workerID) || !strings.Contains(activity, "running") {
		t.Fatalf("activity = %q, want the worker row", activity)
	}
	// The detail tier's cooldown answers an immediate second detail call.
	cooldown := decodeToolPayload(t, binding.getWorkerStatus(map[string]any{"focus": "tools"}))
	if cooldown["status"] != "cooldown" {
		t.Fatalf("second detail call = %v, want cooldown", cooldown)
	}
	clock.advance(11)

	memory := binding.getWorkerStatus(map[string]any{"focus": "memory"})
	if !strings.Contains(memory, "tracker_query") {
		t.Fatalf("memory = %q, want the honest no-shared-buffer note", memory)
	}
	clock.advance(11)

	tools := binding.getWorkerStatus(map[string]any{"focus": "tools"})
	if !strings.Contains(tools, "No tools currently running.") {
		t.Fatalf("tools = %q", tools)
	}
	clock.advance(11)

	issues := binding.getWorkerStatus(map[string]any{"focus": "issues"})
	if !strings.Contains(issues, "No issues detected") {
		t.Fatalf("issues = %q", issues)
	}
	clock.advance(11)

	progress := binding.getWorkerStatus(map[string]any{"focus": "progress"})
	if !strings.Contains(progress, "Lifecycle goal") {
		t.Fatalf("progress = %q, want the goal name", progress)
	}

	full := decodeToolPayload(t, binding.getWorkerStatus(map[string]any{"focus": "full"}))
	if full["status"] != "running" {
		t.Fatalf("full status = %v, want running", full["status"])
	}
	if full["worker_colony_id"] != "colony_lifecycle" {
		t.Fatalf("full worker_colony_id = %v", full["worker_colony_id"])
	}

	// The full tier's 30s cooldown answers the immediate second call.
	cooldown = decodeToolPayload(t, binding.getWorkerStatus(map[string]any{"focus": "full"}))
	if cooldown["status"] != "cooldown" {
		t.Fatalf("second full call = %v, want cooldown", cooldown)
	}

	if got := binding.getWorkerStatus(map[string]any{"focus": "bogus"}); !strings.Contains(got, "Unknown focus 'bogus'") {
		clock.advance(11)
		if got = binding.getWorkerStatus(map[string]any{"focus": "bogus"}); !strings.Contains(got, "Unknown focus 'bogus'") {
			t.Fatalf("unknown focus = %q", got)
		}
	}

	provider.release()
	runtime.Stop()
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestInjectMessage(t *testing.T) {
	provider := newLifecycleGateLLM()
	runtime := lifecycleTestColony(t, eventbus.NewEventBus(), provider)
	session, binding, _ := lifecycleTestSession(t)

	if content, isError := binding.Execute(context.Background(), QueenInjectMessageToolName, map[string]any{"content": "hi"}); !isError || !strings.Contains(content, "No colony running in this session.") {
		t.Fatalf("no-colony inject = (%v, %q)", isError, content)
	}

	session.Colony = colonyHostStub{runtime: runtime}
	if content, isError := binding.Execute(context.Background(), QueenInjectMessageToolName, map[string]any{"content": "hi"}); !isError || !strings.Contains(content, "No active graph node found") {
		t.Fatalf("idle inject = (%v, %q)", isError, content)
	}

	workerID := spawnGatedWorker(t, runtime, "hold")
	content, isError := binding.Execute(context.Background(), QueenInjectMessageToolName, map[string]any{"content": "use the second alias"})
	if isError {
		t.Fatalf("inject failed: %s", content)
	}
	payload := decodeToolPayload(t, content)
	if payload["status"] != "delivered" || payload["node_id"] != workerID {
		t.Fatalf("inject payload = %#v", payload)
	}
	if payload["content_preview"] != "use the second alias" {
		t.Fatalf("preview = %v", payload["content_preview"])
	}

	provider.release()
	runtime.Stop()
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestStopWorker(t *testing.T) {
	provider := newLifecycleGateLLM()
	runtime := lifecycleTestColony(t, eventbus.NewEventBus(), provider)
	session, binding, _ := lifecycleTestSession(t)

	if content, isError := binding.Execute(context.Background(), QueenStopWorkerToolName, nil); !isError || !strings.Contains(content, "No runtime on this session.") {
		t.Fatalf("no-runtime stop = (%v, %q)", isError, content)
	}

	session.Colony = colonyHostStub{runtime: runtime}
	workerID := spawnGatedWorker(t, runtime, "long task")

	content, isError := binding.Execute(context.Background(), QueenStopWorkerToolName, map[string]any{"reason": "user asked", "grace_seconds": 0})
	if isError {
		t.Fatalf("stop_worker failed: %s", content)
	}
	payload := decodeToolPayload(t, content)
	if payload["status"] != "stopped" {
		t.Fatalf("status = %v, want stopped", payload["status"])
	}
	if payload["workers_stopped"] != json.Number("1") {
		t.Fatalf("workers_stopped = %v, want 1", payload["workers_stopped"])
	}
	if payload["reason"] != "user asked" {
		t.Fatalf("reason = %v", payload["reason"])
	}
	if info, ok := runtime.Worker(workerID); !ok || info.Status.IsActive() {
		t.Fatalf("worker %s still active after stop", workerID)
	}

	// Nothing live any more: the honest answer is no_active_workers.
	provider.release()
	content, isError = binding.Execute(context.Background(), QueenStopWorkerToolName, map[string]any{"grace_seconds": 0})
	if isError {
		t.Fatalf("second stop failed: %s", content)
	}
	if payload := decodeToolPayload(t, content); payload["status"] != "no_active_workers" {
		t.Fatalf("second stop status = %v", payload["status"])
	}
	runtime.Stop()
}

// Parity: tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen
func TestListWorkerQuestionsAndReply(t *testing.T) {
	provider := newLifecycleGateLLM()
	runtime := lifecycleTestColony(t, eventbus.NewEventBus(), provider)
	session, binding, _ := lifecycleTestSession(t)
	session.Colony = colonyHostStub{runtime: runtime}
	workerID := spawnGatedWorker(t, runtime, "wait for guidance")

	session.RecordPendingEscalation("req-1", map[string]any{
		"request_id": "req-1",
		"worker_id":  workerID,
		"colony_id":  "colony_lifecycle",
		"node_id":    "research_node",
		"reason":     "Credential wall",
		"context":    "HTTP 401 while calling the API",
		"opened_at":  session.NowWall() - 5,
	})

	listContent, isError := binding.Execute(context.Background(), QueenListWorkerQuestionsToolName, nil)
	if isError {
		t.Fatalf("list_worker_questions failed: %s", listContent)
	}
	list := decodeToolPayload(t, listContent)
	if list["count"] != json.Number("1") {
		t.Fatalf("count = %v", list["count"])
	}
	pending := list["pending"].([]any)
	entry := pending[0].(map[string]any)
	if entry["request_id"] != "req-1" || entry["worker_id"] != workerID || entry["reason"] != "Credential wall" {
		t.Fatalf("entry = %#v", entry)
	}
	if !strings.Contains(entry["context_preview"].(string), "HTTP 401") {
		t.Fatalf("context_preview = %v", entry["context_preview"])
	}

	replyContent, isError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{
		"request_id": "req-1",
		"reply":      "use the backup key",
	})
	if isError {
		t.Fatalf("reply_to_worker failed: %s", replyContent)
	}
	reply := decodeToolPayload(t, replyContent)
	if reply["status"] != "delivered" || reply["worker_id"] != workerID || reply["request_id"] != "req-1" {
		t.Fatalf("reply = %#v", reply)
	}
	if _, ok := session.PendingEscalation("req-1"); ok {
		t.Fatal("answered escalation must be dropped from the inbox")
	}

	// Unknown request ids answer with the upstream redirect.
	unknown, isError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{"request_id": "req-1", "reply": "again"})
	if !isError || !strings.Contains(unknown, "Unknown request_id") {
		t.Fatalf("unknown request = (%v, %q)", isError, unknown)
	}

	// A worker that already terminated reports worker_not_active, and the
	// entry is still dropped.
	session.RecordPendingEscalation("req-dead", map[string]any{
		"request_id": "req-dead",
		"worker_id":  "worker:ghost",
		"reason":     "gone",
		"context":    "",
		"opened_at":  session.NowWall(),
	})
	dead, isError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{"request_id": "req-dead", "reply": "hello"})
	if isError {
		t.Fatalf("dead reply failed: %s", dead)
	}
	if payload := decodeToolPayload(t, dead); payload["status"] != "worker_not_active" {
		t.Fatalf("dead reply = %#v", payload)
	}
	if _, ok := session.PendingEscalation("req-dead"); ok {
		t.Fatal("undeliverable escalation must be dropped")
	}

	provider.release()
	runtime.Stop()
}

// Parity: tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen
func TestReplyToWorkerErrorPaths(t *testing.T) {
	session, binding, _ := lifecycleTestSession(t)

	if content, isError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{"request_id": "req", "reply": "x"}); !isError || !strings.Contains(content, "No colony running in this session.") {
		t.Fatalf("no-colony reply = (%v, %q)", isError, content)
	}

	session.Colony = colonyHostStub{runtime: lifecycleTestColony(t, eventbus.NewEventBus(), nil)}
	session.PendingEscalations = nil
	if content, isError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{"request_id": "req", "reply": "x"}); !isError || !strings.Contains(content, "no escalation inbox") {
		t.Fatalf("no-inbox reply = (%v, %q)", isError, content)
	}

	session.PendingEscalations = map[string]map[string]any{}
	session.RecordPendingEscalation("req-nw", map[string]any{"request_id": "req-nw", "worker_id": ""})
	if content, isError := binding.Execute(context.Background(), QueenReplyToWorkerToolName, map[string]any{"request_id": "req-nw", "reply": "x"}); !isError || !strings.Contains(content, "missing worker_id") {
		t.Fatalf("missing-worker reply = (%v, %q)", isError, content)
	}
}

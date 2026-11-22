package colony

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

type scriptedResponse struct {
	Content    string
	ToolCalls  []llm.ToolCall
	StopReason string
}

func textResponse(text string) scriptedResponse {
	return scriptedResponse{Content: text, StopReason: "stop"}
}

func reportResponse(status, summary string, data map[string]any) scriptedResponse {
	arguments, _ := json.Marshal(map[string]any{
		"status":  status,
		"summary": summary,
		"data":    data,
	})
	return scriptedResponse{
		ToolCalls: []llm.ToolCall{{
			ID:        "report_1",
			Name:      "report_to_parent",
			Arguments: string(arguments),
		}},
		StopReason: "tool_calls",
	}
}

// toolResponse scripts one non-terminal tool call. A turn that calls a tool is
// "work": it resets the worker's text-only stall streak and lets the judge see
// the turn (a text-only worker turn never reaches the judge).
func toolResponse(id, name, arguments string) scriptedResponse {
	return scriptedResponse{
		ToolCalls: []llm.ToolCall{{
			ID:        id,
			Name:      name,
			Arguments: arguments,
		}},
		StopReason: "tool_calls",
	}
}

type scriptedLLM struct {
	mu        sync.Mutex
	scenarios []scriptedResponse
	byTask    map[string]scriptedResponse
	callIndex int
	usedTasks map[string]bool
	calls     []llm.CompletionRequest
}

func newScriptedLLM(scenarios ...scriptedResponse) *scriptedLLM {
	return &scriptedLLM{
		scenarios: scenarios,
		usedTasks: map[string]bool{},
	}
}

func newTaskScriptedLLM(byTask map[string]scriptedResponse) *scriptedLLM {
	return &scriptedLLM{byTask: byTask, usedTasks: map[string]bool{}}
}

func (m *scriptedLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)

	if len(m.byTask) > 0 {
		firstUser := firstUserText(req.Messages)
		for task, response := range m.byTask {
			if !strings.Contains(firstUser, task) {
				continue
			}
			if m.usedTasks[task] {
				return llm.Completion{Content: "Done.", StopReason: "stop"}, nil
			}
			m.usedTasks[task] = true
			return toCompletion(response), nil
		}
		return llm.Completion{Content: "Done.", StopReason: "stop"}, nil
	}

	if m.callIndex >= len(m.scenarios) {
		return llm.Completion{Content: "Done.", StopReason: "stop"}, nil
	}
	response := m.scenarios[m.callIndex]
	m.callIndex++
	return toCompletion(response), nil
}

func (m *scriptedLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *scriptedLLM) firstCall() (llm.CompletionRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return llm.CompletionRequest{}, false
	}
	return m.calls[0], true
}

func toCompletion(response scriptedResponse) llm.Completion {
	return llm.Completion{
		Content:    response.Content,
		ToolCalls:  response.ToolCalls,
		StopReason: response.StopReason,
	}
}

func firstUserText(messages []llm.ChatMessage) string {
	for _, message := range messages {
		if message.Role == "user" {
			return message.Content
		}
	}
	return ""
}

type crashingLLM struct {
	mu    sync.Mutex
	calls int
}

func (c *crashingLLM) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return llm.Completion{}, errors.New("boom — simulated LLM crash")
}

func testAgentSpec() agentloop.AgentSpec {
	systemPrompt := "You are a test agent."
	spec := agentloop.NewAgentSpec(
		"test_colony_agent",
		"Test Colony Agent",
		"Agent spec used for colony runtime tests.",
	)
	spec.SystemPrompt = &systemPrompt
	spec.ToolAccessPolicy = "all"
	return spec
}

func testGoal() *schemas.Goal {
	return &schemas.Goal{
		ID:          "test-goal",
		Name:        "Test goal",
		Description: "A test goal for the colony runtime.",
	}
}

func makeTestColony(t *testing.T, provider llm.Completer, bus *eventbus.EventBus) *ColonyRuntime {
	t.Helper()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: bus,
		StreamID: "test_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

type eventCollector struct {
	mu     sync.Mutex
	events []*eventbus.AgentEvent
}

func collectEvents(bus *eventbus.EventBus, types ...eventbus.EventType) *eventCollector {
	collector := &eventCollector{}
	bus.Subscribe(types, func(event *eventbus.AgentEvent) {
		collector.mu.Lock()
		collector.events = append(collector.events, event)
		collector.mu.Unlock()
	})
	return collector
}

func (c *eventCollector) snapshot() []*eventbus.AgentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*eventbus.AgentEvent(nil), c.events...)
}

func (c *eventCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return condition()
}

func toolNames(tools []llm.ToolDef) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func hasTool(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func assertJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("value = %s, want %s", gotJSON, wantJSON)
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestColonyRuntimeGoalProperty::test_goal_is_public_property
func TestColonyRuntimeGoalIsPublicProperty(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(textResponse("ok")), eventbus.NewEventBus())

	if runtime.Goal() == nil {
		t.Fatal("Goal() = nil, want the configured goal")
	}
	if runtime.Goal().ID != "test-goal" {
		t.Fatalf("Goal().ID = %q, want test-goal", runtime.Goal().ID)
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestStartOverseer::test_start_overseer_creates_persistent_worker
func TestStartOverseerCreatesPersistentWorker(t *testing.T) {
	provider := newScriptedLLM(textResponse("idle"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	overseer, err := runtime.StartOverseer(context.Background(), testAgentSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if runtime.Overseer() != overseer {
		t.Fatal("Overseer() does not return the started overseer")
	}
	if !overseer.IsPersistent() {
		t.Fatal("overseer.IsPersistent() = false, want true")
	}
	if overseer.Context == nil || overseer.Context.StreamID != "overseer" {
		t.Fatalf("overseer context stream = %q, want overseer", streamIDOf(overseer))
	}
	if want := "overseer:" + runtime.StreamID(); overseer.ID != want {
		t.Fatalf("overseer.ID = %q, want %q", overseer.ID, want)
	}
	if !eventually(t, time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never called the LLM")
	}
	if !overseer.IsActive() {
		t.Fatal("overseer is not active while the runtime is live")
	}
}

func streamIDOf(worker *Worker) string {
	if worker.Context == nil {
		return ""
	}
	return worker.Context.StreamID
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestStartOverseer::test_start_overseer_idempotent
func TestStartOverseerIdempotent(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(textResponse("idle")), eventbus.NewEventBus())

	first, err := runtime.StartOverseer(context.Background(), testAgentSpec(), nil)
	if err != nil {
		t.Fatalf("first StartOverseer: %v", err)
	}
	second, err := runtime.StartOverseer(context.Background(), testAgentSpec(), nil)
	if err != nil {
		t.Fatalf("second StartOverseer: %v", err)
	}
	if first != second {
		t.Fatal("StartOverseer is not idempotent: returned two different workers")
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestReportToParent::test_worker_report_emits_subagent_report_event
func TestWorkerReportEmitsSubagentReportEvent(t *testing.T) {
	provider := newScriptedLLM(reportResponse(
		"success",
		"Fetched 5 rows from the API.",
		map[string]any{"rows": 5, "table": "honeycomb"},
	))
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)

	reports := collectEvents(bus, eventbus.EventSubagentReport)
	lifecycle := collectEvents(bus, eventbus.EventExecutionCompleted, eventbus.EventExecutionFailed)

	ids, err := runtime.Spawn(context.Background(), "Fetch 5 rows from honeycomb", SpawnOptions{Count: 1})
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

	// EventBus 以 goroutine 派发 handler：SUBAGENT_REPORT 与 lifecycle 事件到达
	// 顺序不定，快照前两者都要等到（与 overseer_park_test 同一竞态类）。
	if !eventually(t, 5*time.Second, func() bool {
		return !worker.IsActive() && reports.count() > 0 && lifecycle.count() > 0
	}) {
		t.Fatal("worker did not finish and publish its report+lifecycle event within timeout")
	}

	events := reports.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d SUBAGENT_REPORT events, want 1", len(events))
	}
	event := events[0]
	if got := event.Data["worker_id"]; got != ids[0] {
		t.Fatalf("data.worker_id = %v, want %s", got, ids[0])
	}
	if got := event.Data["status"]; got != "success" {
		t.Fatalf("data.status = %v, want success", got)
	}
	if got := event.Data["summary"]; got != "Fetched 5 rows from the API." {
		t.Fatalf("data.summary = %v, want the explicit summary", got)
	}
	assertJSONEqual(t, event.Data["data"], map[string]any{"rows": 5, "table": "honeycomb"})
	if got := event.Data["task"]; got != "Fetch 5 rows from honeycomb" {
		t.Fatalf("data.task = %v, want the worker task", got)
	}

	lifecycleEvents := lifecycle.snapshot()
	if len(lifecycleEvents) != 1 {
		t.Fatalf("got %d lifecycle events, want 1", len(lifecycleEvents))
	}
	if lifecycleEvents[0].Type != eventbus.EventExecutionCompleted {
		t.Fatalf("lifecycle event = %s, want execution_completed", lifecycleEvents[0].Type)
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestReportToParent::test_worker_crash_emits_synthesised_failed_report
func TestWorkerCrashEmitsSynthesisedFailedReport(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, &crashingLLM{}, bus)

	reports := collectEvents(bus, eventbus.EventSubagentReport)

	ids, err := runtime.Spawn(context.Background(), "crashing task", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}

	if !eventually(t, 5*time.Second, func() bool {
		return !worker.IsActive() && reports.count() > 0
	}) {
		t.Fatal("crashed worker did not publish a report within timeout")
	}

	events := reports.snapshot()
	if len(events) < 1 {
		t.Fatal("crashed worker published no SUBAGENT_REPORT")
	}
	if got := events[0].Data["worker_id"]; got != ids[0] {
		t.Fatalf("data.worker_id = %v, want %s", got, ids[0])
	}
	if got := events[0].Data["status"]; got != "failed" {
		t.Fatalf("data.status = %v, want failed", got)
	}
	if worker.Status() != WorkerFailed {
		t.Fatalf("worker status = %s, want failed", worker.Status())
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports::test_spawn_batch_returns_one_id_per_task
func TestSpawnBatchReturnsOneIDPerTask(t *testing.T) {
	provider := newTaskScriptedLLM(map[string]scriptedResponse{
		"Fetch batch 1": reportResponse("success", "batch 1 done", nil),
		"Fetch batch 2": reportResponse("success", "batch 2 done", nil),
		"Fetch batch 3": reportResponse("success", "batch 3 done", nil),
	})
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{
		{Task: "Fetch batch 1"},
		{Task: "Fetch batch 2"},
		{Task: "Fetch batch 3"},
	})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("SpawnBatch returned %d ids, want 3", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate worker id %q", id)
		}
		seen[id] = true
		if runtime.GetWorker(id) == nil {
			t.Fatalf("GetWorker(%q) = nil", id)
		}
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports::test_wait_for_worker_reports_collects_all
func TestWaitForWorkerReportsCollectsAll(t *testing.T) {
	provider := newTaskScriptedLLM(map[string]scriptedResponse{
		"batch 1": reportResponse("success", "w1 done", map[string]any{"batch": 1, "rows": 10}),
		"batch 2": reportResponse("success", "w2 done", map[string]any{"batch": 2, "rows": 15}),
		"batch 3": reportResponse("failed", "w3 broke", map[string]any{"batch": 3, "error_code": 503}),
	})
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{
		{Task: "batch 1"},
		{Task: "batch 2"},
		{Task: "batch 3"},
	})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}

	reports, err := runtime.WaitForWorkerReports(context.Background(), ids, 10*time.Second)
	if err != nil {
		t.Fatalf("WaitForWorkerReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}

	byID := map[string]map[string]any{}
	for _, report := range reports {
		byID[report["worker_id"].(string)] = report
	}

	first := byID[ids[0]]
	if first["status"] != "success" || first["summary"] != "w1 done" {
		t.Fatalf("first report = %v, want success/w1 done", first)
	}
	assertJSONEqual(t, first["data"], map[string]any{"batch": 1, "rows": 10})

	second := byID[ids[1]]
	if second["status"] != "success" {
		t.Fatalf("second report status = %v, want success", second["status"])
	}
	assertJSONEqual(t, second["data"], map[string]any{"batch": 2, "rows": 15})

	third := byID[ids[2]]
	if third["status"] != "failed" {
		t.Fatalf("third report status = %v, want failed", third["status"])
	}
	assertJSONEqual(t, third["data"], map[string]any{"batch": 3, "error_code": 503})
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports::test_wait_for_worker_reports_returns_in_input_order
func TestWaitForWorkerReportsReturnsInInputOrder(t *testing.T) {
	provider := newTaskScriptedLLM(map[string]scriptedResponse{
		"task-A": reportResponse("success", "A", nil),
		"task-B": reportResponse("success", "B", nil),
		"task-C": reportResponse("success", "C", nil),
	})
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{
		{Task: "task-A"},
		{Task: "task-B"},
		{Task: "task-C"},
	})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}

	reports, err := runtime.WaitForWorkerReports(context.Background(), ids, 10*time.Second)
	if err != nil {
		t.Fatalf("WaitForWorkerReports: %v", err)
	}

	for index, report := range reports {
		if report["worker_id"] != ids[index] {
			t.Fatalf("report[%d].worker_id = %v, want %s", index, report["worker_id"], ids[index])
		}
	}
	for index, want := range []string{"A", "B", "C"} {
		if reports[index]["summary"] != want {
			t.Fatalf("report[%d].summary = %v, want %s", index, reports[index]["summary"], want)
		}
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSpawnBatchAndWaitForReports::test_wait_for_worker_reports_missing_id
func TestWaitForWorkerReportsMissingID(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(textResponse("noop")), eventbus.NewEventBus())

	reports, err := runtime.WaitForWorkerReports(context.Background(), []string{"nonexistent_worker"}, time.Second)
	if err != nil {
		t.Fatalf("WaitForWorkerReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	if reports[0]["worker_id"] != "nonexistent_worker" {
		t.Fatalf("worker_id = %v, want nonexistent_worker", reports[0]["worker_id"])
	}
	if reports[0]["status"] != "failed" {
		t.Fatalf("status = %v, want failed", reports[0]["status"])
	}
	if reports[0]["error"] != "no_such_worker" {
		t.Fatalf("error = %v, want no_such_worker", reports[0]["error"])
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestSeedConversation::test_seed_conversation_writes_parts_to_storage
func TestSeedConversationWritesPartsToStorage(t *testing.T) {
	provider := newScriptedLLM(textResponse("idle"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	seed := []SeedMessage{
		{Seq: 0, Role: "user", Content: "What's the plan?"},
		{Seq: 1, Role: "assistant", Content: "Let's fetch data."},
		{Seq: 2, Role: "user", Content: "Do it in parallel."},
	}
	overseer, err := runtime.StartOverseer(context.Background(), testAgentSpec(), seed)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if runtime.Overseer() == nil || runtime.Overseer() != overseer {
		t.Fatal("overseer was not started")
	}
	if !eventually(t, time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never called the LLM")
	}
	if !overseer.IsActive() {
		t.Fatal("overseer is not active after seeding")
	}

	partsDir := filepath.Join(runtime.storagePath, "workers", overseer.ID, "parts")
	for index := range seed {
		path := filepath.Join(partsDir, strconv.Itoa(index)+".json")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("seed part %s missing: %v", path, err)
		}
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestReportToParentGatingByStream::test_report_to_parent_only_for_worker_streams
func TestReportToParentOnlyForWorkerStreams(t *testing.T) {
	provider := newScriptedLLM(textResponse("ok"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	if _, err := runtime.Spawn(context.Background(), "test", SpawnOptions{Count: 1}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !eventually(t, 5*time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("worker never called the LLM")
	}

	first, ok := provider.firstCall()
	if !ok {
		t.Fatal("no recorded LLM call")
	}
	if !hasTool(toolNames(first.Tools), "report_to_parent") {
		t.Fatalf("worker tools = %v, want report_to_parent", toolNames(first.Tools))
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestReportToParentGatingByStream::test_worker_with_non_worker_colon_stream_id_does_not_terminate
func TestWorkerWithNonWorkerColonStreamIDDoesNotTerminate(t *testing.T) {
	provider := newScriptedLLM(
		reportResponse("success", "I'm trying to report", nil),
		textResponse("still alive"),
	)
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.Spawn(context.Background(), "diagnostic", SpawnOptions{Count: 1, StreamID: "worker"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}

	if !eventually(t, 5*time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("worker never called the LLM")
	}
	first, _ := provider.firstCall()
	if hasTool(toolNames(first.Tools), "report_to_parent") {
		t.Fatalf("report_to_parent was offered to a stream_id='worker' worker: %v", toolNames(first.Tools))
	}

	eventually(t, 5*time.Second, func() bool { return !worker.IsActive() })

	if worker.ExplicitReport() != nil {
		t.Fatalf("worker recorded an explicit_report despite stream_id not matching worker:*: %v", worker.ExplicitReport())
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestReportToParentGatingByStream::test_report_to_parent_with_extra_tools_in_same_batch_still_terminates
func TestReportToParentWithExtraToolsInSameBatchStillTerminates(t *testing.T) {
	runaway := scriptedResponse{
		ToolCalls: []llm.ToolCall{{
			ID:        "runaway_1",
			Name:      "some_other_tool",
			Arguments: `{"x": 1}`,
		}},
		StopReason: "tool_calls",
	}
	withReport := scriptedResponse{
		ToolCalls: []llm.ToolCall{
			{ID: "runaway_1", Name: "some_other_tool", Arguments: `{"x": 1}`},
			{ID: "report_1", Name: "report_to_parent", Arguments: `{"status":"success","summary":"done","data":{}}`},
		},
		StopReason: "tool_calls",
	}
	provider := newScriptedLLM(withReport, runaway, runaway, runaway)
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.Spawn(context.Background(), "report+filler", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}

	if !eventually(t, 5*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatal("worker failed to terminate")
	}

	report := worker.ExplicitReport()
	if report == nil {
		t.Fatal("worker did not record an explicit report")
	}
	if report["status"] != "success" {
		t.Fatalf("report status = %v, want success", report["status"])
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("worker made %d LLM calls, want 1 — the inner loop re-streamed after report_to_parent", got)
	}
}

// Parity: core/tests/test_colony_runtime_overseer.py::TestReportToParentGatingByStream::test_worker_with_worker_colon_stream_id_terminates_via_report
func TestWorkerWithWorkerColonStreamIDTerminatesViaReport(t *testing.T) {
	provider := newScriptedLLM(
		reportResponse("success", "done", nil),
		textResponse("zombie"),
	)
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.Spawn(context.Background(), "positive control", SpawnOptions{Count: 1})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("GetWorker returned nil for a spawned worker")
	}

	if !eventually(t, 5*time.Second, func() bool { return !worker.IsActive() }) {
		t.Fatal("worker:* worker failed to terminate after report_to_parent")
	}

	report := worker.ExplicitReport()
	if report == nil {
		t.Fatal("worker did not record an explicit report")
	}
	if report["status"] != "success" {
		t.Fatalf("report status = %v, want success", report["status"])
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("worker made %d LLM calls, want 1 — the loop did not exit on the report", got)
	}
}

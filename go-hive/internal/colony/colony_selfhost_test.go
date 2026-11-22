package colony

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// queenTestCaseSpec is the identity the overseer runs under in these tests: a
// queen prompt distinct from the worker prompt, so a scripted completer can
// tell the two apart.
func queenTestCaseSpec() agentloop.AgentSpec {
	systemPrompt := "You are the queen of this test colony."
	return agentloop.AgentSpec{
		ID:           "queen",
		Name:         "Queen",
		AgentType:    "event_loop",
		SystemPrompt: &systemPrompt,
	}
}

// queenAwareLLM routes responses by role: the overseer dispatches one
// run_worker batch on its first turn, workers answer with a plain result.
type queenAwareLLM struct {
	mu            sync.Mutex
	overseerCalls int
	workerCalls   int
	requests      []llm.CompletionRequest
}

func isQueenRequest(req llm.CompletionRequest) bool {
	if strings.Contains(strings.ToLower(req.System), "queen") {
		return true
	}
	for _, message := range req.Messages {
		if message.Role == "system" && strings.Contains(strings.ToLower(message.Content), "queen") {
			return true
		}
	}
	return false
}

func (m *queenAwareLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if !isQueenRequest(req) {
		m.workerCalls++
		return llm.Completion{Content: "worker finished the unit", StopReason: "stop"}, nil
	}
	m.overseerCalls++
	if m.overseerCalls == 1 {
		arguments, _ := json.Marshal(map[string]any{
			"tasks": []any{map[string]any{"task": "dig one hole"}},
		})
		return llm.Completion{
			ToolCalls:  []llm.ToolCall{{ID: "run_worker_1", Name: RunWorkerToolName, Arguments: string(arguments)}},
			StopReason: "tool_calls",
		}, nil
	}
	return llm.Completion{Content: "Dispatched the worker.", StopReason: "stop"}, nil
}

func (m *queenAwareLLM) counts() (overseer, worker int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.overseerCalls, m.workerCalls
}

func (m *queenAwareLLM) queenRequests() []llm.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]llm.CompletionRequest, 0, len(m.requests))
	for _, req := range m.requests {
		if isQueenRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func TestOverseerToolSurfaceIncludesQueenTools(t *testing.T) {
	provider := newScriptedLLM(textResponse("idle"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	if _, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !eventually(t, time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never called the LLM")
	}
	first, ok := provider.firstCall()
	if !ok {
		t.Fatal("no recorded overseer call")
	}
	names := toolNames(first.Tools)
	for _, want := range []string{
		RunWorkerToolName,
		"tracker_sql", "tracker_register_writable", "tracker_upsert", "tracker_query",
		"task_create", "task_update", "task_list", "task_get",
	} {
		if !hasTool(names, want) {
			t.Errorf("overseer tools = %v, missing %q", names, want)
		}
	}
	// Worker-only channels stay off the overseer surface: the overseer is the
	// parent, it reports to the user, not to another worker.
	if hasTool(names, "report_to_parent") {
		t.Errorf("overseer tools = %v, want no report_to_parent", names)
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::run_worker
func TestOverseerDispatchesWorkerThroughRunWorker(t *testing.T) {
	provider := &queenAwareLLM{}
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)
	events := collectEvents(bus, eventbus.EventSubagentReport)

	if _, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	var spawned *Worker
	if !eventually(t, 5*time.Second, func() bool {
		spawned = runtime.GetWorker("worker_1")
		return spawned != nil
	}) {
		t.Fatal("overseer run_worker call spawned no worker")
	}
	if !eventually(t, 5*time.Second, func() bool { return spawned.Status().Done() }) {
		t.Fatalf("worker status = %s, want a terminal state", spawned.Status())
	}
	if spawned.Status() != WorkerCompleted {
		t.Fatalf("worker status = %s, want completed", spawned.Status())
	}
	overseerCalls, workerCalls := provider.counts()
	if overseerCalls < 2 {
		t.Fatalf("overseer calls = %d, want a second turn after the tool result", overseerCalls)
	}
	if workerCalls < 1 {
		t.Fatalf("worker calls = %d, want at least one", workerCalls)
	}
	if !eventually(t, time.Second, func() bool { return len(events.snapshot()) > 0 }) {
		t.Fatal("spawned worker published no SUBAGENT_REPORT")
	}
	report := events.snapshot()[0]
	if report.Data["worker_id"] != spawned.ID {
		t.Fatalf("report worker_id = %v, want %q", report.Data["worker_id"], spawned.ID)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func TestOverseerConsumesInjectedMessage(t *testing.T) {
	provider := newScriptedLLM(textResponse("idle"), textResponse("working on it"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	overseer, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil)
	if err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !eventually(t, time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}
	if !overseer.Inject("now check the north field") {
		t.Fatal("Inject returned false on a live overseer")
	}
	if !eventually(t, 2*time.Second, func() bool {
		return anyCallContains(provider, "check the north field")
	}) {
		t.Fatal("overseer never ran a turn for the injected message")
	}
	last, ok := lastScriptedCall(provider)
	if !ok {
		t.Fatal("no recorded LLM call")
	}
	if !anyUserTextContains(last.Messages, "check the north field") {
		t.Fatalf("injected message missing from the overseer turn: %v", last.Messages)
	}
}

// Parity: framework/tasks/tools/register.py::register_task_tools
func TestOverseerTaskToolsWritePlanUnderColony(t *testing.T) {
	provider := newScriptedLLM(textResponse("idle"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())
	if _, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}

	result, handled := runtime.executeOverseerTool(nil, agentloop.ToolCallRequest{
		Name: "task_create",
		Input: map[string]any{
			"goal": "keep the hive tidy",
			"tasks": []any{
				map[string]any{"subject": "sweep the entrance"},
			},
		},
	})
	if !handled {
		t.Fatal("task_create was not routed to the overseer task tools")
	}
	if result.IsError {
		t.Fatalf("task_create failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"success": true`) && !strings.Contains(result.Content, `"success":true`) {
		t.Fatalf("task_create result = %s", result.Content)
	}
	planPath := filepath.Join(runtime.storagePath, "_misc", overseerTaskSessionID, "tasks.json")
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("overseer task list missing at %s: %v", planPath, err)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
func TestWorkerTranscriptPersistedToDisk(t *testing.T) {
	provider := newScriptedLLM(textResponse("wrote the report"))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "persist me"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if worker == nil {
		t.Fatal("spawned worker is not registered")
	}
	// Done closes after the terminal artefacts are written; Status() flips a
	// few instructions earlier (finishOnce sets it before its after callback),
	// which made this assertion race the events.jsonl write.
	select {
	case <-worker.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("worker status = %s, want a terminal state", worker.Status())
	}

	workerDir := filepath.Join(runtime.storagePath, "workers", ids[0])
	for _, path := range []string{
		filepath.Join(workerDir, "conversations", "parts", "0000000000.json"),
		filepath.Join(workerDir, workerMetaFile),
		filepath.Join(workerDir, workerResultFile),
		filepath.Join(workerDir, workerEventsFile),
	} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("worker artefact missing at %s: %v", path, statErr)
		}
	}
	meta := readWorkerMeta(workerDir)
	if meta["task"] != "persist me" {
		t.Fatalf("meta task = %v, want %q", meta["task"], "persist me")
	}
	if meta["colony_id"] != runtime.StreamID() {
		t.Fatalf("meta colony_id = %v, want %q", meta["colony_id"], runtime.StreamID())
	}
}

// Parity: framework/host/worker.py::Worker._emit_terminal_events
func TestWorkerReportCarriesDurationSeconds(t *testing.T) {
	provider := newScriptedLLM(textResponse("done"))
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)
	events := collectEvents(bus, eventbus.EventSubagentReport)

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "time me"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return worker != nil && worker.Status().Done() }) {
		t.Fatal("worker did not finish")
	}
	if !eventually(t, time.Second, func() bool { return len(events.snapshot()) > 0 }) {
		t.Fatal("no SUBAGENT_REPORT event")
	}
	data := events.snapshot()[0].Data
	duration, ok := floatInput(data["duration_seconds"])
	if !ok || duration <= 0 {
		t.Fatalf("report duration_seconds = %v, want a positive number", data["duration_seconds"])
	}
	formatted := FormatWorkerReport(data, 0)
	if !strings.Contains(formatted, "<duration_seconds>") {
		t.Fatalf("[WORKER_REPORT] block has no duration line:\n%s", formatted)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
func TestResumeWorkersContinuesSavedTranscript(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "first pass", nil), reportResponse("success", "second pass", nil))
	runtime := makeTestColony(t, provider, eventbus.NewEventBus())

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "finish the ledger"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return worker.Status().Done() }) {
		t.Fatalf("worker status = %s, want a terminal state", worker.Status())
	}
	before := provider.callCount()

	resumed, failures := runtime.ResumeWorkers(context.Background(), ids, "use the second drawer", nil, "batch_resume")
	if len(failures) != 0 {
		t.Fatalf("resume failures = %v", failures)
	}
	if len(resumed) != 1 || resumed[0] != ids[0] {
		t.Fatalf("resumed = %v, want [%s]", resumed, ids[0])
	}
	resumedWorker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return resumedWorker.Status().Done() }) {
		t.Fatalf("resumed worker status = %s, want a terminal state", resumedWorker.Status())
	}
	if provider.callCount() <= before {
		t.Fatal("resumed worker never called the LLM")
	}
	last, ok := lastScriptedCall(provider)
	if !ok {
		t.Fatal("no recorded LLM call")
	}
	userText := firstUserText(last.Messages)
	if !strings.Contains(userText, "finish the ledger") {
		t.Fatalf("resumed turn lost the original task: %q", userText)
	}
	if !strings.Contains(lastUserText(last.Messages), "use the second drawer") {
		t.Fatalf("resumed turn lost the queen's guidance: %q", lastUserText(last.Messages))
	}
	if resumedWorker.BatchID != "batch_resume" {
		t.Fatalf("resumed batch id = %q, want batch_resume", resumedWorker.BatchID)
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.resume_worker
func TestResumeWorkersRejectsMissingTranscript(t *testing.T) {
	runtime := makeTestColony(t, newScriptedLLM(textResponse("ok")), eventbus.NewEventBus())

	resumed, failures := runtime.ResumeWorkers(context.Background(), []string{"worker_nope"}, "", nil, "")
	if len(resumed) != 0 {
		t.Fatalf("resumed = %v, want none", resumed)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want one entry", failures)
	}
	message, _ := failures[0]["error"].(string)
	if !strings.Contains(message, "no saved conversation") {
		t.Fatalf("failure = %q, want a missing-transcript reason", message)
	}
}

// Parity: framework/server/queen_orchestrator.py::_on_worker_report
func TestOverseerReportReachesQueenSubscriber(t *testing.T) {
	provider := newScriptedLLM(textResponse("idle"))
	bus := eventbus.NewEventBus()
	runtime := makeTestColony(t, provider, bus)

	var mu sync.Mutex
	blocks := []string{}
	unsubscribe, err := SubscribeQueenReports(runtime, func(block string) {
		mu.Lock()
		blocks = append(blocks, block)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("SubscribeQueenReports: %v", err)
	}
	defer unsubscribe()

	if _, err := runtime.StartOverseer(context.Background(), queenTestCaseSpec(), nil); err != nil {
		t.Fatalf("StartOverseer: %v", err)
	}
	if !eventually(t, time.Second, func() bool { return provider.callCount() >= 1 }) {
		t.Fatal("overseer never ran its first turn")
	}
	runtime.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(blocks) == 0 {
		t.Fatal("overseer terminal report was not forwarded to the queen")
	}
	if !strings.Contains(blocks[0], "<worker_id>overseer:") {
		t.Fatalf("forwarded block = %q, want the overseer report", blocks[0])
	}
	if !strings.Contains(blocks[0], "<duration_seconds>") {
		t.Fatalf("forwarded block has no duration:\n%s", blocks[0])
	}
}

// lastScriptedCall returns the most recent request the scripted completer saw.
func lastScriptedCall(provider *scriptedLLM) (llm.CompletionRequest, bool) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.calls) == 0 {
		return llm.CompletionRequest{}, false
	}
	return provider.calls[len(provider.calls)-1], true
}

// lastUserText returns the last user message in a request.
func lastUserText(messages []llm.ChatMessage) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return messages[index].Content
		}
	}
	return ""
}

// anyUserTextContains reports whether any user message carries text.
func anyUserTextContains(messages []llm.ChatMessage, text string) bool {
	for _, message := range messages {
		if message.Role == "user" && strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

// anyCallContains reports whether any recorded request carried text, in the
// system prompt, a message, or a tool result.
func anyCallContains(provider *scriptedLLM, text string) bool {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	for _, req := range provider.calls {
		if strings.Contains(req.System, text) {
			return true
		}
		for _, message := range req.Messages {
			if strings.Contains(message.Content, text) {
				return true
			}
		}
	}
	return false
}

package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tasks"
)

// recordingChatLLM answers each call from a fixed script and records the
// requests, so a test can assert what the model actually saw.
type recordingChatLLM struct {
	mu        sync.Mutex
	requests  []llm.CompletionRequest
	responses []llm.Completion
	err       error
}

func (s *recordingChatLLM) ModelName() string { return "scripted-chat" }

func (s *recordingChatLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	index := len(s.requests) - 1
	s.mu.Unlock()
	if s.err != nil {
		return llm.Completion{}, s.err
	}
	if len(s.responses) == 0 {
		return llm.Completion{Content: "ok", Model: "scripted-chat"}, nil
	}
	if index >= len(s.responses) {
		index = len(s.responses) - 1
	}
	return s.responses[index], nil
}

func (s *recordingChatLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *recordingChatLLM) request(index int) llm.CompletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[index]
}

func chatAllText(messages []llm.ChatMessage) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

// newReminderChatRuntime builds a queen chat runtime with the reminder wiring
// on, returning it without a cleanup hook so a test can assert post-Close
// behavior explicitly.
func newReminderChatRuntime(t *testing.T, root, sessionID string, model llm.Completer, tools []llm.ToolDef, idle *agentloop.IdleNudgeConfig, bound ColonyRuntime) *ChatRuntime {
	t.Helper()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     tools,
		Execute:   func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:      root,
		SessionID: sessionID,
		IdleNudge: idle,
		Colony:    bound,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	return runtime
}

func waitForCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func countSystemRemindersWith(parts []map[string]any, needle string) int {
	count := 0
	for _, part := range parts {
		flag, _ := part["is_system_reminder"].(bool)
		content, _ := part["content"].(string)
		if flag && strings.Contains(content, needle) {
			count++
		}
	}
	return count
}

// Parity: core/tests/test_reminder_e2e.py::test_session_start_reminder_reaches_the_model
func TestChatRuntimeSessionStartTaskReminderInjectsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	tasks.ResetSessionDirCache()
	t.Cleanup(tasks.ResetSessionDirCache)

	sessionID := "reminder-session-start"
	store := tasks.NewTaskStore(home)
	if _, err := store.CreateTask(sessionID, tasks.CreateTaskSpec{Subject: "Verify reminder wiring"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	runtime := newReminderChatRuntime(t, t.TempDir(), sessionID, model,
		[]llm.ToolDef{{Name: "task_create"}}, nil, nil)
	defer func() { _ = runtime.Close() }()

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	if err := runtime.Deliver("again"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts := waitForAssistantParts(t, runtime, 2)

	first := chatAllText(model.request(0).Messages)
	reminderAt := strings.Index(first, "Verify reminder wiring")
	userAt := strings.Index(first, "hello")
	if reminderAt < 0 || userAt < 0 {
		t.Fatalf("first request missing task reminder or user message:\n%s", first)
	}
	if reminderAt > userAt {
		t.Fatalf("task reminder should precede the first user message:\n%s", first)
	}
	if got := countSystemRemindersWith(parts, "Verify reminder wiring"); got != 1 {
		t.Fatalf("task reminder injected %d times, want 1", got)
	}
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_fires_after_pilot_then_tracker_write
func TestChatRuntimePostToolColonyNudgeRequiresBoundColony(t *testing.T) {
	tools := []llm.ToolDef{
		{Name: "browser_navigate"},
		{Name: "tracker_upsert"},
		{Name: "run_worker"},
	}
	responses := []llm.Completion{
		{ToolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "browser_navigate", Arguments: `{"url":"https://example.com"}`},
			{ID: "call_2", Name: "tracker_upsert", Arguments: `{"row":1}`},
		}},
		{Content: "done"},
	}
	run := func(t *testing.T, bound ColonyRuntime) *ChatRuntime {
		t.Helper()
		model := &recordingChatLLM{responses: responses}
		runtime := newReminderChatRuntime(t, t.TempDir(), "reminder-colony", model, tools, nil, bound)
		t.Cleanup(func() { _ = runtime.Close() })
		if err := runtime.Deliver("run the pilot"); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		// One assistant message carries the tool calls; the second is the
		// final text turn after the tool batch.
		waitForAssistantParts(t, runtime, 2)
		return runtime
	}

	boundRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{StreamID: "colony-1"})
	bound := run(t, boundRuntime)
	if got := chatAllText(bound.modelRequestText(t, 1)); !strings.Contains(got, "pilot unit") {
		t.Fatalf("bound colony did not inject the parallel nudge:\n%s", got)
	}
	if items := bound.hub.Collect(agentloop.POST_TOOL_USE, agentloop.NewReminderAgentContext(bound.agentCtx), []string{"browser_navigate", "tracker_upsert"}, ""); len(items) != 0 {
		t.Fatalf("parallel nudge repeated within its cooldown: %+v", items)
	}

	plain := run(t, nil)
	if got := chatAllText(plain.modelRequestText(t, 1)); strings.Contains(got, "pilot unit") {
		t.Fatalf("unbound colony must not inject the parallel nudge:\n%s", got)
	}
}

// modelRequestText exposes the recorded model request to the test helper.
func (r *ChatRuntime) modelRequestText(t *testing.T, index int) []llm.ChatMessage {
	t.Helper()
	model, ok := r.deps.LLM.(*recordingChatLLM)
	if !ok {
		t.Fatalf("runtime model is %T, want *recordingChatLLM", r.deps.LLM)
	}
	return model.request(index).Messages
}

// Parity: core/tests/test_idle_nudge.py::test_hub_ticker_parks_idle_nudge
func TestChatRuntimeIdleNudgeWakesBrokenSessionOnce(t *testing.T) {
	model := &recordingChatLLM{err: errors.New("boom")}
	idle := &agentloop.IdleNudgeConfig{MaxNudges: 1, BrokenBudgetSeconds: 0.02}
	runtime := newReminderChatRuntime(t, t.TempDir(), "reminder-idle", model, nil, idle, nil)
	defer func() { _ = runtime.Close() }()

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForCondition(t, "the first turn to fail", func() bool { return runtime.LastError() != nil })
	waitForCondition(t, "the idle nudge turn", func() bool { return model.callCount() >= 2 })

	nudge := chatAllText(model.request(1).Messages)
	for _, want := range []string{"<system-reminder>", "parked", "llm_error"} {
		if !strings.Contains(nudge, want) {
			t.Fatalf("idle nudge missing %q:\n%s", want, nudge)
		}
	}
	time.Sleep(120 * time.Millisecond)
	if got := model.callCount(); got != 2 {
		t.Fatalf("idle nudge repeated: %d model calls, want 2", got)
	}
	if !runtime.hub.HasTicker() {
		t.Fatal("idle ticker should be running while the session is open")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if runtime.hub.HasTicker() {
		t.Fatal("idle ticker still running after Close")
	}
	time.Sleep(60 * time.Millisecond)
	if pending := runtime.hub.PendingLen(); pending != 0 {
		t.Fatalf("closed session parked %d reminder(s)", pending)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_when_park_is_turn_done
func TestChatRuntimeIdleNudgeStaysSilentAfterCleanTurn(t *testing.T) {
	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	idle := &agentloop.IdleNudgeConfig{
		MaxNudges:             1,
		BudgetSeconds:         0.02,
		AwaitingBudgetSeconds: 0.02,
		BrokenBudgetSeconds:   0.02,
	}
	runtime := newReminderChatRuntime(t, t.TempDir(), "reminder-idle-clean", model, nil, idle, nil)
	defer func() { _ = runtime.Close() }()

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	time.Sleep(120 * time.Millisecond)
	if got := model.callCount(); got != 1 {
		t.Fatalf("clean turn_done park was nudged: %d model calls, want 1", got)
	}
}

// Parity: core/tests/test_reminder_e2e.py::test_session_start_reminder_reaches_the_model
func TestChatRuntimeSkillsCatalogInjectsOnce(t *testing.T) {
	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:           model,
		Root:          t.TempDir(),
		SessionID:     "reminder-skills",
		SkillsCatalog: func() string { return "SKILLS-CATALOG-MARKER" },
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	if err := runtime.Deliver("again"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts := waitForAssistantParts(t, runtime, 2)

	if got := chatAllText(model.request(0).Messages); !strings.Contains(got, "SKILLS-CATALOG-MARKER") {
		t.Fatalf("skills catalog missing from the first request:\n%s", got)
	}
	if got := countSystemRemindersWith(parts, "SKILLS-CATALOG-MARKER"); got != 1 {
		t.Fatalf("skills catalog injected %d times, want 1", got)
	}
}

// Parity-exempt: Go session-runtime provider wiring; upstream reads the task
// store and colony directly instead of through providers.
func TestChatRuntimeReminderProvidersEmptyWithoutData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	tasks.ResetSessionDirCache()
	t.Cleanup(tasks.ResetSessionDirCache)

	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	runtime := newReminderChatRuntime(t, t.TempDir(), "reminder-empty", model, nil, nil, nil)
	defer func() { _ = runtime.Close() }()

	ctx := agentloop.NewReminderAgentContext(runtime.agentCtx)
	if ctx.OpenTasksProvider == nil || ctx.SessionGoalProvider == nil ||
		ctx.ActiveWorkersProvider == nil || ctx.ColonyStatsProvider == nil ||
		ctx.QueenSkillsCatalogProvider == nil {
		t.Fatal("reminder providers were not filled")
	}
	if tasks := ctx.OpenTasksProvider(); len(tasks) != 0 {
		t.Fatalf("open tasks = %+v, want empty", tasks)
	}
	if goal := ctx.SessionGoalProvider(); goal != "" {
		t.Fatalf("session goal = %q, want empty", goal)
	}
	if workers := ctx.ActiveWorkersProvider(); len(workers) != 0 {
		t.Fatalf("active workers = %+v, want empty", workers)
	}
	if stats := ctx.ColonyStatsProvider(); stats.Active != 0 || stats.Total != 0 {
		t.Fatalf("colony stats = %+v, want zero", stats)
	}
	if catalog := ctx.QueenSkillsCatalogProvider(); catalog != "" {
		t.Fatalf("skills catalog = %q, want empty", catalog)
	}
}

// Parity-exempt: Go session-runtime no-data gate; asserts the hub stays silent
// when no source has anything to say.
func TestChatRuntimeNoReminderWithoutData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	tasks.ResetSessionDirCache()
	t.Cleanup(tasks.ResetSessionDirCache)

	model := &recordingChatLLM{responses: []llm.Completion{{Content: "ok", Model: "scripted-chat"}}}
	runtime := newReminderChatRuntime(t, t.TempDir(), "reminder-nodata", model, nil, nil, nil)
	defer func() { _ = runtime.Close() }()

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	if got := chatAllText(model.request(0).Messages); strings.Contains(got, "<system-reminder>") {
		t.Fatalf("a dataless session injected a reminder:\n%s", got)
	}
}

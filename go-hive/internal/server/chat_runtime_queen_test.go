package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// queenChatLLM replays a fixed completion script and records every request, so
// a test can inspect the system prompt and the conversation the loop sent.
type queenChatLLM struct {
	mu        sync.Mutex
	responses []llm.Completion
	requests  []llm.CompletionRequest
}

func (f *queenChatLLM) ModelName() string { return "queen-test-model" }

func (f *queenChatLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if len(f.responses) == 0 {
		return llm.Completion{Content: "done", Model: "queen-test-model", StopReason: "end_turn"}, nil
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	if next.Model == "" {
		next.Model = "queen-test-model"
	}
	if next.StopReason == "" {
		next.StopReason = "end_turn"
	}
	return next, nil
}

func (f *queenChatLLM) requestSnapshot() []llm.CompletionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.CompletionRequest(nil), f.requests...)
}

func newQueenChatRuntime(t *testing.T, model llm.Completer, homeRoot, queenName string) (*ChatRuntime, *chatEventRecorder, *eventbus.EventBus) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_20260101_000000_queen001",
		QueenName: queenName,
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, recordChatEvents(bus), bus
}

// Parity: framework/server/queen_orchestrator.py::_queen_identity_hook
// Parity: tests/test_queen_identity_prompt.py::test_identity_prompt_starts_with_plain_identity
func TestChatRuntimeInjectsQueenProfileIdentity(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	model := &queenChatLLM{}
	runtime, recorder, _ := newQueenChatRuntime(t, model, home.Root, "queen_technology")

	identity := runtime.agentCtx.IdentityPrompt
	if !strings.Contains(identity, "<core_identity>") {
		t.Fatalf("identity prompt = %q, want the formatted queen profile persona", identity)
	}
	if !strings.Contains(identity, "your name is Elon Musk, Head of Technology") {
		t.Fatalf("identity prompt = %q, want the selected queen's name and role", identity)
	}
	if strings.Contains(identity, "<roleplay_examples>") {
		t.Fatalf("live identity prompt = %q, want max_examples=0 to omit roleplay examples", identity)
	}
	if identity == chatIdentityPrompt {
		t.Fatal("identity prompt still the generic fallback for a queen with a profile")
	}

	if err := runtime.Deliver("hello queen"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	requests := model.requestSnapshot()
	if len(requests) == 0 {
		t.Fatal("model never received a completion request")
	}
	if !strings.Contains(requests[0].System, "your name is Elon Musk, Head of Technology") {
		t.Fatalf("completion system prompt = %q, want the queen persona", requests[0].System)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: tests/test_event_loop_node.py::test_client_input_requested_event_published
// Parity: tests/test_event_loop_node.py::test_queen_ask_user_with_real_tools
func TestChatRuntimeAskUserParksForInput(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	model := &queenChatLLM{responses: []llm.Completion{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "ask-1",
				Name:      "ask_user",
				Arguments: `{"questions":[{"id":"next","prompt":"Which next?","options":["Ship it","Wait"]}]}`,
			}},
		},
		{Content: "Over to you."},
	}}
	runtime, recorder, bus := newQueenChatRuntime(t, model, home.Root, "queen_technology")

	if err := runtime.Deliver("start the work"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	// ask_user must be advertised even though the session registered no tools.
	requests := model.requestSnapshot()
	if len(requests) == 0 {
		t.Fatal("model never received a completion request")
	}
	if !toolDefined(requests[0].Tools, "ask_user") {
		t.Fatalf("completion tools = %v, want ask_user advertised", toolNames(requests[0].Tools))
	}

	requested := recorder.ofType(eventbus.EventClientInputRequested)
	if len(requested) != 1 {
		t.Fatalf("client_input_requested count = %d, want 1 (%v)", len(requested), recorder.types())
	}
	if requested[0].StreamID != "queen" || requested[0].NodeID == nil || *requested[0].NodeID != "queen" {
		t.Fatalf("client_input_requested stream/node = %q/%v, want queen/queen", requested[0].StreamID, requested[0].NodeID)
	}
	if requested[0].Data["park_reason"] != "ask_user" {
		t.Fatalf("client_input_requested park_reason = %#v, want ask_user", requested[0].Data["park_reason"])
	}
	questions, ok := requested[0].Data["questions"].([]map[string]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("client_input_requested questions = %#v, want one question", requested[0].Data["questions"])
	}
	if questions[0]["id"] != "next" || questions[0]["prompt"] != "Which next?" {
		t.Fatalf("question = %#v, want id/prompt preserved", questions[0])
	}
	options, _ := questions[0]["options"].([]string)
	if strings.Join(options, "|") != "Ship it|Wait" {
		t.Fatalf("question options = %#v, want the plain string choices", questions[0]["options"])
	}

	// The turn ends parked on the question, not on plain turn_done.
	states := recorder.ofType(eventbus.EventLoopStateChanged)
	if len(states) < 2 {
		t.Fatalf("loop_state_changed count = %d, want executing then awaiting_user", len(states))
	}
	final := states[len(states)-1].Data
	if final["activity"] != "awaiting_user" || final["park_reason"] != "ask_user" {
		t.Fatalf("final loop state = %#v, want awaiting_user/ask_user", final)
	}
	if !runtime.hasPendingQuestions() {
		t.Fatal("runtime lost the pending ask_user question")
	}
	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["awaiting_input"] != true || snapshot["queen_busy_reason"] != "awaiting_input" {
		t.Fatalf("snapshot = %#v, want awaiting_input", snapshot)
	}
	if snapshot["park_reason"] != "ask_user" {
		t.Fatalf("snapshot park_reason = %#v, want ask_user", snapshot["park_reason"])
	}
	if pending, ok := snapshot["pending_questions"].([]map[string]any); !ok || len(pending) != 1 {
		t.Fatalf("snapshot pending_questions = %#v, want the ask_user question", snapshot["pending_questions"])
	}
}

// Parity: framework/agent_loop/agent_loop.py::_await_user_input
// Parity: tests/test_event_loop_node.py::test_answering_restored_question_clears_pending_input_on_disk
func TestChatRuntimeAnswerResumesFromAskUserPark(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	model := &queenChatLLM{responses: []llm.Completion{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "ask-1",
				Name:      "ask_user",
				Arguments: `{"questions":[{"id":"next","prompt":"Which next?","options":["Ship it","Wait"]}]}`,
			}},
		},
		{Content: "Over to you."},
		{Content: "Answer received."},
	}}
	runtime, recorder, bus := newQueenChatRuntime(t, model, home.Root, "queen_technology")

	if err := runtime.Deliver("start the work"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	if !runtime.hasPendingQuestions() {
		t.Fatal("runtime did not park on the ask_user question")
	}

	// The next user message is the answer: it clears the park and the turn
	// finishes on the ordinary turn_done state instead of asking again. The
	// chat route publishes the user echo before delivering; mirror that so the
	// snapshot resolves the outstanding question as it does in a live session.
	publishClientInputReceived(NewSession(runtime.sessionID, bus), "Ship it")
	if err := runtime.Deliver("Ship it"); err != nil {
		t.Fatalf("Deliver answer: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 2)

	if runtime.hasPendingQuestions() {
		t.Fatal("pending ask_user question survived the answer")
	}
	if got := len(recorder.ofType(eventbus.EventClientInputRequested)); got != 2 {
		t.Fatalf("client_input_requested count = %d, want the ask_user park plus the turn_done park after the answer", got)
	}
	// The answer's own turn parks on turn_done and announces that wait the
	// way upstream's _await_user_input does; the resolved ask_user request is
	// followed by the ordinary end-of-turn request.
	requested := recorder.ofType(eventbus.EventClientInputRequested)
	if requested[len(requested)-1].Data["park_reason"] != "turn_done" {
		t.Fatalf("final client_input_requested park_reason = %#v, want turn_done",
			requested[len(requested)-1].Data["park_reason"])
	}
	states := recorder.ofType(eventbus.EventLoopStateChanged)
	final := states[len(states)-1].Data
	if final["activity"] != "awaiting_user" || final["park_reason"] != "turn_done" {
		t.Fatalf("final loop state = %#v, want awaiting_user/turn_done after the answer", final)
	}
	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["pending_questions"] != nil {
		t.Fatalf("snapshot pending_questions = %#v, want cleared", snapshot["pending_questions"])
	}

	// The answer reached the model as the next user turn.
	requests := model.requestSnapshot()
	if len(requests) < 2 {
		t.Fatalf("model requests = %d, want the answer turn", len(requests))
	}
	found := false
	for _, message := range requests[len(requests)-1].Messages {
		if message.Role == "user" && strings.Contains(message.Content, "Ship it") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("last request messages = %#v, want the answer as a user message", requests[len(requests)-1].Messages)
	}
}

// Parity: framework/server/queen_orchestrator.py::_queen_identity_hook
func TestChatRuntimeIdentityFallsBackWithoutProfile(t *testing.T) {
	cases := []struct {
		name      string
		queenName string
		withHome  bool
	}{
		{name: "default queen has no profile", queenName: "default", withHome: true},
		{name: "no hive home", queenName: "queen_technology", withHome: false},
		{name: "unknown queen", queenName: "queen_missing", withHome: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			homeRoot := ""
			if tc.withHome {
				homeRoot = t.TempDir()
			}
			runtime, _, _ := newQueenChatRuntime(t, &queenChatLLM{}, homeRoot, tc.queenName)
			if runtime.agentCtx.IdentityPrompt != chatIdentityPrompt {
				t.Fatalf("identity prompt = %q, want the generic fallback", runtime.agentCtx.IdentityPrompt)
			}
		})
	}
}

func toolDefined(tools []llm.ToolDef, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func toolNames(tools []llm.ToolDef) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

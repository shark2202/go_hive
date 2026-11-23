package agentloop

// Framework-level colony dispatch parity: the two queen calls that open the
// "Create Colony" popup are intercepted before any executor runs. These pin
// the upstream gates (queen-only, phase, slug/goal/handoff/tasks), the exact
// error texts, and the park payloads the host turns into
// COLONY_SUGGESTION_REQUESTED.

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// queenAgentContext builds a queen-stream context with the given phase
// provider ("" leaves the phase unset, mirroring a host with no provider).
func queenAgentContext(completer llm.Completer, phase string) *AgentContext {
	ctx := midturnAgentContext(completer, SuggestColonyToolName, TaskCreateToolName)
	ctx.StreamID = "queen"
	if phase != "" {
		ctx.Phase = func() string { return phase }
	}
	return ctx
}

func toolResultFor(t *testing.T, result TurnResult, name string) ToolCallRecord {
	t.Helper()
	for _, record := range result.ToolResults {
		if record.ToolName == name {
			return record
		}
	}
	t.Fatalf("%s result missing from %#v", name, result.ToolResults)
	return ToolCallRecord{}
}

// Parity: framework/agent_loop/agent_loop.py:5083-5096 (suggest_colony queen-only gate)
func TestSuggestColonyIsQueenOnly(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "s1", Name: SuggestColonyToolName, Arguments: `{"colony_id":"daily_digest"}`}}},
		{content: "corrected"},
	}}
	executed := false
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == SuggestColonyToolName {
			executed = true
		}
		return ToolResult{Content: "result"}, nil
	})
	ctx := midturnAgentContext(completer, SuggestColonyToolName)
	ctx.StreamID = "judge" // not the queen stream
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	record := toolResultFor(t, result, SuggestColonyToolName)
	if !record.IsError || record.Content != ColonySuggestionQueenOnlyError {
		t.Fatalf("result = %#v, want the upstream queen-only error", record)
	}
	if result.PendingColonySuggestion != nil {
		t.Fatalf("pending suggestion = %#v, want none", result.PendingColonySuggestion)
	}
	if executed {
		t.Fatal("suggest_colony reached the executor")
	}
	if calls := completer.callCount(); calls != 2 {
		t.Fatalf("model calls = %d, want 2 (the error is an ordinary tool error)", calls)
	}
}

// Parity: framework/agent_loop/agent_loop.py:5100-5133 (suggest_colony phase gate)
func TestSuggestColonyRejectsColonyPhase(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "s1", Name: SuggestColonyToolName, Arguments: `{"colony_id":"daily_digest"}`}}},
		{content: "corrected"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		t.Fatalf("executor called for %s", req.Name)
		return ToolResult{}, nil
	})
	ctx := queenAgentContext(completer, "colony")
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	record := toolResultFor(t, result, SuggestColonyToolName)
	if !record.IsError || record.Content != ColonySuggestionPhaseError {
		t.Fatalf("result = %#v, want the upstream phase error", record)
	}
	if result.PendingColonySuggestion != nil {
		t.Fatalf("pending suggestion = %#v, want none", result.PendingColonySuggestion)
	}
}

// Parity: framework/agent_loop/agent_loop.py:5135-5147 (colony_id slug gate)
func TestSuggestColonyRejectsInvalidSlug(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "s1", Name: SuggestColonyToolName, Arguments: `{"colony_id":"Morning-HN"}`}}},
		{content: "corrected"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		t.Fatalf("executor called for %s", req.Name)
		return ToolResult{}, nil
	})
	ctx := queenAgentContext(completer, "independent")
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	record := toolResultFor(t, result, SuggestColonyToolName)
	if !record.IsError || record.Content != ColonySuggestionSlugError {
		t.Fatalf("result = %#v, want the upstream slug error", record)
	}
}

// Parity: framework/agent_loop/agent_loop.py:5150-5175 (suggest_colony success stash)
func TestSuggestColonySuccessParksWithPayload(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "s1", Name: SuggestColonyToolName, Arguments: `{"colony_id":"morning_hn_digest","reason":"23 profiles to research in parallel"}`}}},
		{content: "should not be requested"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		t.Fatalf("executor called for %s", req.Name)
		return ToolResult{}, nil
	})
	ctx := queenAgentContext(completer, "independent")
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if calls := completer.callCount(); calls != 1 {
		t.Fatalf("model calls = %d, want 1 (the turn parks on the popup)", calls)
	}
	if result.PendingColonySuggestion == nil {
		t.Fatalf("pending suggestion = nil, want the stashed payload")
	}
	if got := result.PendingColonySuggestion["colony_id"]; got != "morning_hn_digest" {
		t.Fatalf("colony_id = %#v", got)
	}
	if got := result.PendingColonySuggestion["reason"]; got != "23 profiles to research in parallel" {
		t.Fatalf("reason = %#v", got)
	}
	record := toolResultFor(t, result, SuggestColonyToolName)
	want := "Colony creation popup opened with colony_id='morning_hn_digest'. Waiting for the user " +
		"to confirm or dismiss. If they confirm, this session will lock and you'll be compacted into " +
		"the new colony's queen seed. If they dismiss, you'll receive a follow-up message and can continue."
	if record.IsError || record.Content != want {
		t.Fatalf("result = %#v, want the synthetic popup message", record)
	}
}

// Parity: framework/agent_loop/agent_loop.py:5100-5133 (phase provider nil skips the gate)
func TestSuggestColonyWithoutPhaseProviderSkipsPhaseGate(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "s1", Name: SuggestColonyToolName, Arguments: `{"colony_id":"ok_slug"}`}}},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		t.Fatalf("executor called for %s", req.Name)
		return ToolResult{}, nil
	})
	ctx := queenAgentContext(completer, "")
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if result.PendingColonySuggestion == nil {
		t.Fatal("pending suggestion = nil, want a park: a nil phase provider skips the gate")
	}
}

// Parity: framework/agent_loop/agent_loop.py:5178-5210 (task_create new_colony queen-only/phase gates)
func TestTaskCreateNewColonyGates(t *testing.T) {
	newColonyCall := llm.ToolCall{ID: "t1", Name: TaskCreateToolName, Arguments: `{"new_colony":true,"goal":"g","handoff":"h","tasks":[{"subject":"s"}]}`}
	cases := []struct {
		name   string
		stream string
		phase  string
		want   string
	}{
		{name: "queen only", stream: "judge", phase: "colony", want: ColonyPivotQueenOnlyError},
		{name: "colony phase only", stream: "queen", phase: "independent", want: "ERROR: task_create(new_colony=true) is colony-only (currently 'independent'). In DM, use new_session for unrelated work, or suggest_colony to spawn the first colony."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completer := &scriptedCompleter{scenarios: []scriptedResponse{
				{toolCalls: []llm.ToolCall{newColonyCall}},
				{content: "corrected"},
			}}
			loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
				t.Fatalf("executor called for %s", req.Name)
				return ToolResult{}, nil
			})
			ctx := midturnAgentContext(completer, TaskCreateToolName)
			ctx.StreamID = tc.stream
			if tc.phase != "" {
				ctx.Phase = func() string { return tc.phase }
			}
			conv := NewNodeConversation("You are a test assistant.")
			if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
				t.Fatalf("AddUserMessage: %v", err)
			}

			result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
			if err != nil {
				t.Fatalf("ExecuteTurn: %v", err)
			}
			record := toolResultFor(t, result, TaskCreateToolName)
			if !record.IsError || record.Content != tc.want {
				t.Fatalf("result = %#v, want %q", record, tc.want)
			}
			if result.PendingColonyPivot != nil {
				t.Fatalf("pending pivot = %#v, want none", result.PendingColonyPivot)
			}
			if calls := completer.callCount(); calls != 2 {
				t.Fatalf("model calls = %d, want 2 (a rejected pivot is an ordinary tool error)", calls)
			}
		})
	}
}

// Parity: framework/agent_loop/agent_loop.py:5215-5250 (goal/handoff/tasks gates)
func TestTaskCreateNewColonyRequiresGoalHandoffAndTasks(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{name: "goal", args: `{"new_colony":true,"handoff":"h","tasks":[{"subject":"s"}]}`, want: ColonyPivotGoalError},
		{name: "handoff", args: `{"new_colony":true,"goal":"g","tasks":[{"subject":"s"}]}`, want: ColonyPivotHandoffError},
		{name: "tasks", args: `{"new_colony":true,"goal":"g","handoff":"h"}`, want: ColonyPivotTasksError},
		{name: "empty tasks", args: `{"new_colony":true,"goal":"g","handoff":"h","tasks":[]}`, want: ColonyPivotTasksError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completer := &scriptedCompleter{scenarios: []scriptedResponse{
				{toolCalls: []llm.ToolCall{{ID: "t1", Name: TaskCreateToolName, Arguments: tc.args}}},
				{content: "corrected"},
			}}
			loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
				t.Fatalf("executor called for %s", req.Name)
				return ToolResult{}, nil
			})
			ctx := queenAgentContext(completer, "colony")
			conv := NewNodeConversation("You are a test assistant.")
			if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
				t.Fatalf("AddUserMessage: %v", err)
			}

			result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
			if err != nil {
				t.Fatalf("ExecuteTurn: %v", err)
			}
			record := toolResultFor(t, result, TaskCreateToolName)
			if !record.IsError || record.Content != tc.want {
				t.Fatalf("result = %#v, want %q", record, tc.want)
			}
			if calls := completer.callCount(); calls != 2 {
				t.Fatalf("model calls = %d, want 2", calls)
			}
		})
	}
}

// Parity: framework/agent_loop/agent_loop.py:5270-5330 (new_colony success stash + popup result)
func TestTaskCreateNewColonySuccessParksWithPayload(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "t1", Name: TaskCreateToolName, Arguments: `{"new_colony":true,"goal":"daily news digest","handoff":"full brief","tasks":[{"subject":"one"},{"subject":"two"}]}`}}},
		{content: "should not be requested"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		t.Fatalf("executor called for %s", req.Name)
		return ToolResult{}, nil
	})
	ctx := queenAgentContext(completer, "colony")
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if calls := completer.callCount(); calls != 1 {
		t.Fatalf("model calls = %d, want 1 (the turn parks on the popup)", calls)
	}
	pivot := result.PendingColonyPivot
	if pivot == nil {
		t.Fatal("pending pivot = nil")
	}
	if pivot["goal"] != "daily news digest" || pivot["handoff"] != "full brief" || pivot["source_phase"] != "colony" {
		t.Fatalf("pivot = %#v", pivot)
	}
	tasks, _ := pivot["tasks"].([]map[string]any)
	if len(tasks) != 2 {
		t.Fatalf("pivot tasks = %#v, want 2 object specs", pivot["tasks"])
	}
	record := toolResultFor(t, result, TaskCreateToolName)
	want := "Colony pivot popup opened (goal: daily news digest). Waiting for the user to confirm a " +
		"slug and create the new colony, or dismiss. If they confirm, the new colony is spawned with " +
		"this handoff + task plan and they navigate there; you stay here idle on this colony's " +
		"existing plan. If they dismiss, you'll receive a follow-up message instructing you to call " +
		"ask_user for explicit direction. End your turn now and wait for the user."
	if record.IsError || record.Content != want {
		t.Fatalf("result = %#v, want the synthetic pivot message", record)
	}
	if result.InnerTurns != 1 {
		t.Fatalf("inner turns = %d, want 1", result.InnerTurns)
	}
}

// Parity: framework/agent_loop/agent_loop.py:5272-5295 (pivot_payload_sink veto)
func TestTaskCreateNewColonySinkVetoSkipsThePopup(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "t1", Name: TaskCreateToolName, Arguments: `{"new_colony":true,"goal":"g","handoff":"h","tasks":[{"subject":"s"}]}`}}},
		{content: "corrected"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		t.Fatalf("executor called for %s", req.Name)
		return ToolResult{}, nil
	})
	ctx := queenAgentContext(completer, "colony")
	ctx.PivotPayloadSink = func(payload map[string]any) string {
		if payload["goal"] != "g" {
			t.Fatalf("sink payload = %#v", payload)
		}
		return "this session was just forked; do not pivot again"
	}
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	record := toolResultFor(t, result, TaskCreateToolName)
	if !record.IsError || record.Content != "ERROR: this session was just forked; do not pivot again" {
		t.Fatalf("result = %#v, want the sink veto", record)
	}
	if result.PendingColonyPivot != nil {
		t.Fatalf("pending pivot = %#v, want none", result.PendingColonyPivot)
	}
	if calls := completer.callCount(); calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
}

// Parity-exempt: guards the intercept boundary — a task_create without the
// new_colony argument is an ordinary tool call and must reach the executor.
func TestTaskCreateWithoutNewColonyStillExecutes(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "t1", Name: TaskCreateToolName, Arguments: `{"goal":"g","tasks":[{"subject":"s"}]}`}}},
		{content: "done"},
	}}
	executed := 0
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, func(req ToolCallRequest) (ToolResult, error) {
		executed++
		return ToolResult{Content: `{"success":true,"task_ids":[1]}`}, nil
	})
	ctx := queenAgentContext(completer, "independent")
	conv := NewNodeConversation("You are a test assistant.")
	if _, err := conv.AddUserMessage("start", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if executed != 1 {
		t.Fatalf("executor calls = %d, want 1", executed)
	}
	if result.PendingColonyPivot != nil || result.PendingColonySuggestion != nil {
		t.Fatalf("turn parked on a colony payload unexpectedly: %#v", result)
	}
	if !strings.Contains(result.FinalText, "done") {
		t.Fatalf("final text = %q", result.FinalText)
	}
}

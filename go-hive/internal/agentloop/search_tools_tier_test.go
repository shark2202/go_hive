package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

func workerTierTestState() *tools.ToolTierState {
	state := &tools.ToolTierState{
		Pool: []tools.Tool{
			{Name: "browser_interact", Description: "interact with a browser"},
			{Name: "browser_upload", Description: "upload a file"},
			{Name: "send_email", Description: "send an email"},
		},
		AlwaysEnabledNames: map[string]struct{}{"browser_interact": {}},
		GateableNames:      map[string]struct{}{"browser_upload": {}, "send_email": {}},
	}
	state.Rebuild()
	return state
}

func workerTierTestContext(state *tools.ToolTierState, completer llm.Completer) *AgentContext {
	available := make([]llm.ToolDef, 0, len(state.Pool))
	for _, tool := range state.Pool {
		available = append(available, llm.ToolDef{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	spec := NewAgentSpec("worker", "Worker", "tiered worker")
	return &AgentContext{
		AgentID:        "worker",
		AgentSpec:      spec,
		LLM:            completer,
		AvailableTools: available,
		StreamID:       "worker:w1",
		InputData:      map[string]any{"task": "do the thing"},
		ToolTierState:  state,
		SearchableToolsProvider: func() []Tool {
			out := []Tool{}
			for _, tool := range state.GetSearchableTools() {
				out = append(out, Tool{Name: tool.Name, Description: tool.Description})
			}
			return out
		},
		LoadedToolNamesProvider: func() []string {
			return append([]string(nil), state.LoadedToolNames...)
		},
		DynamicToolsProvider: func() []llm.ToolDef {
			out := []llm.ToolDef{}
			for _, tool := range state.GetCurrentTools() {
				out = append(out, llm.ToolDef{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
			}
			return out
		},
	}
}

func availableToolDefs(state *tools.ToolTierState) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(state.Pool))
	for _, tool := range state.Pool {
		out = append(out, llm.ToolDef{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return out
}

// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_select_promotes_and_reports
func TestWorkerTierSearchToolsLoadsDeferredTool(t *testing.T) {
	state := workerTierTestState()
	var executed []string
	executor := func(req ToolCallRequest) (ToolResult, error) {
		executed = append(executed, req.Name)
		return ToolResult{Content: "ok"}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "search_tools", Arguments: `{"query": "select:browser_upload"}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: "browser_upload", Arguments: `{}`}}},
		{content: "done"},
	}}
	ctx := workerTierTestContext(state, completer)
	conv := NewNodeConversation("You are a worker.")
	loop := NewAgentLoop(DefaultLoopConfig(), executor)
	// Isolate the tier assertions from the independent response-stall guard:
	// this script deliberately yields the same terminal text more than once.
	loop.Config.StallDetectionThreshold = 999

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, availableToolDefs(state))
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn failed: %+v", result)
	}
	first := toolNames(completer.request(0).Tools)
	if !containsName(first, "search_tools") {
		t.Fatalf("first request tools = %v; tiered worker was not given search_tools", first)
	}
	if containsName(first, "browser_upload") {
		t.Fatalf("first request tools = %v; deferred tool leaked before loading", first)
	}
	second := toolNames(completer.request(1).Tools)
	if !containsName(second, "browser_upload") {
		t.Fatalf("second request tools = %v; search_tools load did not reach the model", second)
	}
	if !containsName(executed, "browser_upload") {
		t.Fatalf("executed tools = %v, want browser_upload after promotion", executed)
	}
	if len(state.LoadedToolNames) != 1 || state.LoadedToolNames[0] != "browser_upload" {
		t.Fatalf("loaded tools = %v, want [browser_upload]", state.LoadedToolNames)
	}
}

// Parity-exempt: upstream refuses unloaded tools in AgentLoop._execute_tool, but no dedicated Python test covers the refusal branch.
func TestWorkerTierBlocksUnloadedSearchableTool(t *testing.T) {
	state := workerTierTestState()
	executed := []string{}
	executor := func(req ToolCallRequest) (ToolResult, error) {
		executed = append(executed, req.Name)
		return ToolResult{Content: "ok"}, nil
	}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "send_email", Arguments: `{}`}}},
		{content: "done"},
	}}
	ctx := workerTierTestContext(state, completer)
	conv := NewNodeConversation("You are a worker.")
	loop := NewAgentLoop(DefaultLoopConfig(), executor)
	// Isolate the tier assertions from the independent response-stall guard:
	// this script deliberately yields the same terminal text more than once.
	loop.Config.StallDetectionThreshold = 999

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, availableToolDefs(state)); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	for _, name := range executed {
		if name == "send_email" {
			t.Fatalf("unloaded searchable tool reached the executor: %v", executed)
		}
	}
	found := false
	for _, message := range conv.Messages() {
		if message.Role == RoleTool && strings.Contains(message.Content, "not loaded yet") && strings.Contains(message.Content, "select:send_email") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no refusal message recorded; messages = %#v", conv.Messages())
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestDynamicRefreshSynthetics::test_provider_supplied_search_tools_not_duplicated
func TestUntieredContextHasNoSearchTools(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	ctx := &AgentContext{
		AgentID:        "plain",
		AgentSpec:      NewAgentSpec("plain", "Plain", "no tier"),
		LLM:            completer,
		AvailableTools: []llm.ToolDef{{Name: "terminal_exec", Parameters: map[string]any{"type": "object"}}},
		StreamID:       "judge",
	}
	conv := NewNodeConversation("You are an agent.")
	if _, err := NewAgentLoop(DefaultLoopConfig(), func(ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	}).ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if names := toolNames(completer.request(0).Tools); containsName(names, "search_tools") {
		t.Fatalf("untiered request tools = %v; search_tools must not appear", names)
	}
}

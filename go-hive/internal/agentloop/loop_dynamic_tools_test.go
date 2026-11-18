package agentloop

import (
	"context"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity: core/tests/test_dynamic_tool_refresh_midturn.py::_phase_state
type fakePhaseState struct {
	mu         sync.Mutex
	eager      []llm.ToolDef
	searchable []llm.ToolDef
	loaded     map[string]bool
}

func newFakePhaseState() *fakePhaseState {
	return &fakePhaseState{
		eager: []llm.ToolDef{
			{Name: "search_tools", Description: "load tools", Parameters: map[string]any{"type": "object"}},
			{Name: "terminal_exec", Description: "run shell", Parameters: map[string]any{"type": "object"}},
		},
		searchable: []llm.ToolDef{
			{Name: "chart_render", Description: "render a chart to PNG", Parameters: map[string]any{"type": "object"}},
		},
		loaded: map[string]bool{},
	}
}

func (p *fakePhaseState) CurrentTools() []llm.ToolDef {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]llm.ToolDef(nil), p.eager...)
	for _, tool := range p.searchable {
		if p.loaded[tool.Name] {
			out = append(out, tool)
		}
	}
	return out
}

func (p *fakePhaseState) Promote(names []string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	promoted := 0
	for _, name := range names {
		for _, tool := range p.searchable {
			if tool.Name == name && !p.loaded[name] {
				p.loaded[name] = true
				promoted++
			}
		}
	}
	return promoted
}

func toolNames(tools []llm.ToolDef) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// Parity: core/tests/test_dynamic_tool_refresh_midturn.py::test_search_tools_load_reaches_model_same_turn_loop
func TestSearchToolsLoadReachesModelSameTurnLoop(t *testing.T) {
	phase := newFakePhaseState()

	executor := func(req ToolCallRequest) (ToolResult, error) {
		if req.Name == "search_tools" {
			promoted := phase.Promote([]string{"chart_render"})
			return ToolResult{Content: "loaded " + itoa(promoted)}, nil
		}
		return ToolResult{Content: "ok"}, nil
	}

	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "c1", Name: "search_tools", Arguments: `{"query": "select:chart_render"}`}}},
		{toolCalls: []llm.ToolCall{{ID: "c2", Name: "terminal_exec", Arguments: `{"command": "echo prep"}`}}},
		{content: "done"},
	}}
	ctx := &AgentContext{
		AgentID:              "midturn_agent",
		AgentSpec:            NewAgentSpec("midturn_agent", "Mid-turn Agent", "dynamic tool refresh"),
		InputData:            map[string]any{"task": "chart the data"},
		LLM:                  completer,
		AvailableTools:       phase.CurrentTools(),
		StreamID:             "judge",
		DynamicToolsProvider: phase.CurrentTools,
	}
	conv := NewNodeConversation("You are a test assistant.")

	result, err := NewAgentLoop(LoopConfig{MaxIterations: 3}, executor).
		ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}

	if completer.callCount() < 2 {
		t.Fatalf("model was streamed %d times, want at least 2", completer.callCount())
	}
	first := toolNames(completer.request(0).Tools)
	if containsName(first, "chart_render") {
		t.Fatalf("stream 1 tools = %v; chart_render should not be loaded yet", first)
	}
	second := toolNames(completer.request(1).Tools)
	if !containsName(second, "chart_render") {
		t.Fatalf("stream 2 tools = %v; the mid-turn load did not reach the model", second)
	}
	if !containsName(second, "terminal_exec") {
		t.Fatalf("stream 2 tools = %v; eager tool terminal_exec disappeared", second)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// Parity: core/tests/test_dynamic_tool_refresh_midturn.py::test_refresh_dynamic_tools_in_place_and_guarded
func TestRefreshDynamicToolsInPlaceAndGuarded(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{}, nil)

	base := []llm.ToolDef{{Name: "a"}}
	refreshed := loop.RefreshDynamicTools(&AgentContext{}, base)
	if got := toolNames(refreshed); len(got) != 1 || got[0] != "a" {
		t.Fatalf("no-provider refresh = %v, want [a]", got)
	}

	phase := newFakePhaseState()
	synthetic := append([]llm.ToolDef{{Name: "ask_user", Description: "synthetic", Parameters: map[string]any{}}}, phase.CurrentTools()...)
	ctx := &AgentContext{DynamicToolsProvider: phase.CurrentTools}

	refreshed = loop.RefreshDynamicTools(ctx, synthetic)
	names := toolNames(refreshed)
	if !containsName(names, "ask_user") {
		t.Fatalf("refresh dropped the synthetic tool: %v", names)
	}
	if containsName(names, "chart_render") {
		t.Fatalf("refresh surfaced chart_render before it was loaded: %v", names)
	}

	phase.Promote([]string{"chart_render"})
	refreshed = loop.RefreshDynamicTools(ctx, synthetic)
	names = toolNames(refreshed)
	if !containsName(names, "chart_render") {
		t.Fatalf("refresh missed the load: %v", names)
	}
	if !containsName(names, "ask_user") {
		t.Fatalf("refresh dropped the synthetic tool after a load: %v", names)
	}
}

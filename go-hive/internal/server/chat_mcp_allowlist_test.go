package server

import (
	"context"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity-exempt: Go-side session catalogue filter. The chat queen's tool list
// is built before a colony attaches, so the allowlist has to apply per turn.
func TestChatRuntimeMCPAllowlistFiltersCatalogueAndExecution(t *testing.T) {
	var executed []string
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &fakeChatLLM{},
		Tools: []llm.ToolDef{
			{Name: "mcp__files__read"},
			{Name: "mcp__files__write"},
			{Name: "read_file"},
		},
		Execute: func(_ context.Context, name string, _ map[string]any) (string, bool, error) {
			executed = append(executed, name)
			return "ok", false, nil
		},
		Root:      t.TempDir(),
		SessionID: "s-mcp-allowlist",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{
		EventBus: eventbus.NewEventBus(),
		StreamID: "colony_mcp_allowlist",
		MCPToolNames: map[string]struct{}{
			"mcp__files__read":  {},
			"mcp__files__write": {},
		},
	})
	colonyRuntime.SetToolAllowlist([]string{"mcp__files__read"}, nil)
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})

	names := queenToolNameSet(runtime.toolsForTurn())
	if !names["mcp__files__read"] || !names["read_file"] {
		t.Fatalf("tools = %v, want enabled MCP + non-MCP tools", names)
	}
	if names["mcp__files__write"] {
		t.Fatalf("tools = %v, blocked MCP tool is still advertised", names)
	}

	blocked, err := runtime.executeTool(agentloop.ToolCallRequest{Name: "mcp__files__write"})
	if err != nil {
		t.Fatalf("executeTool blocked: %v", err)
	}
	if !blocked.IsError {
		t.Fatalf("blocked MCP tool result = %#v, want an error result", blocked)
	}
	if len(executed) != 0 {
		t.Fatalf("executor ran blocked tool: %v", executed)
	}

	allowed, err := runtime.executeTool(agentloop.ToolCallRequest{Name: "mcp__files__read"})
	if err != nil || allowed.IsError {
		t.Fatalf("allowed MCP tool = %#v err=%v", allowed, err)
	}
	if len(executed) != 1 || executed[0] != "mcp__files__read" {
		t.Fatalf("executor calls = %v, want the enabled MCP tool", executed)
	}
}

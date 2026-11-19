package colony

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity-exempt: Go-side worker tool pool. With the MCP name set populated the
// worker surface drops MCP tools the colony allowlist did not enable, while
// non-MCP tools keep their previous visibility.
func TestWorkerToolPoolHonoursMCPAllowlist(t *testing.T) {
	spec := testAgentSpec()
	// The worker definition explicitly asks for the blocked MCP tool: the
	// allowlist must also filter the declared pool, not only the base list.
	spec.Tools = []string{"mcp__files__write"}
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   spec,
		StoragePath: t.TempDir(),
		StreamID:    "colony_worker_allowlist",
		Tools: []llm.ToolDef{
			{Name: "mcp__files__read"},
			{Name: "mcp__files__write"},
			{Name: "read_file"},
		},
		DeclaredTools: []llm.ToolDef{
			{Name: "mcp__files__read"},
			{Name: "mcp__files__write"},
		},
		MCPToolNames: map[string]struct{}{
			"mcp__files__read":  {},
			"mcp__files__write": {},
		},
	})
	colony.SetToolAllowlist([]string{"mcp__files__read"}, nil)

	worker := NewWorker("w1", "inspect", "worker:w1")
	names := toolDefNameSet(colony.workerTools(worker))
	if _, ok := names["mcp__files__read"]; !ok {
		t.Fatalf("worker tools = %v, want the enabled MCP tool", names)
	}
	if _, ok := names["mcp__files__write"]; ok {
		t.Fatalf("worker tools = %v, blocked MCP tool is still advertised", names)
	}
	if _, ok := names["read_file"]; !ok {
		t.Fatalf("worker tools = %v, non-MCP tool was filtered", names)
	}
}

// Parity-exempt: no allowlist keeps every loaded MCP tool visible.
func TestWorkerToolPoolOpenWithoutAllowlist(t *testing.T) {
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_worker_open",
		Tools:       []llm.ToolDef{{Name: "mcp__files__read"}, {Name: "mcp__files__write"}},
		MCPToolNames: map[string]struct{}{
			"mcp__files__read":  {},
			"mcp__files__write": {},
		},
	})
	worker := NewWorker("w1", "inspect", "worker:w1")
	names := toolDefNameSet(colony.workerTools(worker))
	if _, ok := names["mcp__files__read"]; !ok {
		t.Fatalf("worker tools = %v, want open MCP surface", names)
	}
	if _, ok := names["mcp__files__write"]; !ok {
		t.Fatalf("worker tools = %v, want open MCP surface", names)
	}
}

package colony

import (
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/mcpregistry"
	"github.com/shark2202/go_hive/go-hive/internal/pipeline"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

type emptyMCPResolver struct{}

func (emptyMCPResolver) ResolveForAgent(mcpregistry.ResolveOptions) ([]*mcpregistry.ServerConfig, error) {
	return nil, nil
}

type fixedCredentialAdapter struct {
	accounts []map[string]any
}

func (f fixedCredentialAdapter) GetAllAccountInfo() []map[string]any { return f.accounts }

// Parity: framework/host/colony_runtime.py::ColonyRuntime._apply_pipeline_results
// The MCP stage's tool registry becomes the worker tool surface: tools the
// stage (or its injected registry) carries are advertised and callable, and the
// runtime's original tools survive through the seeded base registrations.
func TestMCPRegistryStageFeedsWorkerToolSurface(t *testing.T) {
	stage := &pipeline.MCPRegistryStage{
		BaseStage:    pipeline.BaseStage{StageName: "mcp_registry", StageOrder: 50},
		Resolver:     emptyMCPResolver{},
		ToolRegistry: tools.NewToolRegistry(),
	}
	stage.ToolRegistry.Register("mcp_tool", tools.Tool{Name: "mcp_tool", Description: "Loaded MCP tool."}, func(map[string]any) (any, error) {
		return "mcp-ok", nil
	})

	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		Tools: []llm.ToolDef{{
			Name:        "base_tool",
			Description: "Base tool.",
		}},
		ToolExecutor: func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "base-ok:" + request.Name}, nil
		},
		EventBus:       eventbus.NewEventBus(),
		StreamID:       "pipeline_colony",
		PipelineStages: []pipeline.Stage{stage},
	})
	if err := runtime.initializePipeline(); err != nil {
		t.Fatalf("initialize pipeline: %v", err)
	}

	surface := map[string]bool{}
	for _, def := range runtime.baseWorkerTools() {
		surface[def.Name] = true
	}
	if !surface["mcp_tool"] {
		t.Fatalf("worker surface = %v, want the MCP stage's tool", surface)
	}
	if !surface["base_tool"] {
		t.Fatalf("worker surface = %v, want the runtime's base tool preserved", surface)
	}

	result, err := runtime.toolExec(agentloop.ToolCallRequest{Name: "mcp_tool", Input: map[string]any{}})
	if err != nil {
		t.Fatalf("mcp_tool execution: %v", err)
	}
	if result.Content != "mcp-ok" {
		t.Fatalf("mcp_tool result = %q", result.Content)
	}

	result, err = runtime.toolExec(agentloop.ToolCallRequest{Name: "base_tool", Input: map[string]any{}})
	if err != nil {
		t.Fatalf("base_tool execution: %v", err)
	}
	if result.Content != "base-ok:base_tool" {
		t.Fatalf("base_tool result = %q", result.Content)
	}
}

// Parity: framework/host/colony_runtime.py::_apply_pipeline_results
// (accounts_prompt flows from the credential stage into the worker context)
func TestCredentialStageAccountsPromptReachesWorkerContext(t *testing.T) {
	stage := &pipeline.CredentialResolverStage{
		BaseStage: pipeline.BaseStage{StageName: "credential_resolver", StageOrder: 40},
		Adapter: fixedCredentialAdapter{accounts: []map[string]any{
			{"provider": "github", "alias": "work"},
		}},
	}
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus:       eventbus.NewEventBus(),
		StreamID:       "pipeline_colony",
		PipelineStages: []pipeline.Stage{stage},
	})
	if err := runtime.initializePipeline(); err != nil {
		t.Fatalf("initialize pipeline: %v", err)
	}
	if !strings.HasPrefix(runtime.accountsPrompt, "# Connected integrations") {
		t.Fatalf("runtime accounts prompt = %q", runtime.accountsPrompt)
	}

	ctx := runtime.buildContext(&Worker{StreamID: "worker:1"}, testAgentSpec())
	if ctx.AccountsPrompt != runtime.accountsPrompt {
		t.Fatalf("worker context prompt = %q, want %q", ctx.AccountsPrompt, runtime.accountsPrompt)
	}
}

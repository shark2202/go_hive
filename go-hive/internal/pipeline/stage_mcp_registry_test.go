package pipeline

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/mcpregistry"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

type fakeMCPResolver struct {
	calls []mcpregistry.ResolveOptions
}

func (f *fakeMCPResolver) ResolveForAgent(opts mcpregistry.ResolveOptions) ([]*mcpregistry.ServerConfig, error) {
	f.calls = append(f.calls, opts)
	return nil, nil
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// declared server refs are resolved first, then files-tools is ensured when
// the registry has not loaded it.
func TestMCPRegistryResolvesServerRefsThenFilesTools(t *testing.T) {
	resolver := &fakeMCPResolver{}
	stage := &MCPRegistryStage{
		BaseStage:    BaseStage{StageName: "mcp_registry", StageOrder: 50},
		ServerRefs:   []string{"github"},
		Resolver:     resolver,
		ToolRegistry: tools.NewToolRegistry(),
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	// The resolved refs produced no configs, so the fallback runs too — the
	// same chain upstream follows (mcp_loaded only flips on a non-empty load).
	if len(resolver.calls) != 3 {
		t.Fatalf("resolve calls = %+v, want refs, fallback, files-tools", resolver.calls)
	}
	if !reflect.DeepEqual(resolver.calls[0].Include, []string{"github"}) {
		t.Fatalf("first resolve = %+v", resolver.calls[0])
	}
	if resolver.calls[1].Profile != "all" {
		t.Fatalf("second resolve = %+v, want profile=all fallback", resolver.calls[1])
	}
	if !reflect.DeepEqual(resolver.calls[2].Include, []string{filesMCPServerName}) {
		t.Fatalf("third resolve = %+v", resolver.calls[2])
	}
	if stage.ToolRegistry == nil {
		t.Fatal("tool registry output not installed")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; without
// refs or a legacy mcp_servers.json the stage falls back to every installed
// server, then ensures files-tools.
func TestMCPRegistryFallsBackToAllInstalledServers(t *testing.T) {
	resolver := &fakeMCPResolver{}
	stage := &MCPRegistryStage{
		BaseStage:    BaseStage{StageName: "mcp_registry", StageOrder: 50},
		Resolver:     resolver,
		ToolRegistry: tools.NewToolRegistry(),
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if len(resolver.calls) != 2 {
		t.Fatalf("resolve calls = %+v, want fallback then files-tools", resolver.calls)
	}
	if resolver.calls[0].Profile != "all" {
		t.Fatalf("first resolve = %+v, want profile=all", resolver.calls[0])
	}
	if !reflect.DeepEqual(resolver.calls[1].Include, []string{filesMCPServerName}) {
		t.Fatalf("second resolve = %+v", resolver.calls[1])
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a legacy
// mcp_servers.json next to agent_path short-circuits the registry fallback.
func TestMCPRegistryLegacyAgentConfigShortCircuitsFallback(t *testing.T) {
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "mcp_servers.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write mcp_servers.json: %v", err)
	}
	resolver := &fakeMCPResolver{}
	stage := &MCPRegistryStage{
		BaseStage:    BaseStage{StageName: "mcp_registry", StageOrder: 50},
		AgentPath:    agentDir,
		Resolver:     resolver,
		ToolRegistry: tools.NewToolRegistry(),
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if len(resolver.calls) != 1 || !reflect.DeepEqual(resolver.calls[0].Include, []string{filesMCPServerName}) {
		t.Fatalf("resolve calls = %+v, want only the files-tools ensure", resolver.calls)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline;
// server_refs accepts name objects (upstream `ref["name"]`) and plain names.
func TestMCPRegistryConfigParsing(t *testing.T) {
	stage, err := BuildStage(map[string]any{
		"type": "mcp_registry",
		"config": map[string]any{
			"agent_path": "/tmp/agent",
			"server_refs": []any{
				map[string]any{"name": "github"},
				"slack",
				map[string]any{},
			},
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	registry := stage.(*MCPRegistryStage)
	if registry.AgentPath != "/tmp/agent" {
		t.Fatalf("agent path = %q", registry.AgentPath)
	}
	if !reflect.DeepEqual(registry.ServerRefs, []string{"github", "slack"}) {
		t.Fatalf("server refs = %v", registry.ServerRefs)
	}
	if _, err := BuildStage(map[string]any{
		"type":   "mcp_registry",
		"config": map[string]any{"server_refs": "github"},
	}); err == nil {
		t.Fatal("non-list server_refs must fail")
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._apply_pipeline_results
// (the runtime adopts the registry's tools in registration order)
func TestToolDefsFromRegistryPreservesRegistrationOrder(t *testing.T) {
	registry := tools.NewToolRegistry()
	registry.Register("first_tool", tools.Tool{Name: "first_tool", Description: "First."}, nil)
	registry.Register("second_tool", tools.Tool{Name: "second_tool", Description: "Second."}, nil)

	defs := ToolDefsFromRegistry(registry)
	if len(defs) != 2 {
		t.Fatalf("defs = %+v, want 2", defs)
	}
	if defs[0].Name != "first_tool" || defs[1].Name != "second_tool" {
		t.Fatalf("def order = %q, %q", defs[0].Name, defs[1].Name)
	}
	if defs[0].Description != "First." {
		t.Fatalf("description = %q", defs[0].Description)
	}
	if got := ToolDefsFromRegistry(nil); got != nil {
		t.Fatalf("nil registry defs = %+v", got)
	}
}

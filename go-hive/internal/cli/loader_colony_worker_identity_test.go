package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// Parity: tests/test_worker_definition.py::TestSystemPrompt::test_is_identity_free
func TestForkedWorkerJSONCarriesWorkerIdentityNotQueenPersona(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("forked")
	task := "ship the quarterly report"
	params := hiveserver.CreateColonyFromSourceParams{
		SourceSessionID: "session_src",
		ColonyID:        "forked",
		InitialPrompt:   &task,
	}

	if _, err := writeColonyFromSession(colonyPath, "forked", "queen_alpha", nil, params); err != nil {
		t.Fatalf("writeColonyFromSession: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(colonyPath, "worker.json"))
	if err != nil {
		t.Fatalf("read forked worker.json: %v", err)
	}
	worker := map[string]any{}
	if err := json.Unmarshal(data, &worker); err != nil {
		t.Fatalf("decode forked worker.json: %v", err)
	}

	prompt, _ := worker["system_prompt"].(string)
	if want := colony.BuildSystemPrompt(&task); prompt != want {
		t.Fatalf("forked system_prompt is not the worker definition's prompt:\n got %q\nwant %q", prompt, want)
	}
	if !strings.Contains(prompt, "focused worker agent") || !strings.Contains(prompt, "report_to_parent") {
		t.Fatalf("forked system_prompt lost the worker identity: %q", prompt)
	}
	for _, marker := range []string{"You are the queen", "queen of this colony", "queen_alpha"} {
		if strings.Contains(prompt, marker) {
			t.Fatalf("forked system_prompt still carries the queen persona (%q): %q", marker, prompt)
		}
	}

	goal, _ := worker["goal"].(map[string]any)
	if goal["description"] != task {
		t.Fatalf("forked goal.description = %v, want the handoff task %q", goal["description"], task)
	}
	if tools, ok := worker["tools"].([]any); !ok || tools == nil {
		t.Fatalf("forked worker.json tools = %v, want a list", worker["tools"])
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_workers_inherit_only_queens_tools
func TestColonyWorkerToolSurfaceCarriesDeclaredIntegrationTools(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("tools")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}

	base, declared, execute := colonyWorkerToolSurface(colonyPath, builtin.New(colonyPath), home)
	if !toolDefNamed(base, "read_file") {
		t.Fatalf("base surface %v lost the builtin read_file", toolDefNames(base))
	}
	if toolDefNamed(base, "csv") {
		t.Fatal("base surface must stay builtin-only; declared tools belong to the pool")
	}
	if !toolDefNamed(declared, "csv") {
		t.Fatalf("declared-tool pool is missing the integration catalogue's csv tool (%d defs)", len(declared))
	}
	if execute == nil {
		t.Fatal("declared-tool executor is nil")
	}

	spec := agentloop.AgentSpec{
		ID:               "worker",
		Name:             "Worker",
		AgentType:        "event_loop",
		ToolAccessPolicy: "explicit",
		Tools:            []string{"csv"},
	}
	host, err := colonyhost.New(colonyhost.Config{
		ColonyID:      "tools",
		AgentSpec:     spec,
		StoragePath:   colonyPath,
		LLM:           &lifecycleTestLLM{},
		Tools:         base,
		DeclaredTools: declared,
		ToolExecutor:  execute,
	})
	if err != nil {
		t.Fatalf("colony host: %v", err)
	}
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("host start: %v", err)
	}
	t.Cleanup(host.Stop)

	worker := host.Runtime().BuildWorker(colony.WorkerBuildOptions{WorkerID: "w1", Task: "export rows"})
	if worker == nil || worker.Context == nil {
		t.Fatal("BuildWorker returned no context")
	}
	names := toolDefNames(worker.Context.AvailableTools)
	if !toolDefNamed(worker.Context.AvailableTools, "csv") {
		t.Fatalf("worker start surface %v is missing the declared integration tool csv", names)
	}
	if !toolDefNamed(worker.Context.AvailableTools, "read_file") {
		t.Fatalf("worker start surface %v lost the builtin base", names)
	}
}

// Parity: tests/test_worker_definition.py::TestSystemPrompt::test_is_identity_free
func TestForkedColonyWorkerRunsWorkerIdentity(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("forked")
	task := "compile the weekly digest"
	if _, err := writeColonyFromSession(colonyPath, "forked", "queen_alpha", nil,
		hiveserver.CreateColonyFromSourceParams{SourceSessionID: "session_src", ColonyID: "forked", InitialPrompt: &task}); err != nil {
		t.Fatalf("writeColonyFromSession: %v", err)
	}
	spec, err := agents.LoadAgentSpec(colonyPath)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	fields := spec.HostFields()
	toolbox := builtin.New(colonyPath)
	base, declared, execute := colonyWorkerToolSurface(colonyPath, toolbox, home)
	provider := &promptCaptureLLM{}
	host, err := colonyhost.New(colonyhost.Config{
		ColonyID:      "forked",
		AgentSpec:     fields.AgentSpec,
		Goal:          fields.Goal,
		StoragePath:   colonyPath,
		LLM:           provider,
		Tools:         base,
		DeclaredTools: declared,
		ToolExecutor:  execute,
	})
	if err != nil {
		t.Fatalf("colony host: %v", err)
	}
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("host start: %v", err)
	}
	t.Cleanup(host.Stop)

	if _, err := host.Runtime().RunBatch(context.Background(), []colony.BatchTask{{Task: task}}, colony.RunSpawnOptions{}); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	system := provider.firstSystem()
	if !strings.Contains(system, "focused worker agent") || !strings.Contains(system, task) {
		t.Fatalf("forked worker ran without its worker identity/task: %q", system)
	}
	if strings.Contains(system, "queen_alpha") || strings.Contains(system, "You are the queen") {
		t.Fatalf("forked worker ran under the queen persona: %q", system)
	}
}

type promptCaptureLLM struct {
	systems []string
}

func (p *promptCaptureLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	p.systems = append(p.systems, req.System)
	return llm.Completion{Content: "done", StopReason: "stop"}, nil
}

func (p *promptCaptureLLM) firstSystem() string {
	if len(p.systems) == 0 {
		return ""
	}
	return p.systems[0]
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_workers_inherit_only_queens_tools
// Parity: framework/agents/queen/worker_definition.py::build_meta (live tool snapshot)
func TestForkedWorkerDeclaredCategoriesResolveToPoolTools(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("forked")
	task := "build the supplier sheet"
	// The worker's declared tools are the source queen's live snapshot, so the
	// fixture carries a live runtime whose surface is the names the colony
	// tool pool can resolve.
	source := hiveserver.NewSession("session_src", eventbus.NewEventBus())
	source.QueenName = "queen_operations"
	liveQueen, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
		LLM: &lifecycleTestLLM{},
		Tools: []llm.ToolDef{
			{Name: "terminal_rg", Description: "search"},
			{Name: "attach_file", Description: "attach"},
		},
		Execute: func(context.Context, string, map[string]any) (string, bool, error) {
			return "ok", false, nil
		},
		Root:      t.TempDir(),
		Bus:       eventbus.NewEventBus(),
		SessionID: "session_src",
	})
	if err != nil {
		t.Fatalf("build source queen runtime: %v", err)
	}
	t.Cleanup(func() { _ = liveQueen.Close() })
	source.Chat = liveQueen
	if _, err := writeColonyFromSession(colonyPath, "forked", "queen_operations", source,
		hiveserver.CreateColonyFromSourceParams{SourceSessionID: "session_src", ColonyID: "forked", InitialPrompt: &task}); err != nil {
		t.Fatalf("writeColonyFromSession: %v", err)
	}
	spec, err := agents.LoadAgentSpec(colonyPath)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if !specContains(spec.AgentSpec.Tools, "terminal_rg") {
		t.Fatalf("forked tools = %v, want the queen's live tool snapshot", spec.AgentSpec.Tools)
	}

	base, declared, execute := colonyWorkerToolSurface(colonyPath, builtin.New(colonyPath), home)
	host, err := colonyhost.New(colonyhost.Config{
		ColonyID:      "forked",
		AgentSpec:     spec.AgentSpec,
		Goal:          spec.Goal,
		StoragePath:   colonyPath,
		LLM:           &promptCaptureLLM{},
		Tools:         base,
		DeclaredTools: declared,
		ToolExecutor:  execute,
	})
	if err != nil {
		t.Fatalf("colony host: %v", err)
	}
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("host start: %v", err)
	}
	t.Cleanup(host.Stop)

	worker := host.Runtime().BuildWorker(colony.WorkerBuildOptions{WorkerID: "w1", Task: task})
	if worker == nil || worker.Context == nil {
		t.Fatal("BuildWorker returned no context")
	}
	names := toolDefNames(worker.Context.AvailableTools)
	for _, want := range []string{"terminal_rg", "attach_file"} {
		if !toolDefNamed(worker.Context.AvailableTools, want) {
			t.Fatalf("forked worker start surface %v is missing %q from its declared categories", names, want)
		}
	}
	if toolDefNamed(worker.Context.AvailableTools, "terminal_basic") {
		t.Fatalf("category label terminal_basic was advertised as a tool: %v", names)
	}
}

func specContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func toolDefNamed(defs []llm.ToolDef, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func toolDefNames(defs []llm.ToolDef) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

// Parity-exempt: worker tool-surface block propagation is Go-only; upstream
// returns MCP content blocks and LiteLLM carries them.
func TestColonyWorkerToolSurfaceCarriesAttachFileBlocks(t *testing.T) {
	root := testTempDir(t)
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("blocks")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(colonyPath, "pic.png")
	if err := os.WriteFile(png, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, execute := colonyWorkerToolSurface(colonyPath, builtin.New(colonyPath), home)
	result, err := execute(agentloop.ToolCallRequest{Name: "attach_file", Input: map[string]any{"paths": png}})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.ImageContent) != 1 || result.ImageContent[0]["type"] != "image_url" {
		t.Fatalf("worker attach_file content blocks = %#v", result.ImageContent)
	}
}

package colony

import (
	"context"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// fakeDeclaredToolRegistry stands in for the colony host's resolved integration
// catalogue: a fixed definition pool the worker's declared names select from.
type fakeDeclaredToolRegistry struct {
	defs []llm.ToolDef
}

func (f *fakeDeclaredToolRegistry) Resolve() []llm.ToolDef {
	return append([]llm.ToolDef(nil), f.defs...)
}

func declaredToolDef(name, description string) llm.ToolDef {
	return llm.ToolDef{
		Name:        name,
		Description: description,
		Parameters:  map[string]any{"type": "object"},
	}
}

func newDeclaredToolColony(t *testing.T, provider llm.Completer, wanted []string, registry *fakeDeclaredToolRegistry) *ColonyRuntime {
	t.Helper()
	spec := testAgentSpec()
	spec.Tools = append([]string(nil), wanted...)
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   spec,
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools: []llm.ToolDef{
			declaredToolDef("read_file", "read a file"),
			declaredToolDef("write_file", "write a file"),
		},
		DeclaredTools: registry.Resolve(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "declared_tools_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_workers_inherit_only_queens_tools
func TestWorkerLoopAppliesDeclaredTools(t *testing.T) {
	registry := &fakeDeclaredToolRegistry{defs: []llm.ToolDef{
		declaredToolDef("jira_issue", "read and write Jira issues"),
		declaredToolDef("stripe_charge", "charge a card"),
	}}
	provider := newScriptedLLM(reportResponse("success", "done", map[string]any{}))
	runtime := newDeclaredToolColony(t, provider, []string{"jira_issue"}, registry)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "file the ticket"}}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Succeeded != 1 {
		t.Fatalf("succeeded = %d, want 1 (failed=%d error=%v)", result.Succeeded, result.Failed, result.Error)
	}

	worker := runtime.GetWorker(result.WorkerResults[0].WorkerID)
	if worker == nil || worker.Context == nil {
		t.Fatal("worker context missing after the run")
	}
	spawned := toolNames(worker.Context.AvailableTools)
	if !hasTool(spawned, "jira_issue") {
		t.Fatalf("worker spawn surface %v is missing the declared tool jira_issue", spawned)
	}
	if !hasTool(spawned, "read_file") {
		t.Fatalf("worker spawn surface %v dropped the builtin base", spawned)
	}

	request, ok := provider.firstCall()
	if !ok {
		t.Fatal("worker never called the model")
	}
	advertised := toolNames(request.Tools)
	if !hasTool(advertised, "jira_issue") {
		t.Fatalf("model-facing tool list %v is missing the declared tool jira_issue", advertised)
	}
	if hasTool(advertised, "stripe_charge") {
		t.Fatalf("model-facing tool list %v leaked an undeclared pool tool", advertised)
	}
	if !hasTool(advertised, "read_file") {
		t.Fatalf("model-facing tool list %v dropped the builtin base", advertised)
	}
	if !hasTool(advertised, "report_to_parent") {
		t.Fatalf("model-facing tool list %v dropped report_to_parent", advertised)
	}
}

// Parity-exempt: Go-only default-surface guard — an absent/empty worker.json
// tools list must keep the pre-existing base surface, not an empty pool.
func TestWorkerLoopUndeclaredToolsKeepDefaultSurface(t *testing.T) {
	registry := &fakeDeclaredToolRegistry{defs: []llm.ToolDef{
		declaredToolDef("jira_issue", "read and write Jira issues"),
	}}
	provider := newScriptedLLM(reportResponse("success", "done", map[string]any{}))
	runtime := newDeclaredToolColony(t, provider, nil, registry)

	result, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "plain task"}}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Succeeded != 1 {
		t.Fatalf("succeeded = %d, want 1 (failed=%d error=%v)", result.Succeeded, result.Failed, result.Error)
	}
	request, ok := provider.firstCall()
	if !ok {
		t.Fatal("worker never called the model")
	}
	advertised := toolNames(request.Tools)
	if hasTool(advertised, "jira_issue") {
		t.Fatalf("undeclared worker received pool tool jira_issue: %v", advertised)
	}
	if !hasTool(advertised, "read_file") {
		t.Fatalf("undeclared worker lost the default base surface: %v", advertised)
	}
}

// Parity-exempt: Go-only overseer gate — worker.json's declared tools belong to
// the worker, never to the colony's client-facing overseer.
func TestOverseerToolSurfaceIgnoresWorkerDeclaredTools(t *testing.T) {
	registry := &fakeDeclaredToolRegistry{defs: []llm.ToolDef{
		declaredToolDef("jira_issue", "read and write Jira issues"),
	}}
	runtime := newDeclaredToolColony(t, newScriptedLLM(), []string{"jira_issue"}, registry)

	overseer := toolNames(runtime.workerTools(&Worker{StreamID: "overseer"}))
	if hasTool(overseer, "jira_issue") {
		t.Fatalf("overseer surface %v inherited a worker-declared tool", overseer)
	}
	if !hasTool(overseer, "read_file") {
		t.Fatalf("overseer surface %v lost the colony base tools", overseer)
	}
}

// Parity-exempt: Go-only selection guard — declared names that no registry
// resolves, and queen-lifecycle names, must never reach a worker.
func TestSelectWorkerDeclaredToolsFiltersUnresolvableAndLifecycleNames(t *testing.T) {
	base := []llm.ToolDef{declaredToolDef("read_file", "builtin read")}
	declared := []llm.ToolDef{
		declaredToolDef("jira_issue", "integration"),
		declaredToolDef("read_file", "pool shadow of a builtin"),
	}

	selected := selectWorkerDeclaredTools(base, declared, []string{"jira_issue", "stale_tool", "read_file", RunWorkerToolName, "unknown_name"})
	byName := make(map[string]llm.ToolDef, len(selected))
	for _, def := range selected {
		byName[def.Name] = def
	}
	if len(byName) != 2 {
		t.Fatalf("selected = %v, want read_file + jira_issue only", byName)
	}
	if byName["read_file"].Description != "builtin read" {
		t.Fatalf("read_file = %q, builtin base must win the collision", byName["read_file"].Description)
	}
	if _, ok := byName["stale_tool"]; ok {
		t.Fatalf("unresolved declared name leaked into the surface: %v", byName)
	}
	if _, ok := byName[RunWorkerToolName]; ok {
		t.Fatalf("queen lifecycle tool %q reached a worker", RunWorkerToolName)
	}

	if got := selectWorkerDeclaredTools(base, declared, nil); len(got) != 1 || got[0].Name != "read_file" {
		t.Fatalf("empty declaration changed the base surface: %v", got)
	}
}

// Parity-exempt: Go-only category expansion — a worker.json forked from a queen
// profile declares the profile's tool categories; those labels must select the
// concrete pool tools behind them.
func TestSelectWorkerDeclaredToolsExpandsCategoryLabels(t *testing.T) {
	declared := []llm.ToolDef{
		declaredToolDef("csv_read", "read a csv"),
		declaredToolDef("csv_write", "write a csv"),
		declaredToolDef("jira_issue", "integration"),
	}

	selected := selectWorkerDeclaredTools(nil, declared, []string{"spreadsheet_advanced"})
	names := toolNames(selected)
	if !hasTool(names, "csv_read") || !hasTool(names, "csv_write") {
		t.Fatalf("category spreadsheet_advanced selected %v, want its csv tools", names)
	}
	if hasTool(names, "jira_issue") {
		t.Fatalf("category expansion leaked an unrelated pool tool: %v", names)
	}
	if hasTool(names, "spreadsheet_advanced") {
		t.Fatalf("the category label itself was advertised as a tool: %v", names)
	}
}

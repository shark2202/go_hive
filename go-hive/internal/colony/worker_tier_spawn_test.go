package colony

import (
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

func tierSpawnTools() []llm.ToolDef {
	names := []string{"browser_interact", "browser_upload", "send_email", "task_update"}
	out := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		out = append(out, llm.ToolDef{
			Name:        name,
			Description: "desc of " + name,
			Parameters:  map[string]any{"type": "object"},
		})
	}
	return out
}

func tierSpawnColony(t *testing.T, keep func() map[string]struct{}) *ColonyRuntime {
	t.Helper()
	colony := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		Tools:       tierSpawnTools(),
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus:      eventbus.NewEventBus(),
		StreamID:      "tier_test",
		WorkerKeepSet: keep,
	})
	colony.SetToolAllowlist(nil, map[string]struct{}{
		"browser_interact": {},
		"browser_upload":   {},
		"send_email":       {},
	})
	return colony
}

func toolDefNameSet(defs []llm.ToolDef) map[string]struct{} {
	out := map[string]struct{}{}
	for _, def := range defs {
		out[def.Name] = struct{}{}
	}
	return out
}

func reminderNameSet(list []agentloop.Tool) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tool := range list {
		out[tool.Name] = struct{}{}
	}
	return out
}

func toolNameSet(list []tools.Tool) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tool := range list {
		out[tool.Name] = struct{}{}
	}
	return out
}

func assertNameSet(t *testing.T, label string, got map[string]struct{}, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

// Parity: core/tests/test_worker_tier_spawn.py::test_keep_set_wires_tier_and_providers
func TestWorkerKeepSetWiresTierAndProviders(t *testing.T) {
	colony := tierSpawnColony(t, func() map[string]struct{} {
		return map[string]struct{}{"browser_interact": {}}
	})
	worker := colony.BuildWorker(WorkerBuildOptions{
		WorkerID:   "w1",
		Storage:    filepath.Join(t.TempDir(), "w1"),
		Task:       "do the thing",
		SpawnTools: tierSpawnTools(),
	})
	ctx := worker.Context
	if ctx == nil {
		t.Fatal("worker context is nil")
	}
	tier, ok := ctx.ToolTierState.(*tools.ToolTierState)
	if !ok || tier == nil {
		t.Fatalf("tool tier state = %T, want a *tools.ToolTierState", ctx.ToolTierState)
	}

	dynamic, ok := ctx.DynamicToolsProvider.(func() []llm.ToolDef)
	if !ok || dynamic == nil {
		t.Fatalf("dynamic tools provider = %T, want func() []llm.ToolDef", ctx.DynamicToolsProvider)
	}
	searchableProvider, ok := ctx.SearchableToolsProvider.(func() []agentloop.Tool)
	if !ok || searchableProvider == nil {
		t.Fatalf("searchable tools provider = %T, want func() []agentloop.Tool", ctx.SearchableToolsProvider)
	}
	loadedProvider, ok := ctx.LoadedToolNamesProvider.(func() []string)
	if !ok || loadedProvider == nil {
		t.Fatalf("loaded tool names provider = %T, want func() []string", ctx.LoadedToolNamesProvider)
	}
	if loaded := loadedProvider(); len(loaded) != 0 {
		t.Errorf("loaded_tool_names_provider() = %v, want empty", loaded)
	}

	assertNameSet(t, "eager", toolNameSet(tier.GetCurrentTools()), "browser_interact", "task_update")
	assertNameSet(t, "searchable", toolNameSet(tier.GetSearchableTools()), "browser_upload", "send_email")
	assertNameSet(t, "dynamic provider", toolDefNameSet(dynamic()), "browser_interact", "task_update")
	assertNameSet(t, "searchable provider", reminderNameSet(searchableProvider()), "browser_upload", "send_email")
}

// Parity: core/tests/test_worker_tier_spawn.py::test_empty_keep_set_is_dark
func TestWorkerEmptyKeepSetIsDark(t *testing.T) {
	colony := tierSpawnColony(t, func() map[string]struct{} {
		return map[string]struct{}{}
	})
	worker := colony.BuildWorker(WorkerBuildOptions{
		WorkerID:   "w1",
		Storage:    filepath.Join(t.TempDir(), "w1"),
		Task:       "do the thing",
		SpawnTools: tierSpawnTools(),
	})
	ctx := worker.Context
	if ctx == nil {
		t.Fatal("worker context is nil")
	}
	if ctx.ToolTierState != nil {
		t.Errorf("tool tier state = %T, want nil", ctx.ToolTierState)
	}
	if ctx.DynamicToolsProvider != nil {
		t.Errorf("dynamic tools provider = %T, want nil", ctx.DynamicToolsProvider)
	}
	if ctx.SearchableToolsProvider != nil {
		t.Errorf("searchable tools provider = %T, want nil", ctx.SearchableToolsProvider)
	}
	assertNameSet(t, "available tools", toolDefNameSet(ctx.AvailableTools),
		"browser_interact", "browser_upload", "send_email", "task_update")
}

// Parity: core/tests/test_worker_tier_spawn.py::test_preload_tools_promoted_at_spawn
func TestWorkerPreloadToolsPromotedAtSpawn(t *testing.T) {
	colony := tierSpawnColony(t, func() map[string]struct{} {
		return map[string]struct{}{"browser_interact": {}}
	})
	worker := colony.BuildWorker(WorkerBuildOptions{
		WorkerID:     "w1",
		Storage:      filepath.Join(t.TempDir(), "w1"),
		Task:         "do the thing",
		SpawnTools:   tierSpawnTools(),
		PreloadTools: []string{"send_email", "not_a_tool"},
	})
	tier, ok := worker.Context.ToolTierState.(*tools.ToolTierState)
	if !ok || tier == nil {
		t.Fatalf("tool tier state = %T, want a *tools.ToolTierState", worker.Context.ToolTierState)
	}
	if _, ok := toolNameSet(tier.GetCurrentTools())["send_email"]; !ok {
		t.Errorf("eager tools = %v, want send_email loaded", tier.CurrentToolNames())
	}
	if len(tier.LoadedToolNames) != 1 || tier.LoadedToolNames[0] != "send_email" {
		t.Errorf("loaded_tool_names = %v, want [send_email]", tier.LoadedToolNames)
	}
	for _, name := range tier.LoadedToolNames {
		if name == "not_a_tool" {
			t.Errorf("not_a_tool must not be promoted, loaded = %v", tier.LoadedToolNames)
		}
	}
}

// Parity: core/tests/test_worker_tier_spawn.py::test_loaded_tools_persist_across_rebuild
func TestWorkerLoadedToolsPersistAcrossRebuild(t *testing.T) {
	colony := tierSpawnColony(t, func() map[string]struct{} {
		return map[string]struct{}{"browser_interact": {}}
	})
	storage := filepath.Join(t.TempDir(), "w2")
	worker := colony.BuildWorker(WorkerBuildOptions{
		WorkerID:   "w2",
		Storage:    storage,
		Task:       "do the thing",
		SpawnTools: tierSpawnTools(),
	})
	tier, ok := worker.Context.ToolTierState.(*tools.ToolTierState)
	if !ok || tier == nil {
		t.Fatalf("tool tier state = %T, want a *tools.ToolTierState", worker.Context.ToolTierState)
	}
	tier.PromoteSearchedTools([]string{"browser_upload"})

	rebuilt := colony.BuildWorker(WorkerBuildOptions{
		WorkerID:   "w2",
		Storage:    storage,
		Task:       "do the thing",
		SpawnTools: tierSpawnTools(),
	})
	rebuiltTier, ok := rebuilt.Context.ToolTierState.(*tools.ToolTierState)
	if !ok || rebuiltTier == nil {
		t.Fatalf("rebuilt tool tier state = %T, want a *tools.ToolTierState", rebuilt.Context.ToolTierState)
	}
	if _, ok := toolNameSet(rebuiltTier.GetCurrentTools())["browser_upload"]; !ok {
		t.Errorf("rebuilt eager tools = %v, want browser_upload loaded", rebuiltTier.CurrentToolNames())
	}
}

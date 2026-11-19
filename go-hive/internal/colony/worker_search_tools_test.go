package colony

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
)

func workerSearchToolNames(defs []llm.ToolDef) map[string]struct{} {
	out := map[string]struct{}{}
	for _, def := range defs {
		out[def.Name] = struct{}{}
	}
	return out
}

// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_select_promotes_and_reports
func TestWorkerSearchToolsLoadsMCPToolFromTier(t *testing.T) {
	colony := tierSpawnColony(t, func() map[string]struct{} {
		return map[string]struct{}{"browser_interact": {}}
	})
	worker := colony.BuildWorker(WorkerBuildOptions{
		WorkerID:   "w1",
		Storage:    filepath.Join(t.TempDir(), "w1"),
		Task:       "upload the report",
		SpawnTools: colony.workerToolDefs(),
	})
	if worker.Context == nil {
		t.Fatal("worker context is nil")
	}
	model := newScriptedLLM(
		scriptedResponse{
			ToolCalls:  []llm.ToolCall{{ID: "c1", Name: "search_tools", Arguments: `{"query": "select:browser_upload"}`}},
			StopReason: "tool_calls",
		},
		scriptedResponse{
			ToolCalls:  []llm.ToolCall{{ID: "c2", Name: "browser_upload", Arguments: `{}`}},
			StopReason: "tool_calls",
		},
		textResponse("uploaded"),
	)
	worker.Context.LLM = model

	executed := []string{}
	baseExec := colony.executorFor(worker)
	executor := func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		executed = append(executed, req.Name)
		return baseExec(req)
	}
	conv := agentloop.NewNodeConversation("You are a worker.")
	if _, err := conv.AddUserMessage(worker.Task, agentloop.UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	loop := agentloop.NewAgentLoop(agentloop.DefaultLoopConfig(), executor)
	if _, err := loop.ExecuteTurn(context.Background(), worker.Context, conv, colony.workerTools(worker)); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}

	if model.callCount() < 2 {
		t.Fatalf("model calls = %d, want at least 2", model.callCount())
	}
	first := workerSearchToolNames(model.calls[0].Tools)
	if _, ok := first["search_tools"]; !ok {
		t.Fatalf("first tool surface = %v, want search_tools", first)
	}
	for _, deferred := range []string{"browser_upload", "send_email"} {
		if _, ok := first[deferred]; ok {
			t.Fatalf("first tool surface = %v; %s leaked before loading", first, deferred)
		}
	}
	second := workerSearchToolNames(model.calls[1].Tools)
	if _, ok := second["browser_upload"]; !ok {
		t.Fatalf("second tool surface = %v, want the loaded browser_upload", second)
	}
	found := false
	for _, name := range executed {
		if name == "browser_upload" {
			found = true
		}
	}
	if !found {
		t.Fatalf("executed tools = %v, want browser_upload", executed)
	}
	tier, ok := worker.Context.ToolTierState.(*tools.ToolTierState)
	if !ok || tier == nil {
		t.Fatalf("tool tier state = %T, want *tools.ToolTierState", worker.Context.ToolTierState)
	}
	if len(tier.LoadedToolNames) != 1 || tier.LoadedToolNames[0] != "browser_upload" {
		t.Fatalf("loaded tools = %v, want [browser_upload]", tier.LoadedToolNames)
	}
}

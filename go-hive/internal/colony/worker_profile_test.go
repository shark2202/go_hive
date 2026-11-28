package colony

// P0-20: a dispatched worker profile must actually be applied at spawn — its
// credential aliases, its Chrome browser binding, and the no-credential MCP
// tool filter — not just stamped as a label.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn
// Parity: framework/host/worker.py::Worker.run (account_overrides +
// browser-profile pre-flight)

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func writeWorkerProfileMetadata(t *testing.T, colonyDir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(colonyDir, colonyMetadataFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
}

// accountCapture records the account argument the tool executor receives.
type accountCapture struct {
	mu      sync.Mutex
	account string
	calls   int
}

func (c *accountCapture) executor(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	c.mu.Lock()
	c.calls++
	if value, ok := req.Input["account"].(string); ok {
		c.account = value
	}
	c.mu.Unlock()
	return agentloop.ToolResult{Content: "ok"}, nil
}

func (c *accountCapture) captured() (string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.account, c.calls
}

// Parity: framework/host/worker.py::Worker.run (account_overrides)
func TestWorkerProfileIntegrationsApplyAsAccountDefault(t *testing.T) {
	provider := newScriptedLLM(
		toolResponse("call_1", "slack_post", `{"text":"hello"}`),
		reportResponse("success", "posted", nil),
	)
	capture := &accountCapture{}
	colonyDir := t.TempDir()
	writeWorkerProfileMetadata(t, colonyDir, `{
		"name": "profiles",
		"worker_profiles": [
			{"name": "slack_work", "integrations": {"slack": "work"}, "browser_profile": "blue"}
		]
	}`)
	bus := eventbus.NewEventBus()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:       testAgentSpec(),
		StoragePath:     colonyDir,
		LLM:             provider,
		Tools:           []llm.ToolDef{{Name: "slack_post", Parameters: map[string]any{"type": "object", "properties": map[string]any{"account": map[string]any{"type": "string"}}}}},
		ToolExecutor:    capture.executor,
		ToolProviderMap: map[string]string{"slack_post": "slack"},
		EventBus:        bus,
		StreamID:        "profile_alias_colony",
		ConnectedBrowserProfiles: func() ([]string, error) {
			return []string{"blue"}, nil
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)

	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{
		Task:        "post the update",
		ProfileName: "slack_work",
	}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if worker.ProfileName != "slack_work" {
		t.Fatalf("worker profile = %q, want slack_work", worker.ProfileName)
	}
	if worker.Integrations["slack"] != "work" {
		t.Fatalf("worker integrations = %#v, want slack=work", worker.Integrations)
	}
	if worker.BrowserProfile != "blue" {
		t.Fatalf("worker browser profile = %q, want blue", worker.BrowserProfile)
	}
	if account, calls := capture.captured(); calls != 1 || account != "work" {
		t.Fatalf("tool executor saw account=%q calls=%d, want account=work calls=1", account, calls)
	}
}

// An explicit account on the call always beats the profile binding.
//
// Parity: tools/src/aden_tools/credentials/store_adapter.py::account_overrides
// ("An explicit non-empty alias on the call wins.")
func TestWorkerProfileExplicitAccountWins(t *testing.T) {
	provider := newScriptedLLM(
		toolResponse("call_1", "slack_post", `{"text":"hello","account":"personal"}`),
		reportResponse("success", "posted", nil),
	)
	capture := &accountCapture{}
	colonyDir := t.TempDir()
	writeWorkerProfileMetadata(t, colonyDir, `{
		"worker_profiles": [{"name": "slack_work", "integrations": {"slack": "work"}}]
	}`)
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:       testAgentSpec(),
		StoragePath:     colonyDir,
		LLM:             provider,
		Tools:           []llm.ToolDef{{Name: "slack_post"}},
		ToolExecutor:    capture.executor,
		ToolProviderMap: map[string]string{"slack_post": "slack"},
		EventBus:        eventbus.NewEventBus(),
		StreamID:        "profile_alias_explicit_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "post", ProfileName: "slack_work"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if account, _ := capture.captured(); account != "personal" {
		t.Fatalf("account = %q, want the explicit personal alias", account)
	}
}

// An undeclared profile keeps its raw name as a label but applies no binding,
// exactly like get_worker_profile returning None.
// Parity-exempt: upstream get_worker_profile returns None for an undeclared name and has no pytest for the raw-label/default-binding fallback.
func TestUnknownWorkerProfileKeepsLabelAndDefaults(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "done", nil))
	colonyDir := t.TempDir()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: colonyDir,
		LLM:         provider,
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "profile_unknown_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "do", ProfileName: "ghost"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if worker.ProfileName != "ghost" {
		t.Fatalf("profile = %q, want the raw unknown label ghost", worker.ProfileName)
	}
	if len(worker.Integrations) != 0 {
		t.Fatalf("integrations = %#v, want none for an unknown profile", worker.Integrations)
	}
	if worker.BrowserProfile != defaultBrowserProfile {
		t.Fatalf("browser profile = %q, want default", worker.BrowserProfile)
	}
}

// The batch-level profile applies when a task does not name one (spawn_batch
// precedence: per-task override, then batch default).
// Parity-exempt: spawn_batch's batch-level profile default has no pytest upstream; core/tests/test_colony_runtime_overseer.py covers only ids/reports, not profile resolution.
func TestSpawnBatchProfileDefaultApplies(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "done", nil))
	colonyDir := t.TempDir()
	writeWorkerProfileMetadata(t, colonyDir, `{
		"worker_profiles": [{"name": "batch_profile", "integrations": {"github": "bot"}}]
	}`)
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: colonyDir,
		LLM:         provider,
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "profile_batch_default_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	ids, err := runtime.SpawnBatchWithOptions(context.Background(),
		[]BatchTask{{Task: "do"}},
		SpawnBatchOptions{ProfileName: "batch_profile"})
	if err != nil {
		t.Fatalf("SpawnBatchWithOptions: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if worker.ProfileName != "batch_profile" || worker.Integrations["github"] != "bot" {
		t.Fatalf("profile/integrations = %q/%#v, want batch_profile github=bot",
			worker.ProfileName, worker.Integrations)
	}
}

// spawn_batch precedence: a task's own profile_name beats the batch default,
// and each worker carries the resolved binding of the profile it was dispatched
// with.
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
// (per-task profile_name override)
func TestSpawnBatchPerTaskProfileBeatsBatchDefault(t *testing.T) {
	provider := newScriptedLLM(
		reportResponse("success", "one done", nil),
		reportResponse("success", "two done", nil),
	)
	colonyDir := t.TempDir()
	writeWorkerProfileMetadata(t, colonyDir, `{
		"worker_profiles": [
			{"name": "task_profile", "integrations": {"github": "task-bot"}, "browser_profile": "blue"},
			{"name": "batch_profile", "integrations": {"github": "batch-bot"}}
		]
	}`)
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: colonyDir,
		LLM:         provider,
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "profile_task_override_colony",
		ConnectedBrowserProfiles: func() ([]string, error) {
			return []string{"blue"}, nil
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	ids, err := runtime.SpawnBatchWithOptions(context.Background(),
		[]BatchTask{{Task: "one", ProfileName: "task_profile"}, {Task: "two"}},
		SpawnBatchOptions{ProfileName: "batch_profile"})
	if err != nil {
		t.Fatalf("SpawnBatchWithOptions: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("SpawnBatchWithOptions returned %d ids, want 2", len(ids))
	}
	workers := []*Worker{runtime.GetWorker(ids[0]), runtime.GetWorker(ids[1])}
	for _, worker := range workers {
		if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
			t.Fatal("worker never terminated")
		}
	}
	first, second := workers[0], workers[1]
	if first.ProfileName != "task_profile" || first.Integrations["github"] != "task-bot" || first.BrowserProfile != "blue" {
		t.Fatalf("task-profile worker = %q/%#v/%q, want task_profile/task-bot/blue",
			first.ProfileName, first.Integrations, first.BrowserProfile)
	}
	if second.ProfileName != "batch_profile" || second.Integrations["github"] != "batch-bot" || second.BrowserProfile != defaultBrowserProfile {
		t.Fatalf("batch-default worker = %q/%#v/%q, want batch_profile/batch-bot/default",
			second.ProfileName, second.Integrations, second.BrowserProfile)
	}
}

// Parity: framework/credentials/validation.py::compute_unavailable_mcp_tools
func TestSpawnFiltersMCPToolsWithoutLiveCredentials(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "done", nil))
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools: []llm.ToolDef{
			{Name: "mcp__github__issues"},
			{Name: "mcp__slack__post"},
			{Name: "echo"},
		},
		MCPToolNames: map[string]struct{}{
			"mcp__github__issues": {},
			"mcp__slack__post":    {},
		},
		ToolProviderMap: map[string]string{
			"mcp__github__issues": "github",
			"mcp__slack__post":    "slack",
		},
		CredentialAccounts: func() []map[string]any {
			return []map[string]any{{"provider": "github", "alias": "bot"}}
		},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "profile_mcp_filter_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "do"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if worker.Context == nil {
		t.Fatal("worker context missing")
	}
	contextTools := toolDefNameSet(worker.Context.AvailableTools)
	if _, ok := contextTools["mcp__github__issues"]; !ok {
		t.Fatalf("spawn tools = %v, want the credentialed github tool", contextTools)
	}
	if _, ok := contextTools["mcp__slack__post"]; ok {
		t.Fatalf("spawn tools = %v, slack tool survived without a live account", contextTools)
	}
	loopTools := toolDefNameSet(runtime.workerTools(worker))
	if _, ok := loopTools["mcp__slack__post"]; ok {
		t.Fatalf("worker tools = %v, slack tool survived without a live account", loopTools)
	}
	// A name with no provider mapping is never filtered.
	if _, ok := loopTools["echo"]; !ok {
		t.Fatalf("worker tools = %v, uncredentialed echo was dropped", loopTools)
	}
}

// A nil account view degrades to "no filter", upstream's adapter-unavailable
// path, instead of dropping every credentialed tool.
// Parity-exempt: the nil credential-view no-op is the Go port's adapter-unavailable path; upstream compute_unavailable_mcp_tools is untested for that fallback.
func TestSpawnKeepsMCPToolsWithoutCredentialView(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "done", nil))
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{{Name: "mcp__slack__post"}},
		MCPToolNames: map[string]struct{}{
			"mcp__slack__post": {},
		},
		ToolProviderMap: map[string]string{"mcp__slack__post": "slack"},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "profile_mcp_noview_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "do"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if _, ok := toolDefNameSet(worker.Context.AvailableTools)["mcp__slack__post"]; !ok {
		t.Fatal("tool dropped without a credential view; the filter must degrade to a no-op")
	}
}

// Parity: framework/host/worker.py::Worker.run (browser-profile pre-flight)
func TestBrowserProfilePreflightFailsDisconnectedWorker(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "should not run", nil))
	colonyDir := t.TempDir()
	writeWorkerProfileMetadata(t, colonyDir, `{
		"worker_profiles": [{"name": "blue_worker", "browser_profile": "blue"}]
	}`)
	bus := eventbus.NewEventBus()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: colonyDir,
		LLM:         provider,
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: bus,
		StreamID: "profile_browser_fail_colony",
		ConnectedBrowserProfiles: func() ([]string, error) {
			return []string{"red"}, nil
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	reports := collectEvents(bus, eventbus.EventSubagentReport)
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "browse", ProfileName: "blue_worker"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 5*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never failed the browser pre-flight")
	}
	if worker.Status() != WorkerFailed {
		t.Fatalf("worker status = %s, want failed", worker.Status())
	}
	if got := provider.callCount(); got != 0 {
		t.Fatalf("model calls = %d, want 0 for a pre-flight failure", got)
	}
	result := worker.Result()
	if result.Error == nil || *result.Error != "browser profile 'blue' not connected" {
		t.Fatalf("result error = %v, want the browser profile error", result.Error)
	}
	if !strings.HasPrefix(result.Summary, "Not attempted: the Chrome profile 'blue'") ||
		!strings.Contains(result.Summary, "Connected browser profiles right now: red.") {
		t.Fatalf("result summary = %q, want the Not attempted browser message", result.Summary)
	}
	if !eventually(t, 5*time.Second, func() bool { return reports.count() == 1 }) {
		t.Fatalf("SUBAGENT_REPORT events = %d, want 1", reports.count())
	}
	payload := reports.snapshot()[0].Data
	if payload["status"] != "failed" || payload["error"] != "browser profile 'blue' not connected" {
		t.Fatalf("report = %#v, want the failed browser pre-flight report", payload)
	}
}

// An unreachable probe is "unknown": the worker proceeds instead of being
// false-failed.
// Parity: core/tests/test_worker_browser_preflight.py::test_worker_not_blocked_when_bridge_unreachable
func TestBrowserProfileProbeUnknownProceeds(t *testing.T) {
	provider := newScriptedLLM(reportResponse("success", "done", nil))
	colonyDir := t.TempDir()
	writeWorkerProfileMetadata(t, colonyDir, `{
		"worker_profiles": [{"name": "blue_worker", "browser_profile": "blue"}]
	}`)
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: colonyDir,
		LLM:         provider,
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "profile_browser_unknown_colony",
		ConnectedBrowserProfiles: func() ([]string, error) {
			return nil, os.ErrDeadlineExceeded
		},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	ids, err := runtime.SpawnBatch(context.Background(), []BatchTask{{Task: "browse", ProfileName: "blue_worker"}})
	if err != nil {
		t.Fatalf("SpawnBatch: %v", err)
	}
	worker := runtime.GetWorker(ids[0])
	if !eventually(t, 10*time.Second, func() bool { return worker != nil && !worker.IsActive() }) {
		t.Fatal("worker never terminated")
	}
	if worker.Status() != WorkerCompleted {
		t.Fatalf("worker status = %s, want completed (unknown probe proceeds)", worker.Status())
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
}

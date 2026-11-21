package colony

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/pipeline"
)

func writeColonyConfig(t *testing.T, home string, config map[string]any) {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), data, 0o600); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}
}

// newTriggerTestColony builds a started colony over a temp HIVE_HOME. Stages
// passed here model upstream's injected `pipeline_stages`; config stages come
// from the caller's configuration.json.
func newTriggerTestColony(t *testing.T, stages []pipeline.Stage) *ColonyRuntime {
	t.Helper()
	t.Setenv("HIVE_HOME", t.TempDir())
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         newScriptedLLM(reportResponse("success", "done", nil)),
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus:       eventbus.NewEventBus(),
		StreamID:       "trigger_colony",
		PipelineStages: stages,
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

type rejectStage struct {
	pipeline.BaseStage
	reason string
}

func (s *rejectStage) Process(*pipeline.Context) (pipeline.Result, error) {
	return pipeline.RejectResult(s.reason), nil
}

type transformStage struct {
	pipeline.BaseStage
	input map[string]any
}

func (s *transformStage) Process(*pipeline.Context) (pipeline.Result, error) {
	return pipeline.TransformResult(s.input), nil
}

// Parity-exempt: upstream ships no test for ColonyRuntime.trigger; this pins
// the empty-pipeline no-op path (trigger goes straight to the spawn).
func TestTriggerEmptyPipelineSpawnsWorker(t *testing.T) {
	runtime := newTriggerTestColony(t, nil)
	workerID, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "do work"}, TriggerOptions{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	worker := runtime.GetWorker(workerID)
	if worker == nil {
		t.Fatalf("worker %q not spawned", workerID)
	}
	if worker.Task != "do work" {
		t.Fatalf("worker task = %q", worker.Task)
	}
	if worker.Context == nil || worker.Context.InputData["task"] != "do work" {
		t.Fatalf("worker input data = %#v", worker.Context)
	}
}

// Parity-exempt: upstream ships no test for ColonyRuntime.trigger; a transform
// stage's input_data flows into the spawned worker and its task text.
func TestTriggerPipelineTransformFlowsIntoWorker(t *testing.T) {
	runtime := newTriggerTestColony(t, []pipeline.Stage{
		&transformStage{
			BaseStage: pipeline.BaseStage{StageName: "rewrite", StageOrder: 10},
			input:     map[string]any{"task": "rewritten"},
		},
	})
	workerID, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "original"}, TriggerOptions{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	worker := runtime.GetWorker(workerID)
	if worker == nil {
		t.Fatalf("worker %q not spawned", workerID)
	}
	if worker.Task != "rewritten" {
		t.Fatalf("worker task = %q, want the transformed task", worker.Task)
	}
}

// Parity-exempt: upstream ships no test for ColonyRuntime.trigger; a rejecting
// stage surfaces PipelineRejectedError and spawns nothing.
func TestTriggerPipelineRejectSpawnsNothing(t *testing.T) {
	runtime := newTriggerTestColony(t, []pipeline.Stage{
		&rejectStage{BaseStage: pipeline.BaseStage{StageName: "guard", StageOrder: 10}, reason: "no"},
	})
	_, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "x"}, TriggerOptions{})
	var rejected *pipeline.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *pipeline.RejectedError", err)
	}
	if rejected.StageName != "guard" || rejected.Reason != "no" {
		t.Fatalf("rejection = %+v", rejected)
	}
	if stats := runtime.GetStats(); stats["total_workers"] != 0 {
		t.Fatalf("total_workers = %v, want 0 after a rejection", stats["total_workers"])
	}
}

// Parity-exempt: upstream ships no test for ColonyRuntime.trigger; the
// configuration.json pipeline.stages list is honoured at the trigger entry
// (rate_limit rejects the second request in the same session bucket).
func TestTriggerRunsConfiguredPipelineStages(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	writeColonyConfig(t, os.Getenv("HIVE_HOME"), map[string]any{
		"pipeline": map[string]any{
			"stages": []any{
				map[string]any{
					"type":   "rate_limit",
					"config": map[string]any{"max_requests_per_minute": 1},
				},
			},
		},
	})
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         newScriptedLLM(reportResponse("success", "done", nil)),
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "trigger_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)

	session := map[string]any{"session_id": "s1"}
	if _, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "one"}, TriggerOptions{SessionState: session}); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	_, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "two"}, TriggerOptions{SessionState: session})
	var rejected *pipeline.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("second Trigger err = %v, want rejection", err)
	}
	if !strings.Contains(rejected.Reason, "Rate limit exceeded: 1 req/min") {
		t.Fatalf("reason = %q", rejected.Reason)
	}
}

// Parity-exempt: upstream ships no test for ColonyRuntime.trigger; an
// idempotency key returns the cached worker id before the pipeline runs again.
func TestTriggerIdempotencyKeyReturnsCachedWorker(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	writeColonyConfig(t, os.Getenv("HIVE_HOME"), map[string]any{
		"pipeline": map[string]any{
			"stages": []any{
				map[string]any{
					"type":   "rate_limit",
					"config": map[string]any{"max_requests_per_minute": 1},
				},
			},
		},
	})
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
		LLM:         newScriptedLLM(reportResponse("success", "done", nil)),
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: eventbus.NewEventBus(),
		StreamID: "trigger_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)

	first, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "x"}, TriggerOptions{IdempotencyKey: "same-call"})
	if err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	second, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "x"}, TriggerOptions{IdempotencyKey: "same-call"})
	if err != nil {
		t.Fatalf("second Trigger (cached): %v", err)
	}
	if first == "" || first != second {
		t.Fatalf("ids = %q / %q, want the cached worker", first, second)
	}
	if stats := runtime.GetStats(); stats["total_workers"] != 1 {
		t.Fatalf("total_workers = %v, want 1 (no second spawn)", stats["total_workers"])
	}
}

// Parity-exempt: upstream ships no test for ColonyRuntime._load_pipeline_from_config;
// an unknown stage type in configuration.json fails startup loudly.
func TestUnknownConfiguredStageTypeFailsStartup(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	writeColonyConfig(t, os.Getenv("HIVE_HOME"), map[string]any{
		"pipeline": map[string]any{
			"stages": []any{
				map[string]any{"type": "not_a_stage"},
			},
		},
	})
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
	})
	err := runtime.Start(context.Background())
	if err == nil {
		t.Fatal("Start must fail on an unknown configured stage type")
	}
	if !strings.Contains(err.Error(), `unknown pipeline stage type "not_a_stage"`) {
		t.Fatalf("Start error = %v", err)
	}
}

// Parity-exempt: upstream ships no test for ColonyRuntime._load_pipeline_from_config;
// injected pipeline_stages bypass configuration.json entirely.
func TestInjectedStagesOverrideBrokenConfig(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	writeColonyConfig(t, os.Getenv("HIVE_HOME"), map[string]any{
		"pipeline": map[string]any{
			"stages": []any{
				map[string]any{"type": "not_a_stage"},
			},
		},
	})
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:      testAgentSpec(),
		StoragePath:    t.TempDir(),
		LLM:            newScriptedLLM(reportResponse("success", "done", nil)),
		PipelineStages: []pipeline.Stage{&transformStage{BaseStage: pipeline.BaseStage{StageName: "injected", StageOrder: 10}, input: map[string]any{"task": "t"}}},
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start with injected stages: %v", err)
	}
	t.Cleanup(runtime.Stop)
}

// Parity-exempt: upstream ships no test for ColonyRuntime.trigger; a Trigger
// on a non-running runtime keeps upstream's exact error text.
func TestTriggerRequiresRunningRuntime(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	runtime := NewColonyRuntime(RuntimeOptions{AgentSpec: testAgentSpec(), StoragePath: t.TempDir()})
	_, err := runtime.Trigger(context.Background(), "api", map[string]any{"task": "x"}, TriggerOptions{})
	if err == nil || err.Error() != "ColonyRuntime is not running" {
		t.Fatalf("err = %v", err)
	}
}

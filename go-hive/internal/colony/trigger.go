package colony

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/hiveconf"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/pipeline"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

const (
	// triggerIdempotencyTTL and triggerIdempotencyMaxKeys mirror
	// ColonyConfig.idempotency_ttl_seconds / idempotency_max_keys.
	triggerIdempotencyTTL     = 300 * time.Second
	triggerIdempotencyMaxKeys = 10000
)

// TriggerOptions carries the optional trigger() call arguments upstream
// exposes as keyword arguments.
type TriggerOptions struct {
	// CorrelationID optionally ties the request to a caller-side trace and
	// rides the pipeline context.
	CorrelationID string
	// SessionState is the session snapshot the pipeline reads (e.g. the
	// rate-limit bucket's session_id).
	SessionState map[string]any
	// IdempotencyKey returns the previously spawned worker for a repeated
	// call instead of spawning again.
	IdempotencyKey string
}

// Trigger spawns one worker from a trigger definition, after running the
// configured pipeline over the request. It is non-blocking and returns the
// worker id immediately.
//
// A pipeline rejection surfaces as *pipeline.RejectedError and spawns nothing;
// a stage failure surfaces as *pipeline.StageError. With no configured stages
// the call goes straight to the spawn — the default no-op path.
//
// Production reachability: this is API-surface parity, not a production
// entry point. Upstream's only callers are trigger_and_wait
// (core/framework/host/colony_runtime.py:2425) and the colony timer loop
// (:2904); the timer registry that feeds it (register_trigger, :2823) has no
// caller in the checkout, and SessionManager builds its colony with
// pipeline_stages=[] (core/framework/server/session_manager.py:1777). The
// production trigger path injects the queen node instead
// (core/framework/server/routes_sessions.py:1527 ->
// core/framework/tools/queen_lifecycle_tools.py:810), and the Go server does
// the same (internal/server/routes_session_triggers.go:367,
// internal/server/trigger_runtime.go:512). Do not wire this into
// handleRunTrigger: that would spawn a worker where upstream injects a queen
// event. See docs/pipeline.md §7 and internal/pipeline/reachability_test.go.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.trigger
func (r *ColonyRuntime) Trigger(ctx context.Context, triggerID string, inputData map[string]any, opts TriggerOptions) (string, error) {
	if !r.running {
		return "", errors.New("ColonyRuntime is not running")
	}
	if opts.IdempotencyKey != "" {
		if cached, ok := r.lookupIdempotencyKey(opts.IdempotencyKey); ok {
			return cached, nil
		}
	}
	if err := r.initializePipeline(); err != nil {
		return "", err
	}
	if inputData == nil {
		inputData = map[string]any{}
	}

	if r.triggerPipeline != nil && r.triggerPipeline.Len() > 0 {
		pipelineCtx := pipeline.NewContext(triggerID, inputData)
		pipelineCtx.CorrelationID = opts.CorrelationID
		pipelineCtx.SessionState = opts.SessionState
		ran, err := r.triggerPipeline.Run(pipelineCtx)
		if err != nil {
			return "", err
		}
		inputData = ran.InputData
	}

	// Upstream: input_data.get("task", json.dumps(input_data)) — an explicit
	// task wins even when it is not a string; otherwise the whole payload is
	// the task text.
	task := ""
	if raw, present := inputData["task"]; present {
		if text, ok := raw.(string); ok {
			task = text
		} else {
			task = pyJSONDumps(raw)
		}
	} else {
		task = pyJSONDumps(inputData)
	}

	workerIDs, err := r.SpawnBatchWithOptions(ctx, []BatchTask{{
		Task: task,
		Data: inputData,
	}}, SpawnBatchOptions{})
	if err != nil {
		return "", err
	}
	workerID := ""
	if len(workerIDs) > 0 {
		workerID = workerIDs[0]
	}
	if opts.IdempotencyKey != "" && workerID != "" {
		r.storeIdempotencyKey(opts.IdempotencyKey, workerID)
	}
	return workerID, nil
}

// loadPipelineFromConfig reads `pipeline.stages` from configuration.json.
// No/empty section yields an empty runner (the no-op default); a configured
// stage with an unknown type is a startup error, never a silent skip.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime._load_pipeline_from_config
func loadPipelineFromConfig() (*pipeline.Runner, error) {
	home, err := hivehome.Resolve()
	if err != nil {
		// Upstream's get_hive_config reads a missing file as {}; an
		// unresolvable home is the same "no configuration" state.
		return pipeline.NewRunner(nil), nil
	}
	section := hiveconf.Section(home, "pipeline")
	if len(section) == 0 {
		return pipeline.NewRunner(nil), nil
	}
	rawStages, present := section["stages"]
	if !present || rawStages == nil {
		return pipeline.NewRunner(nil), nil
	}
	stages, ok := rawStages.([]any)
	if !ok {
		return nil, fmt.Errorf("configuration.json: pipeline.stages must be a list, got %T", rawStages)
	}
	if len(stages) == 0 {
		return pipeline.NewRunner(nil), nil
	}
	runner, err := pipeline.BuildFromConfig(stages)
	if err != nil {
		return nil, fmt.Errorf("configuration.json: %w", err)
	}
	return runner, nil
}

// initializePipeline runs every stage's Initialize once and applies its
// outputs to the runtime. An empty pipeline is a no-op.
func (r *ColonyRuntime) initializePipeline() error {
	r.pipelineInitMu.Lock()
	defer r.pipelineInitMu.Unlock()
	if r.pipelineInitialized {
		return nil
	}
	if r.pipelineErr != nil {
		return r.pipelineErr
	}
	if r.triggerPipeline == nil || r.triggerPipeline.Len() == 0 {
		r.pipelineInitialized = true
		return nil
	}
	r.preparePipelineToolRegistries()
	if err := r.triggerPipeline.InitializeAll(); err != nil {
		return err
	}
	r.applyPipelineOutputs()
	r.pipelineInitialized = true
	return nil
}

// preparePipelineToolRegistries gives every mcp_registry stage the runtime's
// base worker tools before the stage loads its servers. Upstream constructs the
// stage with the session's fully-built ToolRegistry; the Go stage normally
// builds an empty one, so the runtime seeds it here. Tools already registered
// on an injected registry are kept and never overwritten.
func (r *ColonyRuntime) preparePipelineToolRegistries() {
	if r.triggerPipeline == nil {
		return
	}
	for _, stage := range r.triggerPipeline.Stages() {
		mcpStage, ok := stage.(*pipeline.MCPRegistryStage)
		if !ok {
			continue
		}
		registry := mcpStage.ToolRegistry
		if registry == nil {
			registry = tools.NewToolRegistry()
		}
		baseExec := r.toolExec
		for _, def := range r.tools {
			if def.Name == "" || registry.HasTool(def.Name) {
				continue
			}
			toolDef := def
			registry.Register(toolDef.Name, tools.Tool{
				Name:            toolDef.Name,
				Description:     toolDef.Description,
				Parameters:      toolDef.Parameters,
				ProducesImage:   toolDef.ProducesImage,
				ConcurrencySafe: toolDef.ConcurrencySafe,
			}, func(inputs map[string]any) (any, error) {
				if baseExec == nil {
					return nil, fmt.Errorf("tool %s has no executor", toolDef.Name)
				}
				result, err := baseExec(agentloop.ToolCallRequest{Name: toolDef.Name, Input: inputs})
				if err != nil {
					return nil, err
				}
				return tools.ToolResult{
					Content:        result.Content,
					IsError:        result.IsError,
					IsSkillContent: result.IsSkillContent,
					SpilloverPath:  result.SpilloverPath,
					ImageContent:   result.ImageContent,
				}, nil
			})
		}
		mcpStage.ToolRegistry = registry
	}
}

// applyPipelineOutputs mirrors upstream's _apply_pipeline_results: a stage's
// LLM, credential, skills and MCP registry outputs feed the runtime when it
// does not already have them. The MCP registry replaces the worker tool
// surface and its executor, the same wholesale swap upstream performs after
// registering task/tracker tools onto that registry.
//
// Parity: framework/host/colony_runtime.py::_apply_pipeline_results
func (r *ColonyRuntime) applyPipelineOutputs() {
	if r.triggerPipeline == nil {
		return
	}
	for _, stage := range r.triggerPipeline.Stages() {
		switch typed := stage.(type) {
		case *pipeline.LLMProviderStage:
			if r.llm == nil && typed.LLM != nil {
				r.llm = typed.LLM
			}
		case *pipeline.CredentialResolverStage:
			if r.toolProviderMap == nil && len(typed.ToolProviderMap) > 0 {
				copied := make(map[string]string, len(typed.ToolProviderMap))
				for name, provider := range typed.ToolProviderMap {
					copied[name] = provider
				}
				r.toolProviderMap = copied
			}
			if typed.AccountsPrompt != "" {
				r.accountsPrompt = typed.AccountsPrompt
			}
		case *pipeline.SkillRegistryStage:
			if typed.SkillsManager != nil {
				manager := typed.SkillsManager
				r.skillsProvider = func() ([]*skills.ParsedSkill, bool) {
					return manager.EnumerateSkillsWithSource(), manager.IsLoaded()
				}
			}
		case *pipeline.MCPRegistryStage:
			if typed.ToolRegistry == nil {
				continue
			}
			defs := pipeline.ToolDefsFromRegistry(typed.ToolRegistry)
			if len(defs) == 0 {
				continue
			}
			r.tools = defs
			r.toolExec = toolRegistryExecutor(typed.ToolRegistry.GetExecutor())
		}
	}
}

// toolRegistryExecutor adapts a registry executor to the loop's tool-call
// shape, preserving the content/error/image fields the conversation consumes.
func toolRegistryExecutor(executor *tools.RegistryExecutor) func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	if executor == nil {
		return nil
	}
	return func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		result, err := executor.Execute(context.Background(), tools.ToolUse{
			ID:    request.ID,
			Name:  request.Name,
			Input: request.Input,
		})
		if err != nil {
			return agentloop.ToolResult{}, err
		}
		return agentloop.ToolResult{
			Content:        result.Content,
			IsError:        result.IsError,
			IsSkillContent: result.IsSkillContent,
			SpilloverPath:  result.SpilloverPath,
			ImageContent:   result.ImageContent,
		}, nil
	}
}

// lookupIdempotencyKey prunes the cache and returns a cached worker id.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime._prune_idempotency_keys
func (r *ColonyRuntime) lookupIdempotencyKey(key string) (string, bool) {
	r.triggerIdemMu.Lock()
	defer r.triggerIdemMu.Unlock()
	r.pruneIdempotencyKeysLocked(time.Now())
	workerID, ok := r.idempotencyKeys[key]
	return workerID, ok
}

func (r *ColonyRuntime) storeIdempotencyKey(key, workerID string) {
	r.triggerIdemMu.Lock()
	defer r.triggerIdemMu.Unlock()
	if _, exists := r.idempotencyKeys[key]; !exists {
		r.idempotencyOrder = append(r.idempotencyOrder, key)
	}
	r.idempotencyKeys[key] = workerID
	r.idempotencyTimes[key] = time.Now()
}

// pruneIdempotencyKeysLocked expires entries past the TTL and evicts the
// oldest entries beyond the key cap. Caller holds triggerIdemMu.
func (r *ColonyRuntime) pruneIdempotencyKeysLocked(now time.Time) {
	if r.idempotencyKeys == nil {
		r.idempotencyKeys = map[string]string{}
		r.idempotencyTimes = map[string]time.Time{}
	}
	cutoff := now.Add(-triggerIdempotencyTTL)
	for key, recordedAt := range r.idempotencyTimes {
		if recordedAt.Before(cutoff) {
			delete(r.idempotencyTimes, key)
			delete(r.idempotencyKeys, key)
		}
	}
	for len(r.idempotencyKeys) > triggerIdempotencyMaxKeys && len(r.idempotencyOrder) > 0 {
		oldest := r.idempotencyOrder[0]
		r.idempotencyOrder = r.idempotencyOrder[1:]
		if _, still := r.idempotencyKeys[oldest]; !still {
			continue
		}
		delete(r.idempotencyKeys, oldest)
		delete(r.idempotencyTimes, oldest)
	}
}

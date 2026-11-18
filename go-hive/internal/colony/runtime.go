package colony

// Parity: framework/host/colony_runtime.py

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/schemas"
	"github.com/aden-hive/hive-go/internal/skills"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tracker"
)

const DefaultReportWait = 60 * time.Second

// Parity: framework/host/colony_runtime.py (module constants)
var (
	DefaultAdaptiveToolBudget = colonyEnvBool("HIVE_ADAPTIVE_TOOL_BUDGET", true)
	AdaptiveBudgetMinSamples  = colonyEnvInt("HIVE_ADAPTIVE_BUDGET_MIN_SAMPLES", 3)
	AdaptiveBudgetFloor       = colonyEnvInt("HIVE_ADAPTIVE_BUDGET_FLOOR", 30)
)

const (
	adaptiveBudgetMultiplier      = 2.0
	adaptiveBudgetMaxGuard        = 1.25
	adaptiveBudgetDispersionLimit = 4.0
	adaptiveBudgetSampleWindow    = 50
)

type RuntimeOptions struct {
	AgentSpec          agentloop.AgentSpec
	Goal               *schemas.Goal
	StoragePath        string
	LLM                llm.Completer
	Tools              []llm.ToolDef
	ToolExecutor       func(agentloop.ToolCallRequest) (agentloop.ToolResult, error)
	EventBus           *eventbus.EventBus
	StreamID           string
	MCPToolNames       map[string]struct{}
	WorkerKeepSet      func() map[string]struct{}
	AdaptiveToolBudget *bool
	// MaxConcurrentWorkers caps how many spawned workers execute at once.
	// Extra workers stay QUEUED until a slot opens. <=0 uses the colony
	// default (HIVE_MAX_CONCURRENT_WORKERS, 4).
	MaxConcurrentWorkers int

	// WorkerLLM is the completer workers call. Nil falls back to LLM (the Go
	// port does not split queen/worker models by default; upstream resolves
	// worker_llm separately).
	WorkerLLM llm.Completer
	// SkillsProvider supplies the colony's skill catalog for the read surface
	// (GET /colony/skills). Nil reports "catalog unavailable", matching
	// upstream's absent skills_manager.
	SkillsProvider func() ([]*skills.ParsedSkill, bool)
	// ToolProviderMap attributes tool names to providers for the tools read
	// surface (upstream ColonyRuntime._tool_provider_map).
	ToolProviderMap map[string]string
}

type SpawnOptions struct {
	Count    int
	StreamID string
}

type BatchTask struct {
	Task     string
	StreamID string
	// Data is forwarded to the worker as AgentContext.InputData. It is
	// where per-task payloads (row batches, URLs, account ids) belong.
	Data map[string]any
	// ProfileName tags the worker with the colony worker profile it was
	// dispatched from; empty for legacy / single-template colonies.
	ProfileName string
}

type SpawnBatchOptions struct {
	LoopConfigOverrides map[string]any
}

type WorkerBuildOptions struct {
	WorkerID     string
	Storage      string
	Task         string
	StreamID     string
	SpawnTools   []llm.ToolDef
	PreloadTools []string
	InputData    map[string]any
	// ProfileName tags the worker with its colony worker profile.
	ProfileName string
}

type SeedMessage struct {
	Seq     int
	Role    string
	Content string
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime (orchestration subset)
type ColonyRuntime struct {
	agentSpec   agentloop.AgentSpec
	goal        *schemas.Goal
	storagePath string
	llm         llm.Completer
	tools       []llm.ToolDef
	toolExec    func(agentloop.ToolCallRequest) (agentloop.ToolResult, error)
	bus         *eventbus.EventBus
	streamID    string
	keepSet     func() map[string]struct{}

	toolsMu         sync.RWMutex
	enabledMCPTools []string
	mcpToolNamesAll map[string]struct{}

	workerLLM       llm.Completer
	skillsProvider  func() ([]*skills.ParsedSkill, bool)
	toolProviderMap map[string]string
	// dispatchBlocked stops queued workers from starting during a stop sweep
	// (and, when a caller holds it, until UnblockDispatch). Guarded by mu.
	dispatchBlocked bool

	mu       sync.Mutex
	workers  map[string]*Worker
	overseer *Worker
	nextID   int
	started  bool
	running  bool
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	maxConcurrent int
	workerSlots   chan struct{}

	adaptiveEnabled bool

	budgetMu         sync.Mutex
	budgetSamples    []int
	budgetApplied    int
	budgetAppliedSet bool
	budgetMaxSuccess int
}

func NewColonyRuntime(opts RuntimeOptions) *ColonyRuntime {
	storage := opts.StoragePath
	if storage == "" {
		storage = "."
	}
	maxConcurrent := opts.MaxConcurrentWorkers
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrentWorkers
	}
	runtime := &ColonyRuntime{
		agentSpec:     opts.AgentSpec,
		goal:          opts.Goal,
		storagePath:   storage,
		llm:           opts.LLM,
		tools:         append([]llm.ToolDef(nil), opts.Tools...),
		toolExec:      opts.ToolExecutor,
		bus:           opts.EventBus,
		streamID:      opts.StreamID,
		keepSet:       opts.WorkerKeepSet,
		workers:       map[string]*Worker{},
		maxConcurrent: maxConcurrent,
		workerSlots:   make(chan struct{}, maxConcurrent),
	}
	runtime.adaptiveEnabled = DefaultAdaptiveToolBudget
	if opts.AdaptiveToolBudget != nil {
		runtime.adaptiveEnabled = *opts.AdaptiveToolBudget
	}
	if opts.MCPToolNames != nil {
		runtime.mcpToolNamesAll = make(map[string]struct{}, len(opts.MCPToolNames))
		for name := range opts.MCPToolNames {
			runtime.mcpToolNamesAll[name] = struct{}{}
		}
	}
	runtime.workerLLM = opts.WorkerLLM
	runtime.skillsProvider = opts.SkillsProvider
	if opts.ToolProviderMap != nil {
		runtime.toolProviderMap = make(map[string]string, len(opts.ToolProviderMap))
		for name, provider := range opts.ToolProviderMap {
			runtime.toolProviderMap[name] = provider
		}
	}
	return runtime
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.goal
func (r *ColonyRuntime) Goal() *schemas.Goal { return r.goal }

func (r *ColonyRuntime) StreamID() string { return r.streamID }

// Parity: framework/server/routes_sessions.py::_active_worker_count
func (r *ColonyRuntime) ActiveWorkerCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	active := 0
	for _, worker := range r.workers {
		if worker.IsActive() {
			active++
		}
	}
	return active
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_stats
func (r *ColonyRuntime) GetStats() map[string]any {
	r.mu.Lock()
	running := r.running
	total := len(r.workers)
	active := 0
	queued := 0
	for _, worker := range r.workers {
		if worker.IsActive() {
			active++
		}
		if worker.Status() == WorkerQueued {
			queued++
		}
	}
	r.mu.Unlock()

	eventBusStats := any(map[string]any{})
	if r.bus != nil {
		eventBusStats = r.bus.GetStats()
	}
	return map[string]any{
		"running":                running,
		"colony_id":              r.streamID,
		"active_workers":         active,
		"queued_workers":         queued,
		"max_concurrent_workers": r.maxConcurrent,
		"total_workers":          total,
		"triggers":               0,
		"event_bus":              eventBusStats,
		"adaptive_tool_budget":   r.AdaptiveToolBudgetStats(),
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.set_tool_allowlist
func (r *ColonyRuntime) SetToolAllowlist(enabledMCPTools []string, mcpToolNamesAll map[string]struct{}) {
	r.toolsMu.Lock()
	defer r.toolsMu.Unlock()
	if enabledMCPTools == nil {
		r.enabledMCPTools = nil
	} else {
		r.enabledMCPTools = append([]string{}, enabledMCPTools...)
	}
	if mcpToolNamesAll != nil {
		r.mcpToolNamesAll = make(map[string]struct{}, len(mcpToolNamesAll))
		for name := range mcpToolNamesAll {
			r.mcpToolNamesAll[name] = struct{}{}
		}
	}
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._apply_tool_allowlist
func (r *ColonyRuntime) applyToolAllowlist(toolDefs []llm.ToolDef) []llm.ToolDef {
	r.toolsMu.RLock()
	enabled := r.enabledMCPTools
	mcpNames := r.mcpToolNamesAll
	r.toolsMu.RUnlock()
	if enabled == nil {
		return toolDefs
	}
	allowed := make(map[string]struct{}, len(enabled))
	for _, name := range enabled {
		allowed[name] = struct{}{}
	}
	out := make([]llm.ToolDef, 0, len(toolDefs))
	for _, def := range toolDefs {
		if _, gated := mcpNames[def.Name]; gated {
			if _, ok := allowed[def.Name]; !ok {
				continue
			}
		}
		out = append(out, def)
	}
	return out
}

func (r *ColonyRuntime) gateableNames() map[string]struct{} {
	r.toolsMu.RLock()
	defer r.toolsMu.RUnlock()
	out := make(map[string]struct{}, len(r.mcpToolNamesAll))
	for name := range r.mcpToolNamesAll {
		out[name] = struct{}{}
	}
	return out
}

func (r *ColonyRuntime) workerKeepSet() map[string]struct{} {
	if r.keepSet != nil {
		return r.keepSet()
	}
	return tools.WorkerAlwaysEnabledToolNames(nil)
}

func (r *ColonyRuntime) workerStorage(workerID string) string {
	return filepath.Join(r.storagePath, "workers", workerID)
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
func (r *ColonyRuntime) BuildWorker(opts WorkerBuildOptions) *Worker {
	streamID := opts.StreamID
	if streamID == "" {
		streamID = "worker:" + opts.WorkerID
	}
	storage := opts.Storage
	if storage == "" {
		storage = r.workerStorage(opts.WorkerID)
	}
	spawnTools := opts.SpawnTools
	if spawnTools == nil {
		spawnTools = r.applyToolAllowlist(r.tools)
	}
	worker := NewWorker(opts.WorkerID, opts.Task, streamID)
	worker.ProfileName = opts.ProfileName
	worker.Context = r.buildWorkerContext(worker, r.agentSpec, spawnTools, storage, opts.PreloadTools)
	if opts.InputData != nil {
		worker.Context.InputData = opts.InputData
		if binding := tracker.BindingFromInputData(opts.InputData); binding != nil {
			trackerDB := binding.TrackerDB
			worker.Context.ColonyBindingProvider = func() *agentloop.ColonyBinding {
				return &agentloop.ColonyBinding{TrackerDB: trackerDB}
			}
		}
	}
	return worker
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
func (r *ColonyRuntime) buildWorkerContext(worker *Worker, spec agentloop.AgentSpec, spawnTools []llm.ToolDef, storage string, preload []string) *agentloop.AgentContext {
	ctx := r.buildContext(worker, spec)
	ctx.AvailableTools = append([]llm.ToolDef(nil), spawnTools...)

	keepSet := r.workerKeepSet()
	if len(keepSet) == 0 {
		return ctx
	}

	pool := make([]tools.Tool, 0, len(spawnTools))
	registered := make(map[string]struct{}, len(spawnTools))
	for _, def := range spawnTools {
		pool = append(pool, tools.Tool{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.Parameters,
		})
		registered[def.Name] = struct{}{}
	}
	tier := &tools.ToolTierState{
		Pool:               pool,
		AlwaysEnabledNames: keepSet,
		GateableNames:      r.gateableNames(),
		PersistPath:        filepath.Join(storage, "tool_tiers.json"),
	}
	tier.RestoreLoadedTools(tier.LoadPersistedTools(), registered)
	tier.Rebuild()
	if len(preload) > 0 {
		valid := make([]string, 0, len(preload))
		for _, name := range preload {
			if _, ok := registered[name]; ok {
				valid = append(valid, name)
			}
		}
		if len(valid) > 0 {
			tier.PromoteSearchedTools(valid)
		}
	}

	ctx.ToolTierState = tier
	ctx.DynamicToolsProvider = func() []llm.ToolDef {
		return tierToolDefs(tier.GetCurrentTools())
	}
	ctx.SearchableToolsProvider = func() []agentloop.Tool {
		return tierReminderTools(tier.GetSearchableTools())
	}
	ctx.LoadedToolNamesProvider = func() []string {
		return append([]string(nil), tier.LoadedToolNames...)
	}
	return ctx
}

func tierToolDefs(list []tools.Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(list))
	for _, tool := range list {
		out = append(out, llm.ToolDef{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return out
}

func tierReminderTools(list []tools.Tool) []agentloop.Tool {
	out := make([]agentloop.Tool, 0, len(list))
	for _, tool := range list {
		out = append(out, agentloop.Tool{Name: tool.Name, Description: tool.Description})
	}
	return out
}

func (r *ColonyRuntime) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.started = true
	r.running = true
	return nil
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop
func (r *ColonyRuntime) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	started := r.started
	r.running = false
	r.mu.Unlock()
	if started && cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

func (r *ColonyRuntime) runtimeContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		r.ctx, r.cancel = context.WithCancel(ctx)
		r.started = true
	}
	r.running = true
	return r.ctx
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_worker
func (r *ColonyRuntime) GetWorker(id string) *Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.workers[id]
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn
func (r *ColonyRuntime) Spawn(ctx context.Context, task string, opts SpawnOptions) ([]string, error) {
	count := opts.Count
	if count <= 0 {
		count = 1
	}
	tasks := make([]BatchTask, 0, count)
	for i := 0; i < count; i++ {
		tasks = append(tasks, BatchTask{Task: task, StreamID: opts.StreamID})
	}
	return r.SpawnBatch(ctx, tasks)
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
func (r *ColonyRuntime) SpawnBatch(ctx context.Context, tasks []BatchTask) ([]string, error) {
	return r.SpawnBatchWithOptions(ctx, tasks, SpawnBatchOptions{})
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch (worker
func (r *ColonyRuntime) SpawnBatchWithOptions(ctx context.Context, tasks []BatchTask, opts SpawnBatchOptions) ([]string, error) {
	// A fresh spawn is a new work request (a resumed queen turn), so it
	// re-opens dispatch after a stop sweep. The block exists to keep workers
	// from starting *into* a sweep, not to gate the next turn.
	r.UnblockDispatch()
	runCtx := r.runtimeContext(ctx)
	batchID := ""
	if len(tasks) > 1 {
		batchID = r.newID("batch")
	}
	return r.spawnBatchOn(runCtx, tasks, opts, batchID)
}

// spawnBatchOn registers and starts one goroutine per task. Every worker gets
// its own context so a single run/worker can be stopped without touching its
// siblings. Admission is gated by workerSlots: workers beyond the colony cap
// stay QUEUED until a running peer releases its slot.
func (r *ColonyRuntime) spawnBatchOn(runCtx context.Context, tasks []BatchTask, opts SpawnBatchOptions, batchID string) ([]string, error) {
	cfg, err := BuildWorkerLoopConfig(opts.LoopConfigOverrides)
	if err != nil {
		return nil, err
	}
	pinned := false
	if _, ok := opts.LoopConfigOverrides["tool_call_lifetime_budget"]; ok {
		pinned = true
	}

	ids := make([]string, 0, len(tasks))
	r.mu.Lock()
	for range tasks {
		ids = append(ids, r.newIDLocked("worker"))
	}
	r.mu.Unlock()

	workers := make([]*Worker, 0, len(tasks))
	for index, task := range tasks {
		worker := r.BuildWorker(WorkerBuildOptions{
			WorkerID:    ids[index],
			Task:        task.Task,
			StreamID:    task.StreamID,
			InputData:   task.Data,
			ProfileName: task.ProfileName,
		})
		worker.BatchID = batchID
		worker.BatchIndex = index
		worker.BatchSize = len(tasks)
		worker.loopOverrides = opts.LoopConfigOverrides
		workerConfig := cfg
		worker.loopConfig = &workerConfig
		worker.BudgetPinned = pinned
		workers = append(workers, worker)
	}

	r.mu.Lock()
	for _, worker := range workers {
		r.workers[worker.ID] = worker
	}
	r.mu.Unlock()

	for _, worker := range workers {
		worker := worker
		workerCtx, cancel := context.WithCancel(runCtx)
		worker.setCancel(cancel)
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer cancel()
			r.runGatedWorker(workerCtx, worker)
		}()
	}
	return ids, nil
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.wait_for_worker_reports
func (r *ColonyRuntime) WaitForWorkerReports(ctx context.Context, ids []string, timeout time.Duration) ([]map[string]any, error) {
	if timeout <= 0 {
		timeout = DefaultReportWait
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)

	reports := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		worker := r.GetWorker(id)
		if worker == nil {
			reports = append(reports, map[string]any{
				"worker_id": id,
				"status":    "failed",
				"error":     "no_such_worker",
			})
			continue
		}
		select {
		case <-worker.Done():
		case <-time.After(time.Until(deadline)):
			reports = append(reports, map[string]any{
				"worker_id": id,
				"status":    "failed",
				"error":     "timeout",
			})
			continue
		case <-ctx.Done():
			reports = append(reports, map[string]any{
				"worker_id": id,
				"status":    "failed",
				"error":     "cancelled",
			})
			continue
		}
		reports = append(reports, worker.reportPayload())
	}
	return reports, nil
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func (r *ColonyRuntime) StartOverseer(ctx context.Context, queenSpec agentloop.AgentSpec, seedConversation []SeedMessage) (*Worker, error) {
	r.mu.Lock()
	if r.overseer != nil {
		existing := r.overseer
		r.mu.Unlock()
		return existing, nil
	}
	id := "overseer:" + r.streamID
	worker := NewWorker(id, "", "overseer")
	worker.Persistent = true
	worker.BudgetPinned = true
	worker.Context = r.buildContext(worker, queenSpec)
	conv := agentloop.NewNodeConversation(systemPromptFor(queenSpec))
	for _, message := range seedConversation {
		content := message.Content
		if message.Role == "assistant" {
			_, _ = conv.AddAssistantMessage(content, nil, agentloop.AssistantMessageOptions{})
			continue
		}
		_, _ = conv.AddUserMessage(content, agentloop.UserMessageOptions{})
	}
	worker.conversation = conv
	r.overseer = worker
	r.mu.Unlock()

	r.mu.Lock()
	runCtx := r.ctx
	r.mu.Unlock()
	if runCtx == nil {
		var err error
		runCtx, err = r.armContext(ctx)
		if err != nil {
			return nil, err
		}
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.runOverseer(runCtx, worker, seedConversation)
	}()
	return worker, nil
}

func (r *ColonyRuntime) Overseer() *Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.overseer
}

func (r *ColonyRuntime) armContext(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		r.ctx, r.cancel = context.WithCancel(ctx)
		r.started = true
	}
	r.running = true
	return r.ctx, nil
}

func (r *ColonyRuntime) newID(prefix string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.newIDLocked(prefix)
}

func (r *ColonyRuntime) newIDLocked(prefix string) string {
	r.nextID++
	return fmt.Sprintf("%s_%d", prefix, r.nextID)
}

func systemPromptFor(spec agentloop.AgentSpec) string {
	if spec.SystemPrompt != nil {
		return *spec.SystemPrompt
	}
	return "You are " + spec.Name + "."
}

func (r *ColonyRuntime) buildContext(worker *Worker, spec agentloop.AgentSpec) *agentloop.AgentContext {
	colonyID := r.streamID
	ctx := &agentloop.AgentContext{
		AgentID:   spec.ID,
		AgentSpec: spec,
		LLM:       r.llm,
		StreamID:  worker.StreamID,
		ColonyID:  &colonyID,
	}
	ctx.StopAfterToolBatch = func() bool { return worker.ExplicitReport() != nil }
	return ctx
}

// Parity: framework/agent_loop/agent_loop.py report_to_parent gating
func (r *ColonyRuntime) workerTools(worker *Worker) []llm.ToolDef {
	tools := append([]llm.ToolDef(nil), r.tools...)
	if strings.HasPrefix(worker.StreamID, "worker:") {
		tools = append(tools, agentloop.BuildReportToParentTool(r.agentSpec.ReportSchema))
	}
	return tools
}

func (r *ColonyRuntime) executorFor(worker *Worker) func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	return func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		if req.Name == "report_to_parent" && strings.HasPrefix(worker.StreamID, "worker:") {
			worker.RecordExplicitReport(req.Input)
			return agentloop.ToolResult{Content: "Report delivered to the overseer."}, nil
		}
		if r.toolExec != nil {
			return r.toolExec(req)
		}
		return agentloop.ToolResult{Content: "ok"}, nil
	}
}

// runGatedWorker is the admission point for spawned workers. It blocks on the
// colony-wide concurrency gate, then hands off to the agent loop. A worker
// stopped while queued never starts but still reaches a terminal state and
// publishes a report, so batch counters and SSE consumers never wait forever.
func (r *ColonyRuntime) runGatedWorker(ctx context.Context, worker *Worker) {
	if !r.acquireWorkerSlot(ctx) {
		r.finishWorkerStopped(worker, "Worker was stopped before it started — the colony stopped while it was queued.")
		return
	}
	defer r.releaseWorkerSlot()
	if r.DispatchBlocked() {
		r.finishWorkerStopped(worker, "Worker was not started — colony dispatch is blocked.")
		return
	}
	if !worker.beginRun(ctx) {
		r.finishWorkerStopped(worker, "Worker was stopped before it started.")
		return
	}
	r.runWorker(ctx, worker)
}

func (r *ColonyRuntime) acquireWorkerSlot(ctx context.Context) bool {
	if r.workerSlots == nil {
		return true
	}
	select {
	case r.workerSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *ColonyRuntime) releaseWorkerSlot() {
	if r.workerSlots == nil {
		return
	}
	<-r.workerSlots
}

func (r *ColonyRuntime) finishWorkerStopped(worker *Worker, summary string) {
	// A report that landed just before cancellation is more informative than
	// the stop marker — preserve it instead of masking a delivered result.
	if worker.ExplicitReport() != nil {
		worker.finishOnce(WorkerCompleted, r.workerTerminalResult(worker, map[string]any{}, nil), func() {
			r.publishReport(worker, summary)
		})
		return
	}
	errorText := summary
	worker.finishOnce(WorkerStopped, WorkerResult{Output: map[string]any{}, Error: &errorText}, func() {
		r.publishStopped(worker, summary)
	})
}

// StopWorker stops one live or queued worker. Running workers unwind through
// their own context; queued workers wake from the admission gate and publish a
// stopped report. Returns false when the worker is unknown or already terminal.
// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop_worker
func (r *ColonyRuntime) StopWorker(id string) bool {
	worker := r.GetWorker(id)
	if worker == nil {
		return false
	}
	cancel, active := worker.requestStop()
	if !active {
		return false
	}
	if cancel != nil {
		cancel()
	}
	return true
}

func (r *ColonyRuntime) runWorker(ctx context.Context, worker *Worker) {
	if err := ctx.Err(); err != nil {
		r.finishWorkerStopped(worker, "Worker was stopped before it started.")
		return
	}

	spec := r.agentSpec
	if worker.Context == nil {
		worker.Context = r.buildWorkerContext(worker, spec, r.applyToolAllowlist(r.tools), r.workerStorage(worker.ID), nil)
	}
	conv := agentloop.NewNodeConversation(systemPromptFor(spec))
	if worker.Task != "" {
		_, _ = conv.AddUserMessage(worker.Task, agentloop.UserMessageOptions{})
	} else {
		_, _ = conv.AddUserMessage("Hello", agentloop.UserMessageOptions{})
	}
	worker.conversation = conv

	cfg := worker.loopConfig
	if cfg == nil {
		built, err := BuildWorkerLoopConfig(worker.loopOverrides)
		if err != nil {
			errorText := err.Error()
			worker.finishOnce(WorkerFailed, WorkerResult{Error: &errorText, Output: map[string]any{}}, func() {
				r.publishFailed(worker, errorText)
			})
			return
		}
		cfg = &built
	}
	loop := &agentloop.AgentLoop{
		ToolExecutor: r.executorFor(worker),
		Config:       *cfg,
	}
	worker.SetAgentLoop(loop)
	r.ApplyAdaptiveBudget(worker)

	turn, err := loop.ExecuteTurn(ctx, worker.Context, conv, r.workerTools(worker))

	if err != nil {
		if ctx.Err() != nil {
			r.finishWorkerStopped(worker, "Worker was cancelled before completion.")
			return
		}
		errorText := err.Error()
		worker.finishOnce(WorkerFailed, r.workerTerminalResult(worker, map[string]any{}, &errorText), func() {
			r.publishFailed(worker, errorText)
		})
		return
	}

	worker.finishOnce(WorkerCompleted, r.workerTerminalResult(worker, map[string]any{"text": turn.FinalText}, nil), func() {
		r.publishReport(worker, turn.FinalText)
	})
}

// Parity: framework/host/worker.py::Worker._build_result (telemetry)
func (r *ColonyRuntime) workerTerminalResult(worker *Worker, output map[string]any, errorText *string) WorkerResult {
	return WorkerResult{
		Output:        output,
		Error:         errorText,
		ToolCallsUsed: worker.LoopToolCallsUsed(),
		BudgetLimited: worker.LoopBudgetLimited(),
	}
}

func (r *ColonyRuntime) runOverseer(ctx context.Context, worker *Worker, seed []SeedMessage) {
	worker.SetStatus(WorkerRunning)
	worker.MarkStarted(time.Now())
	if err := writeSeedParts(r.storagePath, worker.ID, seed); err != nil {
		errorText := err.Error()
		worker.Finish(WorkerFailed, WorkerResult{Error: &errorText})
		return
	}
	if worker.conversation == nil {
		worker.conversation = agentloop.NewNodeConversation(systemPromptFor(r.agentSpec))
	}
	if worker.Context == nil {
		worker.Context = r.buildContext(worker, r.agentSpec)
	}

	loop := &agentloop.AgentLoop{
		ToolExecutor: r.executorFor(worker),
		Config:       agentloop.DefaultLoopConfig(),
	}
	if err := ctx.Err(); err == nil {
		_, _ = loop.ExecuteTurn(ctx, worker.Context, worker.conversation, r.workerTools(worker))
	}

	<-ctx.Done()
	worker.Finish(WorkerStopped, WorkerResult{Output: map[string]any{}})
}

func (r *ColonyRuntime) publishReport(worker *Worker, finalText string) {
	payload := worker.reportPayload()
	payload["colony_id"] = r.streamID
	summary := reportSummary(worker, finalText)
	payload["summary"] = summary
	payload["task"] = worker.Task
	worker.setResultSummary(summary)
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.maybeAdaptColonyBudget(payload)
	r.publishEvent(worker, payload)

	if r.bus != nil {
		executionID := ""
		if worker.Context != nil {
			executionID = worker.Context.ExecutionID
		}
		r.bus.EmitExecutionCompleted(worker.StreamID, executionID, map[string]any{"status": payload["status"]}, nil, nil)
	}
}

func (r *ColonyRuntime) publishFailed(worker *Worker, errorText string) {
	payload := map[string]any{
		"worker_id": worker.ID,
		"colony_id": r.streamID,
		"task":      worker.Task,
		"status":    "failed",
		"summary":   "Worker crashed: " + errorText,
		"data":      map[string]any{},
		"error":     errorText,
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.publishEvent(worker, payload)

	if r.bus != nil {
		executionID := ""
		if worker.Context != nil {
			executionID = worker.Context.ExecutionID
		}
		r.bus.EmitExecutionFailed(worker.StreamID, executionID, errorText, nil, nil)
	}
}

func (r *ColonyRuntime) publishStopped(worker *Worker, summary string) {
	payload := map[string]any{
		"worker_id": worker.ID,
		"colony_id": r.streamID,
		"task":      worker.Task,
		"status":    "stopped",
		"summary":   summary,
		"data":      map[string]any{},
		"error":     nil,
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.publishEvent(worker, payload)
}

func (r *ColonyRuntime) publishEvent(worker *Worker, payload map[string]any) {
	if r.bus == nil {
		return
	}
	event := eventbus.NewAgentEvent(eventbus.EventSubagentReport, worker.StreamID)
	event.Data = payload
	colonyID := r.streamID
	event.ColonyID = &colonyID
	r.bus.Publish(event)
}

func reportSummary(worker *Worker, finalText string) string {
	if report := worker.ExplicitReport(); report != nil {
		if summary, ok := report["summary"].(string); ok {
			return summary
		}
	}
	return finalText
}

func writeSeedParts(storagePath, workerID string, seed []SeedMessage) error {
	if len(seed) == 0 {
		return nil
	}
	dir := filepath.Join(storagePath, "workers", workerID, "parts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for index, message := range seed {
		part := map[string]any{
			"seq":     message.Seq,
			"role":    message.Role,
			"content": message.Content,
		}
		data, err := json.Marshal(part)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%d.json", index)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

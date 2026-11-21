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
	"sync/atomic"
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
	// DeclaredTools is the optional tool pool a worker definition's "tools"
	// list selects from (integration/MCP tools the caller resolved). A worker
	// only receives pool entries it declares; an empty declaration keeps the
	// colony's default worker surface. Nil is the same as empty.
	DeclaredTools []llm.ToolDef
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
	// Binding is the colony's tracker binding. Hosts that already resolved
	// one (fork flow, tests) pass it here; otherwise the runtime derives it
	// from StoragePath when that path is a materialised colony directory.
	Binding *tracker.Binding
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
	// Goal is the plain-language title the dashboard shows for the worker.
	// It is metadata only: the worker never sees it.
	Goal string
	// PreloadTools promotes the named tools into the worker's eager set at
	// spawn, skipping its search_tools round trip. Unknown names are ignored.
	PreloadTools []string
	// ReportSchema is the playbook receipt contract (worker(schema=...)):
	// it specializes the worker's report_to_parent `data` parameter and its
	// system prompt — upstream's spawn_batch `report_schema` entry.
	ReportSchema map[string]any
}

type SpawnBatchOptions struct {
	LoopConfigOverrides map[string]any
	// BatchID correlates the workers (and their reports) with the queen's
	// run_worker call. Empty lets the runtime mint one for multi-task batches.
	BatchID string
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
	// ReportSchema specializes report_to_parent and the worker prompt for a
	// playbook dispatch (upstream spawn spec `report_schema`).
	ReportSchema map[string]any
}

type SeedMessage struct {
	Seq     int
	Role    string
	Content string
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime (orchestration subset)
type ColonyRuntime struct {
	agentSpec     agentloop.AgentSpec
	goal          *schemas.Goal
	storagePath   string
	llm           llm.Completer
	tools         []llm.ToolDef
	declaredTools []llm.ToolDef
	toolExec      func(agentloop.ToolCallRequest) (agentloop.ToolResult, error)
	bus           *eventbus.EventBus
	streamID      string
	keepSet       func() map[string]struct{}

	toolsMu         sync.RWMutex
	enabledMCPTools []string
	mcpToolNamesAll map[string]struct{}

	workerLLM       llm.Completer
	skillsProvider  func() ([]*skills.ParsedSkill, bool)
	toolProviderMap map[string]string
	// skillsCatalogMu guards the memoised colony catalog prompt in-flight
	// workers read each iteration; ReloadSkills swaps it (skills_surface.go).
	skillsCatalogMu     sync.RWMutex
	skillsCatalogPrompt string
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
	// workerSlots is the admission gate every spawned worker enters through.
	// Its capacity follows maxConcurrent: a playbook run raises both while it
	// executes (upstream parity, see worker_slots.go).
	workerSlots *workerSlotGate

	adaptiveEnabled bool

	// trackerBinding is the colony's shared ledger binding. It is resolved at
	// Start/spawn (or injected through RuntimeOptions.Binding) and read by
	// every worker's InputData and tracker tool call.
	trackerMu      sync.Mutex
	trackerBinding *tracker.Binding

	budgetMu         sync.Mutex
	budgetSamples    []int
	budgetApplied    int
	budgetAppliedSet bool
	budgetMaxSuccess int

	// overseerTasks is the persistent overseer's own session task list. It
	// lives under the colony directory so the colony stays one portable unit.
	overseerTasks *overseerTaskSet

	// overseerQuestions is the question the persistent overseer parked on with
	// ask_user. It is the colony's runtime half of the shared park: the chat
	// runtime persists the same question as pending_input in the session
	// cursor, and the answer clears both. Guarded by mu.
	overseerQuestions []map[string]any
	overseerParkedAt  time.Time

	// improvementSeq keeps signal ids unique within the process.
	improvementSeq atomic.Int64
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
		declaredTools: append([]llm.ToolDef(nil), opts.DeclaredTools...),
		toolExec:      opts.ToolExecutor,
		bus:           opts.EventBus,
		streamID:      opts.StreamID,
		keepSet:       opts.WorkerKeepSet,
		workers:       map[string]*Worker{},
		maxConcurrent: maxConcurrent,
		workerSlots:   newWorkerSlotGate(maxConcurrent),
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
	runtime.overseerTasks = newOverseerTaskSet(storage)
	if runtime.skillsProvider == nil {
		// No host passed a provider, but a colony that owns a skills/ folder
		// still has a catalog: read it straight off disk so the colony skills
		// route and the worker prompt agree (upstream's SkillsManager loads
		// the same-scope folder on demand).
		runtime.skillsProvider = runtime.colonySkills
	}
	if opts.Binding != nil {
		copied := *opts.Binding
		runtime.trackerBinding = &copied
	}
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
	maxConcurrent := r.maxConcurrent
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
		"max_concurrent_workers": maxConcurrent,
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
		spawnTools = appendMissingToolDefs(r.workerToolDefs(), skills.SkillViewToolDef(), skills.ListSkillsToolDef())
	}
	worker := NewWorker(opts.WorkerID, opts.Task, streamID)
	worker.ProfileName = opts.ProfileName
	worker.ReportSchema = opts.ReportSchema
	worker.StoragePath = storage
	worker.OutputFile = filepath.Join(storage, workerConversationsDir)
	worker.Context = r.buildWorkerContext(worker, r.specForWorker(worker), spawnTools, storage, opts.PreloadTools)
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
	// Colony skills are both discoverable (catalog prompt) and readable
	// (SkillDirs turns view_file in the skill folder into skill content).
	// write_skill's colony scope writes to exactly this folder.
	if skillDirs := r.workerSkillDirs(); len(skillDirs) > 0 {
		ctx.SkillDirs = skillDirs
		ctx.SkillsCatalogPrompt = r.workerSkillsCatalogPrompt()
		// Upstream spawns workers with dynamic_skills_catalog_provider set to
		// the runtime's live catalog, so an in-flight worker picks up a reload
		// on its next iteration instead of keeping the spawn-time prompt.
		ctx.DynamicSkillsCatalogProvider = func() string {
			return r.cachedSkillsCatalogPrompt()
		}
	}
	// The worker's escalation channel is automatic: the tool doom-loop
	// detector publishes through this sink (the worker itself has no escalate
	// tool, matching upstream's parallel-worker surface). The queen session
	// subscribes colony-scoped, records the request_id and surfaces the
	// handoff; reply_to_worker then injects guidance back here if the worker
	// is still alive.
	//
	// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
	// loop -> emit_escalation_requested)
	ctx.EscalationSink = r.escalationSinkForWorker()

	keepSet := r.workerKeepSet()
	if len(keepSet) == 0 {
		return ctx
	}
	alwaysEnabled := make(map[string]struct{}, len(keepSet)+2)
	for name := range keepSet {
		alwaysEnabled[name] = struct{}{}
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
		AlwaysEnabledNames: alwaysEnabled,
		GateableNames:      r.gateableNames(),
		PersistPath:        filepath.Join(storage, "tool_tiers.json"),
	}
	// Skill reads are part of the worker's core orientation surface: keep them
	// eager whenever the spawn pool carries them, so a task naming a skill does
	// not cost a search_tools round first.
	for _, name := range []string{skills.SkillViewToolName, skills.ListSkillsToolName} {
		if _, ok := registered[name]; ok {
			tier.AlwaysEnabledNames[name] = struct{}{}
		}
	}
	// A worker's own task plan is part of its orientation surface too: making
	// it search before it can create its first task would cost a round trip
	// for the tool upstream ships eager on every worker.
	for _, name := range workerTaskNames() {
		if _, ok := registered[name]; ok {
			tier.AlwaysEnabledNames[name] = struct{}{}
		}
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
	// The colony's ledger exists before any worker can be spawned: upstream
	// provisions tracker.db in the fork flow, and run_worker refuses to fan
	// out without it.
	if err := r.provisionTracker(); err != nil {
		return fmt.Errorf("start colony %q: provision tracker.db: %w", r.streamID, err)
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.started = true
	r.running = true
	r.mu.Unlock()
	// HIVE_SPLIT_WORKER_EVENTS moves a parallel worker's per-turn chatter out
	// of the queen's event log and into <worker storage>/events.jsonl. Off by
	// default (the dashboard must render worker bubbles from META alone before
	// this is safe to turn on), exactly as upstream gates it.
	//
	// Parity: core/framework/host/colony_runtime.py::ColonyRuntime._install_worker_log_resolver
	r.enableWorkerEventLogs()
	// Restart recovery runs after the runtime is marked started (the workers it
	// rebuilds use runtimeContext) but before the caller starts the overseer,
	// so a resumed worker is already queued when the colony comes up.
	r.ResumeInFlightWorkers(r.ctx)
	return nil
}

// enableWorkerEventLogs installs the per-worker event-log resolver when
// HIVE_SPLIT_WORKER_EVENTS opts in. Only "worker:<uuid>" streams have a
// directory of their own; the bare "worker" tag used by single spawns falls
// back to the queen's log.
func (r *ColonyRuntime) enableWorkerEventLogs() {
	if r.bus == nil || !splitWorkerEventsEnabled() {
		return
	}
	r.bus.SetWorkerLogResolver(func(streamID string) (string, bool) {
		if !strings.HasPrefix(streamID, "worker:") {
			return "", false
		}
		workerID := strings.TrimSpace(streamID[len("worker:"):])
		if workerID == "" {
			return "", false
		}
		return filepath.Join(r.workerStorage(workerID), "events.jsonl"), true
	})
}

// splitWorkerEventsEnabled reads HIVE_SPLIT_WORKER_EVENTS with upstream's
// allow-list: only 1/true/yes (case-insensitive) enable the split.
//
// Parity: core/framework/host/colony_runtime.py (HIVE_SPLIT_WORKER_EVENTS)
func splitWorkerEventsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HIVE_SPLIT_WORKER_EVENTS"))) {
	case "1", "true", "yes":
		return true
	}
	return false
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
	if err := r.provisionTracker(); err != nil {
		return nil, fmt.Errorf("spawn in colony %q: provision tracker.db: %w", r.streamID, err)
	}
	runCtx := r.runtimeContext(ctx)
	batchID := strings.TrimSpace(opts.BatchID)
	if batchID == "" && len(tasks) > 1 {
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
			WorkerID:     ids[index],
			Task:         task.Task,
			StreamID:     task.StreamID,
			InputData:    r.workerInputData(task.Data, ids[index], task.Task),
			ProfileName:  task.ProfileName,
			PreloadTools: task.PreloadTools,
			ReportSchema: task.ReportSchema,
		})
		worker.Goal = task.Goal
		worker.BatchID = batchID
		worker.BatchIndex = index
		worker.BatchSize = len(tasks)
		worker.loopOverrides = opts.LoopConfigOverrides
		workerConfig := cfg
		worker.loopConfig = &workerConfig
		worker.BudgetPinned = pinned
		// Lineage breadcrumb: resume needs the original task/profile, and the
		// colony id is the cross-colony guard a later resume checks.
		r.writeWorkerMeta(worker)
		workers = append(workers, worker)
	}

	r.mu.Lock()
	for _, worker := range workers {
		r.workers[worker.ID] = worker
	}
	r.mu.Unlock()

	for _, worker := range workers {
		r.startWorker(runCtx, worker)
	}
	return ids, nil
}

// startWorker launches one worker under the colony's admission gate. Every
// worker gets its own context so a single run/worker can be stopped without
// touching its siblings; resumed workers enter through the same path.
func (r *ColonyRuntime) startWorker(runCtx context.Context, worker *Worker) {
	// Every worker enters the run-state record here, including a resumed one
	// whose meta predates the record: the restarted colony must see it as
	// in flight until it reaches a terminal status.
	r.writeWorkerRunState(worker, string(WorkerQueued))
	workerCtx, cancel := context.WithCancel(runCtx)
	worker.setCancel(cancel)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		r.runGatedWorker(workerCtx, worker)
	}()
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
	worker.StoragePath = r.storagePath
	worker.OutputFile = filepath.Join(r.storagePath, workerConversationsDir)
	worker.Context = r.buildContext(worker, queenSpec)
	// The overseer's conversation lives at the colony root (upstream keeps it
	// at <colony>/conversations, not under a worker directory). A restarted
	// colony reloads that transcript; only a fresh colony replays the seed.
	store := agentloop.NewFileConversationStore(filepath.Join(r.storagePath, workerConversationsDir))
	conv, restored, err := restoreWorkerConversation(store)
	if err != nil || conv == nil {
		conv = agentloop.NewNodeConversationWithOptions(agentloop.ConversationOptions{
			SystemPrompt: systemPromptFor(queenSpec),
			Store:        store,
		})
		restored = false
	}
	if !restored {
		for _, message := range seedConversation {
			content := message.Content
			if message.Role == "assistant" {
				_, _ = conv.AddAssistantMessage(content, nil, agentloop.AssistantMessageOptions{})
				continue
			}
			_, _ = conv.AddUserMessage(content, agentloop.UserMessageOptions{})
		}
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
	for {
		r.nextID++
		id := fmt.Sprintf("%s_%d", prefix, r.nextID)
		// A resumed worker brings an id from an earlier run; a fresh spawn in
		// the same process must not mint the same one.
		if _, taken := r.workers[id]; !taken {
			return id
		}
	}
}

func systemPromptFor(spec agentloop.AgentSpec) string {
	if spec.SystemPrompt != nil {
		return *spec.SystemPrompt
	}
	return "You are " + spec.Name + "."
}

func (r *ColonyRuntime) buildContext(worker *Worker, spec agentloop.AgentSpec) *agentloop.AgentContext {
	colonyID := r.streamID
	// Workers run on the dedicated worker model when one is configured
	// (worker_llm); the persistent overseer is the colony's own Queen and
	// keeps the colony completer. The split mirrors upstream, where a worker
	// spawned through run_worker inherits the colony runtime's `_llm` — the
	// provider SessionManager built from worker_llm (falling back to
	// session.llm when the slot is unset).
	completer := r.llm
	if worker != nil && !worker.Persistent {
		completer = r.workerCompleter()
	}
	ctx := &agentloop.AgentContext{
		AgentID:   spec.ID,
		AgentSpec: spec,
		LLM:       completer,
		StreamID:  worker.StreamID,
		ColonyID:  &colonyID,
	}
	// Workers carry the colony goal into their prompt: criteria, MUST/SHOULD
	// constraints, and context. The persistent overseer keeps an empty goal
	// context (upstream StartOverseer sets goal_context=""), so the worker
	// goal never leaks into the queen's identity.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
	// (goal_context=self._goal.to_prompt_context()); framework/host/
	// colony_runtime.py::start_overseer (goal_context="").
	if !worker.Persistent && r.goal != nil {
		ctx.GoalContext = r.goal.ToPromptContext()
	}
	ctx.StopAfterToolBatch = func() bool { return worker.ExplicitReport() != nil }
	return ctx
}

// specForWorker is the agent spec a worker runs under: the colony's spec with
// the playbook receipt schema overlaid when the dispatch carried one
// (upstream's spawn_batch `report_schema` entry specializes the worker's
// report tool AND its system prompt).
func (r *ColonyRuntime) specForWorker(worker *Worker) agentloop.AgentSpec {
	if r == nil {
		return agentloop.AgentSpec{}
	}
	spec := r.agentSpec
	if worker != nil && len(worker.ReportSchema) > 0 {
		spec.ReportSchema = worker.ReportSchema
	}
	return spec
}

// reportSchemaFor is the receipt schema the worker's report_to_parent tool
// advertises: the dispatch's schema when present, else the colony's.
func (r *ColonyRuntime) reportSchemaFor(worker *Worker) map[string]any {
	if worker != nil && len(worker.ReportSchema) > 0 {
		return worker.ReportSchema
	}
	if r == nil {
		return nil
	}
	return r.agentSpec.ReportSchema
}

// workerCompleter is the completer a worker and its judge use: the colony's
// worker model when configured, otherwise the colony completer.
func (r *ColonyRuntime) workerCompleter() llm.Completer {
	if r == nil {
		return nil
	}
	if r.workerLLM != nil {
		return r.workerLLM
	}
	return r.llm
}

// Parity: framework/agent_loop/agent_loop.py::AgentLoop._reports_to_parent (worker
// stream_id gate for build_report_to_parent_tool)
func (r *ColonyRuntime) workerTools(worker *Worker) []llm.ToolDef {
	// The persistent overseer is the colony's own Queen: it runs with the
	// dispatch/state tools a chat Queen gets (run_worker, the queen tracker
	// table, its task list) instead of the worker surface.
	if worker.Persistent {
		return r.overseerToolDefs()
	}
	if !strings.HasPrefix(worker.StreamID, "worker:") {
		return r.applyToolAllowlist(r.tools)
	}
	// worker.json's declared tool set selects the worker's optional tools
	// (integration/MCP); the overseer keeps the colony surface untouched.
	tools := r.baseWorkerTools()
	// Workers get the worker-role tracker table: tracker_upsert is their
	// durable progress channel and tracker_query their read surface. The
	// queen-only pair stays out.
	tools = appendMissingToolDefs(tools, trackerToolDefs("worker")...)
	tools = appendMissingToolDefs(tools, agentloop.BuildReportToParentTool(r.reportSchemaFor(worker)))
	// Parallel workers fail fast, not escalate. Upstream 1.0.14 only ships the
	// escalate tool to the legacy primary worker (stream_id="worker", no
	// colon); a worker that reports to a parent (stream_id="worker:<id>", the
	// run_worker shape) gets report_to_parent instead. Every colony worker this
	// port spawns is the worker:<id> shape, so the escalate tool stays off the
	// surface. A blocked worker surfaces the blocker in its report (status
	// failed/partial); a tool doom loop is escalated automatically by the loop,
	// which is the one worker escalation producer upstream leaves open.
	//
	// Parity: framework/agent_loop/agent_loop.py::_execute_impl (tool surface
	// gate: is_parallel_worker / _reports_to_parent)
	tools = withoutTool(tools, agentloop.EscalateToolName)
	// Every worker keeps its own task list: task_create/update/list/get is how
	// it decomposes the assignment it was handed.
	tools = appendMissingToolDefs(tools, workerTaskToolDefs()...)
	// Workers read skills (the queen authors them); skill_view/list_skills
	// complement the SkillDirs view_file interceptor so a skill is reachable
	// without guessing its filesystem path.
	tools = appendMissingToolDefs(tools, skills.SkillViewToolDef(), skills.ListSkillsToolDef())
	return tools
}

// baseWorkerTools is the surface every worker starts from: the colony's
// configured tools after the MCP allowlist, merged with the tools the worker
// definition declares (worker.json "tools").
//
// Parity: framework/loader/agent_loader.py::AgentLoader.load (the config's
// tools list becomes the worker node's declared tool set)
func (r *ColonyRuntime) baseWorkerTools() []llm.ToolDef {
	base := selectWorkerDeclaredTools(r.applyToolAllowlist(r.tools), r.applyToolAllowlist(r.declaredTools), r.agentSpec.Tools)
	// run_playbook is the queen's batch driver; a worker must not recurse into
	// a converge loop of its own. The session catalogue also feeds the colony's
	// declared-tool pool, so filter the name even when worker.json asks for it.
	return withoutTool(base, RunPlaybookToolName)
}

func (r *ColonyRuntime) executorFor(worker *Worker) func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	var trackerRegistry *tracker.Registry
	var overseerTracker *tracker.Registry
	if strings.HasPrefix(worker.StreamID, "worker:") {
		trackerRegistry = r.workerTrackerRegistry(worker)
	}
	// Task tools act on the worker's own plan, so the binding is per worker:
	// its own directory, its own list.
	var taskTools *workerTaskToolSet
	if worker.ID != "" && !worker.Persistent {
		workerDir := worker.StoragePath
		if workerDir == "" {
			workerDir = r.workerStorage(worker.ID)
		}
		taskTools = newWorkerTaskToolSet(workerDir, worker.ID)
	}
	if worker.Persistent {
		// The overseer's tracker tools are the queen table over the colony's
		// shared ledger; its run_worker dispatches through this runtime.
		overseerTracker = r.overseerTrackerRegistry()
	}
	return func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		if req.Name == "report_to_parent" && strings.HasPrefix(worker.StreamID, "worker:") {
			worker.RecordExplicitReport(req.Input)
			return agentloop.ToolResult{Content: "Report delivered to the overseer."}, nil
		}
		if worker.Persistent {
			if result, ok := r.executeOverseerTool(overseerTracker, req); ok {
				return result, nil
			}
		}
		// Tracker tools own their own registry: the worker's binding comes
		// from input_data, so the call targets the colony's shared ledger.
		if trackerRegistry != nil {
			if result, ok := r.executeTrackerTool(trackerRegistry, req); ok {
				return result, nil
			}
		}
		// The worker's own task plan. A successful call is a normal result
		// carrying success=false for a rejected operation, so is_error stays
		// false, exactly like the queen's session task tools.
		if content, isError, handled := taskTools.Execute(req.Name, req.Input); handled {
			return agentloop.ToolResult{Content: content, IsError: isError}, nil
		}
		// Skill reads are filesystem reads scoped to the colony's skills/
		// folder; the worker's own dirs are the only authority (never the
		// queen's private skill tree).
		if content, isError, handled := skills.ExecuteAgentTool(req.Name, req.Input, skills.ToolDirs{
			ColonyDir:  r.storagePath,
			SessionDir: r.storagePath,
		}); handled {
			return agentloop.ToolResult{Content: content, IsError: isError}, nil
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
	r.writeWorkerRunState(worker, string(WorkerRunning))
	r.publishStarted(worker)
	r.runWorker(ctx, worker)
}

func (r *ColonyRuntime) acquireWorkerSlot(ctx context.Context) bool {
	return r.workerSlots.acquire(ctx)
}

func (r *ColonyRuntime) releaseWorkerSlot() {
	r.workerSlots.release()
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

	spec := r.specForWorker(worker)
	if worker.Context == nil {
		worker.Context = r.buildWorkerContext(worker, spec, r.workerToolDefs(), r.workerStorage(worker.ID), nil)
	}
	// The worker's transcript is persisted before the first LLM call, so a
	// worker stopped mid-run can be resumed from exactly where it stopped.
	// A fresh worker owns an empty store: stray parts from a previous process
	// that reused this id must not leak into its conversation.
	conv := worker.conversation
	if conv == nil {
		store := r.workerConversationStore(worker.ID)
		_ = store.Destroy()
		conv = agentloop.NewNodeConversationWithOptions(agentloop.ConversationOptions{
			SystemPrompt: r.workerSystemPrompt(worker, spec),
			Store:        store,
		})
		if worker.Task != "" {
			_, _ = conv.AddUserMessage(worker.Task, agentloop.UserMessageOptions{})
		} else {
			_, _ = conv.AddUserMessage("Hello", agentloop.UserMessageOptions{})
		}
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
	tools := r.workerTools(worker)
	loop, stopHub := r.newWorkerAgentLoop(worker, *cfg, tools)
	defer stopHub()
	// The deterministic doom-loop detection is published as
	// node_tool_doom_loop at the threshold that also raises the escalation, so
	// the session event log and a watching SSE client see the repetition.
	loop.DoomLoopSink = r.workerDoomLoopSink(worker)
	// A goal that declares criteria but whose spec carries no compiled
	// criteria paragraph still gets a judge: the criteria come from the goal.
	judgeSpec := worker.Context.AgentSpec
	if !agentloop.HasSuccessCriteria(judgeSpec) {
		if goalCriteria := agentloop.GoalCriteriaText(r.goal); goalCriteria != nil {
			judgeSpec.SuccessCriteria = goalCriteria
		}
	}
	loop.ConfigureJudge(judgeSpec, r.workerCompleter(), r.judgeEventWriter())
	// The judge's RETRY/ESCALATE verdict is the in-band miss the improvement
	// loop consumes; record it as soon as the loop normalizes it.
	loop.OnJudgeVerdict = func(verdict agentloop.JudgeVerdict, iteration int) {
		r.recordJudgeImprovementSignal(worker, verdict, iteration)
	}
	worker.SetAgentLoop(loop)
	r.ApplyAdaptiveBudget(worker)

	// Publish the worker's live turn progress on the colony bus. The session
	// bridge re-stamps text/tool/token events onto the session stream with
	// relayed_from=<worker id>, so the worker is visible while it runs.
	previousObserver := worker.Context.Observer
	if r.bus != nil {
		executionID := worker.Context.ExecutionID
		if executionID == "" {
			executionID = worker.ID
		}
		worker.Context.Observer = newWorkerEmitter(r.bus, worker.StreamID, executionID)
	}

	turn, err := loop.ExecuteTurn(ctx, worker.Context, conv, tools)
	worker.Context.Observer = previousObserver

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

	// Execute the goal's weighted success criteria against the worker's final
	// output and turn misses into improvement signals. No declared criteria
	// means no evaluation and no signal (nil is "nothing to execute").
	evaluation := r.evaluateWorkerGoal(ctx, worker, loop, turn)
	worker.SetGoalEvaluation(evaluation)
	r.recordGoalImprovementSignals(worker, evaluation)

	output := map[string]any{"text": turn.FinalText}
	if evaluation != nil {
		output["goal_evaluation"] = evaluation.Map()
	}
	worker.finishOnce(WorkerCompleted, r.workerTerminalResult(worker, output, nil), func() {
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

	// The overseer runs the same loop shape as a worker (hub + inferno guards
	// included); upstream builds a full AgentLoop for it too.
	loop := agentloop.NewAgentLoop(agentloop.DefaultLoopConfig(), r.executorFor(worker))
	// The overseer is the colony's lead: give it the queen-role reminder
	// sources its own loop was missing (fleet, tracker, parallel nudge,
	// stream-stall). No ticker is started (see overseer_reminders.go).
	stopReminders := r.startOverseerReminders(loop, worker, loop.Config, r.workerTools(worker))
	defer stopReminders()
	// The overseer is the colony's lead: when the queen spec carries no
	// criteria of its own it inherits the colony goal's, exactly like a
	// worker. With neither, ConfigureJudge leaves the judge off (no extra
	// model call on the overseer's turn).
	judgeSpec := worker.Context.AgentSpec
	if !agentloop.HasSuccessCriteria(judgeSpec) {
		if goalCriteria := agentloop.GoalCriteriaText(r.goal); goalCriteria != nil {
			judgeSpec.SuccessCriteria = goalCriteria
			worker.Context.AgentSpec.SuccessCriteria = goalCriteria
		}
	}
	loop.ConfigureJudge(judgeSpec, r.workerCompleter(), r.judgeEventWriter())
	// Worker.Inject routes new input through this loop, so an overseer that
	// sits between turns still wakes on the next message.
	worker.SetAgentLoop(loop)

	// First turn: reply to whatever the colony seeded (the boot greeting the
	// previous single-turn overseer emitted). Afterwards the overseer is a
	// persistent Queen that waits for injected messages and answers each one.
	turn := 0
	// runTurn brackets one overseer turn with the client-facing execution
	// events and attaches the text emitter, so the session that front-ends the
	// colony sees the reply rather than a silent worker.
	runTurn := func() {
		if ctx.Err() != nil {
			return
		}
		turn++
		emitter := newOverseerEmitter(r.bus, worker.StreamID, fmt.Sprintf("%s_turn_%d", worker.ID, turn))
		previous := worker.Context.Observer
		worker.Context.Observer = emitter
		emitter.begin()
		_, err := loop.ExecuteTurn(ctx, worker.Context, worker.conversation, r.workerTools(worker))
		if err != nil && ctx.Err() != nil {
			// A cancelled turn is the colony stopping, not a failure worth
			// broadcasting to the session front end.
			err = nil
		}
		emitter.end(err, r.overseerTurnParkReason(err))
		worker.Context.Observer = previous
	}
	if ctx.Err() == nil {
		runTurn()
	}
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-worker.wakeCh():
		}
		if ctx.Err() != nil {
			break
		}
		pending := worker.takePendingInput()
		if len(pending) == 0 {
			continue
		}
		for _, text := range pending {
			// Same framing the loop gives a client injection, so the model
			// reads the overseer's messages exactly like the queen's.
			stamped := fmt.Sprintf("[%s] %s", time.Now().Format("2006-01-02 15:04 MST"), text)
			if _, err := worker.conversation.AddUserMessage(stamped, agentloop.UserMessageOptions{IsClientInput: true}); err != nil {
				break
			}
		}
		runTurn()
	}
	worker.finishOnce(WorkerStopped, WorkerResult{Output: map[string]any{}}, func() {
		r.publishStopped(worker, "Overseer stopped with the colony.")
	})
}

func (r *ColonyRuntime) publishReport(worker *Worker, finalText string) {
	payload := worker.reportPayload()
	payload["colony_id"] = r.streamID
	summary := reportSummary(worker, finalText)
	payload["summary"] = summary
	payload["task"] = worker.Task
	worker.setResultSummary(summary)
	// The executed goal verdict and the improvement signals it produced ride
	// every terminal report, including the explicit-report path: a worker that
	// reports success while a weighted criterion missed must show both.
	if evaluation := worker.GoalEvaluation(); evaluation != nil {
		payload["goal_evaluation"] = evaluation.Map()
	}
	if signals := worker.improvementSignalMaps(); len(signals) > 0 {
		payload["improvement_signals"] = signals
	}
	if payload["goal_evaluation"] != nil || payload["improvement_signals"] != nil {
		payload["data"] = mergeReportData(payload["data"], payload["goal_evaluation"], payload["improvement_signals"])
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.maybeAdaptColonyBudget(payload)
	r.recordWorkerTerminal(worker, payload)
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
		"worker_id":        worker.ID,
		"colony_id":        r.streamID,
		"task":             worker.Task,
		"status":           "failed",
		"summary":          "Worker crashed: " + errorText,
		"data":             map[string]any{},
		"error":            errorText,
		"duration_seconds": worker.Result().DurationSeconds,
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.recordWorkerTerminal(worker, payload)
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
		"worker_id":        worker.ID,
		"colony_id":        r.streamID,
		"task":             worker.Task,
		"status":           "stopped",
		"summary":          summary,
		"data":             map[string]any{},
		"error":            nil,
		"duration_seconds": worker.Result().DurationSeconds,
	}
	if worker.BatchID != "" {
		payload["batch_id"] = worker.BatchID
		payload["batch_index"] = worker.BatchIndex
		payload["batch_size"] = worker.BatchSize
	}
	r.recordWorkerTerminal(worker, payload)
	r.publishEvent(worker, payload)
}

// recordWorkerTerminal persists the terminal report beside the transcript
// (result.json + one events.jsonl line). Every terminal path — success,
// failure, stop — goes through here, so a later resume or audit finds the same
// payload the queen received.
func (r *ColonyRuntime) recordWorkerTerminal(worker *Worker, payload map[string]any) {
	// Terminal first, so a restart between the report and the artefact writes
	// never sees the worker as in flight and resumes finished work.
	r.writeWorkerRunState(worker, workerStateFromReport(stringValue(payload["status"])))
	r.persistWorkerResult(worker, payload)
	r.appendWorkerEvent(worker, payload)
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

package colony

// Parity: framework/host/colony_runtime.py

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/pipeline"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// DefaultReportWait is the fallback wait when a caller passes no timeout.
// Upstream's ColonyRuntime.wait_for_worker_reports defaults to 600.0 seconds
// (ten minutes); the port used to stop at 60s, which cut long worker batches
// short and pushed the queen into an extra model turn to poll again.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.wait_for_worker_reports
const DefaultReportWait = 600 * time.Second

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
	// WorkerSeq is the run-scoped dispatch ordinal a playbook run stamps on
	// each of its size-1 batches, so peers that share one batch_id stay
	// individually identifiable. 0 for ordinary fan-out.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
	// (spec["worker_seq"])
	WorkerSeq int
}

type SpawnBatchOptions struct {
	LoopConfigOverrides map[string]any
	// BatchID correlates the workers (and their reports) with the queen's
	// run_worker call. Empty mints a fresh `rpw_…` id here, including for a
	// size-1 batch, matching spawn_batch.
	BatchID string
	// ProfileName is the batch-level worker-profile default. A task's own
	// ProfileName wins, matching spawn_batch's per-task override precedence.
	ProfileName string
	// solo marks the single-worker spawn() path, which upstream leaves
	// batch-less (spawn_batch always mints an id; spawn count=n does not).
	// It is unexported because it is an implementation detail of Spawn, not
	// part of the batch surface.
	solo bool
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
	// Integrations / BrowserProfile are the resolved profile binding the
	// spawn path applies (see Worker.Integrations / Worker.BrowserProfile).
	Integrations   map[string]string
	BrowserProfile string
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
	// workerExecFactory builds a worker's own tool executor: it receives the
	// worker's storage root and the colony-root executor to fall back to for
	// tools that are not rooted per worker. Nil keeps every worker on toolExec
	// (the colony-root executor). Installed before Start through
	// SetWorkerToolExecutorFactory.
	workerExecMu      sync.RWMutex
	workerExecFactory func(root string, base agentloop.ToolExecutor) agentloop.ToolExecutor
	// toolProber / toolKiller are the executor's liveness handles (see
	// RuntimeOptions.ToolProber): every loop this runtime builds (worker and
	// overseer) carries them so a timed-out tool call probes and reclaims its
	// shared MCP transport instead of leaking it.
	toolProber tools.LivenessProber
	toolKiller tools.ToolKiller
	bus        *eventbus.EventBus
	streamID   string
	// queenName is the id of the queen that owns this colony ("queen_growth",
	// or "" for a colony with no recorded queen). It is spawn metadata only:
	// meta.json records it so the UI can answer "whose worker is this?" after
	// the runtime is gone.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime._queen_id
	queenName string
	keepSet   func() map[string]struct{}

	toolsMu         sync.RWMutex
	enabledMCPTools []string
	mcpToolNamesAll map[string]struct{}

	workerLLM      llm.Completer
	skillsProvider func() ([]*skills.ParsedSkill, bool)
	// defaultPresetSkills is the preset-scope enable set applied to the worker
	// catalog. It is resolved once at construction from the owning queen's
	// profile (empty for a colony with no queen).
	defaultPresetSkills map[string]struct{}
	// queenDir is the owning queen's home; empty omits the queen_ui scope
	// (see RuntimeOptions.QueenDir).
	queenDir string
	// protocolsPrompt is the operational-protocols block workers and the
	// overseer carry (RuntimeOptions.ProtocolsPrompt; upstream's
	// ColonyRuntime.protocols_prompt). Empty drops the section.
	protocolsPrompt string
	toolProviderMap map[string]string
	// accountsPrompt is the connected-accounts block a credential_resolver
	// stage produced; it rides every worker/overseer system prompt.
	accountsPrompt string
	// credentialAccounts / connectedBrowserProfiles are the spawn-time
	// profile-application hooks (RuntimeOptions). Nil credentialAccounts
	// disables the MCP credential filter; nil connectedBrowserProfiles uses
	// the local bridge probe.
	credentialAccounts       func() []map[string]any
	connectedBrowserProfiles func() ([]string, error)
	// browserClose is the host-injected bridge closer the browser reaper
	// calls on a per-worker stop and on colony stop. Nil disables reaping —
	// the same degradation upstream has when its gcu browser tools are not
	// loaded. browserReaped records the workers whose tab group this runtime
	// already closed, so a per-worker stop overlapping a colony stop cannot
	// double-close it. Both guarded by mu.
	browserClose  CloseProfileContext
	browserReaped map[string]struct{}
	// stopTimeout bounds Stop's wait for workers to unwind. Zero uses
	// DefaultStopTimeout (upstream's STOP_TIMEOUT_SEC).
	stopTimeout time.Duration
	// skillsCatalogMu guards the memoised colony catalog prompt in-flight
	// workers read each iteration; ReloadSkills swaps it (skills_surface.go).
	skillsCatalogMu     sync.RWMutex
	skillsCatalogPrompt string
	// skillsWatcherMu guards the colony's hot-reload watcher (started in
	// Start, stopped in Stop). It polls the same origin roots
	// workerSkillSurface discovers and rebuilds the memo through
	// ReloadSkills, so a skill edit reaches in-flight workers like upstream's
	// SkillsManager.start_watching in ColonyRuntime.start.
	skillsWatcherMu     sync.Mutex
	skillsWatcher       *skills.Watcher
	skillsWatchInterval time.Duration
	skillsWatchDebounce time.Duration
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
	// background tracks fire-and-forget watchers (upstream's
	// _background_tasks): a panic is reported instead of taking the process
	// down, and Stop cancels what is still running.
	background *tasks.Tracker

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

	// decision owns the colony-wide storage every worker/overseer decision
	// stream persists through (decision_runtime.go). It materialises on the
	// first worker context, so a runtime with no storage root stays unwired.
	decision decisionRuntimes

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

	// triggerPipeline is the configuration-driven trigger middleware. Nil and
	// empty both mean "no stages": Trigger goes straight to the spawn.
	// pipelineErr records a configuration error (unknown stage type) that
	// fails startup loudly; pipelineInitialized guards the one-time
	// InitializeAll.
	triggerPipeline     *pipeline.Runner
	pipelineErr         error
	pipelineInitMu      sync.Mutex
	pipelineInitialized bool

	// Idempotency cache for Trigger (TTL 300 s, 10000 keys max, insertion
	// eviction), guarded by triggerIdemMu.
	triggerIdemMu    sync.Mutex
	idempotencyKeys  map[string]string
	idempotencyTimes map[string]time.Time
	idempotencyOrder []string
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
		decision:      decisionRuntimes{root: opts.StoragePath},
		llm:           opts.LLM,
		tools:         append([]llm.ToolDef(nil), opts.Tools...),
		declaredTools: append([]llm.ToolDef(nil), opts.DeclaredTools...),
		toolExec:      opts.ToolExecutor,
		toolProber:    opts.ToolProber,
		toolKiller:    opts.ToolKiller,
		bus:           opts.EventBus,
		streamID:      opts.StreamID,
		queenName:     strings.TrimSpace(opts.QueenName),
		keepSet:       opts.WorkerKeepSet,
		workers:       map[string]*Worker{},
		background:    tasks.NewTracker(opts.StreamID),
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
	runtime.skillsWatchInterval = opts.SkillsWatchInterval
	runtime.skillsWatchDebounce = opts.SkillsWatchDebounce
	runtime.skillsProvider = opts.SkillsProvider
	runtime.queenDir = strings.TrimSpace(opts.QueenDir)
	runtime.protocolsPrompt = opts.ProtocolsPrompt
	runtime.credentialAccounts = opts.CredentialAccounts
	runtime.connectedBrowserProfiles = opts.ConnectedBrowserProfiles
	runtime.browserClose = opts.BrowserClose
	runtime.browserReaped = map[string]struct{}{}
	runtime.stopTimeout = opts.StopTimeout
	runtime.defaultPresetSkills = opts.DefaultPresetSkills
	if runtime.defaultPresetSkills == nil {
		runtime.defaultPresetSkills = defaultPresetSkillsForQueenDir(runtime.queenDir)
	}
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
	// Trigger pipeline: injected stages win, otherwise configuration.json's
	// pipeline.stages is read now (upstream builds it in __init__ too). A
	// configuration error is kept and re-surfaces from Start/Trigger.
	if len(opts.PipelineStages) > 0 {
		runtime.triggerPipeline = pipeline.NewRunner(opts.PipelineStages)
	} else {
		runtime.triggerPipeline, runtime.pipelineErr = loadPipelineFromConfig()
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
	spawnTools = r.filterUnavailableMCPTools(spawnTools)
	worker := NewWorker(opts.WorkerID, opts.Task, streamID)
	worker.ProfileName = opts.ProfileName
	worker.Integrations = copyStringMap(opts.Integrations)
	worker.BrowserProfile = opts.BrowserProfile
	if worker.BrowserProfile == "" {
		worker.BrowserProfile = defaultBrowserProfile
	}
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
	// The worker's system prompt is rebuilt from this context before each
	// model call, so a skill written (or disabled) while the worker is in
	// flight reaches its next turn without a respawn. Reading through the
	// closure — not a snapshot — is what makes "base prompt + catalog +
	// protocols" one live assembly instead of three copied strings.
	//
	// Parity: framework/agent_loop/agent_loop.py::_execute_impl step 6b3
	// (dynamic prompt refresh -> build_system_prompt_parts_for_context)
	overrides := workerPromptOverrides(spec)
	ctx.DynamicSystemPromptProvider = func() (string, string) {
		return agentloop.BuildSystemPromptPartsForContext(ctx, overrides)
	}
	ctx.AvailableTools = append([]llm.ToolDef(nil), spawnTools...)
	// Colony skills are both discoverable (catalog prompt) and readable
	// (SkillDirs turns view_file in the skill folder into skill content).
	// write_skill's colony scope writes to exactly this folder.
	r.attachWorkerSkillSurface(ctx)
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
	// The trigger pipeline initializes once at startup: a configuration error
	// (unknown stage type) fails here, loudly, and a stage's Initialize
	// failure aborts startup instead of being swallowed. The default empty
	// pipeline is a no-op.
	if err := r.initializePipeline(); err != nil {
		return err
	}
	// Watch the same skill roots the worker surface discovers. A change
	// rebuilds the memo every in-flight worker reads through its dynamic
	// provider — upstream's ColonyRuntime.start -> SkillsManager.start_watching.
	r.startSkillsWatcher()
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

// Stop cancels every worker — the persistent overseer included — and waits,
// bounded (DefaultStopTimeout, 10 s), for them to reach a terminal state. A
// worker still running at the deadline is force-finished STOPPED so the
// registry converges; its browser tab group is then reaped.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop
func (r *ColonyRuntime) Stop() {
	r.stopSkillsWatcher()
	// Snapshot the live set before cancelling: that set is what shutdown owes a
	// browser reap, even for a worker whose goroutine reaches terminal while
	// Stop waits out the deadline.
	live := r.snapshotLiveWorkers()
	r.mu.Lock()
	cancel := r.cancel
	started := r.started
	background := r.background
	r.running = false
	r.mu.Unlock()
	if started && cancel != nil {
		cancel()
	}
	// Fire-and-forget watchers (batch timeouts) ride the runtime context; wait
	// for them within a bounded slice of the stop budget so none outlives Stop.
	if background != nil {
		background.CancelAll(time.Second)
	}
	live = r.awaitWorkersStopped(live)
	r.reapWorkerBrowsers(live)
	// A worker admitted in the instant before running flipped would not be in
	// the snapshot; converge it too instead of orphaning it in the registry.
	if late := liveWorkers(r.snapshotWorkers()); len(late) > 0 {
		timeout := r.effectiveStopTimeout()
		for _, worker := range late {
			r.forceStopWorker(worker, timeout)
		}
		r.reapWorkerBrowsers(late)
	}
}

// backgroundTasks returns the tracker that owns fire-and-forget watchers,
// creating it for a runtime assembled from a struct literal instead of New.
func (r *ColonyRuntime) backgroundTasks() *tasks.Tracker {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.background == nil {
		r.background = tasks.NewTracker(r.streamID)
	}
	return r.background
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
	// Upstream spawn() leaves batch_id empty (only spawn_batch mints one), so
	// the runtime is told not to stamp these workers with a batch.
	return r.SpawnBatchWithOptions(ctx, tasks, SpawnBatchOptions{solo: true})
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
func (r *ColonyRuntime) SpawnBatch(ctx context.Context, tasks []BatchTask) ([]string, error) {
	return r.SpawnBatchWithOptions(ctx, tasks, SpawnBatchOptions{})
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch (worker
func (r *ColonyRuntime) SpawnBatchWithOptions(ctx context.Context, tasks []BatchTask, opts SpawnBatchOptions) ([]string, error) {
	// Parity: Python's spawn raises when the runtime is not running. Follow
	// that for a stopped runtime; a never-started runtime still lazily arms its
	// context below, which embedded hosts (and the chat wiring) rely on.
	r.mu.Lock()
	stopped := r.started && !r.running
	r.mu.Unlock()
	if stopped {
		return nil, fmt.Errorf("ColonyRuntime is not running")
	}
	// Parity: spawn_batch funnels every worker through
	// _enqueue_or_admit_worker, whose first gate is _dispatch_blocked: a stop
	// sweep refuses the spawn (terminal STOPPED + synthesised report) instead
	// of re-opening admission. The block is released by the next user turn
	// (resume_dispatch), never by the spawn itself — auto-unblocking here let
	// a queen turn that was still unwinding spawn workers into the sweep.
	if err := r.provisionTracker(); err != nil {
		return nil, fmt.Errorf("spawn in colony %q: provision tracker.db: %w", r.streamID, err)
	}
	runCtx := r.runtimeContext(ctx)
	batchID := strings.TrimSpace(opts.BatchID)
	// Upstream mints a short timestamped batch id for every spawn_batch call,
	// including a size-1 one, so the queen-side report formatter can always
	// correlate a report back to its dispatch. A caller-supplied id wins (the
	// queen's run_worker and the playbook runner both mint their own), and a
	// solo spawn() keeps no batch at all (opts.solo) — that is the one path
	// upstream leaves batch-less.
	if batchID == "" && !opts.solo {
		batchID = newWorkerBatchID()
	}
	return r.spawnBatchOn(runCtx, tasks, opts, batchID)
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
			reports = append(reports, r.timedOutWorkerReport(worker))
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

// stopWaitGrace bounds how long the wait path gives a force-stopped worker to
// unwind, publish and close its terminal result. Upstream's stop_worker awaits
// the cancellation; the Go wait must stay bounded so a wedged worker cannot
// hang the queen's wait — a caller that hits the bound still gets the synthetic
// entry below.
const stopWaitGrace = 2 * time.Second

// stopTailMaxChars mirrors upstream's _STOP_TAIL_MAX_CHARS: the assistant
// excerpt attached to a timed-out worker's stopped report.
const stopTailMaxChars = 500

// timedOutWorkerReport is the wait path's timeout branch. Upstream force-stops
// each still-pending worker, then surfaces the worker's own result (explicit
// report or the canned cancel result) plus the tail of its last assistant
// message; the old Go port synthesised failed/timeout and left the worker
// running behind the queen's back.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.wait_for_worker_reports
// (timeout branch: stop_worker -> worker._result -> last_assistant_excerpt)
func (r *ColonyRuntime) timedOutWorkerReport(worker *Worker) map[string]any {
	if worker == nil {
		return map[string]any{"status": "failed", "error": "timeout"}
	}
	r.StopWorker(worker.ID)
	select {
	case <-worker.Done():
	case <-time.After(stopWaitGrace):
	}

	payload := worker.reportPayload()
	payload["worker_id"] = worker.ID
	if stringValue(payload["status"]) == "" {
		// Upstream: result.status or "timeout" — a worker whose handle exists
		// but never recorded a terminal status is the timeout case.
		payload["status"] = "timeout"
	}
	if _, ok := payload["error"]; !ok {
		result := worker.Result()
		if result.Error != nil && *result.Error != "" {
			payload["error"] = *result.Error
		} else {
			payload["error"] = "timeout"
		}
	}
	if stringValue(payload["summary"]) == "" {
		payload["summary"] = "Worker did not report within the configured timeout."
	}
	// Copy the data map before enriching it: reportPayload hands back the
	// worker's live result map, and the excerpt is a report-only annotation.
	data := map[string]any{}
	if existing, ok := payload["data"].(map[string]any); ok {
		for key, value := range existing {
			data[key] = value
		}
	}
	if stringValue(payload["status"]) == "stopped" {
		if excerpt := lastAssistantExcerpt(worker); excerpt != "" {
			data["last_assistant_excerpt"] = excerpt
		}
	}
	payload["data"] = data
	return payload
}

// lastAssistantExcerpt is the upstream stop-path enrichment: the most recent
// non-empty assistant turn, trimmed and capped at stopTailMaxChars characters
// (code points, with an ellipsis when truncated). Best-effort — a worker with
// no conversation yields "".
//
// Parity: framework/host/colony_runtime.py::_tail_last_assistant_message
func lastAssistantExcerpt(worker *Worker) string {
	if worker == nil {
		return ""
	}
	conv := worker.Conversation()
	if conv == nil {
		return ""
	}
	messages := conv.Messages()
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(messages[index].Content)
		if content == "" {
			continue
		}
		runes := []rune(content)
		if len(runes) > stopTailMaxChars {
			return string(runes[:stopTailMaxChars]) + "…"
		}
		return content
	}
	return ""
}

// StartOverseer starts the colony's persistent overseer: the runtime-side
// incarnation of the queen (a Worker with ID "overseer:<colony>"), not a
// second agent.
//
// Legacy (2026-11-26): the production serve path does not call this. Hive
// 1.0.15 starts the colony runtime without attaching an overseer — the
// colony-bound queen chat session is the executor (internal/cli/
// loader_colony_host.go::startColonyHost). This entry remains for upstream
// parity, self-host/e2e paths and tests; do not add new production callers.
// Removal is to be coordinated with upstream.
//
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
	// The overseer is the colony's client-facing queen and reads the same
	// phase-filtered hierarchy its workers do (upstream start_overseer passes
	// skills_catalog_prompt/skill_dirs and a dynamic provider).
	r.attachWorkerSkillSurface(worker.Context)
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

// workerCompleter is the completer a worker runs on: the colony's worker model
// when configured, otherwise the colony completer.
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
		return r.filterUnavailableMCPTools(r.applyToolAllowlist(r.tools))
	}
	// worker.json's declared tool set selects the worker's optional tools
	// (integration/MCP); the overseer keeps the colony surface untouched.
	// The spawn-time credential filter drops tools whose provider has no live
	// account, so the worker's prompt never advertises an unusable tool.
	tools := r.filterUnavailableMCPTools(r.baseWorkerTools())
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

// WorkerToolScope is the live worker tool surface run_worker hands to a
// resume: the port's counterpart of upstream tools_override. It is the same
// post-allowlist, credential-filtered surface a fresh spawn gets, so resuming
// a worker can never widen its tools beyond what the colony currently exposes.
//
// Parity: framework/tools/queen_lifecycle_tools.py::run_worker
// (tools_override_parallel built from colony._tools after queen-only and
// unavailable-credential filtering)
func (r *ColonyRuntime) WorkerToolScope() []llm.ToolDef {
	if r == nil {
		return nil
	}
	out := appendMissingToolDefs(r.workerToolDefs(), skills.SkillViewToolDef(), skills.ListSkillsToolDef())
	return r.filterUnavailableMCPTools(out)
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
	// a converge loop of its own. Its three control tools are queen-only too.
	// The session catalogue also feeds the colony's declared-tool pool, so
	// filter the names even when worker.json asks for them.
	base = withoutTool(base, RunPlaybookToolName)
	for _, def := range PlaybookControlToolDefs() {
		base = withoutTool(base, def.Name)
	}
	return base
}

// attachToolLiveness gives a loop this runtime builds the tool executor's
// probe/kill handles. On a tool timeout the loop probes the shared MCP
// connection first and only force-disconnects a server that is provably dead
// (or unprobeable), which is upstream's timeout recovery — reclaiming a hung
// MCP connection without killing a merely-slow shared transport. The executor
// this runtime receives is a closure and cannot carry the methods, so the
// handles travel separately. Nil handles are a no-op.
//
// Parity: framework/agent_loop/internals/tool_result_handler.py (timeout
// handler: probe_for_tool / kill_for_tool)
func (r *ColonyRuntime) attachToolLiveness(loop *agentloop.AgentLoop) {
	if r == nil || loop == nil {
		return
	}
	loop.ToolProber = r.toolProber
	loop.ToolKiller = r.toolKiller
}

// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker (executor
// closure; the probe/kill pair is attached through attachToolLiveness)
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
	// A worker's built-in file tools and terminal default cwd resolve under
	// its own storage directory, not the colony workspace. The host installs
	// the root-aware executor; the persistent overseer never takes it (it
	// stays the colony's own client-facing session, rooted at the colony).
	workerRoot := workerExecutionRoot(worker)
	executor := r.toolExec
	if workerRoot != "" {
		if factory := r.workerExecutorFactory(); factory != nil {
			if rooted := factory(workerRoot, executor); rooted != nil {
				executor = rooted
			}
		}
	}
	// Skill reads are scoped to the same session root the worker's own tools
	// use: a worker's transient state (global skill requests) belongs in its
	// storage directory, while the overseer keeps the colony root.
	sessionDir := r.storagePath
	if workerRoot != "" {
		sessionDir = workerRoot
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
		// folder and the owning queen's skills/ folder: upstream's queen
		// scope is visible to every colony she owns, and the colony's copy
		// wins on a name collision (scopedSkillRoots searches colony first).
		// Any other queen's tree stays unreachable.
		if content, isError, handled := skills.ExecuteAgentTool(req.Name, req.Input, skills.ToolDirs{
			ColonyDir:  r.storagePath,
			QueenDir:   r.queenDir,
			SessionDir: sessionDir,
		}); handled {
			return agentloop.ToolResult{Content: content, IsError: isError}, nil
		}
		if executor != nil {
			// Worker-profile credential aliases apply to every provider-mapped
			// tool call (upstream account_overrides). An explicit account on
			// the call wins.
			return executor(r.applyProfileAccountDefault(worker, req))
		}
		return agentloop.ToolResult{Content: "ok"}, nil
	}
}

// runGatedWorker is the admission point for spawned workers. It blocks on the
// colony-wide concurrency gate, then hands off to the agent loop. A worker
// stopped while queued never starts but still reaches a terminal state and
// publishes a report, so batch counters and SSE consumers never wait forever.
func (r *ColonyRuntime) runGatedWorker(ctx context.Context, worker *Worker) {
	// The dispatch gate is the first admission check (upstream
	// _enqueue_or_admit_worker checks _dispatch_blocked before the cap): a
	// stopped colony refuses the worker immediately instead of parking it in
	// the slot queue. The summary is upstream's stopped-report text.
	if r.DispatchBlocked() {
		r.finishWorkerStopped(worker, "Colony was stopped — this task was never started.")
		return
	}
	if !r.acquireWorkerSlot(ctx) {
		r.finishWorkerStopped(worker, "Worker was stopped before it started — the colony stopped while it was queued.")
		return
	}
	defer r.releaseWorkerSlot()
	if r.DispatchBlocked() {
		r.finishWorkerStopped(worker, "Colony was stopped — this task was never started.")
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
	// included); upstream builds a full AgentLoop for it too, with the
	// colony-queen profile rather than the generic defaults.
	loop := agentloop.NewAgentLoop(overseerLoopConfig(r.storagePath), r.executorFor(worker))
	// The overseer restores its transcript across a restart; its loop-owned
	// cursor state (cumulative tool calls, doom fingerprint window) must
	// resume with it, exactly like a worker's.
	//
	// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
	// Parity: framework/agent_loop/agent_loop.py::execute (restored counters)
	if worker.conversation != nil {
		_ = loop.RestoreCursorState(worker.conversation)
	}
	// P0-14: an overseer's cross-turn replay publishes
	// tool_call_replay_detected on the colony bus like a worker's.
	//
	// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected
	if r.bus != nil {
		loop.ReplayEvents = r.bus
	}
	r.attachToolLiveness(loop)
	// The overseer is the colony's lead: give it the queen-role reminder
	// sources its own loop was missing (fleet, tracker, parallel nudge,
	// stream-stall). No ticker is started (see overseer_reminders.go).
	stopReminders := r.startOverseerReminders(loop, worker, loop.Config, r.workerTools(worker))
	defer stopReminders()
	// Hive 1.0.15 constructs the overseer loop with judge=None; the queen spec
	// it receives sets skip_judge=True. The port consequently does not arm a
	// loop judge here, so a natural stop costs no extra model call and injects
	// no [Judge feedback].
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

package host

// Host composes a colony runtime with its event bus and run registry. It is
// the package-level integration point the server/SSE lane can wire without
// reaching into colony internals: construct a Host from session config, Start
// it, then Spawn a run (non-blocking) or Run it (blocking) and follow progress
// through Bus() or Stats().

import (
	"context"
	"errors"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

const DefaultColonyID = "colony"

// Config describes one colony host.
type Config struct {
	ColonyID  string
	AgentSpec agentloop.AgentSpec
	Goal      *schemas.Goal

	StoragePath  string
	LLM          llm.Completer
	Tools        []llm.ToolDef
	ToolExecutor func(agentloop.ToolCallRequest) (agentloop.ToolResult, error)
	// DeclaredTools is the optional tool pool colony workers select from via
	// their worker definition's "tools" list (integration/MCP tools). Nil
	// leaves workers on the base tool surface.
	DeclaredTools []llm.ToolDef

	// EventBus is optional; when nil the host creates one it owns.
	EventBus *eventbus.EventBus

	// Optional runtime knobs forwarded to the colony runtime.
	MCPToolNames       map[string]struct{}
	WorkerKeepSet      func() map[string]struct{}
	AdaptiveToolBudget *bool

	// MaxConcurrentWorkers caps parallel worker execution for every run.
	// <=0 uses the colony default.
	MaxConcurrentWorkers int

	// WorkerLLM, when set, is the completer workers use; nil shares LLM.
	WorkerLLM llm.Completer
	// SkillsProvider supplies the colony skill catalog for the read routes.
	SkillsProvider func() ([]*skills.ParsedSkill, bool)
	// QueenDir is the owning queen's home (…/queens/<id>); when set, the
	// colony's workers see her skill scope (queen_ui) alongside the colony's
	// own (colony_ui), with the colony overriding the queen on collisions.
	// Empty keeps the worker surface colony-only.
	QueenDir string
	// ToolProviderMap attributes tool names to providers for the tools route.
	ToolProviderMap map[string]string
}

// Host owns a single colony runtime and the runs spawned through it.
type Host struct {
	cfg     Config
	runtime *colony.ColonyRuntime
	bus     *eventbus.EventBus

	mu       sync.Mutex
	runs     map[string]*colony.ColonyRun
	runOrder []string
	started  bool
	stopped  bool
}

func New(cfg Config) (*Host, error) {
	if cfg.LLM == nil {
		return nil, errors.New("host: an LLM completer is required")
	}
	if cfg.AgentSpec.ID == "" {
		return nil, errors.New("host: agent spec ID is required")
	}
	if cfg.ColonyID == "" {
		cfg.ColonyID = DefaultColonyID
	}
	bus := cfg.EventBus
	if bus == nil {
		bus = eventbus.NewEventBus()
	}
	// Resolve the MCP capability boundary once per host: the process loader
	// names the MCP surface, and the colony's tools.json supplies the enabled
	// subset. Without the name set the runtime cannot gate anything.
	mcpToolNames := cfg.MCPToolNames
	if mcpToolNames == nil {
		mcpToolNames = discoveredMCPToolNames()
	}
	runtime := colony.NewColonyRuntime(colony.RuntimeOptions{
		AgentSpec:            cfg.AgentSpec,
		Goal:                 cfg.Goal,
		StoragePath:          cfg.StoragePath,
		LLM:                  cfg.LLM,
		Tools:                cfg.Tools,
		DeclaredTools:        cfg.DeclaredTools,
		ToolExecutor:         cfg.ToolExecutor,
		EventBus:             bus,
		StreamID:             cfg.ColonyID,
		MCPToolNames:         mcpToolNames,
		WorkerKeepSet:        cfg.WorkerKeepSet,
		AdaptiveToolBudget:   cfg.AdaptiveToolBudget,
		MaxConcurrentWorkers: cfg.MaxConcurrentWorkers,
		WorkerLLM:            cfg.WorkerLLM,
		SkillsProvider:       cfg.SkillsProvider,
		QueenDir:             cfg.QueenDir,
		ToolProviderMap:      cfg.ToolProviderMap,
	})
	if enabled, present := readColonyToolsAllowlist(cfg.StoragePath); present {
		runtime.SetToolAllowlist(enabled, mcpToolNames)
	}
	return &Host{
		cfg:     cfg,
		runtime: runtime,
		bus:     bus,
		runs:    map[string]*colony.ColonyRun{},
	}, nil
}

func (h *Host) Runtime() *colony.ColonyRuntime { return h.runtime }

func (h *Host) Bus() *eventbus.EventBus { return h.bus }

func (h *Host) ColonyID() string { return h.cfg.ColonyID }

func (h *Host) Started() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started && !h.stopped
}

// Start arms the underlying runtime. It is idempotent.
func (h *Host) Start(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return errors.New("host: cannot start a stopped host")
	}
	if err := h.runtime.Start(ctx); err != nil {
		return err
	}
	h.started = true
	return nil
}

// StartOverseer starts the colony's client-facing overseer worker. The host
// starts itself first so a caller never has to know the two-step order; this
// is the production entry the serve assembly uses (upstream's
// start_overseer requires an already-running ColonyRuntime).
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start_overseer
func (h *Host) StartOverseer(ctx context.Context, queenSpec agentloop.AgentSpec, seed []colony.SeedMessage) (*colony.Worker, error) {
	if err := h.ensureStarted(ctx); err != nil {
		return nil, err
	}
	return h.runtime.StartOverseer(ctx, queenSpec, seed)
}

// OverseerRunning reports whether the colony has a started overseer.
func (h *Host) OverseerRunning() bool {
	return h.runtime.Overseer() != nil
}

// Stop cancels every run and worker owned by this host.
func (h *Host) Stop() {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.stopped = true
	h.mu.Unlock()
	h.runtime.Stop()
}

// Spawn starts a colony run without blocking. The run is tracked by ID so the
// caller can expose it through status endpoints.
func (h *Host) Spawn(ctx context.Context, tasks []colony.BatchTask, opts colony.RunSpawnOptions) (*colony.ColonyRun, error) {
	if err := h.ensureStarted(ctx); err != nil {
		return nil, err
	}
	run, err := h.runtime.SpawnRun(ctx, tasks, opts)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.runs[run.ID()] = run
	h.runOrder = append(h.runOrder, run.ID())
	h.mu.Unlock()
	return run, nil
}

// Run is the blocking closed loop: spawn, execute every worker, aggregate.
func (h *Host) Run(ctx context.Context, tasks []colony.BatchTask, opts colony.RunSpawnOptions) (*colony.ColonyResult, error) {
	run, err := h.Spawn(ctx, tasks, opts)
	if err != nil {
		return nil, err
	}
	return run.Wait(ctx)
}

func (h *Host) ensureStarted(ctx context.Context) error {
	h.mu.Lock()
	stopped := h.stopped
	started := h.started
	h.mu.Unlock()
	if stopped {
		return errors.New("host: cannot spawn on a stopped host")
	}
	if started {
		return nil
	}
	return h.Start(ctx)
}

// RunStatus returns a run's externally visible snapshot.
func (h *Host) RunStatus(runID string) (map[string]any, bool) {
	h.mu.Lock()
	run := h.runs[runID]
	h.mu.Unlock()
	if run == nil {
		return nil, false
	}
	return run.Stats(), true
}

// Runs returns snapshots of every run in spawn order.
func (h *Host) Runs() []map[string]any {
	h.mu.Lock()
	order := append([]string(nil), h.runOrder...)
	runs := make([]*colony.ColonyRun, 0, len(order))
	for _, id := range order {
		if run := h.runs[id]; run != nil {
			runs = append(runs, run)
		}
	}
	h.mu.Unlock()

	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		out = append(out, run.Stats())
	}
	return out
}

// Stats merges runtime gauges with host-level run counts.
func (h *Host) Stats() map[string]any {
	stats := h.runtime.GetStats()
	h.mu.Lock()
	total := len(h.runOrder)
	active := 0
	for _, run := range h.runs {
		select {
		case <-run.Done():
		default:
			active++
		}
	}
	started, stopped := h.started, h.stopped
	h.mu.Unlock()

	stats["host"] = map[string]any{
		"colony_id":   h.cfg.ColonyID,
		"started":     started && !stopped,
		"stopped":     stopped,
		"runs_total":  total,
		"runs_active": active,
	}
	return stats
}

// Subscribe is a passthrough so integrators can wire SSE without touching the
// underlying bus. Filters (by stream/node/execution/colony) use the bus's own
// SubscribeOption values.
func (h *Host) Subscribe(eventTypes []eventbus.EventType, handler eventbus.EventHandler, opts ...eventbus.SubscribeOption) string {
	return h.bus.Subscribe(eventTypes, handler, opts...)
}

func (h *Host) Unsubscribe(subscriptionID string) bool {
	return h.bus.Unsubscribe(subscriptionID)
}

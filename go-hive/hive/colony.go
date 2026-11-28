package hive

import (
	"context"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/host"
	"github.com/shark2202/go_hive/go-hive/internal/server"
)

// Task is one unit of work a colony dispatches to a worker.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
type Task = colony.BatchTask

// RunOptions controls one colony run.
//
// Parity-exempt: Go-only run options.
type RunOptions = colony.RunSpawnOptions

// RunPhase is a run's lifecycle state.
//
// Parity-exempt: Go-only run state enum.
type RunPhase = colony.RunPhase

// Run states.
const (
	RunQueued    = colony.RunQueued
	RunRunning   = colony.RunRunning
	RunCompleted = colony.RunCompleted
	RunPartial   = colony.RunPartial
	RunFailed    = colony.RunFailed
	RunStopped   = colony.RunStopped
)

// RunResult is the aggregated outcome of one colony run.
//
// Parity-exempt: Go-only aggregate result; Python returns one WorkerResult per
// worker.
type RunResult struct {
	RunID    string
	BatchID  string
	ColonyID string
	Status   RunPhase

	WorkerResults []WorkerOutcome
	Summaries     []string

	Succeeded int
	Partial   int
	Failed    int
	Stopped   int

	// Data merges every worker's report data under its worker id.
	Data map[string]any

	Error *string

	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

// WorkerOutcome is one worker's terminal contribution to a colony run.
//
// Parity: framework/host/worker.py::WorkerResult
type WorkerOutcome struct {
	WorkerID     string
	Task         string
	StreamID     string
	Status       WorkerStatus
	Outcome      string
	ReportStatus string

	Summary       string
	Data          map[string]any
	Error         *string
	ToolCallsUsed int
	BudgetLimited bool
	Duration      time.Duration
}

func runResultFromInternal(in *colony.ColonyResult) *RunResult {
	if in == nil {
		return nil
	}
	out := &RunResult{
		RunID:      in.RunID,
		BatchID:    in.BatchID,
		ColonyID:   in.ColonyID,
		Status:     in.Status,
		Summaries:  in.Summaries,
		Succeeded:  in.Succeeded,
		Partial:    in.Partial,
		Failed:     in.Failed,
		Stopped:    in.Stopped,
		Data:       in.Data,
		Error:      in.Error,
		StartedAt:  in.StartedAt,
		FinishedAt: in.FinishedAt,
		Duration:   in.Duration,
	}
	out.WorkerResults = make([]WorkerOutcome, 0, len(in.WorkerResults))
	for _, entry := range in.WorkerResults {
		out.WorkerResults = append(out.WorkerResults, WorkerOutcome{
			WorkerID:      entry.WorkerID,
			Task:          entry.Task,
			StreamID:      entry.StreamID,
			Status:        entry.Status,
			Outcome:       entry.Outcome,
			ReportStatus:  entry.ReportStatus,
			Summary:       entry.Summary,
			Data:          entry.Data,
			Error:         entry.Error,
			ToolCallsUsed: entry.ToolCallsUsed,
			BudgetLimited: entry.BudgetLimited,
			Duration:      entry.Duration,
		})
	}
	return out
}

// WorkerStatus is a worker's runtime state.
//
// Parity: framework/host/worker.py::WorkerStatus
type WorkerStatus = colony.WorkerStatus

// Worker states.
const (
	WorkerQueued    = colony.WorkerQueued
	WorkerRunning   = colony.WorkerRunning
	WorkerCompleted = colony.WorkerCompleted
	WorkerFailed    = colony.WorkerFailed
	WorkerStopped   = colony.WorkerStopped
)

// WorkerResult is one worker's terminal report.
//
// Parity: framework/host/worker.py::WorkerResult
type WorkerResult struct {
	WorkerID        string
	Status          WorkerStatus
	Output          map[string]any
	Error           *string
	ToolCallsUsed   int
	BudgetLimited   bool
	TokensUsed      int
	Summary         string
	Data            map[string]any
	ReportStatus    string
	DurationSeconds float64
}

func workerResultFromInternal(in *colony.WorkerResult) *WorkerResult {
	if in == nil {
		return nil
	}
	return &WorkerResult{
		WorkerID:        in.WorkerID,
		Status:          in.Status,
		Output:          in.Output,
		Error:           in.Error,
		ToolCallsUsed:   in.ToolCallsUsed,
		BudgetLimited:   in.BudgetLimited,
		TokensUsed:      in.TokensUsed,
		Summary:         in.Summary,
		Data:            in.Data,
		ReportStatus:    in.ReportStatus,
		DurationSeconds: in.DurationSeconds,
	}
}

// WorkerInfo is the read view of one worker.
//
// Parity: framework/host/worker.py::WorkerInfo
type WorkerInfo struct {
	ID          string
	Task        string
	Goal        string
	Status      WorkerStatus
	StreamID    string
	BatchID     string
	Index       int
	Size        int
	StartedAt   float64
	ProfileName string
	WorkerSeq   int
	Result      *WorkerResult
}

func workerInfoFromInternal(in colony.WorkerInfo) WorkerInfo {
	return WorkerInfo{
		ID:          in.WorkerID,
		Task:        in.Task,
		Goal:        in.Goal,
		Status:      in.Status,
		StreamID:    in.StreamID,
		BatchID:     in.BatchID,
		Index:       in.Index,
		Size:        in.Size,
		StartedAt:   in.StartedAt,
		ProfileName: in.ProfileName,
		WorkerSeq:   in.WorkerSeq,
		Result:      workerResultFromInternal(in.Result),
	}
}

// ColonyOptions configures one colony host.
//
// Home plus ID materialise the conventional colony directory
// (<HIVE_HOME>/colonies/<ID>) when StoragePath is empty; an explicit
// StoragePath wins.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime
type ColonyOptions struct {
	Home Home
	ID   string
	// QueenID, when set, is recorded on the colony's metadata and used for
	// the queen skill scope.
	QueenID string
	// StoragePath is the colony directory. Empty derives it from Home+ID.
	StoragePath string

	AgentSpec AgentSpec
	Goal      *Goal

	// LLM is the queen/colony completer. WorkerLLM, when set, is the
	// completer workers use instead.
	LLM       Model
	WorkerLLM Model

	Tools        []Tool
	ToolExecutor ToolExecutor

	// EventBus, when set, is the bus the colony publishes on. Nil creates
	// one owned by the colony.
	EventBus *EventBus

	// MaxConcurrentWorkers caps parallel worker execution. <=0 uses the
	// colony default (HIVE_MAX_CONCURRENT_WORKERS, 4).
	MaxConcurrentWorkers int

	// SkillsProvider supplies the colony skill catalog. Nil auto-discovers
	// the standard scopes (framework, preset, user, queen, colony).
	SkillsProvider func() ([]*Skill, bool)
}

// Colony is the SDK's serve facade: it owns one colony runtime, starts it,
// spawns and runs workers, and exposes its event bus.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime
type Colony struct {
	inner *host.Host
	bus   *EventBus
}

// NewColony builds a colony host. The runtime is constructed but not started;
// call Start (or Serve/Run, which start it lazily through the host).
//
// A nil LLM or an empty AgentSpec.ID is rejected before any directory is
// created.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime
func NewColony(opts ColonyOptions) (*Colony, error) {
	if opts.LLM == nil {
		return nil, errMissingModel
	}
	if opts.AgentSpec.ID == "" {
		return nil, errMissingAgentSpec
	}
	if opts.ID == "" {
		opts.ID = "colony"
	}
	if opts.StoragePath == "" {
		if opts.Home.Root == "" {
			return nil, errMissingHome
		}
		if err := server.EnsureMinimalColony(opts.Home, opts.ID, opts.QueenID); err != nil {
			return nil, err
		}
		opts.StoragePath = opts.Home.ColonyDir(opts.ID)
	}
	queenDir := ""
	if opts.QueenID != "" && opts.Home.Root != "" {
		queenDir = opts.Home.QueenDir(opts.QueenID)
	}

	var bus *eventbus.EventBus
	if opts.EventBus != nil {
		bus = opts.EventBus.inner
	}
	instance, err := host.New(host.Config{
		ColonyID:             opts.ID,
		AgentSpec:            opts.AgentSpec,
		Goal:                 opts.Goal,
		StoragePath:          opts.StoragePath,
		LLM:                  opts.LLM,
		Tools:                toolDefs(opts.Tools),
		ToolExecutor:         loopExecutor(opts.ToolExecutor),
		EventBus:             bus,
		MaxConcurrentWorkers: opts.MaxConcurrentWorkers,
		WorkerLLM:            opts.WorkerLLM,
		QueenDir:             queenDir,
		SkillsProvider:       opts.SkillsProvider,
	})
	if err != nil {
		return nil, err
	}
	colony := &Colony{inner: instance}
	if opts.EventBus != nil {
		colony.bus = opts.EventBus
	} else {
		colony.bus = &EventBus{inner: instance.Bus()}
	}
	return colony, nil
}

func loopExecutor(executor ToolExecutor) func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	if executor == nil {
		return nil
	}
	return func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		result, err := executor(request)
		if err != nil {
			return agentloop.ToolResult{}, err
		}
		return result.toInternal(), nil
	}
}

// Start arms the colony runtime. It is idempotent.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.start
func (c *Colony) Start(ctx context.Context) error {
	if c == nil || c.inner == nil {
		return errMissingColonyID
	}
	return c.inner.Start(ctx)
}

// Serve starts the colony and blocks until ctx is cancelled, then stops it.
// This is the "serve work" facade: a derivative project owns the process
// lifecycle and the colony owns the workers.
//
// Parity-exempt: Go-only lifecycle helper over ColonyRuntime.start/stop.
func (c *Colony) Serve(ctx context.Context) error {
	if ctx == nil {
		return errNilContext
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	c.Stop()
	return nil
}

// Stop cancels every run and worker owned by the colony.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.stop
func (c *Colony) Stop() {
	if c == nil || c.inner == nil {
		return
	}
	c.inner.Stop()
}

// ID is the colony id.
//
// Parity-exempt: Go-only accessor.
func (c *Colony) ID() string {
	if c == nil || c.inner == nil {
		return ""
	}
	return c.inner.ColonyID()
}

// Events is the colony's event bus.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.event_bus
func (c *Colony) Events() *EventBus {
	if c == nil {
		return nil
	}
	return c.bus
}

// Spawn starts a run without blocking.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
func (c *Colony) Spawn(ctx context.Context, tasks []Task) (*Run, error) {
	return c.SpawnWithOptions(ctx, tasks, RunOptions{})
}

// SpawnWithOptions starts a run without blocking, with explicit run options.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
func (c *Colony) SpawnWithOptions(ctx context.Context, tasks []Task, opts RunOptions) (*Run, error) {
	if c == nil || c.inner == nil {
		return nil, errMissingColonyID
	}
	run, err := c.inner.Spawn(ctx, tasks, opts)
	if err != nil {
		return nil, err
	}
	return &Run{inner: run}, nil
}

// Run executes a batch of tasks and blocks until every worker is terminal.
//
// Parity-exempt: Go-only blocking helper over spawn_batch plus
// wait_for_worker_reports.
func (c *Colony) Run(ctx context.Context, tasks []Task) (*RunResult, error) {
	return c.RunWithOptions(ctx, tasks, RunOptions{})
}

// RunWithOptions is Run with explicit run options.
//
// Parity-exempt: Go-only blocking helper over spawn_batch plus
// wait_for_worker_reports.
func (c *Colony) RunWithOptions(ctx context.Context, tasks []Task, opts RunOptions) (*RunResult, error) {
	if c == nil || c.inner == nil {
		return nil, errMissingColonyID
	}
	result, err := c.inner.Run(ctx, tasks, opts)
	if err != nil {
		return nil, err
	}
	return runResultFromInternal(result), nil
}

// Workers lists every worker in the colony.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.list_workers
func (c *Colony) Workers() []WorkerInfo {
	if c == nil || c.inner == nil {
		return nil
	}
	entries := c.inner.ListWorkers()
	out := make([]WorkerInfo, 0, len(entries))
	for _, entry := range entries {
		out = append(out, workerInfoFromInternal(entry))
	}
	return out
}

// Worker returns one worker handle by id.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_worker
func (c *Colony) Worker(id string) (*Worker, bool) {
	if c == nil || c.inner == nil {
		return nil, false
	}
	entry := c.inner.GetWorker(id)
	if entry == nil {
		return nil, false
	}
	return &Worker{inner: entry}, true
}

// Stats is the colony runtime's counter snapshot.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.get_stats
func (c *Colony) Stats() map[string]any {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.GetStats()
}

// Subscribe registers a handler on the colony's event bus.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.subscribe_to_events
func (c *Colony) Subscribe(types []EventType, handler EventHandler, filter EventFilter) (string, error) {
	if c == nil || c.inner == nil {
		return "", errMissingColonyID
	}
	return c.inner.SubscribeToEvents(types, handler, filter.options()...)
}

// Unsubscribe removes a colony subscription by id.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.unsubscribe
func (c *Colony) Unsubscribe(id string) bool {
	if c == nil || c.inner == nil {
		return false
	}
	return c.inner.Unsubscribe(id)
}

// Run is a live handle on one colony run.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn_batch
type Run struct {
	inner *colony.ColonyRun
}

// ID is the run id.
//
// Parity-exempt: Go-only accessor.
func (r *Run) ID() string {
	if r == nil || r.inner == nil {
		return ""
	}
	return r.inner.ID()
}

// Wait blocks until the run is terminal and returns its aggregate result.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.wait_for_worker_reports
func (r *Run) Wait(ctx context.Context) (*RunResult, error) {
	if r == nil || r.inner == nil {
		return nil, errMissingColonyID
	}
	result, err := r.inner.Wait(ctx)
	if err != nil {
		return nil, err
	}
	return runResultFromInternal(result), nil
}

// Stop cancels the run and its workers.
//
// Parity-exempt: Go-only accessor.
func (r *Run) Stop() {
	if r == nil || r.inner == nil {
		return
	}
	r.inner.Stop()
}

// Done is closed when the run becomes terminal.
//
// Parity-exempt: Go-only accessor.
func (r *Run) Done() <-chan struct{} {
	if r == nil || r.inner == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return r.inner.Done()
}

// Worker is one live worker handle.
//
// Parity: framework/host/worker.py::Worker
type Worker struct {
	inner *colony.Worker
}

// ID is the worker id.
//
// Parity-exempt: Go-only accessor.
func (w *Worker) ID() string {
	if w == nil || w.inner == nil {
		return ""
	}
	return w.inner.ID
}

// Status is the worker's runtime state.
//
// Parity: framework/host/worker.py::Worker.status
func (w *Worker) Status() WorkerStatus {
	if w == nil || w.inner == nil {
		return ""
	}
	return w.inner.Status()
}

// Info is the worker's read view.
//
// Parity: framework/host/worker.py::Worker.info
func (w *Worker) Info() WorkerInfo {
	if w == nil || w.inner == nil {
		return WorkerInfo{}
	}
	return workerInfoFromInternal(w.inner.Info())
}

// Result is the worker's terminal report, if it has one.
//
// Parity: framework/host/worker.py::WorkerInfo
func (w *Worker) Result() *WorkerResult {
	if w == nil || w.inner == nil {
		return nil
	}
	return workerResultFromInternal(ptr(w.inner.Result()))
}

// Inject queues a message for the running worker.
//
// Parity: framework/host/worker.py::Worker.inject
func (w *Worker) Inject(content string) bool {
	if w == nil || w.inner == nil {
		return false
	}
	return w.inner.Inject(content)
}

// Wait blocks until the worker is terminal.
//
// Parity-exempt: Go-only blocking wait; the Python worker signals through
// asyncio tasks.
func (w *Worker) Wait() {
	if w == nil || w.inner == nil {
		return
	}
	w.inner.Wait()
}

// Active reports whether the worker is currently running.
//
// Parity: framework/host/worker.py::Worker.is_active
func (w *Worker) Active() bool {
	if w == nil || w.inner == nil {
		return false
	}
	return w.inner.IsActive()
}

// BudgetLimited reports whether the worker hit its loop budget.
//
// Parity: framework/host/worker.py::Worker.budget_limited
func (w *Worker) BudgetLimited() bool {
	if w == nil || w.inner == nil {
		return false
	}
	return w.inner.LoopBudgetLimited()
}

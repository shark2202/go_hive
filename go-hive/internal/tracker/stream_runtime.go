package tracker

// StreamDecisionTracker — the thread-safe runtime for concurrent executions.
//
// Unlike DecisionTracker's single current run, a stream keys runs by
// execution_id so concurrent executions in the same stream cannot collide.
// Each execution is backed by its own DecisionTracker over the stream's shared
// ConcurrentStorage: the stream owns the execution registry, node context and
// terminal persistence, while decision/outcome shaping stays in one place.
//
// Parity: framework/host/stream_runtime.py::StreamDecisionTracker

import (
	"fmt"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/storage"
)

// StreamLedger is the shared storage behind a family of decision streams: one
// ConcurrentStorage root, one StreamDecisionTracker per stream. It mirrors the
// upstream colony runtime, which owns one ConcurrentStorage and hands it to
// every per-worker stream runtime.
type StreamLedger struct {
	storage *storage.ConcurrentStorage
}

// NewStreamLedger opens (creating when missing) the shared ledger root.
func NewStreamLedger(storagePath string) (*StreamLedger, error) {
	store, err := ensureDecisionStorage(storagePath)
	if err != nil {
		return nil, err
	}
	return &StreamLedger{storage: store}, nil
}

// Storage exposes the shared backends.
func (l *StreamLedger) Storage() *storage.ConcurrentStorage {
	if l == nil {
		return nil
	}
	return l.storage
}

// NewStream opens a decision stream over the shared ledger.
func (l *StreamLedger) NewStream(streamID string) (*StreamDecisionTracker, error) {
	if l == nil || l.storage == nil {
		return nil, ErrStreamStorageRequired
	}
	return NewStreamDecisionTracker(streamID, l.storage)
}

// streamExecution is one in-flight execution's current-run runtime.
type streamExecution struct {
	tracker *DecisionTracker
}

// StreamDecisionTracker tracks runs by execution id and persists each ended run
// as runs/<run_id>.json through the shared storage.
type StreamDecisionTracker struct {
	streamID string
	storage  *storage.ConcurrentStorage

	mu         sync.Mutex
	executions map[string]*streamExecution
	nodes      map[string]string
	order      []string
}

// NewStreamDecisionTracker binds a stream to an existing ledger backend. A
// nil backend is rejected: without storage a stream could accept decisions it
// can never persist.
func NewStreamDecisionTracker(streamID string, store *storage.ConcurrentStorage) (*StreamDecisionTracker, error) {
	if store == nil {
		return nil, ErrStreamStorageRequired
	}
	return &StreamDecisionTracker{
		streamID:   streamID,
		storage:    store,
		executions: map[string]*streamExecution{},
		nodes:      map[string]string{},
	}, nil
}

// StreamID reports the stream this runtime belongs to.
func (s *StreamDecisionTracker) StreamID() string { return s.streamID }

// Storage exposes the shared backend.
func (s *StreamDecisionTracker) Storage() *storage.ConcurrentStorage { return s.storage }

// StartRun opens a run for an execution and returns its id. A second start on
// the same execution replaces the previous run, matching upstream's dict-slot
// overwrite semantics.
func (s *StreamDecisionTracker) StartRun(executionID, goalID, goalDescription string, inputData map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.executions[executionID]; !exists {
		s.order = append(s.order, executionID)
	}
	entry := &streamExecution{tracker: newDecisionTracker(s.storage)}
	s.executions[executionID] = entry
	s.nodes[executionID] = "unknown"
	return entry.tracker.startRun(newDecisionRunForStream(s.streamID, goalID, goalDescription, inputData))
}

// EndRun completes, persists and stops tracking an execution. The execution is
// detached before the write, so a decide racing the end either lands in the
// saved run or returns "" — never in a run nobody will persist. An unknown or
// already-ended execution is a no-op, as upstream's warning path is.
func (s *StreamDecisionTracker) EndRun(executionID string, success bool, narrative string, outputData map[string]any) error {
	s.mu.Lock()
	entry, ok := s.executions[executionID]
	if ok {
		delete(s.executions, executionID)
		delete(s.nodes, executionID)
		s.removeOrderLocked(executionID)
	}
	s.mu.Unlock()
	if !ok || entry == nil {
		return nil
	}
	if err := entry.tracker.EndRun(success, narrative, outputData); err != nil {
		return fmt.Errorf("tracker: save stream run for execution %s: %w", executionID, err)
	}
	return nil
}

func (s *StreamDecisionTracker) removeOrderLocked(executionID string) {
	for i, id := range s.order {
		if id != executionID {
			continue
		}
		s.order = append(s.order[:i], s.order[i+1:]...)
		return
	}
}

// SetNode sets the current node for an execution's decisions.
func (s *StreamDecisionTracker) SetNode(executionID, nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[executionID] = nodeID
}

// GetRun returns the execution's in-flight run, or nil when it has none.
func (s *StreamDecisionTracker) GetRun(executionID string) *schemas.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.executions[executionID]
	if entry == nil {
		return nil
	}
	return entry.tracker.CurrentRun()
}

// Decide appends a decision for an execution and returns its id. Decisions for
// an execution that is not running (or just ended) are dropped with "".
func (s *StreamDecisionTracker) Decide(executionID, intent string, options []DecisionOption, chosen, reasoning string, params DecisionParams) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.executions[executionID]
	if entry == nil {
		return ""
	}
	if params.NodeID == "" {
		params.NodeID = s.nodes[executionID]
	}
	if params.NodeID == "" {
		params.NodeID = "unknown"
	}
	return entry.tracker.Decide(intent, options, chosen, reasoning, params)
}

// RecordOutcome records the outcome of an execution's decision. Unknown
// executions are a graceful no-op (upstream's warning path).
func (s *StreamDecisionTracker) RecordOutcome(executionID string, record OutcomeRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.executions[executionID]; entry != nil {
		entry.tracker.RecordOutcome(record)
	}
}

// ReportProblem appends a problem for an execution and returns its id.
func (s *StreamDecisionTracker) ReportProblem(executionID string, report ProblemReport) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.executions[executionID]
	if entry == nil {
		return ""
	}
	return entry.tracker.ReportProblem(report)
}

// QuickDecision records a single-action decision for an execution.
func (s *StreamDecisionTracker) QuickDecision(executionID, intent, action, reasoning, nodeID string) string {
	return s.Decide(executionID, intent, quickDecisionOptions(action), "action", reasoning, DecisionParams{NodeID: nodeID})
}

// GetActiveExecutions returns the live execution ids in start order.
func (s *StreamDecisionTracker) GetActiveExecutions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.order...)
}

// GetStats renders the stream gauge dictionary (upstream get_stats()).
func (s *StreamDecisionTracker) GetStats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := append([]string{}, s.order...)
	return map[string]any{
		"stream_id":         s.streamID,
		"active_executions": len(ids),
		"execution_ids":     ids,
	}
}

// StreamRuntimeAdapter binds one execution id to a stream, giving the
// single-run DecisionTracker surface graph-era node code expects.
//
// Parity: framework/host/stream_runtime.py::StreamRuntimeAdapter
type StreamRuntimeAdapter struct {
	mu          sync.Mutex
	runtime     *StreamDecisionTracker
	executionID string
	currentNode string
}

// NewStreamRuntimeAdapter adapts a stream for one execution.
func NewStreamRuntimeAdapter(runtime *StreamDecisionTracker, executionID string) *StreamRuntimeAdapter {
	return &StreamRuntimeAdapter{runtime: runtime, executionID: executionID, currentNode: "unknown"}
}

// ExecutionID is the execution this adapter routes to.
func (a *StreamRuntimeAdapter) ExecutionID() string { return a.executionID }

// Storage exposes the stream's shared backend.
func (a *StreamRuntimeAdapter) Storage() *storage.ConcurrentStorage {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.Storage()
}

// CurrentRun returns this execution's in-flight run.
func (a *StreamRuntimeAdapter) CurrentRun() *schemas.Run {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.GetRun(a.executionID)
}

// StartRun opens a run for this execution.
func (a *StreamRuntimeAdapter) StartRun(goalID, goalDescription string, inputData map[string]any) string {
	if a.runtime == nil {
		return ""
	}
	return a.runtime.StartRun(a.executionID, goalID, goalDescription, inputData)
}

// EndRun ends this execution's run.
func (a *StreamRuntimeAdapter) EndRun(success bool, narrative string, outputData map[string]any) error {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.EndRun(a.executionID, success, narrative, outputData)
}

// SetNode sets the adapter's node and the stream's node context.
func (a *StreamRuntimeAdapter) SetNode(nodeID string) {
	a.mu.Lock()
	a.currentNode = nodeID
	a.mu.Unlock()
	if a.runtime != nil {
		a.runtime.SetNode(a.executionID, nodeID)
	}
}

// Decide records a decision for this execution, defaulting to the adapter's
// current node (which starts at "unknown", as upstream does).
func (a *StreamRuntimeAdapter) Decide(intent string, options []DecisionOption, chosen, reasoning string, params DecisionParams) string {
	if a.runtime == nil {
		return ""
	}
	if params.NodeID == "" {
		a.mu.Lock()
		params.NodeID = a.currentNode
		a.mu.Unlock()
	}
	return a.runtime.Decide(a.executionID, intent, options, chosen, reasoning, params)
}

// RecordOutcome records an outcome for this execution's decision.
func (a *StreamRuntimeAdapter) RecordOutcome(record OutcomeRecord) {
	if a.runtime == nil {
		return
	}
	a.runtime.RecordOutcome(a.executionID, record)
}

// ReportProblem reports a problem for this execution.
func (a *StreamRuntimeAdapter) ReportProblem(report ProblemReport) string {
	if a.runtime == nil {
		return ""
	}
	return a.runtime.ReportProblem(a.executionID, report)
}

// QuickDecision records a single-action decision for this execution.
func (a *StreamRuntimeAdapter) QuickDecision(intent, action, reasoning, nodeID string) string {
	if a.runtime == nil {
		return ""
	}
	if nodeID == "" {
		a.mu.Lock()
		nodeID = a.currentNode
		a.mu.Unlock()
	}
	return a.runtime.QuickDecision(a.executionID, intent, action, reasoning, nodeID)
}

package tracker

// DecisionTracker — the single-run decision ledger an agent records through.
//
// A run accumulates decisions (what was considered and chosen), outcomes (what
// actually happened) and problems (what went wrong). EndRun persists the run
// through the shared ConcurrentStorage and clears the current-run slot, so a
// late decide after the run ended is a graceful no-op returning "", exactly as
// upstream's exception-cascade handling does.
//
// Parity: framework/tracker/decision_tracker.py::DecisionTracker

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/storage"
)

var (
	// ErrDecisionStorageRequired rejects a tracker with no storage root:
	// persisting runs is the tracker's whole job, so an unwired tracker must
	// fail at construction instead of silently dropping completed runs.
	ErrDecisionStorageRequired = errors.New("tracker: decision storage path is required")
	// ErrStreamStorageRequired is the stream runtime's version of the same
	// guard: a stream cannot record decisions without a shared ledger.
	ErrStreamStorageRequired = errors.New("tracker: stream decision runtime requires a storage backend")
)

// DecisionOption is one option a decision considered.
//
// The zero value renders Python's schema defaults: description "",
// action_type "unknown", empty params/pros/cons, confidence 0.5.
type DecisionOption struct {
	ID           string
	Description  string
	ActionType   string
	ActionParams map[string]any
	Pros         []string
	Cons         []string
	// Confidence overrides the 0.5 default when non-nil. A pointer to 0
	// records an explicit 0 (Python's dict access keeps an explicit zero).
	Confidence *float64
}

// DecisionParams carries decide()'s optional arguments.
type DecisionParams struct {
	// NodeID overrides the tracker's current node for this decision.
	NodeID       string
	DecisionType schemas.DecisionType
	Constraints  []string
	Context      map[string]any
}

// OutcomeRecord carries record_outcome()'s arguments.
type OutcomeRecord struct {
	DecisionID   string
	Success      bool
	Result       any
	Error        *string
	Summary      string
	StateChanges map[string]any
	TokensUsed   int
	LatencyMS    int
}

// ProblemReport carries report_problem()'s arguments.
type ProblemReport struct {
	Severity     string
	Description  string
	DecisionID   *string
	RootCause    *string
	SuggestedFix *string
}

// DecisionTracker is the single-run runtime. It is safe for concurrent use;
// the run itself is only visible through CurrentRun while it is in flight.
//
// Writer status: the decision audit chain is PARTIAL, not complete. This type
// and StreamDecisionTracker faithfully port upstream's ledger API, but no
// production code calls Decide/RecordOutcome on either side. Upstream's only
// non-literal reference is the NodeProtocol docstring
// (core/framework/orchestrator/node.py:619-647; the decide/record_outcome hits
// at :633/:644 are string constants), and the graph host that owns the run
// lifecycle — core/framework/orchestrator/orchestrator.py:638/:1695 and
// core/framework/host/execution_manager.py:677/:805/:945 — is registered
// out_of_scope/partial in contracts/module-inventory.json (the Go port has no
// graph executor). The Go colony seam (internal/colony/decision_runtime.go:88)
// installs a stream on AgentContext.Runtime, which the agent loop never reads
// (internal/agentloop/types.go:100; upstream core/framework/agent_loop/
// types.py:134 documents the same). Do not present runs in this ledger as a
// complete audit chain, and never add synthetic no-op decisions to make the
// chain look alive.
type DecisionTracker struct {
	mu      sync.Mutex
	storage *storage.ConcurrentStorage
	current *schemas.Run
	node    string
}

// NewDecisionTracker opens (creating when missing) the storage directory the
// ledger persists runs under.
func NewDecisionTracker(storagePath string) (*DecisionTracker, error) {
	store, err := ensureDecisionStorage(storagePath)
	if err != nil {
		return nil, err
	}
	return newDecisionTracker(store), nil
}

// newDecisionTracker binds a tracker to an existing ledger backend. The stream
// runtime uses it to give every execution its own current-run slot over the
// stream's shared storage.
func newDecisionTracker(store *storage.ConcurrentStorage) *DecisionTracker {
	return &DecisionTracker{storage: store, node: "unknown"}
}

func ensureDecisionStorage(storagePath string) (*storage.ConcurrentStorage, error) {
	if strings.TrimSpace(storagePath) == "" {
		return nil, ErrDecisionStorageRequired
	}
	if err := os.MkdirAll(storagePath, 0o755); err != nil {
		return nil, fmt.Errorf("tracker: create decision storage: %w", err)
	}
	return storage.NewDefaultConcurrentStorage(storagePath), nil
}

// Storage exposes the ledger backend (upstream runtime.storage).
func (t *DecisionTracker) Storage() *storage.ConcurrentStorage { return t.storage }

// ExecutionID mirrors upstream's empty execution id: a single-run tracker is
// not bound to an execution stream.
func (t *DecisionTracker) ExecutionID() string { return "" }

// StartRun opens a new run and returns its id.
func (t *DecisionTracker) StartRun(goalID, goalDescription string, inputData map[string]any) string {
	return t.startRun(newDecisionRun(goalID, goalDescription, inputData))
}

func (t *DecisionTracker) startRun(run *schemas.Run) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.current = run
	return run.ID
}

// EndRun completes the current run, persists it, and clears the slot. A save
// failure keeps the run current so a caller can retry, matching upstream where
// the field is only cleared after save_run_sync returns.
func (t *DecisionTracker) EndRun(success bool, narrative string, outputData map[string]any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	run := t.current
	if run == nil {
		return nil
	}
	status := schemas.RunCompleted
	if !success {
		status = schemas.RunFailed
	}
	run.OutputData = emptyDecisionMap(outputData)
	run.Complete(status, narrative)
	if err := t.storage.SaveRunSync(run); err != nil {
		return fmt.Errorf("tracker: save decision run %s: %w", run.ID, err)
	}
	t.current = nil
	return nil
}

// SetNode sets the node context subsequent decisions inherit.
func (t *DecisionTracker) SetNode(nodeID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.node = nodeID
}

// CurrentRun returns the in-flight run, or nil when none is open.
func (t *DecisionTracker) CurrentRun() *schemas.Run {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

// Decide appends a decision and returns its id ("" when no run is open).
func (t *DecisionTracker) Decide(intent string, options []DecisionOption, chosen, reasoning string, params DecisionParams) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return ""
	}
	nodeID := params.NodeID
	if nodeID == "" {
		nodeID = t.node
	}
	if nodeID == "" {
		nodeID = "unknown"
	}
	decision := newDecision(t.current, intent, options, chosen, reasoning, nodeID, params)
	t.current.AddDecision(decision)
	return decision.ID
}

// RecordOutcome attaches the outcome of a decision. An unknown decision id or
// an ended run is a no-op, matching upstream's graceful error-cascade path.
func (t *DecisionTracker) RecordOutcome(record OutcomeRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return
	}
	t.current.RecordOutcome(record.DecisionID, record.outcome())
}

// ReportProblem appends a problem and returns its id ("" when no run is open).
func (t *DecisionTracker) ReportProblem(report ProblemReport) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return ""
	}
	return t.current.AddProblem(report.Severity, report.Description, report.DecisionID, report.RootCause, report.SuggestedFix)
}

// DecideAndExecute records a decision, runs the action, and records its
// outcome. The executor error is returned after the failure outcome is
// recorded, so the ledger never loses the attempt.
func (t *DecisionTracker) DecideAndExecute(intent string, options []DecisionOption, chosen, reasoning string, params DecisionParams, executor func() (any, error)) (string, any, error) {
	decisionID := t.Decide(intent, options, chosen, reasoning, params)
	start := time.Now()
	result, err := executor()
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		errText := err.Error()
		t.RecordOutcome(OutcomeRecord{DecisionID: decisionID, Success: false, Error: &errText, LatencyMS: latency})
		return decisionID, nil, err
	}
	t.RecordOutcome(OutcomeRecord{DecisionID: decisionID, Success: true, Result: result, LatencyMS: latency})
	return decisionID, result, nil
}

// QuickDecision records a single-action decision.
func (t *DecisionTracker) QuickDecision(intent, action, reasoning, nodeID string) string {
	return t.Decide(intent, quickDecisionOptions(action), "action", reasoning, DecisionParams{NodeID: nodeID})
}

// === LEDGER PRIMITIVES (shared with the stream runtime) ===

func quickDecisionOptions(action string) []DecisionOption {
	return []DecisionOption{{ID: "action", Description: action, ActionType: "execute"}}
}

func newDecision(run *schemas.Run, intent string, options []DecisionOption, chosen, reasoning, nodeID string, params DecisionParams) schemas.Decision {
	built := make([]schemas.Option, 0, len(options))
	for _, option := range options {
		built = append(built, option.schema())
	}
	decisionType := params.DecisionType
	if decisionType == "" {
		decisionType = schemas.DecisionCustom
	}
	return schemas.Decision{
		ID:                fmt.Sprintf("dec_%d", len(run.Decisions)),
		Timestamp:         schemas.NowTimestamp(),
		NodeID:            nodeID,
		Intent:            intent,
		DecisionType:      decisionType,
		Options:           built,
		ChosenOptionID:    chosen,
		Reasoning:         reasoning,
		ActiveConstraints: emptyDecisionStrings(params.Constraints),
		InputContext:      emptyDecisionMap(params.Context),
	}
}

func (o DecisionOption) schema() schemas.Option {
	option := schemas.Option{
		ID:           o.ID,
		Description:  o.Description,
		ActionType:   o.ActionType,
		ActionParams: emptyDecisionMap(o.ActionParams),
		Pros:         emptyDecisionStrings(o.Pros),
		Cons:         emptyDecisionStrings(o.Cons),
	}
	if option.ActionType == "" {
		option.ActionType = "unknown"
	}
	if o.Confidence != nil {
		option.SetConfidence(*o.Confidence)
	}
	return option
}

func (r OutcomeRecord) outcome() schemas.Outcome {
	return schemas.Outcome{
		Success:      r.Success,
		Result:       r.Result,
		Error:        r.Error,
		Summary:      r.Summary,
		StateChanges: emptyDecisionMap(r.StateChanges),
		TokensUsed:   r.TokensUsed,
		LatencyMS:    r.LatencyMS,
		Timestamp:    schemas.NowTimestamp(),
	}
}

func newDecisionRun(goalID, goalDescription string, inputData map[string]any) *schemas.Run {
	return newDecisionRunWithID(decisionRunID(), goalID, goalDescription, inputData)
}

func newDecisionRunForStream(streamID, goalID, goalDescription string, inputData map[string]any) *schemas.Run {
	return newDecisionRunWithID(decisionRunIDForStream(streamID), goalID, goalDescription, inputData)
}

func newDecisionRunWithID(id, goalID, goalDescription string, inputData map[string]any) *schemas.Run {
	return &schemas.Run{
		ID:              id,
		GoalID:          goalID,
		StartedAt:       schemas.NowTimestamp(),
		Status:          schemas.RunRunning,
		Decisions:       []schemas.Decision{},
		Problems:        []schemas.Problem{},
		Metrics:         schemas.RunMetrics{NodesExecuted: []string{}, EdgesTraversed: []string{}},
		GoalDescription: goalDescription,
		InputData:       emptyDecisionMap(inputData),
		OutputData:      map[string]any{},
	}
}

// decisionRunID mirrors upstream's run_<timestamp>_<8 hex> shape (local time,
// second precision, uuid4 hex[:8]).
func decisionRunID() string {
	return fmt.Sprintf("run_%s_%s", time.Now().Format("20060102_150405"), randomHex8())
}

// decisionRunIDForStream mirrors upstream stream_runtime.start_run's
// run_<stream_id>_<timestamp>_<8 hex> shape.
func decisionRunIDForStream(streamID string) string {
	return fmt.Sprintf("run_%s_%s_%s", streamID, time.Now().Format("20060102_150405"), randomHex8())
}

func randomHex8() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// A failing entropy source must not block a ledger write; fall back to
		// the clock, which still keeps ids unique within one process.
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(buf[:])
}

func emptyDecisionMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func emptyDecisionStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

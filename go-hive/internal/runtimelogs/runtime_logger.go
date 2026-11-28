// RuntimeLogger captures runtime data during execution and writes it to the
// store. Each LogStep/LogNodeComplete writes immediately to disk (JSONL
// append); only the L1 summary is written at EndRun since it aggregates L2.
//
// Parity: framework/tracker/runtime_logger.py (module)
package runtimelogs

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// TraceContextProvider returns the process-level default (trace_id,
// execution_id) pair the logger stamps on every record. Upstream reads it from
// the observability ContextVar (framework/observability :: get_trace_context)
// which defaults to empty strings when nothing set it; the port keeps the same
// default and lets a host override it.
//
// Production hosts that run several sessions in one process should bind each
// run's pair with RuntimeLogger.SetTraceContext instead of assigning this
// global at request time: the variable is not synchronized, so a per-request
// assignment would race and could stamp one session's trace onto another's
// records.
//
// Parity: framework/observability/logging.py::get_trace_context
var TraceContextProvider func() (traceID, executionID string)

// tracePair is one logger's immutable trace binding. The value is replaced as
// a whole through an atomic pointer, so a record can never observe a half-old
// (trace_id, execution_id) mix.
type tracePair struct {
	traceID     string
	executionID string
}

// ToolCallInput is one tool call as the loop records it: upstream log_step
// consumes dicts with these keys (content maps to ToolCallLog.result).
type ToolCallInput struct {
	ToolUseID      string
	ToolName       string
	ToolInput      map[string]any
	Content        string
	IsError        bool
	StartTimestamp string
	DurationS      float64
}

// StepInput is the log_step payload (node_id/node_type/step_index plus
// optional detail); zero values match upstream's keyword defaults.
type StepInput struct {
	NodeID          string
	NodeType        string
	StepIndex       int
	LLMText         string
	ToolCalls       []ToolCallInput
	InputTokens     int
	OutputTokens    int
	LatencyMS       int
	Verdict         string
	VerdictFeedback string
	Error           string
	Stacktrace      string
	IsPartial       bool
}

// NodeCompleteInput is the log_node_complete payload. Error nil renders as
// JSON null exactly like upstream's `error: str | None = None`.
type NodeCompleteInput struct {
	NodeID        string
	NodeName      string
	NodeType      string
	Success       bool
	Error         *string
	Stacktrace    string
	TotalSteps    int
	TokensUsed    int
	InputTokens   int
	OutputTokens  int
	LatencyMS     int
	Attempt       int
	ExitStatus    string
	AcceptCount   int
	RetryCount    int
	EscalateCount int
	ContinueCount int
}

// RuntimeLogger captures runtime data during graph execution.
//
// Parity: framework/tracker/runtime_logger.py::RuntimeLogger
type RuntimeLogger struct {
	Store   *RuntimeLogStore
	AgentID string

	mu          sync.Mutex
	runID       string
	goalID      string
	startedAt   string
	loggedNodes map[string]struct{}
	// trace is this run's (trace_id, execution_id) binding. Nil falls back to
	// TraceContextProvider; a production host sets it per logger so sessions
	// sharing the process cannot read each other's trace.
	trace atomic.Pointer[tracePair]
}

// NewRuntimeLogger mirrors RuntimeLogger(store=store, agent_id=...).
func NewRuntimeLogger(store *RuntimeLogStore, agentID string) *RuntimeLogger {
	return &RuntimeLogger{
		Store:       store,
		AgentID:     agentID,
		loggedNodes: map[string]struct{}{},
	}
}

// SetTraceContext binds the (trace_id, execution_id) pair this logger stamps
// on every record. Hosts call it once per run, before logging; passing two
// empty strings restores the TraceContextProvider fallback.
//
// The binding is per logger, mirroring the execution-scoped ContextVar
// upstream's get_trace_context reads: concurrent sessions in one process keep
// their own ids instead of racing on a global.
//
// Parity: framework/observability/logging.py::set_trace_context
func (l *RuntimeLogger) SetTraceContext(traceID, executionID string) {
	if l == nil {
		return
	}
	if traceID == "" && executionID == "" {
		l.trace.Store(nil)
		return
	}
	l.trace.Store(&tracePair{traceID: traceID, executionID: executionID})
}

// NewTraceContext returns a fresh (trace_id, execution_id) pair: two
// independent 32-lowercase-hex ids, the shape upstream's uuid.uuid4().hex
// produces for each. A host binds one pair per run with SetTraceContext.
//
// Parity: framework/observability/logging.py::set_trace_context (uuid4().hex)
func NewTraceContext() (traceID, executionID string) {
	return randomHex(16), randomHex(16)
}

// RunID returns the run id StartRun assigned ("" before StartRun).
func (l *RuntimeLogger) RunID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runID
}

// StartRun starts a new run and returns its id. A provided sessionID is used
// as the run id (unified sessions); otherwise a new timestamped id is
// generated in the old format.
//
// Parity: framework/tracker/runtime_logger.py::RuntimeLogger.start_run
func (l *RuntimeLogger) StartRun(goalID, sessionID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if sessionID != "" {
		l.runID = sessionID
		if err := l.Store.EnsureSessionRunDir(l.runID); err != nil {
			log.Printf("runtimelogs: failed to create session run dir for %s: %v", l.runID, err)
		}
	} else {
		l.runID = time.Now().UTC().Format("20060102T150405") + "_" + randomHex(4)
		if err := l.Store.EnsureRunDir(l.runID); err != nil {
			log.Printf("runtimelogs: failed to create run dir for %s: %v", l.runID, err)
		}
	}
	l.goalID = goalID
	l.startedAt = nowISO()
	l.loggedNodes = map[string]struct{}{}
	return l.runID
}

// LogStep records data for one step within a node. Synchronous; appends to
// the JSONL file immediately.
//
// Parity: framework/tracker/runtime_logger.py::RuntimeLogger.log_step
func (l *RuntimeLogger) LogStep(in StepInput) error {
	calls := make([]ToolCallLog, 0, len(in.ToolCalls))
	for _, tc := range in.ToolCalls {
		input := tc.ToolInput
		if input == nil {
			input = map[string]any{}
		}
		calls = append(calls, ToolCallLog{
			ToolUseID:      tc.ToolUseID,
			ToolName:       tc.ToolName,
			ToolInput:      input,
			Result:         tc.Content,
			IsError:        tc.IsError,
			StartTimestamp: tc.StartTimestamp,
			DurationS:      tc.DurationS,
		})
	}
	traceID, executionID := l.traceContext()
	step := NodeStepLog{
		NodeID:          in.NodeID,
		NodeType:        in.NodeType,
		StepIndex:       in.StepIndex,
		LLMText:         in.LLMText,
		ToolCalls:       calls,
		InputTokens:     in.InputTokens,
		OutputTokens:    in.OutputTokens,
		LatencyMS:       in.LatencyMS,
		Verdict:         in.Verdict,
		VerdictFeedback: in.VerdictFeedback,
		Error:           in.Error,
		Stacktrace:      in.Stacktrace,
		IsPartial:       in.IsPartial,
		TraceID:         traceID,
		SpanID:          randomHex(8),
		ExecutionID:     executionID,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Store.AppendStep(l.runID, &step)
}

// LogNodeComplete records completion of a node, deriving the attention flags
// exactly as upstream does.
//
// Parity: framework/tracker/runtime_logger.py::RuntimeLogger.log_node_complete
func (l *RuntimeLogger) LogNodeComplete(in NodeCompleteInput) error {
	needsAttention := !in.Success
	reasons := []string{}
	if !in.Success && in.Error != nil {
		reasons = append(reasons, "Node "+in.NodeID+" failed: "+*in.Error)
	}
	if in.RetryCount > 3 {
		needsAttention = true
		reasons = append(reasons, "Excessive retries: "+itoa(in.RetryCount))
	}
	if in.EscalateCount > 2 {
		needsAttention = true
		reasons = append(reasons, "Excessive escalations: "+itoa(in.EscalateCount))
	}
	if in.LatencyMS > 60000 {
		needsAttention = true
		reasons = append(reasons, "High latency: "+itoa(in.LatencyMS)+"ms")
	}
	if in.TokensUsed > 100000 {
		needsAttention = true
		reasons = append(reasons, "High token usage: "+itoa(in.TokensUsed))
	}
	if in.TotalSteps > 20 {
		needsAttention = true
		reasons = append(reasons, "Many iterations: "+itoa(in.TotalSteps))
	}
	traceID, _ := l.traceContext()
	attempt := in.Attempt
	if attempt == 0 {
		attempt = 1
	}
	detail := NodeDetail{
		NodeID:           in.NodeID,
		NodeName:         in.NodeName,
		NodeType:         in.NodeType,
		Success:          in.Success,
		Error:            in.Error,
		Stacktrace:       in.Stacktrace,
		TotalSteps:       in.TotalSteps,
		TokensUsed:       in.TokensUsed,
		InputTokens:      in.InputTokens,
		OutputTokens:     in.OutputTokens,
		LatencyMS:        in.LatencyMS,
		Attempt:          attempt,
		ExitStatus:       in.ExitStatus,
		AcceptCount:      in.AcceptCount,
		RetryCount:       in.RetryCount,
		EscalateCount:    in.EscalateCount,
		ContinueCount:    in.ContinueCount,
		NeedsAttention:   needsAttention,
		AttentionReasons: reasons,
		TraceID:          traceID,
		SpanID:           randomHex(8),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.Store.AppendNodeDetail(l.runID, &detail)
	l.loggedNodes[in.NodeID] = struct{}{}
	return err
}

// EnsureNodeLogged guarantees an L2 entry: a no-op when the node already
// logged itself, otherwise a basic NodeDetail.
//
// Parity: framework/tracker/runtime_logger.py::RuntimeLogger.ensure_node_logged
func (l *RuntimeLogger) EnsureNodeLogged(nodeID, nodeName, nodeType string, success bool, errorMessage, stacktrace string, tokensUsed, latencyMS int) error {
	l.mu.Lock()
	_, logged := l.loggedNodes[nodeID]
	l.mu.Unlock()
	if logged {
		return nil
	}
	var errPtr *string
	if !success && errorMessage != "" {
		msg := errorMessage
		errPtr = &msg
	}
	return l.LogNodeComplete(NodeCompleteInput{
		NodeID:     nodeID,
		NodeName:   nodeName,
		NodeType:   nodeType,
		Success:    success,
		Error:      errPtr,
		Stacktrace: stacktrace,
		TokensUsed: tokensUsed,
		LatencyMS:  latencyMS,
		ExitStatus: "",
		Attempt:    1,
	})
}

// EndRun reads L2 back from disk, aggregates it into L1, and writes
// summary.json. Errors are logged, never returned: logging failure must
// never kill a successful run.
//
// Parity: framework/tracker/runtime_logger.py::RuntimeLogger.end_run
func (l *RuntimeLogger) EndRun(status string, durationMS int, nodePath []string, executionQuality string) {
	l.mu.Lock()
	runID := l.runID
	goalID := l.goalID
	agentID := l.AgentID
	startedAt := l.startedAt
	l.mu.Unlock()

	nodeDetails, err := l.Store.ReadNodeDetailsSync(runID)
	if err != nil {
		log.Printf("runtimelogs: failed to read L2 for run_id=%s: %v", runID, err)
	}
	totalInput := 0
	totalOutput := 0
	needsAttention := false
	attentionReasons := []string{}
	for _, nd := range nodeDetails {
		totalInput += nd.InputTokens
		totalOutput += nd.OutputTokens
		if nd.NeedsAttention {
			needsAttention = true
		}
		attentionReasons = append(attentionReasons, nd.AttentionReasons...)
	}
	traceID, executionID := l.traceContext()
	if nodePath == nil {
		nodePath = []string{}
	}
	summary := RunSummaryLog{
		RunID:              runID,
		AgentID:            agentID,
		GoalID:             goalID,
		Status:             status,
		TotalNodesExecuted: len(nodeDetails),
		NodePath:           nodePath,
		TotalInputTokens:   totalInput,
		TotalOutputTokens:  totalOutput,
		NeedsAttention:     needsAttention,
		AttentionReasons:   attentionReasons,
		StartedAt:          startedAt,
		DurationMS:         durationMS,
		ExecutionQuality:   executionQuality,
		TraceID:            traceID,
		ExecutionID:        executionID,
	}
	if err := l.Store.SaveSummary(runID, &summary); err != nil {
		log.Printf("runtimelogs: failed to save runtime logs for run_id=%s (non-fatal): %v", runID, err)
		return
	}
	log.Printf("runtimelogs: runtime logs saved: run_id=%s status=%s nodes=%d", runID, status, len(nodeDetails))
}

// traceContext reads this run's trace binding, falling back to the
// process-level provider default (empty strings when neither is set).
func (l *RuntimeLogger) traceContext() (string, string) {
	if l != nil {
		if pair := l.trace.Load(); pair != nil {
			return pair.traceID, pair.executionID
		}
	}
	if TraceContextProvider == nil {
		return "", ""
	}
	return TraceContextProvider()
}

// nowISO mirrors datetime.now(UTC).isoformat(): microsecond precision with an
// explicit +00:00 offset.
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000") + "+00:00"
}

// NowISO is the exported spelling hosts use to stamp tool-call start times
// with the same shape upstream's datetime.now(UTC).isoformat() produces.
func NowISO() string { return nowISO() }

// randomHex returns n random bytes as lowercase hex (upstream takes
// uuid4().hex[:2n] for span ids and run suffixes).
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString(make([]byte, n))
	}
	return hex.EncodeToString(buf)
}

// itoa is strconv.Itoa without the import noise at the call sites.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	digits := [20]byte{}
	i := len(digits)
	for v > 0 {
		i--
		digits[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

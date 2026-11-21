// Package runtimelogs ports the three-level runtime logging system: L1
// summary.json, L2 details.jsonl, L3 tool_logs.jsonl.
//
// Parity: framework/tracker/runtime_log_schemas.py (module)
//
// JSON shapes are field-for-field copies of the Pydantic models: the same
// names, the same declaration order (encoding/json marshals struct fields in
// order), and the same defaults. Pydantic's model_dump() emits every field, so
// nothing here uses omitempty; optional values that Python renders as null
// (NodeDetail.error) are pointers.
package runtimelogs

// ToolCallLog is a single tool call within a step.
//
// Parity: framework/tracker/runtime_log_schemas.py::ToolCallLog
type ToolCallLog struct {
	ToolUseID      string         `json:"tool_use_id"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	Result         string         `json:"result"`
	IsError        bool           `json:"is_error"`
	StartTimestamp string         `json:"start_timestamp"`
	DurationS      float64        `json:"duration_s"`
}

// NodeStepLog is the full tool and LLM detail for one step within a node.
// For an event loop, each iteration is a step; single-step nodes use 0.
//
// Parity: framework/tracker/runtime_log_schemas.py::NodeStepLog
type NodeStepLog struct {
	NodeID          string        `json:"node_id"`
	NodeType        string        `json:"node_type"`
	StepIndex       int           `json:"step_index"`
	LLMText         string        `json:"llm_text"`
	ToolCalls       []ToolCallLog `json:"tool_calls"`
	InputTokens     int           `json:"input_tokens"`
	OutputTokens    int           `json:"output_tokens"`
	LatencyMS       int           `json:"latency_ms"`
	Verdict         string        `json:"verdict"`
	VerdictFeedback string        `json:"verdict_feedback"`
	Error           string        `json:"error"`
	Stacktrace      string        `json:"stacktrace"`
	IsPartial       bool          `json:"is_partial"`
	TraceID         string        `json:"trace_id"`
	SpanID          string        `json:"span_id"`
	ParentSpanID    string        `json:"parent_span_id"`
	ExecutionID     string        `json:"execution_id"`
}

// NodeDetail is the per-node completion result and attention flags.
//
// Parity: framework/tracker/runtime_log_schemas.py::NodeDetail
type NodeDetail struct {
	NodeID           string   `json:"node_id"`
	NodeName         string   `json:"node_name"`
	NodeType         string   `json:"node_type"`
	Success          bool     `json:"success"`
	Error            *string  `json:"error"`
	Stacktrace       string   `json:"stacktrace"`
	TotalSteps       int      `json:"total_steps"`
	TokensUsed       int      `json:"tokens_used"`
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	LatencyMS        int      `json:"latency_ms"`
	Attempt          int      `json:"attempt"`
	ExitStatus       string   `json:"exit_status"`
	AcceptCount      int      `json:"accept_count"`
	RetryCount       int      `json:"retry_count"`
	EscalateCount    int      `json:"escalate_count"`
	ContinueCount    int      `json:"continue_count"`
	NeedsAttention   bool     `json:"needs_attention"`
	AttentionReasons []string `json:"attention_reasons"`
	TraceID          string   `json:"trace_id"`
	SpanID           string   `json:"span_id"`
}

// RunSummaryLog is the run-level summary for a full graph execution.
//
// Parity: framework/tracker/runtime_log_schemas.py::RunSummaryLog
type RunSummaryLog struct {
	RunID              string   `json:"run_id"`
	AgentID            string   `json:"agent_id"`
	GoalID             string   `json:"goal_id"`
	Status             string   `json:"status"`
	TotalNodesExecuted int      `json:"total_nodes_executed"`
	NodePath           []string `json:"node_path"`
	TotalInputTokens   int      `json:"total_input_tokens"`
	TotalOutputTokens  int      `json:"total_output_tokens"`
	NeedsAttention     bool     `json:"needs_attention"`
	AttentionReasons   []string `json:"attention_reasons"`
	StartedAt          string   `json:"started_at"`
	DurationMS         int      `json:"duration_ms"`
	ExecutionQuality   string   `json:"execution_quality"`
	TraceID            string   `json:"trace_id"`
	ExecutionID        string   `json:"execution_id"`
}

// RunDetailsLog is the L2 container: all node details for a run.
//
// Parity: framework/tracker/runtime_log_schemas.py::RunDetailsLog
type RunDetailsLog struct {
	RunID string       `json:"run_id"`
	Nodes []NodeDetail `json:"nodes"`
}

// RunToolLogs is the L3 container: all step logs for a run.
//
// Parity: framework/tracker/runtime_log_schemas.py::RunToolLogs
type RunToolLogs struct {
	RunID string        `json:"run_id"`
	Steps []NodeStepLog `json:"steps"`
}

// normalizeStep fills the Python defaults Pydantic would apply on
// model_dump(): empty calls/inputs serialize as [] and {}, never null.
func normalizeStep(step *NodeStepLog) {
	if step.ToolCalls == nil {
		step.ToolCalls = []ToolCallLog{}
	}
	for i := range step.ToolCalls {
		if step.ToolCalls[i].ToolInput == nil {
			step.ToolCalls[i].ToolInput = map[string]any{}
		}
	}
}

// normalizeDetail fills NodeDetail defaults ([] for attention reasons).
func normalizeDetail(detail *NodeDetail) {
	if detail.AttentionReasons == nil {
		detail.AttentionReasons = []string{}
	}
	if detail.Attempt == 0 {
		detail.Attempt = 1
	}
}

// normalizeSummary fills RunSummaryLog defaults ([] for the two lists).
func normalizeSummary(summary *RunSummaryLog) {
	if summary.NodePath == nil {
		summary.NodePath = []string{}
	}
	if summary.AttentionReasons == nil {
		summary.AttentionReasons = []string{}
	}
}

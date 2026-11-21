// Package pipeline is the configuration-driven trigger middleware of the
// colony runtime: every trigger passes through an ordered list of stages
// before a worker is spawned.
//
// The mechanism is opt-in. With no `pipeline.stages` in configuration.json the
// runner holds no stages and running it is a no-op, exactly like upstream's
// default `PipelineRunner([])`. A configured stage whose type is not
// registered fails loudly at startup instead of being skipped.
//
// Parity: framework/pipeline/stage.py
package pipeline

import "fmt"

// Action is the outcome category of one stage's Process call. The string
// values match upstream's PipelineResult.action literal.
type Action string

const (
	// ActionContinue passes the request through untouched.
	ActionContinue Action = "continue"
	// ActionReject short-circuits the pipeline; the trigger is refused.
	ActionReject Action = "reject"
	// ActionTransform replaces the request input for the remaining stages.
	ActionTransform Action = "transform"
)

// Result is what a stage returns from Process.
//
// Parity: framework/pipeline/stage.py::PipelineResult
type Result struct {
	Action Action
	// InputData replaces the context input when Action is ActionTransform.
	// A transform with nil InputData keeps the current input (upstream
	// updates ctx.input_data only when result.input_data is not None).
	InputData map[string]any
	// RejectionReason is surfaced in RejectedError when Action is
	// ActionReject.
	RejectionReason string
}

// ContinueResult is the pass-through result.
func ContinueResult() Result { return Result{Action: ActionContinue} }

// RejectResult refuses the request with the given reason.
func RejectResult(reason string) Result {
	return Result{Action: ActionReject, RejectionReason: reason}
}

// TransformResult replaces the request input for the remaining stages.
func TransformResult(inputData map[string]any) Result {
	return Result{Action: ActionTransform, InputData: inputData}
}

// Context carries one trigger request through the pipeline.
//
// Parity: framework/pipeline/stage.py::PipelineContext
type Context struct {
	// EntryPointID is the trigger id the request entered through.
	EntryPointID string
	// InputData is the mutable request payload; a transform stage replaces
	// it for every stage that follows.
	InputData map[string]any
	// CorrelationID optionally ties the request to a caller-side trace.
	CorrelationID string
	// SessionState is the caller-side session snapshot, e.g. session_id for
	// the rate-limit bucket key.
	SessionState map[string]any
	// Metadata is stage-to-stage scratch space (e.g. estimated_cost for the
	// cost guard).
	Metadata map[string]any
}

// NewContext builds a pipeline context with the entry point and input set and
// an empty metadata map.
func NewContext(entryPointID string, inputData map[string]any) *Context {
	return &Context{
		EntryPointID: entryPointID,
		InputData:    inputData,
		Metadata:     map[string]any{},
	}
}

// Clone returns a shallow copy of the context. The stage list may hand the
// same context to several stages, so callers that need isolation copy first.
func (c *Context) Clone() *Context {
	if c == nil {
		return nil
	}
	copied := *c
	return &copied
}

// Stage is one middleware unit: Name/Order place it in the runner, Initialize
// runs once at startup, Process handles each trigger, and PostProcess may
// rewrite the execution result afterwards.
//
// Infrastructure stages (llm_provider, credential_resolver, mcp_registry,
// skill_registry) do their work in Initialize and pass every request through;
// request stages (input_validation, rate_limit, cost_guard) do their work in
// Process.
//
// Parity: framework/pipeline/stage.py::PipelineStage
type Stage interface {
	// Name is the stage's identifier; it appears in logs and in a
	// rejection's error text (upstream uses the class name).
	Name() string
	// Order sorts the stage within the runner (ascending; ties keep
	// registration order).
	Order() int
	// Initialize runs once when the runtime starts; a failure aborts
	// startup rather than being swallowed.
	Initialize() error
	// Process handles one request.
	Process(ctx *Context) (Result, error)
	// PostProcess may rewrite the execution result; the default is a
	// pass-through. Errors here never fail the run.
	PostProcess(ctx *Context, result any) (any, error)
}

// BaseStage supplies the default Order/Initialize/PostProcess behavior so a
// stage only implements Name and its work method.
//
// Parity: framework/pipeline/stage.py::PipelineStage (defaults)
type BaseStage struct {
	StageName  string
	StageOrder int
}

// Name returns the configured stage name.
func (s BaseStage) Name() string { return s.StageName }

// Order returns the configured execution order.
func (s BaseStage) Order() int { return s.StageOrder }

// setOrder lets BuildStage apply a spec-level order override.
func (s *BaseStage) setOrder(order int) { s.StageOrder = order }

// Initialize is a no-op by default.
func (s BaseStage) Initialize() error { return nil }

// PostProcess is a pass-through by default.
func (s BaseStage) PostProcess(_ *Context, result any) (any, error) { return result, nil }

// RejectedError is returned by Runner.Run when a stage rejects the request.
// It mirrors upstream PipelineRejectedError, message included.
//
// Parity: framework/pipeline/stage.py::PipelineRejectedError
type RejectedError struct {
	StageName string
	Reason    string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("Pipeline rejected by %s: %s", e.StageName, e.Reason)
}

// StageError classifies a stage's own failure (as opposed to a rejection):
// the stage's Process returned an error. The runner wraps the original error
// so callers can tell "refused" from "broke" with errors.As.
type StageError struct {
	StageName string
	Err       error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("pipeline stage %s failed: %v", e.StageName, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

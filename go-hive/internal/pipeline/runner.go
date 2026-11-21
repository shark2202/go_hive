package pipeline

import (
	"errors"
	"sort"
)

// Runner executes a list of stages in ascending Order.
//
// Stages sort once at construction (and again on AddStage), ties keep their
// registration order — the same stable ordering upstream's sorted(stages,
// key=lambda s: s.order) produces. A reject short-circuits the run and is
// returned as *RejectedError; a stage failure is returned as *StageError;
// a nil context is refused.
//
// Parity: framework/pipeline/runner.py::PipelineRunner
type Runner struct {
	stages []Stage
}

// NewRunner builds a runner over the given stages, copied and sorted by Order.
func NewRunner(stages []Stage) *Runner {
	runner := &Runner{}
	for _, stage := range stages {
		if stage != nil {
			runner.stages = append(runner.stages, stage)
		}
	}
	runner.sort()
	return runner
}

// Stages returns a copy of the ordered stage list.
func (r *Runner) Stages() []Stage {
	if r == nil {
		return nil
	}
	return append([]Stage(nil), r.stages...)
}

// Len reports the number of registered stages.
func (r *Runner) Len() int {
	if r == nil {
		return 0
	}
	return len(r.stages)
}

// AddStage appends a stage and re-sorts, for dynamic registration.
func (r *Runner) AddStage(stage Stage) {
	if r == nil || stage == nil {
		return
	}
	r.stages = append(r.stages, stage)
	r.sort()
}

// InitializeAll calls Initialize on every stage in order. The first failure is
// returned (startup fails; the stage is never skipped silently).
func (r *Runner) InitializeAll() error {
	if r == nil {
		return nil
	}
	for _, stage := range r.stages {
		if err := stage.Initialize(); err != nil {
			return &StageError{StageName: stage.Name(), Err: err}
		}
	}
	return nil
}

// Run feeds the context through every stage. It returns the (possibly
// transformed) context, or an error: *RejectedError when a stage refuses the
// request, *StageError when a stage itself fails. With no stages it returns
// the context untouched — the default-pipeline no-op.
//
// Parity: framework/pipeline/runner.py::PipelineRunner.run
func (r *Runner) Run(ctx *Context) (*Context, error) {
	if ctx == nil {
		return nil, errors.New("pipeline: nil context")
	}
	if r == nil || len(r.stages) == 0 {
		return ctx, nil
	}
	for _, stage := range r.stages {
		result, err := stage.Process(ctx)
		if err != nil {
			return ctx, &StageError{StageName: stage.Name(), Err: err}
		}
		switch result.Action {
		case ActionReject:
			reason := result.RejectionReason
			if reason == "" {
				// Upstream substitutes this exact placeholder.
				reason = "(no reason given)"
			}
			return ctx, &RejectedError{StageName: stage.Name(), Reason: reason}
		case ActionTransform:
			// A transform without replacement input keeps the current one.
			if result.InputData != nil {
				ctx.InputData = result.InputData
			}
		}
		// Continue — and any unknown action, which upstream also treats as
		// a pass — falls through to the next stage.
	}
	return ctx, nil
}

// RunPost runs every stage's PostProcess hook in order, each transforming the
// previous value. A hook error is swallowed: post-processing must not break a
// successful execution, exactly as upstream logs and continues.
//
// Parity: framework/pipeline/runner.py::PipelineRunner.run_post
func (r *Runner) RunPost(ctx *Context, result any) any {
	if r == nil || len(r.stages) == 0 {
		return result
	}
	current := result
	for _, stage := range r.stages {
		next, err := stage.PostProcess(ctx, current)
		if err != nil {
			continue
		}
		current = next
	}
	return current
}

func (r *Runner) sort() {
	sort.SliceStable(r.stages, func(i, j int) bool {
		return r.stages[i].Order() < r.stages[j].Order()
	})
}

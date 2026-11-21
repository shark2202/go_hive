package pipeline

import (
	"errors"
	"reflect"
	"testing"
)

// fakeStage records its execution and returns a scripted result.
type fakeStage struct {
	BaseStage
	processed []string
	run       *[]string
	result    Result
	err       error
	postFn    func(any) any
	postErr   error
}

func newFakeStage(name string, order int, run *[]string, result Result) *fakeStage {
	stage := &fakeStage{
		BaseStage: BaseStage{StageName: name, StageOrder: order},
		run:       run,
		result:    result,
	}
	return stage
}

func (s *fakeStage) Process(_ *Context) (Result, error) {
	if s.run != nil {
		*s.run = append(*s.run, s.Name())
	}
	return s.result, s.err
}

func (s *fakeStage) PostProcess(_ *Context, result any) (any, error) {
	if s.postErr != nil {
		return result, s.postErr
	}
	if s.postFn != nil {
		return s.postFn(result), nil
	}
	return result, nil
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; this
// pins PipelineRunner.run's ascending-order execution against the source.
func TestRunnerExecutesStagesInAscendingOrder(t *testing.T) {
	var ran []string
	runner := NewRunner([]Stage{
		newFakeStage("late", 300, &ran, ContinueResult()),
		newFakeStage("early", 10, &ran, ContinueResult()),
		newFakeStage("mid", 200, &ran, ContinueResult()),
	})
	ctx := NewContext("trigger", map[string]any{})
	if _, err := runner.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{"early", "mid", "late"}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("execution order = %v, want %v", ran, want)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; ties
// keep registration order (Python's sorted() is stable).
func TestRunnerKeepsRegistrationOrderForTiedStages(t *testing.T) {
	var ran []string
	runner := NewRunner([]Stage{
		newFakeStage("first", 100, &ran, ContinueResult()),
		newFakeStage("second", 100, &ran, ContinueResult()),
	})
	if _, err := runner.Run(NewContext("t", map[string]any{})); err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(ran, want) {
		t.Fatalf("execution order = %v, want %v", ran, want)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; this
// pins the reject short-circuit and PipelineRejectedError shape.
func TestRunnerRejectShortCircuits(t *testing.T) {
	var ran []string
	runner := NewRunner([]Stage{
		newFakeStage("first", 10, &ran, ContinueResult()),
		newFakeStage("guard", 20, &ran, RejectResult("over budget")),
		newFakeStage("never", 30, &ran, ContinueResult()),
	})
	_, err := runner.Run(NewContext("t", map[string]any{}))
	var rejected *RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error = %v, want *RejectedError", err)
	}
	if rejected.StageName != "guard" || rejected.Reason != "over budget" {
		t.Fatalf("rejection = %+v", rejected)
	}
	if want := "Pipeline rejected by guard: over budget"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
	if want := []string{"first", "guard"}; !reflect.DeepEqual(ran, want) {
		t.Fatalf("execution order = %v, want %v (never must not run)", ran, want)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a reject
// without a reason carries the "(no reason given)" placeholder.
func TestRunnerRejectWithoutReasonUsesPlaceholder(t *testing.T) {
	runner := NewRunner([]Stage{
		newFakeStage("guard", 10, nil, Result{Action: ActionReject}),
	})
	_, err := runner.Run(NewContext("t", map[string]any{}))
	var rejected *RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error = %v, want *RejectedError", err)
	}
	if rejected.Reason != "(no reason given)" {
		t.Fatalf("reason = %q", rejected.Reason)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; this
// pins transform propagation into ctx.input_data.
func TestRunnerTransformPropagatesToLaterStages(t *testing.T) {
	var observed map[string]any
	spy := &spyStage{
		fakeStage: fakeStage{
			BaseStage: BaseStage{StageName: "spy", StageOrder: 200},
			result:    ContinueResult(),
		},
		onProcess: func(ctx *Context) { observed = ctx.InputData },
	}
	runner := NewRunner([]Stage{
		newFakeStage("transform", 10, nil, TransformResult(map[string]any{"task": "rewritten"})),
		spy,
	})
	ctx := NewContext("t", map[string]any{"task": "original"})
	out, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := out.InputData["task"]; got != "rewritten" {
		t.Fatalf("final input task = %v, want rewritten", got)
	}
	if got := observed["task"]; got != "rewritten" {
		t.Fatalf("later stage saw task = %v, want rewritten", got)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a
// transform result without input_data keeps the current input.
func TestRunnerTransformWithNilKeepsCurrentInput(t *testing.T) {
	runner := NewRunner([]Stage{
		newFakeStage("transform", 10, nil, Result{Action: ActionTransform}),
	})
	original := map[string]any{"task": "keep"}
	ctx := NewContext("t", original)
	out, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.InputData["task"] != "keep" {
		t.Fatalf("input mutated: %v", out.InputData)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a stage
// failure surfaces as a classified *StageError that wraps the cause.
func TestRunnerStageFailureIsClassified(t *testing.T) {
	cause := errors.New("boom")
	var ran []string
	runner := NewRunner([]Stage{
		&fakeStage{
			BaseStage: BaseStage{StageName: "broken", StageOrder: 10},
			run:       &ran,
			err:       cause,
		},
		newFakeStage("never", 20, &ran, ContinueResult()),
	})
	_, err := runner.Run(NewContext("t", map[string]any{}))
	var stageErr *StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("error = %v, want *StageError", err)
	}
	if stageErr.StageName != "broken" || !errors.Is(stageErr, cause) {
		t.Fatalf("stage error = %+v", stageErr)
	}
	if len(ran) != 1 {
		t.Fatalf("execution continued after failure: %v", ran)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the empty
// runner is the default no-op (returned context untouched).
func TestRunnerEmptyPipelineIsNoOp(t *testing.T) {
	var runner *Runner
	for _, candidate := range []*Runner{runner, NewRunner(nil), NewRunner([]Stage{})} {
		ctx := NewContext("t", map[string]any{"task": "x"})
		out, err := candidate.Run(ctx)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if out != ctx {
			t.Fatalf("context replaced by empty pipeline")
		}
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a nil
// context is refused instead of panicking.
func TestRunnerNilContextRejected(t *testing.T) {
	if _, err := NewRunner(nil).Run(nil); err == nil {
		t.Fatal("nil context must be refused")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline;
// post_process chains transforms and swallows hook failures.
func TestRunnerPostProcessChainsAndSwallows(t *testing.T) {
	broken := &fakeStage{
		BaseStage: BaseStage{StageName: "broken-post", StageOrder: 5},
		result:    ContinueResult(),
		postFn:    func(any) any { return "broken" },
	}
	broken.postErr = errors.New("post exploded")
	appender := &fakeStage{
		BaseStage: BaseStage{StageName: "appender", StageOrder: 10},
		result:    ContinueResult(),
		postFn:    func(value any) any { return value.(string) + "+suffix" },
	}
	runner := NewRunner([]Stage{broken, appender})
	got := runner.RunPost(NewContext("t", map[string]any{}), "base")
	if got != "base+suffix" {
		t.Fatalf("post result = %v, want base+suffix (broken hook skipped)", got)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline;
// add_stage keeps the runner ordered (runner.add_stage + re-sort).
func TestRunnerAddStageKeepsOrder(t *testing.T) {
	var ran []string
	runner := NewRunner([]Stage{newFakeStage("second", 200, &ran, ContinueResult())})
	runner.AddStage(newFakeStage("first", 10, &ran, ContinueResult()))
	if _, err := runner.Run(NewContext("t", map[string]any{})); err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(ran, want) {
		t.Fatalf("execution order = %v, want %v", ran, want)
	}
}

// spyStage observes the context without altering the run.
type spyStage struct {
	fakeStage
	onProcess func(ctx *Context)
}

func (s *spyStage) Process(ctx *Context) (Result, error) {
	if s.onProcess != nil {
		s.onProcess(ctx)
	}
	if s.run != nil {
		*s.run = append(*s.run, s.Name())
	}
	return s.result, s.err
}

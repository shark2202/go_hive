package playbook

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type PlaybookError struct {
	Message string
}

func (e *PlaybookError) Error() string { return e.Message }

type PlaybookScriptError struct {
	Message string
	Err     error
}

func (e *PlaybookScriptError) Error() string { return e.Message }

func (e *PlaybookScriptError) Unwrap() error { return e.Err }

type ScriptRuntimeError struct {
	Kind    string
	Message string
	Line    int
}

func (e *ScriptRuntimeError) Error() string { return e.Kind + ": " + e.Message }

type DeadLetter struct {
	mu    sync.Mutex
	items []map[string]any
}

func (d *DeadLetter) Add(item map[string]any) {
	d.mu.Lock()
	d.items = append(d.items, item)
	d.mu.Unlock()
}

func (d *DeadLetter) List() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]map[string]any, len(d.items))
	copy(out, d.items)
	return out
}

func (d *DeadLetter) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

type RateGate struct {
	MinInterval time.Duration

	mu     sync.Mutex
	nextAt *time.Time

	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func NewRateGate(ratePerMin float64) *RateGate {
	return &RateGate{MinInterval: time.Duration(60.0 / ratePerMin * float64(time.Second))}
}

func (g *RateGate) Acquire(ctx context.Context) error {
	nowFn := g.now
	if nowFn == nil {
		nowFn = time.Now
	}
	sleepFn := g.sleep
	if sleepFn == nil {
		sleepFn = sleepContext
	}

	now := nowFn()
	g.mu.Lock()
	start := now
	if g.nextAt != nil && g.nextAt.After(now) {
		start = *g.nextAt
	}
	next := start.Add(g.MinInterval)
	g.nextAt = &next
	wait := start.Sub(now)
	g.mu.Unlock()

	if wait > 0 {
		return sleepFn(ctx, wait)
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type DispatchOne func(ctx context.Context, task string, opts DispatchOptions) (map[string]any, error)

type QueryRows func(sql string) []map[string]any

type DispatchOptions struct {
	Data    map[string]any
	Profile string
	Timeout time.Duration
	Schema  map[string]any
	Goal    *string
}

type WorkerOptions struct {
	Profile string
	Lane    string
	Skill   string
	Retries int
	Backoff string
	Timeout *time.Duration
	Schema  map[string]any
	Phase   string
	Data    map[string]any
	Goal    *string
}

type WorkerResult struct {
	Status   string
	Receipt  map[string]any
	Attempts int
	Error    *string
}

type LaneOptions struct {
	Concurrency int
	RatePerMin  *int
}

type laneSpec struct {
	concurrency int
	ratePerMin  *int
}

type RunOptions struct {
	DispatchOne    DispatchOne
	QueryRows      QueryRows
	RunID          string
	DefaultTimeout *time.Duration
	ConcurrencyCap *int
}

type PlaybookRun struct {
	dispatchOne    DispatchOne
	queryRows      QueryRows
	RunID          string
	defaultTimeout time.Duration
	concurrencyCap *int

	Deadletter *DeadLetter
	Logs       []string
	Dispatched int

	mu        sync.Mutex
	lanes     map[string]chan struct{}
	laneSpecs map[string]laneSpec
	laneGates map[string]*RateGate
	phase     *string
	meta      map[string]any
	runFn     func(context.Context, any, *Hooks) (any, error)

	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func NewPlaybookRun(opts RunOptions) *PlaybookRun {
	timeout := 600 * time.Second
	if opts.DefaultTimeout != nil {
		timeout = *opts.DefaultTimeout
	}
	return &PlaybookRun{
		dispatchOne:    opts.DispatchOne,
		queryRows:      opts.QueryRows,
		RunID:          opts.RunID,
		defaultTimeout: timeout,
		concurrencyCap: opts.ConcurrencyCap,
		Deadletter:     &DeadLetter{},
		Logs:           []string{},
		lanes:          map[string]chan struct{}{},
		laneSpecs:      map[string]laneSpec{},
		laneGates:      map[string]*RateGate{},
	}
}

func (r *PlaybookRun) SetClock(now func() time.Time, sleep func(context.Context, time.Duration) error) {
	r.now = now
	r.sleep = sleep
}

func (r *PlaybookRun) clockNow() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *PlaybookRun) sleepFor(ctx context.Context, d time.Duration) error {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	return sleepContext(ctx, d)
}

func (r *PlaybookRun) Log(message string) {
	text := fmt.Sprint(message)
	r.mu.Lock()
	r.Logs = append(r.Logs, text)
	r.mu.Unlock()
}

func (r *PlaybookRun) Phase(title string) {
	text := fmt.Sprint(title)
	r.mu.Lock()
	r.phase = &text
	r.mu.Unlock()
	r.Log("phase: " + text)
}

func (r *PlaybookRun) CurrentPhase() *string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase == nil {
		return nil
	}
	out := *r.phase
	return &out
}

func (r *PlaybookRun) Lane(name string, opts LaneOptions) {
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	r.mu.Lock()
	r.lanes[name] = make(chan struct{}, concurrency)
	r.laneSpecs[name] = laneSpec{concurrency: concurrency, ratePerMin: opts.RatePerMin}
	var rate int
	hasRate := opts.RatePerMin != nil && *opts.RatePerMin > 0
	if hasRate {
		rate = *opts.RatePerMin
		gate := NewRateGate(float64(rate))
		gate.now = r.clockNow
		gate.sleep = r.sleepFor
		r.laneGates[name] = gate
	}
	cap := r.concurrencyCap
	r.mu.Unlock()

	if hasRate {
		r.Log(fmt.Sprintf("lane '%s': concurrency=%d, rate_per_min=%d (>= %.1fs between dispatches)",
			name, concurrency, rate, 60.0/float64(rate)))
	}
	if cap != nil && concurrency > *cap {
		r.Log(fmt.Sprintf(
			"note: lane '%s' concurrency=%d exceeds the colony cap (%d) — at most %d of this account's workers run at once. Raise max_concurrent_workers to go higher.",
			name, concurrency, *cap, *cap))
	}
}

func (r *PlaybookRun) TrackerQuery(sql string) []map[string]any {
	if r.queryRows == nil {
		return nil
	}
	return r.queryRows(sql)
}

func (r *PlaybookRun) TrackerCount(sql string) int {
	return len(r.TrackerQuery(sql))
}

func (r *PlaybookRun) laneSemaphore(lane string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lanes[lane]
}

func (r *PlaybookRun) laneGate(lane string) *RateGate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.laneGates[lane]
}

func (r *PlaybookRun) Worker(ctx context.Context, task string, opts WorkerOptions) (WorkerResult, error) {
	taskText := fmt.Sprint(task)
	if opts.Skill != "" && !strings.Contains(taskText, opts.Skill) {
		taskText = taskText + "\n\nFollow the '" + opts.Skill + "' skill."
	}
	timeout := r.defaultTimeout
	if opts.Timeout != nil {
		timeout = *opts.Timeout
	}
	attempts := opts.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastError string
	receipt := map[string]any{}

	for attempt := 1; attempt <= attempts; attempt++ {
		report, err := r.dispatchGuarded(ctx, taskText, DispatchOptions{
			Data:    opts.Data,
			Profile: opts.Profile,
			Timeout: timeout,
			Schema:  opts.Schema,
			Goal:    opts.Goal,
		}, opts.Lane)
		if err != nil {
			return WorkerResult{}, err
		}
		r.mu.Lock()
		r.Dispatched++
		r.mu.Unlock()

		status := "unknown"
		if report != nil {
			status = fmt.Sprint(report["status"])
			if report["status"] == nil {
				status = "unknown"
			}
			receipt = asMap(report["data"])
			if receipt == nil {
				receipt = map[string]any{}
			}
		}
		if status == "success" {
			return WorkerResult{Status: "done", Receipt: receipt, Attempts: attempt}, nil
		}
		if report != nil {
			if msg, ok := report["error"].(string); ok && msg != "" {
				lastError = msg
			} else {
				lastError = "worker status=" + status
			}
		} else {
			lastError = "worker status=" + status
		}
		if attempt < attempts {
			r.backoffSleep(ctx, opts.Backoff, attempt)
		}
	}
	return WorkerResult{Status: "failed", Receipt: receipt, Attempts: attempts, Error: &lastError}, nil
}

func (r *PlaybookRun) dispatchGuarded(ctx context.Context, taskText string, opts DispatchOptions, lane string) (map[string]any, error) {
	gate := r.laneGate(lane)
	if lane != "" && gate != nil {
		if err := gate.Acquire(ctx); err != nil {
			return nil, err
		}
	}
	sem := r.laneSemaphore(lane)
	if lane != "" && sem != nil {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-sem }()
	}
	if lane != "" && sem == nil {
		r.Log(fmt.Sprintf("warning: lane '%s' not declared via lane(); dispatching unthrottled", lane))
	}
	if r.dispatchOne == nil {
		return nil, errors.New("no dispatch_one configured")
	}
	return r.dispatchOne(ctx, taskText, opts)
}

func (r *PlaybookRun) backoffSleep(ctx context.Context, backoff string, attempt int) {
	switch backoff {
	case "exp":
		d := time.Duration(math.Min(30.0, math.Pow(2.0, float64(attempt))) * float64(time.Second))
		_ = r.sleepFor(ctx, d)
	case "linear":
		d := time.Duration(math.Min(30.0, 2.0*float64(attempt)) * float64(time.Second))
		_ = r.sleepFor(ctx, d)
	}
}

type Pending func() []map[string]any

type ConvergeDispatch func(ctx context.Context, row map[string]any, idx int) (any, error)

type ConvergeOptions struct {
	Pending        Pending
	Dispatch       ConvergeDispatch
	MaxRounds      int
	CircuitBreaker *float64
	Chunk          *int
}

type ConvergeResult struct {
	Rounds       int
	Converged    bool
	Remaining    int
	DeadLettered int
}

func (r *PlaybookRun) Converge(ctx context.Context, opts ConvergeOptions) ConvergeResult {
	cap := r.concurrencyCap
	chunk := opts.Chunk
	if chunk == nil && cap != nil {
		v := *cap
		chunk = &v
	}
	if cap != nil {
		r.mu.Lock()
		laneTotal := 0
		for _, spec := range r.laneSpecs {
			laneTotal += spec.concurrency
		}
		r.mu.Unlock()
		if (chunk != nil && *chunk > *cap) || laneTotal > *cap {
			chunkText := "None"
			if chunk != nil {
				chunkText = fmt.Sprint(*chunk)
			}
			r.Log(fmt.Sprintf(
				"note: colony max_concurrent_workers=%d bounds this run; chunk=%s / total lane concurrency=%d above it won't increase actual parallelism — raise the colony cap to go higher.",
				*cap, chunkText, laneTotal))
		}
	}

	maxRounds := opts.MaxRounds
	if maxRounds < 1 {
		maxRounds = 1
	}
	rounds := 0
	for round := 0; round < maxRounds; round++ {
		rows := r.pendingRows(opts.Pending)
		if len(rows) == 0 {
			break
		}
		rounds++
		results := r.dispatchRound(ctx, rows, opts.Dispatch, chunk)
		failed := 0
		for _, res := range results {
			if isFailure(res) {
				failed++
			}
		}
		r.Log(fmt.Sprintf("round %d: %d dispatched, %d ok, %d failed",
			rounds, len(rows), len(rows)-failed, failed))
		if opts.CircuitBreaker != nil && len(rows) > 0 &&
			float64(failed)/float64(len(rows)) > *opts.CircuitBreaker {
			r.Log(fmt.Sprintf("circuit breaker tripped: %d/%d failed > %.0f%%; stopping",
				failed, len(rows), *opts.CircuitBreaker*100))
			break
		}
	}

	remaining := r.pendingRows(opts.Pending)
	for _, row := range remaining {
		r.Deadletter.Add(map[string]any{
			"row":    row,
			"reason": fmt.Sprintf("unresolved after %d round(s)", rounds),
		})
	}
	return ConvergeResult{
		Rounds:       rounds,
		Converged:    len(remaining) == 0,
		Remaining:    len(remaining),
		DeadLettered: len(remaining),
	}
}

func (r *PlaybookRun) pendingRows(p Pending) []map[string]any {
	if p == nil {
		return nil
	}
	return p()
}

func (r *PlaybookRun) dispatchRound(ctx context.Context, rows []map[string]any, dispatch ConvergeDispatch, chunk *int) []any {
	var sem chan struct{}
	if chunk != nil && *chunk > 0 {
		sem = make(chan struct{}, *chunk)
	}
	results := make([]any, len(rows))
	var wg sync.WaitGroup
	for i, row := range rows {
		wg.Add(1)
		go func(idx int, row map[string]any) {
			defer wg.Done()
			if sem != nil {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					results[idx] = ctx.Err()
					return
				}
				defer func() { <-sem }()
			}
			if dispatch == nil {
				results[idx] = nil
				return
			}
			res, err := dispatch(ctx, row, idx)
			if err != nil {
				results[idx] = err
				return
			}
			results[idx] = res
		}(i, row)
	}
	wg.Wait()
	return results
}

func isFailure(result any) bool {
	if result == nil {
		return false
	}
	if err, ok := result.(error); ok {
		return err != nil
	}
	switch v := result.(type) {
	case WorkerResult:
		return v.Status != "done" && v.Status != ""
	case *WorkerResult:
		if v == nil {
			return false
		}
		return v.Status != "done" && v.Status != ""
	case map[string]any:
		status, present := v["status"]
		if !present || status == nil {
			return false
		}
		return fmt.Sprint(status) != "done"
	}
	return false
}

func asMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

type Hooks struct {
	Converge     func(ctx context.Context, opts ConvergeOptions) ConvergeResult
	Worker       func(ctx context.Context, task string, opts WorkerOptions) (WorkerResult, error)
	TrackerQuery func(sql string) []map[string]any
	TrackerCount func(sql string) int
	Lane         func(name string, opts LaneOptions)
	Deadletter   *DeadLetter
	Log          func(message string)
	Phase        func(title string)
}

func (r *PlaybookRun) Hooks() *Hooks {
	return &Hooks{
		Converge:     r.Converge,
		Worker:       r.Worker,
		TrackerQuery: r.TrackerQuery,
		TrackerCount: r.TrackerCount,
		Lane:         r.Lane,
		Deadletter:   r.Deadletter,
		Log:          r.Log,
		Phase:        r.Phase,
	}
}

type Script struct {
	Meta    map[string]any
	Run     func(ctx context.Context, args any, h *Hooks) (any, error)
	BodyErr error
}

func (r *PlaybookRun) Load(script *Script) error {
	if script != nil && script.BodyErr != nil {
		return &PlaybookScriptError{
			Message: fmt.Sprintf("playbook body failed to load: %v", script.BodyErr),
			Err:     script.BodyErr,
		}
	}
	var meta map[string]any
	if script != nil {
		meta = script.Meta
	}
	if meta == nil || !isTruthy(meta["name"]) {
		return &PlaybookScriptError{Message: "playbook must define a `meta` dict with at least a 'name'"}
	}
	if script.Run == nil {
		return &PlaybookScriptError{Message: "playbook must define `async def run(args)`"}
	}
	r.meta = meta
	r.runFn = script.Run
	return nil
}

func (r *PlaybookRun) RunLoaded(ctx context.Context, args any) (map[string]any, error) {
	if r.runFn == nil {
		return nil, &PlaybookScriptError{Message: "playbook not loaded"}
	}
	result, err := r.runFn(ctx, args, r.Hooks())
	if err != nil {
		var scriptErr *PlaybookScriptError
		if errors.As(err, &scriptErr) {
			return nil, scriptErr
		}
		return nil, &PlaybookScriptError{
			Message: fmt.Sprintf("playbook run() raised: %v", err),
			Err:     err,
		}
	}
	return map[string]any{
		"meta":       r.meta,
		"result":     result,
		"logs":       r.Logs,
		"deadletter": r.Deadletter.List(),
		"dispatched": r.Dispatched,
	}, nil
}

func (r *PlaybookRun) Execute(ctx context.Context, script *Script, args any) (map[string]any, error) {
	if err := r.Load(script); err != nil {
		return nil, err
	}
	return r.RunLoaded(ctx, args)
}

func RunPlaybookScript(ctx context.Context, script *Script, opts RunOptions, args any) (map[string]any, error) {
	run := NewPlaybookRun(opts)
	return run.Execute(ctx, script, args)
}

func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	}
	return true
}

func sortedKeys(m map[string]laneSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

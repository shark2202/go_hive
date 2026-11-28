package tasks

// Parity: framework/utils/task_registry.py::TaskRegistry
//
// Upstream's TaskRegistry owns `asyncio.create_task` work that nobody awaits:
// it keeps a strong reference until the task finishes, logs an exception the
// task raised, drops the reference on completion, and cancels everything on
// shutdown. Go keeps a goroutine alive without a strong reference, but the other
// two failure modes survive: a panic in a fire-and-forget goroutine takes the
// process down, and a returned error is visible only to whoever remembers to
// wait. Tracker closes both, and adds the name-keyed lookup/replacement the Go
// hosts need (upstream keys its per-execution dicts the same way).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Tracker owns named background goroutines for one owner (a runtime, a host).
// The zero value is ready to use; NewTracker records the owner name used in
// failure reports.
type Tracker struct {
	mu    sync.Mutex
	owner string
	tasks map[string]*TrackedTask
	next  int
	// logf is the unhandled-failure sink. Nil means log.Printf; tests replace
	// it to observe reports without touching the process logger.
	logf func(format string, args ...any)
}

// TrackedTask is the handle Start returns. Waiting on it yields the work's
// error (a recovered panic counts as an error); it is closed when the work
// returns, whether or not the task is still registered.
type TrackedTask struct {
	name   string
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	err       error
	cancelled bool
}

// NewTracker builds a tracker whose reports name owner (the Python `owner`
// argument). An empty owner reports as "?".
func NewTracker(owner string) *Tracker {
	return &Tracker{owner: owner, tasks: map[string]*TrackedTask{}}
}

// Start runs work in a tracked goroutine and returns its handle. A live task
// with the same name is cancelled first and its slot is replaced: the old
// task's completion can never remove the new entry (removal is identity
// checked). An empty name gets a generated one.
//
// The goroutine stops when work returns, when parent is cancelled, or when
// CancelAll cancels the tracker.
func (t *Tracker) Start(parent context.Context, name string, work func(context.Context) error) *TrackedTask {
	if t == nil || work == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}

	t.mu.Lock()
	if t.tasks == nil {
		t.tasks = map[string]*TrackedTask{}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		t.next++
		name = fmt.Sprintf("task-%d", t.next)
	}
	if previous := t.tasks[name]; previous != nil {
		previous.mu.Lock()
		previous.cancelled = true
		previous.mu.Unlock()
		previous.cancel()
	}
	ctx, cancel := context.WithCancel(parent)
	task := &TrackedTask{name: name, cancel: cancel, done: make(chan struct{})}
	t.tasks[name] = task
	t.mu.Unlock()

	go t.run(task, ctx, work)
	return task
}

// Lookup returns the task currently registered under name.
func (t *Tracker) Lookup(name string) (*TrackedTask, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	task, ok := t.tasks[strings.TrimSpace(name)]
	return task, ok
}

// Len is the number of registered tasks (upstream's __len__): finished tasks
// are gone, a task still unwinding after cancellation is not.
func (t *Tracker) Len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.tasks)
}

// CancelAll cancels every registered task and waits up to timeout for them to
// finish, returning how many were still running when it gave up (upstream logs
// that count; the caller may too). A timeout <= 0 cancels without waiting.
func (t *Tracker) CancelAll(timeout time.Duration) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	pending := make([]*TrackedTask, 0, len(t.tasks))
	for _, task := range t.tasks {
		pending = append(pending, task)
	}
	t.mu.Unlock()
	if len(pending) == 0 {
		return 0
	}
	for _, task := range pending {
		task.mu.Lock()
		task.cancelled = true
		task.mu.Unlock()
		task.cancel()
	}
	if timeout > 0 {
		allDone := make(chan struct{})
		go func() {
			for _, task := range pending {
				<-task.done
			}
			close(allDone)
		}()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-allDone:
			return 0
		case <-timer.C:
		}
	}
	live := 0
	for _, task := range pending {
		select {
		case <-task.done:
		default:
			live++
		}
	}
	if live > 0 {
		t.reportf("%d tracked task(s) still running after cancel", live)
	}
	return live
}

// Done is closed when the work returns.
func (task *TrackedTask) Done() <-chan struct{} {
	if task == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return task.done
}

// Wait blocks until the work returns or ctx is done, and returns the work's
// error (a recovered panic is reported as an error).
func (task *TrackedTask) Wait(ctx context.Context) error {
	if task == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-task.done:
		return task.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Err is the work's terminal error, nil until the work returns.
func (task *TrackedTask) Err() error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.err
}

func (t *Tracker) run(task *TrackedTask, ctx context.Context, work func(context.Context) error) {
	defer close(task.done)

	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				// A fire-and-forget goroutine must not take the process down
				// (upstream: the done callback logs the exception).
				err = fmt.Errorf("tracked task %q panicked: %v", task.name, recovered)
			}
		}()
		err = work(ctx)
	}()

	task.mu.Lock()
	task.err = err
	cancelled := task.cancelled
	task.mu.Unlock()

	t.mu.Lock()
	if t.tasks[task.name] == task {
		delete(t.tasks, task.name)
	}
	t.mu.Unlock()

	// A cancelled task returning context.Canceled is the expected unwind, not
	// an unhandled failure (upstream checks task.cancelled() before calling
	// task.exception()).
	if err != nil && !cancelled && !errors.Is(err, context.Canceled) {
		t.reportf("tracked task %q (owner=%s) raised an unhandled error: %v", task.name, t.ownerName(), err)
	}
}

func (t *Tracker) ownerName() string {
	owner := strings.TrimSpace(t.owner)
	if owner == "" {
		return "?"
	}
	return owner
}

func (t *Tracker) reportf(format string, args ...any) {
	logf := t.logf
	if logf == nil {
		logf = log.Printf
	}
	logf(format, args...)
}

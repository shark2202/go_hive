package tasks

// Parity-exempt: core/framework/utils/task_registry.py ships no test module
// (only the framework.utils re-export); these tests pin the ported lifecycle,
// lookup, replacement, cleanup and cancellation semantics directly.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitForTracker(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !condition() {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Parity-exempt: pins the ported TaskRegistry lifetime (strong reference until
// done, removal on completion, lookup by name) — upstream has no test module.
func TestTrackerRegistersLooksUpAndCleansUp(t *testing.T) {
	tracker := NewTracker("test")
	started := make(chan struct{})
	release := make(chan struct{})
	task := tracker.Start(context.Background(), "worker", func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	<-started

	found, ok := tracker.Lookup("worker")
	if !ok || found != task {
		t.Fatalf("Lookup(worker) = %v/%v, want the started task", found, ok)
	}
	if tracker.Len() != 1 {
		t.Fatalf("Len = %d, want 1", tracker.Len())
	}
	if _, ok := tracker.Lookup("missing"); ok {
		t.Fatal("Lookup(missing) = found, want false (negative lookup)")
	}

	close(release)
	if err := task.Wait(context.Background()); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	waitForTracker(t, "the finished task to be dropped", func() bool {
		_, ok := tracker.Lookup("worker")
		return !ok && tracker.Len() == 0
	})
}

// Parity-exempt: concurrent registration is the Go reason this type exists;
// upstream's set.add is not exercised concurrently in its (absent) tests.
func TestTrackerConcurrentRegistration(t *testing.T) {
	const workers = 32
	tracker := NewTracker("concurrent")
	release := make(chan struct{})
	started := make(chan struct{}, workers)

	var group sync.WaitGroup
	handles := make([]*TrackedTask, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			handles[index] = tracker.Start(context.Background(), fmt.Sprintf("worker-%02d", index), func(context.Context) error {
				started <- struct{}{}
				<-release
				return nil
			})
		}(index)
	}
	group.Wait()
	for index := 0; index < workers; index++ {
		<-started
	}
	if tracker.Len() != workers {
		t.Fatalf("Len = %d, want %d concurrent registrations", tracker.Len(), workers)
	}
	for index, handle := range handles {
		if handle == nil {
			t.Fatalf("handles[%d] = nil", index)
		}
		if found, ok := tracker.Lookup(fmt.Sprintf("worker-%02d", index)); !ok || found != handle {
			t.Fatalf("Lookup(worker-%02d) did not return its own handle", index)
		}
	}

	close(release)
	for _, handle := range handles {
		if err := handle.Wait(context.Background()); err != nil {
			t.Fatalf("Wait = %v, want nil", err)
		}
	}
	waitForTracker(t, "all finished registrations to be dropped", func() bool { return tracker.Len() == 0 })
}

// Parity-exempt: name-keyed replacement is the Go lookup contract this port
// adds for the host's named watchers (upstream keys per-execution dicts the
// same way); no upstream test module.
func TestTrackerReplacesAnExistingName(t *testing.T) {
	tracker := NewTracker("replacement")
	firstStarted := make(chan struct{})
	first := tracker.Start(context.Background(), "same", func(ctx context.Context) error {
		close(firstStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	<-firstStarted

	release := make(chan struct{})
	second := tracker.Start(context.Background(), "same", func(context.Context) error {
		<-release
		return nil
	})

	if found, ok := tracker.Lookup("same"); !ok || found != second {
		t.Fatalf("Lookup(same) = %v/%v, want the replacement", found, ok)
	}
	if tracker.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (the replacement)", tracker.Len())
	}

	// The replaced task unwinds on its cancelled context without deleting the
	// replacement's registry entry.
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.Wait(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("replaced task Wait = %v, want context.Canceled", err)
	}
	if found, ok := tracker.Lookup("same"); !ok || found != second {
		t.Fatalf("replaced task's completion removed the replacement: %v/%v", found, ok)
	}

	close(release)
	if err := second.Wait(context.Background()); err != nil {
		t.Fatalf("replacement Wait = %v, want nil", err)
	}
	waitForTracker(t, "the replacement to be dropped", func() bool { return tracker.Len() == 0 })
}

// Parity-exempt: negative regression — a panic in pinned-goroutine work must be
// reported, not crash the process, and a cancelled task's context.Canceled must
// not be reported as an unhandled failure (upstream checks task.cancelled()).
func TestTrackerReportsFailuresButNotCancellation(t *testing.T) {
	tracker := NewTracker("reports")
	var mu sync.Mutex
	var reports []string
	tracker.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, fmt.Sprintf(format, args...))
	}

	panicked := tracker.Start(context.Background(), "boom", func(context.Context) error {
		panic("kaboom")
	})
	if err := panicked.Wait(context.Background()); err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("panicking Wait = %v, want an error naming the panic", err)
	}

	failed := tracker.Start(context.Background(), "failed", func(context.Context) error {
		return errors.New("nope")
	})
	if err := failed.Wait(context.Background()); err == nil || err.Error() != "nope" {
		t.Fatalf("failing Wait = %v, want nope", err)
	}

	cancelled := tracker.Start(context.Background(), "cancelled", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if live := tracker.CancelAll(time.Second); live != 0 {
		t.Fatalf("CancelAll = %d still running, want 0", live)
	}
	if err := cancelled.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Wait = %v, want context.Canceled", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 2 {
		t.Fatalf("reports = %q, want exactly the panic and the error", reports)
	}
	if !strings.Contains(reports[0], "boom") || !strings.Contains(reports[0], "unhandled error") {
		t.Fatalf("first report = %q, want the panic reported", reports[0])
	}
	if !strings.Contains(reports[1], "failed") {
		t.Fatalf("second report = %q, want the returned error reported", reports[1])
	}
	for _, report := range reports {
		if strings.Contains(report, "cancelled") {
			t.Fatalf("cancellation was reported as a failure: %q", report)
		}
	}
}

// Parity-exempt: upstream's cancel_all(timeout) semantics, including the
// timeout branch that reports tasks which ignored cancellation.
func TestTrackerCancelAllCancelsAndBoundsTheWait(t *testing.T) {
	tracker := NewTracker("shutdown")
	started := make(chan struct{}, 2)
	handles := make([]*TrackedTask, 2)
	for index := range handles {
		handles[index] = tracker.Start(context.Background(), fmt.Sprintf("watcher-%d", index), func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}
	<-started
	<-started

	if live := tracker.CancelAll(2 * time.Second); live != 0 {
		t.Fatalf("CancelAll = %d still running, want 0", live)
	}
	for _, handle := range handles {
		if err := handle.Wait(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait after CancelAll = %v, want context.Canceled", err)
		}
	}
	waitForTracker(t, "cancelled tasks to be dropped", func() bool { return tracker.Len() == 0 })

	// A task that ignores its context must be reported as still running when
	// the timeout expires, and must not keep the registry alive after it exits.
	release := make(chan struct{})
	stubborn := tracker.Start(context.Background(), "stubborn", func(context.Context) error {
		<-release
		return nil
	})
	if live := tracker.CancelAll(20 * time.Millisecond); live != 1 {
		t.Fatalf("CancelAll(stubborn) = %d still running, want 1", live)
	}
	close(release)
	if err := stubborn.Wait(context.Background()); err != nil {
		t.Fatalf("stubborn Wait = %v, want nil", err)
	}
	waitForTracker(t, "the stubborn task to be dropped", func() bool { return tracker.Len() == 0 })
}

// Parity-exempt: negative regression — an empty/zero tracker must be a quiet
// no-op instead of panicking on a nil map or a nil receiver.
func TestTrackerEmptyAndZeroValueAreSafe(t *testing.T) {
	empty := NewTracker("")
	if empty.Len() != 0 {
		t.Fatalf("Len = %d, want 0", empty.Len())
	}
	if live := empty.CancelAll(0); live != 0 {
		t.Fatalf("CancelAll = %d, want 0", live)
	}
	if _, ok := empty.Lookup("absent"); ok {
		t.Fatal("Lookup(absent) = found, want false")
	}

	var zero Tracker
	done := make(chan struct{})
	handle := zero.Start(context.Background(), "", func(context.Context) error {
		close(done)
		return nil
	})
	<-done
	if err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("zero-value Start Wait = %v, want nil", err)
	}
	waitForTracker(t, "the unnamed task to be dropped", func() bool { return zero.Len() == 0 })

	var nilTracker *Tracker
	if nilTracker.Len() != 0 || nilTracker.CancelAll(0) != 0 {
		t.Fatal("nil tracker should be a no-op")
	}
	if _, ok := nilTracker.Lookup("x"); ok {
		t.Fatal("nil tracker Lookup = found")
	}
	if nilTracker.Start(context.Background(), "x", func(context.Context) error { return nil }) != nil {
		t.Fatal("nil tracker Start = non-nil")
	}
}

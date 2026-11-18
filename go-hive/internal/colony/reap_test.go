package colony

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// Parity: core/tests/test_colony_reap_timeout.py::test_reap_gives_up_on_a_bridge_that_never_answers
func TestReapGivesUpOnBridgeThatNeverAnswers(t *testing.T) {
	var mu sync.Mutex
	var abandoned []string
	reasonSeen := map[string]string{}
	profileSeen := map[string]string{}

	neverAnswers := func(ctx context.Context, profileName, reason, browserProfile string) error {
		select {
		case <-ctx.Done():
			mu.Lock()
			abandoned = append(abandoned, profileName)
			reasonSeen[profileName] = reason
			profileSeen[profileName] = browserProfile
			mu.Unlock()
			return ctx.Err()
		}
	}

	reaper := BrowserReaper{Close: neverAnswers, Timeout: 10 * time.Millisecond}

	done := make(chan struct{})
	go func() {
		reaper.Reap(context.Background(), []string{"w1", "w2", "w3"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("reap hung on an unresponsive bridge")
	}

	mu.Lock()
	got := append([]string(nil), abandoned...)
	mu.Unlock()
	sort.Strings(got)
	want := []string{"w1", "w2", "w3"}
	if len(got) != len(want) {
		t.Fatalf("abandoned = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("abandoned = %#v, want %#v", got, want)
		}
	}
	for _, id := range want {
		if reasonSeen[id] != "colony_shutdown" {
			t.Errorf("reason for %s = %q, want colony_shutdown", id, reasonSeen[id])
		}
		if profileSeen[id] != "default" {
			t.Errorf("profile for %s = %q, want default", id, profileSeen[id])
		}
	}
}

// Parity-exempt: Go-side contract for the reference's early returns — no worker
func TestReapNoopsWithoutWorkersOrBridge(t *testing.T) {
	called := 0
	reaper := BrowserReaper{Close: func(context.Context, string, string, string) error {
		called++
		return nil
	}}
	reaper.Reap(context.Background(), nil)
	if called != 0 {
		t.Fatalf("close called %d times for empty worker list", called)
	}

	inert := BrowserReaper{}
	inert.Reap(context.Background(), []string{"w1"})
	if called != 0 {
		t.Fatalf("close called %d times with no bridge", called)
	}
}

// Parity-exempt: one failing close must not stop the others; errors are
func TestReapSwallowsCloseErrors(t *testing.T) {
	var mu sync.Mutex
	closed := map[string]bool{}
	reaper := BrowserReaper{
		Close: func(_ context.Context, profileName, _ string, _ string) error {
			mu.Lock()
			closed[profileName] = true
			mu.Unlock()
			if profileName == "w1" {
				return errors.New("bridge exploded")
			}
			return nil
		},
		Timeout: time.Second,
	}

	reaper.Reap(context.Background(), []string{"w1", "w2", "w3"})

	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"w1", "w2", "w3"} {
		if !closed[id] {
			t.Errorf("worker %s was not closed", id)
		}
	}
}

// Parity-exempt: a worker's own profile is passed through; unknown and
func TestReapResolvesWorkerProfiles(t *testing.T) {
	profiles := map[string]string{"w1": "work", "w2": ""}
	var mu sync.Mutex
	seen := map[string]string{}
	reaper := BrowserReaper{
		Close: func(_ context.Context, workerID, _ string, browserProfile string) error {
			mu.Lock()
			seen[workerID] = browserProfile
			mu.Unlock()
			return nil
		},
		Profile: func(workerID string) string { return profiles[workerID] },
		Timeout: time.Second,
	}

	reaper.Reap(context.Background(), []string{"w1", "w2", "w3"})

	mu.Lock()
	defer mu.Unlock()
	want := map[string]string{"w1": "work", "w2": "default", "w3": "default"}
	for id, profile := range want {
		if seen[id] != profile {
			t.Errorf("profile for %s = %q, want %q", id, seen[id], profile)
		}
	}
}

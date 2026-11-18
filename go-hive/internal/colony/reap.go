package colony

import (
	"context"
	"sync"
	"time"
)

const DefaultBrowserReapTimeout = 5 * time.Second

const browserShutdownReason = "colony_shutdown"

const defaultBrowserProfile = "default"

const reapCancelGrace = time.Second

type CloseProfileContext func(ctx context.Context, profileName, reason, browserProfile string) error

type BrowserReaper struct {
	Close   CloseProfileContext
	Profile func(workerID string) string
	Timeout time.Duration
}

func (r BrowserReaper) Reap(ctx context.Context, workerIDs []string) {
	if len(workerIDs) == 0 || r.Close == nil {
		return
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultBrowserReapTimeout
	}

	var wg sync.WaitGroup
	for _, workerID := range workerIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r.reapOne(ctx, id, timeout)
		}(workerID)
	}
	wg.Wait()
}

func (r BrowserReaper) reapOne(ctx context.Context, workerID string, timeout time.Duration) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Close(callCtx, workerID, browserShutdownReason, r.profile(workerID))
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return
	case <-ctx.Done():
		cancel()
	case <-timer.C:
		cancel()
	}
	r.awaitUnwind(done)
}

func (r BrowserReaper) awaitUnwind(done <-chan struct{}) {
	grace := time.NewTimer(reapCancelGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
	}
}

func (r BrowserReaper) profile(workerID string) string {
	if r.Profile == nil {
		return defaultBrowserProfile
	}
	if profile := r.Profile(workerID); profile != "" {
		return profile
	}
	return defaultBrowserProfile
}

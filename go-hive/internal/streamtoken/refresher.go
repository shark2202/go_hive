package streamtoken

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

const (
	// CredentialID is the credential the runtime's hive streamToken lives in.
	//
	// Parity: core/framework/server/streamtoken_refresh.py::CREDENTIAL_ID
	CredentialID = "hive"
	// KeyName is the key inside that credential.
	//
	// Parity: core/framework/server/streamtoken_refresh.py::KEY_NAME
	KeyName = "api_key"
)

// CredentialStore is the slice of the credential store the refresher needs. The
// loop re-reads on every tick so a token pushed by the desktop through the
// credentials API is picked up without a restart.
type CredentialStore interface {
	GetCredential(credentialID string, refreshIfNeeded bool) (*credentials.CredentialObject, error)
	SaveCredential(credential *credentials.CredentialObject) error
}

// Options configures a Refresher. Zero values select production behaviour.
type Options struct {
	// BaseURL is the cloud base (HIVE_CLOUD_BASE). Empty disables the loop.
	BaseURL string
	// Store persists the rotated token. Required for a running loop.
	Store CredentialStore
	// Client is the refresh endpoint client. Nil builds the production client.
	Client *Client
	// Policy overrides DefaultPolicy.
	Policy *Policy
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
	// Sleep waits for a tick. Nil uses the production interruptible sleep;
	// tests inject a fake to drive ticks without wall-clock waits.
	Sleep func(ctx context.Context, duration time.Duration) error
	// Log receives progress lines. Nil uses log.Printf.
	Log func(format string, args ...any)
}

// Refresher rotates the managed hive streamToken before its TTL expires.
type Refresher struct {
	baseURL string
	store   CredentialStore
	client  *Client
	policy  Policy
	now     func() time.Time
	sleep   func(ctx context.Context, duration time.Duration) error
	logf    func(format string, args ...any)

	mu    sync.Mutex
	force bool
	wake  chan struct{}

	// refreshMu serialises forced refreshes so N concurrent callers that all
	// saw hive_stream_token_invalid produce one POST, not N.
	refreshMu sync.Mutex
}

// New builds a refresher. It does not start anything; call Run (or Start).
func New(opts Options) *Refresher {
	refresher := &Refresher{
		baseURL: strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		store:   opts.Store,
		client:  opts.Client,
		now:     opts.Now,
		logf:    opts.Log,
		wake:    make(chan struct{}),
	}
	if opts.Policy != nil {
		refresher.policy = *opts.Policy
	} else {
		refresher.policy = DefaultPolicy
	}
	if refresher.client == nil {
		refresher.client = NewClient()
	}
	if refresher.now == nil {
		refresher.now = time.Now
	}
	if refresher.logf == nil {
		refresher.logf = log.Printf
	}
	refresher.sleep = opts.Sleep
	if refresher.sleep == nil {
		refresher.sleep = refresher.sleepWithForce
	}
	return refresher
}

// Enabled reports whether the loop has somewhere to refresh against.
func (r *Refresher) Enabled() bool {
	return r != nil && r.baseURL != "" && r.store != nil
}

// Run is the background loop. It returns immediately in a non-cloud runtime
// (no HIVE_CLOUD_BASE) and on context cancellation; a failed tick never kills
// it.
//
// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func (r *Refresher) Run(ctx context.Context) {
	if r.baseURL == "" {
		r.logf("streamtoken: disabled (no HIVE_CLOUD_BASE — runtime not in cloud mode)")
		return
	}
	if r.store == nil {
		r.logf("streamtoken: disabled (no credential store)")
		return
	}
	r.logf(
		"streamtoken: started (threshold=%ds, min_sleep=%ds, max_sleep=%ds, base=%s)",
		r.policy.RefreshBeforeSeconds, r.policy.MinSleepSeconds, r.policy.MaxSleepSeconds, r.baseURL,
	)
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			r.logf("streamtoken: cancelled")
			return
		}
		if err := r.tick(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				r.logf("streamtoken: cancelled")
				return
			}
			r.logf("streamtoken: tick failed: %v", err)
			if sleepErr := r.sleep(ctx, r.minSleep()); sleepErr != nil {
				r.logf("streamtoken: cancelled")
				return
			}
		}
	}
}

// tick performs one read-sleep-recheck-refresh cycle. A returned error means
// the tick itself broke (Run logs and backs off); ordinary refresh failures are
// logged here and leave the tick successful so the next one retries.
//
// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop (body)
func (r *Refresher) tick(ctx context.Context) error {
	token, ok := r.readToken()
	if !ok {
		// No hive credential yet: the desktop has not pushed one via
		// configureRemoteLlm. Wait briefly and re-check.
		return r.sleep(ctx, r.minSleep())
	}

	var exp *int64
	if value, ok := DecodeJWTExp(token); ok {
		exp = &value
	}
	sleepSeconds := r.policy.SleepSeconds(exp, r.nowUnix())
	if err := r.sleep(ctx, time.Duration(sleepSeconds)*time.Second); err != nil {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	// Re-read after the sleep: the desktop may have pushed a fresh token in the
	// meantime, in which case ours may already be valid again.
	current, ok := r.readToken()
	if !ok {
		return nil
	}
	currentExp, hasExp := DecodeJWTExp(current)
	forced := r.forcePending()
	if hasExp && !r.policy.NeedsRefresh(&currentExp, r.nowUnix()) && !forced {
		return nil
	}
	r.clearForce()

	newToken, err := r.client.Refresh(ctx, r.baseURL, current)
	if err != nil {
		r.logf("streamtoken: refresh failed: %v", err)
		// Failed refresh: the stored credential stays as it was and the next
		// tick retries. The force flag is cleared so a backend outage does not
		// turn into a busy loop.
		r.clearForce()
		return nil
	}
	if r.saveToken(newToken) {
		newExp, _ := DecodeJWTExp(newToken)
		r.logf("streamtoken: rotated token (new_exp=%d, %ds from now)", newExp, newExp-int64(r.nowUnix()))
	}
	r.clearForce()
	return nil
}

// ForceRefresh wakes the loop and marks the next tick as forced. Idempotent:
// multiple calls between ticks collapse into a single forced tick.
//
// Parity: core/framework/server/streamtoken_refresh.py::start_streamtoken_refresh
// (_force_refresh / app["streamtoken_force_refresh"])
func (r *Refresher) ForceRefresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.force = true
	close(r.wake)
	r.wake = make(chan struct{})
}

// RefreshNow forces a refresh right now. Concurrent callers serialise: one
// POST goes out, and the rest observe the fresh credential (or its rotated
// value) on their re-read. It returns the token to retry with and true on
// success, or "" and false when refresh is not configured, no credential is
// stored, or the backend fails.
//
// Parity: core/framework/server/streamtoken_refresh.py::try_refresh_now
func (r *Refresher) RefreshNow(ctx context.Context) (string, bool) {
	if !r.Enabled() {
		return "", false
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	current, ok := r.readToken()
	if !ok {
		return "", false
	}
	if exp, ok := DecodeJWTExp(current); ok && !r.policy.NeedsRefresh(&exp, r.nowUnix()) {
		// Another caller refreshed the credential while we waited for the
		// lock. Anything tighter than the threshold might be the very token
		// the caller was rejected for, so only a clearly fresh token skips.
		r.logf("streamtoken: forced refresh skipped — credential was already refreshed concurrently (exp=%d)", exp)
		return current, true
	}
	newToken, err := r.client.Refresh(ctx, r.baseURL, current)
	if err != nil {
		r.logf("streamtoken: forced refresh failed: %v", err)
		return "", false
	}
	if !r.saveToken(newToken) {
		// Saved-but-failed should not happen with the encrypted store; return
		// the live token so the caller can still retry.
		return newToken, true
	}
	r.logf("streamtoken: forced refresh succeeded")
	return newToken, true
}

// sleepWithForce waits for the duration, returning early when ForceRefresh
// fires or the context is cancelled.
//
// Parity: core/framework/server/streamtoken_refresh.py::_sleep_with_force
func (r *Refresher) sleepWithForce(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	// Snapshot the wake channel before checking the flag: a ForceRefresh that
	// lands between the two either closes this snapshot (select wakes) or is
	// already visible as a pending flag.
	wake := r.wakeChan()
	if r.forcePending() {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	case <-wake:
		return nil
	}
}

func (r *Refresher) wakeChan() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wake
}

func (r *Refresher) forcePending() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.force
}

func (r *Refresher) clearForce() {
	r.mu.Lock()
	r.force = false
	r.mu.Unlock()
}

func (r *Refresher) readToken() (string, bool) {
	credential, err := r.store.GetCredential(CredentialID, false)
	if err != nil || credential == nil {
		return "", false
	}
	value, ok := credential.GetKeyValue(KeyName)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

// saveToken writes the rotated token back under the same credential id. The
// store's write path refreshes its own cache, so the next LLM call reads the
// new value.
//
// Parity: core/framework/server/streamtoken_refresh.py::_save_token
func (r *Refresher) saveToken(token string) bool {
	credential := credentials.NewCredentialObject(CredentialID)
	credential.SetKey(KeyName, token, nil, nil)
	if err := r.store.SaveCredential(credential); err != nil {
		r.logf("streamtoken: failed to save credential: %v", err)
		return false
	}
	return true
}

func (r *Refresher) minSleep() time.Duration {
	return time.Duration(r.policy.MinSleepSeconds) * time.Second
}

func (r *Refresher) nowUnix() float64 {
	return float64(r.now().UnixNano()) / 1e9
}

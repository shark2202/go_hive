package streamtoken

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

var testEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

type fakeStore struct {
	mu         sync.Mutex
	credential *credentials.CredentialObject
	saves      int
	saved      chan struct{}
	savedOnce  sync.Once
}

func newFakeStore(token string) *fakeStore {
	store := &fakeStore{saved: make(chan struct{})}
	if token != "" {
		store.credential = hiveCredential(token)
	}
	return store
}

func hiveCredential(token string) *credentials.CredentialObject {
	credential := credentials.NewCredentialObject(CredentialID)
	credential.SetKey(KeyName, token, nil, nil)
	return credential
}

func (s *fakeStore) GetCredential(credentialID string, _ bool) (*credentials.CredentialObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credential == nil || s.credential.ID != credentialID {
		return nil, nil
	}
	return s.credential, nil
}

func (s *fakeStore) SaveCredential(credential *credentials.CredentialObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.credential = credential
	s.savedOnce.Do(func() { close(s.saved) })
	return nil
}

func (s *fakeStore) token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credential == nil {
		return ""
	}
	value, _ := s.credential.GetKeyValue(KeyName)
	return value
}

func (s *fakeStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

// tickDriver fakes the loop's sleep: it records the requested duration, advances
// the fake clock and cancels the run once the scripted tick budget is spent.
type tickDriver struct {
	mu        sync.Mutex
	clock     *fakeClock
	durations []time.Duration
	limit     int
	calls     int
	cancel    context.CancelFunc
}

func (d *tickDriver) Sleep(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	d.calls++
	if d.limit > 0 && d.calls > d.limit {
		d.mu.Unlock()
		d.cancel()
		return context.Canceled
	}
	d.durations = append(d.durations, duration)
	d.mu.Unlock()
	d.clock.Advance(duration)
	return nil
}

func (d *tickDriver) sleeps() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Duration(nil), d.durations...)
}

// refreshBackend scripts the cloud refresh endpoint. Reply N answers request N;
// the last reply repeats.
type refreshBackend struct {
	mu        sync.Mutex
	store     *fakeStore
	tokens    []string
	observed  []string
	replies   []refreshReply
	done      chan struct{}
	doneOnce  sync.Once
	requested int
}

type refreshReply struct {
	status  int
	token   string
	missing bool
}

func newRefreshBackend(t *testing.T, store *fakeStore, replies ...refreshReply) (*refreshBackend, *httptest.Server) {
	t.Helper()
	backend := &refreshBackend{store: store, replies: replies, done: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			StreamToken string `json:"streamToken"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode refresh body: %v", err)
		}
		backend.mu.Lock()
		backend.tokens = append(backend.tokens, body.StreamToken)
		if store != nil {
			backend.observed = append(backend.observed, store.token())
		}
		index := len(backend.tokens) - 1
		reply := backend.replies[len(backend.replies)-1]
		if index < len(backend.replies) {
			reply = backend.replies[index]
		}
		backend.requested++
		backend.mu.Unlock()

		if reply.missing {
			_, _ = writer.Write([]byte(`{"success":true}`))
			backend.doneOnce.Do(func() { close(backend.done) })
			return
		}
		if reply.status != 0 && reply.status != http.StatusOK {
			writer.WriteHeader(reply.status)
			_, _ = writer.Write([]byte(`{"success":false,"msg":"rejected"}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"success": true, "streamToken": reply.token})
		backend.doneOnce.Do(func() { close(backend.done) })
	}))
	t.Cleanup(server.Close)
	return backend, server
}

func (b *refreshBackend) requests() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.tokens...)
}

func (b *refreshBackend) storeSeenAtRequests() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.observed...)
}

// runLoop drives Run through the scripted tick budget and waits for it to exit.
func runLoop(t *testing.T, refresher *Refresher, driver *tickDriver, ticks int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	driver.mu.Lock()
	driver.limit = ticks
	driver.cancel = cancel
	driver.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		refresher.Run(ctx)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("refresh loop did not stop within 5s")
	}
	cancel()
}

// makeJWT lives in token_test.go; tokens in these tests are only ever decoded
// for their exp.
func tokenExpiringAt(offset time.Duration) string {
	return makeJWT(map[string]any{"sub": "u@e.com", "exp": testEpoch.Add(offset).Unix()})
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func TestRefresherRotatesTokenBeforeExpiry(t *testing.T) {
	clock := newFakeClock(testEpoch)
	oldToken := tokenExpiringAt(time.Hour)
	newToken := tokenExpiringAt(24 * time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: newToken})
	driver := &tickDriver{clock: clock}

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: driver.Sleep})
	runLoop(t, refresher, driver, 1)

	requests := backend.requests()
	if len(requests) != 1 || requests[0] != oldToken {
		t.Fatalf("refresh requests = %v, want one POST of the current token", requests)
	}
	if got := store.token(); got != newToken {
		t.Fatalf("stored token = %q, want the rotated token", got)
	}
	if store.saveCount() != 1 {
		t.Fatalf("saves = %d, want 1", store.saveCount())
	}
	sleeps := driver.sleeps()
	if len(sleeps) != 1 || sleeps[0] != time.Minute {
		t.Fatalf("sleeps = %v, want one 60s wait (token inside the 2h threshold)", sleeps)
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func TestRefresherRefreshesExpiredToken(t *testing.T) {
	clock := newFakeClock(testEpoch)
	oldToken := tokenExpiringAt(-time.Hour)
	newToken := tokenExpiringAt(24 * time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: newToken})
	driver := &tickDriver{clock: clock}

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: driver.Sleep})
	runLoop(t, refresher, driver, 1)

	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("refresh requests = %v, want one POST", requests)
	}
	if got := store.token(); got != newToken {
		t.Fatalf("stored token = %q, want the rotated token", got)
	}
	if sleeps := driver.sleeps(); len(sleeps) != 1 || sleeps[0] != time.Minute {
		t.Fatalf("sleeps = %v, want the 60s floor for an expired token", sleeps)
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func TestRefresherRetriesTransientFailureOnNextTick(t *testing.T) {
	clock := newFakeClock(testEpoch)
	oldToken := tokenExpiringAt(time.Hour)
	newToken := tokenExpiringAt(24 * time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store,
		refreshReply{status: http.StatusInternalServerError},
		refreshReply{status: http.StatusOK, token: newToken},
	)
	driver := &tickDriver{clock: clock}

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: driver.Sleep})
	runLoop(t, refresher, driver, 2)

	requests := backend.requests()
	if len(requests) != 2 {
		t.Fatalf("refresh attempts = %d, want 2 (transient failure retried on the next tick)", len(requests))
	}
	// The failed first attempt must not clobber the credential the retry sends.
	if observed := backend.storeSeenAtRequests(); len(observed) != 2 || observed[1] != oldToken {
		t.Fatalf("stored token at the retry request = %v, want the untouched old token", observed)
	}
	if got := store.token(); got != newToken {
		t.Fatalf("stored token = %q, want the rotated token after the retry", got)
	}
	if store.saveCount() != 1 {
		t.Fatalf("saves = %d, want exactly one successful write", store.saveCount())
	}
	if sleeps := driver.sleeps(); len(sleeps) != 2 || sleeps[1] != time.Minute {
		t.Fatalf("sleeps = %v, want the 60s floor between attempts", sleeps)
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func TestRefresherKeepsCredentialOnPermanentFailure(t *testing.T) {
	clock := newFakeClock(testEpoch)
	oldToken := tokenExpiringAt(time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusUnauthorized})
	driver := &tickDriver{clock: clock}

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: driver.Sleep})
	runLoop(t, refresher, driver, 2)

	if attempts := len(backend.requests()); attempts != 2 {
		t.Fatalf("refresh attempts = %d, want 2 (permanent failures keep the loop alive)", attempts)
	}
	if got := store.token(); got != oldToken {
		t.Fatalf("stored token = %q, want the untouched credential", got)
	}
	if store.saveCount() != 0 {
		t.Fatalf("saves = %d, want 0", store.saveCount())
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func TestRefresherSkipsTokenThatIsStillFresh(t *testing.T) {
	clock := newFakeClock(testEpoch)
	oldToken := tokenExpiringAt(24 * time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: tokenExpiringAt(48 * time.Hour)})
	driver := &tickDriver{clock: clock}

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: driver.Sleep})
	runLoop(t, refresher, driver, 2)

	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("refresh requests = %v, want none while the credential has more than the threshold left", requests)
	}
	if got := store.token(); got != oldToken {
		t.Fatalf("stored token = %q, want the untouched credential", got)
	}
	if sleeps := driver.sleeps(); len(sleeps) != 2 || sleeps[0] != time.Hour {
		t.Fatalf("sleeps = %v, want the 1h cap", sleeps)
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func TestRefresherDoesNotOverwriteOnMissingToken(t *testing.T) {
	clock := newFakeClock(testEpoch)
	oldToken := tokenExpiringAt(time.Hour)
	store := newFakeStore(oldToken)
	_, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, missing: true})
	driver := &tickDriver{clock: clock}

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: driver.Sleep})
	runLoop(t, refresher, driver, 1)

	if got := store.token(); got != oldToken {
		t.Fatalf("stored token = %q, want the credential untouched by an empty response", got)
	}
	if store.saveCount() != 0 {
		t.Fatalf("saves = %d, want 0", store.saveCount())
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
func TestRefresherWaitsWhenCredentialAbsent(t *testing.T) {
	clock := newFakeClock(testEpoch)
	store := newFakeStore("")
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: tokenExpiringAt(24 * time.Hour)})
	driver := &tickDriver{clock: clock}

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: driver.Sleep})
	runLoop(t, refresher, driver, 2)

	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("refresh requests = %v, want none without a stored credential", requests)
	}
	if sleeps := driver.sleeps(); len(sleeps) != 2 || sleeps[0] != time.Minute {
		t.Fatalf("sleeps = %v, want the 60s poll floor", sleeps)
	}
}

// Parity-exempt: Python cancellation is asyncio.CancelledError; the Go loop must
// observe context cancellation both before and inside its sleep.
func TestRefresherStopsOnCancellation(t *testing.T) {
	store := newFakeStore(tokenExpiringAt(time.Hour))
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: tokenExpiringAt(24 * time.Hour)})

	t.Run("cancelled before start", func(t *testing.T) {
		refresher := New(Options{BaseURL: server.URL, Store: store, Now: newFakeClock(testEpoch).Now})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			refresher.Run(ctx)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return on a cancelled context")
		}
	})

	t.Run("cancelled during sleep", func(t *testing.T) {
		clock := newFakeClock(testEpoch)
		ctx, cancel := context.WithCancel(context.Background())
		refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now, Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}})
		done := make(chan struct{})
		go func() {
			defer close(done)
			refresher.Run(ctx)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancellation inside sleep")
		}
	})

	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("refresh requests = %v, want none after cancellation", requests)
	}
	if store.saveCount() != 0 {
		t.Fatalf("saves = %d, want 0", store.saveCount())
	}
}

// Parity-exempt: Python's test module drives the loop through a fake clock; the
// forced-refresh wake path needs the production interruptible sleep to be
// observable, which the Go port covers with a real timer plus a channel signal.
func TestRefresherForceRefreshWakesSleep(t *testing.T) {
	clock := newFakeClock(testEpoch)
	oldToken := tokenExpiringAt(10 * time.Hour)
	newToken := tokenExpiringAt(24 * time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: newToken})

	refresher := New(Options{BaseURL: server.URL, Store: store, Now: clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		refresher.Run(ctx)
	}()

	// The loop would sleep a capped hour; the force signal must cut it short.
	refresher.ForceRefresh()
	select {
	case <-backend.done:
	case <-time.After(5 * time.Second):
		t.Fatal("forced refresh did not reach the backend")
	}
	select {
	case <-store.saved:
	case <-time.After(5 * time.Second):
		t.Fatal("forced refresh response was not persisted")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if got := store.token(); got != newToken {
		t.Fatalf("stored token = %q, want the forced rotation", got)
	}
	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("refresh requests = %v, want exactly one forced POST", requests)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestTryRefreshNow::test_no_op_when_module_refs_unset
func TestRefreshNowNoOpsWhenNotConfigured(t *testing.T) {
	refresher := New(Options{})
	if token, ok := refresher.RefreshNow(context.Background()); ok || token != "" {
		t.Fatalf("RefreshNow = (%q, %v), want (\"\", false)", token, ok)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestTryRefreshNow::test_no_op_when_credential_missing
func TestRefreshNowNoOpsWhenCredentialMissing(t *testing.T) {
	store := newFakeStore("")
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: tokenExpiringAt(24 * time.Hour)})
	refresher := New(Options{BaseURL: server.URL, Store: store, Now: newFakeClock(testEpoch).Now})

	if token, ok := refresher.RefreshNow(context.Background()); ok || token != "" {
		t.Fatalf("RefreshNow = (%q, %v), want (\"\", false)", token, ok)
	}
	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("refresh requests = %v, want none", requests)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestTryRefreshNow::test_happy_path_writes_new_token_to_store
func TestRefreshNowWritesTokenBack(t *testing.T) {
	oldToken := tokenExpiringAt(-time.Minute)
	newToken := tokenExpiringAt(24 * time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: newToken})
	refresher := New(Options{BaseURL: server.URL, Store: store, Now: newFakeClock(testEpoch).Now})

	token, ok := refresher.RefreshNow(context.Background())
	if !ok || token != newToken {
		t.Fatalf("RefreshNow = (%q, %v), want (%q, true)", token, ok, newToken)
	}
	if requests := backend.requests(); len(requests) != 1 || requests[0] != oldToken {
		t.Fatalf("refresh requests = %v, want one POST of the current token", requests)
	}
	if got := store.token(); got != newToken {
		t.Fatalf("stored token = %q, want %q", got, newToken)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestTryRefreshNow::test_backend_failure_does_not_overwrite_existing_token
func TestRefreshNowKeepsTokenOnFailure(t *testing.T) {
	oldToken := tokenExpiringAt(-time.Minute)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusUnauthorized})
	refresher := New(Options{BaseURL: server.URL, Store: store, Now: newFakeClock(testEpoch).Now})

	if token, ok := refresher.RefreshNow(context.Background()); ok || token != "" {
		t.Fatalf("RefreshNow = (%q, %v), want (\"\", false)", token, ok)
	}
	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("refresh attempts = %d, want 1", len(requests))
	}
	if store.saveCount() != 0 {
		t.Fatalf("saves = %d, want 0", store.saveCount())
	}
	if got := store.token(); got != oldToken {
		t.Fatalf("stored token = %q, want the original credential", got)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestTryRefreshNow::test_concurrent_callers_serialize_to_a_single_refresh
func TestRefreshNowSerialisesConcurrentCallers(t *testing.T) {
	oldToken := tokenExpiringAt(-time.Minute)
	newToken := tokenExpiringAt(24 * time.Hour)
	store := newFakeStore(oldToken)
	backend, server := newRefreshBackend(t, store, refreshReply{status: http.StatusOK, token: newToken})
	refresher := New(Options{BaseURL: server.URL, Store: store, Now: newFakeClock(testEpoch).Now})

	const callers = 3
	results := make([]string, callers)
	var waitGroup sync.WaitGroup
	for index := 0; index < callers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			token, ok := refresher.RefreshNow(context.Background())
			if !ok {
				t.Errorf("caller %d: RefreshNow failed", index)
			}
			results[index] = token
		}(index)
	}
	waitGroup.Wait()

	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("refresh requests = %d, want exactly 1 (the lock collapses racing callers)", len(requests))
	}
	for index, token := range results {
		if token != newToken {
			t.Fatalf("caller %d returned %q, want %q", index, token, newToken)
		}
	}
}

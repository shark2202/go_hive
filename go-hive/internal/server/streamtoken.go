package server

// StreamToken rotation host. The reference runtime rotates its own `hive`
// streamToken in-process so a cloud VM keeps calling the LLM proxy after the
// desktop that minted the token goes away. The scheduling/retry engine lives in
// internal/streamtoken; this file supplies the serve-process wiring (credential
// store from the home, HIVE_CLOUD_BASE gate, the llm call-site seam and the
// stop path).
//
// Parity: core/framework/server/streamtoken_refresh.py::start_streamtoken_refresh

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/streamtoken"
)

// StreamTokenRefreshDeps is the serve-side configuration for the rotation loop.
// Zero values reproduce production behaviour; tests override Store/Client/Sleep.
type StreamTokenRefreshDeps struct {
	// Home locates credentials/ when Store is nil.
	Home hivehome.Home
	// Store overrides the credential store built from Home.
	Store streamtoken.CredentialStore
	// BaseURL overrides HIVE_CLOUD_BASE.
	BaseURL string
	// Log receives progress lines. Nil uses log.Printf.
	Log func(format string, args ...any)
	// Client overrides the refresh endpoint client.
	Client *streamtoken.Client
	// Policy overrides the default refresh schedule bounds.
	Policy *streamtoken.Policy
	// Sleep overrides the loop's wait; tests use it to drive ticks.
	Sleep func(ctx context.Context, duration time.Duration) error
}

// StartStreamTokenRefresh starts the in-process rotation loop when the runtime
// is in cloud mode. The returned stop function is always safe to call: it
// uninstalls the LLM-call seam, cancels the loop and waits for it to exit.
//
// No HIVE_CLOUD_BASE means no loop — the same early return as the reference
// module, so a desktop-hosted runtime keeps consuming externally pushed
// credentials.
func StartStreamTokenRefresh(deps StreamTokenRefreshDeps) func() {
	logf := deps.Log
	if logf == nil {
		logf = log.Printf
	}
	baseURL := strings.TrimSpace(deps.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("HIVE_CLOUD_BASE"))
	}
	if baseURL == "" {
		logf("streamtoken: disabled (no HIVE_CLOUD_BASE — runtime not in cloud mode)")
		return func() {}
	}

	store := deps.Store
	if store == nil {
		if deps.Home.Root == "" {
			logf("streamtoken: disabled (no credential store home)")
			return func() {}
		}
		// Pick up a persisted key file before the encrypted storage resolves
		// its key: a fresh process must read the same store the credential
		// routes write.
		_, _ = credentials.LoadCredentialKey()
		built, err := credentials.WithAdenSync(
			deps.Home.CredentialsDir(), nil, 0, false,
			// The loop re-reads every tick so a token the desktop pushes
			// through POST /api/credentials is visible immediately instead of
			// after the store's 5-minute cache window.
			credentials.WithCacheTTL(0),
		)
		if err != nil {
			logf("streamtoken: disabled (credential store unavailable: %v)", err)
			return func() {}
		}
		store = built
	}

	refresher := streamtoken.New(streamtoken.Options{
		BaseURL: baseURL,
		Store:   store,
		Client:  deps.Client,
		Policy:  deps.Policy,
		Sleep:   deps.Sleep,
		Log:     logf,
	})
	llm.SetStreamTokenRefresher(refresher.RefreshNow)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		refresher.Run(ctx)
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			llm.SetStreamTokenRefresher(nil)
			cancel()
			<-done
		})
	}
}

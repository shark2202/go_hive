// Shared batch-3 primitives for the media tools: the injectable dependency
// bundle and the HTTP/credential helpers image_generate and web_search both
// use. The image tool itself lives in the image subpackage; web_search stays
// here until the web-search lane splits, so its option plumbing remains in the
// root package.
package integrations

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// batch3MediaDeps carries the injectable side effects shared by the batch-3
// tools. Tests point the HTTP client at an httptest server and replace the
// clock and sleep so retries stay fast.
type batch3MediaDeps struct {
	http        *http.Client
	getenv      func(string) string
	userHomeDir func() (string, error)
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	resolver    batch3Resolver
	baseURL     string
}

type batch3MediaOption func(*batch3MediaDeps)

// withBatch3HTTPClient swaps the transport used by both tools.
func withBatch3HTTPClient(client *http.Client) batch3MediaOption {
	return func(deps *batch3MediaDeps) { deps.http = client }
}

// withBatch3Getenv overrides environment lookup (credential tests).
func withBatch3Getenv(fn func(string) string) batch3MediaOption {
	return func(deps *batch3MediaDeps) { deps.getenv = fn }
}

// withBatch3Clock overrides the clock used for artifact filenames.
func withBatch3Clock(now func() time.Time) batch3MediaOption {
	return func(deps *batch3MediaDeps) { deps.now = now }
}

// withBatch3Sleep swaps the retry backoff used by web_search.
func withBatch3Sleep(sleep func(context.Context, time.Duration) error) batch3MediaOption {
	return func(deps *batch3MediaDeps) { deps.sleep = sleep }
}

// withBatch3MediaResolver swaps the DNS resolver behind the reference-image
// SSRF gate (tests that serve reference images from loopback).
func withBatch3MediaResolver(resolver batch3Resolver) batch3MediaOption {
	return func(deps *batch3MediaDeps) { deps.resolver = resolver }
}

// withBatch3UserHomeDir overrides the home directory used for "~" expansion.
func withBatch3UserHomeDir(fn func() (string, error)) batch3MediaOption {
	return func(deps *batch3MediaDeps) { deps.userHomeDir = fn }
}

// withBatch3BaseURL points every batch-3 module at one fake upstream.
func withBatch3BaseURL(base string) batch3MediaOption {
	return func(deps *batch3MediaDeps) { deps.baseURL = strings.TrimRight(base, "/") }
}

func defaultBatch3MediaDeps() batch3MediaDeps {
	return batch3MediaDeps{
		getenv: os.Getenv,
		now:    time.Now,
		sleep:  batch3Sleep,
	}
}

// batch3Sleep waits for d unless the context ends first.
func batch3Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// batch3Modules declares the default endpoints. The image module's BaseURL is
// overridden by HIVE_LLM_BASE_URL at call time; tests use withBatch3BaseURL.
func batch3Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"image_gen_tool": {ID: "image_gen_tool", BaseURL: "https://llm.open-hive.com"},
		"brave_search":   {ID: "brave_search", BaseURL: "https://api.search.brave.com"},
		"google_cse":     {ID: "google_cse", BaseURL: "https://www.googleapis.com"},
	}
}

// batch3ModuleBaseURL resolves a module endpoint, falling back to the
// compiled-in default when no configuration is present.
func batch3ModuleBaseURL(rt *Runtime, moduleID, fallback string) string {
	base := ""
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			base = cfg.BaseURL
		}
	}
	if strings.TrimSpace(base) == "" {
		base = fallback
	}
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

// batch3Env reads one environment value through the runtime seam.
func batch3Env(rt *Runtime, name string) string {
	if rt == nil || rt.Getenv == nil {
		return os.Getenv(name)
	}
	return rt.Getenv(name)
}

// batch3HTTPClient returns a client bounded by timeout, keeping a stricter
// timeout already configured on the injected client.
func batch3HTTPClient(rt *Runtime, timeout time.Duration) *http.Client {
	base := http.DefaultClient
	if rt != nil && rt.HTTP != nil && rt.HTTP.Inner != nil {
		base = rt.HTTP.Inner
	}
	if base.Timeout > 0 && base.Timeout <= timeout {
		return base
	}
	clone := *base
	clone.Timeout = timeout
	return &clone
}

// batch3IsTimeout reports whether err is a deadline/timeout failure.
func batch3IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return os.IsTimeout(err)
}

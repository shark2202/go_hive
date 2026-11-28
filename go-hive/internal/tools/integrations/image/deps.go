package image

// Dependency seams: the HTTP client, environment lookup, clock, DNS resolver
// and home-directory resolver the image tool reads through. Tests inject every
// seam so no assertion depends on $HOME, on a developer's HIVE_* exports or on
// real DNS.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Resolver resolves a hostname to its addresses for the reference-image SSRF
// gate. Runtime.ResolveHost has the same shape, so a fake resolver keeps the
// SSRF tests off the network.
type Resolver func(context.Context, string) ([]string, error)

// Deps carries the injectable side effects of the image tool. Register fills
// nil fields with the process defaults, so the zero value is usable.
type Deps struct {
	// Getenv resolves HIVE_* configuration and the artifact directory.
	Getenv func(string) string
	// Now supplies artifact filename timestamps.
	Now func() time.Time
	// Resolver backs the SSRF gate for reference-image URLs.
	Resolver Resolver
	// UserHomeDir expands a leading "~" in artifact paths.
	UserHomeDir func() (string, error)
}

type config struct {
	http        *http.Client
	getenv      func(string) string
	userHomeDir func() (string, error)
	now         func() time.Time
	resolver    Resolver
	baseURL     string
}

// Option mutates the standalone registry configuration.
type Option func(*config)

// WithHTTPClient swaps the transport used by the image proxy and reference
// downloads.
func WithHTTPClient(client *http.Client) Option {
	return func(cfg *config) { cfg.http = client }
}

// WithGetenv overrides environment lookup (credential and path tests).
func WithGetenv(fn func(string) string) Option {
	return func(cfg *config) { cfg.getenv = fn }
}

// WithClock overrides the clock used for artifact filenames.
func WithClock(now func() time.Time) Option {
	return func(cfg *config) { cfg.now = now }
}

// WithResolver swaps the DNS resolver behind the reference-image SSRF gate
// (tests that serve reference images from loopback).
func WithResolver(resolver Resolver) Option {
	return func(cfg *config) { cfg.resolver = resolver }
}

// WithBaseURL points the image module at a fake upstream.
func WithBaseURL(base string) Option {
	return func(cfg *config) { cfg.baseURL = strings.TrimRight(base, "/") }
}

func defaultConfig() config {
	return config{
		getenv: os.Getenv,
		now:    time.Now,
	}
}

// envValue reads one environment value through the runtime seam.
func envValue(rt *Runtime, name string) string {
	if rt == nil || rt.Getenv == nil {
		return os.Getenv(name)
	}
	return rt.Getenv(name)
}

// httpClient returns a client bounded by timeout, keeping a stricter timeout
// already configured on the injected client.
func httpClient(rt *Runtime, timeout time.Duration) *http.Client {
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

// isTimeout reports whether err is a deadline/timeout failure.
func isTimeout(err error) bool {
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

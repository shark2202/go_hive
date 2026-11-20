// Package integrationkit holds the transport, credential and parameter
// primitives shared by the integration modules in internal/tools/integrations.
//
// The package deliberately depends only on the standard library and
// internal/tools: it knows nothing about the generated catalog or about any
// individual provider. Integrations keep thin aliases and facades so provider
// files continue to reference Runtime, Tool, Action, RestSpec and ModuleConfig
// by their historical names.
package integrationkit

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ActionFunc executes one action. Returning an error is reserved for
// infrastructure failures; user-visible failures belong in the returned map.
type ActionFunc func(context.Context, *Runtime, map[string]any) (any, error)

// HTTPClient wraps net/http with a bounded body read and a default timeout.
// Inner is exported so scanner tools can clone the underlying transport for
// per-request TLS or redirect policy while keeping the shared timeout.
type HTTPClient struct {
	Inner *http.Client
}

// NewHTTPClient returns a client with a 30s default timeout when none is given.
func NewHTTPClient(client *http.Client) *HTTPClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPClient{Inner: client}
}

// Do forwards the request to the wrapped transport.
func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) { return c.Inner.Do(req) }

// Runtime carries the injectable dependencies shared by all actions.
type Runtime struct {
	// HTTP is used by REST actions and network scanners. Never nil after New.
	HTTP *HTTPClient
	// ResolveHost resolves a URL host for SSRF checks. Nil uses the system
	// resolver; tests inject a stub so no network is touched.
	ResolveHost func(context.Context, string) ([]string, error)
	// Now supplies timestamps; defaults to time.Now.
	Now func() time.Time
	// Roots are the only directories file-backed tools may touch.
	Roots []string
	// Accounts is the credential-store view used by get_account_info.
	Accounts AccountSource
	// Modules holds per-module credential and endpoint configuration.
	Modules map[string]*ModuleConfig
	// Getenv resolves credentials; defaults to os.Getenv.
	Getenv func(string) string

	mu      sync.RWMutex
	clients map[string]*restClient
}

// AccountSource lists connected accounts for get_account_info.
type AccountSource interface {
	Accounts(provider string) []map[string]any
}

// Option mutates the Runtime during New.
type Option func(*Runtime)

// WithHTTPClient swaps the underlying transport (tests, proxies).
func WithHTTPClient(client *http.Client) Option {
	return func(rt *Runtime) { rt.HTTP = NewHTTPClient(client) }
}

// WithBaseURL points one module at a different API base (tests).
func WithBaseURL(moduleID, baseURL string) Option {
	return func(rt *Runtime) {
		cfg := rt.Modules[moduleID]
		if cfg == nil {
			cfg = &ModuleConfig{ID: moduleID}
			rt.Modules[moduleID] = cfg
		}
		cfg.BaseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithCredential supplies a credential for one module without touching env.
func WithCredential(moduleID, value string) Option {
	return func(rt *Runtime) {
		cfg := rt.Modules[moduleID]
		if cfg == nil {
			cfg = &ModuleConfig{ID: moduleID}
			rt.Modules[moduleID] = cfg
		}
		cfg.token = value
	}
}

// WithRoots overrides the filesystem sandbox roots.
func WithRoots(roots ...string) Option {
	return func(rt *Runtime) { rt.Roots = roots }
}

// WithClock overrides the clock (deterministic timestamps).
func WithClock(now func() time.Time) Option {
	return func(rt *Runtime) { rt.Now = now }
}

// WithAccountSource installs the credential-store view.
func WithAccountSource(source AccountSource) Option {
	return func(rt *Runtime) { rt.Accounts = source }
}

// WithGetenv overrides environment lookup (tests).
func WithGetenv(fn func(string) string) Option {
	return func(rt *Runtime) { rt.Getenv = fn }
}

// ModuleConfig describes how one module authenticates and where it lives.
type ModuleConfig struct {
	ID      string
	BaseURL string
	EnvVars []string
	// ResolveToken overrides the default "first non-empty env var" rule.
	ResolveToken func(getenv func(string) string) (string, []string)
	Headers      func(token string) map[string]string
	Query        func(token string) map[string]string
	Form         bool
	token        string
}

// ConfiguredToken reports the credential injected with WithCredential. It is
// empty when the module resolves its credential from the environment.
func (c *ModuleConfig) ConfiguredToken() string {
	if c == nil {
		return ""
	}
	return c.token
}

// ResolvedToken returns the credential for this module: the value injected
// with WithCredential, the custom resolver's result, or the first non-empty
// environment variable. Missing names are returned when none resolve.
func (c *ModuleConfig) ResolvedToken(getenv func(string) string) (string, []string) {
	if c.token != "" {
		return c.token, nil
	}
	if c.ResolveToken != nil {
		return c.ResolveToken(getenv)
	}
	for _, name := range c.EnvVars {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value, nil
		}
	}
	return "", append([]string(nil), c.EnvVars...)
}

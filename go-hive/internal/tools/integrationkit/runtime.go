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
	// Accounts is the credential-store view used by get_account_info and by
	// account-aware actions. The session attaches its view per call (see
	// tools.ToolRegistry.SetAccountSource); WithAccountSource pins one for
	// tests and standalone registries.
	Accounts AccountSource
	// Modules holds per-module credential and endpoint configuration.
	Modules map[string]*ModuleConfig
	// Getenv resolves credentials; defaults to os.Getenv.
	Getenv func(string) string

	// callAccount is the account alias the action selected on this call. It is
	// only set on the per-call copy returned by ForCall, never on the shared
	// runtime, so concurrent calls cannot observe each other's selection.
	callAccount string

	mu      sync.RWMutex
	clients map[string]*restClient
}

// AccountSource is the read-only credential view a runtime resolves accounts
// through: the account list for get_account_info, and one secret per
// (provider, alias) for account-aware actions. Upstream parity:
// aden_tools CredentialStoreAdapter (get_all_account_info / get_by_alias).
// It is declared here rather than imported from internal/tools so this package
// keeps its standard-library-only boundary; the two interfaces are structural
// and assign across each other.
type AccountSource interface {
	Accounts(provider string) []map[string]any
	AccountToken(provider, alias string) (string, bool)
}

// ForCall returns the per-call view of the runtime: the session's credential
// view when one is attached, and the account alias this action selected. The
// copy shares the transport, module configs and clock, and starts with an
// empty REST client cache, so concurrent calls never race on shared state.
func (rt *Runtime) ForCall(source AccountSource, alias string) *Runtime {
	if rt == nil {
		return nil
	}
	call := &Runtime{
		HTTP:        rt.HTTP,
		ResolveHost: rt.ResolveHost,
		Now:         rt.Now,
		Roots:       rt.Roots,
		Accounts:    rt.Accounts,
		Modules:     rt.Modules,
		Getenv:      rt.Getenv,
	}
	if source != nil {
		call.Accounts = source
	}
	call.callAccount = strings.TrimSpace(alias)
	return call
}

// ResolveAccountToken resolves the secret of the account this call selected.
// handled reports whether an account was selected at all: false means the
// caller falls back to the module's own credential; true with an empty token
// means the alias did not resolve, and the caller reports its auth error like
// upstream does when get_by_alias returns None.
func (rt *Runtime) ResolveAccountToken(provider string) (string, bool) {
	if rt == nil || rt.Accounts == nil {
		return "", false
	}
	alias := strings.TrimSpace(rt.callAccount)
	provider = strings.TrimSpace(provider)
	if alias == "" || provider == "" {
		return "", false
	}
	token, ok := rt.Accounts.AccountToken(provider, alias)
	if !ok {
		return "", true
	}
	return strings.TrimSpace(token), true
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

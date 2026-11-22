package enrichment

// Registry construction and assembly hooks. The root package calls NewRegistry
// (or Register into an existing registry); tests call NewRegistry with options
// to point the modules at a fake server.

import (
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

const (
	maxBodyBytes  = 8 << 20
	maxErrorBytes = 500
	maxValueBytes = 5000
	maxQueryChars = 500
)

// RetrySleep backs the Exa 429 backoff; tests swap it for a no-op.
var RetrySleep = func(delay time.Duration) { time.Sleep(delay) }

// NewRegistry builds a registry containing only the enrichment tools (apify,
// apollo, lusha, serpapi, exa_search), sharing the Runtime surface used by the
// rest of the port.
func NewRegistry(opts ...Option) *Registry {
	rt := &Runtime{
		HTTP:    integrationkit.NewHTTPClient(nil),
		Now:     time.Now,
		Modules: modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(rt)
	}
	return Register(integrationkit.NewRegistry(rt))
}

// Register adds the five catalogs and their action implementations to reg. It
// is the assembly hook the root uses when it owns the kernel registry.
func Register(reg *Registry) *Registry {
	for _, item := range []*Tool{
		apifyTool,
		apolloTool,
		lushaTool,
		serpapiTool,
		exaTool,
	} {
		reg.AddTool(item)
	}
	for name, impls := range map[string]map[string]ActionFunc{
		"apify":      apifyImpls(),
		"apollo":     apolloImpls(),
		"lusha":      lushaImpls(),
		"serpapi":    serpapiImpls(),
		"exa_search": exaImpls(),
	} {
		for action, impl := range impls {
			reg.AddImpl(name, action, impl)
		}
	}
	return reg
}

// modules declares the public API roots and credential variables.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"apify_tool": {
			ID: "apify_tool", BaseURL: "https://api.apify.com/v2",
			EnvVars: []string{"APIFY_API_TOKEN"},
		},
		"apollo_tool": {
			ID: "apollo_tool", BaseURL: "https://api.apollo.io/api/v1",
			EnvVars: []string{"APOLLO_API_KEY"},
		},
		"lusha_tool": {
			ID: "lusha_tool", BaseURL: "https://api.lusha.com",
			EnvVars: []string{"LUSHA_API_KEY"},
		},
		"serpapi_tool": {
			ID: "serpapi_tool", BaseURL: "https://serpapi.com",
			EnvVars: []string{"SERPAPI_API_KEY"},
		},
		"exa_search_tool": {
			ID: "exa_search_tool", BaseURL: "https://api.exa.ai",
			EnvVars: []string{"EXA_API_KEY"},
		},
	}
}

package webintel

// Registry construction and assembly hooks. The root package calls NewRegistry
// (or Register into an existing registry); tests call NewRegistry with options
// to point the modules at a fake server.

import (
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

const (
	maxBodyBytes = 8 << 20
	// errorTextBytes caps provider error bodies like upstream
	// response.text[:200].
	errorTextBytes = 200
	// maxRetries is the upstream max_retries for 429 backoff.
	maxRetries = 3
)

// backoffSleep is the rate-limit backoff hook; tests replace it so the
// upstream 1s/2s/4s waits do not slow the suite down.
var backoffSleep = time.Sleep

// NewRegistry builds a registry containing only the web-intelligence tools
// (similarweb, news, google_maps), sharing the Runtime surface used by the
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

// Catalog returns the three module tools in registration order. It is the
// exported view the root assembly uses to derive compatibility aliases from
// the aggregate catalogs.
func Catalog() []*Tool {
	return []*Tool{similarWebTool, newsTool, googleMapsTool}
}

// Register adds the three catalogs and their action implementations to reg.
// It is the assembly hook the root uses when it owns the kernel registry.
func Register(reg *Registry) *Registry {
	for _, tool := range Catalog() {
		reg.AddTool(tool)
	}
	for action, impl := range similarWebImpls() {
		reg.AddImpl("similarweb", action, impl)
	}
	for action, impl := range newsImpls() {
		reg.AddImpl("news", action, impl)
	}
	for action, impl := range googleMapsImpls() {
		reg.AddImpl("google_maps", action, impl)
	}
	return reg
}

// modules declares the credential variables and public endpoints
// for the modules. The Finlight entry is a credential/base-URL carrier for the
// news fallback provider, not a tool of its own. Tests override BaseURL with
// WithBaseURL.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"similarweb_tool": {
			ID:      "similarweb_tool",
			BaseURL: "https://api.similarweb.com/v5",
			EnvVars: []string{"SIMILARWEB_API_KEY"},
		},
		"news_tool": {
			ID:      "news_tool",
			BaseURL: "https://newsdata.io/api/1",
			EnvVars: []string{"NEWSDATA_API_KEY"},
		},
		"finlight_tool": {
			ID:      "finlight_tool",
			BaseURL: "https://api.finlight.me/v2",
			EnvVars: []string{"FINLIGHT_API_KEY"},
		},
		"google_maps_tool": {
			ID:      "google_maps_tool",
			BaseURL: "https://maps.googleapis.com/maps/api",
			EnvVars: []string{"GOOGLE_MAPS_API_KEY"},
		},
	}
}

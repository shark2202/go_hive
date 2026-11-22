package cloudflare

// Registry construction: NewRegistry is what the root package's thin facade
// (integrations/cloudflare_install.go) forwards to; Register is the assembly
// hook for a registry the caller already owns.

import (
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// NewRegistry builds a registry containing only the Cloudflare tool, sharing
// the Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    integrationkit.NewHTTPClient(nil),
		Now:     time.Now,
		Modules: modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	return Register(integrationkit.NewRegistry(runtime))
}

// Register adds the Cloudflare catalog and its action implementations to a
// registry the caller owns.
func Register(registry *Registry) *Registry {
	tool := newTool()
	registry.AddTool(tool)
	for _, action := range actionCatalog() {
		registry.AddImpl(tool.Name, action.Name, actionImpl(action))
	}
	return registry
}

const (
	maxBodyBytes   = 8 << 20
	maxErrorBytes  = 500
	maxPageSize    = 100
	moduleID       = "cloudflare_tool"
	defaultBaseURL = "https://api.cloudflare.com/client/v4"
)

// ModuleID is the module key Cloudflare credentials and Runtime options are
// registered under (the upstream cloudflare_tool module).
const ModuleID = moduleID

// modules declares the public endpoint and credential variable.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		moduleID: {
			ID:      moduleID,
			BaseURL: defaultBaseURL,
			EnvVars: []string{"CLOUDFLARE_API_TOKEN"},
		},
	}
}

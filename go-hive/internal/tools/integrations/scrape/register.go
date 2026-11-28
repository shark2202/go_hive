package scrape

// Registry construction and assembly hooks. NewRegistry builds a standalone
// web_scrape registry; Register adds the catalog and implementation to a
// registry the root assembly already owns (it already carries pdf_read). The
// root package's InstallBatch3Doc facade forwards here.

import (
	"context"
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// NewRegistry builds a registry containing only the web_scrape tool, sharing
// the Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	deps := DefaultDeps()
	for _, opt := range opts {
		opt(&deps)
	}
	runtime := &Runtime{
		HTTP:        integrationkit.NewHTTPClient(nil),
		Now:         time.Now,
		Modules:     map[string]*ModuleConfig{},
		Getenv:      os.Getenv,
		ResolveHost: deps.Resolver,
	}
	return Register(integrationkit.NewRegistry(runtime), deps)
}

// Register adds the web_scrape catalog and action implementation to reg.
func Register(reg *Registry, deps Deps) *Registry {
	deps = deps.withDefaults()
	reg.AddTool(scrapeTool)
	reg.AddImpl("web_scrape", "web_scrape", func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		return webScrapeAction(ctx, rt, input, deps)
	})
	return reg
}

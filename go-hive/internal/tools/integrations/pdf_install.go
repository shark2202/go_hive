package integrations

// The PDF document family lives in its domain subpackage
// (internal/tools/integrations/pdf) and web-scrape in
// internal/tools/integrations/scrape; this thin entry point keeps the
// historical assembly name used by the CLI and by the batch-3 test suite.

import (
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/pdf"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/scrape"
)

// Historical dependency seams: web_scrape moved to the scrape subpackage, but
// the batch-3 tests and the media helpers keep these names.
type (
	batch3Resolver = scrape.Resolver
	batch3Renderer = scrape.Renderer
	batch3Option   = scrape.Option
)

func withBatch3Resolver(resolver batch3Resolver) batch3Option {
	return scrape.WithResolver(resolver)
}

func withBatch3Renderer(renderer batch3Renderer) batch3Option {
	return scrape.WithRenderer(renderer)
}

func withBatch3ScrapeHomeDir(fn func() (string, error)) batch3Option {
	return scrape.WithHomeDir(fn)
}

// InstallBatch3Doc publishes the batch-3 document tools (pdf_read and
// web_scrape) into a shared registry and returns the number of tools
// registered.
func InstallBatch3Doc(target *tools.ToolRegistry) int {
	return newBatch3Registry().Install(target)
}

// newBatch3Registry builds a registry containing only the batch-3 tools: the
// pdf_read tool from the pdf subpackage followed by web_scrape from the
// scrape subpackage, so tool and action order are unchanged. The scrape-side
// dependencies stay injectable so tests never touch the network or launch a
// browser.
func newBatch3Registry(opts ...batch3Option) *Registry {
	deps := scrape.DefaultDeps()
	for _, opt := range opts {
		opt(&deps)
	}
	runtime := &Runtime{
		HTTP:        newHTTPClient(nil),
		Now:         time.Now,
		Modules:     map[string]*ModuleConfig{},
		Getenv:      os.Getenv,
		ResolveHost: deps.Resolver,
	}
	registry := pdf.Register(NewRegistry(runtime))
	return scrape.Register(registry, deps)
}

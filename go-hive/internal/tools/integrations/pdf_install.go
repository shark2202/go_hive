package integrations

// The PDF document family lives in its domain subpackage
// (internal/tools/integrations/pdf); this thin entry point keeps the
// historical assembly name used by the CLI and by the batch-3 test suite.

import (
	"context"
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/pdf"
)

// InstallBatch3Doc publishes the batch-3 document tools (pdf_read and
// web_scrape) into a shared registry and returns the number of tools
// registered.
func InstallBatch3Doc(target *tools.ToolRegistry) int {
	return newBatch3Registry().Install(target)
}

// newBatch3Registry builds a registry containing only the batch-3 tools: the
// pdf_read tool from the pdf subpackage and web_scrape, which stays in this
// package until the web-scrape lane splits. The scrape-side dependencies stay
// injectable so tests never touch the network.
func newBatch3Registry(opts ...batch3Option) *Registry {
	deps := defaultBatch3Deps()
	for _, opt := range opts {
		opt(&deps)
	}
	runtime := &Runtime{
		HTTP:        newHTTPClient(nil),
		Now:         time.Now,
		Modules:     map[string]*ModuleConfig{},
		Getenv:      os.Getenv,
		ResolveHost: deps.scrapeResolver,
	}
	registry := pdf.Register(NewRegistry(runtime))
	registry.AddTool(batch3ScrapeTool)
	registry.AddImpl("web_scrape", "web_scrape", func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		return webScrapeAction(ctx, rt, input, deps.scrapeResolver, deps.scrapeRenderer)
	})
	return registry
}

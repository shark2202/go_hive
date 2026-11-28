package integrations

// The image-generation family lives in its domain subpackage
// (internal/tools/integrations/image); this thin entry point keeps the
// historical batch-3 assembly name used by the CLI and by the cross-tool test
// suite. web_search still lives here, so the shared batch-3 deps/options stay
// in batch3_media.go.

import (
	"context"
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/image"
)

// InstallBatch3Media publishes the batch-3 media tools (image_generate and
// web_search) into a shared registry and returns the number of tools
// registered.
func InstallBatch3Media(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	registry := newBatch3MediaRegistry()
	for _, tool := range registry.Tools() {
		name := tool.Name
		target.Register(name, tools.Tool{
			Name:            name,
			Description:     tool.Description(),
			Parameters:      tool.Schema(),
			ConcurrencySafe: false,
		}, func(inputs map[string]any) (any, error) {
			return registry.Execute(context.Background(), name, inputs)
		})
	}
	return len(registry.Names())
}

// newBatch3MediaRegistry builds a registry containing only the batch-3
// tools, with a Runtime wired the same way as New() so transports, base URLs
// and credentials stay injectable.
func newBatch3MediaRegistry(opts ...batch3MediaOption) *Registry {
	deps := defaultBatch3MediaDeps()
	for _, opt := range opts {
		opt(&deps)
	}
	if deps.getenv == nil {
		deps.getenv = os.Getenv
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.sleep == nil {
		deps.sleep = batch3Sleep
	}
	modules := batch3Modules()
	if deps.baseURL != "" {
		for _, cfg := range modules {
			cfg.BaseURL = deps.baseURL
		}
	}
	runtime := &Runtime{
		HTTP:    newHTTPClient(deps.http),
		Now:     deps.now,
		Modules: modules,
		Getenv:  deps.getenv,
	}
	registry := NewRegistry(runtime)
	image.Register(registry, image.Deps{
		Getenv:      deps.getenv,
		Now:         deps.now,
		Resolver:    image.Resolver(deps.resolver),
		UserHomeDir: deps.userHomeDir,
	})
	registry.AddTool(batch3WebSearchTool())
	registry.AddImpl("web_search", "web_search", func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		return webSearch(ctx, rt, input, deps)
	})
	return registry
}

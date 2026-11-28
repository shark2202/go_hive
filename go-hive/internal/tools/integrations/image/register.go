package image

// Registry construction and assembly. NewRegistry builds a standalone
// image-only registry; Register adds the catalog and implementation to a
// registry the root assembly already owns. The root's InstallBatch3Media facade
// forwards here so the CLI keeps its historical entry point.

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// NewRegistry builds a registry containing only the image_generate tool,
// sharing the Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.getenv == nil {
		cfg.getenv = os.Getenv
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.userHomeDir == nil {
		cfg.userHomeDir = os.UserHomeDir
	}
	modules := imageModules()
	if cfg.baseURL != "" {
		for _, module := range modules {
			module.BaseURL = cfg.baseURL
		}
	}
	runtime := &Runtime{
		HTTP:        integrationkit.NewHTTPClient(cfg.http),
		Now:         cfg.now,
		Modules:     modules,
		Getenv:      cfg.getenv,
		ResolveHost: cfg.resolver,
	}
	registry := integrationkit.NewRegistry(runtime)
	return Register(registry, Deps{
		Getenv:      cfg.getenv,
		Now:         cfg.now,
		Resolver:    cfg.resolver,
		UserHomeDir: cfg.userHomeDir,
	})
}

// Register adds the image_generate catalog and implementation to a registry
// the caller owns. The caller's Runtime supplies the transport, environment and
// clock; Deps supplies the filesystem/DNS seams the image action reads
// directly.
func Register(registry *Registry, deps Deps) *Registry {
	if deps.Getenv == nil {
		deps.Getenv = os.Getenv
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.UserHomeDir == nil {
		deps.UserHomeDir = os.UserHomeDir
	}
	registry.AddTool(imageTool())
	registry.AddImpl("image_generate", "image_generate", func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		return imageGenerate(ctx, rt, input, deps)
	})
	return registry
}

// imageModules declares the default endpoint. The image module's BaseURL is
// overridden by HIVE_LLM_BASE_URL at call time; tests use WithBaseURL.
func imageModules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"image_gen_tool": {ID: "image_gen_tool", BaseURL: imageProxyBase},
	}
}

// moduleBaseURL resolves the image module endpoint, falling back to the
// compiled-in default when no configuration is present.
func moduleBaseURL(rt *Runtime, moduleID, fallback string) string {
	base := ""
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			base = cfg.BaseURL
		}
	}
	if strings.TrimSpace(base) == "" {
		base = fallback
	}
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

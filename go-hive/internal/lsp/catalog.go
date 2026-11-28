package lsp

import (
	_ "embed"
	"errors"
	"fmt"
)

// embeddedDefaults is the representative embedded server catalog. The frozen
// design calls for a 55-server data catalog; v1 ships seven tested
// representatives (gopls, pyright, rust-analyzer, typescript-language-server,
// clangd, yamlls, bashls). Status reports the actual embedded count so the
// catalog is never over-claimed, and custom servers remain available through
// config.
//
//go:embed defaults.json
var embeddedDefaults []byte

// CatalogTarget is the catalog size named by the design's source contract. It
// is reported next to EmbeddedCatalogSize so missing catalog entries are
// visible instead of implied.
const CatalogTarget = 55

// parseCatalog loads server definitions from catalog JSON without reading any
// on-disk config.
func parseCatalog(data []byte) (*Config, error) {
	cfg := &Config{
		Settings:    DefaultSettings(),
		IdleTimeout: DefaultIdleTimeout,
		Servers:     map[string]*ServerDef{},
		origins:     map[string]string{},
	}
	if err := mergeFile(cfg, data, "built-in", "built-in"); err != nil {
		return nil, fmt.Errorf("lsp: embedded catalog: %w", err)
	}
	cfg.Sources = []string{"built-in"}
	return cfg, nil
}

// EmbeddedCatalogSize is the number of server definitions compiled into the
// binary. It is data, not a promise that any server is installed.
func EmbeddedCatalogSize() int {
	cfg, err := parseCatalog(embeddedDefaults)
	if err != nil {
		return 0
	}
	return len(cfg.Servers)
}

// EmbeddedCatalog returns the compiled-in definitions. Callers must treat the
// result as read-only.
func EmbeddedCatalog() (map[string]*ServerDef, error) {
	cfg, err := parseCatalog(embeddedDefaults)
	if err != nil {
		return nil, err
	}
	if len(cfg.Servers) == 0 {
		return nil, errors.New("lsp: embedded catalog is empty")
	}
	return cfg.Servers, nil
}

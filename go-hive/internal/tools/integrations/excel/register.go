package excel

// Registry construction and assembly hooks. The root package calls
// NewRegistry (or Register into an existing registry); tests call NewRegistry
// with WithRoots to point the sandbox at a fixture directory.

import (
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// Config carries the injectable dependencies for a standalone Excel registry.
type Config struct {
	roots []string
}

// Option mutates Config during NewRegistry.
type Option func(*Config)

// WithRoots points the filesystem sandbox at the given directories (tests).
func WithRoots(roots ...string) Option {
	return func(config *Config) { config.roots = roots }
}

// NewRegistry builds a registry containing only the Excel tool, sharing the
// Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	config := Config{}
	for _, opt := range opts {
		opt(&config)
	}
	runtime := &Runtime{
		HTTP:    integrationkit.NewHTTPClient(nil),
		Now:     time.Now,
		Roots:   config.roots,
		Modules: map[string]*integrationkit.ModuleConfig{},
		Getenv:  os.Getenv,
	}
	return Register(integrationkit.NewRegistry(runtime))
}

// Register adds the Excel catalog and its six implementations to reg. It is
// the assembly hook the root package uses when it owns the runtime.
func Register(reg *Registry) *Registry {
	for _, tool := range catalog {
		reg.AddTool(tool)
	}
	for action, fn := range map[string]ActionFunc{
		"excel_append":     excelAppend,
		"excel_info":       excelInfo,
		"excel_read":       excelRead,
		"excel_search":     excelSearch,
		"excel_sheet_list": excelSheetList,
		"excel_write":      excelWrite,
	} {
		reg.AddImpl("excel", action, fn)
	}
	return reg
}

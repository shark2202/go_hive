package integrations

// The web-intelligence family lives in its domain subpackage
// (internal/tools/integrations/webintel); this thin entry point keeps the
// historical assembly name used by the CLI.

import (
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/webintel"
)

// InstallBatch7Lane6 publishes the batch-7 lane-6 tools (similarweb, news,
// google_maps) into a shared registry and returns the tool count.
func InstallBatch7Lane6(target *tools.ToolRegistry) int {
	return webintel.NewRegistry().Install(target)
}

// newBatch7Lane6Registry builds a registry containing only the lane-6 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane6Registry(opts ...Option) *Registry {
	return webintel.NewRegistry(opts...)
}

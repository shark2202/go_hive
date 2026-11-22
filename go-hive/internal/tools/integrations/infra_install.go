package integrations

// The infrastructure automation family lives in its domain subpackage
// (internal/tools/integrations/infra); this thin entry point keeps the
// historical assembly names used by the CLI and by the lane test suite.

import (
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/infra"
)

// InstallBatch7Lane3 publishes the batch-7 lane-3 infrastructure tools
// (terraform, pagerduty, prometheus, kafka, n8n, tines) into a shared registry
// and returns the tool count.
func InstallBatch7Lane3(target *tools.ToolRegistry) int {
	return infra.NewRegistry().Install(target)
}

// newBatch7Lane3Registry builds a registry containing only the lane-3 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane3Registry(opts ...Option) *Registry {
	return infra.NewRegistry(opts...)
}

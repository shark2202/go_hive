package integrations

// The enrichment/search family lives in its domain subpackage
// (internal/tools/integrations/enrichment); this thin entry point keeps the
// historical assembly name used by the CLI and by the lane test suite.

import (
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/integrations/enrichment"
)

// Batch7Lane5Install publishes the batch-7 lane-5 tools (apify, apollo, lusha,
// serpapi, exa_search) into a shared registry and returns the tool count.
func Batch7Lane5Install(target *tools.ToolRegistry) int {
	return enrichment.NewRegistry().Install(target)
}

// batch7Lane5NewRegistry builds a registry containing only the lane-5 tools,
// sharing the Runtime surface used by the rest of the port.
func batch7Lane5NewRegistry(opts ...Option) *Registry {
	return enrichment.NewRegistry(opts...)
}

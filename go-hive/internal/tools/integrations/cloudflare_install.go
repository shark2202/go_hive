package integrations

// The Cloudflare family lives in its domain subpackage
// (internal/tools/integrations/cloudflare); this thin entry point keeps the
// historical assembly name used by the CLI and by the lane test suite.

import (
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/cloudflare"
)

// InstallBatch7Lane2 publishes the batch-7 lane-2 Cloudflare tool into a
// shared registry and returns the number of tools registered.
func InstallBatch7Lane2(target *tools.ToolRegistry) int {
	return cloudflare.NewRegistry().Install(target)
}

// newCloudflareRegistry builds a registry containing only the Cloudflare tool,
// sharing the Runtime surface used by the rest of the port.
func newCloudflareRegistry(opts ...Option) *Registry {
	return cloudflare.NewRegistry(opts...)
}

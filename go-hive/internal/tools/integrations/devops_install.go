package integrations

// The DevOps hosting family lives in its domain subpackage
// (internal/tools/integrations/devops); this thin entry point keeps the
// historical assembly names used by the CLI and by the lane test suite.

import (
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/devops"
)

// InstallBatch7Lane1 publishes the batch-7 lane-1 tools (gitlab, aws_s3,
// vercel, docker_hub) into a shared registry and returns the tool count.
func InstallBatch7Lane1(target *tools.ToolRegistry) int {
	return devops.NewRegistry().Install(target)
}

// newBatch7Lane1Registry builds a registry containing only the lane-1 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane1Registry(opts ...Option) *Registry {
	return devops.NewRegistry(opts...)
}

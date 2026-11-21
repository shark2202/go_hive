package integrations

// The scheduling/chat family lives in its domain subpackage
// (internal/tools/integrations/schedchat); this thin entry point keeps the
// historical assembly name used by the CLI and by the lane test suite.

import (
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/integrations/schedchat"
)

// InstallBatch7Lane8 publishes the batch-7 lane-8 tools (brevo, calcom,
// calendly, discord, mattermost) into a shared registry and returns the tool
// count.
func InstallBatch7Lane8(target *tools.ToolRegistry) int {
	return schedchat.NewRegistry().Install(target)
}

// newBatch7Lane8Registry builds a registry containing only the lane-8 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane8Registry(opts ...Option) *Registry {
	return schedchat.NewRegistry(opts...)
}

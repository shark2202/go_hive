package integrations

// The social-media family lives in its domain subpackage
// (internal/tools/integrations/social); this thin entry point keeps the
// historical assembly name used by the CLI.

import (
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/integrations/social"
)

// InstallBatch7Lane7 publishes the batch-7 lane-7 tools (youtube, reddit,
// twitter, twilio, pushover) into a shared registry and returns the tool count.
func InstallBatch7Lane7(target *tools.ToolRegistry) int {
	return social.NewRegistry().Install(target)
}

package integrations

// The AI-platform family lives in its domain subpackage
// (internal/tools/integrations/ai); this thin entry point keeps the assembly
// entry the CLI and the lane test suite call.

import (
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/integrations/ai"
)

// InstallAIPlatform publishes the AI-platform tools (pinecone, wandb,
// langfuse, huggingface, vision) into a shared registry and returns the
// number of tools registered.
func InstallAIPlatform(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	return ai.NewRegistry().Install(target)
}

// newAIPlatformRegistry builds a registry containing only these tools,
// sharing the Runtime surface used by the rest of the port.
func newAIPlatformRegistry(opts ...Option) *Registry {
	return ai.NewRegistry(opts...)
}

package integrations

// Excel lives in its domain subpackage (internal/tools/integrations/excel);
// this thin entry point keeps the historical assembly name used by the CLI.

import (
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/excel"
)

// InstallBatch3Excel publishes the Excel module into a shared registry and
// returns the number of tools registered (one tool, six actions).
func InstallBatch3Excel(target *tools.ToolRegistry) int {
	return excel.NewRegistry().Install(target)
}

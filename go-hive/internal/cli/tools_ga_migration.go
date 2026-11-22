package cli

// Runtime-startup GA (generally-available) tool migration.
//
// Parity: framework/agents/queen/tools_ga_migration.py::run_ga_tool_migration
// Parity: framework/server/app.py::create_app

import (
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// runGAToolMigration mirrors create_app's startup pass: grant newly-GA tools
// to existing queen/colony sidecars before any session (or preloaded colony)
// reads its allowlist. A no-op once every sidecar's saved_on_version is
// current; per-sidecar failures are warnings only.
func (r *Runner) runGAToolMigration(home hivehome.Home) {
	queens, colonies := hiveserver.RunGAToolMigration(home, func(msg string) {
		fmt.Fprintf(r.Stdout, "Warning: %s\n", msg)
	})
	if len(queens) > 0 {
		fmt.Fprintf(r.Stdout, "ga_migration: granted tools to %d queen(s): %v\n", len(queens), queens)
	}
	if len(colonies) > 0 {
		fmt.Fprintf(r.Stdout, "ga_migration: granted tools to %d colony(s): %v\n", len(colonies), colonies)
	}
}

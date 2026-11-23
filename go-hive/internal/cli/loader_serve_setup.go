package cli

// Serve-time startup helpers kept out of loader.go so the serve entry point
// stays inside the repository's frozen size budgets.

import (
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// prepareServeRuntime installs the process-wide storage, credential and
// aux-token state before the HTTP app or any session exists. Every step is
// idempotent and non-fatal, mirroring upstream's create_app startup order.
func (r *Runner) prepareServeRuntime(home hivehome.Home) {
	// Runtime logs first: early migration lines land in the same sinks
	// upstream's configure_logging covers before create_app.
	installRuntimeLogBuffer(home)
	// SessionManager.__init__ migrates v2 → v3 → colony sessions → tracker
	// backfill before any session or runtime exists.
	r.runStorageStartupMigrations(home)
	// Newly-GA tools reach existing queen/colony sidecars before their
	// allowlists are read; saved_on_version keeps it idempotent.
	r.runGAToolMigration(home)
	// Tools read os.Getenv while the dashboard writes the encrypted store.
	hiveserver.SyncCredentialEnv(home)
	// Governs recall selection, sentinel classification, judge and skill tests.
	llm.SetAuxMaxTokens(hiveserver.AuxMaxTokens(home))
}

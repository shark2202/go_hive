package cli

import (
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// runStorageStartupMigrations runs the storage prep Python's
// SessionManager.__init__ performs before the server serves any session:
// v2 → v3 → colony-session consolidation → colony tracker backfill.
// Failures are warnings only — they must never block serve startup — and the
// tracker-ensure count mirrors upstream's startup info log.
func (r *Runner) runStorageStartupMigrations(home hivehome.Home) {
	ensured := hiveserver.RunStorageStartupMigrations(home, func(msg string) {
		fmt.Fprintf(r.Stdout, "Warning: %s\n", msg)
	})
	if len(ensured) > 0 {
		fmt.Fprintf(r.Stdout, "tracker_db: ensured %d colony tracker DB(s) at startup\n", len(ensured))
	}
}

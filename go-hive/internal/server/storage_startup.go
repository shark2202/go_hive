package server

import (
	"fmt"
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/storage"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// storageMigrationSteps lists the four storage-prep steps of
// SessionManager.__init__, in the order upstream runs them. Tests swap in
// stub steps to inject failures; production always uses the real migrations.
type storageMigrationSteps struct {
	migrateV2       func(homeRoot, exportsDir string) error
	migrateV3       func(homeRoot string) error
	migrateColonies func(homeRoot string) error
	ensureTrackers  func(coloniesDir string) ([]string, error)
}

var productionStorageMigrationSteps = storageMigrationSteps{
	migrateV2:       storage.RunMigrationV2,
	migrateV3:       storage.RunMigrationV3,
	migrateColonies: storage.RunMigrationColonySessions,
	ensureTrackers:  tracker.EnsureAllColonyTrackerDBs,
}

// RunStorageStartupMigrations runs the storage prep Python's
// SessionManager.__init__ performs before the server serves any session:
//
//  1. migrate_v2 — v1 home → v2 layout, marker $HIVE_HOME/.migrated-v2
//  2. migrate_v3 — v2 home → v3 layout, marker $HIVE_HOME/.migrated-v3
//  3. migrate_colony_sessions — stray colony_fork sessions into the colony
//     tree, marker $HIVE_HOME/colonies/.migrations/colony_sessions_v1
//  4. tracker backfill — a tracker/tracker.db for every colonies/<entry>
//
// The order matters: v3 reads the paths v2 populates, colony_sessions reads
// the v3 colony layout. Every step is idempotent — its marker file (or its
// v2-exclusive-artifact predicate) short-circuits a re-run, so a second
// startup changes nothing. A failure in any step only calls warn and returns
// the tracker DBs ensured so far; it never aborts the caller, matching
// upstream's `logger.warning("… (non-fatal)")` semantics.
//
// exportsDir is $HIVE_HOME/exports. Upstream's migrate_v2 defaults to the
// CWD-relative Path("exports") because the v1 server ran from the checkout
// root that contained exports/ (see framework/server/app.py's
// _REPO_ROOT/exports compat fallback). The Go runtime anchors that lookup to
// the home root — the location the storage-compat fixtures and the
// e2e/cmd/storagecompat harness use — so startup behavior does not depend on
// the directory `hive serve` was launched from.
//
// Parity: framework/server/session_manager.py::SessionManager.__init__
func RunStorageStartupMigrations(home hivehome.Home, warn func(string)) []string {
	return runStorageStartupMigrations(home, warn, productionStorageMigrationSteps)
}

func runStorageStartupMigrations(home hivehome.Home, warn func(string), steps storageMigrationSteps) []string {
	if home.Root == "" {
		storageWarnf(warn, "storage migrations skipped: empty hive home")
		return nil
	}
	if err := steps.migrateV2(home.Root, filepath.Join(home.Root, "exports")); err != nil {
		storageWarnf(warn, "v2 migration failed (non-fatal): %v", err)
	}
	if err := steps.migrateV3(home.Root); err != nil {
		storageWarnf(warn, "v3 migration failed (non-fatal): %v", err)
	}
	if err := steps.migrateColonies(home.Root); err != nil {
		storageWarnf(warn, "colony_sessions migration failed (non-fatal): %v", err)
	}
	ensured, err := steps.ensureTrackers(home.ColoniesDir())
	if err != nil {
		storageWarnf(warn, "tracker_db: backfill at startup failed (non-fatal): %v", err)
	}
	return ensured
}

func storageWarnf(warn func(string), format string, args ...any) {
	if warn != nil {
		warn(fmt.Sprintf(format, args...))
	}
}

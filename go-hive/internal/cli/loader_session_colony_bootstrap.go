package cli

// Fresh-colony bootstrap for the standard create-session path: a create that
// names a colony which is not on disk yet materialises the minimal colony
// before the session's runtime is built, so POST /api/sessions returns the
// live colony session instead of "Colony not found". Split out of loader.go
// (frozen size budget).
//
// Parity: framework/server/session_manager.py::_ensure_minimal_colony
// Parity: framework/server/session_manager.py::SessionManager.create_session

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// ensureColonyForCreate is the fresh-colony branch of create_session:
//
//   - colony_id whose metadata.json is absent (the directory may not exist at
//     all) → bootstrap the minimal colony: metadata.json {name, created_at,
//     queen_name?} and worker.json {name: humanize_slug, description,
//     tools: [], goal, queen_name?}, each written only when missing.
//   - colony_id whose metadata.json exists — a scaffold-only colony included —
//     is left untouched: the existing-colony branch loads it as-is, which is
//     upstream's scaffolded exception (session_manager.py::create_session
//     :738-760: a scaffolded colony is this create's upgrade, not a husk to
//     deduplicate around).
//
// queenName is the request's queen_name (Python's optional kwarg). It rides
// into the bootstrap files only as upstream passes it: an absent/empty name
// leaves the queen_name key out of both files.
func ensureColonyForCreate(home hivehome.Home, colonyID string, queenName *string) error {
	metadataPath := filepath.Join(home.ColonyDir(colonyID), "metadata.json")
	if _, err := os.Stat(metadataPath); err == nil {
		return nil
	}
	name := ""
	if queenName != nil {
		name = *queenName
	}
	if err := hiveserver.EnsureMinimalColony(home, colonyID, name); err != nil {
		return fmt.Errorf("bootstrap colony %s: %w", colonyID, err)
	}
	return nil
}

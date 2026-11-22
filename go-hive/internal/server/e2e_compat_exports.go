package server

// Exported sidecar surfaces for the HIVE_HOME storage-compat harness
// (go_hive/scripts/storage_sc04_06_run.sh, invoked via
// go_hive/go-hive/e2e/cmd/storagecompat). The SC-06 double run writes the
// colony sidecars through the same production writers the HTTP routes use, so
// the cross-implementation read/write cycle exercises the real code paths
// instead of re-implementing the on-disk format. These are thin wrappers: no
// call sites change and no runtime behavior changes.
//
// Parity: framework/server/session_manager.py::_ensure_minimal_colony
// Parity: framework/host/colony_tools_config.py::load_colony_tools_config
// Parity: framework/host/colony_tools_config.py::update_colony_tools_config

import (
	"github.com/aden-hive/hive-go/internal/hivehome"
)

// EnsureMinimalColony materialises metadata.json + worker.json + the colony
// scaffold for colonyID (mirrors framework/server/session_manager.py::
// _ensure_minimal_colony; see the unexported ensureMinimalColony).
func EnsureMinimalColony(home hivehome.Home, colonyID, queenName string) error {
	return ensureMinimalColony(home, colonyID, queenName)
}

// LoadColonyToolsConfig reads the colony tools.json allowlist
// (framework/host/colony_tools_config.py::load_colony_tools_config).
func LoadColonyToolsConfig(home hivehome.Home, colonyID string) []string {
	return loadColonyToolsConfig(home, colonyID)
}

// UpdateColonyToolsConfig writes the colony tools.json sidecar
// (framework/host/colony_tools_config.py::update_colony_tools_config).
func UpdateColonyToolsConfig(home hivehome.Home, colonyID string, enabled []string) error {
	return updateColonyToolsConfig(home, colonyID, enabled)
}

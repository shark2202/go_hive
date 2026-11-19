package server

// Colony scaffold directories: a materialised colony is a self-contained unit,
// so the scaffold route creates the same skeleton a full colony carries before
// any skill, trigger or worker run exists. Every step is idempotent: an
// existing directory or triggers.json is left exactly as it is (a scaffold
// must never clobber content a real session already wrote).
//
// Parity: framework/server/session_manager.py::_ensure_minimal_colony
// Parity: framework/config.py::colony_skills_dir

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/triggers"
)

const (
	colonySkillsDirName = "skills"
	colonyRunsDirName   = "runs"
)

// ensureColonyScaffold materialises the colony's empty working directories and
// the triggers store. skills/ is where write_skill(scope='colony') lands, runs/
// is where the worker run-state records live (a restarted colony reads them to
// resume in-flight workers), and triggers.json is the (empty) trigger registry
// the trigger runtime restores from.
func ensureColonyScaffold(colonyDir string) error {
	for _, name := range []string{colonySkillsDirName, colonyRunsDirName} {
		if err := os.MkdirAll(filepath.Join(colonyDir, name), 0o755); err != nil {
			return err
		}
	}
	triggersPath := filepath.Join(colonyDir, triggers.TriggersFileName)
	if _, err := os.Stat(triggersPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicfs.WriteJSON(triggersPath, []any{}, 0o644)
}

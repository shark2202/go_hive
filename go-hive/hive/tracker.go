package hive

import "github.com/shark2202/go_hive/go-hive/internal/tracker"

// TrackerBinding locates one colony's tracker database.
//
// Parity-exempt: Go-only tracker DB surface (hive-desktop-runtime); Python's
// framework/tracker package has no SQLite binding.
type TrackerBinding = tracker.Binding

// TrackerFor resolves the tracker binding for a colony under this Hive home.
// The colony directory must already exist.
//
// Parity-exempt: Go-only tracker DB surface (hive-desktop-runtime).
func TrackerFor(home Home, colonyID string) (*TrackerBinding, error) {
	binding, err := tracker.BindingForColonyDir(home.ColonyDir(colonyID), colonyID)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, errMissingColonyDir
	}
	return binding, nil
}

// EnsureTracker creates the colony's tracker database if it does not exist and
// returns its path.
//
// Parity-exempt: Go-only tracker DB surface (hive-desktop-runtime).
func EnsureTracker(home Home, colonyID string) (string, error) {
	return tracker.EnsureTrackerDB(home.ColonyDir(colonyID))
}

// QueryTracker runs a read-only SQL query against a tracker and returns the
// runtime's result envelope (columns/rows/truncated).
//
// Parity-exempt: Go-only tracker DB surface (hive-desktop-runtime).
func QueryTracker(binding *TrackerBinding, sql string, rowCap int) map[string]any {
	return tracker.Query(binding, sql, rowCap)
}

// ValidateTrackerSQL rejects writes and unsafe statements before they reach a
// tracker database.
//
// Parity-exempt: Go-only tracker DB surface (hive-desktop-runtime).
func ValidateTrackerSQL(sql string) error {
	return tracker.ValidateSQL(sql)
}

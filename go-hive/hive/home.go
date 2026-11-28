package hive

import "github.com/shark2202/go_hive/go-hive/internal/hivehome"

// Home is one Hive home directory: the root that owns configuration.json,
// queens/, colonies/, memories/ and the rest of the on-disk state.
//
// The type is re-exported from the runtime so path helpers keep one
// implementation. It carries locations only, never credential values.
//
// Parity: framework/config.py::HIVE_HOME
type Home = hivehome.Home

// ResolveHome resolves Hive's home the way the runtime does: $HIVE_HOME when
// set, otherwise the platform default.
//
// Parity: framework/config.py::_resolve_hive_home
func ResolveHome() (Home, error) {
	return hivehome.Resolve()
}

// HomeAt resolves a Hive home rooted at an explicit path. The directory is
// not created; call Home.EnsureDirs when a caller needs the layout on disk.
//
// Parity-exempt: Go-only constructor over hivehome.ResolveAt.
func HomeAt(root string) (Home, error) {
	return hivehome.ResolveAt(root)
}

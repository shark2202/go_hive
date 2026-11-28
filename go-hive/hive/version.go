package hive

import "github.com/shark2202/go_hive/go-hive/internal/version"

// Version is the Go runtime version ("1.0.27-go.1" for this release line).
//
// Parity: core/hive/__init__.py::__version__
const Version = version.Version

// UpstreamVersion is the Python Hive release this Go runtime tracks.
//
// Parity: framework/__init__.py
const UpstreamVersion = version.Upstream

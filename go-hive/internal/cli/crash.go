package cli

// Parity: framework/cli.py::main, framework/credentials/cli.py::main
//
// Top-level crash presentation lives in internal/cli/cliguard; this alias is
// the entrypoint-facing name (cmd/hive, cmd/hive-browser, cmd/hive-cred) and
// keeps the crash shape stable for package callers.

import "github.com/shark2202/go_hive/go-hive/internal/cli/cliguard"

// GuardMain runs one CLI entry point and converts a panic into the Python
// crash presentation. Usage: os.Exit(cli.GuardMain(func() int { ... })).
var GuardMain = cliguard.Guard

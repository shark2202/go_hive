package cli

// RunBrowserCLI is the `hive-browser` entry point; the command family lives in
// internal/cli/browsercli. The wrapper keeps cmd/hive-browser's historical
// call site and the GuardMain crash-shape contract unchanged.
//
// Parity: tools/src/gcu/cli.py::main

import (
	"io"

	"github.com/shark2202/go_hive/go-hive/internal/cli/browsercli"
)

func RunBrowserCLI(argv []string, stdout, stderr io.Writer) int {
	return browsercli.Run(argv, stdout, stderr)
}

// Package browsercli owns the `hive-browser` command surface: the verb tree,
// the HTTP transport to the standalone browser bridge, the exit-code taxonomy,
// the CLI-side artifact spill layer, and the bridge control-plane client that
// `hive serve` keeps connected.
//
// Parity: tools/src/gcu/cli.py::main and tools/src/gcu/errors.py
//
// The package is a leaf: it must not import internal/cli. The composition root
// keeps only the RunBrowserCLI entry point and the supervisor seam, and the
// parser comes from internal/cli/cliargs. The bridge itself lives behind its
// own module boundary and is reached over local HTTP/WebSocket, so nothing
// here depends on the browserbridge module.
package browsercli

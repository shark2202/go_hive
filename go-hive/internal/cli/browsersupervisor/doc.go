// Package browsersupervisor owns the lifecycle of the standalone Go browser
// bridge (`browserbridge`) that `hive serve` keeps alive.
//
// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
//
// The main module deliberately does not import the browserbridge module (it
// carries its own go.mod), so the contract is the process boundary plus the
// documented ports: exec the child, wait for /health, stop on exit, and
// restart a bounded number of times. The composition root (internal/cli)
// keeps thin aliases for the exported helpers and supplies the control-plane
// keepalive through Config, so this package never imports internal/cli.
package browsersupervisor

//go:build !windows

package cli

// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
//
// Unix side of the browser-bridge process plumbing: the child gets its own
// process group so a Ctrl+C on `hive serve` (SIGINT to the foreground group)
// does not kill the bridge before the supervisor can shut it down, and signals
// are addressed to that group so a wrapper plus the real bridge go down
// together.

import (
	"errors"
	"os"
	"syscall"
)

func browserBridgeBinaryNames() []string { return []string{"browserbridge"} }

func browserBridgeExecutableName() string { return "browserbridge" }

// browserProcessAlive reports whether pid still exists. The supervisor uses it
// to refuse a respawn into a dead runtime, the same guard upstream's
// _run_supervisor parent watchdog enforces.
//
// Parity: tools/src/gcu/bridge_host.py::_run_supervisor
func browserProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func browserBridgeSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// browserBridgeDetachedSysProcAttr starts a bridge that must outlive the
// one-shot CLI that spawned it (upstream's `start_new_session=True`): the
// child gets its own session, so a Ctrl+C or a closed terminal does not take
// it down before the next command reuses it.
func browserBridgeDetachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// browserBridgeSignal signals the child's process group, falling back to the
// single process when the group is already gone.
func browserBridgeSignal(process *os.Process, sig syscall.Signal) error {
	if process == nil {
		return nil
	}
	if err := syscall.Kill(-process.Pid, sig); err == nil {
		return nil
	}
	return process.Signal(sig)
}
